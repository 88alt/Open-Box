package routing

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/protocol"
)

// 自写测试向量:entryPlan(系统入口 nft 的进内核策略)。输入自己拟,期望值按下面的规则人工推算:
//   - 选项缺省:members [direct]、内置缺省、enabled 为真、dnsMode dnsmasq、fakeIp 为假;传了就按 JS 真假判断
//     (enabled: null 也算关;members / builtin / selections 传 null 算没传;dnsMode 只有恰好是字符串 "off" 才算 DNS 禁用);
//   - 先看前提,哪条不满足就退回 blacklist,原因按下面的顺序取第一条:
//     没开直连不进内核 → 没开 FakeIP → DNS 禁用 → 兜底不走直连 → 有终端被指定走代理 → 前置自定义分流的端口行 / 规则集行;
//     blacklist 的 fakeIp 是「FakeIP 开着且 DNS 没禁用」,needSets / needCidrs 恒为 [],directAnswer 见下;
//   - directAnswer(只在 blacklist 里有意义,whitelist 恒为 true):选项开着、FakeIP 开着、DNS 没禁用,
//     且前置自定义分流(启用且有规则)里没有出口不是内置直连的端口行;
//   - 全部满足 → whitelist:前置自定义分流里出口不是直连的行,ipCidr 照写进 needCidrs(不去重)、geoip 进 needSets(去重),
//     域名 / 后缀 / 关键词 / geosite 行不产生真实 IP 不管;再按站点集顺序,此刻不走直连的站点集里 geoip- 规则集进 needSets(去重)、
//     ipCidr 进 needCidrs(与已有的完全相同才去重,含自定义分流那几条),别的规则集(geosite- / 规则集链接)不管。

const (
	entOff      = "后端设置里关掉了「直连不进内核」:所有流量都进内核,入口不放行"
	entNoFake   = "没开 FakeIP:走代理的域名解析出来是真实 IP,入口分不出它和直连目标,不能默认放行"
	entDNSOff   = "DNS 禁用模式下终端的查询不一定经内核,走代理的域名拿不到占位地址,不能默认放行"
	entProxyCli = "有终端被指定走代理,该终端的全部流量都要进内核"
	entWhite    = "FakeIP 开着、兜底直连:入口默认放行,只有 FakeIP 占位池和走代理 / 拒绝的 IP 集合进内核"
	entUrl      = "https://example.com/a.list"
)

func entFallback(name string) string {
	return "兜底「" + name + "」走代理:没命中的目标都要进内核,入口没法默认放行"
}
func entPort(v string) string {
	return "前置自定义分流「" + v + "」按端口分流,入口按 IP 判不了"
}
func entList(v string) string {
	return "前置自定义分流「" + v + "」是规则集链接 / 规则集,里面有没有 IP 段说不清"
}

func entWant(mode, reason string, fakeIP bool, sets, cidrs []string, da bool) string {
	if sets == nil {
		sets = []string{}
	}
	if cidrs == nil {
		cidrs = []string{}
	}
	b, _ := json.Marshal(map[string]any{"mode": mode, "reason": reason, "fakeIp": fakeIP, "needSets": sets, "needCidrs": cidrs, "directAnswer": da})
	return string(b)
}

func entBlack(reason string, fakeIP, da bool) string {
	return entWant("blacklist", reason, fakeIP, nil, nil, da)
}

func entRun(t *testing.T, routing, opts string) any {
	t.Helper()
	clsNoGeo()
	if routing == "" {
		routing = `{"fallbackDefault":"direct"}`
	}
	if opts == "" {
		opts = `null`
	}
	return roundTrip(t, entryPlan(mustJSON(t, routing), mustJSON(t, opts)))
}

func entCheck(t *testing.T, name string, got any, want string) {
	t.Helper()
	clsCheck(t, name, got, want)
}

// 满足 whitelist 前提的选项(FakeIP 开着、成员里有节点)
const entOK = `"members":["direct","block","美国","日本"],"fakeIp":true`

func entOpts(extra ...string) string {
	return `{` + strings.Join(append([]string{entOK}, extra...), ",") + `}`
}

func TestEntryPlanGateOrder(t *testing.T) {
	proxyFB := `{"fallbackDefault":"美国"}`
	portRule := `"custom":{"enabled":true,"rules":[{"type":"port","value":"443","outbound":"美国"}]}`
	proxyCli := `"clientRoutes":[{"ip":"192.168.1.5","outbound":"美国"}]`
	for _, c := range []struct {
		name, routing, opts, want string
	}{
		{"直连不进内核关着(FakeIP 开着也一样),directAnswer 关", "", entOpts(`"enabled":false`), entBlack(entOff, true, false)},
		{"关着且 FakeIP 没开:先报关着", "", `{"enabled":false}`, entBlack(entOff, false, false)},
		{"没开 FakeIP", "", `{"members":["direct","美国"]}`, entBlack(entNoFake, false, false)},
		{"没开 FakeIP 且 DNS 禁用:先报没开 FakeIP", "", `{"dnsMode":"off"}`, entBlack(entNoFake, false, false)},
		{"DNS 禁用", "", entOpts(`"dnsMode":"off"`), entBlack(entDNSOff, false, false)},
		{"DNS 禁用且兜底走代理:先报 DNS 禁用", proxyFB, entOpts(`"dnsMode":"off"`), entBlack(entDNSOff, false, false)},
		{"兜底走代理(FakeIP 开着,directAnswer 开)", proxyFB, entOpts(), entBlack(entFallback("其他"), true, true)},
		{"兜底走代理且有终端走代理:先报兜底", proxyFB, entOpts(proxyCli), entBlack(entFallback("其他"), true, true)},
		{"兜底改名后原因跟着改", `{"fallbackName":"漏网","fallbackDefault":"美国"}`, entOpts(), entBlack(entFallback("漏网"), true, true)},
		{"有终端走代理", "", entOpts(proxyCli), entBlack(entProxyCli, true, true)},
		{"有终端走代理且有端口行:先报终端", `{"fallbackDefault":"direct",` + portRule + `}`, entOpts(proxyCli), entBlack(entProxyCli, true, false)},
		{"端口行", `{"fallbackDefault":"direct",` + portRule + `}`, entOpts(), entBlack(entPort("443"), true, false)},
		{"兜底走代理且有端口行:先报兜底,directAnswer 因端口行关", `{"fallbackDefault":"美国",` + portRule + `}`, entOpts(), entBlack(entFallback("其他"), true, false)},
		{"DNS 禁用且有端口行:先报 DNS 禁用", `{"fallbackDefault":"direct",` + portRule + `}`, entOpts(`"dnsMode":"off"`), entBlack(entDNSOff, false, false)},
		{"全部满足", "", entOpts(), entWant("whitelist", entWhite, true, nil, nil, true)},
	} {
		entCheck(t, c.name, entRun(t, c.routing, c.opts), c.want)
	}
}

func TestEntryPlanOptionParsing(t *testing.T) {
	ok := entWant("whitelist", entWhite, true, nil, nil, true)
	// 选项不是对象:全部缺省,没开 FakeIP
	for _, o := range []string{``, `null`, `5`, `[]`, `"x"`, `true`} {
		entCheck(t, "选项 "+o, entRun(t, "", o), entBlack(entNoFake, false, false))
	}
	// enabled 缺省为开;传了按 JS 真假,null 也算关
	for _, v := range []string{`true`, `1`, `"yes"`, `[]`, `{}`, `"0"`} {
		entCheck(t, "enabled="+v, entRun(t, "", entOpts(`"enabled":`+v)), ok)
	}
	for _, v := range []string{`false`, `0`, `""`, `null`} {
		entCheck(t, "enabled="+v, entRun(t, "", entOpts(`"enabled":`+v)), entBlack(entOff, true, false))
	}
	// fakeIp 按 JS 真假
	for _, v := range []string{`1`, `"x"`, `[]`, `{}`} {
		entCheck(t, "fakeIp="+v, entRun(t, "", `{"members":["direct"],"fakeIp":`+v+`}`), entWant("whitelist", entWhite, true, nil, nil, true))
	}
	for _, v := range []string{`false`, `0`, `""`, `null`} {
		entCheck(t, "fakeIp="+v, entRun(t, "", `{"members":["direct"],"fakeIp":`+v+`}`), entBlack(entNoFake, false, false))
	}
	// dnsMode 只有恰好是字符串 off 才算禁用
	for _, v := range []string{`"hijack"`, `"dnsmasq"`, `"OFF"`, `" off"`, `0`, `null`, `false`, `["off"]`} {
		entCheck(t, "dnsMode="+v, entRun(t, "", entOpts(`"dnsMode":`+v)), ok)
	}
	entCheck(t, "dnsMode=off", entRun(t, "", entOpts(`"dnsMode":"off"`)), entBlack(entDNSOff, false, false))

	// members 缺省 / null 按 [direct];兜底 default=美国 不在成员里,落到第一项直连 → 兜底直连
	fb := `{"fallbackDefault":"美国"}`
	entCheck(t, "members 缺省", entRun(t, fb, `{"fakeIp":true}`), ok)
	entCheck(t, "members=null", entRun(t, fb, `{"fakeIp":true,"members":null}`), ok)
	entCheck(t, "members 不是数组按缺省", entRun(t, fb, `{"fakeIp":true,"members":"x"}`), ok)
	entCheck(t, "members 里有美国", entRun(t, fb, `{"fakeIp":true,"members":["direct","美国"]}`), entBlack(entFallback("其他"), true, true))
	entCheck(t, "members 为空:选不出直连", entRun(t, "", `{"fakeIp":true,"members":[]}`), entBlack(entFallback("其他"), true, true))
	entCheck(t, "members 里非字符串占位成空串", entRun(t, fb, `{"fakeIp":true,"members":[1,"美国"]}`), entBlack(entFallback("其他"), true, true))

	// selections:传 null / 非对象当没传;选择盖过兜底 default
	entCheck(t, "选择让兜底走代理", entRun(t, "", entOpts(`"selections":{"其他":"美国"}`)), entBlack(entFallback("其他"), true, true))
	entCheck(t, "选择让兜底回直连", entRun(t, fb, entOpts(`"selections":{"其他":"direct"}`)), ok)
	entCheck(t, "选择链下钻到直连", entRun(t, fb, entOpts(`"selections":{"其他":"自动","自动":"direct"}`)), ok)
	entCheck(t, "selections=null", entRun(t, fb, `{"members":["direct","美国"],"fakeIp":true,"selections":null}`), entBlack(entFallback("其他"), true, true))
	entCheck(t, "selections 不是对象", entRun(t, fb, `{"members":["direct","美国"],"fakeIp":true,"selections":[1]}`), entBlack(entFallback("其他"), true, true))
}

func TestEntryPlanRenamedBuiltins(t *testing.T) {
	renamed := `"builtin":{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`
	m := `"members":["直连","拒绝","美国"],"fakeIp":true`
	ok := entWant("whitelist", entWhite, true, nil, nil, true)
	entCheck(t, "改名后 default=direct 指新直连", entRun(t, "", `{`+m+`,`+renamed+`}`), ok)
	entCheck(t, "没传 builtin 时成员叫「直连」不是内置直连", entRun(t, "", `{`+m+`}`), entBlack(entFallback("其他"), true, true))
	entCheck(t, "builtin=null 同没传", entRun(t, "", `{`+m+`,"builtin":null}`), entBlack(entFallback("其他"), true, true))
	entCheck(t, "builtin 不是对象按缺省", entRun(t, "", `{`+m+`,"builtin":"x"}`), entBlack(entFallback("其他"), true, true))
	entCheck(t, "builtin 缺键:直连名是空串", entRun(t, "", `{"members":["direct"],"fakeIp":true,"builtin":{}}`), entBlack(entFallback("其他"), true, true))
	// 改名后终端出口写老记号 direct 不再是内置直连 → 算走代理;写新名字才算直连
	entCheck(t, "改名后终端写老记号算走代理", entRun(t, "", `{`+m+`,`+renamed+`,"clientRoutes":[{"ip":"192.168.1.5","outbound":"direct"}]}`), entBlack(entProxyCli, true, true))
	entCheck(t, "改名后终端写新名字算直连", entRun(t, "", `{`+m+`,`+renamed+`,"clientRoutes":[{"ip":"192.168.1.5","outbound":"直连"}]}`), ok)
	// 自定义分流行的出口:改名后老记号与新名字都指内置直连(跳过),没改名时「直连」只是节点名
	port := func(out string) string {
		return `{"fallbackDefault":"direct","custom":{"enabled":true,"rules":[{"type":"port","value":"443","outbound":"` + out + `"}]}}`
	}
	entCheck(t, "改名后端口行写老记号 direct:跳过", entRun(t, port("direct"), `{`+m+`,`+renamed+`}`), ok)
	entCheck(t, "改名后端口行写新名字:跳过", entRun(t, port("直连"), `{`+m+`,`+renamed+`}`), ok)
	entCheck(t, "改名后端口行写 block:不跳过", entRun(t, port("block"), `{`+m+`,`+renamed+`}`), entBlack(entPort("443"), true, false))
	entCheck(t, "没改名时端口行写「直连」是节点名:不跳过", entRun(t, port("直连"), `{"members":["direct","直连"],"fakeIp":true}`), entBlack(entPort("443"), true, false))
}

func TestEntryPlanProxyClient(t *testing.T) {
	ok := entWant("whitelist", entWhite, true, nil, nil, true)
	for _, c := range []struct {
		name, routes string
		proxy        bool
	}{
		{"出口是节点", `[{"ip":"192.168.1.5","outbound":"美国"}]`, true},
		{"出口是 direct", `[{"ip":"192.168.1.5","outbound":"direct"}]`, false},
		{"出口是 block 也算(不是内置直连)", `[{"ip":"192.168.1.5","outbound":"block"}]`, true},
		{"出口为空串 / 缺失 / null / false / 0 不算", `[{"outbound":""},{"ip":"1"},{"outbound":null},{"outbound":false},{"outbound":0}]`, false},
		{"出口是别的真值(数字)算", `[{"outbound":123}]`, true},
		{"非对象的项不算", `[5,null,"美国",[],true]`, false},
		{"一个直连一个节点:有一个算就算", `[{"outbound":"direct"},{"outbound":"美国"}]`, true},
		// 按现有实现:只看出口,不看这条终端分流有没有启用
		{"停用的终端分流照算", `[{"ip":"192.168.1.5","outbound":"美国","enabled":false}]`, true},
		{"空数组", `[]`, false},
		{"不是数组:忽略", `{"outbound":"美国"}`, false},
		{"null", `null`, false},
	} {
		got := entRun(t, "", entOpts(`"clientRoutes":`+c.routes))
		if c.proxy {
			entCheck(t, c.name, got, entBlack(entProxyCli, true, true))
		} else {
			entCheck(t, c.name, got, ok)
		}
	}
}

func TestEntryPlanDirectAnswer(t *testing.T) {
	rule := func(typ, v, out string) string {
		return `{"type":"` + typ + `","value":"` + v + `","outbound":"` + out + `"}`
	}
	custom := func(enabled bool, rules ...string) string {
		en := "true"
		if !enabled {
			en = "false"
		}
		return `{"fallbackDefault":"美国","custom":{"enabled":` + en + `,"rules":[` + strings.Join(rules, ",") + `]}}`
	}
	// 兜底走代理的路径上只看 directAnswer:它由 FakeIP / DNS 模式 / 开关 / 端口行决定
	for _, c := range []struct {
		name, routing, opts string
		da                  bool
	}{
		{"基础:开着", custom(true), entOpts(), true},
		{"端口行走节点 → 关", custom(true, rule("port", "443", "美国")), entOpts(), false},
		{"端口行走拒绝 → 关", custom(true, rule("port", "443", "block")), entOpts(), false},
		{"端口行走直连 → 不影响", custom(true, rule("port", "443", "direct")), entOpts(), true},
		{"别的类型的行不影响", custom(true, rule("ipCidr", "10.0.0.0/8", "美国"), rule("domain", "a.example", "美国"), rule("geoip", "us", "美国"), rule("ruleset", "r", "美国")), entOpts(), true},
		{"端口行在后面也算", custom(true, rule("domain", "a.example", "美国"), rule("port", "443", "美国")), entOpts(), false},
		{"自定义分流停用:端口行不算", custom(false, rule("port", "443", "美国")), entOpts(), true},
		{"自定义分流没有规则", custom(true), entOpts(), true},
		{"没开 FakeIP → 关", custom(true), `{"members":["direct","美国"]}`, false},
		{"DNS 禁用 → 关", custom(true), entOpts(`"dnsMode":"off"`), false},
		{"直连不进内核关着 → 关", custom(true), entOpts(`"enabled":false`), false},
	} {
		got := entRun(t, c.routing, c.opts)
		m, _ := got.(map[string]any)
		if m["directAnswer"] != c.da {
			gb, _ := json.Marshal(got)
			t.Errorf("%s: directAnswer 应为 %v: %s", c.name, c.da, gb)
		}
	}
}

// directAnswer 只在 blacklist 里能读到(whitelist 恒为 true),所以用「兜底走代理」把结果钉在 blacklist 上,
// 单看前置自定义分流端口行的出口判定:出口先换成当前内置名字再和直连名比。
func TestEntryPlanDirectAnswerPortRowRenamed(t *testing.T) {
	routing := func(out string) string {
		return `{"fallbackDefault":"美国","custom":{"enabled":true,"rules":[{"type":"port","value":"443","outbound":"` + out + `"}]}}`
	}
	renamed := `"builtin":{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`
	m := `"members":["直连","拒绝","direct","block","美国"],"fakeIp":true`
	for _, c := range []struct {
		name, out, builtin string
		da                 bool
	}{
		{"改名后端口行写老记号 direct:仍是直连,不关", "direct", renamed, true},
		{"改名后端口行写新名字:不关", "直连", renamed, true},
		{"改名后端口行写老记号 block:关", "block", renamed, false},
		{"改名后端口行写新拒绝名:关", "拒绝", renamed, false},
		{"没改名时端口行写「直连」只是节点名:关", "直连", `"builtin":null`, false},
		{"没改名时端口行写 direct:不关", "direct", `"builtin":null`, true},
	} {
		entCheck(t, c.name, entRun(t, routing(c.out), `{`+m+`,`+c.builtin+`}`), entBlack(entFallback("其他"), true, c.da))
	}
}

func TestEntryPlanCustomRules(t *testing.T) {
	rule := func(typ, v, out string) string {
		return `{"type":"` + typ + `","value":"` + v + `","outbound":"` + out + `"}`
	}
	routing := func(rules ...string) string {
		return `{"fallbackDefault":"direct","custom":{"enabled":true,"rules":[` + strings.Join(rules, ",") + `]}}`
	}
	// 端口行 / 规则集行退回 blacklist,先出现的先报;在它前面的行什么都没留下(needSets / needCidrs 空)
	entCheck(t, "规则集链接", entRun(t, routing(rule("ruleUrl", entUrl, "美国")), entOpts()), entBlack(entList(entUrl), true, true))
	entCheck(t, "规则集", entRun(t, routing(rule("ruleset", "my-set", "美国")), entOpts()), entBlack(entList("my-set"), true, true))
	entCheck(t, "规则集行走拒绝也报", entRun(t, routing(rule("ruleset", "my-set", "block")), entOpts()), entBlack(entList("my-set"), true, true))
	entCheck(t, "规则集行在端口行前面:先报规则集", entRun(t, routing(rule("ruleset", "my-set", "美国"), rule("port", "443", "美国")), entOpts()), entBlack(entList("my-set"), true, false))
	entCheck(t, "端口行在规则集行前面:先报端口", entRun(t, routing(rule("port", "443", "美国"), rule("ruleUrl", entUrl, "美国")), entOpts()), entBlack(entPort("443"), true, false))
	entCheck(t, "前面的 ipCidr / geoip 一并丢掉", entRun(t, routing(rule("ipCidr", "10.0.0.0/8", "美国"), rule("geoip", "us", "美国"), rule("port", "80", "美国")), entOpts()), entBlack(entPort("80"), true, false))
	entCheck(t, "端口值是多个口:原样写进原因", entRun(t, routing(rule("port", "80,443 8000-9000", "美国")), entOpts()), entBlack(entPort("80,443 8000-9000"), true, false))
	// 出口是直连的行整行跳过,即使是端口 / 规则集
	entCheck(t, "直连的端口 / 规则集 / ipCidr / geoip 行全跳过", entRun(t, routing(
		rule("port", "443", "direct"), rule("ruleUrl", entUrl, "direct"), rule("ruleset", "r", "direct"),
		rule("ipCidr", "10.0.0.0/8", "direct"), rule("geoip", "cn", "direct")), entOpts()), entWant("whitelist", entWhite, true, nil, nil, true))

	// ipCidr / geoip:出口不是直连(节点或拒绝)都进;域名类行不管
	entCheck(t, "ipCidr 与 geoip", entRun(t, routing(
		rule("ipCidr", "10.0.0.0/8", "美国"), rule("geoip", "us", "美国"), rule("ipCidr", "172.16.0.0/12", "block"),
		rule("geoip", "jp", "block"), rule("domain", "a.example", "美国"), rule("domainSuffix", "b.example", "美国"),
		rule("domainKeyword", "kw", "美国"), rule("geosite", "netflix", "block")), entOpts()),
		entWant("whitelist", entWhite, true, []string{"geoip-us", "geoip-jp"}, []string{"10.0.0.0/8", "172.16.0.0/12"}, true))
	// 自定义分流自己的 ipCidr 不去重,geoip 去重;geoip 的值原样拼(不转小写)
	entCheck(t, "ipCidr 不去重 / geoip 去重", entRun(t, routing(
		rule("ipCidr", "10.0.0.0/8", "美国"), rule("ipCidr", "10.0.0.0/8", "日本"), rule("geoip", "US", "美国"), rule("geoip", "US", "日本")), entOpts()),
		entWant("whitelist", entWhite, true, []string{"geoip-US"}, []string{"10.0.0.0/8", "10.0.0.0/8"}, true))
	entCheck(t, "只有域名类行:两个都空", entRun(t, routing(rule("domain", "a.example", "美国"), rule("geosite", "x", "block")), entOpts()), entWant("whitelist", entWhite, true, nil, nil, true))
	// 前置自定义分流不生效:端口行也不看
	for _, c := range []struct{ name, custom string }{
		{"停用", `{"enabled":false,"rules":[` + rule("port", "443", "美国") + `]}`},
		{"没有规则", `{"enabled":true,"rules":[]}`},
		{"规则全写错", `{"enabled":true,"rules":[{"type":"port","value":"99999","outbound":"美国"},{"type":"x","value":"1","outbound":"美国"}]}`},
	} {
		entCheck(t, c.name, entRun(t, `{"fallbackDefault":"direct","custom":`+c.custom+`}`, entOpts()), entWant("whitelist", entWhite, true, nil, nil, true))
	}
}

func TestEntryPlanPolicies(t *testing.T) {
	pols := `{"fallbackDefault":"direct","policies":[
		{"name":"美国线","default":"美国","rulesets":["geoip-us","geosite-x","geoip-jp"],"ipCidr":["1.1.1.0/24"]},
		{"name":"直连线","default":"direct","rulesets":["geoip-cn"],"ipCidr":["2.2.2.0/24"]},
		{"name":"停用","default":"美国","enabled":false,"rulesets":["geoip-off"],"ipCidr":["9.9.9.0/24"]},
		{"name":"日本线","default":"日本","rulesets":["geoip-us","geoip-kr"],"ipCidr":["1.1.1.0/24","3.3.3.0/24"]},
		{"name":"链接线","default":"美国","ruleUrls":["` + entUrl + `"],"rulesets":["geosite-y"]},
		{"name":"广告线","default":"block","ipCidr":["4.4.4.0/24"]}
	]}`
	// 走代理 / 拒绝的站点集:geoip- 进 needSets(去重、保序),ipCidr 进 needCidrs(完全相同才去重);
	// 走直连的、停用的不看;geosite- / 规则集链接这类不管
	entCheck(t, "各站点集", entRun(t, pols, entOpts()),
		entWant("whitelist", entWhite, true, []string{"geoip-us", "geoip-jp", "geoip-kr"}, []string{"1.1.1.0/24", "3.3.3.0/24", "4.4.4.0/24"}, true))
	// 选择改变站点集走向
	entCheck(t, "选择:直连线改走节点,美国线改直连", entRun(t, pols, entOpts(`"selections":{"直连线":"美国","美国线":"direct"}`)),
		entWant("whitelist", entWhite, true, []string{"geoip-cn", "geoip-us", "geoip-kr"}, []string{"2.2.2.0/24", "1.1.1.0/24", "3.3.3.0/24", "4.4.4.0/24"}, true))
	entCheck(t, "选择链下钻到直连", entRun(t, pols, entOpts(`"selections":{"日本线":"自动","自动":"direct","广告线":"direct","美国线":"direct","链接线":"direct"}`)),
		entWant("whitelist", entWhite, true, nil, nil, true))
	// 没有任何站点集走代理(或没有站点集):whitelist,两个数组是 [] 不是 null
	entCheck(t, "没有站点集", entRun(t, `{"fallbackDefault":"direct"}`, entOpts()), entWant("whitelist", entWhite, true, nil, nil, true))
	entCheck(t, "走代理的站点集只有域名 / geosite", entRun(t, `{"fallbackDefault":"direct","policies":[{"name":"A","default":"美国","domain":["a.example"],"rulesets":["geosite-x"]}]}`, entOpts()), entWant("whitelist", entWhite, true, nil, nil, true))
	// 站点集 default 空着落到成员第一项
	one := `{"fallbackDefault":"direct","policies":[{"name":"A","default":"","rulesets":["geoip-us"]}]}`
	entCheck(t, "default 空着落到第一项(直连)", entRun(t, one, `{"members":["direct","美国"],"fakeIp":true}`), entWant("whitelist", entWhite, true, nil, nil, true))
	entCheck(t, "default 空着落到第一项(节点):站点集走代理,兜底 direct 仍在成员里", entRun(t, one, `{"members":["美国","direct"],"fakeIp":true}`), entWant("whitelist", entWhite, true, []string{"geoip-us"}, nil, true))
	// 站点集名叫「其他」的整理时被丢,不影响;DNS 模式 hijack / dnsmasq 都一样
	for _, mode := range []string{`"hijack"`, `"dnsmasq"`} {
		entCheck(t, "dnsMode="+mode, entRun(t, pols, entOpts(`"dnsMode":`+mode)),
			entWant("whitelist", entWhite, true, []string{"geoip-us", "geoip-jp", "geoip-kr"}, []string{"1.1.1.0/24", "3.3.3.0/24", "4.4.4.0/24"}, true))
	}
	// 自定义分流在前、站点集在后:needCidrs 先自定义,与站点集重复的只留先到的;needSets 同理
	both := `{"fallbackDefault":"direct","custom":{"enabled":true,"rules":[
			{"type":"ipCidr","value":"1.1.1.0/24","outbound":"美国"},{"type":"geoip","value":"kr","outbound":"美国"}]},
		"policies":[{"name":"A","default":"美国","rulesets":["geoip-us","geoip-kr"],"ipCidr":["3.3.3.0/24","1.1.1.0/24"]}]}`
	entCheck(t, "自定义在前并与站点集去重", entRun(t, both, entOpts()),
		entWant("whitelist", entWhite, true, []string{"geoip-kr", "geoip-us"}, []string{"1.1.1.0/24", "3.3.3.0/24"}, true))
}

func TestEntryPlanJSONShape(t *testing.T) {
	clsNoGeo()
	mk := func(routing, opts string) string {
		b, err := json.Marshal(entryPlan(mustJSON(t, routing), mustJSON(t, opts)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got, want := mk(`{"fallbackDefault":"direct"}`, `{"members":["direct"],"fakeIp":true}`),
		`{"mode":"whitelist","reason":"`+entWhite+`","fakeIp":true,"needSets":[],"needCidrs":[],"directAnswer":true}`; got != want {
		t.Errorf("whitelist:\n有: %s\n要: %s", got, want)
	}
	if got, want := mk(`{"fallbackDefault":"direct","policies":[{"name":"A","default":"美国","rulesets":["geoip-us"],"ipCidr":["1.1.1.0/24"]}]}`, `{"members":["direct","美国"],"fakeIp":true}`),
		`{"mode":"whitelist","reason":"`+entWhite+`","fakeIp":true,"needSets":["geoip-us"],"needCidrs":["1.1.1.0/24"],"directAnswer":true}`; got != want {
		t.Errorf("whitelist 带内容:\n有: %s\n要: %s", got, want)
	}
	if got, want := mk(`{}`, `{}`),
		`{"mode":"blacklist","reason":"`+entNoFake+`","fakeIp":false,"needSets":[],"needCidrs":[],"directAnswer":false}`; got != want {
		t.Errorf("blacklist:\n有: %s\n要: %s", got, want)
	}
}

func TestEntryPlanDoesNotMutateInput(t *testing.T) {
	routing := mustJSON(t, `{"fallbackDefault":"direct","custom":{"enabled":true,"rules":[{"type":"ipCidr","value":"10.0.0.0/8","outbound":"美国"}]},"policies":[{"name":"A","default":"美国","rulesets":["geoip-us"],"ipCidr":["1.1.1.0/24"]}]}`)
	opts := mustJSON(t, entOpts(`"selections":{"A":"日本"},"clientRoutes":[{"outbound":"direct"}]`))
	rb, _ := json.Marshal(routing)
	ob, _ := json.Marshal(opts)
	first := roundTrip(t, entryPlan(routing, opts))
	second := roundTrip(t, entryPlan(routing, opts))
	if !reflect.DeepEqual(first, second) {
		t.Error("两次调用结果不同")
	}
	ra, _ := json.Marshal(routing)
	oa, _ := json.Marshal(opts)
	if string(rb) != string(ra) || string(ob) != string(oa) {
		t.Error("入参被改写")
	}
}

// 走完整的协议入口:缺省参数与非法形状参数。
func TestEntryPlanHandlerArgs(t *testing.T) {
	reg := protocol.NewRegistry()
	Register(reg)
	run := func(args string) any {
		t.Helper()
		return call(t, reg, []byte(`{"protocol":1,"method":"entryPlan","args":`+args+`}`))
	}
	routing := `{"fallbackDefault":"direct","policies":[{"name":"A","default":"美国","rulesets":["geoip-us"]}]}`
	entCheck(t, "只给 routing:没开 FakeIP", run(`[`+routing+`]`), entBlack(entNoFake, false, false))
	entCheck(t, "选项 null", run(`[`+routing+`,null]`), entBlack(entNoFake, false, false))
	entCheck(t, "选项给全", run(`[`+routing+`,{"members":["direct","美国"],"fakeIp":true}]`), entWant("whitelist", entWhite, true, []string{"geoip-us"}, nil, true))
	entCheck(t, "没有参数:空档案", run(`[]`), entBlack(entNoFake, false, false))
	entCheck(t, "空档案加 FakeIP", run(`[{},{"fakeIp":true}]`), entWant("whitelist", entWhite, true, nil, nil, true))
}
