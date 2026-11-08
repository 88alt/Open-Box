package routing

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/protocol"
)

// 自写测试向量:bypassPlan(系统入口哪些目标 IP 可以直接放走、不进内核)。输入自己拟,期望值按下面的规则人工推算:
//   - 选项缺省:members [direct]、内置缺省、enabled 为真、dnsMode dnsmasq、fakeIp 为假;传了就按 JS 真假判断
//     (enabled: null 也算关;members / builtin / selections 传 null 算没传;dnsMode 只有恰好是字符串 "off" 才算 DNS 禁用);
//   - domainSafe = FakeIP 开着且 DNS 没禁用,结果里的 fakeIp 就是它(关着的返回也一样);
//   - 先看总开关:关着直接返回关闭原因(不带任何提示);
//   - 提示(reason 里排在最前面,用分号连接):FakeIP 开且 DNS 禁用 → 一句;domainSafe → 一句;
//   - 前置自定义分流(启用且有规则)逐行看,出口是内置直连的跳过:port 行直接关;ipCidr / geoip 行收进「前置自定义分流」这一条在前规则;
//     ruleUrl 行在 domainSafe 时只收规则集名,否则关;ruleset 行一律关;其余(域名 / 后缀 / 关键词 / geosite)不是 domainSafe 就关;
//   - 有终端出口不是内置直连:不带 ignoreProxyClients 就关,带了只补一句提示(排在 FakeIP / DNS 提示之后);
//   - 不是 domainSafe 时:此刻不走直连的站点集只要靠域名 / 非 geoip 规则集匹配就关;兜底不走直连也关;
//   - 关着的返回:sets / pending 都是空数组,原因 = 关闭原因 + 之前攒下的提示;
//   - 之后按站点集顺序:不走直连的站点集里有 geoip- / ipCidr / 规则集链接就记成「在前规则」;走直连的站点集里没有 geoip- 规则集就跳过,
//     有的话:此前没有在前规则 → 直接进 sets(去重保序),有 → 记成待核对项,带着当时全部在前规则的拷贝;
//   - sets 和待核对项都空 → 关,原因是「没有可用的 IP 集合」;否则 enabled = sets 非空,原因 = 提示 + 每个待核对项一句。

const (
	bpOff   = "后端设置里关掉了「直连不进内核」:所有流量都进内核,入口不放行"
	bpN1    = "DNS 禁用模式下终端的查询不一定经内核,FakeIP 不作旁路前提,按真实 IP 计算"
	bpN2    = "FakeIP 试验开着:走代理的域名规则按占位地址计,不挡直连集合（终端自带加密 DNS / 开启前已缓存的真实地址 / 转发漏匹配不在此保证内）"
	bpN3    = "有终端被指定走代理:这些集合只用于入口按来源旁路,代理终端本身由入口 guard 拦下"
	bpNone  = "走直连的站点集里没有 geoip 规则集,入口没有可用的 IP 集合"
	bpCli   = "有终端被指定走代理,该终端的全部流量都要进内核"
	bpURLA  = "https://example.com/a.srs" // 规则集名 list-b99d2d1f(FNV-1a,独立 Python 另算)
	bpTagA  = "list-b99d2d1f"
	bpURLAd = "https://example.com/ads.srs" // list-5a070e3e
	bpTagAd = "list-5a070e3e"
)

func bpPort(v string) string {
	return "前置自定义分流「" + v + "」按端口分流,任何目标 IP 都可能命中,入口旁路不能越过它"
}
func bpList(v string) string {
	return "前置自定义分流「" + v + "」是规则集链接 / 规则集,里面可能有 IP 段,入口旁路不能越过它"
}
func bpDom(v string) string {
	return "前置自定义分流「" + v + "」按域名分流,它解析出来的 IP 在入口分不出来,旁路不能越过它"
}
func bpPolDom(n string) string {
	return "站点集「" + n + "」按域名走代理 / 拒绝:有域名的访问只按域名判,这些域名解析出来的真实地址在入口分不出来,按 IP 放走会放错（开 FakeIP 后它们拿的是占位地址,就不挡了）"
}
func bpFB(n string) string {
	return "兜底「" + n + "」走代理:没命中域名规则的域名都走兜底,它们解析出来的真实地址在入口分不出来,按 IP 放走会放错（开 FakeIP 后它们拿的是占位地址,就不挡了）"
}
func bpPend(p string, names ...string) string {
	return "站点集「" + p + "」的集合要先和前面带 IP 条件的「" + strings.Join(names, "」「") + "」核对重叠,部署时决定"
}
func bpJoin(parts ...string) string { return strings.Join(parts, ";") }

func bpWant(enabled bool, sets, pending string, fake bool, reason string) string {
	if sets == "" {
		sets = "[]"
	}
	if pending == "" {
		pending = "[]"
	}
	rb, _ := json.Marshal(reason)
	return fmt.Sprintf(`{"enabled":%t,"sets":%s,"pending":%s,"fakeIp":%t,"reason":%s}`, enabled, sets, pending, fake, rb)
}

// 关着的返回(sets / pending 空)
func bpClosed(fake bool, reason string) string { return bpWant(false, "", "", fake, reason) }

func bpP(name, def, extra string) string {
	return `{"name":"` + name + `","default":"` + def + `",` + extra + `}`
}

func bpR(fb string, pols []string, extra ...string) string {
	s := `{"fallbackDefault":"` + fb + `","policies":[` + strings.Join(pols, ",") + `]`
	for _, e := range extra {
		s += "," + e
	}
	return s + "}"
}

func bpCR(typ, v, out string) string {
	return `{"type":"` + typ + `","value":"` + v + `","outbound":"` + out + `"}`
}

func bpC(rules ...string) string {
	return `"custom":{"enabled":true,"rules":[` + strings.Join(rules, ",") + `]}`
}

const bpM = `"members":["direct","block","美国","日本"]`

func bpOpts(extra ...string) string {
	return `{` + strings.Join(append([]string{bpM}, extra...), ",") + `}`
}

var (
	bpCN = bpP("国内", "direct", `"rulesets":["geoip-cn"]`)
	bpJP = bpP("日本", "direct", `"rulesets":["geoip-jp"]`)
	bpKR = bpP("韩国", "direct", `"rulesets":["geoip-kr"]`)
	bpUS = bpP("海外", "美国", `"rulesets":["geoip-us"]`)
	bpEU = bpP("欧洲", "美国", `"ipCidr":["2.2.2.0/24"]`)
	bpNF = bpP("流媒体", "美国", `"domainSuffix":["netflix.com"]`)
)

func bpRun(t *testing.T, routing, opts string) any {
	t.Helper()
	clsNoGeo()
	if routing == "" {
		routing = `{"fallbackDefault":"direct"}`
	}
	if opts == "" {
		opts = `null`
	}
	return roundTrip(t, bypassPlan(mustJSON(t, routing), mustJSON(t, opts)))
}

type bpCase struct {
	name, routing, opts, want string
}

func bpCheckAll(t *testing.T, cases []bpCase) {
	t.Helper()
	for _, c := range cases {
		clsCheck(t, c.name, bpRun(t, c.routing, c.opts), c.want)
	}
}

func TestBypassPlanGates(t *testing.T) {
	r := bpR("direct", []string{bpCN})
	portUS := bpR("direct", []string{bpCN}, bpC(bpCR("port", "443", "美国")))
	pc := `"clientRoutes":[{"ip":"192.168.1.5","outbound":"美国"}]`
	bpCheckAll(t, []bpCase{
		{"总开关关着", r, `{"enabled":false}`, bpClosed(false, bpOff)},
		{"关着且 FakeIP 开着:fakeIp 仍是 true,不带提示", r, `{"enabled":false,"fakeIp":true}`, bpClosed(true, bpOff)},
		{"关着且 FakeIP 开着 DNS 禁用:fakeIp 是 false", r, `{"enabled":false,"fakeIp":true,"dnsMode":"off"}`, bpClosed(false, bpOff)},
		{"enabled=null 也算关", r, `{"enabled":null}`, bpClosed(false, bpOff)},
		{"关着优先于端口行和走代理的终端", portUS, bpOpts(`"enabled":false`, pc), bpClosed(false, bpOff)},
		{"基础:走直连的站点集带 geoip", r, `{}`, bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"没有站点集", bpR("direct", nil), `{}`, bpClosed(false, bpNone)},
		{"没有站点集 + FakeIP:提示跟在原因后面", bpR("direct", nil), `{"fakeIp":true}`, bpClosed(true, bpJoin(bpNone, bpN2))},
		{"没有站点集 + FakeIP + DNS 禁用", bpR("direct", nil), `{"fakeIp":true,"dnsMode":"off"}`, bpClosed(false, bpJoin(bpNone, bpN1))},
		{"有站点集 + FakeIP + DNS 禁用:只剩 DNS 提示", r, `{"fakeIp":true,"dnsMode":"off"}`, bpWant(true, `["geoip-cn"]`, "", false, bpN1)},
		{"有站点集 + FakeIP:只剩 FakeIP 提示", r, `{"fakeIp":true}`, bpWant(true, `["geoip-cn"]`, "", true, bpN2)},
	})
}

func TestBypassPlanSets(t *testing.T) {
	bpCheckAll(t, []bpCase{
		{"sets 去重保序,只收 geoip- 前缀",
			bpR("direct", []string{
				bpP("A", "direct", `"rulesets":["geoip-cn","geosite-x","geoip-jp"]`),
				bpP("B", "direct", `"rulesets":["geoip-jp","geoip-kr"]`)}),
			`{}`, bpWant(true, `["geoip-cn","geoip-jp","geoip-kr"]`, "", false, "")},
		{"规则集链接不进 sets", bpR("direct", []string{bpP("A", "direct", `"rulesets":["`+bpTagA+`","geoip-cn"]`)}), `{}`, bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"走直连但只有 geosite / ipCidr / 域名:什么都不进", bpR("direct", []string{
			bpP("A", "direct", `"rulesets":["geosite-x"]`), bpP("B", "direct", `"ipCidr":["1.1.1.0/24"]`), bpP("C", "direct", `"domain":["a.com"]`)}),
			`{}`, bpClosed(false, bpNone)},
		{"停用的站点集不算", bpR("direct", []string{bpP("A", "direct", `"enabled":false,"rulesets":["geoip-cn"]`)}), `{}`, bpClosed(false, bpNone)},
		{"没有任何条件的站点集被丢掉", bpR("direct", []string{bpP("A", "direct", `"rulesets":[]`)}), `{}`, bpClosed(false, bpNone)},
		{"选择改成走代理后记成在前规则,什么都不进",
			bpR("direct", []string{bpCN, bpUS}), bpOpts(`"selections":{"国内":"美国"}`), bpClosed(false, bpNone)},
		{"走代理的站点集被选成直连后进 sets",
			bpR("direct", []string{bpCN, bpUS}), bpOpts(`"selections":{"海外":"direct"}`), bpWant(true, `["geoip-cn","geoip-us"]`, "", false, "")},
		{"没有选择时走代理的站点集排在后面,只记在前规则", bpR("direct", []string{bpCN, bpUS}), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"选择顺着 selector 链下钻到直连",
			bpR("direct", []string{bpCN, bpUS}), bpOpts(`"selections":{"海外":"auto","auto":"direct"}`), bpWant(true, `["geoip-cn","geoip-us"]`, "", false, "")},
		{"选择成环:叶子不是直连", bpR("direct", []string{bpCN, bpUS}), bpOpts(`"selections":{"海外":"a","a":"b","b":"a"}`), bpWant(true, `["geoip-cn"]`, "", false, "")},
	})
}

func TestBypassPlanPending(t *testing.T) {
	usFull := bpP("海外", "美国", `"rulesets":["geoip-us","`+bpTagA+`"],"ipCidr":["1.1.1.0/24"]`)
	usCidr := bpP("海外", "美国", `"rulesets":["geoip-us"],"ipCidr":["1.1.1.0/24"]`)
	bpCheckAll(t, []bpCase{
		{"直连站点集排在走代理的后面:待核对,带在前规则(不带 lists 键)",
			bpR("direct", []string{bpCN, usCidr, bpJP}), bpOpts(),
			bpWant(true, `["geoip-cn"]`, `[{"policy":"日本","sets":["geoip-jp"],"against":[{"name":"海外","geoip":["geoip-us"],"cidrs":["1.1.1.0/24"]}]}]`, false, bpPend("日本", "海外"))},
		{"在前规则带规则集链接名(FakeIP 开着才不挡)",
			bpR("direct", []string{bpCN, usFull, bpJP}), bpOpts(`"fakeIp":true`),
			bpWant(true, `["geoip-cn"]`, `[{"policy":"日本","sets":["geoip-jp"],"against":[{"name":"海外","geoip":["geoip-us"],"cidrs":["1.1.1.0/24"],"lists":["`+bpTagA+`"]}]}]`, true, bpJoin(bpN2, bpPend("日本", "海外")))},
		{"两条在前规则按站点集顺序",
			bpR("direct", []string{bpUS, bpEU, bpJP}), bpOpts(),
			bpWant(false, "", `[{"policy":"日本","sets":["geoip-jp"],"against":[{"name":"海外","geoip":["geoip-us"],"cidrs":[]},{"name":"欧洲","geoip":[],"cidrs":["2.2.2.0/24"]}]}]`, false, bpPend("日本", "海外", "欧洲"))},
		{"待核对项的在前规则只含当时已有的",
			bpR("direct", []string{bpUS, bpJP, bpEU, bpKR}), bpOpts(),
			bpWant(false, "", `[{"policy":"日本","sets":["geoip-jp"],"against":[{"name":"海外","geoip":["geoip-us"],"cidrs":[]}]},`+
				`{"policy":"韩国","sets":["geoip-kr"],"against":[{"name":"海外","geoip":["geoip-us"],"cidrs":[]},{"name":"欧洲","geoip":[],"cidrs":["2.2.2.0/24"]}]}]`,
				false, bpJoin(bpPend("日本", "海外"), bpPend("韩国", "海外", "欧洲")))},
		{"走直连但没有 geoip 的站点集在在前规则之后也跳过",
			bpR("direct", []string{bpUS, bpP("丙", "direct", `"ipCidr":["9.9.9.0/24"]`), bpJP}), bpOpts(),
			bpWant(false, "", `[{"policy":"日本","sets":["geoip-jp"],"against":[{"name":"海外","geoip":["geoip-us"],"cidrs":[]}]}]`, false, bpPend("日本", "海外"))},
		{"待核对项的 sets 是该站点集全部 geoip,不和已进 sets 的去重",
			bpR("direct", []string{bpCN, bpUS, bpP("国内2", "direct", `"rulesets":["geoip-cn","geoip-jp"]`)}), bpOpts(),
			bpWant(true, `["geoip-cn"]`, `[{"policy":"国内2","sets":["geoip-cn","geoip-jp"],"against":[{"name":"海外","geoip":["geoip-us"],"cidrs":[]}]}]`, false, bpPend("国内2", "海外"))},
		{"提示在前、待核对在后",
			bpR("direct", []string{bpJP, bpUS, bpKR}), bpOpts(`"fakeIp":true`),
			bpWant(true, `["geoip-jp"]`, `[{"policy":"韩国","sets":["geoip-kr"],"against":[{"name":"海外","geoip":["geoip-us"],"cidrs":[]}]}]`, true, bpJoin(bpN2, bpPend("韩国", "海外")))},
		{"FakeIP 开着:只有规则集链接的走代理站点集也记成在前规则(只带 lists)",
			bpR("direct", []string{bpP("订阅", "美国", `"rulesets":["`+bpTagA+`"]`), bpJP}), bpOpts(`"fakeIp":true`),
			bpWant(false, "", `[{"policy":"日本","sets":["geoip-jp"],"against":[{"name":"订阅","geoip":[],"cidrs":[],"lists":["`+bpTagA+`"]}]}]`, true, bpJoin(bpN2, bpPend("日本", "订阅")))},
		{"FakeIP 开着:只按域名走代理的站点集不记在前规则",
			bpR("direct", []string{bpNF, bpJP}), bpOpts(`"fakeIp":true`), bpWant(true, `["geoip-jp"]`, "", true, bpN2)},
		{"FakeIP 开着:只有 geosite 的走代理站点集不记在前规则",
			bpR("direct", []string{bpP("G", "美国", `"rulesets":["geosite-netflix"]`), bpJP}), bpOpts(`"fakeIp":true`), bpWant(true, `["geoip-jp"]`, "", true, bpN2)},
	})
}

func TestBypassPlanDomainBlock(t *testing.T) {
	bpCheckAll(t, []bpCase{
		{"走代理的站点集按域名后缀", bpR("direct", []string{bpCN, bpNF}), bpOpts(), bpClosed(false, bpPolDom("流媒体"))},
		{"按域名", bpR("direct", []string{bpCN, bpP("流媒体", "美国", `"domain":["a.com"]`)}), bpOpts(), bpClosed(false, bpPolDom("流媒体"))},
		{"按关键词", bpR("direct", []string{bpCN, bpP("流媒体", "美国", `"domainKeyword":["flix"]`)}), bpOpts(), bpClosed(false, bpPolDom("流媒体"))},
		{"按 geosite 规则集", bpR("direct", []string{bpCN, bpP("流媒体", "美国", `"rulesets":["geosite-netflix"]`)}), bpOpts(), bpClosed(false, bpPolDom("流媒体"))},
		{"按规则集链接", bpR("direct", []string{bpCN, bpP("流媒体", "美国", `"rulesets":["`+bpTagA+`"]`)}), bpOpts(), bpClosed(false, bpPolDom("流媒体"))},
		{"域名加 geoip 并存也算", bpR("direct", []string{bpCN, bpP("流媒体", "美国", `"domain":["a.com"],"rulesets":["geoip-us"]`)}), bpOpts(), bpClosed(false, bpPolDom("流媒体"))},
		{"拒绝的站点集也算", bpR("direct", []string{bpCN, bpP("广告", "block", `"domainSuffix":["ads.com"]`)}), bpOpts(), bpClosed(false, bpPolDom("广告"))},
		{"只有 geoip / ipCidr 的走代理站点集不挡", bpR("direct", []string{bpCN, bpP("海外", "美国", `"rulesets":["geoip-us"],"ipCidr":["1.1.1.0/24"]`)}), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"此刻走直连的按域名站点集不挡", bpR("direct", []string{bpP("流媒体", "direct", `"domainSuffix":["netflix.com"]`), bpCN}), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"选择把它改成直连后不挡", bpR("direct", []string{bpCN, bpNF}), bpOpts(`"selections":{"流媒体":"direct"}`), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"选择把直连站点集改成走代理后要挡", bpR("direct", []string{bpCN, bpP("流媒体", "direct", `"domainSuffix":["netflix.com"]`)}), bpOpts(`"selections":{"流媒体":"美国"}`), bpClosed(false, bpPolDom("流媒体"))},
		{"停用的站点集不挡", bpR("direct", []string{bpCN, bpP("流媒体", "美国", `"enabled":false,"domainSuffix":["netflix.com"]`)}), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"多个都该挡时报第一个", bpR("direct", []string{bpCN, bpP("甲", "美国", `"domain":["a.com"]`), bpP("乙", "美国", `"domain":["b.com"]`)}), bpOpts(), bpClosed(false, bpPolDom("甲"))},
		{"FakeIP 开着:都不挡,只带提示", bpR("美国", []string{bpCN, bpNF}), bpOpts(`"fakeIp":true`), bpWant(true, `["geoip-cn"]`, "", true, bpN2)},
		{"FakeIP 开着 DNS 禁用:照挡,原因后跟 DNS 提示", bpR("direct", []string{bpCN, bpNF}), bpOpts(`"fakeIp":true,"dnsMode":"off"`), bpClosed(false, bpJoin(bpPolDom("流媒体"), bpN1))},
	})
}

func TestBypassPlanFallback(t *testing.T) {
	cn := []string{bpCN}
	bpCheckAll(t, []bpCase{
		{"兜底走节点", bpR("美国", cn), bpOpts(), bpClosed(false, bpFB("其他"))},
		{"兜底走拒绝", bpR("block", cn), bpOpts(), bpClosed(false, bpFB("其他"))},
		{"兜底写 proxy:取第一个非内置成员", bpR("proxy", cn), bpOpts(), bpClosed(false, bpFB("其他"))},
		{"兜底 proxy 但成员只有 direct:落到成员第一项,直连", bpR("proxy", cn), `{}`, bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"兜底没写:落到成员第一项(direct)", bpR("", cn), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"兜底直连", bpR("direct", cn), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"选择把兜底改成走节点", bpR("direct", cn), bpOpts(`"selections":{"其他":"美国"}`), bpClosed(false, bpFB("其他"))},
		{"兜底改名后原因用新名字", bpR("美国", cn, `"fallbackName":"漏网"`), bpOpts(), bpClosed(false, bpFB("漏网"))},
		{"兜底改名后选择按新名字取", bpR("美国", cn, `"fallbackName":"漏网"`), bpOpts(`"selections":{"漏网":"direct"}`), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"兜底改名后按老名字的选择不起作用", bpR("direct", cn, `"fallbackName":"漏网"`), bpOpts(`"selections":{"其他":"美国"}`), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"站点集和兜底都不行:先报站点集", bpR("美国", []string{bpCN, bpNF}), bpOpts(), bpClosed(false, bpPolDom("流媒体"))},
		{"FakeIP 开着:兜底走节点不挡", bpR("美国", cn), bpOpts(`"fakeIp":true`), bpWant(true, `["geoip-cn"]`, "", true, bpN2)},
		{"FakeIP 开着 DNS 禁用:兜底照挡,带 DNS 提示", bpR("美国", cn), bpOpts(`"fakeIp":true,"dnsMode":"off"`), bpClosed(false, bpJoin(bpFB("其他"), bpN1))},
	})
}

func TestBypassPlanCustom(t *testing.T) {
	cn := []string{bpCN}
	custom := func(rules ...string) string { return bpR("direct", cn, bpC(rules...)) }
	renamed := `"builtin":{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`
	rm := `"members":["直连","拒绝","美国"]`
	earlyCIDR := `{"name":"前置自定义分流","geoip":[],"cidrs":["10.0.0.0/8"]}`
	bpCheckAll(t, []bpCase{
		// 端口行
		{"端口行走节点:关", custom(bpCR("port", "443", "美国")), bpOpts(), bpClosed(false, bpPort("443"))},
		{"端口行即使 FakeIP 开着也关,带提示", custom(bpCR("port", "443", "美国")), bpOpts(`"fakeIp":true`), bpClosed(true, bpJoin(bpPort("443"), bpN2))},
		{"端口行 FakeIP + DNS 禁用", custom(bpCR("port", "443", "美国")), bpOpts(`"fakeIp":true,"dnsMode":"off"`), bpClosed(false, bpJoin(bpPort("443"), bpN1))},
		{"端口行走拒绝也关", custom(bpCR("port", "443", "block")), bpOpts(), bpClosed(false, bpPort("443"))},
		{"端口行走直连:跳过", custom(bpCR("port", "443", "direct")), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"改名后端口行写老记号 direct:仍是直连", custom(bpCR("port", "443", "direct")), `{` + rm + `,` + renamed + `}`, bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"改名后端口行写新名字:是直连", custom(bpCR("port", "443", "直连")), `{` + rm + `,` + renamed + `}`, bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"改名后端口行写 block:不是直连", custom(bpCR("port", "443", "block")), `{` + rm + `,` + renamed + `}`, bpClosed(false, bpPort("443"))},
		{"没改名时端口行写「直连」只是节点名", custom(bpCR("port", "443", "直连")), `{"members":["direct","直连"]}`, bpClosed(false, bpPort("443"))},
		// ipCidr / geoip 行
		{"ipCidr 行:成为在前规则,后面的直连站点集变待核对", custom(bpCR("ipCidr", "10.0.0.0/8", "美国")), bpOpts(),
			bpWant(false, "", `[{"policy":"国内","sets":["geoip-cn"],"against":[`+earlyCIDR+`]}]`, false, bpPend("国内", "前置自定义分流"))},
		{"ipCidr 行走拒绝也算在前规则", custom(bpCR("ipCidr", "10.0.0.0/8", "block")), bpOpts(),
			bpWant(false, "", `[{"policy":"国内","sets":["geoip-cn"],"against":[`+earlyCIDR+`]}]`, false, bpPend("国内", "前置自定义分流"))},
		{"ipCidr 行走直连:跳过", custom(bpCR("ipCidr", "10.0.0.0/8", "direct")), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"只有 geoip 行也成为在前规则", custom(bpCR("geoip", "us", "美国")), bpOpts(),
			bpWant(false, "", `[{"policy":"国内","sets":["geoip-cn"],"against":[{"name":"前置自定义分流","geoip":["geoip-us"],"cidrs":[]}]}]`, false, bpPend("国内", "前置自定义分流"))},
		{"ipCidr 与 geoip 行分别按顺序收", custom(bpCR("ipCidr", "10.0.0.0/8", "美国"), bpCR("geoip", "us", "美国"), bpCR("ipCidr", "172.16.0.0/12", "日本")), bpOpts(),
			bpWant(false, "", `[{"policy":"国内","sets":["geoip-cn"],"against":[{"name":"前置自定义分流","geoip":["geoip-us"],"cidrs":["10.0.0.0/8","172.16.0.0/12"]}]}]`, false, bpPend("国内", "前置自定义分流"))},
		{"自定义分流的在前规则排在站点集的前面", bpR("direct", []string{bpUS, bpJP}, bpC(bpCR("ipCidr", "10.0.0.0/8", "美国"))), bpOpts(),
			bpWant(false, "", `[{"policy":"日本","sets":["geoip-jp"],"against":[`+earlyCIDR+`,{"name":"海外","geoip":["geoip-us"],"cidrs":[]}]}]`, false, bpPend("日本", "前置自定义分流", "海外"))},
		// 域名类行
		{"域名行不是 domainSafe:关", custom(bpCR("domain", "a.com", "美国")), bpOpts(), bpClosed(false, bpDom("a.com"))},
		{"后缀行", custom(bpCR("domainSuffix", "a.com", "美国")), bpOpts(), bpClosed(false, bpDom("a.com"))},
		{"关键词行", custom(bpCR("domainKeyword", "flix", "美国")), bpOpts(), bpClosed(false, bpDom("flix"))},
		{"geosite 行按域名算", custom(bpCR("geosite", "netflix", "美国")), bpOpts(), bpClosed(false, bpDom("netflix"))},
		{"前面的 ipCidr 行不妨碍后面的域名行关", custom(bpCR("ipCidr", "10.0.0.0/8", "美国"), bpCR("domain", "a.com", "美国")), bpOpts(), bpClosed(false, bpDom("a.com"))},
		{"域名行走直连:跳过", custom(bpCR("domain", "a.com", "direct")), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"域名行在前、端口行在后:报域名行", custom(bpCR("domain", "a.com", "美国"), bpCR("port", "443", "美国")), bpOpts(), bpClosed(false, bpDom("a.com"))},
		{"端口行在前、域名行在后:报端口行", custom(bpCR("port", "443", "美国"), bpCR("domain", "a.com", "美国")), bpOpts(), bpClosed(false, bpPort("443"))},
		{"FakeIP 开着:域名类行不挡,也不产生在前规则", custom(bpCR("domain", "a.com", "美国"), bpCR("domainSuffix", "b.com", "美国"), bpCR("geosite", "netflix", "美国")), bpOpts(`"fakeIp":true`),
			bpWant(true, `["geoip-cn"]`, "", true, bpN2)},
		{"FakeIP 开着 DNS 禁用:域名行照挡,带 DNS 提示", custom(bpCR("domain", "a.com", "美国")), bpOpts(`"fakeIp":true,"dnsMode":"off"`), bpClosed(false, bpJoin(bpDom("a.com"), bpN1))},
		// 规则集行
		{"规则集链接行不是 domainSafe:关", custom(bpCR("ruleUrl", bpURLA, "美国")), bpOpts(), bpClosed(false, bpList(bpURLA))},
		{"规则集链接行 FakeIP 开着:只收规则集名", custom(bpCR("ruleUrl", bpURLA, "美国")), bpOpts(`"fakeIp":true`),
			bpWant(false, "", `[{"policy":"国内","sets":["geoip-cn"],"against":[{"name":"前置自定义分流","geoip":[],"cidrs":[],"lists":["`+bpTagA+`"]}]}]`, true, bpJoin(bpN2, bpPend("国内", "前置自定义分流")))},
		{"多条规则集链接与 ipCidr 混排:lists 与 cidrs 各按出现顺序", custom(bpCR("ruleUrl", bpURLA, "美国"), bpCR("ipCidr", "10.0.0.0/8", "美国"), bpCR("ruleUrl", bpURLAd, "日本")), bpOpts(`"fakeIp":true`),
			bpWant(false, "", `[{"policy":"国内","sets":["geoip-cn"],"against":[{"name":"前置自定义分流","geoip":[],"cidrs":["10.0.0.0/8"],"lists":["`+bpTagA+`","`+bpTagAd+`"]}]}]`, true, bpJoin(bpN2, bpPend("国内", "前置自定义分流")))},
		{"规则集行一律关(FakeIP 开着也一样)", custom(bpCR("ruleset", "myset", "美国")), bpOpts(`"fakeIp":true`), bpClosed(true, bpJoin(bpList("myset"), bpN2))},
		{"规则集行不是 domainSafe 同样关", custom(bpCR("ruleset", "myset", "美国")), bpOpts(), bpClosed(false, bpList("myset"))},
		{"规则集链接行走直连:跳过", custom(bpCR("ruleUrl", bpURLA, "direct")), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		// 不起作用的情形
		{"自定义分流停用:端口行不算", bpR("direct", cn, `"custom":{"enabled":false,"rules":[`+bpCR("port", "443", "美国")+`]}`), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		{"自定义分流没有有效规则(写错的行整理时丢掉)", custom(bpCR("port", "abc", "美国")), bpOpts(), bpWant(true, `["geoip-cn"]`, "", false, "")},
		// 与终端的先后
		{"端口行先于走代理的终端", custom(bpCR("port", "443", "美国")), bpOpts(`"clientRoutes":[{"outbound":"美国"}]`), bpClosed(false, bpPort("443"))},
		{"自定义分流没拦住时报终端", custom(bpCR("ipCidr", "10.0.0.0/8", "美国")), bpOpts(`"clientRoutes":[{"outbound":"美国"}]`), bpClosed(false, bpCli)},
	})
}

func TestBypassPlanProxyClients(t *testing.T) {
	pc := `"clientRoutes":[{"ip":"192.168.1.5","outbound":"美国"}]`
	ig := `"ignoreProxyClients":true`
	bpCheckAll(t, []bpCase{
		{"有终端走代理:关", bpR("direct", []string{bpCN}), bpOpts(pc), bpClosed(false, bpCli)},
		{"带 FakeIP 提示", bpR("direct", []string{bpCN}), bpOpts(pc, `"fakeIp":true`), bpClosed(true, bpJoin(bpCli, bpN2))},
		{"带 DNS 禁用提示", bpR("direct", []string{bpCN}), bpOpts(pc, `"fakeIp":true,"dnsMode":"off"`), bpClosed(false, bpJoin(bpCli, bpN1))},
		{"终端先于站点集按域名的判断", bpR("direct", []string{bpCN, bpNF}), bpOpts(pc), bpClosed(false, bpCli)},
		{"终端先于兜底的判断", bpR("美国", []string{bpCN}), bpOpts(pc), bpClosed(false, bpCli)},
		{"ignoreProxyClients:不关,补一句", bpR("direct", []string{bpCN}), bpOpts(pc, ig), bpWant(true, `["geoip-cn"]`, "", false, bpN3)},
		{"ignore + FakeIP:补的那句排在 FakeIP 提示后", bpR("direct", []string{bpCN}), bpOpts(pc, ig, `"fakeIp":true`), bpWant(true, `["geoip-cn"]`, "", true, bpJoin(bpN2, bpN3))},
		{"ignore + DNS 禁用:排在 DNS 提示后", bpR("direct", []string{bpCN}), bpOpts(pc, ig, `"fakeIp":true,"dnsMode":"off"`), bpWant(true, `["geoip-cn"]`, "", false, bpJoin(bpN1, bpN3))},
		{"ignore + 待核对:补的那句在待核对前", bpR("direct", []string{bpUS, bpJP}), bpOpts(pc, ig),
			bpWant(false, "", `[{"policy":"日本","sets":["geoip-jp"],"against":[{"name":"海外","geoip":["geoip-us"],"cidrs":[]}]}]`, false, bpJoin(bpN3, bpPend("日本", "海外")))},
		{"ignore 之后站点集按域名照挡,原因里带上补的那句", bpR("direct", []string{bpCN, bpNF}), bpOpts(pc, ig), bpClosed(false, bpJoin(bpPolDom("流媒体"), bpN3))},
		{"ignore 之后兜底走代理照挡", bpR("美国", []string{bpCN}), bpOpts(pc, ig), bpClosed(false, bpJoin(bpFB("其他"), bpN3))},
		{"ignore 之后没有可用集合", bpR("direct", nil), bpOpts(pc, ig), bpClosed(false, bpJoin(bpNone, bpN3))},
		{"ignore 但没有终端走代理:不补", bpR("direct", []string{bpCN}), bpOpts(ig), bpWant(true, `["geoip-cn"]`, "", false, "")},
	})
	// 改名后:终端出口写老记号 direct 不再是内置直连,算走代理;写新名字才算直连
	renamed := `"members":["直连","拒绝","美国"],"builtin":{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`
	clsCheck(t, "改名后终端写老记号算走代理", bpRun(t, bpR("direct", []string{bpCN}), `{`+renamed+`,"clientRoutes":[{"outbound":"direct"}]}`), bpClosed(false, bpCli))
	clsCheck(t, "改名后终端写新名字算直连", bpRun(t, bpR("direct", []string{bpCN}), `{`+renamed+`,"clientRoutes":[{"outbound":"直连"}]}`), bpWant(true, `["geoip-cn"]`, "", false, ""))
	// 终端出口的判定:出口是真值且不是内置直连才算
	for _, c := range []struct {
		name, routes string
		proxy        bool
	}{
		{"出口是节点", `[{"ip":"192.168.1.5","outbound":"美国"}]`, true},
		{"出口是 direct", `[{"ip":"192.168.1.5","outbound":"direct"}]`, false},
		{"出口是 block 也算", `[{"outbound":"block"}]`, true},
		{"出口为空串 / 缺失 / null / false / 0 不算", `[{"outbound":""},{"ip":"1"},{"outbound":null},{"outbound":false},{"outbound":0}]`, false},
		{"出口是别的真值(数字)算", `[{"outbound":123}]`, true},
		{"非对象的项不算", `[5,null,"美国",[],true]`, false},
		{"一个直连一个节点:有一个算就算", `[{"outbound":"direct"},{"outbound":"美国"}]`, true},
		{"停用的终端分流照算", `[{"outbound":"美国","enabled":false}]`, true},
		{"空数组", `[]`, false},
		{"不是数组:忽略", `{"outbound":"美国"}`, false},
		{"null", `null`, false},
	} {
		got := bpRun(t, bpR("direct", []string{bpCN}), bpOpts(`"clientRoutes":`+c.routes))
		if c.proxy {
			clsCheck(t, c.name, got, bpClosed(false, bpCli))
		} else {
			clsCheck(t, c.name, got, bpWant(true, `["geoip-cn"]`, "", false, ""))
		}
	}
}

func TestBypassPlanOptionParsing(t *testing.T) {
	cnNF := bpR("direct", []string{bpNF, bpCN})
	okCN := bpWant(true, `["geoip-cn"]`, "", false, "")
	// 选项不是对象 / 缺省:全部取缺省(members [direct],所以默认为 美国 的站点集也落到直连)
	for _, o := range []string{``, `null`, `5`, `[]`, `"x"`, `true`, `{}`} {
		clsCheck(t, "选项 "+o, bpRun(t, cnNF, o), okCN)
	}
	// members:传了才改变;null / 非数组等于没传;空数组与非字符串成员都不会落到直连
	for _, c := range []struct{ name, members, want string }{
		{"null", `null`, okCN},
		{"非数组字符串", `"x"`, okCN},
		{"非数组数字", `5`, okCN},
		{"非数组对象", `{}`, okCN},
		{"有节点", `["direct","美国"]`, bpClosed(false, bpPolDom("流媒体"))},
		{"空数组:默认落不到任何成员,算走代理", `[]`, bpClosed(false, bpPolDom("流媒体"))},
		{"非字符串成员占位成空串", `[5,"direct"]`, bpClosed(false, bpPolDom("流媒体"))},
	} {
		clsCheck(t, "members="+c.name, bpRun(t, cnNF, `{"members":`+c.members+`}`), c.want)
	}
	// builtin:null 等于没传;传了对象就按它(缺键的名字是空串,不补默认)
	renamed := `"builtin":{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`
	cn := bpR("direct", []string{bpCN})
	clsCheck(t, "改名后 default=direct 指新直连", bpRun(t, cn, `{"members":["直连","拒绝","美国"],`+renamed+`}`), okCN)
	clsCheck(t, "没传 builtin 时成员叫「直连」不是内置直连", bpRun(t, cn, `{"members":["直连","美国"]}`), bpClosed(false, bpFB("其他")))
	clsCheck(t, "builtin=null 同没传", bpRun(t, cn, `{"members":["direct","美国"],"builtin":null}`), okCN)
	clsCheck(t, "builtin 不是对象按缺省", bpRun(t, cn, `{"members":["direct"],"builtin":"x"}`), okCN)
	clsCheck(t, "builtin 缺键:直连名是空串,谁都不是直连", bpRun(t, cn, `{"members":["direct"],"builtin":{}}`), bpClosed(false, bpFB("其他")))
	// selections:null 等于没传;非对象当空表
	for _, s := range []string{`null`, `"x"`, `5`, `[]`} {
		clsCheck(t, "selections="+s, bpRun(t, bpR("direct", []string{bpCN, bpUS}), bpOpts(`"selections":`+s)), okCN)
	}
	clsCheck(t, "selections 是对象:按选择", bpRun(t, bpR("direct", []string{bpCN, bpUS}), bpOpts(`"selections":{"海外":"direct"}`)), bpWant(true, `["geoip-cn","geoip-us"]`, "", false, ""))
	// clientRoutes 不是数组:忽略;见 TestBypassPlanProxyClients
	// fakeIp 按 JS 真假
	for _, v := range []string{`true`, `1`, `"yes"`, `[]`, `{}`, `"0"`, `"false"`} {
		clsCheck(t, "fakeIp="+v, bpRun(t, cn, `{"fakeIp":`+v+`}`), bpWant(true, `["geoip-cn"]`, "", true, bpN2))
	}
	for _, v := range []string{`false`, `0`, `""`, `null`} {
		clsCheck(t, "fakeIp="+v, bpRun(t, cn, `{"fakeIp":`+v+`}`), okCN)
	}
	// dnsMode:只有恰好是字符串 off 才算 DNS 禁用
	clsCheck(t, "dnsMode=off", bpRun(t, cn, `{"fakeIp":true,"dnsMode":"off"}`), bpWant(true, `["geoip-cn"]`, "", false, bpN1))
	for _, v := range []string{`"OFF"`, `" off"`, `""`, `null`, `0`, `false`, `["off"]`, `{}`, `"dnsmasq"`, `"hijack"`} {
		clsCheck(t, "dnsMode="+v, bpRun(t, cn, `{"fakeIp":true,"dnsMode":`+v+`}`), bpWant(true, `["geoip-cn"]`, "", true, bpN2))
	}
	// enabled:缺省为开;传了按 JS 真假,null 也算关
	for _, v := range []string{`true`, `1`, `"yes"`, `[]`, `{}`, `"0"`} {
		clsCheck(t, "enabled="+v, bpRun(t, cn, `{"enabled":`+v+`}`), okCN)
	}
	for _, v := range []string{`false`, `0`, `""`, `null`} {
		clsCheck(t, "enabled="+v, bpRun(t, cn, `{"enabled":`+v+`}`), bpClosed(false, bpOff))
	}
	// ignoreProxyClients 按 JS 真假
	pc := `"clientRoutes":[{"outbound":"美国"}]`
	for _, v := range []string{`true`, `1`, `"x"`, `[]`, `{}`, `"0"`} {
		clsCheck(t, "ignoreProxyClients="+v, bpRun(t, cn, bpOpts(pc, `"ignoreProxyClients":`+v)), bpWant(true, `["geoip-cn"]`, "", false, bpN3))
	}
	for _, v := range []string{`false`, `0`, `""`, `null`} {
		clsCheck(t, "ignoreProxyClients="+v, bpRun(t, cn, bpOpts(pc, `"ignoreProxyClients":`+v)), bpClosed(false, bpCli))
	}
}

func TestBypassPlanJSONShape(t *testing.T) {
	clsNoGeo()
	mk := func(routing, opts string) string {
		b, err := json.Marshal(bypassPlan(mustJSON(t, routing), mustJSON(t, opts)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got, want := mk(bpR("direct", nil), `{}`),
		`{"enabled":false,"sets":[],"pending":[],"fakeIp":false,"reason":"`+bpNone+`"}`; got != want {
		t.Errorf("没有集合:\n有: %s\n要: %s", got, want)
	}
	if got, want := mk(bpR("direct", []string{bpCN}), `{}`),
		`{"enabled":true,"sets":["geoip-cn"],"pending":[],"fakeIp":false,"reason":""}`; got != want {
		t.Errorf("基础:\n有: %s\n要: %s", got, want)
	}
	// 在前规则:name / geoip / cidrs 三键固定,lists 没有就不带、有就排在最后
	if got, want := mk(bpR("direct", []string{bpUS, bpJP}), bpOpts()),
		`{"enabled":false,"sets":[],"pending":[{"policy":"日本","sets":["geoip-jp"],"against":[{"name":"海外","geoip":["geoip-us"],"cidrs":[]}]}],"fakeIp":false,"reason":"`+bpPend("日本", "海外")+`"}`; got != want {
		t.Errorf("待核对:\n有: %s\n要: %s", got, want)
	}
	if got, want := mk(bpR("direct", []string{bpJP}, bpC(bpCR("ruleUrl", bpURLA, "美国"), bpCR("ipCidr", "10.0.0.0/8", "美国"))), bpOpts(`"fakeIp":true`)),
		`{"enabled":false,"sets":[],"pending":[{"policy":"日本","sets":["geoip-jp"],"against":[{"name":"前置自定义分流","geoip":[],"cidrs":["10.0.0.0/8"],"lists":["`+bpTagA+`"]}]}],"fakeIp":true,"reason":"`+bpJoin(bpN2, bpPend("日本", "前置自定义分流"))+`"}`; got != want {
		t.Errorf("带 lists:\n有: %s\n要: %s", got, want)
	}
}

func TestBypassPlanDoesNotMutateInput(t *testing.T) {
	routing := mustJSON(t, bpR("direct", []string{bpCN, bpUS, bpJP}, bpC(bpCR("ipCidr", "10.0.0.0/8", "美国"))))
	opts := mustJSON(t, bpOpts(`"selections":{"海外":"日本"},"clientRoutes":[{"outbound":"direct"}],"fakeIp":true`))
	rb, _ := json.Marshal(routing)
	ob, _ := json.Marshal(opts)
	first := roundTrip(t, bypassPlan(routing, opts))
	second := roundTrip(t, bypassPlan(routing, opts))
	if !reflect.DeepEqual(first, second) {
		t.Error("两次调用结果不同")
	}
	ra, _ := json.Marshal(routing)
	oa, _ := json.Marshal(opts)
	if string(rb) != string(ra) || string(ob) != string(oa) {
		t.Error("入参被改写")
	}
	// 待核对项里的在前规则是拷贝:改一份不影响另一份
	p := bypassPlan(mustJSON(t, bpR("direct", []string{bpUS, bpJP, bpKR})), mustJSON(t, bpOpts()))
	if len(p.Pending) != 2 {
		t.Fatalf("应有两个待核对项: %+v", p)
	}
	p.Pending[0].Against[0].Geoip[0] = "改过"
	if p.Pending[1].Against[0].Geoip[0] != "geoip-us" {
		t.Error("待核对项共用了在前规则的底层数组")
	}
}

// 走完整的协议入口:缺省参数。
func TestBypassPlanHandlerArgs(t *testing.T) {
	reg := protocol.NewRegistry()
	Register(reg)
	run := func(args string) any {
		t.Helper()
		return call(t, reg, []byte(`{"protocol":1,"method":"bypassPlan","args":`+args+`}`))
	}
	r := bpR("direct", []string{bpCN})
	clsCheck(t, "只给 routing", run(`[`+r+`]`), bpWant(true, `["geoip-cn"]`, "", false, ""))
	clsCheck(t, "选项 null", run(`[`+r+`,null]`), bpWant(true, `["geoip-cn"]`, "", false, ""))
	clsCheck(t, "选项给全", run(`[`+r+`,{"fakeIp":true}]`), bpWant(true, `["geoip-cn"]`, "", true, bpN2))
	clsCheck(t, "没有参数:空档案", run(`[]`), bpClosed(false, bpNone))
	clsCheck(t, "空档案加 FakeIP", run(`[{},{"fakeIp":true}]`), bpClosed(true, bpJoin(bpNone, bpN2)))
	clsCheck(t, "总开关关", run(`[`+r+`,{"enabled":false}]`), bpClosed(false, bpOff))
}
