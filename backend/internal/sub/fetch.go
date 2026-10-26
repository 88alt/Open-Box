package sub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxBodyBytes 是订阅内容的大小上限(路由器内存小;正常订阅远小于这个值)。
	MaxBodyBytes = 16 << 20
	// DefaultUserAgent:多数机场按 UA 决定返回格式,v2rayN 会拿到分享链接 / base64。
	DefaultUserAgent = "v2rayN/7.0"
	// DefaultFetchTimeout 是单次拉取的总超时。
	DefaultFetchTimeout = 30 * time.Second
)

// FetchError 表示「订阅服务器那一头」的问题(连不上、超时、HTTP 非 2xx、内容过大),
// 与调用方自己传错地址区分开,接口层据此返回 502 而不是 400。
type FetchError struct{ Msg string }

func (e *FetchError) Error() string { return e.Msg }

func fetchErr(format string, a ...any) error { return &FetchError{Msg: fmt.Sprintf(format, a...)} }

// Usage 是响应头 subscription-userinfo 里的用量;缺的字段为 nil(前端当 null)。
type Usage struct {
	Upload   *int64 `json:"upload"`
	Download *int64 `json:"download"`
	Total    *int64 `json:"total"`
	Expire   *int64 `json:"expire"`
}

// Fetched 是一次拉取的结果。
type Fetched struct {
	Body  []byte
	Usage *Usage // 响应头没带用量时为 nil
}

// Fetch 拉取订阅地址。只接受 http / https;内容超过 MaxBodyBytes 报错而不是截断。
// 错误信息里不带完整 URL(里面常有 token)。
func Fetch(ctx context.Context, client *http.Client, rawURL, userAgent string) (*Fetched, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("订阅地址必须是 http:// 或 https:// 开头的有效地址")
	}
	if client == nil {
		client = &http.Client{Timeout: DefaultFetchTimeout}
	}
	if userAgent = strings.TrimSpace(userAgent); userAgent == "" {
		userAgent = DefaultUserAgent
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("订阅地址无效")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fetchErr("拉取订阅已取消或超时(%s)", u.Hostname())
		}
		return nil, fetchErr("无法连接订阅服务器 %s", u.Hostname())
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fetchErr("订阅服务器返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, fetchErr("读取订阅内容失败(%s)", u.Hostname())
	}
	if len(body) > MaxBodyBytes {
		return nil, fetchErr("订阅内容超过 %d MiB", MaxBodyBytes>>20)
	}
	return &Fetched{Body: body, Usage: ParseUserinfo(resp.Header.Get("Subscription-Userinfo"))}, nil
}

// ParseUserinfo 解析 "upload=1; download=2; total=3; expire=1700000000"。
// 一个有效字段都没有返回 nil;expire 为 0 或空表示不限期,记为 nil。
func ParseUserinfo(h string) *Usage {
	if strings.TrimSpace(h) == "" {
		return nil
	}
	u := &Usage{}
	found := false
	for _, part := range strings.Split(h, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64) // 个别机场写成小数 / 科学计数
		if err != nil || n < 0 {
			continue
		}
		v := int64(n)
		switch strings.ToLower(strings.TrimSpace(kv[0])) {
		case "upload":
			u.Upload = &v
		case "download":
			u.Download = &v
		case "total":
			u.Total = &v
		case "expire":
			if v == 0 {
				continue
			}
			u.Expire = &v
		default:
			continue
		}
		found = true
	}
	if !found {
		return nil
	}
	return u
}
