package routing

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/clientroutes"
)

// 自写测试向量:PreMatchBypassRules(首包预判放行,sing-box 1.13+ 的 bypass 动作)。输入自己拟,期望值按 route.go 的规则人工推算。规则:
//   - 没有可放行的(没有直连终端、没有订阅 / 节点站点直连的 IP 那份规则集或网段)→ 空数组(序列化成 [] 而不是 null);
//   - 每条放行 = logical AND [放行对象, 取反的「别处要处理的」…] + 动作 bypass。取反部分按顺序是:
//     ip_cidr(tun 网段 + 前置自定义分流里送去别处的 IP 段)→ rule_set(送去别处的规则集)→ 端口(53 打头,加上自定义分流的端口,有端口段才带 port_range);
//     前两项没有内容就不写,端口那项永远有;
//   - 放行对象的顺序:站点直连的 IP 规则集 → 站点直连的网段(没有规则集时才用)→ 直连终端按 IP 来源 → 直连终端按 MAC;
//   - 直连终端 = 出口恰好是直连(按当前名字)且不是「不进内核」;来源 / MAC 跨终端去重并保序;
//   - 前置自定义分流里出口是直连的行不挖(本来就放行),出口是拒绝或节点的行都挖,KnownOutbounds 非 nil 时出口不在表里的行不算。

func pmRun(t *testing.T, routing string, o PreMatchOptions) any {
	t.Helper()
	var r any = map[string]any{}
	if routing != "" {
		r = mustJSON(t, routing)
	}
	return roundTrip(t, PreMatchBypassRules(normalizeRouting(r), o))
}

const (
	pmP53 = `{"port":[53],"invert":true}`
)

func pmBypass(first string, keep ...string) string {
	subs := append([]string{first}, keep...)
	return rtAnd(`"action":"bypass"`, subs...)
}

func pmCheck(t *testing.T, name string, got any, want string) {
	t.Helper()
	w := mustJSON(t, want)
	if !reflect.DeepEqual(got, w) {
		gb, _ := json.Marshal(got)
		t.Errorf("%s 输出不一致\n有: %s\n要: %s", name, gb, want)
	}
}

func TestPreMatchBypassBasics(t *testing.T) {
	ip10, ip11, ip12 := "192.168.1.10/32", "192.168.1.11/32", "192.168.1.12/32"
	mac1, mac2 := "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"
	hostCidr := "198.51.100.0/24"
	pmCheck(t, "什么都没有", pmRun(t, "", PreMatchOptions{}), `[]`)
	pmCheck(t, "只有站点直连的域名(没有网段、没有规则集)不放行",
		pmRun(t, "", PreMatchOptions{DirectHosts: &DirectHosts{Domains: []string{"a.example"}}}), `[]`)
	pmCheck(t, "站点直连两边都空", pmRun(t, "", PreMatchOptions{DirectHosts: &DirectHosts{Domains: []string{}, Cidrs: []string{}}}), `[]`)
	pmCheck(t, "直连终端按 IP", pmRun(t, "", PreMatchOptions{ClientRoutes: []clientroutes.Route{tmIP("direct", ip10, false)}}),
		rtList(pmBypass(`{"source_ip_cidr":["`+ip10+`"]}`, pmP53)))
	pmCheck(t, "直连终端按 MAC", pmRun(t, "", PreMatchOptions{ClientRoutes: []clientroutes.Route{tmMAC("direct", mac1, false)}}),
		rtList(pmBypass(`{"source_mac_address":["`+mac1+`"]}`, pmP53)))
	pmCheck(t, "写 Match 为空的终端按 IP 来源取(只有 mac 才取 MAC)",
		pmRun(t, "", PreMatchOptions{ClientRoutes: []clientroutes.Route{{ID: "x", Outbound: "direct", Sources: []string{ip10}, Macs: []string{mac1}}}}),
		rtList(pmBypass(`{"source_ip_cidr":["`+ip10+`"]}`, pmP53)))
	pmCheck(t, "按 MAC 的终端不看 Sources", pmRun(t, "", PreMatchOptions{ClientRoutes: []clientroutes.Route{{ID: "x", Match: "mac", Outbound: "direct", Sources: []string{ip10}, Macs: []string{mac1}}}}),
		rtList(pmBypass(`{"source_mac_address":["`+mac1+`"]}`, pmP53)))

	// 四类放行对象的顺序
	routes := []clientroutes.Route{tmMAC("direct", mac1, false), tmIP("direct", ip10, false)}
	pmCheck(t, "规则集那份 IP 在前,其次站点直连网段(有规则集时不用),再 IP 来源,最后 MAC",
		pmRun(t, "", PreMatchOptions{DirectHostSet: "hs-ip", DirectHosts: &DirectHosts{Cidrs: []string{hostCidr}}, ClientRoutes: routes}),
		rtList(pmBypass(`{"rule_set":["hs-ip"]}`, pmP53), pmBypass(`{"source_ip_cidr":["`+ip10+`"]}`, pmP53), pmBypass(`{"source_mac_address":["`+mac1+`"]}`, pmP53)))
	pmCheck(t, "没有规则集时用站点直连的网段(排在终端前面)",
		pmRun(t, "", PreMatchOptions{DirectHosts: &DirectHosts{Domains: []string{"a.example"}, Cidrs: []string{hostCidr, "203.0.113.0/24"}}, ClientRoutes: routes}),
		rtList(pmBypass(`{"ip_cidr":["`+hostCidr+`","203.0.113.0/24"]}`, pmP53), pmBypass(`{"source_ip_cidr":["`+ip10+`"]}`, pmP53), pmBypass(`{"source_mac_address":["`+mac1+`"]}`, pmP53)))
	pmCheck(t, "只有规则集那份:只有一条", pmRun(t, "", PreMatchOptions{DirectHostSet: "hs-ip"}), rtList(pmBypass(`{"rule_set":["hs-ip"]}`, pmP53)))

	// 哪些终端算「直连终端」
	pmCheck(t, "「不进内核」的、出口是节点的、没有出口的都不算",
		pmRun(t, "", PreMatchOptions{ClientRoutes: []clientroutes.Route{tmIP("direct", ip10, true), tmIP("美国", ip11, false), tmIP("", ip12, false), tmIP("direct", "192.168.1.13/32", false)}}),
		rtList(pmBypass(`{"source_ip_cidr":["192.168.1.13/32"]}`, pmP53)))
	pmCheck(t, "没有一个算的:空数组", pmRun(t, "", PreMatchOptions{ClientRoutes: []clientroutes.Route{tmIP("direct", ip10, true), tmIP("美国", ip11, false)}}), `[]`)
	pmCheck(t, "出口拒绝的终端不算", pmRun(t, "", PreMatchOptions{ClientRoutes: []clientroutes.Route{tmIP("block", ip10, false)}}), `[]`)
	pmCheck(t, "直连改名:按新名字认,旧记号不算", pmRun(t, "", PreMatchOptions{DirectTag: "直连", ClientRoutes: []clientroutes.Route{tmIP("直连", ip10, false), tmIP("direct", ip11, false)}}),
		rtList(pmBypass(`{"source_ip_cidr":["`+ip10+`"]}`, pmP53)))
	pmCheck(t, "直连终端没有任何来源 / MAC:没有放行对象,结果是空数组(不是 null)",
		pmRun(t, "", PreMatchOptions{ClientRoutes: []clientroutes.Route{{ID: "a", Match: "ip", Outbound: "direct", Sources: []string{}}, {ID: "b", Match: "mac", Outbound: "direct", Sources: []string{}}}}), `[]`)
	pmCheck(t, "来源 / MAC 跨终端去重并保序",
		pmRun(t, "", PreMatchOptions{ClientRoutes: []clientroutes.Route{
			{ID: "a", Match: "ip", Outbound: "direct", Sources: []string{ip10, ip11}},
			{ID: "b", Match: "ip", Outbound: "direct", Sources: []string{ip11, ip12, ip10}},
			{ID: "c", Match: "mac", Outbound: "direct", Sources: []string{}, Macs: []string{mac2, mac1}},
			{ID: "d", Match: "mac", Outbound: "direct", Sources: []string{}, Macs: []string{mac1, "aa:bb:cc:dd:ee:03"}}}}),
		rtList(pmBypass(`{"source_ip_cidr":["`+ip10+`","`+ip11+`","`+ip12+`"]}`, pmP53),
			pmBypass(`{"source_mac_address":["`+mac2+`","`+mac1+`","aa:bb:cc:dd:ee:03"]}`, pmP53)))
}

func TestPreMatchBypassKeep(t *testing.T) {
	ip10 := "192.168.1.10/32"
	direct := []clientroutes.Route{tmIP("direct", ip10, false)}
	first := `{"source_ip_cidr":["` + ip10 + `"]}`
	tun := []string{rtTun}
	pmCheck(t, "tun 网段:取反的 ip_cidr 排在端口前面", pmRun(t, "", PreMatchOptions{ClientRoutes: direct, TunCidrs: tun}),
		rtList(pmBypass(first, `{"ip_cidr":["`+rtTun+`"],"invert":true}`, pmP53)))
	pmCheck(t, "多个 tun 网段按给定顺序", pmRun(t, "", PreMatchOptions{ClientRoutes: direct, TunCidrs: []string{rtTun, "fd00::/126"}}),
		rtList(pmBypass(first, `{"ip_cidr":["`+rtTun+`","fd00::/126"],"invert":true}`, pmP53)))

	rows := rtCustom(
		rtRow("ipCidr", "10.0.0.0/8", "美国"), rtRow("domainSuffix", "x.example", "美国"), rtRow("geosite", "netflix", "美国"),
		rtRow("port", "443,8000-9000", "美国"), rtRow("ipCidr", "10.0.0.0/8", "美国"), rtRow("port", "53", "美国"), rtRow("port", "443", "美国"),
		rtRow("ipCidr", "192.0.2.0/24", "direct"), rtRow("geoip", "cn", "direct"), rtRow("port", "1234", "direct"),
		rtRow("ipCidr", "203.0.113.0/24", "block"), rtRow("geoip", "us", "美国"))
	pmCheck(t, "前置自定义分流:送去别处的行把网段、规则集、端口挖掉,重复的去重,端口 53 不重复;出口直连的行不挖;拒绝的行也挖;域名行不影响",
		pmRun(t, rows, PreMatchOptions{ClientRoutes: direct, TunCidrs: tun}),
		rtList(pmBypass(first,
			`{"ip_cidr":["`+rtTun+`","10.0.0.0/8","203.0.113.0/24"],"invert":true}`,
			`{"rule_set":["geosite-netflix","geoip-us"],"invert":true}`,
			`{"port":[53,443],"port_range":["8000:9000"],"invert":true}`)))
	pmCheck(t, "没有 tun 网段、自定义分流也没有 IP 段:取反的 ip_cidr 不写",
		pmRun(t, rtCustom(rtRow("geosite", "netflix", "美国")), PreMatchOptions{ClientRoutes: direct}),
		rtList(pmBypass(first, `{"rule_set":["geosite-netflix"],"invert":true}`, pmP53)))
	pmCheck(t, "没有规则集:取反的 rule_set 不写", pmRun(t, rtCustom(rtRow("ipCidr", "10.0.0.0/8", "美国")), PreMatchOptions{ClientRoutes: direct}),
		rtList(pmBypass(first, `{"ip_cidr":["10.0.0.0/8"],"invert":true}`, pmP53)))
	pmCheck(t, "只有端口段(单口为空):port 里仍是 [53],port_range 有", pmRun(t, rtCustom(rtRow("port", "8000-9000", "美国")), PreMatchOptions{ClientRoutes: direct}),
		rtList(pmBypass(first, `{"port":[53],"port_range":["8000:9000"],"invert":true}`)))
	pmCheck(t, "端口段去重", pmRun(t, rtCustom(rtRow("port", "8000-9000", "美国"), rtRow("port", "8000-9000,9100-9200", "美国")), PreMatchOptions{ClientRoutes: direct}),
		rtList(pmBypass(first, `{"port":[53],"port_range":["8000:9000","9100:9200"],"invert":true}`)))
	pmCheck(t, "规则集链接遇形状表:域名、IP 两份都挖,只有 IP 的只挖 IP 那份;没有形状表就挖那一个 tag",
		pmRun(t, rtCustom(rtRow("ruleUrl", urlA, "美国"), rtRow("ruleUrl", urlAds, "美国")),
			PreMatchOptions{ClientRoutes: direct, RuleLists: ruleLists{tagA: ruleShape{domain: true, ip: true}, tagAds: ruleShape{ip: true}}}),
		rtList(pmBypass(first, `{"rule_set":["`+tagA+`","`+tagA+`-ip","`+tagAds+`-ip"],"invert":true}`, pmP53)))
	pmCheck(t, "规则集链接没有形状表", pmRun(t, rtCustom(rtRow("ruleUrl", urlA, "美国")), PreMatchOptions{ClientRoutes: direct}),
		rtList(pmBypass(first, `{"rule_set":["`+tagA+`"],"invert":true}`, pmP53)))
	pmCheck(t, "规则集行(ruleset)按它写的 tag", pmRun(t, rtCustom(rtRow("ruleset", "geosite-x", "美国"), rtRow("ruleset", "geosite-x", "美国")), PreMatchOptions{ClientRoutes: direct}),
		rtList(pmBypass(first, `{"rule_set":["geosite-x"],"invert":true}`, pmP53)))

	// 核对表与改名
	known := rtKnown("direct", "美国")
	pmCheck(t, "KnownOutbounds 非 nil:出口不在表里的行不挖;出口直连的行先于核对被跳过;拒绝不在表里也不挖",
		pmRun(t, rtCustom(rtRow("ipCidr", "10.0.0.0/8", "美国"), rtRow("ipCidr", "11.0.0.0/8", "不存在"), rtRow("ipCidr", "12.0.0.0/8", "block"), rtRow("ipCidr", "13.0.0.0/8", "direct")),
			PreMatchOptions{ClientRoutes: direct, KnownOutbounds: known}),
		rtList(pmBypass(first, `{"ip_cidr":["10.0.0.0/8"],"invert":true}`, pmP53)))
	pmCheck(t, "KnownOutbounds 里有 block 时拒绝的行才挖", pmRun(t, rtCustom(rtRow("ipCidr", "12.0.0.0/8", "block")),
		PreMatchOptions{ClientRoutes: direct, KnownOutbounds: rtKnown("block")}),
		rtList(pmBypass(first, `{"ip_cidr":["12.0.0.0/8"],"invert":true}`, pmP53)))
	pmCheck(t, "KnownOutbounds 为空表(非 nil):自定义分流的行都不挖", pmRun(t, rtCustom(rtRow("ipCidr", "10.0.0.0/8", "美国")),
		PreMatchOptions{ClientRoutes: direct, KnownOutbounds: map[string]bool{}}), rtList(pmBypass(first, pmP53)))
	renamed := []clientroutes.Route{tmIP("直连", ip10, false)}
	pmCheck(t, "内置直连 / 拒绝改名:direct 记号与新名字的行都是直连不挖;拒绝记号换成新拒绝名后仍挖",
		pmRun(t, rtCustom(rtRow("ipCidr", "10.0.0.0/8", "direct"), rtRow("ipCidr", "11.0.0.0/8", "直连"), rtRow("ipCidr", "12.0.0.0/8", "block"), rtRow("ipCidr", "13.0.0.0/8", "拒绝")),
			PreMatchOptions{DirectTag: "直连", BlockTag: "拒绝", ClientRoutes: renamed, KnownOutbounds: rtKnown("直连", "拒绝")}),
		rtList(pmBypass(first, `{"ip_cidr":["12.0.0.0/8","13.0.0.0/8"],"invert":true}`, pmP53)))
	pmCheck(t, "没改名时「直连」只是普通名字:当节点挖", pmRun(t, rtCustom(rtRow("ipCidr", "11.0.0.0/8", "直连")), PreMatchOptions{ClientRoutes: direct}),
		rtList(pmBypass(first, `{"ip_cidr":["11.0.0.0/8"],"invert":true}`, pmP53)))

	// 自定义分流没生效
	off := `{"custom":{"enabled":false,"rules":[` + rtRow("ipCidr", "10.0.0.0/8", "美国") + `]}}`
	pmCheck(t, "停用的自定义分流不挖", pmRun(t, off, PreMatchOptions{ClientRoutes: direct}), rtList(pmBypass(first, pmP53)))
	pmCheck(t, "启用但没有规则不挖", pmRun(t, `{"custom":{"enabled":true,"rules":[]}}`, PreMatchOptions{ClientRoutes: direct}), rtList(pmBypass(first, pmP53)))

	// 每条放行都带同一份取反条件;规则集 / 站点直连放行同样如此
	pmCheck(t, "取反条件对每条放行都一样", pmRun(t, rtCustom(rtRow("ipCidr", "10.0.0.0/8", "美国")),
		PreMatchOptions{DirectHostSet: "hs-ip", ClientRoutes: []clientroutes.Route{tmIP("direct", ip10, false), tmMAC("direct", "aa:bb:cc:dd:ee:01", false)}, TunCidrs: tun}),
		rtList(pmBypass(`{"rule_set":["hs-ip"]}`, `{"ip_cidr":["`+rtTun+`","10.0.0.0/8"],"invert":true}`, pmP53),
			pmBypass(first, `{"ip_cidr":["`+rtTun+`","10.0.0.0/8"],"invert":true}`, pmP53),
			pmBypass(`{"source_mac_address":["aa:bb:cc:dd:ee:01"]}`, `{"ip_cidr":["`+rtTun+`","10.0.0.0/8"],"invert":true}`, pmP53)))
}

func TestPreMatchBypassDoesNotMutateInput(t *testing.T) {
	tun := make([]string, 1, 8)
	tun[0] = rtTun
	srcs := []string{"192.168.1.10/32"}
	cidrs := []string{"198.51.100.0/24"}
	routing := mustJSON(t, rtCustom(rtRow("ipCidr", "10.0.0.0/8", "美国")))
	before, _ := json.Marshal(routing)
	o := PreMatchOptions{TunCidrs: tun, DirectHosts: &DirectHosts{Cidrs: cidrs}, ClientRoutes: []clientroutes.Route{{ID: "a", Match: "ip", Outbound: "direct", Sources: srcs}}}
	a := roundTrip(t, PreMatchBypassRules(normalizeRouting(routing), o))
	b := roundTrip(t, PreMatchBypassRules(normalizeRouting(routing), o))
	if !reflect.DeepEqual(a, b) {
		t.Errorf("两次调用结果不同")
	}
	if !reflect.DeepEqual(tun[:2], []string{rtTun, ""}) {
		t.Errorf("TunCidrs 的底层数组被写穿:%v", tun[:2])
	}
	if !reflect.DeepEqual(srcs, []string{"192.168.1.10/32"}) || !reflect.DeepEqual(cidrs, []string{"198.51.100.0/24"}) {
		t.Errorf("入参被改写:%v %v", srcs, cidrs)
	}
	if after, _ := json.Marshal(routing); string(after) != string(before) {
		t.Errorf("routing 入参被改写")
	}
}
