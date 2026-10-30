package routetest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// 真实 core 对默认档案生成的 dns 段和出站(规则集改成本机可写的路径),规则集文件用源码格式。
func writeConfig(t *testing.T, dir string, suffixes map[string]string) string {
	t.Helper()
	sets := []any{}
	for tag, body := range suffixes {
		p := filepath.Join(dir, tag+".json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		sets = append(sets, map[string]any{"tag": tag, "type": "local", "format": "source", "path": p})
	}
	cfg := map[string]any{
		"dns": map[string]any{
			"final": "dns-direct",
			"rules": []any{
				map[string]any{"rule_set": []any{"geosite-cn"}, "server": "dns-direct"},
				map[string]any{"rule_set": []any{"obflip-fallback"}, "server": "dns-proxy"},
			},
			"servers": []any{
				map[string]any{"tag": "dns-direct", "type": "udp", "server": "223.5.5.5"},
				map[string]any{"tag": "dns-proxy", "type": "tcp", "server": "1.1.1.1", "detour": "其他"},
			},
		},
		"route": map[string]any{"rule_set": sets},
		"outbounds": []any{
			map[string]any{"tag": "直连", "type": "direct"},
			map[string]any{"tag": "其他", "type": "selector"},
			map[string]any{"tag": "自动选择", "type": "urltest"},
			map[string]any{"tag": "hk-01", "type": "trojan"},
		},
	}
	b, _ := json.Marshal(cfg)
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPredictReadsDeployedConfig(t *testing.T) {
	dir := t.TempDir()
	p := &DNSPredictor{ConfigFile: writeConfig(t, dir, map[string]string{
		"geosite-cn":      `{"rules":[{"domain_suffix":["baidu.com"]}]}`,
		"obflip-fallback": `{"rules":[{"domain_suffix":["google.com"]}]}`,
	})}
	d, err := p.Predict(context.Background(), "www.baidu.com")
	if err != nil || d.RuleIndex == nil || *d.RuleIndex != 0 || d.Server.Tag != "dns-direct" || *d.ViaProxy || d.Server.Server != "223.5.5.5" {
		t.Fatalf("%+v %v", d, err)
	}
	d, err = p.Predict(context.Background(), "www.google.com")
	if err != nil || *d.RuleIndex != 1 || d.Server.Tag != "dns-proxy" || !*d.ViaProxy || len(d.RuntimeChain) != 0 {
		t.Fatalf("没接代理列表时不补运行时线路: %+v %v", d, err)
	}
	d, _ = p.Predict(context.Background(), "unknown.example")
	if d.RuleIndex != nil || d.Server.Tag != "dns-direct" {
		t.Fatalf("没命中走 final: %+v", d)
	}
	// 配置改了(规则集文件内容变了)下一次要读到新的
	dir2 := t.TempDir()
	p.ConfigFile = writeConfig(t, dir2, map[string]string{"geosite-cn": `{"rules":[]}`, "obflip-fallback": `{"rules":[]}`})
	if d, _ = p.Predict(context.Background(), "www.baidu.com"); d.RuleIndex != nil {
		t.Errorf("换了配置要重新判断: %+v", d)
	}
}

func TestPredictRuntimeChain(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, map[string]string{"geosite-cn": `{"rules":[]}`, "obflip-fallback": `{"rules":[{"domain_suffix":["g.test"]}]}`})
	proxies := []singbox.KernelProxy{
		{Name: "其他", Type: "Selector", Now: "自动选择"},
		{Name: "自动选择", Type: "URLTest", Now: "hk-01"},
		{Name: "hk-01", Type: "Trojan"},
	}
	p := &DNSPredictor{ConfigFile: cfg, Proxies: func(context.Context) ([]singbox.KernelProxy, error) { return proxies, nil }}
	d, err := p.Predict(context.Background(), "a.g.test")
	if err != nil || !reflect.DeepEqual(d.RuntimeChain, []string{"其他", "自动选择", "hk-01"}) || d.RuntimeLeaf != "hk-01" || !*d.ViaProxy {
		t.Fatalf("%+v %v", d, err)
	}
	// 当前选中的是内置直连:这台 DNS 此刻其实没走代理
	proxies[0].Now = "直连"
	d, _ = p.Predict(context.Background(), "a.g.test")
	if !reflect.DeepEqual(d.RuntimeChain, []string{"其他", "直连"}) || d.RuntimeLeaf != "直连" || *d.ViaProxy {
		t.Fatalf("%+v", d)
	}
	// 环:不会死循环
	proxies = []singbox.KernelProxy{{Name: "其他", Now: "自动选择"}, {Name: "自动选择", Now: "其他"}}
	d, _ = p.Predict(context.Background(), "a.g.test")
	if len(d.RuntimeChain) != maxChain+1 {
		t.Errorf("环应该在 %d 层处停下: %v", maxChain, d.RuntimeChain)
	}
	// 内核里没有这个出站 / 读代理列表失败:不补,但预测本身照常
	proxies = []singbox.KernelProxy{{Name: "别的"}}
	if d, _ = p.Predict(context.Background(), "a.g.test"); len(d.RuntimeChain) != 0 || d.Server.Tag != "dns-proxy" {
		t.Errorf("%+v", d)
	}
	p.Proxies = func(context.Context) ([]singbox.KernelProxy, error) { return nil, errors.New("内核没在运行") }
	if d, err = p.Predict(context.Background(), "a.g.test"); err != nil || len(d.RuntimeChain) != 0 || !*d.ViaProxy {
		t.Errorf("读不到代理列表不影响预测: %+v %v", d, err)
	}
}

func TestPredictFailures(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{坏了"), 0o600)
	nodns := filepath.Join(dir, "nodns.json")
	_ = os.WriteFile(nodns, []byte(`{"outbounds":[]}`), 0o600)
	for name, file := range map[string]string{"不存在": filepath.Join(dir, "none.json"), "不是 JSON": bad, "没有 dns": nodns} {
		if _, err := (&DNSPredictor{ConfigFile: file}).Predict(context.Background(), "a.test"); err == nil {
			t.Errorf("%s 应该报错", name)
		}
	}
	// 规则集文件读不到:不报错,记成前提,结果走 final
	cfg := writeConfig(t, dir, map[string]string{"geosite-cn": `{"rules":[]}`, "obflip-fallback": `{"rules":[]}`})
	_ = os.Remove(filepath.Join(dir, "geosite-cn.json"))
	d, err := (&DNSPredictor{ConfigFile: cfg}).Predict(context.Background(), "a.test")
	if err != nil || len(d.Assumed) != 1 || d.Assumed[0].RuleIndex != 0 || d.Assumed[0].Needs[0] != "rule_set:geosite-cn" || d.RuleIndex != nil {
		t.Errorf("%+v %v", d, err)
	}
}

func TestRunWithDNSPredictor(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, map[string]string{"geosite-cn": `{"rules":[{"domain_suffix":["cn.test"]}]}`, "obflip-fallback": `{"rules":[]}`})
	s := &Service{ProxyAddr: "127.0.0.1:1", DNSAddr: "127.0.0.1:1", Timeout: 200 * 1e6, DNS: &DNSPredictor{ConfigFile: cfg}}
	res, err := s.Run(context.Background(), Input{Target: "a.cn.test", Method: "GET"}) // 探测失败(入口连不上)也要有 dns
	if err != nil {
		t.Fatal(err)
	}
	d, ok := res.DNS.(*dnsdecide.Decision)
	if !ok || d.RuleIndex == nil || *d.RuleIndex != 0 {
		t.Fatalf("%#v", res.DNS)
	}
	b, _ := json.Marshal(res)
	for _, want := range []string{`"ruleIndex":0`, `"server":{"tag":"dns-direct"`, `"viaProxy":false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("响应里没有 %s: %s", want, b)
		}
	}
	// IP 目标不预测;预测失败写 error;没接预测器写「没有接入」
	if res, _ = s.Run(context.Background(), Input{Target: "1.1.1.1", Method: "GET"}); asMap(res.DNS)["skipped"] != true {
		t.Errorf("IP: %#v", res.DNS)
	}
	s.DNS.ConfigFile = filepath.Join(dir, "none.json")
	if res, _ = s.Run(context.Background(), Input{Target: "a.test", Method: "GET"}); !strings.Contains(asMap(res.DNS)["error"].(string), "DNS 判定失败") {
		t.Errorf("失败: %#v", res.DNS)
	}
	s.DNS = nil
	if res, _ = s.Run(context.Background(), Input{Target: "a.test", Method: "GET"}); !strings.Contains(asMap(res.DNS)["error"].(string), "没有接入") {
		t.Errorf("没接: %#v", res.DNS)
	}
}

func TestAnnotateResolve(t *testing.T) {
	yes, no := true, false
	viaProxy := func(v *bool) *dnsdecide.Decision { return &dnsdecide.Decision{ViaProxy: v, RuntimeLeaf: "hk-01"} }
	cases := []struct {
		name       string
		r          Resolve
		dns        any
		wantFrom   string
		wantCached bool
	}{
		{"代理侧回了别处的 fake-ip:标出应答的节点", Resolve{OK: true, FakeIP: true, MS: 80}, viaProxy(&yes), "hk-01", false},
		{"内核自己的 fake-ip 不是节点发的", Resolve{OK: true, FakeIP: true, FakeIPLocal: true, MS: 1}, viaProxy(&yes), "", false},
		{"直连解析回了 fake-ip:没有 fakeIpFrom", Resolve{OK: true, FakeIP: true, MS: 80}, viaProxy(&no), "", false},
		{"代理侧几毫秒就回来:缓存", Resolve{OK: true, MS: 2}, viaProxy(&yes), "", true},
		{"刚好在阈值上也算", Resolve{OK: true, MS: cachedMaxMS}, viaProxy(&yes), "", true},
		{"代理侧正常耗时:不是缓存", Resolve{OK: true, MS: cachedMaxMS + 1}, viaProxy(&yes), "", false},
		{"直连很快是正常的,不标缓存", Resolve{OK: true, MS: 1}, viaProxy(&no), "", false},
		{"没查到(失败)很快也不标缓存", Resolve{OK: false, MS: 1, Error: "NXDOMAIN"}, viaProxy(&yes), "", false},
		{"是否走代理判断不了:不猜", Resolve{OK: true, MS: 1}, viaProxy(nil), "", false},
		{"判定失败({error}):不补", Resolve{OK: true, MS: 1}, map[string]any{"error": "x"}, "", false},
		{"判定没接入:不补", Resolve{OK: true, MS: 1}, nil, "", false},
	}
	for _, c := range cases {
		r := c.r
		annotateResolve(&r, c.dns)
		if r.FakeIPFrom != c.wantFrom || r.Cached != c.wantCached {
			t.Errorf("%s: fakeIpFrom=%q cached=%v,想要 %q %v", c.name, r.FakeIPFrom, r.Cached, c.wantFrom, c.wantCached)
		}
	}
	annotateResolve(nil, viaProxy(&yes)) // 没有 resolve(目标是 IP)不能 panic
}

func TestRunAnnotatesResolveWithDecision(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, map[string]string{"geosite-cn": `{"rules":[]}`, "obflip-fallback": `{"rules":[{"domain_suffix":["g.test"]}]}`})
	proxies := []singbox.KernelProxy{{Name: "其他", Type: "Selector", Now: "hk-01"}, {Name: "hk-01", Type: "Trojan"}}
	f := newFakeDNS(t, false, func(req []byte, _ bool) []byte { return reply(req, 0, false, a4("198.18.3.4", 30)) })
	s := &Service{
		ProxyAddr: "127.0.0.1:1", DNSAddr: f.addr, Timeout: 300 * time.Millisecond, // 面板入站连不上:只看 resolve
		DNS: &DNSPredictor{ConfigFile: cfg, Proxies: func(context.Context) ([]singbox.KernelProxy, error) { return proxies, nil }},
	}
	res, err := s.Run(context.Background(), Input{Target: "a.g.test"})
	if err != nil || res.Resolve == nil {
		t.Fatalf("%+v %v", res, err)
	}
	if !res.Resolve.FakeIP || res.Resolve.FakeIPLocal || res.Resolve.FakeIPFrom != "hk-01" {
		t.Fatalf("应标出是 hk-01 应的占位地址: %+v", res.Resolve)
	}
	b, _ := json.Marshal(res.Resolve)
	for _, want := range []string{`"fakeIp":true`, `"fakeIpFrom":"hk-01"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON 里没有 %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), "fakeIpLocal") || strings.Contains(string(b), "cached") {
		t.Errorf("不该带 fakeIpLocal / cached: %s", b)
	}
}
