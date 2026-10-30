package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 对正在运行的 sing-box 的两个操作(Clash API,官方文档有):
//   - KernelDelay:让内核经某个出站去访问一个地址,量端到端延迟(GET /proxies/<名>/delay);
//   - KernelSelect:改一个 selector 组当前选中的出站(PUT /proxies/<组>)。
// 故障转移管理器靠它们做检测和切换。控制地址和 secret 取自正式配置,和 KernelProxies 一样。
//
// 以下几点是沙箱里跑真实 sing-box 1.14.4 实测的,和直觉不一样,代码都按它来:
//   - 延迟测试只认 https 地址:给 http:// 的地址,内核不报错、悄悄改测默认地址(www.gstatic.com),量出来的是另一个地址的延迟。
//     所以 KernelDelay 对非 https 地址直接报错,不去问内核。
//   - 它只看有没有拿到响应,不看状态码(用的是 HEAD,404 / 501 也算通),所以前端设置里的「期望状态码」内核认不了。
//   - 超时回 504 {"message":"Timeout"};其它失败(连不上 / 握手失败等)回 503 {"message":"An error occurred in the delay test"},
//     后者分不出具体原因。
//   - 名字不存在回 404;名字里的斜杠、空格、中文要按路径转义。
//   - 改选择成功回 204;选的名字不在组里回 400 "Selector update error: not found";对象不是 selector 回 400 "Must be a Selector";组不存在回 404。

// KernelError 是内核控制接口回了非成功状态:Status 是 HTTP 状态码,Message 是它给的 message(没有就空)。
type KernelError struct {
	Status  int
	Message string
}

func (e *KernelError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("控制接口返回 %d", e.Status)
	}
	return fmt.Sprintf("控制接口返回 %d: %s", e.Status, e.Message)
}

// IsNotFound 报告 err 是不是「内核里没有这个名字」(404)。
func IsNotFound(err error) bool {
	var ke *KernelError
	return errors.As(err, &ke) && ke.Status == http.StatusNotFound
}

// IsTimeout 报告 err 是不是「内核说这次测速超时了」(504)。
func IsTimeout(err error) bool {
	var ke *KernelError
	return errors.As(err, &ke) && ke.Status == http.StatusGatewayTimeout
}

// KernelDelay 让内核经出站 name 访问 testURL(必须是 https),返回端到端延迟。
// 失败返回 *KernelError(超时:504,IsTimeout;其它测失败:503;没有这个出站:404,IsNotFound),或「控制接口连不上」之类的错误。timeout 是内核等多久,1 毫秒到 60 秒。
func KernelDelay(ctx context.Context, configFile, name, testURL string, timeout time.Duration) (time.Duration, error) {
	if name == "" {
		return 0, errors.New("没有指定要测的出站")
	}
	if u, err := url.Parse(testURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return 0, errors.New("延迟测试的地址必须是 https:// 开头(内核不认 http 地址,会悄悄改测别的地址)")
	}
	if timeout < time.Millisecond || timeout > time.Minute {
		return 0, errors.New("延迟测试的超时要在 1 毫秒到 60 秒之间")
	}
	q := url.Values{"url": {testURL}, "timeout": {strconv.FormatInt(timeout.Milliseconds(), 10)}}
	// 内核自己按 timeout 收尾;我们多等几秒,免得把内核的超时回答截断成「连不上」
	status, body, err := kernelRequest(ctx, configFile, http.MethodGet, name, "/delay", q, nil, timeout+5*time.Second)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, kernelErrorOf(status, body)
	}
	var doc struct {
		Delay *int64 `json:"delay"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Delay == nil || *doc.Delay < 0 {
		return 0, errors.New("控制接口的延迟结果格式不对")
	}
	return time.Duration(*doc.Delay) * time.Millisecond, nil
}

// KernelSelect 把 selector 组 group 当前选中的出站改成 choice。
// 组不存在是 404(IsNotFound);choice 不在组里、或 group 不是 selector 是 400,都以 *KernelError 返回。
func KernelSelect(ctx context.Context, configFile, group, choice string) error {
	if group == "" || choice == "" {
		return errors.New("组名和要选的出站名都不能为空")
	}
	body, _ := json.Marshal(map[string]string{"name": choice})
	status, resp, err := kernelRequest(ctx, configFile, http.MethodPut, group, "", nil, body, 8*time.Second)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return kernelErrorOf(status, resp)
	}
	return nil
}

func kernelErrorOf(status int, body []byte) error {
	var doc struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &doc)
	return &KernelError{Status: status, Message: strings.TrimSpace(doc.Message)}
}

// kernelRequest 向控制接口发一个请求,路径是 /proxies/ + 转义后的 name + suffix。返回状态码和(限长的)响应体;
// 只有连不上、配置读不到、没开控制接口这类才返回 error,HTTP 状态码不对由调用方判断。
func kernelRequest(ctx context.Context, configFile, method, name, suffix string, query url.Values, body []byte, wait time.Duration) (int, []byte, error) {
	addr, secret, err := ControllerFromFile(configFile)
	if err != nil {
		return 0, nil, err
	}
	u := url.URL{Scheme: "http", Host: addr, Path: "/proxies/" + name + suffix, RawPath: "/proxies/" + url.PathEscape(name) + suffix, RawQuery: query.Encode()}
	cctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(cctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, errors.New("控制接口连不上(内核没在运行?)")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}
