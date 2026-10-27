package singbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFlushCfg(t *testing.T, controller, secret string) string {
	t.Helper()
	api := map[string]any{}
	if controller != "" {
		api["external_controller"] = controller
	}
	if secret != "" {
		api["secret"] = secret
	}
	b, _ := json.Marshal(map[string]any{"experimental": map[string]any{"clash_api": api}})
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKernelFlushDNS(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	status := http.StatusNoContent
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(status)
	}))
	defer s.Close()
	addr := strings.TrimPrefix(s.URL, "http://")

	if err := KernelFlushDNS(context.Background(), writeFlushCfg(t, addr, "s3cret")); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/cache/dns/flush" || gotAuth != "Bearer s3cret" {
		t.Fatalf("%s %s %q", gotMethod, gotPath, gotAuth)
	}
	// 没有 secret 就不带鉴权头
	if err := KernelFlushDNS(context.Background(), writeFlushCfg(t, addr, "")); err != nil || gotAuth != "" {
		t.Fatalf("%v %q", err, gotAuth)
	}
	// 200 也算成功
	status = http.StatusOK
	if err := KernelFlushDNS(context.Background(), writeFlushCfg(t, addr, "")); err != nil {
		t.Fatal(err)
	}
	// 鉴权失败 / 其它状态码:error,且错误里不带 secret
	status = http.StatusUnauthorized
	err := KernelFlushDNS(context.Background(), writeFlushCfg(t, addr, "s3cret"))
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("%v", err)
	}
}

func TestKernelFlushDNSFailures(t *testing.T) {
	dir := t.TempDir()
	if err := KernelFlushDNS(context.Background(), filepath.Join(dir, "none.json")); err == nil {
		t.Fatal("读不到配置应失败")
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{"), 0o600)
	if err := KernelFlushDNS(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("%v", err)
	}
	if err := KernelFlushDNS(context.Background(), writeFlushCfg(t, "", "")); err == nil || !strings.Contains(err.Error(), "控制接口") {
		t.Fatalf("%v", err)
	}
	// 控制接口连不上(没人监听):error,不卡住
	s := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(s.URL, "http://")
	s.Close()
	if err := KernelFlushDNS(context.Background(), writeFlushCfg(t, addr, "")); err == nil {
		t.Fatal("连不上应失败")
	}
}

func TestKernelFlushDNSListenAllUsesLoopback(t *testing.T) {
	// 配置监听 0.0.0.0:端口 时,实际请求回环地址(dialAddr 的约定)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer s.Close()
	port := s.URL[strings.LastIndex(s.URL, ":")+1:]
	if err := KernelFlushDNS(context.Background(), writeFlushCfg(t, "0.0.0.0:"+port, "")); err != nil {
		t.Fatal(err)
	}
}
