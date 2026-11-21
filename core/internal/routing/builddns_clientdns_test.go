package routing

import (
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/clientroutes"
)

// 自写测试:终端分流配了自定义 DNS(Route.DNSTag)时,BuildDNS 给这些终端加一条「来源 → 专用解析器」的规则:
//   - 只有查询进得了内核的终端才加(dnsmasq 模式要有 nft 重定向 AutoRedirect;劫持模式一律);
//   - 路由侧的「先解析」(Resolvers.Clients)也指向专用解析器;
//   - 没配的终端不受影响。

func findClientDNSRule(rules []any, server string) (obj, bool) {
	for _, r := range rules {
		if m, ok := r.(map[string]any); ok && m["server"] == server {
			if _, has := m["source_ip_cidr"]; has {
				return obj(m), true
			}
		}
	}
	return nil, false
}

func TestBuildDNSClientCustomDNS(t *testing.T) {
	routes := []clientroutes.Route{
		{ID: "a", Match: "ip", Outbound: "美国", Sources: []string{"10.0.0.5/32"}, DNS: map[string]any{"url": "udp://10.0.0.1"}, DNSTag: "dns-client-0"},
		{ID: "b", Match: "ip", Outbound: "美国", Sources: []string{"10.0.0.6/32"}},
	}
	run := func(dnsMode string, autoRedirect bool) DNSResult {
		o := plOpts()
		o.ClientRoutes = routes
		o.Terminal = TerminalDNS{AutoRedirect: autoRedirect}
		res, err := BuildDNS(skProfile(obj{"mode": dnsMode}, nil), o)
		if err != nil {
			t.Fatalf("%s: %v", dnsMode, err)
		}
		return res
	}

	for _, mode := range []string{"dnsmasq", "hijack"} {
		res := run(mode, true)
		rules, _ := res.DNS["rules"].([]any)
		rule, ok := findClientDNSRule(rules, "dns-client-0")
		if !ok {
			t.Fatalf("%s: 没有生成终端自定义 DNS 规则: %#v", mode, rules)
		}
		if got := rule["source_ip_cidr"].([]string); len(got) != 1 || got[0] != "10.0.0.5/32" {
			t.Errorf("%s: 规则来源不对: %v", mode, got)
		}
		if _, other := findClientDNSRule(rules, "dns-client-1"); other {
			t.Errorf("%s: 没配自定义 DNS 的终端不该有专用规则", mode)
		}
		foundResolver := false
		for _, c := range res.Resolvers.Clients {
			if c.Server == "dns-client-0" && len(c.Sources) == 1 && c.Sources[0] == "10.0.0.5/32" {
				foundResolver = true
			}
		}
		if !foundResolver {
			t.Errorf("%s: Resolvers.Clients 没指向专用解析器: %+v", mode, res.Resolvers.Clients)
		}
		ownerOK := false
		for _, ow := range res.RuleOwners {
			if ow.Kind == "client" && ow.To-ow.From >= 1 && ow.To <= len(rules) {
				if m, ok := rules[ow.From].(map[string]any); ok && m["server"] == "dns-client-0" {
					ownerOK = true
				}
			}
		}
		if !ownerOK {
			t.Errorf("%s: 规则归属没记 client: %+v", mode, res.RuleOwners)
		}
	}

	// dnsmasq 模式没有 nft 重定向时,终端的查询进不了内核,不生成规则
	res := run("dnsmasq", false)
	rules, _ := res.DNS["rules"].([]any)
	if _, ok := findClientDNSRule(rules, "dns-client-0"); ok {
		t.Errorf("没有重定向时不该生成终端自定义 DNS 规则")
	}
}
