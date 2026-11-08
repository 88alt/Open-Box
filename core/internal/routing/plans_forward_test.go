package routing

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/protocol"
)

// 自写测试向量:forwardPlan(dnsmasq 接管模式下该怎么转发查询)。输入自己拟,期望值按下面的规则人工推算:
//   1. 兜底此刻不走直连 → all,原因「兜底「名」走代理」,别的什么都不看;
//   2. 前置自定义分流(启用且有规则才看),逐行:出口(先把 direct / block 换成当前名字)等于直连名的跳过;
//      域名 / 域名后缀 → 进名单(转小写、去通配、IDN 转 punycode、去重),写不进 dnsmasq → all;
//      geosite / 规则集链接 → 进展开表(标签:geosite-xxx / list-八位十六进制),所属写「前置自定义分流「值」」;
//      域名关键词 / 规则集 → all(展不开);ipCidr / geoip / port 解析阶段用不上,跳过;
//      出口恰是拒绝名的所属多一句「要拒绝的」;
//   3. 生效的站点集(此刻走直连的跳过):先看有没有域名关键词(有 → all),再 domain 后 domainSuffix 逐个进名单,
//      再规则集里非 geoip- 的进展开表(所属「站点集「名」」);ipCidr 与 geoip- 只按 IP 分,不用;
//   4. extra.rewriteDomains 里的非空字符串原样追加(不转小写、不转 punycode),与已有的完全相同才去重;
//   5. 名单和展开表都空 → none,否则 domains;展开表不去重,保持规则 / 站点集 / 规则集的遍历顺序;
//   6. 出现「写不进」或「展不开」就整个退成 all,之前攒下的域名一概不带。

const (
	fwdMembers = `["direct","美国","日本"]`
	fwdNone    = "没有走代理的域名,DNS 全部由路由器原有上游解析"
)

type fwdIn struct {
	routing, members, builtin, sel, extra string
}

// fwdRun 走 planArgs(和协议入口同一条路径)再调 forwardPlan。
func fwdRun(t *testing.T, c fwdIn) any {
	t.Helper()
	clsNoGeo()
	raw := func(s, def string) json.RawMessage {
		if s == "" {
			s = def
		}
		return json.RawMessage(s)
	}
	args := []json.RawMessage{raw(c.routing, `{}`), raw(c.members, fwdMembers), raw(c.builtin, `null`), raw(c.sel, `{}`), raw(c.extra, `null`)}
	routing, members, b, sel, err := planArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := arg(args, 4)
	if err != nil {
		t.Fatal(err)
	}
	return roundTrip(t, forwardPlan(routing, members, b, sel, extra))
}

// fwdWant 拼期望的 JSON;expand 每项是 {标签, 所属}。
func fwdWant(mode string, domains []string, expand [][2]string, reason string) string {
	if domains == nil {
		domains = []string{}
	}
	ex := []map[string]string{}
	for _, e := range expand {
		ex = append(ex, map[string]string{"tag": e[0], "owner": e[1]})
	}
	b, _ := json.Marshal(map[string]any{"mode": mode, "domains": domains, "expand": ex, "reason": reason})
	return string(b)
}

func fwdCheck(t *testing.T, name string, got any, want string) {
	t.Helper()
	clsCheck(t, name, got, want)
}

func fwdAll(reason string) string { return fwdWant("all", nil, nil, reason) }

func fwdRouting(fallback string, rest ...string) string {
	return `{"fallbackDefault":"` + fallback + `"` + strings.Join(append([]string{""}, rest...), ",") + `}`
}

func fwdPols(pols ...string) string { return `"policies":[` + strings.Join(pols, ",") + `]` }
func fwdCustom(rules ...string) string {
	return `"custom":{"enabled":true,"rules":[` + strings.Join(rules, ",") + `]}`
}
func fwdRule(typ, value, out string) string {
	return `{"type":"` + typ + `","value":"` + value + `","outbound":"` + out + `"}`
}
func fwdPol(name, def, body string) string {
	return `{"name":"` + name + `","default":"` + def + `",` + body + `}`
}

const (
	fwdUrlA = "https://example.com/a.list"               // list-1aea4a3b
	fwdUrlB = "https://lists.example.org/streaming.list" // list-d8a71cb6
)

// 期望里的规则集链接标签用 Python 按 FNV-1a(UTF-16 码元)另算,不借用代码里的函数。
const (
	fwdTagA = "list-1aea4a3b"
	fwdTagB = "list-d8a71cb6"
)

func TestForwardPlanFallbackGate(t *testing.T) {
	for _, c := range []struct {
		name string
		in   fwdIn
		want string
	}{
		{"兜底走代理", fwdIn{routing: fwdRouting("美国")}, fwdAll("兜底「其他」走代理")},
		{"兜底改名后原因跟着改", fwdIn{routing: `{"fallbackName":"漏网","fallbackDefault":"美国"}`}, fwdAll("兜底「漏网」走代理")},
		{"兜底直连且没有别的 → none", fwdIn{routing: fwdRouting("direct")}, fwdWant("none", nil, nil, fwdNone)},
		{"default 空着落到成员第一项(直连)", fwdIn{routing: fwdRouting(""), members: `["direct","美国"]`}, fwdWant("none", nil, nil, fwdNone)},
		{"default 空着落到成员第一项(节点)", fwdIn{routing: fwdRouting(""), members: `["美国","direct"]`}, fwdAll("兜底「其他」走代理")},
		{"default 指向不存在的组落到第一项", fwdIn{routing: fwdRouting("ghost")}, fwdWant("none", nil, nil, fwdNone)},
		{"default=proxy 取第一个非内置成员", fwdIn{routing: fwdRouting("proxy"), members: `["direct","block","美国"]`}, fwdAll("兜底「其他」走代理")},
		{"default=proxy 成员里只有内置:落到第一项直连", fwdIn{routing: fwdRouting("proxy"), members: `["direct","block"]`}, fwdWant("none", nil, nil, fwdNone)},
		{"成员为空:选不出直连 → all", fwdIn{routing: fwdRouting("direct"), members: `[]`}, fwdAll("兜底「其他」走代理")},
		{"选择盖过 default(直连改节点)", fwdIn{routing: fwdRouting("direct"), sel: `{"其他":"美国"}`}, fwdAll("兜底「其他」走代理")},
		{"选择盖过 default(节点改直连)", fwdIn{routing: fwdRouting("美国"), sel: `{"其他":"direct"}`}, fwdWant("none", nil, nil, fwdNone)},
		{"选择链下钻到直连", fwdIn{routing: fwdRouting("美国"), sel: `{"其他":"自动","自动":"direct"}`}, fwdWant("none", nil, nil, fwdNone)},
		{"选择拒绝不算直连", fwdIn{routing: fwdRouting("direct"), sel: `{"其他":"block"}`, members: `["direct","block"]`}, fwdAll("兜底「其他」走代理")},
		{"非字符串的选择不算直连", fwdIn{routing: fwdRouting("direct"), sel: `{"其他":123}`}, fwdAll("兜底「其他」走代理")},
		{"选择成环按 proxy", fwdIn{routing: fwdRouting("direct"), sel: `{"其他":"甲","甲":"其他"}`}, fwdAll("兜底「其他」走代理")},
		{"内置改名:default=direct 认新名字", fwdIn{routing: fwdRouting("direct"), members: `["直连","美国"]`, builtin: `{"direct":"直连","block":"拒绝"}`}, fwdWant("none", nil, nil, fwdNone)},
		{"没改名时成员叫「直连」不算直连", fwdIn{routing: fwdRouting("direct"), members: `["直连","美国"]`}, fwdAll("兜底「其他」走代理")},
		{"改名后选择写老记号 direct 不算直连", fwdIn{routing: fwdRouting("direct"), members: `["直连","美国"]`, builtin: `{"direct":"直连","block":"拒绝"}`, sel: `{"其他":"direct"}`}, fwdAll("兜底「其他」走代理")},
		{"内置对象缺键:直连名是空串,default=direct 不算直连", fwdIn{routing: fwdRouting("direct"), builtin: `{}`}, fwdAll("兜底「其他」走代理")},
	} {
		fwdCheck(t, c.name, fwdRun(t, c.in), c.want)
	}

	// 兜底走代理时,站点集 / 自定义分流 / rewriteDomains 一概不看,名单和展开表都是空数组(不是 null)
	busy := `{"fallbackDefault":"美国",` + fwdPols(fwdPol("流媒体", "美国", `"domainKeyword":["x"],"domain":["bad domain"]`)) + `,` + fwdCustom(fwdRule("domain", "a.example", "美国")) + `}`
	fwdCheck(t, "兜底走代理时别的都不看", fwdRun(t, fwdIn{routing: busy, extra: `{"rewriteDomains":["r.example"]}`}), fwdAll("兜底「其他」走代理"))
}

func TestForwardPlanPolicyDomains(t *testing.T) {
	routing := fwdRouting("direct", fwdPols(
		fwdPol("流媒体", "美国", `"domain":["Netflix.com"],"domainSuffix":["*.hulu.com","disneyplus.com"]`),
		fwdPol("下载", "direct", `"domain":["skipme.example"]`),
		fwdPol("停用", "美国", `"enabled":false,"domain":["off.example"]`),
		fwdPol("办公", "日本", `"domain":["NETFLIX.COM","corp.example"]`),
		fwdPol("其他", "美国", `"domain":["fb.example"]`),
		fwdPol("没条件", "美国", `"notes":{}`),
	))
	// 流媒体:domain 在前 domainSuffix 在后;办公里的 netflix.com 转小写后和流媒体的重复,只留一个;
	// 走直连的、停用的、叫「其他」的、没条件的都不看
	fwdCheck(t, "域名收集", fwdRun(t, fwdIn{routing: routing}),
		fwdWant("domains", []string{"netflix.com", "hulu.com", "disneyplus.com", "corp.example"}, nil, ""))

	// 同一个站点集里所有 domain 排在所有 domainSuffix 前面,不按书写顺序
	fwdCheck(t, "domain 在前 domainSuffix 在后", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdPols(fwdPol("A", "美国", `"domainSuffix":["s1.example","s2.example"],"domain":["d1.example","d2.example"]`)))}),
		fwdWant("domains", []string{"d1.example", "d2.example", "s1.example", "s2.example"}, nil, ""))

	// 写法归一:大小写、通配前缀、前后点、前后空白、IDN
	fwdCheck(t, "写法归一", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdPols(fwdPol("A", "美国", `"domain":["  WWW.Example.COM  ",".dot.example.","中国.example"],"domainSuffix":["*.sub.Example.org","日本語.jp"]`)))}),
		fwdWant("domains", []string{"www.example.com", "dot.example", "xn--fiqs8s.example", "sub.example.org", "xn--wgv71a119e.jp"}, nil, ""))

	// 通配与不带通配的同一个域名去重
	fwdCheck(t, "通配与裸域名去重", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdPols(fwdPol("A", "美国", `"domain":["x.example"],"domainSuffix":["*.x.example","X.EXAMPLE"]`)))}),
		fwdWant("domains", []string{"x.example"}, nil, ""))

	// 只有 ipCidr / 只有 geoip- 规则集:解析阶段用不上,什么都不进
	fwdCheck(t, "只有 ipCidr", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdPols(fwdPol("A", "美国", `"ipCidr":["10.0.0.0/8"]`)))}), fwdWant("none", nil, nil, fwdNone))
	fwdCheck(t, "只有 geoip-", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdPols(fwdPol("A", "美国", `"rulesets":["geoip-us","geoip-jp"]`)))}), fwdWant("none", nil, nil, fwdNone))

	// 站点集此刻是否走直连按选择 / default / 成员表判定
	pols := fwdRouting("direct", fwdPols(fwdPol("甲", "美国", `"domain":["a.example"]`), fwdPol("乙", "direct", `"domain":["b.example"]`)))
	fwdCheck(t, "选择让直连的站点集改走代理", fwdRun(t, fwdIn{routing: pols, sel: `{"乙":"日本"}`}), fwdWant("domains", []string{"a.example", "b.example"}, nil, ""))
	fwdCheck(t, "选择让代理的站点集改走直连", fwdRun(t, fwdIn{routing: pols, sel: `{"甲":"direct"}`}), fwdWant("none", nil, nil, fwdNone))
	fwdCheck(t, "选择链下钻到直连", fwdRun(t, fwdIn{routing: pols, sel: `{"甲":"自动","自动":"direct"}`}), fwdWant("none", nil, nil, fwdNone))
	fwdCheck(t, "选择拒绝也不算直连,域名照进名单", fwdRun(t, fwdIn{routing: pols, sel: `{"甲":"block"}`, members: `["direct","block","美国"]`}), fwdWant("domains", []string{"a.example"}, nil, ""))
	fwdCheck(t, "default 空着落到成员第一项(直连)", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdPols(fwdPol("甲", "", `"domain":["a.example"]`))), members: `["direct","美国"]`}), fwdWant("none", nil, nil, fwdNone))
	fwdCheck(t, "default 空着落到成员第一项(节点)", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdPols(fwdPol("甲", "", `"domain":["a.example"]`))), members: `["美国","direct"]`}), fwdWant("domains", []string{"a.example"}, nil, ""))
	fwdCheck(t, "内置改名后 default=direct 认新名字", fwdRun(t, fwdIn{routing: pols, members: `["直连","美国"]`, builtin: `{"direct":"直连","block":"拒绝"}`}), fwdWant("domains", []string{"a.example"}, nil, ""))
	// 站点集 default 写「直连」:没改名时它只是个节点名(算代理);内置直连改名成「直连」后才指内置直连
	named := fwdRouting("direct", fwdPols(fwdPol("甲", "美国", `"domain":["a.example"]`), fwdPol("乙", "直连", `"domain":["b.example"]`)))
	fwdCheck(t, "没改名时 default=直连 只是节点名,域名照进名单", fwdRun(t, fwdIn{routing: named, members: `["direct","直连","美国"]`}), fwdWant("domains", []string{"a.example", "b.example"}, nil, ""))
	fwdCheck(t, "改名后 default=直连 指内置直连,跳过", fwdRun(t, fwdIn{routing: named, members: `["direct","直连","美国"]`, builtin: `{"direct":"直连","block":"拒绝"}`}), fwdWant("domains", []string{"a.example"}, nil, ""))
}

func TestForwardPlanPolicyRulesets(t *testing.T) {
	routing := fwdRouting("direct", fwdPols(
		fwdPol("流媒体", "美国", `"rulesets":["geosite-netflix","geoip-us","geosite-disney"],"domain":["n.example"]`),
		fwdPol("AI", "日本", `"rulesets":["geosite-openai"],"ruleUrls":["`+fwdUrlA+`","`+fwdUrlB+`"]`),
		fwdPol("下载", "direct", `"rulesets":["geosite-pt"]`),
		fwdPol("停用", "美国", `"enabled":false,"rulesets":["geosite-off"]`),
	))
	// 展开表按站点集顺序、规则集顺序排(书写的规则集在前,链接折算的 list- 标签追加在后);geoip- 不进;直连的 / 停用的不看
	fwdCheck(t, "展开表", fwdRun(t, fwdIn{routing: routing}), fwdWant("domains", []string{"n.example"}, [][2]string{
		{"geosite-netflix", "站点集「流媒体」"},
		{"geosite-disney", "站点集「流媒体」"},
		{"geosite-openai", "站点集「AI」"},
		{fwdTagA, "站点集「AI」"},
		{fwdTagB, "站点集「AI」"},
	}, ""))

	// 只有展开项没有域名:仍是 domains,名单是空数组
	fwdCheck(t, "只有展开项", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdPols(fwdPol("A", "美国", `"rulesets":["geosite-x"]`)))}),
		fwdWant("domains", nil, [][2]string{{"geosite-x", "站点集「A」"}}, ""))

	// 展开表不去重:两个站点集引用同一个规则集,各占一项
	fwdCheck(t, "展开表不去重", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdPols(
		fwdPol("A", "美国", `"rulesets":["geosite-x"]`), fwdPol("B", "日本", `"rulesets":["geosite-x"]`)))}),
		fwdWant("domains", nil, [][2]string{{"geosite-x", "站点集「A」"}, {"geosite-x", "站点集「B」"}}, ""))

	// 同一个站点集里同一个规则集写两遍,整理时已去重,只一项
	fwdCheck(t, "站点集内部重复在整理时已去掉", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdPols(fwdPol("A", "美国", `"rulesets":["geosite-x","geosite-x"]`)))}),
		fwdWant("domains", nil, [][2]string{{"geosite-x", "站点集「A」"}}, ""))

	// 站点集走直连时规则集也不展开;选择让它走代理后才展开
	direct := fwdRouting("direct", fwdPols(fwdPol("A", "direct", `"rulesets":["geosite-x"]`)))
	fwdCheck(t, "直连的站点集不展开", fwdRun(t, fwdIn{routing: direct}), fwdWant("none", nil, nil, fwdNone))
	fwdCheck(t, "选择改走代理后展开", fwdRun(t, fwdIn{routing: direct, sel: `{"A":"美国"}`}),
		fwdWant("domains", nil, [][2]string{{"geosite-x", "站点集「A」"}}, ""))
}

func TestForwardPlanPolicyRefusals(t *testing.T) {
	kw := "站点集「关键词」用了域名关键词,dnsmasq 展不开"
	bad := func(owner, v string) string {
		return owner + "的域名「" + v + "」写不进 dnsmasq（非法字符 / 标签超长）"
	}
	long := strings.Repeat("a", 64) + ".example"
	for _, c := range []struct {
		name, routing, want string
	}{
		{"域名关键词", fwdRouting("direct", fwdPols(fwdPol("关键词", "美国", `"domainKeyword":["game"]`))), fwdAll(kw)},
		{"域名关键词连同别的条件:关键词在域名之前判", fwdRouting("direct", fwdPols(fwdPol("关键词", "美国", `"domain":["bad domain"],"domainKeyword":["game"]`))), fwdAll(kw)},
		{"走直连的站点集带关键词不影响别人", fwdRouting("direct", fwdPols(fwdPol("关键词", "direct", `"domainKeyword":["game"]`), fwdPol("好", "美国", `"domain":["ok.example"]`))), fwdWant("domains", []string{"ok.example"}, nil, "")},
		{"停用的站点集带关键词不影响别人", fwdRouting("direct", fwdPols(fwdPol("关键词", "美国", `"enabled":false,"domainKeyword":["game"]`), fwdPol("好", "美国", `"domain":["ok.example"]`))), fwdWant("domains", []string{"ok.example"}, nil, "")},
		{"domain 写不进", fwdRouting("direct", fwdPols(fwdPol("坏", "美国", `"domain":["bad domain.example"]`))), fwdAll(bad("站点集「坏」", "bad domain.example"))},
		{"domainSuffix 写不进(原文照写,不转小写)", fwdRouting("direct", fwdPols(fwdPol("坏", "美国", `"domainSuffix":["#X.example"]`))), fwdAll(bad("站点集「坏」", "#X.example"))},
		{"标签超过 63", fwdRouting("direct", fwdPols(fwdPol("坏", "美国", `"domain":["`+long+`"]`))), fwdAll(bad("站点集「坏」", long))},
		{"横线开头的标签", fwdRouting("direct", fwdPols(fwdPol("坏", "美国", `"domain":["-x.example"]`))), fwdAll(bad("站点集「坏」", "-x.example"))},
		{"空标签", fwdRouting("direct", fwdPols(fwdPol("坏", "美国", `"domain":["a..example"]`))), fwdAll(bad("站点集「坏」", "a..example"))},
		{"同一个站点集里 domain 的坏值先于 domainSuffix 的", fwdRouting("direct", fwdPols(fwdPol("坏", "美国", `"domainSuffix":["bad s.example"],"domain":["bad d.example"]`))), fwdAll(bad("站点集「坏」", "bad d.example"))},
		{"前面的好域名一并丢掉", fwdRouting("direct", fwdPols(fwdPol("好", "美国", `"domain":["ok.example"],"rulesets":["geosite-x"]`), fwdPol("坏", "美国", `"domain":["bad domain"]`))), fwdAll(bad("站点集「坏」", "bad domain"))},
		{"前面的站点集写不进先报,后面的关键词不报", fwdRouting("direct", fwdPols(fwdPol("坏", "美国", `"domain":["bad domain"]`), fwdPol("关键词", "美国", `"domainKeyword":["game"]`))), fwdAll(bad("站点集「坏」", "bad domain"))},
		{"前面的关键词先报,后面的写不进不报", fwdRouting("direct", fwdPols(fwdPol("关键词", "美国", `"domainKeyword":["game"]`), fwdPol("坏", "美国", `"domain":["bad domain"]`))), fwdAll(kw)},
		{"走直连的站点集里写不进的域名不管", fwdRouting("direct", fwdPols(fwdPol("坏", "direct", `"domain":["bad domain"]`))), fwdWant("none", nil, nil, fwdNone)},
	} {
		fwdCheck(t, c.name, fwdRun(t, fwdIn{routing: c.routing}), c.want)
	}
}

func TestForwardPlanCustomRules(t *testing.T) {
	const owner = "前置自定义分流"
	const ban = "前置自定义分流要拒绝的"
	rules := fwdCustom(
		fwdRule("domain", "Proxy.Example.com", "美国"),
		fwdRule("domainSuffix", "*.cdn.example", "日本"),
		fwdRule("geosite", "netflix", "美国"),
		fwdRule("ruleUrl", fwdUrlA, "美国"),
		fwdRule("ipCidr", "10.0.0.0/8", "美国"),
		fwdRule("geoip", "us", "美国"),
		fwdRule("port", "443", "美国"),
		fwdRule("domain", "direct.example", "direct"),
		fwdRule("domain", "ban.example", "block"),
		fwdRule("geosite", "ads", "block"),
		fwdRule("ruleUrl", fwdUrlB, "block"),
		fwdRule("geoip", "cn", "block"),
	)
	fwdCheck(t, "各类行", fwdRun(t, fwdIn{routing: fwdRouting("direct", rules)}), fwdWant("domains",
		[]string{"proxy.example.com", "cdn.example", "ban.example"},
		[][2]string{
			{"geosite-netflix", owner + "「netflix」"},
			{fwdTagA, owner + "「" + fwdUrlA + "」"},
			{"geosite-ads", ban + "「ads」"},
			{fwdTagB, ban + "「" + fwdUrlB + "」"},
		}, ""))

	// 只有 IP / 端口类的行:解析阶段全用不上
	fwdCheck(t, "只有 ipCidr / geoip / port 的行", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdCustom(
		fwdRule("ipCidr", "10.0.0.0/8", "美国"), fwdRule("geoip", "us", "block"), fwdRule("port", "443", "美国")))}), fwdWant("none", nil, nil, fwdNone))

	// 出口不在成员表里也照收(这里不查成员表)
	fwdCheck(t, "出口不在成员表里照收", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdCustom(fwdRule("domain", "g.example", "ghost")))}),
		fwdWant("domains", []string{"g.example"}, nil, ""))

	// 不生效的前置自定义分流整个不看
	for _, c := range []struct{ name, custom string }{
		{"停用", `"custom":{"enabled":false,"rules":[` + fwdRule("domainKeyword", "k", "美国") + `]}`},
		{"没有规则", `"custom":{"enabled":true,"rules":[]}`},
		{"规则全是写错的(整理时丢掉)", `"custom":{"enabled":true,"rules":[` + fwdRule("domain", "", "美国") + `,` + fwdRule("nope", "x", "美国") + `,{"type":"domainKeyword","value":"k"}]}`},
		{"整个 custom 不是对象", `"custom":5`},
	} {
		fwdCheck(t, c.name, fwdRun(t, fwdIn{routing: fwdRouting("direct", c.custom)}), fwdWant("none", nil, nil, fwdNone))
	}

	// 展不开 / 写不进
	for _, c := range []struct{ name, rule, want string }{
		{"代理的域名关键词", fwdRule("domainKeyword", "game", "美国"), owner + "「game」是域名关键词,dnsmasq 展不开"},
		{"代理的规则集", fwdRule("ruleset", "my-set", "美国"), owner + "「my-set」是规则集,dnsmasq 展不开"},
		{"拒绝的域名关键词", fwdRule("domainKeyword", "ad", "block"), ban + "「ad」是域名关键词,dnsmasq 展不开"},
		{"拒绝的规则集", fwdRule("ruleset", "ad-set", "block"), ban + "「ad-set」是规则集,dnsmasq 展不开"},
		{"代理的域名写不进", fwdRule("domain", "bad domain", "美国"), owner + "的域名「bad domain」写不进 dnsmasq（非法字符 / 标签超长）"},
		{"代理的后缀写不进", fwdRule("domainSuffix", "bad#.example", "美国"), owner + "的域名「bad#.example」写不进 dnsmasq（非法字符 / 标签超长）"},
		// 所属先拼上「要拒绝的」再拼「的域名」,所以这里读起来是「要拒绝的的域名」,这是有意保留的原文
		{"拒绝的域名写不进", fwdRule("domain", "bad domain", "block"), ban + "的域名「bad domain」写不进 dnsmasq（非法字符 / 标签超长）"},
	} {
		fwdCheck(t, c.name, fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdCustom(c.rule))}), fwdAll(c.want))
	}

	// 走直连的行连关键词 / 规则集 / 写不进的域名都不管
	fwdCheck(t, "直连的行全部跳过", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdCustom(
		fwdRule("domainKeyword", "k", "direct"), fwdRule("ruleset", "r", "direct"), fwdRule("domain", "bad domain", "direct")))}), fwdWant("none", nil, nil, fwdNone))

	// 前面的好行丢掉
	fwdCheck(t, "前面的好行一并丢掉", fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdCustom(
		fwdRule("domain", "ok.example", "美国"), fwdRule("geosite", "x", "美国"), fwdRule("domainKeyword", "k", "美国")))}), fwdAll(owner+"「k」是域名关键词,dnsmasq 展不开"))
}

func TestForwardPlanCustomRenamedBuiltins(t *testing.T) {
	renamed := `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`
	// 成员表里新旧名字都放上,让兜底(default=direct)在各种改名下都落到直连,单看自定义分流行的出口判定
	members := `["直连","拒绝","direct","block","DIRECT-X","BLOCK-X","美国"]`
	kwOwner := func(owner string) string { return owner + "「k」是域名关键词,dnsmasq 展不开" }
	for _, c := range []struct {
		name, builtin, out, want string
	}{
		{"改名后:老记号 direct 仍指内置直连 → 跳过", renamed, "direct", fwdWant("none", nil, nil, fwdNone)},
		{"改名后:新名字直连 → 跳过", renamed, "直连", fwdWant("none", nil, nil, fwdNone)},
		{"改名后:老记号 block 仍指内置拒绝", renamed, "block", fwdAll(kwOwner("前置自定义分流要拒绝的"))},
		{"改名后:新名字拒绝", renamed, "拒绝", fwdAll(kwOwner("前置自定义分流要拒绝的"))},
		{"没改名:「直连」只是节点名 → 当代理", "", "直连", fwdAll(kwOwner("前置自定义分流"))},
		{"没改名:「拒绝」只是节点名 → 当代理", "", "拒绝", fwdAll(kwOwner("前置自定义分流"))},
		{"没改名:direct → 跳过", "", "direct", fwdWant("none", nil, nil, fwdNone)},
		{"没改名:block → 拒绝", "", "block", fwdAll(kwOwner("前置自定义分流要拒绝的"))},
		{"改名后老的「直连」不再是直连名时当代理", `{"direct":"DIRECT-X","block":"BLOCK-X"}`, "直连", fwdAll(kwOwner("前置自定义分流"))},
	} {
		fwdCheck(t, c.name, fwdRun(t, fwdIn{routing: fwdRouting("direct", fwdCustom(fwdRule("domainKeyword", "k", c.out))), members: members, builtin: c.builtin}), c.want)
	}
}

func TestForwardPlanOrdering(t *testing.T) {
	const owner = "前置自定义分流"
	pols := fwdPols(fwdPol("甲", "美国", `"domain":["p1.example"],"rulesets":["geosite-p1"]`), fwdPol("乙", "日本", `"domainSuffix":["p2.example"],"rulesets":["geosite-p2"]`))
	custom := fwdCustom(fwdRule("domain", "c1.example", "美国"), fwdRule("geosite", "c1", "美国"))

	// 自定义分流先于站点集;名单和展开表各自按这个顺序;rewriteDomains 最后
	fwdCheck(t, "整体顺序", fwdRun(t, fwdIn{routing: fwdRouting("direct", custom, pols), extra: `{"rewriteDomains":["r1.example"]}`}),
		fwdWant("domains", []string{"c1.example", "p1.example", "p2.example", "r1.example"}, [][2]string{
			{"geosite-c1", owner + "「c1」"}, {"geosite-p1", "站点集「甲」"}, {"geosite-p2", "站点集「乙」"},
		}, ""))

	// 自定义分流先于站点集:自定义的展不开先报,站点集的写不进不报
	fwdCheck(t, "自定义的关键词先于站点集的写不进", fwdRun(t, fwdIn{routing: fwdRouting("direct",
		fwdCustom(fwdRule("domainKeyword", "k", "美国")), fwdPols(fwdPol("坏", "美国", `"domain":["bad domain"]`)))}),
		fwdAll(owner+"「k」是域名关键词,dnsmasq 展不开"))
	fwdCheck(t, "自定义的写不进先于站点集的关键词", fwdRun(t, fwdIn{routing: fwdRouting("direct",
		fwdCustom(fwdRule("domain", "bad domain", "美国")), fwdPols(fwdPol("关键词", "美国", `"domainKeyword":["k"]`)))}),
		fwdAll(owner+"的域名「bad domain」写不进 dnsmasq（非法字符 / 标签超长）"))
	fwdCheck(t, "自定义都过了,站点集的关键词才报", fwdRun(t, fwdIn{routing: fwdRouting("direct",
		fwdCustom(fwdRule("domain", "ok.example", "美国")), fwdPols(fwdPol("关键词", "美国", `"domainKeyword":["k"]`)))}),
		fwdAll("站点集「关键词」用了域名关键词,dnsmasq 展不开"))

	// 自定义分流与站点集写同一个域名:去重只留先到的
	fwdCheck(t, "自定义与站点集的重复域名去重", fwdRun(t, fwdIn{routing: fwdRouting("direct",
		fwdCustom(fwdRule("domain", "Same.Example", "美国")), fwdPols(fwdPol("甲", "美国", `"domain":["same.example","other.example"]`)))}),
		fwdWant("domains", []string{"same.example", "other.example"}, nil, ""))
}

func TestForwardPlanRewriteDomains(t *testing.T) {
	base := fwdRouting("direct")
	for _, c := range []struct {
		name, routing, extra string
		want                 string
	}{
		{"只有改写域名", base, `{"rewriteDomains":["a.example","b.example"]}`, fwdWant("domains", []string{"a.example", "b.example"}, nil, "")},
		{"原样追加:不转小写不转 punycode", base, `{"rewriteDomains":["R.Example","中国.example"]}`, fwdWant("domains", []string{"R.Example", "中国.example"}, nil, "")},
		{"完全相同才去重,大小写不同各留一个", base, `{"rewriteDomains":["a.example","A.example","a.example"]}`, fwdWant("domains", []string{"a.example", "A.example"}, nil, "")},
		{"非字符串和空串丢掉", base, `{"rewriteDomains":["a.example","",5,null,{"x":1},["y.example"],true]}`, fwdWant("domains", []string{"a.example"}, nil, "")},
		{"不是数组:忽略", base, `{"rewriteDomains":"a.example"}`, fwdWant("none", nil, nil, fwdNone)},
		{"是对象:忽略", base, `{"rewriteDomains":{"0":"a.example"}}`, fwdWant("none", nil, nil, fwdNone)},
		{"空数组", base, `{"rewriteDomains":[]}`, fwdWant("none", nil, nil, fwdNone)},
		{"extra 是 null / 数字 / 数组 / 字符串", base, `5`, fwdWant("none", nil, nil, fwdNone)},
		{"extra 缺失", base, ``, fwdWant("none", nil, nil, fwdNone)},
		{"extra 没有 rewriteDomains", base, `{"other":["a.example"]}`, fwdWant("none", nil, nil, fwdNone)},
		{"与站点集域名:先到的是整理后的小写,改写域名同名小写去重", fwdRouting("direct", fwdPols(fwdPol("A", "美国", `"domain":["Dup.Example"]`))), `{"rewriteDomains":["dup.example","Dup.Example"]}`, fwdWant("domains", []string{"dup.example", "Dup.Example"}, nil, "")},
		{"站点集有展开项时改写域名照追加", fwdRouting("direct", fwdPols(fwdPol("A", "美国", `"rulesets":["geosite-x"]`))), `{"rewriteDomains":["r.example"]}`, fwdWant("domains", []string{"r.example"}, [][2]string{{"geosite-x", "站点集「A」"}}, "")},
	} {
		fwdCheck(t, c.name, fwdRun(t, fwdIn{routing: c.routing, extra: c.extra}), c.want)
	}
	// 改写域名自己不做合法性检查:写不进 dnsmasq 的也原样进名单,不会触发退成 all
	fwdCheck(t, "改写域名不检查合法性", fwdRun(t, fwdIn{routing: base, extra: `{"rewriteDomains":["bad domain"]}`}), fwdWant("domains", []string{"bad domain"}, nil, ""))
}

func TestForwardPlanDoesNotMutateInput(t *testing.T) {
	routing := mustJSON(t, fwdRouting("direct", fwdPols(fwdPol("A", "美国", `"domain":["X.Example"],"rulesets":["geosite-x"]`)), fwdCustom(fwdRule("domain", "c.example", "美国"))))
	extra := mustJSON(t, `{"rewriteDomains":["r.example"]}`)
	sel := map[string]any{"A": "美国"}
	rb, _ := json.Marshal(routing)
	eb, _ := json.Marshal(extra)
	sb, _ := json.Marshal(sel)
	first := roundTrip(t, forwardPlan(routing, []string{"direct", "美国"}, defaultBuiltin, sel, extra))
	second := roundTrip(t, forwardPlan(routing, []string{"direct", "美国"}, defaultBuiltin, sel, extra))
	if !reflect.DeepEqual(first, second) {
		t.Error("两次调用结果不同")
	}
	ra, _ := json.Marshal(routing)
	ea, _ := json.Marshal(extra)
	sa, _ := json.Marshal(sel)
	if string(rb) != string(ra) || string(eb) != string(ea) || string(sb) != string(sa) {
		t.Error("入参被改写")
	}
}

// 输出外形:键序与空数组(不是 null),逐字节比较。
func TestForwardPlanJSONShape(t *testing.T) {
	clsNoGeo()
	mk := func(routing, extra string) string {
		b, err := json.Marshal(forwardPlan(mustJSON(t, routing), []string{"direct", "美国"}, defaultBuiltin, map[string]any{}, mustJSON(t, extra)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got, want := mk(fwdRouting("direct"), `null`),
		`{"mode":"none","domains":[],"expand":[],"reason":"`+fwdNone+`"}`; got != want {
		t.Errorf("none:\n有: %s\n要: %s", got, want)
	}
	if got, want := mk(fwdRouting("美国"), `null`),
		`{"mode":"all","domains":[],"expand":[],"reason":"兜底「其他」走代理"}`; got != want {
		t.Errorf("all:\n有: %s\n要: %s", got, want)
	}
	if got, want := mk(fwdRouting("direct", fwdPols(fwdPol("P", "美国", `"domain":["a.example"],"rulesets":["geosite-x"]`))), `null`),
		`{"mode":"domains","domains":["a.example"],"expand":[{"tag":"geosite-x","owner":"站点集「P」"}],"reason":""}`; got != want {
		t.Errorf("domains:\n有: %s\n要: %s", got, want)
	}
	if got, want := mk(fwdRouting("direct", fwdPols(fwdPol("P", "美国", `"rulesets":["geosite-x"]`))), `null`),
		`{"mode":"domains","domains":[],"expand":[{"tag":"geosite-x","owner":"站点集「P」"}],"reason":""}`; got != want {
		t.Errorf("只有展开项:\n有: %s\n要: %s", got, want)
	}
}

// 走完整的协议入口:缺省参数与非法形状参数。
func TestForwardPlanHandlerArgs(t *testing.T) {
	reg := protocol.NewRegistry()
	Register(reg)
	run := func(args string) any {
		t.Helper()
		return call(t, reg, []byte(`{"protocol":1,"method":"forwardPlan","args":`+args+`}`))
	}
	routing := `{"fallbackDefault":"美国","policies":[{"name":"A","default":"美国","domain":["a.example"]}]}`
	// 成员缺省 ["direct"]:default=美国 不在成员里,兜底和 A 都落到第一项 direct → 一个都不转发
	fwdCheck(t, "只给 routing", run(`[`+routing+`]`), fwdWant("none", nil, nil, fwdNone))
	fwdCheck(t, "成员显式 null 同缺省", run(`[`+routing+`,null]`), fwdWant("none", nil, nil, fwdNone))
	fwdCheck(t, "成员不是数组按缺省", run(`[`+routing+`,"x"]`), fwdWant("none", nil, nil, fwdNone))
	fwdCheck(t, "成员含美国:兜底走代理", run(`[`+routing+`,["direct","美国"]]`), fwdAll("兜底「其他」走代理"))
	fwdCheck(t, "选择让兜底回直连", run(`[`+routing+`,["direct","美国"],null,{"其他":"direct"}]`), fwdWant("domains", []string{"a.example"}, nil, ""))
	fwdCheck(t, "选择不是对象按没有", run(`[`+routing+`,["direct","美国"],null,"x"]`), fwdAll("兜底「其他」走代理"))
	fwdCheck(t, "extra 经协议带进来", run(`[{"fallbackDefault":"direct"},["direct"],null,{},{"rewriteDomains":["r.example"]}]`), fwdWant("domains", []string{"r.example"}, nil, ""))
	fwdCheck(t, "内置改名经协议带进来", run(`[{"fallbackDefault":"direct"},["直连"],{"direct":"直连","block":"拒绝"}]`), fwdWant("none", nil, nil, fwdNone))
	fwdCheck(t, "没有参数", run(`[]`), fwdWant("none", nil, nil, fwdNone))
}
