package routing

import (
	"reflect"
	"testing"
)

// 自写测试向量:BuildDNS 分流开着、有前置自定义分流(没有终端分流、订阅 / 节点站点直连)时的那一块规则。
// 输入自己拟,期望值按 dns.go 的规则人工推算。自定义分流每一行的出口是设置里定死的(不挂热切换开关):
// 出口直连 → 一条直连解析规则;出口拒绝 → 一条 reject;其余(节点组、查不到的名字)→ 代理侧解析器(带 AAAA 空应答 / FakeIP 占位)。
// 只按 IP / 端口分流的行、geoip 行、DNS 里用不上的规则集不写规则,但仍在 Resolvers.Custom 里占一个 null,保持和行下标对齐。
// 规则集链接 tag 的 FNV-1a 值用独立 Python 另算:https://example.com/a.srs → list-b99d2d1f,https://example.com/ads.srs → list-5a070e3e。

const (
	urlA   = "https://example.com/a.srs"
	urlAds = "https://example.com/ads.srs"
	tagA   = "list-b99d2d1f"
	tagAds = "list-5a070e3e"
)

func cuRow(typ, val, out string) obj { return obj{"type": typ, "value": val, "outbound": out} }

func cuM(k, v string) obj { return obj{k: []string{v}} }

func cuOwn(from, to, idx int) RuleOwner {
	return RuleOwner{From: from, To: to, Kind: "custom", Index: idx, Name: ""}
}

func cuReject(m obj) obj { return plMerge(m, obj{"action": "reject"}) }

func cuDirect(m obj) obj { return plMerge(m, obj{"server": "dns-direct"}) }

func cuProxy(m obj) obj { return plMerge(m, obj{"server": "dns-proxy"}) }

func cuFake(m obj, types ...string) obj {
	return plMerge(m, obj{"query_type": types, "server": "dns-fakeip"})
}

func cuAAAA(m obj) obj {
	return plMerge(m, obj{"query_type": []string{"AAAA"}, "action": "predefined", "rcode": "NOERROR"})
}

// cuRaceP:出口是节点组的行带代理备用上游时的竞速段(没有开关);n 是序号。
func cuRaceP(m obj, n int) []any {
	t0, t1 := raceTag(n, 0), raceTag(n, 1)
	return []any{skEval(m, "dns-proxy", t0), skEval(m, "dns-proxy__alt1", t1), skRespond(t0), skRespond(t1), cuProxy(m)}
}

func TestBuildDNSSplitCustom(t *testing.T) {
	sB := []any{dnsDirect("223.5.5.5"), skProxy}
	sFake := []any{dnsDirect("223.5.5.5"), skProxy, skFake4}
	sFake46 := []any{dnsDirect("223.5.5.5"), skProxy, skFake46}
	sAlt := []any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"), skProxy}
	sAltP := []any{dnsDirect("223.5.5.5"), skProxy, obj{"type": "tcp", "tag": "dns-proxy__alt1", "server": "9.9.9.9", "detour": "其他"}}
	sAltBoth := []any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"), skProxy,
		obj{"type": "tcp", "tag": "dns-proxy__alt1", "server": "9.9.9.9", "detour": "其他"}}
	qq, ads, yt := cuM("domain_suffix", "qq.com"), cuM("domain", "ads.example"), cuM("domain_keyword", "youtube")
	rQQ, rAds, rYT := cuRow("domainSuffix", "qq.com", "direct"), cuRow("domain", "ads.example", "block"), cuRow("domainKeyword", "youtube", "美国")
	fbOnly := []RuleOwner{plFB(1, 2)}
	renamed := DNSOptions{Builtin: builtinTags{"直连", "拒绝", true, true}, GroupTags: []string{"直连", "美国"}}

	cases := []struct {
		name   string
		dns    obj
		top    obj
		rows   []any
		pols   []any
		o      DNSOptions
		wantS  []any
		wantR  []any
		strat  string
		custom []any
		pmap   map[string]string
		owns   []RuleOwner
	}{
		// ---- 各种出口 ----
		{"出口直连:一条直连解析规则", skDM(nil), nil, []any{rQQ}, nil, plOpts(), sB,
			[]any{skHTTPS, cuDirect(qq), skFbProxy}, "ipv4_only", []any{"dns-direct"}, nil, []RuleOwner{cuOwn(1, 2, 0), plFB(2, 3)}},
		{"出口拒绝:reject 规则,解析器记 null", skDM(nil), nil, []any{rAds}, nil, plOpts(), sB,
			[]any{skHTTPS, cuReject(ads), skFbProxy}, "ipv4_only", []any{nil}, nil, []RuleOwner{cuOwn(1, 2, 0), plFB(2, 3)}},
		{"出口节点组:代理侧解析器(没有开关)", skDM(nil), nil, []any{rYT}, nil, plOpts(), sB,
			[]any{skHTTPS, cuProxy(yt), skFbProxy}, "ipv4_only", []any{"dns-proxy"}, nil, []RuleOwner{cuOwn(1, 2, 0), plFB(2, 3)}},
		{"出口是查不到的名字:同样按代理侧(这里不核对出站是否存在)", skDM(nil), nil, []any{cuRow("domainSuffix", "x.example", "不存在的组")}, nil, plOpts(), sB,
			[]any{skHTTPS, cuProxy(cuM("domain_suffix", "x.example")), skFbProxy}, "ipv4_only", []any{"dns-proxy"}, nil, []RuleOwner{cuOwn(1, 2, 0), plFB(2, 3)}},
		{"值和出口前后空白会被去掉", skDM(nil), nil, []any{cuRow("domainSuffix", "  qq.com ", " direct ")}, nil, plOpts(), sB,
			[]any{skHTTPS, cuDirect(qq), skFbProxy}, "ipv4_only", []any{"dns-direct"}, nil, []RuleOwner{cuOwn(1, 2, 0), plFB(2, 3)}},
		{"当前选择不影响自定义行:节点组此刻选了直连,解析器仍记代理侧", skDM(nil), nil, []any{rYT}, nil,
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"美国": "direct"}}, sB,
			[]any{skHTTPS, cuProxy(yt), skFbProxy}, "ipv4_only", []any{"dns-proxy"}, nil, []RuleOwner{cuOwn(1, 2, 0), plFB(2, 3)}},

		// ---- 内置出站改名 ----
		{"内置直连 / 拒绝改了名:direct / block 记号与改名后的名字都认,别的名字当节点组", skDM(nil), nil, []any{
			cuRow("domainSuffix", "a.example", "direct"), cuRow("domainSuffix", "b.example", "block"),
			cuRow("domainSuffix", "c.example", "直连"), cuRow("domainSuffix", "d.example", "拒绝"), cuRow("domainSuffix", "e.example", "美国")}, nil,
			renamed, sB,
			[]any{skHTTPS, cuDirect(cuM("domain_suffix", "a.example")), cuReject(cuM("domain_suffix", "b.example")),
				cuDirect(cuM("domain_suffix", "c.example")), cuReject(cuM("domain_suffix", "d.example")), cuProxy(cuM("domain_suffix", "e.example")), skFbProxy},
			"ipv4_only", []any{"dns-direct", nil, "dns-direct", nil, "dns-proxy"}, nil,
			[]RuleOwner{cuOwn(1, 2, 0), cuOwn(2, 3, 1), cuOwn(3, 4, 2), cuOwn(4, 5, 3), cuOwn(5, 6, 4), plFB(6, 7)}},
		{"没改名时「直连」「拒绝」这两个字只是普通名字,按节点组", skDM(nil), nil, []any{cuRow("domainSuffix", "c.example", "直连"), cuRow("domainSuffix", "d.example", "拒绝")}, nil,
			plOpts(), sB,
			[]any{skHTTPS, cuProxy(cuM("domain_suffix", "c.example")), cuProxy(cuM("domain_suffix", "d.example")), skFbProxy},
			"ipv4_only", []any{"dns-proxy", "dns-proxy"}, nil, []RuleOwner{cuOwn(1, 2, 0), cuOwn(2, 3, 1), plFB(3, 4)}},

		// ---- 匹配类型 ----
		{"geosite 行:引用 geosite-xxx 规则集", skDM(nil), nil, []any{cuRow("geosite", "netflix", "美国")}, nil, plOpts(), sB,
			[]any{skHTTPS, cuProxy(obj{"rule_set": []string{"geosite-netflix"}}), skFbProxy}, "ipv4_only", []any{"dns-proxy"}, nil, []RuleOwner{cuOwn(1, 2, 0), plFB(2, 3)}},
		{"geoip 行:DNS 里用不上,不写规则,占一个 null", skDM(nil), nil, []any{cuRow("geoip", "cn", "direct")}, nil, plOpts(), sB,
			[]any{skHTTPS, skFbProxy}, "ipv4_only", []any{nil}, nil, fbOnly},
		{"ipCidr 行、port 行:同样不进 DNS,各占一个 null", skDM(nil), nil, []any{cuRow("ipCidr", "10.0.0.0/8", "美国"), cuRow("port", "443", "direct")}, nil, plOpts(), sB,
			[]any{skHTTPS, skFbProxy}, "ipv4_only", []any{nil, nil}, nil, fbOnly},
		{"ruleset 行:geosite 的写规则;geoip 的被滤掉成 null", skDM(nil), nil, []any{cuRow("ruleset", "geosite-cn", "direct"), cuRow("ruleset", "geoip-cn", "direct")}, nil, plOpts(), sB,
			[]any{skHTTPS, cuDirect(obj{"rule_set": []string{"geosite-cn"}}), skFbProxy}, "ipv4_only", []any{"dns-direct", nil}, nil, []RuleOwner{cuOwn(1, 2, 0), plFB(2, 3)}},
		{"ruleUrl 行:引用链接编出来的 list-xxxx;出口拒绝也一样", skDM(nil), nil, []any{cuRow("ruleUrl", urlA, "美国"), cuRow("ruleUrl", urlAds, "block")}, nil, plOpts(), sB,
			[]any{skHTTPS, cuProxy(obj{"rule_set": []string{tagA}}), cuReject(obj{"rule_set": []string{tagAds}}), skFbProxy},
			"ipv4_only", []any{"dns-proxy", nil}, nil, []RuleOwner{cuOwn(1, 2, 0), cuOwn(2, 3, 1), plFB(3, 4)}},
		{"ruleUrl 行遇到形状表:只有 IP 那份的不进 DNS(null),域名 + IP 都有的引用同一个 tag", skDM(nil), nil, []any{cuRow("ruleUrl", urlA, "美国"), cuRow("ruleUrl", urlAds, "direct")}, nil,
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, RuleLists: ruleLists{tagA: ruleShape{ip: true}, tagAds: ruleShape{domain: true, ip: true}}}, sB,
			[]any{skHTTPS, cuDirect(obj{"rule_set": []string{tagAds}}), skFbProxy}, "ipv4_only", []any{nil, "dns-direct"}, nil, []RuleOwner{cuOwn(1, 2, 1), plFB(2, 3)}},

		// ---- 行的取舍与下标 ----
		{"混排:没写规则的行不占规则归属,但占 Resolvers.Custom 的位置和后面行的下标", skDM(nil), nil,
			[]any{rQQ, cuRow("geoip", "cn", "美国"), rAds, rYT, cuRow("ipCidr", "10.0.0.0/8", "direct")}, nil, plOpts(), sB,
			[]any{skHTTPS, cuDirect(qq), cuReject(ads), cuProxy(yt), skFbProxy}, "ipv4_only",
			[]any{"dns-direct", nil, nil, "dns-proxy", nil}, nil, []RuleOwner{cuOwn(1, 2, 0), cuOwn(2, 3, 2), cuOwn(3, 4, 3), plFB(4, 5)}},
		{"写错的行在整理时就被丢掉,不占下标:类型不认、值为空、端口写错、链接不是 http、不是对象", skDM(nil), nil, []any{
			cuRow("bogus", "x", "direct"), cuRow("domain", "", "direct"), cuRow("port", "abc", "direct"), cuRow("ruleUrl", "ftp://x", "direct"), "x", rQQ}, nil, plOpts(), sB,
			[]any{skHTTPS, cuDirect(qq), skFbProxy}, "ipv4_only", []any{"dns-direct"}, nil, []RuleOwner{cuOwn(1, 2, 0), plFB(2, 3)}},

		// ---- 和别的块的顺序 ----
		{"自定义分流排在站点集前面;两块的规则归属各管各的", skDM(nil), nil, []any{rQQ}, []any{obj{"id": "p-media", "name": "流媒体", "default": "美国", "domainSuffix": []any{"netflix.com"}}}, plOpts(), sB,
			plCat([]any{skHTTPS, cuDirect(qq)}, plPair(obj{"domain_suffix": []string{"netflix.com"}}, flMedia), []any{skFbProxy}), "ipv4_only",
			[]any{"dns-direct"}, map[string]string{"流媒体": "dns-proxy"}, []RuleOwner{cuOwn(1, 2, 0), plOwn(2, 4, 0, "流媒体"), plFB(4, 5)}},
		{"劫持模式:本地主机名规则在前,下标整体后移", obj{}, nil, []any{rQQ}, nil, plOpts(), []any{dnsDirect("223.5.5.5"), skProxy, dnsLocalServer},
			[]any{dnsLocalRules[0], dnsLocalRules[1], skHTTPS, cuDirect(qq), skFbProxy}, "ipv4_only", []any{"dns-direct"}, nil, []RuleOwner{cuOwn(3, 4, 0), plFB(4, 5)}},

		// ---- FakeIP / IPv6 降级:只管出口走代理的行 ----
		{"FakeIP:代理行前面多一条占位规则;直连行、拒绝行不受影响", skDM(obj{"fakeIpForProxy": true}), nil, []any{rQQ, rYT, rAds}, nil, plOpts(), sFake,
			[]any{skHTTPS, cuDirect(qq), cuFake(yt, "A"), cuProxy(yt), cuReject(ads), skFakeRuleA, skFbProxy}, "ipv4_only",
			[]any{"dns-direct", "dns-proxy", nil}, nil, []RuleOwner{cuOwn(1, 2, 0), cuOwn(2, 4, 1), cuOwn(4, 5, 2), plFB(5, 7)}},
		{"代理 v6 降为 IPv4:代理行前面多一条 AAAA 空应答,直连行不受影响", skDM(nil), obj{"ipv6": true, "ipv6Proxy": "ipv4"}, []any{rYT, rQQ}, nil, plOpts(), sB,
			[]any{skHTTPS, cuAAAA(yt), cuProxy(yt), cuDirect(qq), skEmptyAAAA, skFbProxy}, "prefer_ipv4",
			[]any{"dns-proxy", "dns-direct"}, nil, []RuleOwner{cuOwn(1, 3, 0), cuOwn(3, 4, 1), plFB(4, 6)}},
		{"FakeIP + IPv6 交给节点:占位规则管 A 和 AAAA", skDM(obj{"fakeIpForProxy": true}), obj{"ipv6": true}, []any{rYT}, nil, plOpts(), sFake46,
			[]any{skHTTPS, cuFake(yt, "A", "AAAA"), cuProxy(yt), skFakeRuleAA, skFbProxy}, "prefer_ipv4",
			[]any{"dns-proxy"}, nil, []RuleOwner{cuOwn(1, 3, 0), plFB(3, 5)}},
		{"FakeIP + 降 IPv4:每个代理行先 AAAA 空应答、再占位、再代理", skDM(obj{"fakeIpForProxy": true}), obj{"ipv6": true, "ipv6Proxy": "ipv4"}, []any{rYT}, nil, plOpts(), sFake,
			[]any{skHTTPS, cuAAAA(yt), cuFake(yt, "A"), cuProxy(yt), skEmptyAAAA, skFakeRuleA, skFbProxy}, "prefer_ipv4",
			[]any{"dns-proxy"}, nil, []RuleOwner{cuOwn(1, 4, 0), plFB(4, 7)}},

		// ---- 备用上游竞速 ----
		{"代理备用:代理行变成竞速段(占 race-0),兜底占 race-1;直连行、拒绝行不竞速", skDM(obj{"proxyExtras": []any{obj{"server": "9.9.9.9"}}}), nil, []any{rYT, rQQ, rAds}, nil, plOpts(), sAltP,
			plCat([]any{skHTTPS}, cuRaceP(yt, 0), []any{cuDirect(qq), cuReject(ads),
				skEval(skSwitch(), "dns-proxy", "race-1-0"), skEval(skSwitch(), "dns-proxy__alt1", "race-1-1"), skRespond("race-1-0"), skRespond("race-1-1"), skFbProxy}),
			"ipv4_only", []any{"dns-proxy", "dns-direct", nil}, nil, []RuleOwner{cuOwn(1, 6, 0), cuOwn(6, 7, 1), cuOwn(7, 8, 2), plFB(8, 13)}},
		{"直连备用:直连行变成竞速段;代理行、拒绝行不竞速;兜底末尾补直连竞速", skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}}), nil, []any{rQQ, rYT, rAds}, nil, plOpts(), sAlt,
			plCat([]any{skHTTPS}, plRaceD(qq, 0), []any{cuProxy(yt), cuReject(ads), skFbProxy,
				skEval(nil, "dns-direct", "race-1-0"), skEval(nil, "dns-direct__alt1", "race-1-1"), skRespond("race-1-0"), skRespond("race-1-1")}),
			"ipv4_only", []any{"dns-direct", "dns-proxy", nil}, nil, []RuleOwner{cuOwn(1, 6, 0), cuOwn(6, 7, 1), cuOwn(7, 8, 2), plFB(8, 13)}},
		{"两侧都有备用:代理行占 race-0,直连行占 race-1,兜底代理 race-2、直连 race-3", skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}, "proxyExtras": []any{obj{"server": "9.9.9.9"}}}), nil, []any{rYT, rQQ}, nil, plOpts(), sAltBoth,
			plCat([]any{skHTTPS}, cuRaceP(yt, 0), plRaceD(qq, 1),
				[]any{skEval(skSwitch(), "dns-proxy", "race-2-0"), skEval(skSwitch(), "dns-proxy__alt1", "race-2-1"), skRespond("race-2-0"), skRespond("race-2-1"), skFbProxy,
					skEval(nil, "dns-direct", "race-3-0"), skEval(nil, "dns-direct__alt1", "race-3-1"), skRespond("race-3-0"), skRespond("race-3-1")}),
			"ipv4_only", []any{"dns-proxy", "dns-direct"}, nil, []RuleOwner{cuOwn(1, 6, 0), cuOwn(6, 11, 1), plFB(11, 20)}},
	}
	for _, c := range cases {
		routing := obj{"custom": obj{"enabled": true, "rules": c.rows}}
		if c.pols != nil {
			routing["policies"] = c.pols
		}
		res, err := BuildDNS(skProfile(c.dns, plMerge(c.top, obj{"routing": routing})), c.o)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		want := map[string]any{"servers": c.wantS, "rules": c.wantR, "final": "dns-direct", "strategy": c.strat, "reverse_mapping": true}
		if !reflect.DeepEqual(res.DNS, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, res.DNS, want)
		}
		pmap := c.pmap
		if pmap == nil {
			pmap = map[string]string{}
		}
		wantRes := Resolvers{Policies: pmap, Custom: c.custom, Clients: []ClientResolver{}, Fallback: "dns-direct"}
		if !reflect.DeepEqual(res.Resolvers, wantRes) {
			t.Errorf("%s:Resolvers = %#v,想要 %#v", c.name, res.Resolvers, wantRes)
		}
		if !reflect.DeepEqual(res.RuleOwners, c.owns) {
			t.Errorf("%s:RuleOwners = %#v,想要 %#v", c.name, res.RuleOwners, c.owns)
		}
	}
}

// 随包规则库里已经没有的 geosite 行在整理时被去掉(只是不进配置),不占下标;DNS 这里自然也看不到。
func TestBuildDNSSplitCustomGeoFilter(t *testing.T) {
	setAvailableGeoTags(&[]string{"geosite-cn"})
	defer setAvailableGeoTags(nil)
	rows := []any{cuRow("geosite", "gone", "direct"), cuRow("geosite", "cn", "direct")}
	res, err := BuildDNS(skProfile(skDM(nil), obj{"routing": obj{"custom": obj{"enabled": true, "rules": rows}}}), plOpts())
	if err != nil {
		t.Fatal(err)
	}
	want := []any{skHTTPS, cuDirect(obj{"rule_set": []string{"geosite-cn"}}), skFbProxy}
	if !reflect.DeepEqual(res.DNS["rules"], want) {
		t.Errorf("rules = %#v", res.DNS["rules"])
	}
	if !reflect.DeepEqual(res.Resolvers.Custom, []any{"dns-direct"}) {
		t.Errorf("Custom = %#v", res.Resolvers.Custom)
	}
	if !reflect.DeepEqual(res.RuleOwners, []RuleOwner{cuOwn(1, 2, 0), plFB(2, 3)}) {
		t.Errorf("RuleOwners = %#v", res.RuleOwners)
	}
}
