package routing

import (
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/clientroutes"
)

// 自写测试向量:BuildDNS 分流开着、有终端分流(含「不进内核」终端和「只让这些终端进内核」名单外终端)时的那几块规则。
// 输入自己拟,期望值按 dns.go 的规则人工推算。规则:
//   - 哪些终端由内核按终端答:劫持模式全部;dnsmasq 模式只有 TerminalDNSRedirected 认的(要 AutoRedirect;不进内核、出口是节点 / 节点组、
//     出口直连且「直连不进内核」开着);拒绝的终端在 dnsmasq 模式下不转。
//   - 名单外终端的专用入站(AdmitDirect)和「不进内核」的终端先答,排在前置自定义分流前面:一律解析器 dns-terminal-direct;入站在前、终端按给定顺序在后;
//   - 普通终端分流排在前置自定义分流、订阅 / 节点站点直连之后、站点集之前:出口直连 → dns-terminal-direct,出口拒绝 → reject,
//     出口节点 / 节点组 → 代理侧解析器(带 AAAA 空应答 / FakeIP 占位;KnownOutbounds 非 nil 时查不到的出口整条跳过),出口为空或查不到终端的跳过;
//   - 按 IP 来源的终端登记到 Resolvers.Clients(先「不进内核」再普通终端,各按给定顺序;拒绝的不登记),按 MAC 的不登记;
//   - dnsmasq 模式下转进来的终端(以及名单外入站)不再经过 dnsmasq:每个来源两条规则把本地主机名交回 dns-local,排在 HTTPS 空应答前面,
//     与 KnownOutbounds 无关;dns-local 解析器排在服务器列表最后;
//   - dns-terminal-direct 只在用到时建,备用上游照直连的样子各配一个 __alt;规则同样变竞速段,序号整份配置不撞。

const tmTerm = "dns-terminal-direct"

func tmIP(out, cidr string, bypass bool) clientroutes.Route {
	return clientroutes.Route{ID: "r-" + cidr, Name: cidr, Match: "ip", Outbound: out, Bypass: bypass, Sources: []string{cidr}}
}

func tmMAC(out, mac string, bypass bool) clientroutes.Route {
	return clientroutes.Route{ID: "r-" + mac, Name: mac, Match: "mac", Outbound: out, Bypass: bypass, Sources: []string{}, Macs: []string{mac}}
}

func tmSrc(c string) obj { return obj{"source_ip_cidr": []string{c}} }

func tmMc(m string) obj { return obj{"source_mac_address": []string{m}} }

func tmTo(m obj, server string) obj { return plMerge(m, obj{"server": server}) }

func tmReject(m obj) obj { return plMerge(m, obj{"action": "reject"}) }

// tmLocal:dnsmasq 模式下转进来的来源,本地主机名交回 dns-local 的两条规则。
func tmLocal(m obj) []any {
	return []any{plMerge(m, dnsLocalRules[0].(obj)), plMerge(m, dnsLocalRules[1].(obj))}
}

func tmOwn(from, to int) RuleOwner {
	return RuleOwner{From: from, To: to, Kind: "client", Index: 0, Name: ""}
}

func tmCR(sources []string, server string) ClientResolver {
	return ClientResolver{Sources: sources, Server: server}
}

// tmRace:带直连备用上游(各一个 alt1)的终端直连竞速段;n 是序号。
func tmRace(m obj, n int) []any {
	t0, t1 := raceTag(n, 0), raceTag(n, 1)
	return []any{skEval(m, tmTerm, t0), skEval(m, tmTerm+"__alt1", t1), skRespond(t0), skRespond(t1), tmTo(m, tmTerm)}
}

func tmOpts(term TerminalDNS, routes ...clientroutes.Route) DNSOptions {
	return DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, ClientRoutes: routes, Terminal: term}
}

func TestBuildDNSSplitTerminal(t *testing.T) {
	auto := TerminalDNS{AutoRedirect: true}
	dTerm := skDirectTag("223.5.5.5", tmTerm)
	sB := []any{dnsDirect("223.5.5.5"), skProxy}
	sBL := []any{dnsDirect("223.5.5.5"), skProxy, dnsLocalServer}
	sTL := []any{dnsDirect("223.5.5.5"), skProxy, dTerm, dnsLocalServer}
	fbOnly := []RuleOwner{plFB(1, 2)}
	h := []any{dnsLocalRules[0], dnsLocalRules[1], skHTTPS} // 劫持模式的骨架前缀
	ip10, ip11, ip12, ip20, ip30, ip40 := "192.168.1.10/32", "192.168.1.11/32", "192.168.1.12/32", "192.168.1.20/32", "192.168.1.30/32", "192.168.1.40/32"
	mac1, mac2, mac3, mac4 := "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03", "aa:bb:cc:dd:ee:04"
	noOut := clientroutes.Route{ID: "r-none", Name: "none", Match: "ip", Sources: []string{"192.168.1.50/32"}}
	noSrcIP := clientroutes.Route{ID: "r-nosrc", Name: "nosrc", Match: "ip", Outbound: "direct", Sources: []string{}}
	noMac := clientroutes.Route{ID: "r-nomac", Name: "nomac", Match: "mac", Outbound: "direct", Sources: []string{}}
	noSrcBypass := clientroutes.Route{ID: "r-nosrcb", Name: "nosrcb", Match: "ip", Bypass: true, Sources: []string{}}
	noMacBypass := clientroutes.Route{ID: "r-nomacb", Name: "nomacb", Match: "mac", Bypass: true, Sources: []string{}}
	renamed := func(rs ...clientroutes.Route) DNSOptions {
		o := tmOpts(auto, rs...)
		o.Builtin = builtinTags{"直连", "拒绝", true, true}
		o.GroupTags = []string{"直连", "美国"}
		return o
	}
	known := func(rs ...clientroutes.Route) DNSOptions {
		o := tmOpts(auto, rs...)
		o.KnownOutbounds = map[string]bool{"美国": true}
		return o
	}
	knownOther := func(rs ...clientroutes.Route) DNSOptions {
		o := tmOpts(auto, rs...)
		o.KnownOutbounds = map[string]bool{"别的组": true}
		return o
	}

	cases := []struct {
		name    string
		dns     obj
		top     obj
		o       DNSOptions
		wantS   []any
		wantR   []any
		strat   string
		clients []ClientResolver
		owns    []RuleOwner
	}{
		// ---- dnsmasq 模式:哪些终端被转进来 ----
		{"dnsmasq:「不进内核」的 IP 终端——本地主机名交回 dnsmasq,再直连解析,登记 Clients", skDM(nil), nil, tmOpts(auto, tmIP("", ip20, true)), sTL,
			plCat(tmLocal(tmSrc(ip20)), []any{skHTTPS, tmTo(tmSrc(ip20), tmTerm), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip20}, tmTerm)}, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},
		{"dnsmasq:「不进内核」的 MAC 终端——规则用 source_mac_address,不登记 Clients", skDM(nil), nil, tmOpts(auto, tmMAC("", mac1, true)), sTL,
			plCat(tmLocal(tmMc(mac1)), []any{skHTTPS, tmTo(tmMc(mac1), tmTerm), skFbProxy}), "ipv4_only",
			nil, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},
		{"dnsmasq:名单外终端的专用入站——先交回本地主机名,再直连解析,不登记 Clients", skDM(nil), nil, tmOpts(TerminalDNS{AutoRedirect: true, AdmitDirect: "dns-in-direct"}), sTL,
			plCat(tmLocal(obj{"inbound": []string{"dns-in-direct"}}), []any{skHTTPS, tmTo(obj{"inbound": []string{"dns-in-direct"}}, tmTerm), skFbProxy}), "ipv4_only",
			nil, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},
		{"dnsmasq:入站在「不进内核」终端前面;本地主机名规则先终端后入站", skDM(nil), nil,
			tmOpts(TerminalDNS{AutoRedirect: true, AdmitDirect: "dns-in-direct"}, tmIP("", ip20, true)), sTL,
			plCat(tmLocal(tmSrc(ip20)), tmLocal(obj{"inbound": []string{"dns-in-direct"}}),
				[]any{skHTTPS, tmTo(obj{"inbound": []string{"dns-in-direct"}}, tmTerm), tmTo(tmSrc(ip20), tmTerm), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip20}, tmTerm)}, []RuleOwner{tmOwn(5, 7), plFB(7, 8)}},
		{"dnsmasq:出口直连的终端(「直连不进内核」默认开)——被转进来,在普通终端分流段直连解析", skDM(nil), nil, tmOpts(auto, tmIP("direct", ip10, false)), sTL,
			plCat(tmLocal(tmSrc(ip10)), []any{skHTTPS, tmTo(tmSrc(ip10), tmTerm), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip10}, tmTerm)}, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},
		{"dnsmasq:「直连不进内核」关着时出口直连的终端不转,什么都不写", skDM(nil), nil,
			tmOpts(TerminalDNS{AutoRedirect: true, DirectBypassOff: true}, tmIP("direct", ip10, false)), sB,
			[]any{skHTTPS, skFbProxy}, "ipv4_only", nil, fbOnly},
		{"dnsmasq:出口节点组的终端——被转进来,代理侧解析器,登记 Clients", skDM(nil), nil, tmOpts(auto, tmIP("美国", ip30, false)), sBL,
			plCat(tmLocal(tmSrc(ip30)), []any{skHTTPS, tmTo(tmSrc(ip30), "dns-proxy"), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip30}, "dns-proxy")}, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},
		{"dnsmasq:出口拒绝的终端不转,什么都不写", skDM(nil), nil, tmOpts(auto, tmIP("block", ip40, false)), sB,
			[]any{skHTTPS, skFbProxy}, "ipv4_only", nil, fbOnly},
		{"dnsmasq:没开 AutoRedirect 时各种终端一概不转", skDM(nil), nil,
			tmOpts(TerminalDNS{}, tmIP("", ip20, true), tmIP("direct", ip10, false), tmIP("美国", ip30, false)), sB,
			[]any{skHTTPS, skFbProxy}, "ipv4_only", nil, fbOnly},
		{"dnsmasq:没开 AutoRedirect 时名单外入站照样写(专用入站,不靠入口重定向),终端仍不转", skDM(nil), nil,
			tmOpts(TerminalDNS{AdmitDirect: "dns-in-direct"}, tmIP("", ip20, true), tmIP("美国", ip30, false)), sTL,
			plCat(tmLocal(obj{"inbound": []string{"dns-in-direct"}}), []any{skHTTPS, tmTo(obj{"inbound": []string{"dns-in-direct"}}, tmTerm), skFbProxy}), "ipv4_only",
			nil, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},
		{"dnsmasq:出口为空的普通终端、没有来源的终端都跳过(没有来源的连本地主机名规则也没有)", skDM(nil), nil, tmOpts(auto, noOut, noSrcIP, noMac, noSrcBypass, noMacBypass, tmIP("美国", ip30, false)), sBL,
			plCat(tmLocal(tmSrc(ip30)), []any{skHTTPS, tmTo(tmSrc(ip30), "dns-proxy"), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip30}, "dns-proxy")}, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},
		{"dnsmasq:多台终端按给定顺序;本地主机名规则与终端规则各自保序", skDM(nil), nil,
			tmOpts(auto, tmIP("美国", ip30, false), tmIP("direct", ip10, false), tmIP("", ip20, true)), sTL,
			plCat(tmLocal(tmSrc(ip30)), tmLocal(tmSrc(ip10)), tmLocal(tmSrc(ip20)),
				[]any{skHTTPS, tmTo(tmSrc(ip20), tmTerm), tmTo(tmSrc(ip30), "dns-proxy"), tmTo(tmSrc(ip10), tmTerm), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip20}, tmTerm), tmCR([]string{ip30}, "dns-proxy"), tmCR([]string{ip10}, tmTerm)},
			[]RuleOwner{tmOwn(7, 8), tmOwn(8, 10), plFB(10, 11)}},

		// ---- KnownOutbounds ----
		{"出口在已知出站里:照写", skDM(nil), nil, known(tmIP("美国", ip30, false)), sBL,
			plCat(tmLocal(tmSrc(ip30)), []any{skHTTPS, tmTo(tmSrc(ip30), "dns-proxy"), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip30}, "dns-proxy")}, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},
		{"出口不在已知出站里:终端规则和 Clients 都不写,本地主机名规则仍写", skDM(nil), nil, knownOther(tmIP("美国", ip30, false)), sBL,
			plCat(tmLocal(tmSrc(ip30)), []any{skHTTPS, skFbProxy}), "ipv4_only",
			nil, []RuleOwner{plFB(3, 4)}},
		{"已知出站表只管代理出口:直连终端不受影响", skDM(nil), nil, knownOther(tmIP("direct", ip10, false)), sTL,
			plCat(tmLocal(tmSrc(ip10)), []any{skHTTPS, tmTo(tmSrc(ip10), tmTerm), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip10}, tmTerm)}, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},

		// ---- 劫持模式:全部按终端答,不需要 AutoRedirect,不再有终端本地主机名规则 ----
		{"劫持:各种出口的终端各按自己的出口答;拒绝写 reject 且不登记 Clients;出口为空、没有来源的(含「不进内核」的)跳过", obj{}, nil,
			tmOpts(TerminalDNS{}, tmIP("direct", ip10, false), tmIP("美国", ip30, false), tmIP("", ip20, true), tmIP("block", ip40, false), noOut, noSrcIP, noMac, noSrcBypass, noMacBypass), sTL,
			plCat(h, []any{tmTo(tmSrc(ip20), tmTerm), tmTo(tmSrc(ip10), tmTerm), tmTo(tmSrc(ip30), "dns-proxy"), tmReject(tmSrc(ip40)), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip20}, tmTerm), tmCR([]string{ip10}, tmTerm), tmCR([]string{ip30}, "dns-proxy")},
			[]RuleOwner{tmOwn(3, 4), tmOwn(4, 7), plFB(7, 8)}},
		{"劫持:开着 AutoRedirect 结果相同", obj{}, nil,
			tmOpts(auto, tmIP("direct", ip10, false), tmIP("美国", ip30, false), tmIP("", ip20, true), tmIP("block", ip40, false)), sTL,
			plCat(h, []any{tmTo(tmSrc(ip20), tmTerm), tmTo(tmSrc(ip10), tmTerm), tmTo(tmSrc(ip30), "dns-proxy"), tmReject(tmSrc(ip40)), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip20}, tmTerm), tmCR([]string{ip10}, tmTerm), tmCR([]string{ip30}, "dns-proxy")},
			[]RuleOwner{tmOwn(3, 4), tmOwn(4, 7), plFB(7, 8)}},
		{"劫持:按 MAC 的终端用 source_mac_address,都不登记 Clients(拒绝照样 reject)", obj{}, nil,
			tmOpts(TerminalDNS{}, tmMAC("", mac1, true), tmMAC("direct", mac2, false), tmMAC("美国", mac3, false), tmMAC("block", mac4, false)), sTL,
			plCat(h, []any{tmTo(tmMc(mac1), tmTerm), tmTo(tmMc(mac2), tmTerm), tmTo(tmMc(mac3), "dns-proxy"), tmReject(tmMc(mac4)), skFbProxy}), "ipv4_only",
			nil, []RuleOwner{tmOwn(3, 4), tmOwn(4, 7), plFB(7, 8)}},
		{"劫持:名单外终端入站排在「不进内核」终端前面,没有本地主机名转交", obj{}, nil,
			tmOpts(TerminalDNS{AdmitDirect: "dns-in-direct"}, tmIP("", ip20, true)), sTL,
			plCat(h, []any{tmTo(obj{"inbound": []string{"dns-in-direct"}}, tmTerm), tmTo(tmSrc(ip20), tmTerm), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip20}, tmTerm)}, []RuleOwner{tmOwn(3, 5), plFB(5, 6)}},
		{"劫持:只有代理终端时不建 dns-terminal-direct", obj{}, nil, tmOpts(TerminalDNS{}, tmIP("美国", ip30, false)), []any{dnsDirect("223.5.5.5"), skProxy, dnsLocalServer},
			plCat(h, []any{tmTo(tmSrc(ip30), "dns-proxy"), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip30}, "dns-proxy")}, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},

		// ---- 内置出站改名 ----
		{"dnsmasq:直连 / 拒绝改了名,按新名字认;旧记号 direct 只是普通名字,按节点组", skDM(nil), nil,
			renamed(tmIP("直连", ip10, false), tmIP("拒绝", ip11, false), tmIP("direct", ip12, false)), sTL,
			plCat(tmLocal(tmSrc(ip10)), tmLocal(tmSrc(ip12)), []any{skHTTPS, tmTo(tmSrc(ip10), tmTerm), tmTo(tmSrc(ip12), "dns-proxy"), skFbProxy}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip10}, tmTerm), tmCR([]string{ip12}, "dns-proxy")}, []RuleOwner{tmOwn(5, 7), plFB(7, 8)}},
		{"dnsmasq:直连改了名且「直连不进内核」关着——新名字的终端仍认作直连,不转", skDM(nil), nil,
			func() DNSOptions {
				o := renamed(tmIP("直连", ip10, false))
				o.Terminal.DirectBypassOff = true
				return o
			}(), sB, []any{skHTTPS, skFbProxy}, "ipv4_only", nil, fbOnly},
		{"劫持:拒绝改了名,新名字写 reject", obj{}, nil, renamed(tmIP("拒绝", ip11, false)), []any{dnsDirect("223.5.5.5"), skProxy, dnsLocalServer},
			plCat(h, []any{tmReject(tmSrc(ip11)), skFbProxy}), "ipv4_only", nil, []RuleOwner{tmOwn(3, 4), plFB(4, 5)}},

		// ---- FakeIP / IPv6 降级:只管走代理的终端 ----
		{"FakeIP + 降 IPv4:代理终端先 AAAA 空应答、再占位、再代理;直连终端不受影响", skDM(obj{"fakeIpForProxy": true}), obj{"ipv6": true, "ipv6Proxy": "ipv4"},
			tmOpts(auto, tmIP("美国", ip30, false), tmIP("direct", ip10, false)), []any{dnsDirect("223.5.5.5"), skProxy, dTerm, skFake4, dnsLocalServer},
			plCat(tmLocal(tmSrc(ip30)), tmLocal(tmSrc(ip10)), []any{skHTTPS,
				plMerge(tmSrc(ip30), obj{"query_type": []string{"AAAA"}, "action": "predefined", "rcode": "NOERROR"}),
				plMerge(tmSrc(ip30), obj{"query_type": []string{"A"}, "server": "dns-fakeip"}),
				tmTo(tmSrc(ip30), "dns-proxy"), tmTo(tmSrc(ip10), tmTerm), skEmptyAAAA, skFakeRuleA, skFbProxy}), "prefer_ipv4",
			[]ClientResolver{tmCR([]string{ip30}, "dns-proxy"), tmCR([]string{ip10}, tmTerm)}, []RuleOwner{tmOwn(5, 9), plFB(9, 12)}},

		// ---- 备用上游竞速 ----
		{"直连备用:dns-terminal-direct 也配 __alt;「不进内核」段占 race-0、普通终端段占 race-1、兜底末尾直连竞速占 race-2",
			skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}}), nil, tmOpts(auto, tmIP("", ip20, true), tmIP("direct", ip10, false)),
			[]any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"), skProxy, dTerm, skDirectTag("8.8.8.8", tmTerm+"__alt1"), dnsLocalServer},
			plCat(tmLocal(tmSrc(ip20)), tmLocal(tmSrc(ip10)), []any{skHTTPS}, tmRace(tmSrc(ip20), 0), tmRace(tmSrc(ip10), 1), []any{skFbProxy,
				skEval(nil, "dns-direct", "race-2-0"), skEval(nil, "dns-direct__alt1", "race-2-1"), skRespond("race-2-0"), skRespond("race-2-1")}), "ipv4_only",
			[]ClientResolver{tmCR([]string{ip20}, tmTerm), tmCR([]string{ip10}, tmTerm)}, []RuleOwner{tmOwn(5, 10), tmOwn(10, 15), plFB(15, 20)}},
	}

	for _, c := range cases {
		res, err := BuildDNS(skProfile(c.dns, c.top), c.o)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		want := map[string]any{"servers": c.wantS, "rules": c.wantR, "final": "dns-direct", "strategy": c.strat, "reverse_mapping": true}
		if !reflect.DeepEqual(res.DNS, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, res.DNS, want)
		}
		clients := c.clients
		if clients == nil {
			clients = []ClientResolver{}
		}
		wantRes := Resolvers{Policies: map[string]string{}, Custom: []any{}, Clients: clients, Fallback: "dns-direct"}
		if !reflect.DeepEqual(res.Resolvers, wantRes) {
			t.Errorf("%s:Resolvers = %#v,想要 %#v", c.name, res.Resolvers, wantRes)
		}
		if !reflect.DeepEqual(res.RuleOwners, c.owns) {
			t.Errorf("%s:RuleOwners = %#v,想要 %#v", c.name, res.RuleOwners, c.owns)
		}
	}
}

// 名单外入站和普通终端都在直连备用下竞速;拒绝终端不竞速(reject 不走解析器)。
func TestBuildDNSSplitTerminalRaceMixed(t *testing.T) {
	ip10, ip40 := "192.168.1.10/32", "192.168.1.40/32"
	adm := obj{"inbound": []string{"dns-in-direct"}}
	dns := obj{} // 劫持模式
	dns["directExtras"] = []any{obj{"server": "8.8.8.8"}}
	o := tmOpts(TerminalDNS{AdmitDirect: "dns-in-direct"}, tmIP("direct", ip10, false), tmIP("block", ip40, false))
	res, err := BuildDNS(skProfile(dns, nil), o)
	if err != nil {
		t.Fatal(err)
	}
	want := plCat(dnsLocalRules, []any{skHTTPS}, tmRace(adm, 0), tmRace(tmSrc(ip10), 1), []any{tmReject(tmSrc(ip40)), skFbProxy,
		skEval(nil, "dns-direct", "race-2-0"), skEval(nil, "dns-direct__alt1", "race-2-1"), skRespond("race-2-0"), skRespond("race-2-1")})
	if !reflect.DeepEqual(res.DNS["rules"], want) {
		t.Errorf("rules:\n got %#v\nwant %#v", res.DNS["rules"], want)
	}
	wantOwns := []RuleOwner{tmOwn(3, 8), tmOwn(8, 14), plFB(14, 19)}
	if !reflect.DeepEqual(res.RuleOwners, wantOwns) {
		t.Errorf("RuleOwners = %#v,想要 %#v", res.RuleOwners, wantOwns)
	}
}

// 劫持模式下终端分流与别的块的整体顺序:名单外入站 → 「不进内核」终端 → 前置自定义分流 → 订阅 / 节点站点直连 → 普通终端分流 → 站点集 → 兜底。
func TestBuildDNSSplitTerminalOrder(t *testing.T) {
	ip10, ip20, ip30 := "192.168.1.10/32", "192.168.1.20/32", "192.168.1.30/32"
	adm := obj{"inbound": []string{"dns-in-direct"}}
	routing := obj{
		"custom":   obj{"enabled": true, "rules": []any{cuRow("domainSuffix", "qq.com", "direct")}},
		"policies": []any{obj{"id": "p-media", "name": "流媒体", "default": "美国", "domainSuffix": []any{"netflix.com"}}},
	}
	o := tmOpts(TerminalDNS{AdmitDirect: "dns-in-direct"}, tmIP("美国", ip30, false), tmIP("direct", ip10, false), tmIP("", ip20, true))
	o.DirectHostSets = dhSets()
	res, err := BuildDNS(skProfile(obj{}, obj{"routing": routing}), o)
	if err != nil {
		t.Fatal(err)
	}
	wantR := plCat(dnsLocalRules, []any{skHTTPS, tmTo(adm, tmTerm), tmTo(tmSrc(ip20), tmTerm), cuDirect(cuM("domain_suffix", "qq.com")), dhDirect(dhSet(dhDomTag)),
		tmTo(tmSrc(ip30), "dns-proxy"), tmTo(tmSrc(ip10), tmTerm)}, plPair(obj{"domain_suffix": []string{"netflix.com"}}, flMedia), []any{skFbProxy})
	if !reflect.DeepEqual(res.DNS["rules"], wantR) {
		t.Errorf("rules:\n got %#v\nwant %#v", res.DNS["rules"], wantR)
	}
	wantOwns := []RuleOwner{tmOwn(3, 5), cuOwn(5, 6, 0), dhOwn(6, 7), tmOwn(7, 9), plOwn(9, 11, 0, "流媒体"), plFB(11, 12)}
	if !reflect.DeepEqual(res.RuleOwners, wantOwns) {
		t.Errorf("RuleOwners = %#v,想要 %#v", res.RuleOwners, wantOwns)
	}
	wantClients := []ClientResolver{tmCR([]string{ip20}, tmTerm), tmCR([]string{ip30}, "dns-proxy"), tmCR([]string{ip10}, tmTerm)}
	if !reflect.DeepEqual(res.Resolvers.Clients, wantClients) {
		t.Errorf("Clients = %#v,想要 %#v", res.Resolvers.Clients, wantClients)
	}
	if !reflect.DeepEqual(res.Resolvers.Custom, []any{"dns-direct"}) || !reflect.DeepEqual(res.Resolvers.Policies, map[string]string{"流媒体": "dns-proxy"}) {
		t.Errorf("Resolvers = %#v", res.Resolvers)
	}
}

// 不写穿入参:终端规则的来源切片、已知出站表原样;两次调用结果相同。
func TestBuildDNSSplitTerminalDoesNotMutateInputs(t *testing.T) {
	routes := []clientroutes.Route{tmIP("美国", "192.168.1.30/32", false), tmMAC("direct", "aa:bb:cc:dd:ee:02", false), tmIP("", "192.168.1.20/32", true)}
	o := tmOpts(TerminalDNS{AutoRedirect: true, AdmitDirect: "dns-in-direct"}, routes...)
	o.KnownOutbounds = map[string]bool{"美国": true}
	profile := skProfile(skDM(nil), nil)
	first, err := BuildDNS(profile, o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(routes, []clientroutes.Route{tmIP("美国", "192.168.1.30/32", false), tmMAC("direct", "aa:bb:cc:dd:ee:02", false), tmIP("", "192.168.1.20/32", true)}) {
		t.Errorf("终端规则被改了:%#v", routes)
	}
	if !reflect.DeepEqual(o.KnownOutbounds, map[string]bool{"美国": true}) {
		t.Errorf("已知出站表被改了:%#v", o.KnownOutbounds)
	}
	second, err := BuildDNS(profile, o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("两次调用结果不同:\n%#v\n%#v", first, second)
	}
}
