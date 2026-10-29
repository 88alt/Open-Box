package routetest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 用真实的 sing-box 当 DNS 入站(direct 入站 + hijack-dns + hosts 上游,和 core 的 DNS 入站是同一种做法),
// 检验手写的 DNS 报文内核认、它的答复我们解析得对。没有 sing-box 可执行文件时跳过。
func TestRealSingBoxDNSInbound(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box")
	}
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	cfg := map[string]any{
		"log": map[string]any{"level": "warn"},
		"dns": map[string]any{
			"servers": []any{map[string]any{"type": "hosts", "tag": "h", "predefined": map[string]any{
				"v4.example.test":   "203.0.113.7",
				"dual.example.test": []any{"203.0.113.8", "2001:db8::8"},
			}}},
			"final": "h",
		},
		"inbounds":  []any{map[string]any{"type": "direct", "tag": "dns-in", "listen": "127.0.0.1", "listen_port": port}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"rules": []any{map[string]any{"inbound": []any{"dns-in"}, "action": "hijack-dns"}}},
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

	s := &Service{DNSAddr: fmt.Sprintf("127.0.0.1:%d", port), Timeout: 2 * time.Second}
	var r *Resolve
	for i := 0; i < 200; i++ { // 等内核起来(机器忙时可能要十几秒)
		r = s.resolve(context.Background(), "v4.example.test", false)
		if r.OK {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !r.OK || len(r.Answers) != 1 || r.Answers[0] != "203.0.113.7" {
		t.Fatalf("A 记录没解析对:%+v\n%s", r, out.String())
	}
	r = s.resolve(context.Background(), "dual.example.test", true)
	if !r.OK || r.OK6 == nil || !*r.OK6 || r.Answers6[0] != "2001:db8::8" || r.Answers[0] != "203.0.113.8" {
		t.Fatalf("双栈解析不对:%+v\n%s", r, out.String())
	}
	r = s.resolve(context.Background(), "nope.example.test", false)
	if r.OK || r.Error == "" {
		t.Fatalf("不存在的域名应该失败:%+v", r)
	}
	t.Logf("v4 ttl/ms 正常;不存在的域名:%q status=%v", r.Error, *r.Status)
}
