package routetest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// 一份带热切换开关的 DNS 规则:站点集「海外」的代理支(逻辑与:站点集 + 开关)在前,直连支紧跟其后,最后是兜底代理支。
const staleCfg = `{
 "dns": {"final":"dns-direct","rules":[
  {"type":"logical","mode":"and","rules":[{"rule_set":["geosite-geolocation-!cn"]},{"rule_set":["obflip-abc123"]}],"server":"dns-proxy"},
  {"rule_set":["geosite-geolocation-!cn"],"server":"dns-direct"},
  {"type":"logical","mode":"and","rules":[{"rule_set":["obflip-fallback"]}],"server":"dns-proxy"}
 ]},
 "outbounds":[{"tag":"direct","type":"direct"}]
}`

func staleConfig(t *testing.T) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(staleCfg), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func proxiesOf(list ...singbox.KernelProxy) []singbox.KernelProxy { return list }

func policyStub() func(context.Context) ([]Policy, string, error) {
	return func(context.Context) ([]Policy, string, error) {
		return []Policy{{Name: "海外", Rulesets: []string{"geosite-geolocation-!cn"}}}, "兜底", nil
	}
}

func TestAnnotateStale(t *testing.T) {
	ctx := context.Background()
	cfg := staleConfig(t)
	idx := 0
	dec := func(ruleIdx *int, tag string) *dnsdecide.Decision {
		return &dnsdecide.Decision{RuleIndex: ruleIdx, Server: &dnsdecide.Server{Tag: tag}}
	}
	p := &DNSPredictor{Policies: policyStub()}

	// 1) 配置里这条走代理侧,站点集此刻已经翻到直连 → stale=direct
	proxies := proxiesOf(
		singbox.KernelProxy{Name: "海外", Type: "selector", Now: "直连"},
		singbox.KernelProxy{Name: "直连", Type: "direct"},
	)
	d := dec(&idx, "dns-proxy")
	p.annotateStale(ctx, cfg, d, proxies)
	if d.Stale != "direct" {
		t.Fatalf("stale = %q, 想要 direct", d.Stale)
	}

	// 2) 站点集此刻确实落在节点上:方向一致,不补
	proxies = proxiesOf(
		singbox.KernelProxy{Name: "海外", Type: "selector", Now: "自动选择"},
		singbox.KernelProxy{Name: "自动选择", Type: "urltest", Now: "hk-01"},
		singbox.KernelProxy{Name: "hk-01", Type: "Shadowsocks"},
	)
	d = dec(&idx, "dns-proxy")
	p.annotateStale(ctx, cfg, d, proxies)
	if d.Stale != "" {
		t.Fatalf("stale = %q, 不应该补", d.Stale)
	}

	// 3) 直连支的规则、站点集却翻到了代理:stale=proxy
	direct := 1
	d = dec(&direct, "dns-direct")
	p.annotateStale(ctx, cfg, d, proxiesOf(
		singbox.KernelProxy{Name: "海外", Type: "selector", Now: "hk-01"},
		singbox.KernelProxy{Name: "hk-01", Type: "Shadowsocks"},
	))
	if d.Stale != "proxy" {
		t.Fatalf("stale = %q, 想要 proxy", d.Stale)
	}

	// 4) 没有规则命中:按兜底站点算
	d = dec(nil, "dns-direct")
	p.annotateStale(ctx, cfg, d, proxiesOf(
		singbox.KernelProxy{Name: "兜底", Type: "selector", Now: "hk-01"},
		singbox.KernelProxy{Name: "hk-01", Type: "Shadowsocks"},
	))
	if d.Stale != "proxy" {
		t.Fatalf("stale = %q, 想要 proxy", d.Stale)
	}

	// 5) 站点集不在内核代理列表里:不判断
	d = dec(&idx, "dns-proxy")
	p.annotateStale(ctx, cfg, d, proxiesOf(singbox.KernelProxy{Name: "别的", Type: "direct"}))
	if d.Stale != "" {
		t.Fatalf("stale = %q, 不应该补", d.Stale)
	}

	// 6) 没接 Policies:不判断
	np := &DNSPredictor{}
	d = dec(&idx, "dns-proxy")
	np.annotateStale(ctx, cfg, d, proxies)
	if d.Stale != "" {
		t.Fatalf("stale = %q, 不应该补", d.Stale)
	}

	// 7) 备用上游 tag(__alt1)也算代理侧
	proxies = proxiesOf(
		singbox.KernelProxy{Name: "海外", Type: "selector", Now: "direct"},
		singbox.KernelProxy{Name: "direct", Type: "direct"},
	)
	d = dec(&idx, "dns-proxy__alt1")
	p.annotateStale(ctx, cfg, d, proxies)
	if d.Stale != "direct" {
		t.Fatalf("stale = %q, 想要 direct", d.Stale)
	}
}

// 规则里的开关规则集不参与归属判断:逻辑规则里只看站点集那一支。
func TestRuleCondition(t *testing.T) {
	var rule map[string]any
	if err := json.Unmarshal([]byte(`{"type":"logical","mode":"and","rules":[{"rule_set":["obflip-abc"]},{"rule_set":["geosite-cn","geosite-x"]}],"server":"dns-proxy"}`), &rule); err != nil {
		t.Fatal(err)
	}
	sets, suffixes := ruleCondition(rule)
	if len(sets) != 2 || sets[0] != "geosite-cn" || sets[1] != "geosite-x" || len(suffixes) != 0 {
		t.Fatalf("%v %v", sets, suffixes)
	}
	// 非逻辑规则:去掉 obflip-* 后照原样
	var flat map[string]any
	if err := json.Unmarshal([]byte(`{"rule_set":["obflip-abc","geosite-cn"],"server":"dns-proxy"}`), &flat); err != nil {
		t.Fatal(err)
	}
	if sets, _ := ruleCondition(flat); len(sets) != 1 || sets[0] != "geosite-cn" {
		t.Fatalf("%v", sets)
	}
}
