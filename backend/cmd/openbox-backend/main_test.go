package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// 整体装配冒烟测试:首次运行 → 设置密码 → 受保护接口 → 重启后仍保留密码。
func TestAppWiring(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	_ = os.MkdirAll(web, 0o755)
	_ = os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>PANEL</html>"), 0o644)
	cfg := system.Config{Paths: system.Paths{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "conf"), WebDir: web}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	start := func() (*httptest.Server, *http.Client) {
		srv, err := newApp(cfg, log)
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		jar, _ := cookiejar.New(nil)
		return ts, &http.Client{Jar: jar}
	}
	get := func(c *http.Client, url string) (int, string) {
		resp, err := c.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	ts, c := start()
	if code, body := get(c, ts.URL+"/"); code != 200 || !strings.Contains(body, "PANEL") {
		t.Fatalf("首页: %d %s", code, body)
	}
	if code, _ := get(c, ts.URL+"/api/health"); code != 200 {
		t.Fatalf("health: %d", code)
	}
	if code, body := get(c, ts.URL+"/api/anything"); code != 403 || !strings.Contains(body, "PASSWORD_SETUP_REQUIRED") {
		t.Fatalf("未设密码访问受保护接口: %d %s", code, body)
	}
	resp, err := c.Post(ts.URL+"/api/auth/setup", "application/json", strings.NewReader(`{"password":"secret-1"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("设置密码: %v %v", err, resp)
	}
	resp.Body.Close()
	if code, body := get(c, ts.URL+"/api/anything"); code != 404 || !strings.Contains(body, "接口不存在") {
		t.Fatalf("登录后未知接口应为 404: %d %s", code, body)
	}

	// 上游 DNS 接口已装配:登录后可读,读不到文件时也是 {"servers":[...]} 而不是 404
	if code, body := get(c, ts.URL+"/api/openbox/dns/wan"); code != 200 || !strings.Contains(body, `"servers":[`) {
		t.Fatalf("dns/wan: %d %s", code, body)
	}

	if code, body := get(c, ts.URL+"/api/openbox/dns/region-detect"); code != 200 || !strings.Contains(body, `"pending":false`) || !strings.Contains(body, `"region":"cn"`) {
		t.Fatalf("dns/region-detect: %d %s", code, body)
	}

	// 清 DNS 缓存已装配:POST 才有;测试机上没有可清的层时是 503 + error,而不是 404 / 405
	if code, _ := get(c, ts.URL+"/api/openbox/dns/flush-cache"); code != 405 {
		t.Fatalf("dns/flush-cache GET: %d", code)
	}

	// 重启:密码保留,旧 Cookie 仍有效
	ts2, _ := start()
	jar, _ := cookiejar.New(nil)
	u, _ := http.NewRequest("GET", ts2.URL, nil)
	jar.SetCookies(u.URL, c.Jar.Cookies(mustURL(t, ts.URL)))
	c2 := &http.Client{Jar: jar}
	var st map[string]any
	resp, _ = c2.Get(ts2.URL + "/api/auth/status")
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st["passwordSet"] != true || st["authenticated"] != true {
		t.Fatalf("重启后状态不对: %v", st)
	}
}

func mustURL(t *testing.T, s string) *neturl.URL {
	t.Helper()
	req, err := http.NewRequest("GET", s, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req.URL
}
