package contentfilter

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPrivateHost(t *testing.T) {
	private := []string{
		"localhost", "LOCALHOST.", "a.localhost", "foo.internal", "0.0.0.0", "127.0.0.1", "127.9.9.9", "0.1.2.3", "10.1.2.3",
		"172.16.0.1", "172.31.255.255", "192.168.1.1", "169.254.169.254", "::", "::1", "[::1]", "fd00::1", "fc00::1", "fe80::1", "febf::1",
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", "not:an:ip:", "10.999.1.1", "",
	}
	for _, h := range private {
		if !privateHost(h) {
			t.Errorf("%q 应当是内网", h)
		}
	}
	public := []string{
		"example.com", "172.15.0.1", "172.32.0.1", "192.167.1.1", "169.253.1.1", "100.64.0.1", "8.8.8.8", "2001:db8::1", "::ffff:8.8.8.8",
		"fec0::1", "localhost.example.com", "internal", "999.1.1.1",
	}
	for _, h := range public {
		if privateHost(h) {
			t.Errorf("%q 不该是内网", h)
		}
	}
}

func TestCheckSourceURL(t *testing.T) {
	for raw, reason := range map[string]string{
		"://x":                       "source_url_invalid",
		"https://":                   "source_url_invalid",
		"http://example.com/x.js":    "source_url_not_https",
		"ftp://example.com/x.js":     "source_url_not_https",
		"example.com/x.js":           "source_url_invalid",
		"https://127.0.0.1/x.js":     "source_url_private",
		"https://[::1]/x.js":         "source_url_private",
		"https://router.internal/x":  "source_url_private",
		"https://[fe80::1]:8443/x":   "source_url_private",
		"https://10.0.0.1:8080/x.js": "source_url_private",
	} {
		_, err := checkSourceURL(raw)
		var se *SourceError
		if !errors.As(err, &se) || se.Reason != reason {
			t.Errorf("%q: 得到 %v,想要 %s", raw, err, reason)
		}
	}
	if u, err := checkSourceURL("https://example.com:8443/a/b.js?x=1"); err != nil || u.Hostname() != "example.com" {
		t.Fatal(u, err)
	}
}

// fakeClient 把所有请求都拨到测试服务器上,证书对 example.com 有效,所以测试里用 https://example.com/...。
func fakeServer(t *testing.T, h http.HandlerFunc) *FetchOptions {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	return &FetchOptions{Client: &http.Client{Transport: tr}}
}

func reasonOf(err error) string {
	var se *SourceError
	if errors.As(err, &se) {
		return se.Reason
	}
	return "NOT-A-SOURCE-ERROR: " + fmt.Sprint(err)
}

func TestFetchSourceBasics(t *testing.T) {
	var accept string
	o := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("hello 世界"))
		case "/bad-utf8":
			_, _ = w.Write([]byte("a\xffb"))
		case "/missing":
			http.NotFound(w, r)
		case "/boom":
			w.WriteHeader(502)
		}
	})
	got, err := FetchSource(context.Background(), "https://example.com/ok", o)
	if err != nil || got != "hello 世界" || !strings.Contains(accept, "text/plain") {
		t.Fatalf("%q %v %q", got, err, accept)
	}
	if got, err = FetchSource(context.Background(), "https://example.com/bad-utf8", o); err != nil || got != "a\uFFFDb" {
		t.Fatalf("%q %v", got, err)
	}
	for path, want := range map[string]string{"/missing": "source_fetch_failed:404", "/boom": "source_fetch_failed:502"} {
		if _, err := FetchSource(context.Background(), "https://example.com"+path, o); reasonOf(err) != want {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestFetchSourceContentType(t *testing.T) {
	o := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if ct := r.URL.Query().Get("ct"); ct != "" {
			w.Header().Set("Content-Type", ct)
		} else {
			w.Header()["Content-Type"] = nil // 不让 Go 自动嗅探出一个类型
		}
		_, _ = w.Write([]byte("x"))
	})
	for _, ct := range []string{"text/plain", "Text/JavaScript; charset=utf-8", "application/octet-stream", "application/javascript",
		"application/x-javascript", "application/yaml", "application/x-yaml", "text/html", ""} {
		if _, err := FetchSource(context.Background(), "https://example.com/?ct="+url.QueryEscape(ct), o); err != nil {
			t.Errorf("%q 应当放行: %v", ct, err)
		}
	}
	for _, ct := range []string{"application/json", "image/png", "application/x-www-form-urlencoded", "application/xml; charset=utf-8"} {
		_, err := FetchSource(context.Background(), "https://example.com/?ct="+url.QueryEscape(ct), o)
		if reasonOf(err) != "source_content_type_unsupported:"+ct {
			t.Errorf("%q: %v", ct, err)
		}
	}
}

func TestFetchSourceSize(t *testing.T) {
	o := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		switch r.URL.Path {
		case "/exact":
			_, _ = w.Write([]byte(strings.Repeat("a", MaxSourceBytes)))
		case "/over": // 分块传输,没有 Content-Length,靠实际读到的字节数
			w.(http.Flusher).Flush()
			_, _ = w.Write([]byte(strings.Repeat("a", MaxSourceBytes+1)))
		case "/declared": // 声明的长度就超了,不用读
			w.Header().Set("Content-Length", fmt.Sprint(MaxSourceBytes+10))
			_, _ = w.Write([]byte(strings.Repeat("a", MaxSourceBytes+10)))
		}
	})
	if got, err := FetchSource(context.Background(), "https://example.com/exact", o); err != nil || len(got) != MaxSourceBytes {
		t.Fatalf("正好上限应当放行: %d %v", len(got), err)
	}
	for _, p := range []string{"/over", "/declared"} {
		if _, err := FetchSource(context.Background(), "https://example.com"+p, o); reasonOf(err) != "source_too_large" {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestFetchSourceRedirects(t *testing.T) {
	o := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("done"))
		case "/r1":
			http.Redirect(w, r, "/r2", 301)
		case "/r2":
			http.Redirect(w, r, "r3", 302) // 相对地址
		case "/r3":
			http.Redirect(w, r, "https://example.com/ok", 307)
		case "/c1":
			http.Redirect(w, r, "/c2", 302)
		case "/c2":
			http.Redirect(w, r, "/c3", 302)
		case "/c3":
			http.Redirect(w, r, "/c4", 302)
		case "/c4":
			http.Redirect(w, r, "/ok", 302)
		case "/to-http":
			w.Header().Set("Location", "http://example.com/ok")
			w.WriteHeader(302)
		case "/to-private":
			w.Header().Set("Location", "https://127.0.0.1/ok")
			w.WriteHeader(302)
		case "/to-localhost":
			w.Header().Set("Location", "https://localhost/ok")
			w.WriteHeader(308)
		case "/no-location":
			w.WriteHeader(302)
		case "/bad-location":
			w.Header().Set("Location", "http://[::1")
			w.WriteHeader(302)
		}
	})
	ctx := context.Background()
	// 三次重定向照样拿到正文;第四次重定向就报错
	if got, err := FetchSource(ctx, "https://example.com/r1", o); err != nil || got != "done" {
		t.Fatalf("3 次重定向: %q %v", got, err)
	}
	for path, want := range map[string]string{
		"/c1": "source_too_many_redirects", "/to-http": "source_redirect_not_https", "/to-private": "source_url_private",
		"/to-localhost": "source_url_private", "/no-location": "source_redirect_without_location", "/bad-location": "source_redirect_invalid",
	} {
		if _, err := FetchSource(ctx, "https://example.com"+path, o); reasonOf(err) != want {
			t.Errorf("%s: 得到 %v,想要 %s", path, err, want)
		}
	}
}

func TestFetchSourceTimeoutAndCancel(t *testing.T) {
	release := make(chan struct{})
	o := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	o.Timeout = 80 * time.Millisecond
	start := time.Now()
	if _, err := FetchSource(context.Background(), "https://example.com/slow", o); reasonOf(err) != "source_fetch_timeout" {
		t.Fatalf("超时: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("超时没有及时返回")
	}
	// 调用方自己取消:原样返回 ctx 的错误,不冒充成超时
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	o.Timeout = 5 * time.Second
	if _, err := FetchSource(ctx, "https://example.com/slow", o); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消: %v", err)
	}
	// 连不上:source_fetch_failed,带原因
	o2 := &FetchOptions{Client: &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("refused")
	}}}}
	_, err := FetchSource(context.Background(), "https://example.com/x", o2)
	var se *SourceError
	if !errors.As(err, &se) || se.Reason != "source_fetch_failed" || se.Cause == nil {
		t.Fatalf("连不上: %v", err)
	}
}

// 默认客户端在拨号时校验实际 IP:域名解析到内网地址(DNS 重绑定)也拦下。
func TestGuardedTransportBlocksPrivateDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	tr := guardedTransport()
	conn, err := tr.DialContext(context.Background(), "tcp", ln.Addr().String())
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, errPrivateAddr) {
		t.Fatalf("连回环地址应当被拦: %v", err)
	}
	for _, a := range []string{"10.0.0.1:443", "[::1]:443", "[fd00::1]:443", "192.168.0.1:443", "[::ffff:127.0.0.1]:443"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if c, err := tr.DialContext(ctx, "tcp", a); !errors.Is(err, errPrivateAddr) {
			if err != nil && strings.Contains(err.Error(), "address family not supported") {
				cancel()
				continue // 沙箱没有 IPv6,拨号走不到校验这一步;IPv6 的判断在 TestPrivateHost 里测
			}
			t.Errorf("%s: %v", a, err)
		} else if c != nil {
			c.Close()
		}
		cancel()
	}
	if tr.Proxy != nil {
		t.Fatal("不该走环境代理")
	}
}
