package config

import (
	"strings"
	"testing"
)

// 自写测试向量:前置自定义分流里出口不在已知出站表里的行(节点组被删 / 停用,规则还指着它)整行丢掉。
// 期望值人工推算:连接侧本来就跳过这种行;DNS 侧若不跳会写一条 `rule_set: geosite-xxx` 的解析规则,
// 而这份规则集在路由里没有声明,sing-box 1.14 起会拒绝整份配置(内核起不来)。
// 见 routing.DropUnknownCustomRules。
func TestBuildDetailedDropsDanglingCustomRules(t *testing.T) {
	// 节点 节点1 是已知出站;「已删的组」不是。两行都是 geosite 行(DNS 侧会引用 geosite-xxx)。
	args := `{"nodes":[` + sk("节点1", "1.2.3.4", 1080) + `],"profile":{"dns":{"mode":"hijack","split":true},"routing":{"custom":{"enabled":true,"rules":[` +
		`{"type":"geosite","value":"netflix","outbound":"已删的组"},` +
		`{"type":"geosite","value":"youtube","outbound":"节点1"}]}}}}`
	res := bhMust(t, args)

	dns := res["config"].(map[string]any)["dns"].(map[string]any)
	rules, _ := dns["rules"].([]any)
	var sawNetflix, sawYoutube bool
	for _, r := range rules {
		m, _ := r.(map[string]any)
		sets, _ := m["rule_set"].([]any)
		for _, s := range sets {
			switch s {
			case "geosite-netflix":
				sawNetflix = true
			case "geosite-youtube":
				sawYoutube = true
			}
		}
	}
	if sawNetflix {
		t.Errorf("出口不认识的 geosite 行仍然写进了 DNS 规则(会引用路由里没声明的规则集):%s", bhJSON(t, rules))
	}
	if !sawYoutube {
		t.Errorf("出口认识的 geosite 行没有写进 DNS 规则:%s", bhJSON(t, rules))
	}

	// 连接侧同样不该出现这一行(它一直是这样,这里一并钉住,免得以后两侧又走岔)。
	route := res["config"].(map[string]any)["route"].(map[string]any)
	routeRules, _ := route["rules"].([]any)
	for _, r := range routeRules {
		if strings.Contains(bhJSON(t, r), "geosite-netflix") {
			t.Errorf("连接侧出现了出口不认识的行:%s", bhJSON(t, r))
		}
	}

	// 规则归属:只有「认识的那一行」有归属,index 仍是它在档案里的行号(1,被跳过的 netflix 是 0);
	// 前三条是本地域名 / 单标签 / HTTPS 预判,排在自定义行前面。
	if got, want := bhJSON(t, res["dnsRuleOwners"]), `[{"from":3,"index":1,"kind":"custom","name":"","to":4},{"from":4,"index":0,"kind":"directHosts","name":"","to":5},{"from":5,"index":0,"kind":"fallback","name":"其他","to":6}]`; got != want {
		t.Errorf("dnsRuleOwners = %s,想要 %s", got, want)
	}
}
