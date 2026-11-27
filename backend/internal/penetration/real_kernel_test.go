package penetration

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

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
	"github.com/timeflysoon/Open-Box/backend/internal/routedecide"
	"github.com/timeflysoon/Open-Box/backend/internal/routetest"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// 这组测试把「域名 → 真实内核 DNS 解析 → 拿解析出的地址去比 IP 规则 → 命中时记 viaIp」
// 整条链路装在真实 sing-box 上跑一遍。此前的 viaIp 只用假 Resolver 验过,这次补的是
// 「解析这一步是真的内核给的地址」这一段。
//
// 做法:起一个真实 sing-box,用 dns 的 hosts 服务器把一个测试域名固定解析到私网地址,
// 再把内核的 DNS 入站地址交给 routetest.Service(真 ResolveName),策略配置用真实 core
// 生成的那类形状(私网直连规则 ip_is_private 不带「连接没有域名」的前提,所以域名目标
// 靠解析出的地址命中)。没有 sing-box 可执行文件时跳过。
func TestRealKernelResolveYieldsViaIP(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box")
	}

	// 私有地址:命中 core 生成的「私网直连」规则(ip_is_private,不带域名前提)
	const privateAddr = "192.168.99.99"
	const probeDomain = "viaip.probe.test"

	dnsPort := freeTCPPort(t)
	apiPort := freeTCPPort(t)
	cfg := map[string]any{
		"log": map[string]any{"level": "warn"},
		"dns": map[string]any{
			"servers": []any{
				map[string]any{
					"type": "hosts", "tag": "vipprobe",
					"predefined": map[string]any{probeDomain: privateAddr},
				},
			},
			"rules": []any{
				map[string]any{"domain": []any{probeDomain}, "server": "vipprobe"},
			},
			"final": "vipprobe",
		},
		"inbounds": []any{
			map[string]any{"type": "direct", "tag": "dns-in", "listen": "127.0.0.1", "listen_port": dnsPort},
		},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route": map[string]any{
			"rules": []any{map[string]any{"inbound": []any{"dns-in"}, "action": "hijack-dns"}},
			// core 生成的「私网直连」就是这个形状:只有 ip_is_private,没有「连接没有域名」的前提
			"rule_set": []any{},
		},
		"experimental": map[string]any{
			"clash_api": map[string]any{
				"external_controller": fmt.Sprintf("127.0.0.1:%d", apiPort),
				"secret":              "viaip-real",
			},
		},
	}
	cfg["route"].(map[string]any)["rules"] = []any{
		map[string]any{"inbound": []any{"dns-in"}, "action": "hijack-dns"},
	}

	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgFile, b, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "-c", cfgFile, "-D", dir)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()

	// 等 DNS 入站起来
	dnsAddr := fmt.Sprintf("127.0.0.1:%d", dnsPort)
	deadline := time.Now().Add(20 * time.Second)
	for {
		c, derr := net.Dial("udp", dnsAddr)
		if derr == nil {
			_ = c.SetDeadline(time.Now().Add(300 * time.Millisecond))
			_, _ = c.Write(dnsQueryBytes(probeDomain))
			buf := make([]byte, 512)
			n, _ := c.Read(buf)
			_ = c.Close()
			if n > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("DNS 入站一直没应答\n%s", out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 真实解析入口:用的就是 main.go 里那一段的同款 Service(只把 DNSAddr 指到本次实例)
	rs := &routetest.Service{DNSAddr: dnsAddr, Timeout: 3 * time.Second}
	fake, addrs, errText := rs.ResolveName(context.Background(), probeDomain)
	if errText != "" {
		t.Fatalf("真实内核解析失败:%s\n%s", errText, out.String())
	}
	if fake {
		t.Fatal("hosts 解析器给的是真地址,不该判成 fake-ip")
	}
	if len(addrs) != 1 || addrs[0] != privateAddr {
		t.Fatalf("真实内核应把 %s 解析成 %s,实际 %v", probeDomain, privateAddr, addrs)
	}

	// 用真实 core 生成的规则形状判定:私网直连(不带域名前提)应靠解析出的地址命中并记 viaIp
	s := &Service{
		Config: func() (map[string]any, error) {
			var m map[string]any
			return m, json.Unmarshal([]byte(viaIPCfg), &m)
		},
		Sets: func(map[string]any) routedecide.Sets {
			return sets{}
		},
		DNS:      dns{d: &dnsdecide.Decision{}},
		Resolver: rs,
		Proxies:  func(context.Context) ([]singbox.KernelProxy, error) { return nil, nil },
	}
	res, err := s.Query(context.Background(), probeDomain)
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolved == nil || len(res.Resolved.Addresses) != 1 || res.Resolved.Addresses[0] != privateAddr {
		t.Fatalf("响应里的 resolved 应是真实内核给的地址:%+v", res.Resolved)
	}
	if res.Matched == nil {
		t.Fatalf("应靠解析出的私网地址命中私网直连:%+v", res)
	}
	if res.Matched.ViaIP != privateAddr {
		t.Fatalf("命中靠的是解析出的地址,ViaIP 应是 %s,实际 %q", privateAddr, res.Matched.ViaIP)
	}
	if res.Matched.Outbound != "direct" {
		t.Fatalf("私网直连的出站应是 direct:%+v", res.Matched)
	}
}

// viaIPCfg 是「私网直连」一条规则的配置(与 core 生成的形状一致:只有 ip_is_private)
const viaIPCfg = `{
  "route": {
    "rules": [
      {"ip_is_private": true, "outbound": "direct"}
    ],
    "final": "proxy"
  },
  "outbounds": [{"type":"direct","tag":"direct"},{"type":"direct","tag":"proxy"}]
}`

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// dnsQueryBytes 拼一个 A 查询
func dnsQueryBytes(name string) []byte {
	b := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, p := range strings.Split(name, ".") {
		b = append(b, byte(len(p)))
		b = append(b, p...)
	}
	b = append(b, 0, 0, 1, 0, 1)
	return b
}
