package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// 恢复出厂设置的整机装配:先留下各处的数据 → 重置 → 数据、密码、会话都没了,
// 证书这类「设备身份」留着;之后能像第一次那样重新设置密码,重启后也是未设置。
func TestAppFactoryReset(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	_ = os.MkdirAll(web, 0o755)
	_ = os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>PANEL</html>"), 0o644)
	cfg := system.Config{
		Paths: system.Paths{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "conf"), WebDir: web},
		// 用一个肯定不存在的服务名:在装了 systemd 的开发机上也只会读到「没在运行」,不会真去停任何服务
		CoreService: "openbox-test-no-such-service", PanelService: "openbox-test-no-such-panel",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := cfg.Paths.Ensure(); err != nil {
		t.Fatal(err)
	}

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
	call := func(c *http.Client, method, url, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	write := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	ts, c := start()
	// 没设密码时也受保护
	if code, body := call(c, "POST", ts.URL+"/api/openbox/factory-reset", ""); code != 403 || !strings.Contains(body, "PASSWORD_SETUP_REQUIRED") {
		t.Fatalf("未设密码不该能重置: %d %s", code, body)
	}
	if code, body := call(c, "POST", ts.URL+"/api/auth/setup", `{"password":"secret-1"}`); code != 200 {
		t.Fatalf("设置密码: %d %s", code, body)
	}
	if code, _ := call(c, "GET", ts.URL+"/api/openbox/factory-reset", ""); code != 405 {
		t.Fatalf("GET 应是 405: %d", code)
	}

	// 留下各处的数据:面板设置、背景图(经真实接口),以及磁盘上的各种派生文件
	if code, body := call(c, "PUT", ts.URL+"/api/storage", `{"entries":{"config/theme":"dark"}}`); code != 200 {
		t.Fatalf("存设置: %d %s", code, body)
	}
	if code, body := call(c, "PUT", ts.URL+"/api/background-image", `{"image":"data:image/png;base64,AAAA"}`); code != 200 {
		t.Fatalf("存背景图: %d %s", code, body)
	}
	data, conf := cfg.DataDir, cfg.ConfigDir
	gone := []string{
		filepath.Join(conf, "config.json"), filepath.Join(conf, "config.prev.json"), filepath.Join(conf, "failover.json"),
		filepath.Join(data, "cache.db"), filepath.Join(data, "rules", "list-abc.srs"), filepath.Join(data, "dns-filter", "x", "y.txt"),
		filepath.Join(data, "traffic", "2026-10-01.json"),
	}
	kept := []string{filepath.Join(conf, "certs", "server.crt"), filepath.Join(conf, "certs", "server.key")}
	for _, p := range append(append([]string{}, gone...), kept...) {
		write(p)
	}
	for _, name := range []string{"profile", "subscriptions", "sub-nodes", "groups", "deploy-state", "applied-config", "runtime"} {
		write(filepath.Join(cfg.StateDir(), name+".json"))
	}
	write(filepath.Join(cfg.StateDir(), "dns-takeover.json")) // DNS 接管的记录是撤销用的,不删

	code, body := call(c, "POST", ts.URL+"/api/openbox/factory-reset", "")
	if code != 200 {
		t.Fatalf("恢复出厂设置: %d %s", code, body)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if res["ok"] != true || res["profileSeeded"] != true || res["seeded"] != float64(2) {
		t.Errorf("响应不对: %s", body)
	}
	if _, ok := res["kernelStopped"].(bool); !ok {
		t.Errorf("kernelStopped 应是布尔值: %s", body)
	}

	for _, p := range gone {
		if exists(p) {
			t.Errorf("应已删除: %s", p)
		}
	}
	for _, p := range kept {
		if !exists(p) {
			t.Errorf("设备身份(证书)不该删: %s", p)
		}
	}
	if !exists(filepath.Join(cfg.StateDir(), "dns-takeover.json")) {
		t.Error("dns-takeover.json 不该删")
	}
	left, _ := filepath.Glob(filepath.Join(cfg.StateDir(), "*.json"))
	if len(left) != 1 || filepath.Base(left[0]) != "dns-takeover.json" {
		t.Errorf("状态目录里只该剩 dns-takeover.json: %v", left)
	}
	for _, d := range []string{filepath.Join(data, "rules"), filepath.Join(data, "dns-filter")} {
		if !exists(d) {
			t.Errorf("目录本身要留着(别的代码还要往里写): %s", d)
		}
	}

	// 登录会话和密码都没了:受保护接口回「需要先设置密码」,状态是未设置
	if code, body := call(c, "GET", ts.URL+"/api/storage", ""); code != 403 || !strings.Contains(body, "PASSWORD_SETUP_REQUIRED") {
		t.Fatalf("重置后旧会话应失效: %d %s", code, body)
	}
	var st map[string]any
	_, body = call(c, "GET", ts.URL+"/api/auth/status", "")
	_ = json.Unmarshal([]byte(body), &st)
	if st["passwordSet"] != false {
		t.Fatalf("重置后应是未设置: %v", st)
	}

	// 像第一次那样设置新密码:设置、背景图都是空的
	if code, body := call(c, "POST", ts.URL+"/api/auth/setup", `{"password":"another-2"}`); code != 200 {
		t.Fatalf("重新设置密码: %d %s", code, body)
	}
	if code, body := call(c, "GET", ts.URL+"/api/storage", ""); code != 200 || strings.Contains(body, "config/theme") {
		t.Fatalf("设置应已清空: %d %s", code, body)
	}
	if code, body := call(c, "GET", ts.URL+"/api/background-image", ""); code != 200 || strings.Contains(body, "data:image") {
		t.Fatalf("背景图应已清空: %d %s", code, body)
	}

	// 重新设置之后再重置、再重启:重启后是未设置
	if code, body := call(c, "POST", ts.URL+"/api/openbox/factory-reset", ""); code != 200 {
		t.Fatalf("第二次重置: %d %s", code, body)
	}
	ts2, c2 := start()
	_, body = call(c2, "GET", ts2.URL+"/api/auth/status", "")
	st = nil
	_ = json.Unmarshal([]byte(body), &st)
	if st["passwordSet"] != false {
		t.Fatalf("重启后应仍是未设置: %v", st)
	}
}
