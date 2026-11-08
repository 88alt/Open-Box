package routing

import (
	"reflect"
	"testing"
)

// 自写测试向量:BuildDNS 分流开着、有生效的站点集(没有前置自定义分流、终端分流、订阅 / 节点站点直连)时的成对规则。
// 输入自己拟,期望值按 dns.go 的规则人工推算。每个站点集的域名类条件写成「代理支(包着开关)在前、直连支紧跟其后」,
// 站点集里规则集和域名条件并存时按 splitRuleSetConditions 拆成两段、每段各成一对;开关 tag 的十六进制值用独立 Python 的
// hashlib 对站点集 id 另算(sha256 前 12 位)。站点集此刻走哪边只影响 Resolvers.Policies,规则结构两支都写、不变。

const (
	flMedia = "obflip-95a5105a4cbc" // sha256("p-media")[:12]
	flWork  = "obflip-cd0119860948" // sha256("p-work")[:12]
	flPol0  = "obflip-f242f1627478" // sha256("policy-0")[:12]
	flPol1  = "obflip-a9abbb22710f" // sha256("policy-1")[:12]
	flDup2  = "obflip-35c639df758e" // sha256("p-dup2")[:12]
)

func plMerge(a, b obj) obj {
	out := obj{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func plFlag(flag string) obj { return obj{"rule_set": []string{flag}} }

// plPair:一段匹配条件的成对规则(没有备用上游、FakeIP、v6 降级时)。
func plPair(part obj, flag string) []any {
	return []any{
		skAnd(obj{"server": "dns-proxy"}, part, plFlag(flag)),
		plMerge(part, obj{"server": "dns-direct"}),
	}
}

// plRaceP / plRaceD:代理支 / 直连支带备用上游(各一个 alt1)时的竞速段;n 是这一段占的序号。
func plRaceP(part obj, flag string, n int) []any {
	t0, t1 := raceTag(n, 0), raceTag(n, 1)
	return []any{
		skAnd(obj{"action": "evaluate", "server": "dns-proxy", "tag": t0}, part, plFlag(flag)),
		skAnd(obj{"action": "evaluate", "server": "dns-proxy__alt1", "tag": t1}, part, plFlag(flag)),
		skRespond(t0), skRespond(t1),
		skAnd(obj{"server": "dns-proxy"}, part, plFlag(flag)),
	}
}

func plRaceD(part obj, n int) []any {
	t0, t1 := raceTag(n, 0), raceTag(n, 1)
	return []any{
		skEval(part, "dns-direct", t0), skEval(part, "dns-direct__alt1", t1),
		skRespond(t0), skRespond(t1),
		plMerge(part, obj{"server": "dns-direct"}),
	}
}

func raceTag(n, i int) string { return "race-" + string(rune('0'+n)) + "-" + string(rune('0'+i)) }

func plCat(parts ...[]any) []any {
	out := []any{}
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func plOwn(from, to, idx int, name string) RuleOwner {
	return RuleOwner{From: from, To: to, Kind: "policy", Index: idx, Name: name}
}

func plFB(from, to int) RuleOwner {
	return RuleOwner{From: from, To: to, Kind: "fallback", Index: 0, Name: "其他"}
}

func plRouting(policies ...obj) obj {
	l := []any{}
	for _, p := range policies {
		l = append(l, p)
	}
	return obj{"routing": obj{"policies": l}}
}

func plOpts() DNSOptions {
	return DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}}
}

func TestBuildDNSSplitPolicies(t *testing.T) {
	media := func(extra obj) obj {
		return plMerge(obj{"id": "p-media", "name": "流媒体", "default": "美国"}, extra)
	}
	work := func(extra obj) obj {
		return plMerge(obj{"id": "p-work", "name": "办公", "default": "direct"}, extra)
	}
	suffix := obj{"domain_suffix": []string{"netflix.com"}}
	sB := []any{dnsDirect("223.5.5.5"), skProxy}
	sFake := []any{dnsDirect("223.5.5.5"), skProxy, skFake4}
	sAlt := []any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"), skProxy}
	sAltP := []any{dnsDirect("223.5.5.5"), skProxy, obj{"type": "tcp", "tag": "dns-proxy__alt1", "server": "9.9.9.9", "detour": "其他"}}
	sAltBoth := []any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"), skProxy,
		obj{"type": "tcp", "tag": "dns-proxy__alt1", "server": "9.9.9.9", "detour": "其他"}}
	setDom := func(set, dom string) obj {
		return obj{"rulesets": []any{set}, "domain": []any{dom}}
	}
	partSet := func(set string) obj { return obj{"rule_set": []string{set}} }
	partDom := func(dom string) obj { return obj{"domain": []string{dom}} }

	cases := []struct {
		name  string
		dns   obj
		top   obj // 顶层:ipv6 等
		pols  []obj
		o     DNSOptions
		wantS []any
		wantR []any
		strat string
		pmap  map[string]string
		owns  []RuleOwner
	}{
		{"单个站点集、只有域名后缀、默认走节点组:代理支包开关在前,直连支在后;解析器记代理侧",
			skDM(nil), nil, []obj{media(obj{"domainSuffix": []any{"netflix.com"}})}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(suffix, flMedia), []any{skFbProxy}), "ipv4_only",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 3, 0, "流媒体"), plFB(3, 4)}},
		{"默认走直连:规则结构不变,解析器记直连侧",
			skDM(nil), nil, []obj{media(obj{"default": "direct", "domainSuffix": []any{"netflix.com"}})}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(suffix, flMedia), []any{skFbProxy}), "ipv4_only",
			map[string]string{"流媒体": "dns-direct"}, []RuleOwner{plOwn(1, 3, 0, "流媒体"), plFB(3, 4)}},
		{"域名 / 后缀 / 关键字三类一起:同一条规则里,不拆",
			skDM(nil), nil, []obj{media(obj{"domain": []any{"a.example"}, "domainSuffix": []any{"b.example"}, "domainKeyword": []any{"cdn"}})}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(obj{"domain": []string{"a.example"}, "domain_suffix": []string{"b.example"}, "domain_keyword": []string{"cdn"}}, flMedia), []any{skFbProxy}), "ipv4_only",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 3, 0, "流媒体"), plFB(3, 4)}},
		{"只有规则集:一条规则带 rule_set",
			skDM(nil), nil, []obj{media(obj{"rulesets": []any{"geosite-netflix"}})}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(partSet("geosite-netflix"), flMedia), []any{skFbProxy}), "ipv4_only",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 3, 0, "流媒体"), plFB(3, 4)}},
		{"规则集 + 域名并存:拆成两段,各自成对,规则集那段在前",
			skDM(nil), nil, []obj{media(setDom("geosite-netflix", "a.example"))}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(partSet("geosite-netflix"), flMedia), plPair(partDom("a.example"), flMedia), []any{skFbProxy}), "ipv4_only",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 5, 0, "流媒体"), plFB(5, 6)}},
		{"规则集里的 geoip-* 不进 DNS:只留 geosite 那段,再加域名那段",
			skDM(nil), nil, []obj{media(obj{"rulesets": []any{"geosite-a", "geoip-cn"}, "domain": []any{"x.example"}})}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(partSet("geosite-a"), flMedia), plPair(partDom("x.example"), flMedia), []any{skFbProxy}), "ipv4_only",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 5, 0, "流媒体"), plFB(5, 6)}},
		{"只有 geoip 规则集:没有域名类条件,整个站点集跳过,不登记解析器",
			skDM(nil), nil, []obj{media(obj{"rulesets": []any{"geoip-cn"}})}, plOpts(),
			sB, []any{skHTTPS, skFbProxy}, "ipv4_only",
			map[string]string{}, []RuleOwner{plFB(1, 2)}},
		{"前面有只含 IP 条件的站点集:被跳过,但后面站点集的下标仍按它在生效列表里的位置算",
			skDM(nil), nil, []obj{{"id": "p-ip", "name": "内网", "ipCidr": []any{"10.0.0.0/8"}}, media(obj{"domainSuffix": []any{"netflix.com"}})}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(suffix, flMedia), []any{skFbProxy}), "ipv4_only",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 3, 1, "流媒体"), plFB(3, 4)}},
		{"两个站点集:按列表顺序各占一段,各自的默认出口决定解析器",
			skDM(nil), nil, []obj{media(obj{"domainSuffix": []any{"netflix.com"}}), work(obj{"domain": []any{"corp.example"}})}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(suffix, flMedia), plPair(partDom("corp.example"), flWork), []any{skFbProxy}), "ipv4_only",
			map[string]string{"流媒体": "dns-proxy", "办公": "dns-direct"}, []RuleOwner{plOwn(1, 3, 0, "流媒体"), plOwn(3, 5, 1, "办公"), plFB(5, 6)}},
		{"停用的站点集不出现",
			skDM(nil), nil, []obj{media(obj{"enabled": false, "domainSuffix": []any{"netflix.com"}}), work(obj{"domain": []any{"corp.example"}})}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(partDom("corp.example"), flWork), []any{skFbProxy}), "ipv4_only",
			map[string]string{"办公": "dns-direct"}, []RuleOwner{plOwn(1, 3, 0, "办公"), plFB(3, 4)}},
		{"没写 id 的站点集:开关 tag 按「policy-原始下标」算(第 0 个)",
			skDM(nil), nil, []obj{{"name": "无ID", "default": "美国", "domain": []any{"x.example"}}}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(partDom("x.example"), flPol0), []any{skFbProxy}), "ipv4_only",
			map[string]string{"无ID": "dns-proxy"}, []RuleOwner{plOwn(1, 3, 0, "无ID"), plFB(3, 4)}},
		{"没写 id 的站点集在原始列表第 1 位(前一个因没有条件被丢掉):按 policy-1 算",
			skDM(nil), nil, []obj{{"id": "x"}, {"name": "无ID", "default": "美国", "domain": []any{"x.example"}}}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(partDom("x.example"), flPol1), []any{skFbProxy}), "ipv4_only",
			map[string]string{"无ID": "dns-proxy"}, []RuleOwner{plOwn(1, 3, 0, "无ID"), plFB(3, 4)}},
		{"两个同名站点集:开关表按名字存,后写的赢,两段都用后一个的开关(JS Map 语义)",
			skDM(nil), nil, []obj{
				{"id": "p-dup1", "name": "同名", "default": "美国", "domain": []any{"a.example"}},
				{"id": "p-dup2", "name": "同名", "default": "美国", "domain": []any{"b.example"}}}, plOpts(),
			sB, plCat([]any{skHTTPS}, plPair(partDom("a.example"), flDup2), plPair(partDom("b.example"), flDup2), []any{skFbProxy}), "ipv4_only",
			map[string]string{"同名": "dns-proxy"}, []RuleOwner{plOwn(1, 3, 0, "同名"), plOwn(3, 5, 1, "同名"), plFB(5, 6)}},
		{"叫「其他」的站点集被当成兜底的同名而丢掉",
			skDM(nil), nil, []obj{media(obj{"name": "其他", "domainSuffix": []any{"netflix.com"}})}, plOpts(),
			sB, []any{skHTTPS, skFbProxy}, "ipv4_only",
			map[string]string{}, []RuleOwner{plFB(1, 2)}},
		{"劫持模式:本地主机名规则在前,下标整体后移;本地服务器仍排最后",
			obj{}, nil, []obj{media(obj{"domainSuffix": []any{"netflix.com"}})}, plOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, dnsLocalServer},
			plCat([]any{dnsLocalRules[0], dnsLocalRules[1], skHTTPS}, plPair(suffix, flMedia), []any{skFbProxy}), "ipv4_only",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(3, 5, 0, "流媒体"), plFB(5, 6)}},

		// ---- FakeIP / IPv6 降级:每段匹配都带 ----
		{"FakeIP:占位规则(挂本站点集的开关、只管 A)排在代理支前面",
			skDM(obj{"fakeIpForProxy": true}), nil, []obj{media(obj{"domainSuffix": []any{"netflix.com"}})}, plOpts(),
			sFake,
			plCat([]any{skHTTPS, skAnd(obj{"server": "dns-fakeip"}, suffix, plFlag(flMedia), obj{"query_type": []string{"A"}})}, plPair(suffix, flMedia), []any{skFakeRuleA, skFbProxy}), "ipv4_only",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 4, 0, "流媒体"), plFB(4, 6)}},
		{"FakeIP + IPv6 交给节点:站点集的占位规则管 A 和 AAAA,占位服务带 v6 段",
			skDM(obj{"fakeIpForProxy": true}), obj{"ipv6": true}, []obj{media(obj{"domainSuffix": []any{"netflix.com"}})}, plOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, skFake46},
			plCat([]any{skHTTPS, skAnd(obj{"server": "dns-fakeip"}, suffix, plFlag(flMedia), obj{"query_type": []string{"A", "AAAA"}})}, plPair(suffix, flMedia), []any{skFakeRuleAA, skFbProxy}), "prefer_ipv4",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 4, 0, "流媒体"), plFB(4, 6)}},
		{"代理 v6 降为 IPv4:AAAA 空应答(挂本站点集的开关)排在最前",
			skDM(nil), obj{"ipv6": true, "ipv6Proxy": "ipv4"}, []obj{media(obj{"domainSuffix": []any{"netflix.com"}})}, plOpts(),
			sB,
			plCat([]any{skHTTPS, skAnd(obj{"action": "predefined", "rcode": "NOERROR"}, suffix, plFlag(flMedia), obj{"query_type": []string{"AAAA"}})}, plPair(suffix, flMedia), []any{skEmptyAAAA, skFbProxy}), "prefer_ipv4",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 4, 0, "流媒体"), plFB(4, 6)}},
		{"FakeIP + 降 IPv4 + 拆成两段:每段各自 AAAA 空应答 → 占位 → 代理支 → 直连支",
			skDM(obj{"fakeIpForProxy": true}), obj{"ipv6": true, "ipv6Proxy": "ipv4"}, []obj{media(setDom("geosite-netflix", "a.example"))}, plOpts(),
			sFake,
			plCat([]any{skHTTPS},
				[]any{skAnd(obj{"action": "predefined", "rcode": "NOERROR"}, partSet("geosite-netflix"), plFlag(flMedia), obj{"query_type": []string{"AAAA"}}),
					skAnd(obj{"server": "dns-fakeip"}, partSet("geosite-netflix"), plFlag(flMedia), obj{"query_type": []string{"A"}})},
				plPair(partSet("geosite-netflix"), flMedia),
				[]any{skAnd(obj{"action": "predefined", "rcode": "NOERROR"}, partDom("a.example"), plFlag(flMedia), obj{"query_type": []string{"AAAA"}}),
					skAnd(obj{"server": "dns-fakeip"}, partDom("a.example"), plFlag(flMedia), obj{"query_type": []string{"A"}})},
				plPair(partDom("a.example"), flMedia),
				[]any{skEmptyAAAA, skFakeRuleA, skFbProxy}), "prefer_ipv4",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 9, 0, "流媒体"), plFB(9, 12)}},

		// ---- 备用上游竞速:序号按「每次竞速」递增,整份配置不撞 ----
		{"代理备用:代理支变成竞速段;两个站点集占 race-0、race-1,兜底占 race-2",
			skDM(obj{"proxyExtras": []any{obj{"server": "9.9.9.9"}}}), nil, []obj{media(obj{"domainSuffix": []any{"netflix.com"}}), work(obj{"domain": []any{"corp.example"}})}, plOpts(),
			sAltP,
			plCat([]any{skHTTPS},
				plRaceP(suffix, flMedia, 0), []any{plMerge(suffix, obj{"server": "dns-direct"})},
				plRaceP(partDom("corp.example"), flWork, 1), []any{plMerge(partDom("corp.example"), obj{"server": "dns-direct"})},
				[]any{skEval(skSwitch(), "dns-proxy", "race-2-0"), skEval(skSwitch(), "dns-proxy__alt1", "race-2-1"), skRespond("race-2-0"), skRespond("race-2-1"), skFbProxy}),
			"ipv4_only",
			map[string]string{"流媒体": "dns-proxy", "办公": "dns-direct"}, []RuleOwner{plOwn(1, 7, 0, "流媒体"), plOwn(7, 13, 1, "办公"), plFB(13, 18)}},
		{"直连备用:直连支变成竞速段;兜底末尾的无条件直连竞速接在后面",
			skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}}), nil, []obj{media(obj{"domainSuffix": []any{"netflix.com"}})}, plOpts(),
			sAlt,
			plCat([]any{skHTTPS}, []any{skAnd(obj{"server": "dns-proxy"}, suffix, plFlag(flMedia))}, plRaceD(suffix, 0),
				[]any{skFbProxy, skEval(nil, "dns-direct", "race-1-0"), skEval(nil, "dns-direct__alt1", "race-1-1"), skRespond("race-1-0"), skRespond("race-1-1")}),
			"ipv4_only",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 7, 0, "流媒体"), plFB(7, 12)}},
		{"两侧都有备用、站点集拆成两段:每段的代理竞速、直连竞速各占一个序号(0、1、2、3),兜底 4、5",
			skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}, "proxyExtras": []any{obj{"server": "9.9.9.9"}}}), nil, []obj{media(setDom("geosite-netflix", "a.example"))}, plOpts(),
			sAltBoth,
			plCat([]any{skHTTPS},
				plRaceP(partSet("geosite-netflix"), flMedia, 0), plRaceD(partSet("geosite-netflix"), 1),
				plRaceP(partDom("a.example"), flMedia, 2), plRaceD(partDom("a.example"), 3),
				[]any{skEval(skSwitch(), "dns-proxy", "race-4-0"), skEval(skSwitch(), "dns-proxy__alt1", "race-4-1"), skRespond("race-4-0"), skRespond("race-4-1"), skFbProxy,
					skEval(nil, "dns-direct", "race-5-0"), skEval(nil, "dns-direct__alt1", "race-5-1"), skRespond("race-5-0"), skRespond("race-5-1")}),
			"ipv4_only",
			map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{plOwn(1, 21, 0, "流媒体"), plFB(21, 30)}},
	}
	for _, c := range cases {
		pols := []any{}
		for _, p := range c.pols {
			pols = append(pols, p)
		}
		res, err := BuildDNS(skProfile(c.dns, plMerge(c.top, obj{"routing": obj{"policies": pols}})), c.o)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		want := map[string]any{"servers": c.wantS, "rules": c.wantR, "final": "dns-direct", "strategy": c.strat, "reverse_mapping": true}
		if !reflect.DeepEqual(res.DNS, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, res.DNS, want)
		}
		wantRes := Resolvers{Policies: c.pmap, Custom: []any{}, Clients: []ClientResolver{}, Fallback: "dns-direct"}
		if !reflect.DeepEqual(res.Resolvers, wantRes) {
			t.Errorf("%s:Resolvers = %#v", c.name, res.Resolvers)
		}
		if !reflect.DeepEqual(res.RuleOwners, c.owns) {
			t.Errorf("%s:RuleOwners = %#v,想要 %#v", c.name, res.RuleOwners, c.owns)
		}
	}
}

// 站点集此刻走直连还是走代理(Resolvers.Policies):当前选择 > 默认 > 成员第一项;规则结构不随它变。
func TestBuildDNSSplitPoliciesResolver(t *testing.T) {
	pol := func(def string) obj {
		return obj{"id": "p-media", "name": "流媒体", "default": def, "domain": []any{"a.example"}}
	}
	cases := []struct {
		name string
		def  string
		o    DNSOptions
		want string
	}{
		{"默认写节点组", "美国", plOpts(), "dns-proxy"},
		{"默认写 direct", "direct", plOpts(), "dns-direct"},
		{"默认空、成员第一项是节点组", "", DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"美国", "direct"}}, "dns-proxy"},
		{"默认空、成员第一项是直连", "", plOpts(), "dns-direct"},
		{"默认写 proxy:取第一个非内置成员", "proxy", plOpts(), "dns-proxy"},
		{"默认指向不存在的组:落成员第一项(直连)", "已删除", plOpts(), "dns-direct"},
		{"没传组:成员只有直连", "美国", DNSOptions{Builtin: defaultBuiltin}, "dns-direct"},
		{"当前选择直连压过默认节点组", "美国", DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"流媒体": "direct"}}, "dns-direct"},
		{"当前选择节点组压过默认直连", "direct", DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"流媒体": "美国"}}, "dns-proxy"},
		{"选择顺着 selector 链下钻:美国 → 直连", "美国", DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"流媒体": "美国", "美国": "direct"}}, "dns-direct"},
		{"选择值不是字符串", "direct", DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"流媒体": 1.0}}, "dns-proxy"},
		{"别的名字的选择不影响它", "美国", DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"办公": "direct"}}, "dns-proxy"},
		{"直连被停用:默认 direct 落到第一项节点组", "direct", DNSOptions{Builtin: builtinTags{"direct", "block", false, true}, GroupTags: []string{"direct", "美国"}}, "dns-proxy"},
		{"内置直连改了名:默认 direct 指向改名后的直连", "direct", DNSOptions{Builtin: builtinTags{"直连", "拒绝", true, true}, GroupTags: []string{"美国", "直连"}}, "dns-direct"},
	}
	wantRules := plCat([]any{skHTTPS}, plPair(obj{"domain": []string{"a.example"}}, flMedia), []any{skFbProxy})
	for _, c := range cases {
		res, err := BuildDNS(skProfile(skDM(nil), obj{"routing": obj{"policies": []any{pol(c.def)}}}), c.o)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		if got := res.Resolvers.Policies["流媒体"]; got != c.want {
			t.Errorf("%s:Policies[流媒体] = %q,想要 %q", c.name, got, c.want)
		}
		if !reflect.DeepEqual(res.DNS["rules"], wantRules) {
			t.Errorf("%s:规则变了:%#v", c.name, res.DNS["rules"])
		}
	}
}

// 规则集链接(list-xxxx)按部署时的形状表过滤:DNS 只要域名那份。
func TestBuildDNSSplitPoliciesRuleListShapes(t *testing.T) {
	const tag = "list-aabbccdd"
	pol := func(extra obj) obj {
		return plMerge(obj{"id": "p-media", "name": "流媒体", "default": "美国", "rulesets": []any{tag}}, extra)
	}
	sets := obj{"rule_set": []string{tag}}
	cases := []struct {
		name  string
		p     obj
		l     ruleLists
		want  []any // 站点集那段(含 https 之后、兜底之前);nil = 整个跳过
		owner []RuleOwner
	}{
		{"形状表没这一项(旧部署):照老样子引用",
			pol(nil), ruleLists{}, plPair(sets, flMedia), []RuleOwner{plOwn(1, 3, 0, "流媒体"), plFB(3, 4)}},
		{"域名 + IP 两份都有:DNS 只引用域名那份(同一个 tag)",
			pol(nil), ruleLists{tag: ruleShape{domain: true, ip: true}}, plPair(sets, flMedia), []RuleOwner{plOwn(1, 3, 0, "流媒体"), plFB(3, 4)}},
		{"只有 IP 那份:没有域名类条件,整个站点集跳过",
			pol(nil), ruleLists{tag: ruleShape{ip: true}}, nil, []RuleOwner{plFB(1, 2)}},
		{"只有 IP 那份 + 手写域名:只剩域名条件,不再带 rule_set,不拆",
			pol(obj{"domain": []any{"x.example"}}), ruleLists{tag: ruleShape{ip: true}}, plPair(obj{"domain": []string{"x.example"}}, flMedia), []RuleOwner{plOwn(1, 3, 0, "流媒体"), plFB(3, 4)}},
		{"链接 + geoip + 域名:geoip 不进,链接那份与域名拆成两段",
			pol(obj{"rulesets": []any{tag, "geoip-cn"}, "domain": []any{"x.example"}}), ruleLists{tag: ruleShape{domain: true}},
			plCat(plPair(sets, flMedia), plPair(obj{"domain": []string{"x.example"}}, flMedia)), []RuleOwner{plOwn(1, 5, 0, "流媒体"), plFB(5, 6)}},
	}
	for _, c := range cases {
		o := plOpts()
		o.RuleLists = c.l
		res, err := BuildDNS(skProfile(skDM(nil), obj{"routing": obj{"policies": []any{c.p}}}), o)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		wantRules := plCat([]any{skHTTPS}, c.want, []any{skFbProxy})
		if !reflect.DeepEqual(res.DNS["rules"], wantRules) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, res.DNS["rules"], wantRules)
		}
		if !reflect.DeepEqual(res.RuleOwners, c.owner) {
			t.Errorf("%s:RuleOwners = %#v", c.name, res.RuleOwners)
		}
	}
}
