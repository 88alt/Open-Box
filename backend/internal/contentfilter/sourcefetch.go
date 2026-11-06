package contentfilter

// 抓规则来源链接和规则里的脚本(`script-path`)用的取文本函数。
//
// 口径对齐上游 v0.1.411(维护者授权只读参考 catch-up-upstream 面板服务端;代码全部自写,没有粘贴源码;
// 上游抓规则来源和抓脚本是同一个函数,这里也是):
//   - 只收 https;主机名是本机 / 内网地址就拒绝(`localhost`、`0.0.0.0`、`::`、`::1`、`.localhost` / `.internal` 结尾、
//     IPv4 的 0/8 127/8 10/8 172.16/12 192.168/16 169.254/16、IPv6 的回环 / 唯一本地 fc00::/7 / 链路本地 fe80::/10、
//     IPv4 映射地址按它里面的 IPv4 算);
//   - 重定向自己跟,最多 3 次(第 4 个重定向响应报错),每一跳都重新校验 https 和内网;
//   - 10 秒超时;响应体最多 512 KiB(先看 Content-Length,再按实际读到的字节数,超了报错不截断);
//   - Content-Type 没有或为空放行,否则主类型要是 text/* 或 application/octet-stream、javascript、x-javascript、yaml、x-yaml。
//
// 有意比上游多做的一件事:上游只看 URL 里写的主机名,解析成内网地址的域名(DNS 重绑定)拦不住;
// 这里连接时再校验一次实际连的 IP,是内网地址同样报 `source_url_private`。另外不走环境里的代理,
// 免得校验的是代理地址而不是目标。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// MaxSourceBytes 是一次抓取最多读多少字节(规则来源和脚本都是 512 KiB)。
	MaxSourceBytes = 512 * 1024
	// SourceTimeout 是一次抓取的总超时(含重定向)。
	SourceTimeout = 10 * time.Second
	// maxRedirects 是最多跟几次重定向。
	maxRedirects = 3
)

// SourceError 是抓取失败。Reason 是机器可读的原因码:
// source_url_invalid / source_url_not_https / source_url_private / source_redirect_without_location /
// source_redirect_invalid / source_redirect_not_https / source_too_many_redirects /
// source_fetch_failed[:状态码] / source_content_type_unsupported:<Content-Type> / source_too_large / source_fetch_timeout。
type SourceError struct {
	Reason string
	URL    string
	Cause  error
}

func (e *SourceError) Error() string {
	return fmt.Sprintf("source fetch failed: %s (%s)", e.Reason, e.URL)
}
func (e *SourceError) Unwrap() error { return e.Cause }

func srcErr(reason, u string) *SourceError { return &SourceError{Reason: reason, URL: u} }

// FetchOptions 是可选项,全部可以不填。
type FetchOptions struct {
	// Client 换掉默认的(带连接时校验的)客户端,测试用;重定向无论它怎么设置都由 FetchSource 自己跟。
	Client *http.Client
	// Timeout 是总超时,0 = SourceTimeout。
	Timeout time.Duration
}

var errPrivateAddr = errors.New("address is private")

// privateV4 是上游认的内网 IPv4 段(只看前两个字节)。
func privateV4(a, b int) bool {
	return a == 0 || a == 127 || a == 10 || a == 172 && b >= 16 && b <= 31 || a == 192 && b == 168 || a == 169 && b == 254
}

// privateAddr 判断一个真实的 IP 是不是本机 / 内网地址。
func privateAddr(ip netip.Addr) bool {
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	if ip.Is4() {
		v := ip.As4()
		return privateV4(int(v[0]), int(v[1]))
	}
	if ip.IsUnspecified() || ip.IsLoopback() {
		return true
	}
	v := ip.As16()
	return v[0]&0xfe == 0xfc || v[0] == 0xfe && v[1]&0xc0 == 0x80
}

var dottedQuad = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)

// privateHost 判断 URL 里写的主机名是不是本机 / 内网。写的是域名时只看名字,真实 IP 在连接时再校验。
func privateHost(host string) bool {
	h := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(host), "["), "]"), ".")
	switch h {
	case "", "localhost", "0.0.0.0", "::", "::1":
		return true
	}
	if strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".internal") {
		return true
	}
	if m := dottedQuad.FindStringSubmatch(h); m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		return privateV4(a, b)
	}
	if strings.Contains(h, ":") {
		ip, err := netip.ParseAddr(h)
		return err != nil || privateAddr(ip)
	}
	return false
}

// checkSourceURL 校验一个要抓的链接,返回解析后的 URL。
func checkSourceURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, srcErr("source_url_invalid", raw)
	}
	if u.Scheme != "https" {
		return nil, srcErr("source_url_not_https", raw)
	}
	if privateHost(u.Hostname()) {
		return nil, srcErr("source_url_private", raw)
	}
	return u, nil
}

// guardedTransport 的连接在拨号时校验实际 IP:是内网地址就拒绝(防 DNS 重绑定)。
func guardedTransport() *http.Transport {
	d := &net.Dialer{
		Timeout: SourceTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip, err := netip.ParseAddr(host); err == nil && privateAddr(ip) {
				return errPrivateAddr
			}
			return nil
		},
	}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   SourceTimeout,
		ResponseHeaderTimeout: SourceTimeout,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
	}
}

func contentTypeOK(h string) bool {
	if strings.TrimSpace(h) == "" {
		return true
	}
	main, _, _ := strings.Cut(h, ";")
	main = strings.ToLower(strings.TrimSpace(main))
	if strings.HasPrefix(main, "text/") {
		return true
	}
	switch main {
	case "application/octet-stream", "application/javascript", "application/x-javascript", "application/yaml", "application/x-yaml":
		return true
	}
	return false
}

func isRedirect(code int) bool {
	switch code {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

// FetchSource 抓一个 https 链接的文本。非法 UTF-8 字节换成 U+FFFD(脚本的 sha256 按换完之后的正文算)。
// 失败一律是 *SourceError(调用方自己取消 ctx 时返回 ctx 的错误)。
func FetchSource(ctx context.Context, raw string, o *FetchOptions) (string, error) {
	var opt FetchOptions
	if o != nil {
		opt = *o
	}
	u, err := checkSourceURL(raw)
	if err != nil {
		return "", err
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = SourceTimeout
	}
	client := opt.Client
	if client == nil {
		tr := guardedTransport()
		defer tr.CloseIdleConnections()
		client = &http.Client{Transport: tr}
	}
	// 直接用 Transport 发请求:重定向、畸形的 Location 都由这里自己处理,每一跳都要校验
	rt := client.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fail := func(err error) error {
		var se *SourceError
		switch {
		case errors.As(err, &se):
			return se
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(tctx.Err(), context.DeadlineExceeded):
			return srcErr("source_fetch_timeout", raw)
		case errors.Is(err, errPrivateAddr):
			e := srcErr("source_url_private", raw)
			e.Cause = err
			return e
		}
		e := srcErr("source_fetch_failed", raw)
		e.Cause = err
		return e
	}

	for i := 0; i <= maxRedirects; i++ {
		req, err := http.NewRequestWithContext(tctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", fail(err)
		}
		req.Header.Set("Accept", "text/plain, text/*;q=0.9, application/octet-stream;q=0.8")
		resp, err := rt.RoundTrip(req)
		if err != nil {
			return "", fail(err)
		}
		if isRedirect(resp.StatusCode) {
			loc := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if loc == "" {
				return "", srcErr("source_redirect_without_location", raw)
			}
			next, err := u.Parse(loc)
			if err != nil {
				return "", srcErr("source_redirect_invalid", loc)
			}
			if next.Scheme != "https" {
				return "", srcErr("source_redirect_not_https", next.String())
			}
			if privateHost(next.Hostname()) {
				return "", srcErr("source_url_private", next.String())
			}
			u = next
			continue
		}
		body, err := readSource(resp, raw)
		_ = resp.Body.Close()
		if err != nil {
			return "", fail(err)
		}
		return strings.ToValidUTF8(string(body), "\uFFFD"), nil
	}
	return "", srcErr("source_too_many_redirects", raw)
}

func readSource(resp *http.Response, raw string) ([]byte, error) {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, srcErr("source_fetch_failed:"+strconv.Itoa(resp.StatusCode), raw)
	}
	ct := resp.Header.Get("Content-Type")
	if !contentTypeOK(ct) {
		return nil, srcErr("source_content_type_unsupported:"+ct, raw)
	}
	if resp.ContentLength > MaxSourceBytes {
		return nil, srcErr("source_too_large", raw)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSourceBytes {
		return nil, srcErr("source_too_large", raw)
	}
	return b, nil
}
