package sitelatency

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/latencyhistory"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// 用真实的 sing-box:混合入站 + Clash API + 一个选择器组,检验「请求经内核 → 从 /connections 认出线路」整条链。
// 没有 sing-box 可执行文件时跳过。
func TestRealSingBoxRoute(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer site.Close()

	mixed, ctl := freePort(t), freePort(t)
	cfg := map[string]any{
		"log":      map[string]any{"level": "warn"},
		"inbounds": []any{map[string]any{"type": "mixed", "tag": "panel-in", "listen": "127.0.0.1", "listen_port": mixed}},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "直连"},
			map[string]any{"type": "selector", "tag": "测试组", "outbounds": []any{"直连"}, "default": "直连"},
		},
		"route":        map[string]any{"final": "测试组"},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": fmt.Sprintf("127.0.0.1:%d", ctl), "secret": "s3"}},
	}
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "-c", cfgFile)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	for i := 0; ; i++ {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ctl), 200*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if i > 200 { // 最多等 20 秒:机器忙时 sing-box 起得慢
			t.Fatalf("sing-box 没起来:%s", out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	st, _ := store.Open(t.TempDir())
	svc := &Service{
		Hist:      &latencyhistory.Service{Store: st, File: "site-latency-history"},
		ProxyAddr: fmt.Sprintf("127.0.0.1:%d", mixed),
		Snapshot: func(ctx context.Context) (singbox.KernelSnapshot, error) {
			snap, err := singbox.KernelConnections(ctx, cfgFile)
			return snap, err
		},
	}
	run, err := svc.Run(context.Background(), []Site{{ID: "local", URL: site.URL + "/generate_204"}})
	if err != nil {
		t.Fatal(err)
	}
	r := run.Sites[0]
	if r.Error != nil || r.MS == nil || r.OpenMS == nil {
		t.Fatalf("%+v %v\n%s", r, r.Error, out.String())
	}
	if r.Via == nil || *r.Via != "测试组" || strings.Join(r.Chain, "|") != "测试组|直连" {
		t.Fatalf("线路没认对:via=%v chain=%v\n%s", r.Via, r.Chain, out.String())
	}
	t.Logf("ms=%d openMs=%d chain=%v", *r.MS, *r.OpenMS, r.Chain)
}
