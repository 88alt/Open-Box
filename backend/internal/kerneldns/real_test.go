package kerneldns

import (
	"context"
	"encoding/binary"
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

func dnsQuery(name string, qtype uint16) []byte {
	b := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, p := range strings.Split(name, ".") {
		b = append(b, byte(len(p)))
		b = append(b, p...)
	}
	b = append(b, 0, 0, 0, 0, 0) // 根标签 + 类型 + 类别
	binary.BigEndian.PutUint16(b[len(b)-4:len(b)-2], qtype)
	binary.BigEndian.PutUint16(b[len(b)-2:], 1)
	return b
}

// 用真实的 sing-box 核对:日志的写法、规则序号(2*i+1)、拨号行、控制接口的日志订阅都和这里解析的一致。
// 没有 sing-box 可执行文件时跳过。换内核版本后这个测试就是「重新对一遍」的那一步。
func TestRealSingBoxKernelDNSTrace(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box")
	}
	// 本机的上游 DNS:对任何 A 查询回 198.51.100.5
	up, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := up.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[12:n]
			end := strings.IndexByte(string(q), 0) + 5
			r := append([]byte{buf[0], buf[1], 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0}, q[:end]...)
			r = append(r, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 198, 51, 100, 5)
			_, _ = up.WriteTo(r, from)
		}
	}()
	upPort := up.LocalAddr().(*net.UDPAddr).Port
	dnsPort, apiPort := freePort(t), freePort(t)
	cfg := map[string]any{
		"log": map[string]any{"level": "warn"}, // 配置里是 warn,debug 行也要能从订阅里收到
		"dns": map[string]any{
			"servers": []any{
				map[string]any{"type": "hosts", "tag": "ha", "predefined": map[string]any{"a.test": "203.0.113.7"}},
				map[string]any{"type": "udp", "tag": "up", "server": "127.0.0.1", "server_port": upPort, "detour": "d2"},
				map[string]any{"type": "fakeip", "tag": "fk", "inet4_range": "198.18.0.0/15"},
			},
			"rules": []any{
				map[string]any{"domain_suffix": []any{"up.test"}, "action": "route", "server": "up"},
				map[string]any{"domain_suffix": []any{"fk.test"}, "action": "route", "server": "fk"},
				map[string]any{"domain": []any{"rej.test"}, "action": "reject"},
				map[string]any{"domain": []any{"pre.test"}, "action": "predefined", "rcode": "NXDOMAIN"},
			},
			"final": "ha",
		},
		"inbounds": []any{map[string]any{"type": "direct", "tag": "dns-in", "listen": "127.0.0.1", "listen_port": dnsPort}},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
			map[string]any{"type": "direct", "tag": "d2", "connect_timeout": "5s"},
		},
		"route": map[string]any{
			"default_domain_resolver": "ha",
			"rules":                   []any{map[string]any{"inbound": []any{"dns-in"}, "action": "hijack-dns"}},
		},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": fmt.Sprintf("127.0.0.1:%d", apiPort), "secret": "real-s3cret"}},
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

	// 等内核起来(控制接口能订阅为止)
	var tap *singbox.LogTap
	for i := 0; i < 200; i++ {
		if tap, err = singbox.OpenKernelLogs(ctx, cfgFile); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if tap == nil {
		t.Fatalf("订阅不上日志:%v\n%s", err, out.String())
	}
	ask := func(name string, qtype uint16, wait time.Duration) bool {
		c, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", dnsPort))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(wait))
		_, _ = c.Write(dnsQuery(name, qtype))
		buf := make([]byte, 512)
		n, _ := c.Read(buf)
		return n > 0
	}
	// DNS 入站比控制接口晚一点起来:先问到有应答为止,再开始正式的查询
	ready := false
	for i := 0; i < 100 && !ready; i++ {
		ready = ask("a.test", 1, 300*time.Millisecond)
	}
	if !ready {
		t.Fatalf("DNS 入站一直没应答\n%s", out.String())
	}
	for _, q := range []struct {
		n string
		t uint16
	}{{"x.up.test", 1}, {"x.up.test", 1}, {"x.fk.test", 1}, {"rej.test", 1}, {"pre.test", 1}, {"a.test", 1}, {"x.up.test", 28}} {
		ask(q.n, q.t, 3*time.Second)
	}
	time.Sleep(300 * time.Millisecond)
	entries := tap.Stop()
	if len(entries) == 0 {
		t.Fatalf("订阅期间一行日志都没收到(配置里 log.level=warn 时 debug 行也该能收到)\n%s", out.String())
	}
	c := ConfigFrom(cfg)

	// x.up.test:A 问了两次,取最后一次——第二次是缓存;AAAA 另算 v6
	m := Analyze(entries, "x.up.test", c).Map()
	if m["seen"] != true || m["ruleIndex"] != 0 || m["ruleText"] != "domain_suffix=up.test" || m["action"] != "route(up)" || m["result"] != "cached" {
		t.Fatalf("x.up.test:%v", m)
	}
	// 第一次(只看第一次的日志行)是真正问了上游:拨号行、出站、答案、耗时
	var first []singbox.LogEntry
	for _, e := range entries {
		first = append(first, e)
		if strings.Contains(e.Payload, "exchanged A x.up.test.") {
			break
		}
	}
	m = Analyze(first, "x.up.test", c).Map()
	if m["result"] != "exchanged" || m["outbound"] != "d2" || m["viaProxy"] != false || m["rcode"] != "NOERROR" ||
		m["answers"].([]string)[0] != "198.51.100.5" || m["ms"] == nil || !strings.HasPrefix(m["source"].(string), "127.0.0.1:") ||
		m["server"].(map[string]any)["port"] != upPort {
		t.Fatalf("x.up.test 第一次:%v", m)
	}
	if m6 := Analyze(entries, "x.up.test", c).Map(); m6["v6"] == nil {
		t.Fatalf("同一个域名有 AAAA 查询,应有 v6:%v", m6)
	}
	// FakeIP / reject / predefined / 兜底:规则序号 2*i+1 的换算要和内核对上
	if m = Analyze(entries, "x.fk.test", c).Map(); m["ruleIndex"] != 1 || m["fakeIpLocal"] != true || m["fakeIp"] != true || m["result"] != "exchanged" {
		t.Fatalf("x.fk.test:%v", m)
	}
	if m = Analyze(entries, "rej.test", c).Map(); m["ruleIndex"] != 2 || m["result"] != "action" || m["action"] != "reject" {
		t.Fatalf("rej.test:%v", m)
	}
	if m = Analyze(entries, "pre.test", c).Map(); m["ruleIndex"] != 3 || m["result"] != "action" || m["rcode"] != "NXDOMAIN" {
		t.Fatalf("pre.test:%v", m)
	}
	if m = Analyze(entries, "a.test", c).Map(); m["ruleIndex"] != nil || m["server"].(map[string]any)["tag"] != "ha" || m["answers"].([]string)[0] != "203.0.113.7" {
		t.Fatalf("a.test(没命中规则,兜底):%v", m)
	}
}
