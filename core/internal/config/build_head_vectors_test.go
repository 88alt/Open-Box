package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// 自写测试向量:build.go 的 BuildDetailed 第一段(从入参缺省到节点出站 / endpoint、出站重名检查,即源文件顺序里
// 「订阅和节点站点直连」「链式代理并表」「节点组」「站点集 selector」「节点出站 / endpoint」「重名检查」六块)。
// 输入自己拟,期望值按下面的规则人工推算,JSON 逐字节比(map 的键序是字母序):
//   - 结果恒为 {config, dnsRuleOwners, directHosts, failover, flipFlags, nativeBypass}(后两个是自研多带的);没有规则归属时 dnsRuleOwners 是 [] 不是 null,没有故障转移组时 failover 是 [];
//   - profile 里没有 dns 对象时报 JS 同款的 "Cannot read properties of undefined (reading 'split')";
//   - 出站顺序:用户组出站(直连、各组、拒绝)→ 生效的站点集 selector(按站点集顺序)→ 兜底 selector → 节点(含并进来的链式节点)
//     → dnsmasq 模式才有的 dnsmasq 回送出站;wireguard 不在 outbounds 里而在 endpoints 里;
//   - 站点集 / 兜底 selector 的成员是对外可见的组 tag(直连 / 拒绝停用就不在),默认值取站点集自己的 default(direct / block 记号换成
//     当前名字),不在成员里时退回第一个成员,proxy 记号取第一个非内置成员;
//   - 订阅和节点站点直连:profile.directForNodes 只有严格等于 false 才关;域名与 IP 取自节点(不含链式候选)与订阅,小写去空白,
//     directHostCidrs 里的字符串追加在后面、跨来源去重保序,与 Cloudflare 公布网段重叠的整条丢掉;
//     默认写成两份本地规则集(obnode-direct / obnode-direct-ip,路径 = rulesetDir + / + tag + .json),inlineDirectHosts 为真值时
//     改成路由规则里直接写地址,取 "aligned" 时空表用占位(obnode-direct.invalid / 192.0.2.255/32)补上;
//   - 链式候选(chainNodes)先核对再并进节点表,排在原节点后面,带 detour;核对不过的整个不进配置;
//   - 出站(含 endpoints)重名时整份配置报错,文字固定。

func bhRun(t *testing.T, argsJSON string) (map[string]any, error) {
	t.Helper()
	var args any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		t.Fatalf("bad json: %v\n%s", err, argsJSON)
	}
	out, err := BuildDetailed(args)
	if err != nil {
		return nil, err
	}
	res := toAny(out).(map[string]any)
	dumpConfig(t, res)
	return res, nil
}

// 设了 OPENBOX_DUMP_DIR 时,把每份产出的配置落盘,供 testdata/tools/sbcheck.py 用官方 sing-box 跑 check(平时不设,不写盘)。
func dumpConfig(t *testing.T, res map[string]any) {
	dir := os.Getenv("OPENBOX_DUMP_DIR")
	if dir == "" {
		return
	}
	dumpSeq++
	b, err := json.Marshal(res["config"])
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%s/%05d.json", dir, dumpSeq)
	if err := os.WriteFile(name, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

var dumpSeq int

func bhMust(t *testing.T, argsJSON string) map[string]any {
	t.Helper()
	res, err := bhRun(t, argsJSON)
	if err != nil {
		t.Fatalf("意外报错 %v\n%s", err, argsJSON)
	}
	return res
}

func bhJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func bhCfg(res map[string]any) map[string]any { return res["config"].(map[string]any) }

func bhOutbounds(t *testing.T, res map[string]any) string {
	return bhJSON(t, bhCfg(res)["outbounds"])
}

// 一份能通过的最小入参:profile 里要有 dns 对象
func bhArgs(extra string, nodes ...string) string {
	n := strings.Join(nodes, ",")
	if extra != "" {
		extra = "," + extra
	}
	return `{"nodes":[` + n + `],"profile":{"dns":{}}` + extra + `}`
}

func sk(tag, server string, port int) string {
	return fmt.Sprintf(`{"tag":%q,"type":"socks","server":%q,"server_port":%d}`, tag, server, port)
}

func skOut(tag, server string, port int) string {
	return fmt.Sprintf(`{"server":%q,"server_port":%d,"tag":%q,"type":"socks"}`, server, port, tag)
}

const (
	bhDirect = `{"tag":"直连","type":"direct"}`
	bhBlock  = `{"tag":"拒绝","type":"block"}`
	// 兜底 selector 缺省:名字「其他」,成员只有直连与拒绝,默认直连
	bhFallbackSel = `{"default":"直连","outbounds":["直连","拒绝"],"tag":"其他","type":"selector"}`
)

func bhOuts(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

func TestBuildHeadShape(t *testing.T) {
	res := bhMust(t, bhArgs(""))
	if len(res) != 6 {
		t.Errorf("结果键数 %d: %v", len(res), res)
	}
	for _, k := range []string{"config", "dnsRuleOwners", "directHosts", "failover", "flipFlags", "nativeBypass"} {
		if _, ok := res[k]; !ok {
			t.Errorf("缺 %s", k)
		}
	}
	if got := bhJSON(t, res["dnsRuleOwners"]); got != `[]` {
		t.Errorf("dnsRuleOwners=%s", got)
	}
	if got := bhJSON(t, res["failover"]); got != `[]` {
		t.Errorf("failover=%s", got)
	}
	// 没有站点集:只有兜底的开关,兜底默认直连 → 关
	if got := bhJSON(t, res["flipFlags"]); got != `{"obflip-fallback":false}` {
		t.Errorf("flipFlags=%s", got)
	}
	if got := bhOutbounds(t, res); got != bhOuts(bhDirect, bhBlock, bhFallbackSel) {
		t.Errorf("没有节点时的出站: %s", got)
	}
	if _, has := bhCfg(res)["endpoints"]; has {
		t.Errorf("没有 wireguard 时不该有 endpoints 键")
	}
	// profile 没有 dns:报 JS 同款错误
	for _, a := range []string{`{"nodes":[],"profile":{}}`, `{"nodes":[]}`, `{"profile":{}}`, `{}`} {
		if _, err := bhRun(t, a); err == nil || err.Error() != "Cannot read properties of undefined (reading 'split')" {
			t.Errorf("%s 该报缺 dns 的错,得到 %v", a, err)
		}
	}
	// nodes / subscriptions / userGroups 不是数组:当没有
	for _, bad := range []string{`null`, `{"a":1}`, `"x"`, `0`, `true`} {
		res := bhMust(t, `{"nodes":`+bad+`,"subscriptions":`+bad+`,"userGroups":`+bad+`,"profile":{"dns":{}}}`)
		if got := bhOutbounds(t, res); got != bhOuts(bhDirect, bhBlock, bhFallbackSel) {
			t.Errorf("nodes 等为 %s 时的出站: %s", bad, got)
		}
	}
}

func TestBuildHeadDirectHosts(t *testing.T) {
	a, b := sk("A", "s1.example.com", 1080), sk("B", "203.0.113.5", 1081)
	dh := func(res map[string]any) string { return bhJSON(t, res["directHosts"]) }
	// 域名与 IP 分开,IP 写成 /32
	if got := dh(bhMust(t, bhArgs("", a, b))); got != `{"cidrs":["203.0.113.5/32"],"domains":["s1.example.com"]}` {
		t.Errorf("基本: %s", got)
	}
	// 大小写与首尾空白去掉,节点顺序保留,同域名只留一次
	c := sk("C", "  S2.Example.COM ", 1)
	d := sk("D", "s1.EXAMPLE.com", 2)
	if got := dh(bhMust(t, bhArgs("", a, c, d))); got != `{"cidrs":[],"domains":["s1.example.com","s2.example.com"]}` {
		t.Errorf("规范化: %s", got)
	}
	// directHostCidrs:字符串追加在节点网段之后、去重保序,非字符串忽略,非数组当没有
	for extra, want := range map[string]string{
		`"directHostCidrs":["198.51.100.0/24",5,null,"203.0.113.5/32","198.51.100.0/24",{"a":1}]`: `{"cidrs":["203.0.113.5/32","198.51.100.0/24"],"domains":["s1.example.com"]}`,
		`"directHostCidrs":"198.51.100.0/24"`:                                                     `{"cidrs":["203.0.113.5/32"],"domains":["s1.example.com"]}`,
		`"directHostCidrs":null`:                                                                  `{"cidrs":["203.0.113.5/32"],"domains":["s1.example.com"]}`,
		`"directHostCidrs":[]`:                                                                    `{"cidrs":["203.0.113.5/32"],"domains":["s1.example.com"]}`,
	} {
		if got := dh(bhMust(t, bhArgs(extra, a, b))); got != want {
			t.Errorf("%s\n得到 %s\n期望 %s", extra, got, want)
		}
	}
	// 与 Cloudflare 网段重叠的整条丢:额外网段、节点 IP 都一样;只是相邻的不算
	cf := sk("CF", "104.16.1.1", 3)
	if got := dh(bhMust(t, bhArgs(`"directHostCidrs":["104.17.0.0/16","104.0.0.0/8","198.51.100.0/24","173.245.48.0/20","104.15.255.255/32"]`, a, cf))); got != `{"cidrs":["198.51.100.0/24","104.15.255.255/32"],"domains":["s1.example.com"]}` {
		t.Errorf("共用 CDN 段: %s", got)
	}
	// 只有严格等于 false 才关:null / 0 / "" / 缺失都照常算
	for _, v := range []string{`null`, `0`, `""`, `"false"`, `true`} {
		a1 := `{"nodes":[` + a + `],"profile":{"dns":{},"directForNodes":` + v + `}}`
		if got := dh(bhMust(t, a1)); got != `{"cidrs":[],"domains":["s1.example.com"]}` {
			t.Errorf("directForNodes=%s: %s", v, got)
		}
	}
	off := bhMust(t, `{"nodes":[`+a+`],"profile":{"dns":{},"directForNodes":false},"directHostCidrs":["198.51.100.0/24"]}`)
	if off["directHosts"] != nil {
		t.Errorf("关闭时 directHosts 应为 null: %v", off["directHosts"])
	}
	// 关闭后配置里没有节点直连的规则集
	if strings.Contains(bhJSON(t, bhCfg(off)["route"]), "obnode-direct") {
		t.Errorf("关闭后 route 里不该有 obnode-direct")
	}
}

func TestBuildHeadDirectHostSets(t *testing.T) {
	a := sk("A", "s1.example.com", 1080)
	ruleSets := func(res map[string]any) string {
		var out []any
		for _, e := range bhCfg(res)["route"].(map[string]any)["rule_set"].([]any) {
			tag := e.(map[string]any)["tag"].(string)
			if strings.HasPrefix(tag, "obnode-direct") {
				out = append(out, e)
			}
		}
		return bhJSON(t, out)
	}
	setsAt := func(dir string) string {
		return `[{"format":"source","path":"` + dir + `/obnode-direct.json","tag":"obnode-direct","type":"local"},{"format":"source","path":"` + dir + `/obnode-direct-ip.json","tag":"obnode-direct-ip","type":"local"}]`
	}
	// 缺省(inlineDirectHosts 不写 / 假值):两份本地规则集,路径默认目录
	for _, extra := range []string{``, `"inlineDirectHosts":false`, `"inlineDirectHosts":0`, `"inlineDirectHosts":""`, `"inlineDirectHosts":null`} {
		if got := ruleSets(bhMust(t, bhArgs(extra, a))); got != setsAt("/opt/open-box/data/rulesets") {
			t.Errorf("%q: %s", extra, got)
		}
	}
	// rulesetDir:自定义、空串、null 当空串(不拼出 "null")、数字按 JS 转字符串
	for extra, dir := range map[string]string{`"rulesetDir":"/r"`: "/r", `"rulesetDir":""`: "", `"rulesetDir":null`: "", `"rulesetDir":12`: "12"} {
		if got := ruleSets(bhMust(t, bhArgs(extra, a))); got != setsAt(dir) {
			t.Errorf("%s: %s", extra, got)
		}
	}
	// 内联(真值):不写规则集,路由规则里直接带域名
	for _, extra := range []string{`"inlineDirectHosts":true`, `"inlineDirectHosts":1`, `"inlineDirectHosts":"x"`, `"inlineDirectHosts":"aligned"`} {
		res := bhMust(t, bhArgs(extra, a))
		if got := ruleSets(res); got != `null` {
			t.Errorf("%s 不该有规则集: %s", extra, got)
		}
		if r := bhJSON(t, bhCfg(res)["route"].(map[string]any)["rules"]); !strings.Contains(r, `"s1.example.com"`) {
			t.Errorf("%s 路由规则里该直接带域名: %s", extra, r)
		}
	}
	// 内联且没有任何地址:取 aligned 才补占位,取别的真值不补
	empty := bhArgs(`"inlineDirectHosts":"aligned"`)
	r := bhJSON(t, bhCfg(bhMust(t, empty))["route"].(map[string]any)["rules"])
	if !strings.Contains(r, `"obnode-direct.invalid"`) || !strings.Contains(r, `"192.0.2.255/32"`) {
		t.Errorf("aligned 且空表应带占位: %s", r)
	}
	r = bhJSON(t, bhCfg(bhMust(t, bhArgs(`"inlineDirectHosts":true`)))["route"].(map[string]any)["rules"])
	if strings.Contains(r, "null") {
		t.Errorf("内联且空表时规则里不该出现 null: %s", r)
	}
	if strings.Contains(r, "invalid") || strings.Contains(r, "192.0.2.255") {
		t.Errorf("true 且空表不该带占位: %s", r)
	}
	// aligned 只域名为空时只补域名占位,网段为空时只补网段占位
	r = bhJSON(t, bhCfg(bhMust(t, bhArgs(`"inlineDirectHosts":"aligned"`, sk("B", "203.0.113.5", 1))))["route"].(map[string]any)["rules"])
	if !strings.Contains(r, `"obnode-direct.invalid"`) || strings.Contains(r, "192.0.2.255") || !strings.Contains(r, `"203.0.113.5/32"`) {
		t.Errorf("只有 IP 时只补域名占位: %s", r)
	}
	r = bhJSON(t, bhCfg(bhMust(t, bhArgs(`"inlineDirectHosts":"aligned"`, a)))["route"].(map[string]any)["rules"])
	if strings.Contains(r, "invalid") || !strings.Contains(r, `"192.0.2.255/32"`) || !strings.Contains(r, `"s1.example.com"`) {
		t.Errorf("只有域名时只补网段占位: %s", r)
	}
}

func TestBuildHeadChainMerge(t *testing.T) {
	a := sk("A", "s1.example.com", 1080)
	// 链式候选并在节点后面,带 detour;直连清单只算原节点(链式候选的服务器不进)
	c := `{"tag":"C","type":"socks","server":"c.example.com","server_port":9,"detour":"A"}`
	res := bhMust(t, bhArgs(`"chainNodes":[`+c+`]`, a))
	if got := bhOutbounds(t, res); got != bhOuts(bhDirect, bhBlock, bhFallbackSel, skOut("A", "s1.example.com", 1080),
		`{"detour":"A","server":"c.example.com","server_port":9,"tag":"C","type":"socks"}`) {
		t.Errorf("并表: %s", got)
	}
	if got := bhJSON(t, res["directHosts"]); got != `{"cidrs":[],"domains":["s1.example.com"]}` {
		t.Errorf("链式候选的服务器不进站点直连: %s", got)
	}
	// 多个候选保持输入顺序;核对不过的(上游不存在 / 成环 / 重名)整个不进
	c2 := `{"tag":"C2","type":"socks","server":"c2.example.com","server_port":9,"detour":"C"}`
	bad := `{"tag":"Bad","type":"socks","server":"b.example.com","server_port":9,"detour":"nope"}`
	dup := `{"tag":"A","type":"socks","server":"x.example.com","server_port":9,"detour":"A"}`
	res = bhMust(t, bhArgs(`"chainNodes":[`+c2+`,`+bad+`,`+dup+`,`+c+`]`, a))
	tags := bhTags(t, res)
	if got := strings.Join(tags, ","); got != "直连,拒绝,其他,A,C2,C" {
		// C2 的上游 C 在它后面,但都是候选所以合格,顺序按输入
		t.Errorf("候选顺序与核对: %s", got)
	}
	// chainNodes 不是数组 / 缺失:没有候选
	for _, bad := range []string{`null`, `{"a":1}`, `"x"`, `1`} {
		res := bhMust(t, bhArgs(`"chainNodes":`+bad, a))
		if got := strings.Join(bhTags(t, res), ","); got != "直连,拒绝,其他,A" {
			t.Errorf("chainNodes=%s: %s", bad, got)
		}
	}
	// wireguard 候选进 endpoints 不进 outbounds
	wg := `{"tag":"W","type":"wireguard","server":"w.example.com","server_port":51820,"detour":"A","fields":{"peer_public_key":"pub","private_key":"priv","local_address":["10.0.0.2"]}}`
	res = bhMust(t, bhArgs(`"chainNodes":[`+wg+`]`, a))
	cfg := bhCfg(res)
	eps := cfg["endpoints"].([]any)
	if len(eps) != 1 || eps[0].(map[string]any)["tag"] != "W" || eps[0].(map[string]any)["type"] != "wireguard" {
		t.Errorf("endpoints: %s", bhJSON(t, cfg["endpoints"]))
	}
	for _, tag := range bhTags(t, res) {
		if tag == "W" {
			t.Errorf("wireguard 不该在 outbounds 里")
		}
	}
	// 链式候选的 wireguard 不能当别的候选的上游(核对不过)
	c3 := `{"tag":"C3","type":"socks","server":"c3.example.com","server_port":9,"detour":"W"}`
	res = bhMust(t, bhArgs(`"chainNodes":[`+wg+`,`+c3+`]`, a))
	if got := strings.Join(bhTags(t, res), ","); got != "直连,拒绝,其他,A" {
		t.Errorf("wireguard 上游: %s", got)
	}
}

func bhTags(t *testing.T, res map[string]any) []string {
	t.Helper()
	var tags []string
	for _, o := range bhCfg(res)["outbounds"].([]any) {
		tags = append(tags, o.(map[string]any)["tag"].(string))
	}
	return tags
}

func TestBuildHeadGroupsAndPolicies(t *testing.T) {
	a, b := sk("A", "s1.example.com", 1080), sk("B", "s2.example.com", 1081)
	sel := func(tag string, def string, members ...string) string {
		ms, _ := json.Marshal(members)
		return fmt.Sprintf(`{"default":%q,"outbounds":%s,"tag":%q,"type":"selector"}`, def, ms, tag)
	}
	// 用户组出站在最前,随后是站点集 selector,兜底 selector,最后才是节点;站点集 / 兜底 selector 的成员是对外可见的组 tag
	ug := `"userGroups":[{"id":"s","name":"S","members":["A","B"]}]`
	pol1 := `{"id":"p1","name":"流媒体","default":"S","domainSuffix":["netflix.com"]}`
	pol2 := `{"id":"p2","name":"下载","default":"direct","domainSuffix":["dl.example"]}`
	res := bhMust(t, bhArgs(ug+`,"profile2":0`, a, b)) // 未知键不影响
	if got := bhOutbounds(t, res); got != bhOuts(bhDirect, `{"outbounds":["A","B"],"tag":"S","type":"selector"}`, bhBlock,
		sel("其他", "直连", "直连", "S", "拒绝"), skOut("A", "s1.example.com", 1080), skOut("B", "s2.example.com", 1081)) {
		t.Errorf("用户组: %s", got)
	}
	// 站点集:按站点集顺序,各自 default;兜底改名与默认
	args := `{"nodes":[` + a + `,` + b + `],` + ug + `,"profile":{"dns":{},"routing":{"policies":[` + pol1 + `,` + pol2 + `],"fallbackName":"漏网","fallbackDefault":"S"}}}`
	res = bhMust(t, args)
	if got := bhOutbounds(t, res); got != bhOuts(bhDirect, `{"outbounds":["A","B"],"tag":"S","type":"selector"}`, bhBlock,
		sel("流媒体", "S", "直连", "S", "拒绝"), sel("下载", "直连", "直连", "S", "拒绝"), sel("漏网", "S", "直连", "S", "拒绝"),
		skOut("A", "s1.example.com", 1080), skOut("B", "s2.example.com", 1081)) {
		t.Errorf("站点集: %s", got)
	}
	// default 不在成员里:退回第一个成员;proxy 记号取第一个非内置成员;block 记号换成当前名字
	for def, want := range map[string]string{"nope": "直连", "proxy": "S", "block": "拒绝", "": "直连"} {
		pol := fmt.Sprintf(`{"id":"p1","name":"P","default":%q,"domainSuffix":["x.example"]}`, def)
		res := bhMust(t, `{"nodes":[`+a+`],`+ug+`,"profile":{"dns":{},"routing":{"policies":[`+pol+`]}}}`)
		for _, o := range bhCfg(res)["outbounds"].([]any) {
			m := o.(map[string]any)
			if m["tag"] == "P" && m["default"] != want {
				t.Errorf("default=%q 该是 %s,得到 %v", def, want, m["default"])
			}
		}
	}
	// 没有条件 / 停用 / 叫「其他」的站点集不生成 selector
	pols := `{"id":"e","name":"空"},{"id":"d","name":"停","enabled":false,"domainSuffix":["a.example"]},{"id":"f","name":"其他","domainSuffix":["b.example"]}`
	res = bhMust(t, `{"nodes":[],"profile":{"dns":{},"routing":{"policies":[`+pols+`]}}}`)
	if got := bhOutbounds(t, res); got != bhOuts(bhDirect, bhBlock, bhFallbackSel) {
		t.Errorf("无效站点集不生成: %s", got)
	}
	// 内置直连 / 拒绝改名与停用:成员与默认跟着变
	ugb := `"userGroups":[{"id":"builtin-direct","name":"DIRECT"},{"id":"builtin-block","name":"BLOCK","enabled":false},{"id":"s","name":"S","members":["A"]}]`
	res = bhMust(t, `{"nodes":[`+a+`],`+ugb+`,"profile":{"dns":{},"routing":{"policies":[{"id":"p","name":"P","default":"direct","domainSuffix":["x.example"]}]}}}`)
	if got := bhOutbounds(t, res); got != bhOuts(`{"tag":"DIRECT","type":"direct"}`, `{"outbounds":["A"],"tag":"S","type":"selector"}`,
		sel("P", "DIRECT", "DIRECT", "S"), sel("其他", "DIRECT", "DIRECT", "S"), skOut("A", "s1.example.com", 1080)) {
		t.Errorf("内置改名 / 停用: %s", got)
	}
	// 直连被停用:直连出站照写,但站点集 / 兜底 selector 的成员里没有它,默认退到第一个成员
	ugd := `"userGroups":[{"id":"builtin-direct","name":"直连","enabled":false},{"id":"s","name":"S","members":["A"]}]`
	res = bhMust(t, `{"nodes":[`+a+`],`+ugd+`,"profile":{"dns":{},"routing":{"policies":[{"id":"p","name":"P","default":"direct","domainSuffix":["x.example"]}]}}}`)
	if got := bhOutbounds(t, res); got != bhOuts(bhDirect, `{"outbounds":["A"],"tag":"S","type":"selector"}`, bhBlock,
		sel("P", "S", "S", "拒绝"), sel("其他", "S", "S", "拒绝"), skOut("A", "s1.example.com", 1080)) {
		t.Errorf("直连停用: %s", got)
	}
	// userGroups 假值当 []
	for _, g := range []string{`null`, `false`, `0`, `""`} {
		res := bhMust(t, `{"nodes":[],"userGroups":`+g+`,"profile":{"dns":{}}}`)
		if got := bhOutbounds(t, res); got != bhOuts(bhDirect, bhBlock, bhFallbackSel) {
			t.Errorf("userGroups=%s: %s", g, got)
		}
	}
	// 因环被丢的组不在出站里,也不是站点集的成员
	cyc := `"userGroups":[{"id":"x","name":"X","members":["Y"]},{"id":"y","name":"Y","members":["X"]}]`
	res = bhMust(t, bhArgs(cyc, a))
	if got := strings.Join(bhTags(t, res), ","); got != "直连,拒绝,其他,A" {
		t.Errorf("环: %s", got)
	}
	// 档案的测试地址与可接受状态码传给组(urltest 没写自己的):url / expected_status
	ut := `"userGroups":[{"id":"u","name":"U","type":"urltest","members":["A"]}]`
	res = bhMust(t, `{"nodes":[`+a+`],`+ut+`,"profile":{"dns":{},"testUrl":"http://t.example/x","testExpectedStatus":"204"}}`)
	for _, o := range bhCfg(res)["outbounds"].([]any) {
		m := o.(map[string]any)
		if m["tag"] == "U" && (m["url"] != "http://t.example/x" || m["expected_status"] != "204") {
			t.Errorf("urltest 没拿到档案的设置: %v", m)
		}
	}
	// 故障转移组:failover 状态原样带出
	fo := `"userGroups":[{"id":"f","name":"F","type":"failover","lanes":[{"id":"l","members":["A"]}]}]`
	res = bhMust(t, bhArgs(fo, a))
	fl := res["failover"].([]any)
	if len(fl) != 1 || fl[0].(map[string]any)["id"] != "f" || fl[0].(map[string]any)["tag"] != "F" {
		t.Errorf("failover: %s", bhJSON(t, res["failover"]))
	}
	// 组生成出错(负载均衡超 64 个)原样往外报
	var tags, members []string
	for i := 0; i < 65; i++ {
		tags = append(tags, sk(fmt.Sprintf("N%02d", i), "n.example.com", 1000+i))
		members = append(members, fmt.Sprintf("%q", fmt.Sprintf("N%02d", i)))
	}
	lb := `"userGroups":[{"id":"l","name":"L","type":"loadbalance","members":[` + strings.Join(members, ",") + `]}]`
	if _, err := bhRun(t, bhArgs(lb, tags...)); err == nil || err.Error() != "load balance supports at most 64 nodes" {
		t.Errorf("负载均衡超限: %v", err)
	}
}

func TestBuildHeadNodeOutbounds(t *testing.T) {
	a := sk("A", "s1.example.com", 1080)
	// 节点出站 / endpoint:wireguard 只在 endpoints,顺序保持
	wg := `{"tag":"W","type":"wireguard","server":"w.example.com","server_port":51820,"fields":{"peer_public_key":"pub","private_key":"priv","local_address":["10.0.0.2"]}}`
	wg2 := strings.Replace(wg, `"W"`, `"W2"`, 1)
	res := bhMust(t, bhArgs("", wg, a, wg2))
	if got := strings.Join(bhTags(t, res), ","); got != "直连,拒绝,其他,A" {
		t.Errorf("outbounds: %s", got)
	}
	eps := bhCfg(res)["endpoints"].([]any)
	if len(eps) != 2 || eps[0].(map[string]any)["tag"] != "W" || eps[1].(map[string]any)["tag"] != "W2" {
		t.Errorf("endpoints 顺序: %s", bhJSON(t, eps))
	}
	// 节点出站错(不认识的类型 / 缺字段)原样往外报
	if _, err := bhRun(t, bhArgs("", `{"tag":"X","type":"nope","server":"s","server_port":1}`)); err == nil {
		t.Errorf("不认识的节点类型应报错")
	}
	// 节点的 detour(字符串且非空)原样带到出站
	d := `{"tag":"D","type":"socks","server":"d.example.com","server_port":1,"detour":"A"}`
	res = bhMust(t, bhArgs("", a, d))
	if got := bhOutbounds(t, res); !strings.Contains(got, `{"detour":"A","server":"d.example.com","server_port":1,"tag":"D","type":"socks"}`) {
		t.Errorf("detour: %s", got)
	}
	// dnsmasq 模式:末尾多一个 dnsmasq 回送出站
	res = bhMust(t, `{"nodes":[`+a+`],"profile":{"dns":{"mode":"dnsmasq"}}}`)
	tags := bhTags(t, res)
	if tags[len(tags)-1] != "dnsmasq" {
		t.Errorf("dnsmasq 模式末尾应是 dnsmasq 出站: %v", tags)
	}
	last := bhCfg(res)["outbounds"].([]any)
	if bhJSON(t, last[len(last)-1]) != `{"bind_interface":"lo","tag":"dnsmasq","type":"direct"}` {
		t.Errorf("dnsmasq 出站: %s", bhJSON(t, last[len(last)-1]))
	}
}

func TestBuildHeadNodeResolver(t *testing.T) {
	// 订阅配了节点专用 DoH(IP 地址,不需要 bootstrap):服务器追加在 dns.servers 末尾,
	// 属于该订阅且服务器是域名的节点出站挂 domain_resolver;IP 服务器、别的订阅、没订阅的节点不挂
	sub := `"subscriptions":[{"id":"s1","nodeDns":{"url":"https://1.1.1.1/dns-query"}}]`
	withSub := func(n, id string) string {
		return strings.Replace(n, `"type":"socks"`, `"type":"socks","subscriptionId":"`+id+`"`, 1)
	}
	a := withSub(sk("A", "s1.example.com", 1), "s1")
	b := withSub(sk("B", "203.0.113.5", 2), "s1")
	c := withSub(sk("C", "s3.example.com", 3), "other")
	d := sk("D", "s4.example.com", 4)
	res := bhMust(t, bhArgs(sub, a, b, c, d))
	outs := bhCfg(res)["outbounds"].([]any)
	byTag := map[string]map[string]any{}
	for _, o := range outs {
		m := o.(map[string]any)
		byTag[m["tag"].(string)] = m
	}
	if byTag["A"]["domain_resolver"] != "dns-sub-s1" {
		t.Errorf("A 该挂解析器: %v", byTag["A"])
	}
	for _, tag := range []string{"B", "C", "D"} {
		if _, has := byTag[tag]["domain_resolver"]; has {
			t.Errorf("%s 不该挂解析器: %v", tag, byTag[tag])
		}
	}
	srvs := bhCfg(res)["dns"].(map[string]any)["servers"].([]any)
	if got := bhJSON(t, srvs[len(srvs)-1]); got != `{"server":"1.1.1.1","tag":"dns-sub-s1","type":"https"}` {
		t.Errorf("追加的 DoH 服务器: %s", got)
	}
	if got := bhJSON(t, srvs[:len(srvs)-1]); strings.Contains(got, "dns-sub-s1") {
		t.Errorf("DoH 服务器只该出现在末尾: %s", got)
	}
	// 订阅停用(布尔 false)或没配 nodeDns:没有服务器也不挂解析器
	for _, subs := range []string{
		`"subscriptions":[{"id":"s1","enabled":false,"nodeDns":{"url":"https://1.1.1.1/dns-query"}}]`,
		`"subscriptions":[{"id":"s1"}]`,
		`"subscriptions":[{"id":"s1","nodeDns":{"url":"http://1.1.1.1/dns-query"}}]`,
	} {
		res := bhMust(t, bhArgs(subs, a))
		if strings.Contains(bhJSON(t, bhCfg(res)), "dns-sub-s1") {
			t.Errorf("%s 不该产生节点解析器", subs)
		}
	}
	// 同一订阅两个域名节点都挂,服务器只追加一份
	a2 := withSub(sk("A2", "s5.example.com", 5), "s1")
	res = bhMust(t, bhArgs(sub, a, a2))
	if n := strings.Count(bhJSON(t, bhCfg(res)["dns"].(map[string]any)["servers"]), `"tag":"dns-sub-s1"`); n != 1 {
		t.Errorf("服务器该只有一份,得到 %d", n)
	}
}

func TestBuildHeadDuplicateTag(t *testing.T) {
	a := sk("A", "s1.example.com", 1080)
	want := func(tag string) string {
		return "出站名称重复:「" + tag + "」——节点组、站点集、节点、内置直连/拒绝之间不能同名,请改名后再启动"
	}
	cases := []struct {
		name, args, dup string
	}{
		{"两个节点同名", bhArgs("", a, sk("A", "s2.example.com", 2)), "A"},
		{"节点与组同名", bhArgs(`"userGroups":[{"id":"s","name":"A","members":["A"]}]`, a), "A"},
		{"节点叫直连", bhArgs("", sk("直连", "s2.example.com", 2)), "直连"},
		{"节点叫拒绝", bhArgs("", sk("拒绝", "s2.example.com", 2)), "拒绝"},
		{"节点与站点集同名", `{"nodes":[` + sk("P", "s.example.com", 1) + `],"profile":{"dns":{},"routing":{"policies":[{"id":"p","name":"P","domainSuffix":["x.example"]}]}}}`, "P"},
		{"节点叫兜底名", bhArgs("", sk("其他", "s2.example.com", 2)), "其他"},
		{"节点与 wireguard 同名(endpoint 也算)", bhArgs("", a, strings.Replace(`{"tag":"A","type":"wireguard","server":"w.example.com","server_port":1,"fields":{"peer_public_key":"p","private_key":"k","local_address":["10.0.0.2"]}}`, `"A"`, `"A"`, 1)), "A"},
		{"节点叫 dnsmasq 且开着 dnsmasq 模式", `{"nodes":[` + sk("dnsmasq", "s2.example.com", 2) + `],"profile":{"dns":{"mode":"dnsmasq"}}}`, "dnsmasq"},
		{"链式候选之间同名", bhArgs(`"chainNodes":[{"tag":"C","type":"socks","server":"c.example.com","server_port":1,"detour":"A"},{"tag":"C","type":"socks","server":"c2.example.com","server_port":2,"detour":"A"}]`, a), "C"},
	}
	for _, c := range cases {
		_, err := bhRun(t, c.args)
		if err == nil || err.Error() != want(c.dup) {
			t.Errorf("%s: 该报 %q,得到 %v", c.name, want(c.dup), err)
		}
	}
	// 不重名:dnsmasq 这个名字在非 dnsmasq 模式下是普通节点名
	if _, err := bhRun(t, `{"nodes":[`+sk("dnsmasq", "s2.example.com", 2)+`],"profile":{"dns":{}}}`); err != nil {
		t.Errorf("非 dnsmasq 模式的 dnsmasq 节点不该报重名: %v", err)
	}
	// 节点与停用的组同名不算(停用的组不生成出站)
	if _, err := bhRun(t, bhArgs(`"userGroups":[{"id":"o","name":"A","enabled":false}]`, a)); err != nil {
		t.Errorf("停用的组不该报重名: %v", err)
	}
}

func TestBuildHeadNoWriteThrough(t *testing.T) {
	args := `{"nodes":[` + sk("A", "s1.example.com", 1080) + `],"userGroups":[{"id":"s","name":"S","members":["A"]}],"chainNodes":[{"tag":"C","type":"socks","server":"c.example.com","server_port":1,"detour":"A"}],"directHostCidrs":["198.51.100.0/24"],"profile":{"dns":{},"routing":{"policies":[{"id":"p","name":"P","default":"S","domainSuffix":["x.example"]}]}}}`
	var in any
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		t.Fatal(err)
	}
	before := bhJSON(t, in)
	out1, err := BuildDetailed(in)
	if err != nil {
		t.Fatal(err)
	}
	if bhJSON(t, in) != before {
		t.Errorf("入参被改写")
	}
	out2, err := BuildDetailed(in)
	if err != nil {
		t.Fatal(err)
	}
	if bhJSON(t, out1) != bhJSON(t, out2) {
		t.Errorf("两次调用结果不同")
	}
}
