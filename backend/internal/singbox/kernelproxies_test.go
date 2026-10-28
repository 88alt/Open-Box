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

func kernelConfig(t *testing.T, srv *httptest.Server, secret string) string {
	t.Helper()
	cfg := map[string]any{"experimental": map[string]any{"clash_api": map[string]any{
		"external_controller": strings.TrimPrefix(srv.URL, "http://"), "secret": secret,
	}}}
	b, _ := json.Marshal(cfg)
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKernelProxiesReadsHistoryWithSecret(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte(`{"proxies":{
			"HK 01":{"type":"Trojan","history":[{"time":"2026-10-28T10:00:00Z","delay":123}]},
			"自动选择":{"type":"URLTest","now":"HK 01","history":[]},
			"直连":{"type":"Direct"}}}`))
	}))
	defer srv.Close()
	got, err := KernelProxies(context.Background(), kernelConfig(t, srv, "s3"))
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer s3" || gotPath != "/proxies" {
		t.Errorf("请求不对: %q %q", gotAuth, gotPath)
	}
	by := map[string]KernelProxy{}
	for _, p := range got {
		by[p.Name] = p
	}
	if len(by) != 3 || by["HK 01"].History[0].Delay != 123 || by["自动选择"].Now != "HK 01" || by["直连"].History != nil {
		t.Errorf("解析结果: %+v", by)
	}
}

func TestKernelProxiesErrors(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer bad.Close()
	if _, err := KernelProxies(context.Background(), kernelConfig(t, bad, "x")); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("鉴权失败应报错: %v", err)
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer junk.Close()
	if _, err := KernelProxies(context.Background(), kernelConfig(t, junk, "")); err == nil {
		t.Error("格式不对应报错")
	}
	if _, err := KernelProxies(context.Background(), filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("没有配置文件应报错")
	}
	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cfg := kernelConfig(t, gone, "")
	gone.Close()
	if _, err := KernelProxies(context.Background(), cfg); err == nil {
		t.Error("连不上应报错")
	}
	noAPI := filepath.Join(t.TempDir(), "c.json")
	_ = os.WriteFile(noAPI, []byte(`{}`), 0o600)
	if _, err := KernelProxies(context.Background(), noAPI); err == nil {
		t.Error("没开控制接口应报错")
	}
}
