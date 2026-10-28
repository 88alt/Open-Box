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
		srv, _, err := newApp(cfg, log)
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

	// 每日流量已装配:没有数据时月视图是空列表、占用是 0 天;后台采集器随 background 启动,没有内核时只是采样失败,不影响接口
	if code, body := get(c, ts.URL+"/api/openbox/traffic/month"); code != 200 || !strings.Contains(body, `"days":[]`) || !strings.Contains(body, `"avgDays":0`) {
		t.Fatalf("traffic/month: %d %s", code, body)
	}
	if code, body := get(c, ts.URL+"/api/openbox/traffic/usage"); code != 200 || !strings.Contains(body, `"days":0`) {
		t.Fatalf("traffic/usage: %d %s", code, body)
	}
	if code, _ := get(c, ts.URL+"/api/openbox/traffic/day"); code != 400 {
		t.Fatalf("traffic/day 缺 day: %d", code)
	}

	// 客户端 App 信息已装配:路由器 ID 第一次读时生成,之后(含重启后)不变;POST 不被接受
	var first, second struct {
		RouterID     string `json:"routerId"`
		RouterName   string `json:"routerName"`
		ShareRegions []any  `json:"shareRegions"`
	}
	code, body := get(c, ts.URL+"/api/openbox/client-app/info")
	if code != 200 || json.Unmarshal([]byte(body), &first) != nil || len(first.RouterID) != 32 || first.RouterName != "Open-Box" || len(first.ShareRegions) != 3 {
		t.Fatalf("client-app/info: %d %s", code, body)
	}
	if resp, err := c.Post(ts.URL+"/api/openbox/client-app/info", "application/json", strings.NewReader("{}")); err != nil || resp.StatusCode != 405 {
		t.Fatalf("client-app/info POST: %v %v", err, resp)
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
	resp, err = c2.Get(ts2.URL + "/api/openbox/client-app/info")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&second)
	resp.Body.Close()
	if second.RouterID != first.RouterID {
		t.Fatalf("重启后路由器 ID 变了: %s → %s", first.RouterID, second.RouterID)
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
