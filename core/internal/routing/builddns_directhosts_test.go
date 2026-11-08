package routing

import (
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/clientroutes"
)

// 自写测试向量:BuildDNS 分流开着、有订阅和节点站点直连(没有终端分流)时的那一块规则。
// 输入自己拟,期望值按 dns.go 的规则人工推算。这一块的规则:
//   - 有 DirectHostSets(部署形态):一条规则,条件是域名那份本地规则集,解析器 dns-direct;IP 那份 DNS 不用;
//   - 没有 DirectHostSets 但 DirectDomains 非空(测试形态):一条规则,条件是 domain 原样写进去;
//   - 两者都给时只用规则集那份;都没有时整块不生成,也不占规则归属;
//   - 解析器固定直连:不挂热切换开关、不带 FakeIP 占位和代理 v6 降级的 AAAA 空应答、不看站点集和当前选择、
//     不查规则集形状表、不拆条件;直连备用上游让它变成竞速段,代理备用上游不影响它;
//   - 位置:自定义分流之后、站点集之前(终端分流夹在中间,下一步再换);归属记 Kind=directHosts、Index=0、Name 为空;
//   - 不往 Resolvers 里登记任何东西。

const (
	dhDomTag = "obnode-direct-domain"
	dhIPTag  = "obnode-direct-ip"
)

func dhSet(tag string) obj { return obj{"rule_set": []string{tag}} }

func dhDom(ds ...string) obj { return obj{"domain": ds} }

func dhDirect(m obj) obj { return plMerge(m, obj{"server": "dns-direct"}) }

func dhOwn(from, to int) RuleOwner {
	return RuleOwner{From: from, To: to, Kind: "directHosts", Index: 0, Name: ""}
}

func dhSets() *DirectHostSets { return &DirectHostSets{Domain: dhDomTag, IP: dhIPTag} }

func TestBuildDNSSplitDirectHosts(t *testing.T) {
	sB := []any{dnsDirect("223.5.5.5"), skProxy}
	sFake := []any{dnsDirect("223.5.5.5"), skProxy, skFake4}
	sAlt := []any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"), skProxy}
	sAltP := []any{dnsDirect("223.5.5.5"), skProxy, obj{"type": "tcp", "tag": "dns-proxy__alt1", "server": "9.9.9.9", "detour": "其他"}}
	sAltBoth := []any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"), skProxy,
		obj{"type": "tcp", "tag": "dns-proxy__alt1", "server": "9.9.9.9", "detour": "其他"}}
	hijackS := []any{dnsDirect("223.5.5.5"), skProxy, dnsLocalServer}
	setRule := dhDirect(dhSet(dhDomTag))
	domRule := dhDirect(dhDom("sub.example.com", "node.example.net"))
	fbOnly := []RuleOwner{plFB(1, 2)}
	mediaPol := []any{obj{"id": "p-media", "name": "流媒体", "default": "美国", "domainSuffix": []any{"netflix.com"}}}
	qqRow := []any{cuRow("domainSuffix", "qq.com", "direct")}

	cases := []struct {
		name    string
		dns     obj
		top     obj
		routing obj
		o       DNSOptions
		wantS   []any
		wantR   []any
		strat   string
		custom  []any
		pmap    map[string]string
		owns    []RuleOwner
	}{
		// ---- 两种形态 ----
		{"规则集形态:一条域名那份规则集的直连规则,IP 那份 DNS 不用", skDM(nil), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, DirectHostSets: dhSets()}, sB,
			[]any{skHTTPS, setRule, skFbProxy}, "ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 2), plFB(2, 3)}},
		{"规则集形态:IP 那份可以是空串,照样只用域名那份", skDM(nil), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, DirectHostSets: &DirectHostSets{Domain: dhDomTag}}, sB,
			[]any{skHTTPS, setRule, skFbProxy}, "ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 2), plFB(2, 3)}},
		{"域名形态:domain 条件,域名原样带出", skDM(nil), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, DirectDomains: []string{"sub.example.com", "node.example.net"}}, sB,
			[]any{skHTTPS, domRule, skFbProxy}, "ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 2), plFB(2, 3)}},
		{"域名形态:不去重、不排序、不改大小写", skDM(nil), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectDomains: []string{"b.example", "A.example", "b.example"}}, sB,
			[]any{skHTTPS, dhDirect(dhDom("b.example", "A.example", "b.example")), skFbProxy}, "ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 2), plFB(2, 3)}},
		{"两种形态都给:只用规则集那份", skDM(nil), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectHostSets: dhSets(), DirectDomains: []string{"sub.example.com"}}, sB,
			[]any{skHTTPS, setRule, skFbProxy}, "ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 2), plFB(2, 3)}},
		{"两种形态都没有:整块不生成,不占规则归属", skDM(nil), nil, nil,
			DNSOptions{Builtin: defaultBuiltin}, sB,
			[]any{skHTTPS, skFbProxy}, "ipv4_only", nil, nil, fbOnly},
		{"域名表为空切片:同样不生成", skDM(nil), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectDomains: []string{}}, sB,
			[]any{skHTTPS, skFbProxy}, "ipv4_only", nil, nil, fbOnly},
		{"规则集 tag 在形状表里只有 IP 那份也照写:这一块不查形状表", skDM(nil), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectHostSets: dhSets(), RuleLists: ruleLists{dhDomTag: ruleShape{ip: true}}}, sB,
			[]any{skHTTPS, setRule, skFbProxy}, "ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 2), plFB(2, 3)}},

		// ---- 和别的块的顺序 ----
		{"劫持模式:本地主机名规则在前,下标整体后移", obj{}, nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectHostSets: dhSets()}, hijackS,
			[]any{dnsLocalRules[0], dnsLocalRules[1], skHTTPS, setRule, skFbProxy}, "ipv4_only", nil, nil, []RuleOwner{dhOwn(3, 4), plFB(4, 5)}},
		{"域名过滤规则排在它前面", skDM(nil), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectHostSets: dhSets(), FilterRules: []any{skFilter}}, sB,
			[]any{skHTTPS, skFilter, setRule, skFbProxy}, "ipv4_only", nil, nil, []RuleOwner{dhOwn(2, 3), plFB(3, 4)}},
		{"前置自定义分流在前、站点集在后;三块的规则归属各管各的,Resolvers 里没有它的位置",
			skDM(nil), nil, obj{"custom": obj{"enabled": true, "rules": qqRow}, "policies": mediaPol},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, DirectHostSets: dhSets()}, sB,
			plCat([]any{skHTTPS, cuDirect(cuM("domain_suffix", "qq.com")), setRule}, plPair(obj{"domain_suffix": []string{"netflix.com"}}, flMedia), []any{skFbProxy}), "ipv4_only",
			[]any{"dns-direct"}, map[string]string{"流媒体": "dns-proxy"},
			[]RuleOwner{cuOwn(1, 2, 0), dhOwn(2, 3), plOwn(3, 5, 0, "流媒体"), plFB(5, 6)}},
		{"自定义分流停用时它不出现,这一块紧跟骨架", skDM(nil), nil, obj{"custom": obj{"enabled": false, "rules": qqRow}},
			DNSOptions{Builtin: defaultBuiltin, DirectDomains: []string{"sub.example.com"}}, sB,
			[]any{skHTTPS, dhDirect(dhDom("sub.example.com")), skFbProxy}, "ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 2), plFB(2, 3)}},

		// ---- FakeIP / IPv6 降级:不影响这一块,只影响兜底段 ----
		{"FakeIP:直连规则前后都没有占位规则,占位只在兜底段", skDM(obj{"fakeIpForProxy": true}), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectHostSets: dhSets()}, sFake,
			[]any{skHTTPS, setRule, skFakeRuleA, skFbProxy}, "ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 2), plFB(2, 4)}},
		{"代理 v6 降为 IPv4 + FakeIP:这一块仍是单条直连规则", skDM(obj{"fakeIpForProxy": true}), obj{"ipv6": true, "ipv6Proxy": "ipv4"}, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectDomains: []string{"sub.example.com"}}, sFake,
			[]any{skHTTPS, dhDirect(dhDom("sub.example.com")), skEmptyAAAA, skFakeRuleA, skFbProxy}, "prefer_ipv4", nil, nil, []RuleOwner{dhOwn(1, 2), plFB(2, 5)}},

		// ---- 备用上游竞速:只看直连侧 ----
		{"直连备用:这一块变成竞速段(占 race-0),兜底末尾的直连竞速占 race-1", skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}}), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectHostSets: dhSets()}, sAlt,
			plCat([]any{skHTTPS}, plRaceD(dhSet(dhDomTag), 0), []any{skFbProxy,
				skEval(nil, "dns-direct", "race-1-0"), skEval(nil, "dns-direct__alt1", "race-1-1"), skRespond("race-1-0"), skRespond("race-1-1")}),
			"ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 6), plFB(6, 11)}},
		{"直连备用 + 域名形态:竞速段里的条件是 domain", skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}}), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectDomains: []string{"sub.example.com"}}, sAlt,
			plCat([]any{skHTTPS}, plRaceD(dhDom("sub.example.com"), 0), []any{skFbProxy,
				skEval(nil, "dns-direct", "race-1-0"), skEval(nil, "dns-direct__alt1", "race-1-1"), skRespond("race-1-0"), skRespond("race-1-1")}),
			"ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 6), plFB(6, 11)}},
		{"只有代理备用:这一块仍是普通直连规则,竞速只在兜底代理支(race-0)", skDM(obj{"proxyExtras": []any{obj{"server": "9.9.9.9"}}}), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectHostSets: dhSets()}, sAltP,
			[]any{skHTTPS, setRule,
				skEval(skSwitch(), "dns-proxy", "race-0-0"), skEval(skSwitch(), "dns-proxy__alt1", "race-0-1"), skRespond("race-0-0"), skRespond("race-0-1"), skFbProxy},
			"ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 2), plFB(2, 7)}},
		{"两侧都有备用:这一块 race-0,兜底代理 race-1,兜底末尾直连 race-2,序号不撞",
			skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}, "proxyExtras": []any{obj{"server": "9.9.9.9"}}}), nil, nil,
			DNSOptions{Builtin: defaultBuiltin, DirectHostSets: dhSets()}, sAltBoth,
			plCat([]any{skHTTPS}, plRaceD(dhSet(dhDomTag), 0), []any{
				skEval(skSwitch(), "dns-proxy", "race-1-0"), skEval(skSwitch(), "dns-proxy__alt1", "race-1-1"), skRespond("race-1-0"), skRespond("race-1-1"), skFbProxy,
				skEval(nil, "dns-direct", "race-2-0"), skEval(nil, "dns-direct__alt1", "race-2-1"), skRespond("race-2-0"), skRespond("race-2-1")}),
			"ipv4_only", nil, nil, []RuleOwner{dhOwn(1, 6), plFB(6, 15)}},
	}
	for _, c := range cases {
		top := plMerge(c.top, nil)
		if c.routing != nil {
			top["routing"] = c.routing
		}
		res, err := BuildDNS(skProfile(c.dns, top), c.o)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		want := map[string]any{"servers": c.wantS, "rules": c.wantR, "final": "dns-direct", "strategy": c.strat, "reverse_mapping": true}
		if !reflect.DeepEqual(res.DNS, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, res.DNS, want)
		}
		custom, pmap := c.custom, c.pmap
		if custom == nil {
			custom = []any{}
		}
		if pmap == nil {
			pmap = map[string]string{}
		}
		wantRes := Resolvers{Policies: pmap, Custom: custom, Clients: []ClientResolver{}, Fallback: "dns-direct"}
		if !reflect.DeepEqual(res.Resolvers, wantRes) {
			t.Errorf("%s:Resolvers = %#v,想要 %#v", c.name, res.Resolvers, wantRes)
		}
		if !reflect.DeepEqual(res.RuleOwners, c.owns) {
			t.Errorf("%s:RuleOwners = %#v,想要 %#v", c.name, res.RuleOwners, c.owns)
		}
	}
}

// 兜底开关 / 站点集都不影响这一块:站点集此刻选直连、兜底改走直连,直连规则仍是单条无开关的规则。
func TestBuildDNSSplitDirectHostsIgnoresSwitches(t *testing.T) {
	routing := obj{"policies": []any{obj{"id": "p-media", "name": "流媒体", "default": "direct", "domainSuffix": []any{"netflix.com"}}}}
	o := DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, DirectHostSets: dhSets(),
		Selections: map[string]any{"流媒体": "direct", "其他": "direct"}}
	res, err := BuildDNS(skProfile(skDM(nil), obj{"routing": routing}), o)
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := res.DNS["rules"].([]any)
	if len(rules) < 2 || !reflect.DeepEqual(rules[1], dhDirect(dhSet(dhDomTag))) {
		t.Fatalf("rules = %#v", res.DNS["rules"])
	}
	if !reflect.DeepEqual(res.Resolvers.Policies, map[string]string{"流媒体": "dns-direct"}) || res.Resolvers.Fallback != "dns-direct" {
		t.Errorf("Resolvers = %#v", res.Resolvers)
	}
	if res.RuleOwners[0] != dhOwn(1, 2) {
		t.Errorf("RuleOwners = %#v", res.RuleOwners)
	}
}

// 不写穿入参:域名表和选项原样,两次调用结果相同。
func TestBuildDNSSplitDirectHostsDoesNotMutateInputs(t *testing.T) {
	domains := []string{"b.example", "a.example"}
	o := DNSOptions{Builtin: defaultBuiltin, DirectDomains: domains}
	profile := skProfile(skDM(nil), nil)
	first, err := BuildDNS(profile, o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(domains, []string{"b.example", "a.example"}) {
		t.Errorf("域名表被改了:%#v", domains)
	}
	second, err := BuildDNS(profile, o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("两次调用结果不同:\n%#v\n%#v", first, second)
	}
	if !reflect.DeepEqual(profile, skProfile(skDM(nil), nil)) {
		t.Errorf("profile 被改了:%#v", profile)
	}
}

// 和终端分流的相对位置(劫持模式,内核接得到每台终端的查询):「不进内核」的终端先答,排在前置自定义分流和这一块前面;
// 普通终端分流排在这一块后面。终端分流本身的向量在下一步里换,这里只钉位置。
func TestBuildDNSSplitDirectHostsAmongTerminals(t *testing.T) {
	sTerm := []any{dnsDirect("223.5.5.5"), skProxy, skDirectTag("223.5.5.5", "dns-terminal-direct"), dnsLocalServer}
	setRule := dhDirect(dhSet(dhDomTag))
	phone := clientroutes.Route{ID: "c1", Name: "手机", Match: "ip", Outbound: "direct", Sources: []string{"192.168.1.10/32"}}
	tv := clientroutes.Route{ID: "c2", Name: "电视", Match: "ip", Outbound: "direct", Bypass: true, Sources: []string{"192.168.1.20/32"}}
	termRule := func(src string) obj { return obj{"source_ip_cidr": []string{src}, "server": "dns-terminal-direct"} }

	cases := []struct {
		name  string
		route clientroutes.Route
		rules []any
		owns  []RuleOwner
		src   string
	}{
		{"普通终端分流排在这一块后面", phone,
			[]any{dnsLocalRules[0], dnsLocalRules[1], skHTTPS, setRule, termRule("192.168.1.10/32"), skFbProxy},
			[]RuleOwner{dhOwn(3, 4), {From: 4, To: 5, Kind: "client", Index: 0, Name: ""}, plFB(5, 6)}, "192.168.1.10/32"},
		{"「不进内核」的终端排在这一块前面", tv,
			[]any{dnsLocalRules[0], dnsLocalRules[1], skHTTPS, termRule("192.168.1.20/32"), setRule, skFbProxy},
			[]RuleOwner{{From: 3, To: 4, Kind: "client", Index: 0, Name: ""}, dhOwn(4, 5), plFB(5, 6)}, "192.168.1.20/32"},
	}
	for _, c := range cases {
		o := DNSOptions{Builtin: defaultBuiltin, DirectHostSets: dhSets(), ClientRoutes: []clientroutes.Route{c.route}}
		res, err := BuildDNS(skProfile(obj{}, nil), o)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		want := map[string]any{"servers": sTerm, "rules": c.rules, "final": "dns-direct", "strategy": "ipv4_only", "reverse_mapping": true}
		if !reflect.DeepEqual(res.DNS, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, res.DNS, want)
		}
		if !reflect.DeepEqual(res.RuleOwners, c.owns) {
			t.Errorf("%s:RuleOwners = %#v,想要 %#v", c.name, res.RuleOwners, c.owns)
		}
		wantClients := []ClientResolver{{Sources: []string{c.src}, Server: "dns-terminal-direct"}}
		if !reflect.DeepEqual(res.Resolvers.Clients, wantClients) {
			t.Errorf("%s:Clients = %#v,想要 %#v", c.name, res.Resolvers.Clients, wantClients)
		}
	}
}
