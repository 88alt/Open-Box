package controllerproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 用真实的 sing-box 跑一遍:内核的控制接口真的要 secret,经代理应当能通,直连不带 secret 应当 401;
// WebSocket 能升级并收到内核推的流量帧。没有 sing-box 可执行文件时跳过。
func TestRealKernel(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box 可执行文件")
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	dir := t.TempDir()
	cfg := map[string]any{
		"log":          map[string]any{"level": "error"},
		"outbounds":    []any{map[string]any{"type": "direct", "tag": "direct"}},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": addr, "secret": "real-s3cret"}},
	}
	b, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "-c", cfgPath, "-D", dir)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	deadline := time.Now().Add(8 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sing-box 没有在 8 秒内开出控制接口")
		}
		time.Sleep(100 * time.Millisecond)
	}

	if resp, err := http.Get("http://" + addr + "/version"); err != nil || resp.StatusCode != 401 {
		t.Fatalf("前提:直连内核不带 secret 应 401: %v %v", resp, err)
	}
	p := newProxy(t, FileResolver(cfgPath))
	resp, err := http.Get(p.URL + "/api/controller/version")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "version") {
		t.Fatalf("经代理应通: %d %s", resp.StatusCode, body)
	}
	if resp, err := http.Get(p.URL + "/api/controller/proxies/direct"); err != nil || resp.StatusCode != 200 {
		t.Errorf("proxies/direct: %v %v", resp, err)
	}
	host := strings.TrimPrefix(p.URL, "http://")
	_, br, wsResp := dialWS(t, p.URL, "/api/controller-ws/traffic", map[string]string{"Origin": "http://" + host})
	if wsResp.StatusCode != 101 {
		t.Fatalf("WebSocket 应升级成功: %d", wsResp.StatusCode)
	}
	var tr struct{ Up, Down int64 }
	if err := json.Unmarshal([]byte(readFrame(t, br)), &tr); err != nil {
		t.Errorf("应收到内核推的流量 JSON: %v", err)
	}
	fmt.Println("真实内核:REST 与 WebSocket 经代理均通")
}
