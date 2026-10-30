package singbox

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKernelDelayRequestAndResult(t *testing.T) {
	var gotAuth, gotMethod, gotPath, gotRaw string
	var gotQ map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod, gotPath, gotRaw = r.Header.Get("Authorization"), r.Method, r.URL.Path, r.URL.EscapedPath()
		gotQ = map[string]string{"url": r.URL.Query().Get("url"), "timeout": r.URL.Query().Get("timeout")}
		_, _ = w.Write([]byte(`{"delay":123}`))
	}))
	defer srv.Close()
	d, err := KernelDelay(context.Background(), kernelConfig(t, srv, "s3"), "节点/b c", "https://www.gstatic.com/generate_204", 2500*time.Millisecond)
	if err != nil || d != 123*time.Millisecond {
		t.Fatalf("d=%v err=%v", d, err)
	}
	if gotAuth != "Bearer s3" || gotMethod != "GET" || gotPath != "/proxies/节点/b c/delay" {
		t.Errorf("请求不对: %q %q %q", gotAuth, gotMethod, gotPath)
	}
	if !strings.Contains(gotRaw, "%2F") || strings.Contains(gotRaw, " ") { // 名字里的斜杠和空格要转义,不然路径会错位
		t.Errorf("路径没有转义: %q", gotRaw)
	}
	if gotQ["url"] != "https://www.gstatic.com/generate_204" || gotQ["timeout"] != "2500" {
		t.Errorf("查询参数不对: %v", gotQ)
	}
}

func TestKernelDelayErrors(t *testing.T) {
	status, body := 200, `{"delay":5}`
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	cfg := kernelConfig(t, srv, "")
	ctx := context.Background()
	const u = "https://x.test/"

	for _, c := range []struct {
		name, url string
		to        time.Duration
	}{
		{"", u, time.Second},
		{"a", "http://x.test/", time.Second},
		{"a", "x.test", time.Second},
		{"a", "https://", time.Second},
		{"a", u, 0},
		{"a", u, 2 * time.Minute},
	} {
		if _, err := KernelDelay(ctx, cfg, c.name, c.url, c.to); err == nil {
			t.Errorf("%+v 应该在发请求之前就被拒绝", c)
		}
	}
	if hits != 0 {
		t.Fatalf("被拒绝的参数不该发请求,发了 %d 次", hits)
	}

	status, body = 503, `{"message":"An error occurred in the delay test"}`
	_, err := KernelDelay(ctx, cfg, "a", u, time.Second)
	var ke *KernelError
	if !asKernelError(err, &ke) || ke.Status != 503 || !strings.Contains(ke.Message, "delay test") || IsNotFound(err) {
		t.Errorf("503: %v", err)
	}
	status, body = 504, `{"message":"Timeout"}`
	if _, err := KernelDelay(ctx, cfg, "a", u, time.Second); !IsTimeout(err) || IsNotFound(err) {
		t.Errorf("504 应认作超时: %v", err)
	}
	status, body = 404, `{"message":"Resource not found"}`
	if _, err := KernelDelay(ctx, cfg, "a", u, time.Second); !IsNotFound(err) || IsTimeout(err) {
		t.Errorf("404 应认作没有这个出站: %v", err)
	}
	status, body = 200, `{"nodelay":1}`
	if _, err := KernelDelay(ctx, cfg, "a", u, time.Second); err == nil {
		t.Error("格式不对应报错")
	}
	status, body = 200, `{"delay":-1}`
	if _, err := KernelDelay(ctx, cfg, "a", u, time.Second); err == nil {
		t.Error("负的延迟应报错")
	}
	srv.Close()
	if _, err := KernelDelay(ctx, cfg, "a", u, time.Second); err == nil || !strings.Contains(err.Error(), "连不上") {
		t.Errorf("内核没在运行: %v", err)
	}
	if _, err := KernelDelay(ctx, filepath.Join(t.TempDir(), "none.json"), "a", u, time.Second); err == nil {
		t.Error("配置读不到应报错")
	}
}

func asKernelError(err error, target **KernelError) bool {
	ke, ok := err.(*KernelError)
	if ok {
		*target = ke
	}
	return ok
}

func TestKernelDelayCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := KernelDelay(ctx, kernelConfig(t, srv, ""), "a", "https://x.test/", 30*time.Second)
	if err == nil || time.Since(start) > 3*time.Second || ctx.Err() == nil {
		t.Fatalf("取消应该马上返回 ctx 的错误: %v (%v)", err, time.Since(start))
	}
	if strings.Contains(err.Error(), "连不上") {
		t.Errorf("被取消不该说成「连不上」: %v", err)
	}
}

func TestKernelSelectRequestAndErrors(t *testing.T) {
	var gotMethod, gotRaw, gotAuth string
	var gotBody map[string]string
	status, resp := 204, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotRaw, gotAuth = r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = nil
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()
	cfg := kernelConfig(t, srv, "k")
	ctx := context.Background()
	if err := KernelSelect(ctx, cfg, "故障/转移 组", "hk 01"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" || gotAuth != "Bearer k" || gotBody["name"] != "hk 01" || !strings.HasPrefix(gotRaw, "/proxies/") || strings.Contains(gotRaw, " ") || strings.Count(gotRaw, "/") != 2 {
		t.Errorf("请求不对: %s %q auth=%q body=%v", gotMethod, gotRaw, gotAuth, gotBody)
	}
	status, resp = 400, `{"message":"Selector update error: not found"}`
	var ke *KernelError
	if err := KernelSelect(ctx, cfg, "g", "zzz"); !asKernelError(err, &ke) || ke.Status != 400 || !strings.Contains(ke.Message, "not found") {
		t.Errorf("400: %v", err)
	}
	status, resp = 404, `{"message":"Resource not found"}`
	if err := KernelSelect(ctx, cfg, "g", "a"); !IsNotFound(err) {
		t.Errorf("404: %v", err)
	}
	if KernelSelect(ctx, cfg, "", "a") == nil || KernelSelect(ctx, cfg, "g", "") == nil {
		t.Error("空名字应报错")
	}
}

// 用真实的 sing-box 验证上面各条「实测」的结论(没有可执行文件时跳过):
// https 地址量得出延迟、拒绝连接回 503、超时回 504、不存在的出站回 404、改选择生效、选不存在的出站 / 对叶子出站改选择回 400。
func TestRealSingBoxKernelCtl(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box")
	}
	// 测试用的 https 服务:内核自己校验证书,把它的证书放进内核进程的信任文件(SSL_CERT_FILE)
	slow := make(chan struct{})
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-slow:
			case <-r.Context().Done():
			}
		}
		w.WriteHeader(204)
	}))
	defer ts.Close()
	defer close(slow)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	free := func() int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	api := free()
	cfg := map[string]any{
		"log":          map[string]any{"level": "warn"},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": fmt.Sprintf("127.0.0.1:%d", api), "secret": "sec"}},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "a"},
			map[string]any{"type": "direct", "tag": "节点/b c"},
			map[string]any{"type": "selector", "tag": "组", "outbounds": []any{"a", "节点/b c"}, "default": "a"},
		},
		"route": map[string]any{"final": "a"},
	}
	cfgFile := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "-c", cfgFile)
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+certFile)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	for i := 0; i < 200; i++ { // 等内核起来
		if _, err := KernelProxies(ctx, cfgFile); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	d, err := KernelDelay(ctx, cfgFile, "a", ts.URL+"/ok", 3*time.Second)
	if err != nil || d < 0 {
		t.Fatalf("https 地址应量得出延迟: d=%v err=%v\n%s", d, err, out.String())
	}
	if _, err := KernelDelay(ctx, cfgFile, "节点/b c", ts.URL+"/ok", 3*time.Second); err != nil {
		t.Fatalf("名字里有斜杠和空格的出站也要能测: %v", err)
	}
	var ke *KernelError
	if _, err := KernelDelay(ctx, cfgFile, "a", "https://127.0.0.1:1/x", 2*time.Second); !asKernelError(err, &ke) || ke.Status != 503 {
		t.Errorf("拒绝连接应回 503: %v", err)
	}
	start := time.Now()
	if _, err := KernelDelay(ctx, cfgFile, "a", ts.URL+"/slow", 600*time.Millisecond); !IsTimeout(err) || time.Since(start) > 3*time.Second {
		t.Errorf("超时应在 timeout 附近回 504: %v (%v)", err, time.Since(start))
	}
	if _, err := KernelDelay(ctx, cfgFile, "不存在", ts.URL+"/ok", time.Second); !IsNotFound(err) {
		t.Errorf("不存在的出站应回 404: %v", err)
	}

	if err := KernelSelect(ctx, cfgFile, "组", "节点/b c"); err != nil {
		t.Fatalf("改选择: %v", err)
	}
	ps, err := KernelProxies(ctx, cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	now := ""
	for _, p := range ps {
		if p.Name == "组" {
			now = p.Now
		}
	}
	if now != "节点/b c" {
		t.Fatalf("选择没生效,当前是 %q", now)
	}
	if err := KernelSelect(ctx, cfgFile, "组", "不存在"); !asKernelError(err, &ke) || ke.Status != 400 {
		t.Errorf("选不在组里的出站应回 400: %v", err)
	}
	if err := KernelSelect(ctx, cfgFile, "a", "a"); !asKernelError(err, &ke) || ke.Status != 400 {
		t.Errorf("对不是 selector 的出站改选择应回 400: %v", err)
	}
	if err := KernelSelect(ctx, cfgFile, "没有的组", "a"); !IsNotFound(err) {
		t.Errorf("组不存在应回 404: %v", err)
	}
}
