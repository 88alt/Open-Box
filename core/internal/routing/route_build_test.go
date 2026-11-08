package routing

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/clientroutes"
)

// 自写测试向量:BuildRoute(生成 sing-box 的 route 段)。输入自己拟,期望值按 route.go 的规则人工推算,写成 JSON 文本。规则:
//   - 规则顺序固定:首包预判放行(只在 PreMatchBypass 开着且有可放行的时候,排在 sniff 前面)→ sniff → DNS 接管(按模式)→ tun 防回环 reject
//     → 前置自定义分流 → ip_is_private 直连 → NTP(UDP 123)直连 → 订阅 / 节点站点直连 → 终端分流 → 站点集 → 兜底的 v6 / QUIC 拒绝;
//   - DNS 接管:hijack = 协议为 dns 的先接管、dns-in(以及名单外终端的专用入站)再接管;off = 只有 dns-in;dnsmasq = 入站接管,
//     有 tun 网段又有 dnsmasq 出站时再多一条把 tun 网段 53 端口改写到 127.0.0.1 的路由;不认识的模式不写 DNS 规则;
//   - IP 只管 IP、域名只管域名:按 IP 判的规则(ip_cidr、geoip-*、规则集链接的 IP 那份)写成 logical AND,带「连接没有域名」的前提;
//   - 规则集 + 域名 / IP 条件的规则拆成紧邻的两条;
//   - v6 / QUIC 拒绝:出口是站点集或兜底的挂在它们的热切换开关上(恒定成对写,不看 RejectV6For 的结论,只看有没有传);其余出口按
//     RejectV6For / RejectQuicFor 此刻的结论,先 v6 拒绝、再 QUIC 拒绝、最后才是路由本身;带端口 / 网络条件的规则不写 QUIC 拒绝;
//   - 规则集标签按首次出现的顺序去重,订阅 / 节点站点直连的两份本地规则集不进这份清单。

const (
	rtSniff  = `{"action":"sniff"}`
	rtProto  = `{"protocol":"dns","action":"hijack-dns"}`
	rtIn     = `{"inbound":["dns-in"],"action":"hijack-dns"}`
	rtPriv   = `{"ip_is_private":true,"outbound":"direct"}`
	rtNTP    = `{"network":"udp","port":[123],"outbound":"direct"}`
	rtG      = `{"domain_regex":["^[^:]*[a-z][^:]*$"],"invert":true}` // 「连接没有域名」的前提
	rtQuic   = `{"network":"udp","port":443}`
	rtReject = `"action":"reject"`
	rtFlagFB = `{"rule_set":["obflip-fallback"]}`
	rtTun    = "172.19.0.0/30"
	rtMedia  = `{"id":"p-media","name":"流媒体","default":"美国","domainSuffix":["netflix.com"]}`
)

func rtList(parts ...string) string { return "[" + strings.Join(parts, ",") + "]" }

// rtAnd:一条 logical AND 规则,tail 是附在它上面的动作 / 出口(不带花括号)。
func rtAnd(tail string, subs ...string) string {
	return `{"type":"logical","mode":"and","rules":[` + strings.Join(subs, ",") + `],` + tail + `}`
}

func rtOut(match, out string) string {
	return strings.TrimSuffix(match, "}") + `,"outbound":"` + out + `"}`
}

func rtFlag(tag string) string { return `{"rule_set":["` + tag + `"]}` }

// rtRules:默认骨架(hijack)——sniff、两条接管、pre、ip_is_private、NTP、post。
func rtRules(pre []string, post ...string) string {
	parts := []string{rtSniff, rtProto, rtIn}
	parts = append(parts, pre...)
	parts = append(parts, rtPriv, rtNTP)
	parts = append(parts, post...)
	return rtList(parts...)
}

// rtFlips:出口是站点集 / 兜底时 v6、QUIC 拒绝各一条(match 是匹配条件,noDomain 带前提)。
func rtFlips(match, flag string, noDomain, v6, quic bool) []string {
	subs := []string{match}
	if noDomain {
		subs = append(subs, rtG)
	}
	var out []string
	if v6 {
		out = append(out, rtAnd(rtReject, append(append([]string{}, subs...), flag, `{"ip_version":6}`)...))
	}
	if quic {
		out = append(out, rtAnd(rtReject, append(append([]string{}, subs...), flag, rtQuic)...))
	}
	return out
}

func rtAll(string) bool { return true }

func rtSet(tags ...string) func(string) bool {
	return func(t string) bool {
		for _, e := range tags {
			if e == t {
				return true
			}
		}
		return false
	}
}

func rtKnown(tags ...string) map[string]bool {
	m := map[string]bool{}
	for _, t := range tags {
		m[t] = true
	}
	return m
}

type rtCase struct {
	name    string
	routing string // JSON,空 = 传 nil
	dir     string // 空 = /rs
	o       RouteOptions
	rules   string // 期望的 route.rules(JSON 数组文本)
	tags    []string
	final   string // 空 = 其他
}

func rtRun(t *testing.T, cases []rtCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var routing any
			if c.routing != "" {
				routing = mustJSON(t, c.routing)
			}
			dir := c.dir
			if dir == "" {
				dir = "/rs"
			}
			final := c.final
			if final == "" {
				final = "其他"
			}
			route, tags := BuildRoute(routing, dir, c.o)
			ruleSet := []any{}
			for _, tg := range c.tags {
				ruleSet = append(ruleSet, obj{"type": "local", "tag": tg, "format": "binary", "path": strings.TrimRight(dir, "/") + "/" + tg + ".srs"})
			}
			wantTags := c.tags
			if wantTags == nil {
				wantTags = []string{}
			}
			want := roundTrip(t, obj{
				"route": obj{
					"auto_detect_interface":   true,
					"default_domain_resolver": "dns-direct",
					"rule_set":                ruleSet,
					"rules":                   mustJSON(t, c.rules),
					"final":                   final,
				},
				"rulesetTags": wantTags,
			})
			got := roundTrip(t, obj{"route": route, "rulesetTags": tags})
			if !reflect.DeepEqual(got, want) {
				gb, _ := json.Marshal(got)
				wb, _ := json.Marshal(want)
				t.Errorf("输出不一致\n有: %s\n要: %s", gb, wb)
			}
		})
	}
}

func rtCustom(rows ...string) string {
	return `{"custom":{"enabled":true,"rules":[` + strings.Join(rows, ",") + `]}}`
}

func rtRow(typ, val, out string) string {
	return `{"type":"` + typ + `","value":"` + val + `","outbound":"` + out + `"}`
}

func rtPolicies(ps ...string) string { return `{"policies":[` + strings.Join(ps, ",") + `]}` }

// ---- 骨架与 DNS 接管 ----

func TestBuildRouteSkeletonAndDNS(t *testing.T) {
	hijackDirect := `{"inbound":["dns-in","dns-in-direct"],"action":"hijack-dns"}`
	dnsmasqRoute := `{"ip_cidr":["` + rtTun + `"],"port":[53],"action":"route","outbound":"dnsmasq-out","override_address":"127.0.0.1"}`
	tunReject := `{"ip_cidr":["` + rtTun + `"],"action":"reject"}`
	rtRun(t, []rtCase{
		{name: "空配置(nil 与 {}):hijack 缺省,只有骨架,没有规则集", rules: rtRules(nil)},
		{name: "空对象同上", routing: `{}`, rules: rtRules(nil)},
		{name: "模式写 hijack 与缺省相同", o: RouteOptions{DNSMode: "hijack"}, rules: rtRules(nil)},
		{name: "hijack:名单外终端的专用入站和 dns-in 一起接管", o: RouteOptions{DNSDirectInbound: "dns-in-direct"},
			rules: rtList(rtSniff, rtProto, hijackDirect, rtPriv, rtNTP)},
		{name: "off:只有 dns-in,专用入站不写", o: RouteOptions{DNSMode: "off", DNSDirectInbound: "dns-in-direct"},
			rules: rtList(rtSniff, rtIn, rtPriv, rtNTP)},
		{name: "dnsmasq:只写入站接管,没有协议接管", o: RouteOptions{DNSMode: "dnsmasq"},
			rules: rtList(rtSniff, rtIn, rtPriv, rtNTP)},
		{name: "dnsmasq:专用入站一起接管", o: RouteOptions{DNSMode: "dnsmasq", DNSDirectInbound: "dns-in-direct"},
			rules: rtList(rtSniff, hijackDirect, rtPriv, rtNTP)},
		{name: "dnsmasq + tun 网段 + dnsmasq 出站:先改写 53 端口再防回环 reject",
			o:     RouteOptions{DNSMode: "dnsmasq", TunCidrs: []string{rtTun}, DnsmasqTag: "dnsmasq-out"},
			rules: rtList(rtSniff, rtIn, dnsmasqRoute, tunReject, rtPriv, rtNTP)},
		{name: "dnsmasq 有 tun 网段但没有 dnsmasq 出站:只有 reject",
			o: RouteOptions{DNSMode: "dnsmasq", TunCidrs: []string{rtTun}}, rules: rtList(rtSniff, rtIn, tunReject, rtPriv, rtNTP)},
		{name: "dnsmasq 有出站但没有 tun 网段:两条都不写",
			o: RouteOptions{DNSMode: "dnsmasq", DnsmasqTag: "dnsmasq-out"}, rules: rtList(rtSniff, rtIn, rtPriv, rtNTP)},
		{name: "多个 tun 网段整体进同一条规则",
			o:     RouteOptions{DNSMode: "dnsmasq", TunCidrs: []string{rtTun, "fd00::/126"}, DnsmasqTag: "dnsmasq-out"},
			rules: rtList(rtSniff, rtIn, `{"ip_cidr":["`+rtTun+`","fd00::/126"],"port":[53],"action":"route","outbound":"dnsmasq-out","override_address":"127.0.0.1"}`, `{"ip_cidr":["`+rtTun+`","fd00::/126"],"action":"reject"}`, rtPriv, rtNTP)},
		{name: "hijack + tun 网段:不写 dnsmasq 改写,reject 排在接管后面",
			o: RouteOptions{TunCidrs: []string{rtTun}, DnsmasqTag: "dnsmasq-out"}, rules: rtRules([]string{tunReject})},
		{name: "off + tun 网段:同样只有 reject",
			o: RouteOptions{DNSMode: "off", TunCidrs: []string{rtTun}, DnsmasqTag: "dnsmasq-out"}, rules: rtList(rtSniff, rtIn, tunReject, rtPriv, rtNTP)},
		{name: "不认识的模式:没有任何 DNS 规则,tun 防回环照写",
			o: RouteOptions{DNSMode: "bogus", TunCidrs: []string{rtTun}}, rules: rtList(rtSniff, tunReject, rtPriv, rtNTP)},
		{name: "不认识的模式、没有 tun:只剩 sniff 与两条直连",
			o: RouteOptions{DNSMode: "bogus", DNSDirectInbound: "dns-in-direct"}, rules: rtList(rtSniff, rtPriv, rtNTP)},
		{name: "内置直连改名:ip_is_private 与 NTP 的出口跟着改",
			o:     RouteOptions{DirectTag: "直连", BlockTag: "拒绝"},
			rules: rtList(rtSniff, rtProto, rtIn, `{"ip_is_private":true,"outbound":"直连"}`, `{"network":"udp","port":[123],"outbound":"直连"}`)},
		{name: "兜底改名:final 取新名字", routing: `{"fallbackName":"我的兜底"}`, final: "我的兜底", rules: rtRules(nil)},
	})
}

// ---- 站点集 ----

func TestBuildRoutePolicies(t *testing.T) {
	g := rtG
	rtRun(t, []rtCase{
		{name: "域名后缀:一条规则,出口是站点集名字", routing: rtPolicies(rtMedia),
			rules: rtRules(nil, `{"domain_suffix":["netflix.com"],"outbound":"流媒体"}`)},
		{name: "domain / domainSuffix / domainKeyword 并存:合成一条",
			routing: rtPolicies(`{"id":"p-x","name":"X","domain":["a.example"],"domainSuffix":["b.example"],"domainKeyword":["cdn"]}`),
			rules:   rtRules(nil, `{"domain":["a.example"],"domain_suffix":["b.example"],"domain_keyword":["cdn"],"outbound":"X"}`)},
		{name: "只有规则集:引用它,清单里有它", routing: rtPolicies(`{"id":"p-x","name":"X","rulesets":["geosite-netflix"]}`),
			rules: rtRules(nil, `{"rule_set":["geosite-netflix"],"outbound":"X"}`), tags: []string{"geosite-netflix"}},
		{name: "规则集 + 域名:拆成紧邻的两条,规则集那条在前",
			routing: rtPolicies(`{"id":"p-x","name":"X","rulesets":["geosite-netflix"],"domain":["a.example"]}`),
			rules:   rtRules(nil, `{"rule_set":["geosite-netflix"],"outbound":"X"}`, `{"domain":["a.example"],"outbound":"X"}`), tags: []string{"geosite-netflix"}},
		{name: "只有 ip_cidr:logical AND,带「没有域名」前提",
			routing: rtPolicies(`{"id":"p-x","name":"X","ipCidr":["10.0.0.0/8","fd00::/8"]}`),
			rules:   rtRules(nil, rtAnd(`"outbound":"X"`, `{"ip_cidr":["10.0.0.0/8","fd00::/8"]}`, g))},
		{name: "只有 geoip 规则集:看 IP 的那份", routing: rtPolicies(`{"id":"p-x","name":"X","rulesets":["geoip-cn"]}`),
			rules: rtRules(nil, rtAnd(`"outbound":"X"`, `{"rule_set":["geoip-cn"]}`, g)), tags: []string{"geoip-cn"}},
		{name: "域名与 IP 都有:域名那份在前;两份各自再按规则集 / 条件拆开,IP 那两条各带前提",
			routing: rtPolicies(`{"id":"p-x","name":"X","rulesets":["geosite-cn","geoip-cn"],"domainSuffix":["a.cn"],"ipCidr":["1.0.0.0/8"]}`),
			rules: rtRules(nil,
				`{"rule_set":["geosite-cn"],"outbound":"X"}`, `{"domain_suffix":["a.cn"],"outbound":"X"}`,
				rtAnd(`"outbound":"X"`, `{"rule_set":["geoip-cn"]}`, g), rtAnd(`"outbound":"X"`, `{"ip_cidr":["1.0.0.0/8"]}`, g)),
			tags: []string{"geosite-cn", "geoip-cn"}},
		{name: "规则集链接没有形状表:按一份域名规则集引用",
			routing: rtPolicies(`{"id":"p-x","name":"X","ruleUrls":["` + urlA + `"]}`),
			rules:   rtRules(nil, `{"rule_set":["`+tagA+`"],"outbound":"X"}`), tags: []string{tagA}},
		{name: "规则集链接形状表有域名也有 IP:域名那条在前,IP 那条带前提;清单两份都有",
			routing: rtPolicies(`{"id":"p-x","name":"X","ruleUrls":["` + urlA + `"]}`),
			o:       RouteOptions{RuleLists: ruleLists{tagA: ruleShape{domain: true, ip: true}}},
			rules:   rtRules(nil, `{"rule_set":["`+tagA+`"],"outbound":"X"}`, rtAnd(`"outbound":"X"`, `{"rule_set":["`+tagA+`-ip"]}`, g)),
			tags:    []string{tagA, tagA + "-ip"}},
		{name: "规则集链接形状表只有 IP:只有 IP 那条",
			routing: rtPolicies(`{"id":"p-x","name":"X","ruleUrls":["` + urlA + `"]}`),
			o:       RouteOptions{RuleLists: ruleLists{tagA: ruleShape{ip: true}}},
			rules:   rtRules(nil, rtAnd(`"outbound":"X"`, `{"rule_set":["`+tagA+`-ip"]}`, g)), tags: []string{tagA + "-ip"}},
		{name: "多个站点集:按给定顺序逐条;停用的、没有条件的、叫「其他」的都不进",
			routing: rtPolicies(
				`{"id":"p-a","name":"A","domainSuffix":["a.example"]}`,
				`{"id":"p-off","name":"关着的","enabled":false,"domainSuffix":["off.example"]}`,
				`{"id":"p-empty","name":"空的"}`,
				`{"id":"p-fb","name":"其他","domainSuffix":["fb.example"]}`,
				`{"id":"p-b","name":"B","domainKeyword":["kw"]}`),
			rules: rtRules(nil, `{"domain_suffix":["a.example"],"outbound":"A"}`, `{"domain_keyword":["kw"],"outbound":"B"}`)},
		{name: "跨站点集重复的规则集只在清单里出现一次,各站点集的规则照引用",
			routing: rtPolicies(`{"id":"p-a","name":"A","rulesets":["geosite-a"]}`, `{"id":"p-b","name":"B","rulesets":["geosite-a","geosite-b"]}`),
			rules:   rtRules(nil, `{"rule_set":["geosite-a"],"outbound":"A"}`, `{"rule_set":["geosite-a","geosite-b"],"outbound":"B"}`),
			tags:    []string{"geosite-a", "geosite-b"}},
		{name: "站点集不看 KnownOutbounds:出口名字不在表里也照写",
			routing: rtPolicies(rtMedia), o: RouteOptions{KnownOutbounds: rtKnown("direct")},
			rules: rtRules(nil, `{"domain_suffix":["netflix.com"],"outbound":"流媒体"}`)},
	})
}

// ---- v6 / QUIC 拒绝 ----

func TestBuildRouteRejects(t *testing.T) {
	media := `{"domain_suffix":["netflix.com"]}`
	mediaRoute := `{"domain_suffix":["netflix.com"],"outbound":"流媒体"}`
	fb6 := rtAnd(rtReject, rtFlagFB, `{"ip_version":6}`)
	fbQ := rtAnd(rtReject, rtFlagFB, rtQuic)
	both := RouteOptions{RejectV6For: rtAll, RejectQuicFor: rtAll}
	v6Only := RouteOptions{RejectV6For: rtAll}
	quicOnly := RouteOptions{RejectQuicFor: rtAll}
	never := func(string) bool { return false }
	ipPol := rtPolicies(`{"id":"p-media","name":"流媒体","ipCidr":["10.0.0.0/8"]}`)
	ipMatch := `{"ip_cidr":["10.0.0.0/8"]}`
	v6, quic := `{"ip_version":6}`, rtQuic
	rtRun(t, []rtCase{
		{name: "站点集:v6、QUIC 拒绝各一条挂在它的开关上,路由本身排在最后;兜底也各一条",
			routing: rtPolicies(rtMedia), o: both,
			rules: rtRules(nil, append(append(rtFlips(media, rtFlag(flMedia), false, true, true), mediaRoute), fb6, fbQ)...)},
		{name: "只传 v6 函数:只有 v6 拒绝(站点集和兜底)", routing: rtPolicies(rtMedia), o: v6Only,
			rules: rtRules(nil, append(append(rtFlips(media, rtFlag(flMedia), false, true, false), mediaRoute), fb6)...)},
		{name: "只传 QUIC 函数:只有 QUIC 拒绝", routing: rtPolicies(rtMedia), o: quicOnly,
			rules: rtRules(nil, append(append(rtFlips(media, rtFlag(flMedia), false, false, true), mediaRoute), fbQ)...)},
		{name: "两个函数都不传:什么拒绝都不写", routing: rtPolicies(rtMedia), rules: rtRules(nil, mediaRoute)},
		{name: "函数的结论不影响站点集和兜底:全返回 false 也照写(开关挂着,翻面才生效)",
			routing: rtPolicies(rtMedia), o: RouteOptions{RejectV6For: never, RejectQuicFor: never},
			rules: rtRules(nil, append(append(rtFlips(media, rtFlag(flMedia), false, true, true), mediaRoute), fb6, fbQ)...)},
		{name: "站点集按 IP 判的规则:拒绝规则同样带「没有域名」前提,路由本身也是 AND",
			routing: ipPol, o: both,
			rules: rtRules(nil, append(append(rtFlips(ipMatch, rtFlag(flMedia), true, true, true), rtAnd(`"outbound":"流媒体"`, ipMatch, rtG)), fb6, fbQ)...)},
		{name: "兜底改名后开关不变(还是兜底的固定开关)", routing: `{"fallbackName":"兜底","policies":[` + rtMedia + `]}`, o: v6Only, final: "兜底",
			rules: rtRules(nil, append(append(rtFlips(media, rtFlag(flMedia), false, true, false), mediaRoute), fb6)...)},

		// 自定义分流与终端分流的出口不是站点集:按函数此刻的结论
		{name: "前置自定义分流出口是节点组:函数对它为真 → 先 v6、再 QUIC、最后路由",
			routing: rtCustom(rtRow("domainSuffix", "qq.com", "美国")), o: RouteOptions{RejectV6For: rtSet("美国"), RejectQuicFor: rtSet("美国")},
			rules: rtRules([]string{
				`{"domain_suffix":["qq.com"],"ip_version":6,"action":"reject"}`,
				`{"domain_suffix":["qq.com"],"network":"udp","port":443,"action":"reject"}`,
				`{"domain_suffix":["qq.com"],"outbound":"美国"}`}, fb6, fbQ)},
		{name: "函数对这个出口为假:不写拒绝(兜底的两条仍在)",
			routing: rtCustom(rtRow("domainSuffix", "qq.com", "美国")), o: RouteOptions{RejectV6For: rtSet("别的"), RejectQuicFor: rtSet("别的")},
			rules: rtRules([]string{`{"domain_suffix":["qq.com"],"outbound":"美国"}`}, fb6, fbQ)},
		{name: "v6 与 QUIC 各看各的函数", routing: rtCustom(rtRow("domainSuffix", "qq.com", "美国")),
			o:     RouteOptions{RejectV6For: rtSet("美国"), RejectQuicFor: rtSet("别的")},
			rules: rtRules([]string{`{"domain_suffix":["qq.com"],"ip_version":6,"action":"reject"}`, `{"domain_suffix":["qq.com"],"outbound":"美国"}`}, fb6, fbQ)},
		{name: "自定义行带端口:只有 v6 拒绝,没有 QUIC 拒绝(端口条件已经限定了)",
			routing: rtCustom(rtRow("port", "443,8000-9000", "美国")), o: RouteOptions{RejectV6For: rtSet("美国"), RejectQuicFor: rtSet("美国")},
			rules: rtRules([]string{
				`{"port":[443],"port_range":["8000:9000"],"ip_version":6,"action":"reject"}`,
				`{"port":[443],"port_range":["8000:9000"],"outbound":"美国"}`}, fb6, fbQ)},
		{name: "自定义行只有端口段:同样不写 QUIC 拒绝",
			routing: rtCustom(rtRow("port", "8000-9000", "美国")), o: RouteOptions{RejectV6For: rtSet("美国"), RejectQuicFor: rtSet("美国")},
			rules: rtRules([]string{
				`{"port_range":["8000:9000"],"ip_version":6,"action":"reject"}`,
				`{"port_range":["8000:9000"],"outbound":"美国"}`}, fb6, fbQ)},
		{name: "自定义行 ipCidr:拒绝规则是 AND(带前提),路由也是 AND",
			routing: rtCustom(rtRow("ipCidr", "10.0.0.0/8", "美国")), o: RouteOptions{RejectV6For: rtSet("美国"), RejectQuicFor: rtSet("美国")},
			rules: rtRules([]string{
				rtAnd(rtReject, ipMatch, rtG, v6), rtAnd(rtReject, ipMatch, rtG, quic), rtAnd(`"outbound":"美国"`, ipMatch, rtG)}, fb6, fbQ)},
		{name: "自定义行出口是站点集的名字:挂站点集的开关(不看函数结论),端口行只有 v6",
			routing: `{"custom":{"enabled":true,"rules":[` + rtRow("domainSuffix", "qq.com", "流媒体") + `,` + rtRow("port", "443", "流媒体") + `]},"policies":[` + rtMedia + `]}`,
			o:       RouteOptions{RejectV6For: never, RejectQuicFor: never},
			rules: rtRules(append(append(append(
				rtFlips(`{"domain_suffix":["qq.com"]}`, rtFlag(flMedia), false, true, true), `{"domain_suffix":["qq.com"],"outbound":"流媒体"}`),
				rtFlips(`{"port":[443]}`, rtFlag(flMedia), false, true, false)...), `{"port":[443],"outbound":"流媒体"}`),
				append(append(rtFlips(media, rtFlag(flMedia), false, true, true), mediaRoute), fb6, fbQ)...)},
		{name: "自定义行出口是兜底的名字:挂兜底的开关;兜底改名后旧名字就只是普通名字",
			routing: `{"fallbackName":"兜底","custom":{"enabled":true,"rules":[` + rtRow("domainSuffix", "a.example", "兜底") + `,` + rtRow("domainSuffix", "b.example", "其他") + `]}}`,
			o:       RouteOptions{RejectV6For: rtSet("其他")}, final: "兜底",
			rules: rtRules(append(append(
				rtFlips(`{"domain_suffix":["a.example"]}`, rtFlagFB, false, true, false), `{"domain_suffix":["a.example"],"outbound":"兜底"}`),
				`{"domain_suffix":["b.example"],"ip_version":6,"action":"reject"}`, `{"domain_suffix":["b.example"],"outbound":"其他"}`), fb6)},
		{name: "直连 / 拒绝出口:函数对它们为假时不写拒绝", routing: rtCustom(rtRow("domainSuffix", "a.example", "direct"), rtRow("domainSuffix", "b.example", "block")),
			o:     RouteOptions{RejectV6For: rtSet("美国"), RejectQuicFor: rtSet("美国")},
			rules: rtRules([]string{`{"domain_suffix":["a.example"],"outbound":"direct"}`, `{"domain_suffix":["b.example"],"outbound":"block"}`}, fb6, fbQ)},
		{name: "终端分流出口是节点组:来源条件上拼 v6 / QUIC 拒绝",
			o:     RouteOptions{RejectV6For: rtSet("美国"), RejectQuicFor: rtSet("美国"), ClientRoutes: []clientroutes.Route{tmIP("美国", "192.168.1.10/32", false)}},
			rules: rtRules(nil, `{"source_ip_cidr":["192.168.1.10/32"],"ip_version":6,"action":"reject"}`, `{"source_ip_cidr":["192.168.1.10/32"],"network":"udp","port":443,"action":"reject"}`, `{"source_ip_cidr":["192.168.1.10/32"],"outbound":"美国"}`, fb6, fbQ)},
		{name: "终端分流出口是站点集的名字:挂站点集的开关", routing: rtPolicies(rtMedia),
			o: RouteOptions{RejectV6For: never, ClientRoutes: []clientroutes.Route{tmMAC("流媒体", "aa:bb:cc:dd:ee:01", false)}},
			rules: rtRules(nil, append(append(
				rtFlips(`{"source_mac_address":["aa:bb:cc:dd:ee:01"]}`, rtFlag(flMedia), false, true, false), `{"source_mac_address":["aa:bb:cc:dd:ee:01"],"outbound":"流媒体"}`),
				append(append(rtFlips(media, rtFlag(flMedia), false, true, false), mediaRoute), fb6)...)...)},
		{name: "订阅 / 节点站点直连、private、NTP 不挂拒绝规则",
			o:     RouteOptions{RejectV6For: rtAll, RejectQuicFor: rtAll, DirectHostSets: &DirectHostSets{Domain: "hs-dom", IP: "hs-ip"}},
			rules: rtRules(nil, `{"rule_set":["hs-dom","hs-ip"],"outbound":"direct"}`, fb6, fbQ)},
	})
}

// ---- 前置自定义分流 ----

func TestBuildRouteCustom(t *testing.T) {
	g := rtG
	rtRun(t, []rtCase{
		{name: "各类行:域名 / 后缀 / 关键词 / ip / 端口 / geosite / geoip / 规则集 / 规则集链接;直连 / 拒绝是记号",
			routing: rtCustom(
				rtRow("domain", "a.example", "direct"), rtRow("domainSuffix", "b.example", "block"), rtRow("domainKeyword", "c", "美国"),
				rtRow("ipCidr", "10.0.0.0/8", "美国"), rtRow("port", "443,8000-9000", "美国"), rtRow("geosite", "netflix", "美国"),
				rtRow("geoip", "cn", "direct"), rtRow("ruleset", "geosite-x", "美国"), rtRow("ruleUrl", urlA, "美国")),
			rules: rtRules([]string{
				`{"domain":["a.example"],"outbound":"direct"}`, `{"domain_suffix":["b.example"],"outbound":"block"}`, `{"domain_keyword":["c"],"outbound":"美国"}`,
				rtAnd(`"outbound":"美国"`, `{"ip_cidr":["10.0.0.0/8"]}`, g), `{"port":[443],"port_range":["8000:9000"],"outbound":"美国"}`,
				`{"rule_set":["geosite-netflix"],"outbound":"美国"}`, rtAnd(`"outbound":"direct"`, `{"rule_set":["geoip-cn"]}`, g),
				`{"rule_set":["geosite-x"],"outbound":"美国"}`, `{"rule_set":["` + tagA + `"],"outbound":"美国"}`}),
			tags: []string{"geosite-netflix", "geoip-cn", "geosite-x", tagA}},
		{name: "端口写法:单个 / 范围 / 逗号和空白混用,单口与范围各成一个字段",
			routing: rtCustom(rtRow("port", "53", "美国"), rtRow("port", "1000-2000", "美国"), rtRow("port", "80, 443 8080-8090", "美国")),
			rules: rtRules([]string{`{"port":[53],"outbound":"美国"}`, `{"port_range":["1000:2000"],"outbound":"美国"}`,
				`{"port":[80,443],"port_range":["8080:8090"],"outbound":"美国"}`})},
		{name: "规则集链接遇形状表:域名、IP 两份各一条(域名在前,IP 那条带前提),只有 IP 的只有一条;清单顺序按出现",
			routing: rtCustom(rtRow("ruleUrl", urlA, "美国"), rtRow("ruleUrl", urlAds, "direct")),
			o:       RouteOptions{RuleLists: ruleLists{tagA: ruleShape{domain: true, ip: true}, tagAds: ruleShape{ip: true}}},
			rules: rtRules([]string{`{"rule_set":["` + tagA + `"],"outbound":"美国"}`, rtAnd(`"outbound":"美国"`, `{"rule_set":["`+tagA+`-ip"]}`, g),
				rtAnd(`"outbound":"direct"`, `{"rule_set":["`+tagAds+`-ip"]}`, g)}),
			tags: []string{tagA, tagA + "-ip", tagAds + "-ip"}},
		{name: "规则集行的 geoip 记号走 IP 那份、带前提;geosite 走域名那份",
			routing: rtCustom(rtRow("ruleset", "geoip-us", "美国"), rtRow("ruleset", "geosite-us", "美国")),
			rules:   rtRules([]string{rtAnd(`"outbound":"美国"`, `{"rule_set":["geoip-us"]}`, g), `{"rule_set":["geosite-us"],"outbound":"美国"}`}),
			tags:    []string{"geoip-us", "geosite-us"}},
		{name: "KnownOutbounds 非 nil:出口不在表里的行整条跳过,它的规则集也不进清单",
			routing: rtCustom(rtRow("geosite", "a", "美国"), rtRow("geosite", "b", "不存在"), rtRow("domain", "c.example", "direct")),
			o:       RouteOptions{KnownOutbounds: rtKnown("direct", "美国")},
			rules:   rtRules([]string{`{"rule_set":["geosite-a"],"outbound":"美国"}`, `{"domain":["c.example"],"outbound":"direct"}`}), tags: []string{"geosite-a"}},
		{name: "KnownOutbounds 里没有 direct / block 时对应的行也被跳过(先换成实际名字再查)",
			routing: rtCustom(rtRow("domain", "a.example", "direct"), rtRow("domain", "b.example", "block"), rtRow("domain", "c.example", "美国")),
			o:       RouteOptions{KnownOutbounds: rtKnown("美国", "block")},
			rules:   rtRules([]string{`{"domain":["b.example"],"outbound":"block"}`, `{"domain":["c.example"],"outbound":"美国"}`})},
		{name: "KnownOutbounds 为空表(非 nil):所有自定义行都被跳过", routing: rtCustom(rtRow("domain", "a.example", "direct")),
			o: RouteOptions{KnownOutbounds: map[string]bool{}}, rules: rtRules(nil)},
		{name: "内置直连 / 拒绝改名:direct / block 记号换成新名字,新名字本身也认;核对表按新名字查",
			routing: rtCustom(rtRow("domain", "a.example", "direct"), rtRow("domain", "b.example", "block"), rtRow("domain", "c.example", "直连"), rtRow("domain", "d.example", "拒绝")),
			o:       RouteOptions{DirectTag: "直连", BlockTag: "拒绝", KnownOutbounds: rtKnown("直连", "拒绝")},
			rules: rtList(rtSniff, rtProto, rtIn, `{"domain":["a.example"],"outbound":"直连"}`, `{"domain":["b.example"],"outbound":"拒绝"}`,
				`{"domain":["c.example"],"outbound":"直连"}`, `{"domain":["d.example"],"outbound":"拒绝"}`,
				`{"ip_is_private":true,"outbound":"直连"}`, `{"network":"udp","port":[123],"outbound":"直连"}`)},
		{name: "没改名时「直连」「拒绝」只是普通名字", routing: rtCustom(rtRow("domain", "a.example", "直连")),
			rules: rtRules([]string{`{"domain":["a.example"],"outbound":"直连"}`})},
		{name: "停用的自定义分流不写", routing: `{"custom":{"enabled":false,"rules":[` + rtRow("domain", "a.example", "direct") + `]}}`, rules: rtRules(nil)},
		{name: "启用但没有规则:不写", routing: `{"custom":{"enabled":true,"rules":[]}}`, rules: rtRules(nil)},
		{name: "写错的行在整理时丢掉:类型不认、值为空、端口写错、链接不是 http",
			routing: rtCustom(rtRow("bogus", "x", "direct"), rtRow("domain", "", "direct"), rtRow("port", "abc", "direct"), rtRow("ruleUrl", "ftp://x", "direct"), rtRow("domain", "ok.example", "direct")),
			rules:   rtRules([]string{`{"domain":["ok.example"],"outbound":"direct"}`})},
		{name: "自定义分流排在 DNS 接管与 tun 防回环之后、ip_is_private 之前",
			routing: rtCustom(rtRow("domain", "a.example", "direct")), o: RouteOptions{TunCidrs: []string{rtTun}},
			rules: rtRules([]string{`{"ip_cidr":["` + rtTun + `"],"action":"reject"}`, `{"domain":["a.example"],"outbound":"direct"}`})},
		{name: "自定义分流排在站点集前面,规则集清单也是自定义的先", routing: `{"custom":{"enabled":true,"rules":[` + rtRow("geosite", "a", "美国") + `]},"policies":[{"id":"p-x","name":"X","rulesets":["geosite-b","geosite-a"]}]}`,
			rules: rtRules([]string{`{"rule_set":["geosite-a"],"outbound":"美国"}`}, `{"rule_set":["geosite-b","geosite-a"],"outbound":"X"}`),
			tags:  []string{"geosite-a", "geosite-b"}},
	})
}

// ---- 订阅 / 节点站点直连、终端分流 ----

func TestBuildRouteDirectHostsAndClients(t *testing.T) {
	sets := &DirectHostSets{Domain: "hs-dom", IP: "hs-ip"}
	setRule := `{"rule_set":["hs-dom","hs-ip"],"outbound":"direct"}`
	ip10, mac1 := "192.168.1.10/32", "aa:bb:cc:dd:ee:01"
	rtRun(t, []rtCase{
		{name: "规则集形态:一条规则引用两份本地规则集,排在 NTP 后面;这两份不进规则集清单",
			o: RouteOptions{DirectHostSets: sets}, rules: rtRules(nil, setRule)},
		{name: "规则集形态遇到改名的直连:出口跟着改", o: RouteOptions{DirectHostSets: sets, DirectTag: "直连"},
			rules: rtList(rtSniff, rtProto, rtIn, `{"ip_is_private":true,"outbound":"直连"}`, `{"network":"udp","port":[123],"outbound":"直连"}`, `{"rule_set":["hs-dom","hs-ip"],"outbound":"直连"}`)},
		{name: "直接给地址:域名与网段各一个字段,原样带出", o: RouteOptions{DirectHosts: &DirectHosts{Domains: []string{"b.example", "a.example"}, Cidrs: []string{"1.2.3.0/24"}}},
			rules: rtRules(nil, `{"domain":["b.example","a.example"],"ip_cidr":["1.2.3.0/24"],"outbound":"direct"}`)},
		{name: "直接给地址:只有域名", o: RouteOptions{DirectHosts: &DirectHosts{Domains: []string{"a.example"}}},
			rules: rtRules(nil, `{"domain":["a.example"],"outbound":"direct"}`)},
		{name: "直接给地址:只有网段", o: RouteOptions{DirectHosts: &DirectHosts{Cidrs: []string{"1.2.3.0/24"}}},
			rules: rtRules(nil, `{"ip_cidr":["1.2.3.0/24"],"outbound":"direct"}`)},
		{name: "直接给地址:两边都空(含空切片)不写规则", o: RouteOptions{DirectHosts: &DirectHosts{Domains: []string{}, Cidrs: []string{}}}, rules: rtRules(nil)},
		{name: "直接给地址:零值不写规则", o: RouteOptions{DirectHosts: &DirectHosts{}}, rules: rtRules(nil)},
		{name: "两种形态都给:只用规则集那份", o: RouteOptions{DirectHostSets: sets, DirectHosts: &DirectHosts{Domains: []string{"a.example"}}}, rules: rtRules(nil, setRule)},
		{name: "直接给地址遇到改名的直连", o: RouteOptions{DirectTag: "直连", DirectHosts: &DirectHosts{Domains: []string{"a.example"}}},
			rules: rtList(rtSniff, rtProto, rtIn, `{"ip_is_private":true,"outbound":"直连"}`, `{"network":"udp","port":[123],"outbound":"直连"}`, `{"domain":["a.example"],"outbound":"直连"}`)},

		{name: "终端分流:按 IP 来源 / 按 MAC,出口原样,顺序按给定",
			o:     RouteOptions{ClientRoutes: []clientroutes.Route{tmIP("美国", ip10, false), tmMAC("direct", mac1, false)}},
			rules: rtRules(nil, `{"source_ip_cidr":["`+ip10+`"],"outbound":"美国"}`, `{"source_mac_address":["`+mac1+`"],"outbound":"direct"}`)},
		{name: "终端分流:多个来源 / 多个 MAC 进同一条规则",
			o: RouteOptions{ClientRoutes: []clientroutes.Route{
				{ID: "a", Match: "ip", Outbound: "美国", Sources: []string{"192.168.1.10/32", "192.168.2.0/24"}},
				{ID: "b", Match: "mac", Outbound: "美国", Sources: []string{}, Macs: []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"}}}},
			rules: rtRules(nil, `{"source_ip_cidr":["192.168.1.10/32","192.168.2.0/24"],"outbound":"美国"}`, `{"source_mac_address":["aa:bb:cc:dd:ee:01","aa:bb:cc:dd:ee:02"],"outbound":"美国"}`)},
		{name: "终端分流:没有出口、没有来源(IP / MAC)的整条跳过",
			o: RouteOptions{ClientRoutes: []clientroutes.Route{
				{ID: "a", Match: "ip", Sources: []string{ip10}},
				{ID: "b", Match: "ip", Outbound: "direct", Sources: []string{}},
				{ID: "c", Match: "mac", Outbound: "direct", Sources: []string{}},
				{ID: "d", Match: "mac", Outbound: "direct", Sources: []string{ip10}},
				tmIP("美国", "192.168.1.11/32", false)}},
			rules: rtRules(nil, `{"source_ip_cidr":["192.168.1.11/32"],"outbound":"美国"}`)},
		{name: "终端分流:按 MAC 的只看 MAC,按 IP 的只看来源(不混用)",
			o: RouteOptions{ClientRoutes: []clientroutes.Route{
				{ID: "a", Match: "ip", Outbound: "direct", Sources: []string{ip10}, Macs: []string{mac1}},
				{ID: "b", Match: "mac", Outbound: "direct", Sources: []string{ip10}, Macs: []string{mac1}}}},
			rules: rtRules(nil, `{"source_ip_cidr":["`+ip10+`"],"outbound":"direct"}`, `{"source_mac_address":["`+mac1+`"],"outbound":"direct"}`)},
		{name: "终端分流:KnownOutbounds 非 nil 时出口不在表里的整条跳过",
			o:     RouteOptions{KnownOutbounds: rtKnown("direct", "美国"), ClientRoutes: []clientroutes.Route{tmIP("美国", ip10, false), tmIP("不存在", "192.168.1.11/32", false), tmIP("direct", "192.168.1.12/32", false)}},
			rules: rtRules(nil, `{"source_ip_cidr":["`+ip10+`"],"outbound":"美国"}`, `{"source_ip_cidr":["192.168.1.12/32"],"outbound":"direct"}`)},
		{name: "终端分流:KnownOutbounds 为空表(非 nil)时所有终端分流都被跳过",
			o:     RouteOptions{KnownOutbounds: map[string]bool{}, ClientRoutes: []clientroutes.Route{tmIP("direct", ip10, false), tmMAC("美国", mac1, false)}},
			rules: rtRules(nil)},
		{name: "终端分流:「不进内核」的终端(Bypass)路由规则里照写,它由入口旁路,这里不特殊对待",
			o:     RouteOptions{ClientRoutes: []clientroutes.Route{tmIP("direct", ip10, true)}},
			rules: rtRules(nil, `{"source_ip_cidr":["`+ip10+`"],"outbound":"direct"}`)},
		{name: "整体顺序:NTP → 订阅 / 节点站点直连 → 终端分流 → 站点集",
			routing: rtPolicies(rtMedia), o: RouteOptions{DirectHostSets: sets, ClientRoutes: []clientroutes.Route{tmIP("美国", ip10, false)}},
			rules: rtRules(nil, setRule, `{"source_ip_cidr":["`+ip10+`"],"outbound":"美国"}`, `{"domain_suffix":["netflix.com"],"outbound":"流媒体"}`)},
	})
}

// ---- 首包预判放行接进 BuildRoute ----

func TestBuildRoutePreMatchBypass(t *testing.T) {
	keep := `{"ip_cidr":["` + rtTun + `"],"invert":true},{"port":[53],"invert":true}`
	byp := func(first string) string {
		return `{"type":"logical","mode":"and","rules":[` + first + `,` + keep + `],"action":"bypass"}`
	}
	tunReject := `{"ip_cidr":["` + rtTun + `"],"action":"reject"}`
	sets := &DirectHostSets{Domain: "hs-dom", IP: "hs-ip"}
	routes := []clientroutes.Route{tmIP("direct", "192.168.1.20/32", false), tmIP("美国", "192.168.1.21/32", false)}
	rtRun(t, []rtCase{
		{name: "开着:规则集那份 IP、直连终端各一条放行,排在 sniff 前面;其余规则照旧",
			o: RouteOptions{PreMatchBypass: true, DirectHostSets: sets, TunCidrs: []string{rtTun}, ClientRoutes: routes},
			rules: rtList(byp(`{"rule_set":["hs-ip"]}`), byp(`{"source_ip_cidr":["192.168.1.20/32"]}`), rtSniff, rtProto, rtIn, tunReject, rtPriv, rtNTP,
				`{"rule_set":["hs-dom","hs-ip"],"outbound":"direct"}`, `{"source_ip_cidr":["192.168.1.20/32"],"outbound":"direct"}`, `{"source_ip_cidr":["192.168.1.21/32"],"outbound":"美国"}`)},
		{name: "没开:同样的输入不写放行规则",
			o: RouteOptions{DirectHostSets: sets, TunCidrs: []string{rtTun}, ClientRoutes: routes},
			rules: rtList(rtSniff, rtProto, rtIn, tunReject, rtPriv, rtNTP,
				`{"rule_set":["hs-dom","hs-ip"],"outbound":"direct"}`, `{"source_ip_cidr":["192.168.1.20/32"],"outbound":"direct"}`, `{"source_ip_cidr":["192.168.1.21/32"],"outbound":"美国"}`)},
		{name: "开着但没有可放行的(没有直连终端、没有站点直连):不写", o: RouteOptions{PreMatchBypass: true, ClientRoutes: routes[1:]},
			rules: rtRules(nil, `{"source_ip_cidr":["192.168.1.21/32"],"outbound":"美国"}`)},
		{name: "直接给的地址形态:没有 IP 那份规则集时用地址里的网段",
			o:     RouteOptions{PreMatchBypass: true, DirectHosts: &DirectHosts{Domains: []string{"a.example"}, Cidrs: []string{"1.2.3.0/24"}}},
			rules: rtList(`{"type":"logical","mode":"and","rules":[{"ip_cidr":["1.2.3.0/24"]},{"port":[53],"invert":true}],"action":"bypass"}`, rtSniff, rtProto, rtIn, rtPriv, rtNTP, `{"domain":["a.example"],"ip_cidr":["1.2.3.0/24"],"outbound":"direct"}`)},
		{name: "内置直连改名:按新名字认直连终端",
			o: RouteOptions{PreMatchBypass: true, DirectTag: "直连", ClientRoutes: []clientroutes.Route{tmIP("直连", "192.168.1.20/32", false), tmIP("direct", "192.168.1.22/32", false)}},
			rules: rtList(`{"type":"logical","mode":"and","rules":[{"source_ip_cidr":["192.168.1.20/32"]},{"port":[53],"invert":true}],"action":"bypass"}`,
				rtSniff, rtProto, rtIn, `{"ip_is_private":true,"outbound":"直连"}`, `{"network":"udp","port":[123],"outbound":"直连"}`,
				`{"source_ip_cidr":["192.168.1.20/32"],"outbound":"直连"}`, `{"source_ip_cidr":["192.168.1.22/32"],"outbound":"direct"}`)},
		{name: "规则集链接的形状表也传给放行:域名、IP 两份都从放行里挖掉,与路由里的两份对得上",
			routing: rtCustom(rtRow("ruleUrl", urlA, "美国")),
			o:       RouteOptions{PreMatchBypass: true, RuleLists: ruleLists{tagA: ruleShape{domain: true, ip: true}}, ClientRoutes: routes[:1]},
			rules: rtList(`{"type":"logical","mode":"and","rules":[{"source_ip_cidr":["192.168.1.20/32"]},{"rule_set":["`+tagA+`","`+tagA+`-ip"],"invert":true},{"port":[53],"invert":true}],"action":"bypass"}`,
				rtSniff, rtProto, rtIn,
				`{"rule_set":["`+tagA+`"],"outbound":"美国"}`, rtAnd(`"outbound":"美国"`, `{"rule_set":["`+tagA+`-ip"]}`, rtG),
				rtPriv, rtNTP, `{"source_ip_cidr":["192.168.1.20/32"],"outbound":"direct"}`),
			tags: []string{tagA, tagA + "-ip"}},
		{name: "前置自定义分流里走代理的行把它的网段、规则集、端口从放行里挖掉;出口不在表里的行不算",
			routing: rtCustom(rtRow("ipCidr", "10.0.0.0/8", "美国"), rtRow("ipCidr", "11.0.0.0/8", "不存在"), rtRow("geosite", "netflix", "美国"), rtRow("port", "443", "美国")),
			o:       RouteOptions{PreMatchBypass: true, KnownOutbounds: rtKnown("direct", "美国"), ClientRoutes: routes[:1]},
			rules: rtList(`{"type":"logical","mode":"and","rules":[{"source_ip_cidr":["192.168.1.20/32"]},{"ip_cidr":["10.0.0.0/8"],"invert":true},{"rule_set":["geosite-netflix"],"invert":true},{"port":[53,443],"invert":true}],"action":"bypass"}`,
				rtSniff, rtProto, rtIn,
				rtAnd(`"outbound":"美国"`, `{"ip_cidr":["10.0.0.0/8"]}`, rtG), `{"rule_set":["geosite-netflix"],"outbound":"美国"}`, `{"port":[443],"outbound":"美国"}`,
				rtPriv, rtNTP, `{"source_ip_cidr":["192.168.1.20/32"],"outbound":"direct"}`),
			tags: []string{"geosite-netflix"}},
	})
}

// ---- 入参不被改写、结果可重复 ----

func TestBuildRouteDoesNotMutateInput(t *testing.T) {
	routing := mustJSON(t, rtCustom(rtRow("ipCidr", "10.0.0.0/8", "美国"), rtRow("domain", "a.example", "direct")))
	routingBefore, _ := json.Marshal(routing)
	tun := make([]string, 1, 8)
	tun[0] = rtTun
	hosts := &DirectHosts{Domains: []string{"a.example"}, Cidrs: []string{"1.2.3.0/24"}}
	macs := []string{"aa:bb:cc:dd:ee:01"}
	srcs := []string{"192.168.1.10/32"}
	o := RouteOptions{
		DNSMode: "dnsmasq", TunCidrs: tun, DnsmasqTag: "dnsmasq-out", DirectHosts: hosts, PreMatchBypass: true,
		RejectV6For: rtAll, RejectQuicFor: rtAll,
		ClientRoutes: []clientroutes.Route{
			{ID: "m", Match: "mac", Outbound: "direct", Sources: []string{}, Macs: macs},
			{ID: "i", Match: "ip", Outbound: "美国", Sources: srcs},
		},
	}
	r1, t1 := BuildRoute(routing, "/rs", o)
	r2, t2 := BuildRoute(routing, "/rs", o)
	a := roundTrip(t, obj{"r": r1, "t": t1})
	if b := roundTrip(t, obj{"r": r2, "t": t2}); !reflect.DeepEqual(a, b) {
		t.Errorf("两次调用结果不同")
	}
	if after, _ := json.Marshal(routing); string(after) != string(routingBefore) {
		t.Errorf("routing 入参被改写:\n%s\n%s", routingBefore, after)
	}
	if !reflect.DeepEqual(tun[:2], []string{rtTun, ""}) {
		t.Errorf("TunCidrs 的底层数组被写穿:%v", tun[:2])
	}
	if !reflect.DeepEqual(hosts, &DirectHosts{Domains: []string{"a.example"}, Cidrs: []string{"1.2.3.0/24"}}) ||
		!reflect.DeepEqual(macs, []string{"aa:bb:cc:dd:ee:01"}) || !reflect.DeepEqual(srcs, []string{"192.168.1.10/32"}) {
		t.Errorf("直连地址 / 终端来源被改写:%v %v %v", hosts, macs, srcs)
	}
	for i, cr := range o.ClientRoutes {
		if cr.Outbound == "" {
			t.Errorf("终端分流 #%d 被改写", i)
		}
	}
}

// ---- route 段的外形 ----

func TestBuildRouteShape(t *testing.T) {
	routing := mustJSON(t, rtPolicies(`{"id":"p-x","name":"X","rulesets":["geosite-a","geoip-b"]}`))
	route, tags := BuildRoute(routing, "/data/rs/", RouteOptions{})
	got := roundTrip(t, route)
	want := mustJSON(t, `{
		"auto_detect_interface": true,
		"default_domain_resolver": "dns-direct",
		"rule_set": [
			{"type":"local","tag":"geosite-a","format":"binary","path":"/data/rs/geosite-a.srs"},
			{"type":"local","tag":"geoip-b","format":"binary","path":"/data/rs/geoip-b.srs"}
		],
		"rules": `+rtRules(nil, `{"rule_set":["geosite-a"],"outbound":"X"}`, rtAnd(`"outbound":"X"`, `{"rule_set":["geoip-b"]}`, rtG))+`,
		"final": "其他"
	}`)
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		t.Errorf("route 段外形不一致\n有: %s\n要: %s", gb, wb)
	}
	if !reflect.DeepEqual(tags, []string{"geosite-a", "geoip-b"}) {
		t.Errorf("规则集清单 = %v", tags)
	}
	// 没有规则集时:清单与 rule_set 都是空数组(序列化成 [] 而不是 null)
	route, tags = BuildRoute(nil, "/rs", RouteOptions{})
	if b, _ := json.Marshal(route["rule_set"]); string(b) != "[]" {
		t.Errorf("空的 rule_set 序列化为 %s", b)
	}
	if b, _ := json.Marshal(tags); string(b) != "[]" {
		t.Errorf("空的清单序列化为 %s", b)
	}
	if route["final"] != "其他" {
		t.Errorf("final = %v", route["final"])
	}
}

// ---- 各种出口一起叠加时的整体顺序 ----

func TestBuildRouteFullOrder(t *testing.T) {
	media := `{"domain_suffix":["netflix.com"]}`
	ip10 := "192.168.1.10/32"
	o := RouteOptions{
		DNSMode: "dnsmasq", DNSDirectInbound: "dns-in-direct", TunCidrs: []string{rtTun}, DnsmasqTag: "dnsmasq-out",
		DirectHostSets: &DirectHostSets{Domain: "hs-dom", IP: "hs-ip"},
		ClientRoutes:   []clientroutes.Route{tmIP("美国", ip10, false)},
		RejectV6For:    rtAll, RejectQuicFor: rtAll,
	}
	rules := rtList(
		rtSniff,
		`{"inbound":["dns-in","dns-in-direct"],"action":"hijack-dns"}`,
		`{"ip_cidr":["`+rtTun+`"],"port":[53],"action":"route","outbound":"dnsmasq-out","override_address":"127.0.0.1"}`,
		`{"ip_cidr":["`+rtTun+`"],"action":"reject"}`,
		`{"domain_suffix":["qq.com"],"ip_version":6,"action":"reject"}`,
		`{"domain_suffix":["qq.com"],"network":"udp","port":443,"action":"reject"}`,
		`{"domain_suffix":["qq.com"],"outbound":"direct"}`,
		rtPriv, rtNTP,
		`{"rule_set":["hs-dom","hs-ip"],"outbound":"direct"}`,
		`{"source_ip_cidr":["`+ip10+`"],"ip_version":6,"action":"reject"}`,
		`{"source_ip_cidr":["`+ip10+`"],"network":"udp","port":443,"action":"reject"}`,
		`{"source_ip_cidr":["`+ip10+`"],"outbound":"美国"}`,
		rtAnd(rtReject, media, rtFlag(flMedia), `{"ip_version":6}`),
		rtAnd(rtReject, media, rtFlag(flMedia), rtQuic),
		`{"domain_suffix":["netflix.com"],"outbound":"流媒体"}`,
		rtAnd(rtReject, rtFlagFB, `{"ip_version":6}`),
		rtAnd(rtReject, rtFlagFB, rtQuic),
	)
	rtRun(t, []rtCase{{
		name:    "dnsmasq + 全部块",
		routing: `{"custom":{"enabled":true,"rules":[` + rtRow("domainSuffix", "qq.com", "direct") + `]},"policies":[` + rtMedia + `]}`,
		o:       o, rules: rules,
	}})
}
