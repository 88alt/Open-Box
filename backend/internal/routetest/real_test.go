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

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/sitelatency"
)

// 用真实的 sing-box 当 DNS 入站(direct 入站 + hijack-dns + hosts 上游,和 core 的 DNS 入站是同一种做法),
// 检验手写的 DNS 报文内核认、它的答复我们解析得对。没有 sing-box 可执行文件时跳过。
func TestRealSingBoxDNSInbound(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box")
	}
	port := freeTCPUDPPort(t)
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

// 用真实的 sing-box 取 /connections 里 rule 的写法,检验 noDomainRule 认得出 IP 规则(没有 sing-box 可执行文件时跳过):
// 按 IP 连 127.0.0.3 命中 ip_cidr 规则,连接没有域名 → noDomain;按域名连(IP 规则对没解析的域名不起作用)走兜底 → 不是。
func TestRealSingBoxRuleStringNoDomain(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box")
	}
	free := func() int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	target, err := net.Listen("tcp", "127.0.0.3:0")
	if err != nil {
		t.Skipf("这台机器没有 127.0.0.3: %v", err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { time.Sleep(3 * time.Second); c.Close() }()
		}
	}()
	tport := target.Addr().(*net.TCPAddr).Port
	panelPort, apiPort := free(), free()
	cfg := map[string]any{
		"log":          map[string]any{"level": "warn"},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": fmt.Sprintf("127.0.0.1:%d", apiPort)}},
		"dns": map[string]any{
			"servers": []any{map[string]any{"type": "hosts", "tag": "h", "predefined": map[string]any{"other.test": "127.0.0.3"}}},
			"final":   "h",
		},
		"inbounds": []any{map[string]any{"type": "mixed", "tag": "panel", "listen": "127.0.0.1", "listen_port": panelPort}},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "fin"},
			map[string]any{"type": "direct", "tag": "viaip"},
		},
		"route": map[string]any{
			"final": "fin", "default_domain_resolver": "h",
			"rules": []any{map[string]any{"ip_cidr": []any{"127.0.0.3/32"}, "outbound": "viaip"}},
		},
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

	proxy := fmt.Sprintf("127.0.0.1:%d", panelPort)
	look := func(addr string) singbox.KernelConn {
		var c net.Conn
		var lp string
		var err error
		for i := 0; i < 200; i++ { // 等内核起来
			if c, lp, err = sitelatency.DialTunnel(ctx, proxy, 2*time.Second, addr); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("建不起隧道: %v\n%s", err, out.String())
		}
		defer c.Close()
		time.Sleep(300 * time.Millisecond)
		snap, err := singbox.KernelConnections(ctx, cfgFile)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range snap.Conns {
			if k.SourcePort == lp {
				return k
			}
		}
		t.Fatalf("连接表里没有 %s(端口 %s)", addr, lp)
		return singbox.KernelConn{}
	}

	byIP := look(fmt.Sprintf("127.0.0.3:%d", tport))
	t.Logf("按 IP 连:rule=%q host=%q chains=%v", byIP.Rule, byIP.Host, byIP.Chains)
	if byIP.Host != "" || !noDomainRule(byIP.Rule, byIP.Host) {
		t.Fatalf("按 IP 连命中 ip_cidr 规则,应认作 noDomain: %+v", byIP)
	}
	byName := look(fmt.Sprintf("other.test:%d", tport))
	t.Logf("按域名连:rule=%q host=%q chains=%v", byName.Rule, byName.Host, byName.Chains)
	if byName.Host != "other.test" || noDomainRule(byName.Rule, byName.Host) {
		t.Fatalf("按域名连有域名,不该认作 noDomain: %+v", byName)
	}
}

// 用真实的 sing-box(mixed 入站 + direct 出站)检验 TCP 方式(没有 sing-box 可执行文件时跳过):
// 目标端口开着 → 成功,并且能在真实的 /connections 里按本地端口认出这条 SOCKS5 连接;
// 目标端口关着 → 内核回 SOCKS5 的「连接被拒绝」,我们如实报失败。
// 同时留一条记录:同一个入站对 HTTP CONNECT 在目标关着时仍回 200,这正是 TCP 方式不走 HTTP CONNECT 的原因。
func TestRealSingBoxTCPMethod(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box")
	}
	free := func() int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { time.Sleep(3 * time.Second); c.Close() }()
		}
	}()
	tport := target.Addr().(*net.TCPAddr).Port
	panelPort, apiPort := free(), free()
	cfg := map[string]any{
		"log":          map[string]any{"level": "warn"},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": fmt.Sprintf("127.0.0.1:%d", apiPort)}},
		"inbounds":     []any{map[string]any{"type": "mixed", "tag": "panel-in", "listen": "127.0.0.1", "listen_port": panelPort}},
		"outbounds":    []any{map[string]any{"type": "direct", "tag": "direct-out"}},
		"route":        map[string]any{"final": "direct-out"},
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

	s := &Service{
		ProxyAddr: fmt.Sprintf("127.0.0.1:%d", panelPort), Timeout: 5 * time.Second,
		Snapshot: func(ctx context.Context) (singbox.KernelSnapshot, error) {
			return singbox.KernelConnections(ctx, cfgFile)
		},
	}
	var res *Result
	for i := 0; i < 200; i++ { // 等内核起来
		res, err = s.Run(ctx, Input{Target: "127.0.0.1", Port: tport, Method: "TCP"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Exit.OK || !strings.Contains(res.Exit.Error, "内核没在运行") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	ex := res.Exit
	if !ex.OK || ex.MS == nil || ex.Error != "" {
		t.Fatalf("目标端口开着应该成功:%+v\n%s", ex, out.String())
	}
	t.Logf("成功:ms=%d chains=%v rule=%q dest=%q notSeen=%v connErr=%q", *ex.MS, ex.Chains, ex.Rule, ex.DestinationIP, ex.NotSeen, ex.ConnectionsError)
	if len(ex.Chains) != 1 || ex.Chains[0] != "direct-out" || ex.NotSeen || ex.ConnectionsError != "" {
		t.Fatalf("应该在真实连接表里按本地端口认出这条 SOCKS5 连接:%+v", ex)
	}

	// 目标端口关掉:SOCKS5 应答「连接被拒绝」
	_ = target.Close()
	res, err = s.Run(ctx, Input{Target: "127.0.0.1", Port: tport, Method: "TCP"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("端口关着:ok=%v error=%q", res.Exit.OK, res.Exit.Error)
	if res.Exit.OK || res.Exit.Error != "内核报告目标拒绝了连接" {
		t.Fatalf("目标端口关着应该报连接被拒绝:%+v\n%s", res.Exit, out.String())
	}

	// 记录:HTTP CONNECT 对关着的端口仍然「成功」(先回 200)
	c, _, err := sitelatency.DialTunnel(ctx, s.ProxyAddr, 2*time.Second, fmt.Sprintf("127.0.0.1:%d", tport))
	if err == nil {
		c.Close()
		t.Log("HTTP CONNECT 对关着的端口也回了 200:单凭它确认不了「连上了」,所以 TCP 方式走 SOCKS5")
	} else {
		t.Logf("注意:这个版本的 sing-box 对 HTTP CONNECT 在目标关着时会失败(%v),TCP 方式可以简化", err)
	}
}

// freeTCPUDPPort 挑一个 TCP 和 UDP 都空闲的端口(sing-box 的 direct 入站两个都会占;只查 UDP 的话,和别的包并发跑测试时 TCP 那边可能撞车)。
func freeTCPUDPPort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		l.Close()
		if err == nil {
			pc.Close()
			return port
		}
	}
	t.Fatal("挑不到 TCP 和 UDP 都空闲的端口")
	return 0
}
