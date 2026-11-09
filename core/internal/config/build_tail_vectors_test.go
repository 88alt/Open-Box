package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 自写测试向量:build.go 的 BuildDetailed 后半(源文件顺序里 tun 入站的 MAC 名单、入口原生旁路与 route_address_set、
// 入站里的 dns-in-direct、终端分流的接线、装配与 FakeIP)。输入自己拟,期望值按下面的规则人工推算:
//   - tun 的 MAC 名单只在 auto_redirect 开着时写:exclude_mac_address = 启用的「不进内核」终端里按 MAC 认的那些(规范成小写冒号写法、
//     跨规则去重保首次出现的顺序、非法 MAC 丢掉);include_mac_address = 启用的「只让这些进内核」里按 MAC 认的那些,
//     且名单里没有按 IP 认的终端(有 IP 的话 MAC 名单就不写);老档案没有 match 字段、bypass / admit 为真且 macs 非空算 MAC;
//   - dns-in-direct 入站:auto_redirect 开着、dns.split 不是假、DNS 模式是 hijack 或 dnsmasq、白名单里有按 IP 认的终端时才有,
//     监听 0.0.0.0(profile.ipv6 真值时 ::),端口 7855;排在 dns-in(以及容器的 dns-in-lan)后面、共享网络入站前面;
//     hijack 模式下路由里的 hijack-dns 规则 inbound 带上它;
//   - 入口原生旁路:auto_redirect 开着时只看 geoip- 候选:每个候选一份 obflip-byp-<tag> 进 route_exclude_address_set,
//     route_address_set = 三份固定的 obflip-need-(all / fakeip / cidr)加每个候选一份 obflip-need-<tag>,
//     这些本地二进制规则集(路径 = flip 目录 + / + tag + .srs)按「旁路 → 必须进内核」的顺序追加进 route.rule_set;没有候选就没有 route_exclude_address_set;
//     auto_redirect 没开时:nativeBypass 是对象就用它(enabled 真值才算开,sets 只收字符串,trimmed 的值按真假;整份的在前、裁剪过的
//     换成 obflip-byp-<tag> 在后),否则按 bypassPlan 算;关着或没有集合就没有 route_exclude_address_set;
//   - 装配:顶层键 dns / experimental / inbounds / log / outbounds / route(有 wireguard 才加 endpoints);log 为 warn;clash_api 监听 127.0.0.1:9095,
//     档案里写了 clashApiSecret(哪怕是空串)才带 secret;cache_file 开着,路径取 cacheFilePath(缺省 /opt/open-box/data/cache.db);
//     FakeIP 开着时路径后面接 .fakeip-198_19_0_0_16_fc00___18 并且 store_fakeip 为 true,同时 selector 的 default 改成档案里记着的选择
//     (只对 selector、选择在成员里的才改);
//   - 终端分流的接线:出口认的是出站和 endpoint 的全部 tag;按 IP 的终端用 clientBindings 扩展出 IPv6 /128;有共享网络入站时按来源认终端的规则
//     (路由和 DNS 两处)带「入站不是共享网络」的条件。

const tlA = `{"tag":"A","type":"socks","server":"s1.example.com","server_port":1}`

func tl(t *testing.T, profile, top string) map[string]any {
	t.Helper()
	return bhMust(t, `{"nodes":[`+tlA+`],"userGroups":[{"id":"s","name":"S","members":["A"]}]`+top+`,"profile":`+profile+`}`)
}

func tlInbound(t *testing.T, res map[string]any, tag string) map[string]any {
	t.Helper()
	for _, i := range bhCfg(res)["inbounds"].([]any) {
		if m := i.(map[string]any); m["tag"] == tag {
			return m
		}
	}
	return nil
}

func tlInboundTags(res map[string]any) []string {
	var out []string
	for _, i := range bhCfg(res)["inbounds"].([]any) {
		out = append(out, i.(map[string]any)["tag"].(string))
	}
	return out
}

func tlStrs(v any) []string {
	if v == nil {
		return nil
	}
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

func tlRuleSets(res map[string]any, prefix string) []string {
	var out []string
	route := bhCfg(res)["route"].(map[string]any)
	l, _ := route["rule_set"].([]any)
	for _, e := range l {
		m := e.(map[string]any)
		if tag, _ := m["tag"].(string); strings.HasPrefix(tag, prefix) {
			b, _ := json.Marshal(m)
			out = append(out, string(b))
		}
	}
	return out
}

func TestBuildTailTunMacLists(t *testing.T) {
	const ar = `"tun":{"autoRedirect":true}`
	mac := func(extra string) string {
		return `{"id":"x","name":"X","match":"mac",` + extra + `}`
	}
	cases := []struct {
		name, tun, routes string
		exclude, include  []string
	}{
		{"黑名单 MAC 规范化去重丢非法", ar, `[` + mac(`"bypass":true,"macs":["AA-BB-CC-DD-EE-01","aa:bb:cc:dd:ee:01","aa:bb:cc:dd:ee:02","zz"]`) + `]`, []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"}, nil},
		{"两条规则跨规则去重保首次顺序", ar, `[` + mac(`"bypass":true,"macs":["aa:bb:cc:dd:ee:02","aa:bb:cc:dd:ee:01"]`) + `,` + mac(`"bypass":true,"macs":["aa:bb:cc:dd:ee:01","aa:bb:cc:dd:ee:03"]`) + `]`, []string{"aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:03"}, nil},
		{"停用与按 IP 的黑名单不进 MAC 名单", ar, `[` + mac(`"bypass":true,"enabled":false,"macs":["aa:bb:cc:dd:ee:01"]`) + `,{"id":"i","name":"I","match":"ip","bypass":true,"sources":["192.168.1.9"]}]`, nil, nil},
		{"老档案无 match:bypass 加 macs 算 MAC", ar, `[{"id":"o","name":"O","bypass":true,"macs":["aa:bb:cc:dd:ee:09"]}]`, []string{"aa:bb:cc:dd:ee:09"}, nil},
		{"没开 auto_redirect 一概不写", `"tun":{"autoRedirect":false}`, `[` + mac(`"bypass":true,"macs":["aa:bb:cc:dd:ee:01"]`) + `,` + mac(`"admit":true,"macs":["aa:bb:cc:dd:ee:02"]`) + `]`, nil, nil},
		{"DNS 模式 off 时 auto_redirect 不成立", ar + `,"dns":{"mode":"off"}`, `[` + mac(`"bypass":true,"macs":["aa:bb:cc:dd:ee:01"]`) + `]`, nil, nil},
		{"白名单只有 MAC", ar, `[` + mac(`"admit":true,"macs":["AA-BB-CC-DD-EE-02","aa:bb:cc:dd:ee:02"]`) + `]`, nil, []string{"aa:bb:cc:dd:ee:02"}},
		{"白名单有按 IP 的终端就不写 MAC 名单", ar, `[` + mac(`"admit":true,"macs":["aa:bb:cc:dd:ee:02"]`) + `,{"id":"i","name":"I","match":"ip","admit":true,"sources":["192.168.1.9"]}]`, nil, nil},
		{"白名单里的 IP 终端停用后 MAC 名单照写", ar, `[` + mac(`"admit":true,"macs":["aa:bb:cc:dd:ee:02"]`) + `,{"id":"i","name":"I","match":"ip","admit":true,"enabled":false,"sources":["192.168.1.9"]}]`, nil, []string{"aa:bb:cc:dd:ee:02"}},
		{"普通按 IP 分流不影响 MAC 白名单", ar, `[` + mac(`"admit":true,"macs":["aa:bb:cc:dd:ee:02"]`) + `,{"id":"i","name":"I","match":"ip","outbound":"S","sources":["192.168.1.9"]}]`, nil, []string{"aa:bb:cc:dd:ee:02"}},
		{"白名单 MAC 全非法就没有名单", ar, `[` + mac(`"admit":true,"macs":["nope","12:34"]`) + `]`, nil, nil},
		{"黑白名单同时在", ar, `[` + mac(`"bypass":true,"macs":["aa:bb:cc:dd:ee:01"]`) + `,` + mac(`"admit":true,"macs":["aa:bb:cc:dd:ee:02"]`) + `]`, []string{"aa:bb:cc:dd:ee:01"}, []string{"aa:bb:cc:dd:ee:02"}},
	}
	for _, c := range cases {
		profile := `{"dns":{}` + `,` + c.tun + `,"clientRoutes":` + c.routes + `}`
		if strings.Contains(c.tun, `"dns"`) {
			profile = `{` + c.tun + `,"clientRoutes":` + c.routes + `}`
		}
		m := tunOf(t, tl(t, profile, ""))
		if got := tlStrs(m["exclude_mac_address"]); !sameList(got, c.exclude) || (c.exclude == nil) != (m["exclude_mac_address"] == nil) {
			t.Errorf("%s: exclude_mac_address 得到 %v 期望 %v", c.name, got, c.exclude)
		}
		if got := tlStrs(m["include_mac_address"]); !sameList(got, c.include) || (c.include == nil) != (m["include_mac_address"] == nil) {
			t.Errorf("%s: include_mac_address 得到 %v 期望 %v", c.name, got, c.include)
		}
	}
}

func TestBuildTailDNSDirectInbound(t *testing.T) {
	admitIP := `{"id":"a","name":"A","match":"ip","admit":true,"sources":["192.168.1.10"]}`
	prof := func(extra string) string {
		return `{"dns":{"split":true` + extra + `},"tun":{"autoRedirect":true},"clientRoutes":[` + admitIP + `]}`
	}
	const direct = `{"listen":"0.0.0.0","listen_port":7855,"tag":"dns-in-direct","type":"direct"}`
	res := tl(t, prof(""), "")
	if got := strings.Join(tlInboundTags(res), ","); got != "tun-in,panel-in,dns-in,dns-in-direct" {
		t.Errorf("入站顺序: %s", got)
	}
	if got := bhJSON(t, tlInbound(t, res, "dns-in-direct")); got != direct {
		t.Errorf("dns-in-direct: %s", got)
	}
	// ipv6 真值时监听 ::
	res = tl(t, `{"dns":{"split":true},"ipv6":true,"tun":{"autoRedirect":true},"clientRoutes":[`+admitIP+`]}`, "")
	if got := bhJSON(t, tlInbound(t, res, "dns-in-direct")); got != strings.Replace(direct, "0.0.0.0", "::", 1) {
		t.Errorf("ipv6 时 dns-in-direct: %s", got)
	}
	// dnsmasq 模式也有
	if got := strings.Join(tlInboundTags(tl(t, prof(`,"mode":"dnsmasq"`), "")), ","); got != "tun-in,panel-in,dns-in,dns-in-direct" {
		t.Errorf("dnsmasq 模式入站: %s", got)
	}
	// 容器部署:dns-in-lan 在 dns-in 后面、dns-in-direct 前面;共享网络入站排最后
	res = tl(t, `{"dns":{"split":true},"tun":{"autoRedirect":true},"clientRoutes":[`+admitIP+`],"servers":[{"id":"a1","protocol":"shadowsocks","port":8388,"method":"aes-128-gcm","password":"p"}]}`, `,"platform":"container"`)
	if got := strings.Join(tlInboundTags(res), ","); got != "tun-in,panel-in,dns-in,dns-in-lan,dns-in-direct,share-a1" {
		t.Errorf("容器入站顺序: %s", got)
	}
	// 不满足条件时没有
	off := map[string]string{
		"没开 auto_redirect": `{"dns":{"split":true},"clientRoutes":[` + admitIP + `]}`,
		"DNS 模式 off":       prof(`,"mode":"off"`),
		"DNS 模式未知":         prof(`,"mode":"foo"`),
		"dns.split 为假":     `{"dns":{"split":false},"tun":{"autoRedirect":true},"clientRoutes":[` + admitIP + `]}`,
		"白名单只有 MAC":        `{"dns":{"split":true},"tun":{"autoRedirect":true},"clientRoutes":[{"id":"m","name":"M","match":"mac","admit":true,"macs":["aa:bb:cc:dd:ee:01"]}]}`,
		"白名单的 IP 终端停用":     `{"dns":{"split":true},"tun":{"autoRedirect":true},"clientRoutes":[{"id":"a","name":"A","match":"ip","admit":true,"enabled":false,"sources":["192.168.1.10"]}]}`,
		"白名单来源全非法":         `{"dns":{"split":true},"tun":{"autoRedirect":true},"clientRoutes":[{"id":"a","name":"A","match":"ip","admit":true,"sources":["nope"]}]}`,
		"只有普通按 IP 分流没有白名单": `{"dns":{"split":true},"tun":{"autoRedirect":true},"clientRoutes":[{"id":"a","name":"A","match":"ip","outbound":"S","sources":["192.168.1.10"]}]}`,
	}
	for name, p := range off {
		if in := tlInbound(t, tl(t, p, ""), "dns-in-direct"); in != nil {
			t.Errorf("%s: 不该有 dns-in-direct", name)
		}
	}
	// 路由里 hijack 模式的 hijack-dns 规则 inbound 带上它;没有名单外终端时只有 dns-in
	hijackInbound := func(res map[string]any) string {
		for _, r := range bhCfg(res)["route"].(map[string]any)["rules"].([]any) {
			m := r.(map[string]any)
			if m["action"] == "hijack-dns" && m["inbound"] != nil {
				return bhJSON(t, m["inbound"])
			}
		}
		return ""
	}
	if got := hijackInbound(tl(t, prof(""), "")); got != `["dns-in","dns-in-direct"]` {
		t.Errorf("hijack 的 inbound: %s", got)
	}
	if got := hijackInbound(tl(t, `{"dns":{"split":true},"tun":{"autoRedirect":true}}`, "")); got != `["dns-in"]` {
		t.Errorf("没有名单外终端时 hijack 的 inbound: %s", got)
	}
}

func TestBuildTailNativeBypass(t *testing.T) {
	cn := `{"id":"p1","name":"国内","default":"direct","rulesets":["geoip-cn"]}`
	us := `{"id":"p2","name":"海外","default":"direct","rulesets":["geoip-us","geoip-cn"]}`
	routing := func(pols ...string) string { return `"routing":{"policies":[` + strings.Join(pols, ",") + `]}` }
	srs := func(dir, tag string) string {
		return fmt.Sprintf(`{"format":"binary","path":"%s/%s.srs","tag":"%s","type":"local"}`, dir, tag, tag)
	}
	const fixedNeed = `"obflip-need-all","obflip-need-fakeip","obflip-need-cidr"`
	needSets := func(dir string, tags ...string) []string {
		var out []string
		for _, t := range tags {
			out = append(out, srs(dir, t))
		}
		return out
	}
	eq := func(name string, got, want []string) {
		t.Helper()
		if !sameList(got, want) {
			t.Errorf("%s:\n得到 %v\n期望 %v", name, got, want)
		}
	}

	// auto_redirect 开着、没有 geoip 候选:只有三份固定的必须进内核集合,没有 route_exclude_address_set
	res := tl(t, `{"dns":{},"tun":{"autoRedirect":true}}`, "")
	m := tunOf(t, res)
	if got := bhJSON(t, m["route_address_set"]); got != `[`+fixedNeed+`]` {
		t.Errorf("无候选 route_address_set: %s", got)
	}
	if _, has := m["route_exclude_address_set"]; has {
		t.Errorf("无候选不该有 route_exclude_address_set")
	}
	eq("无候选的规则集", tlRuleSets(res, "obflip-byp-"), nil)
	eq("无候选的必须进内核集合", tlRuleSets(res, "obflip-need-"), needSets("/opt/open-box/data/flip", "obflip-need-all", "obflip-need-fakeip", "obflip-need-cidr"))

	// 候选按站点集顺序去重:geoip-cn、geoip-us
	res = tl(t, `{"dns":{},"tun":{"autoRedirect":true},`+routing(cn, us)+`}`, "")
	m = tunOf(t, res)
	if got := bhJSON(t, m["route_exclude_address_set"]); got != `["obflip-byp-geoip-cn","obflip-byp-geoip-us"]` {
		t.Errorf("候选 route_exclude_address_set: %s", got)
	}
	if got := bhJSON(t, m["route_address_set"]); got != `[`+fixedNeed+`,"obflip-need-geoip-cn","obflip-need-geoip-us"]` {
		t.Errorf("候选 route_address_set: %s", got)
	}
	eq("候选的旁路规则集", tlRuleSets(res, "obflip-byp-"), needSets("/opt/open-box/data/flip", "obflip-byp-geoip-cn", "obflip-byp-geoip-us"))
	eq("候选的必须进内核集合", tlRuleSets(res, "obflip-need-"), needSets("/opt/open-box/data/flip", "obflip-need-all", "obflip-need-fakeip", "obflip-need-cidr", "obflip-need-geoip-cn", "obflip-need-geoip-us"))

	// flip 目录跟 rulesetDir:去掉最后一段加 /flip;顶层 flipDir 真值时盖掉它
	res = tl(t, `{"dns":{},"tun":{"autoRedirect":true},`+routing(cn)+`}`, `,"rulesetDir":"/data/rs"`)
	eq("rulesetDir 决定 flip 目录", tlRuleSets(res, "obflip-byp-"), needSets("/data/flip", "obflip-byp-geoip-cn"))
	res = tl(t, `{"dns":{},"tun":{"autoRedirect":true},`+routing(cn)+`}`, `,"rulesetDir":"/data/rs","flipDir":"/x/f"`)
	eq("flipDir 盖掉", tlRuleSets(res, "obflip-byp-"), needSets("/x/f", "obflip-byp-geoip-cn"))
	res = tl(t, `{"dns":{},"tun":{"autoRedirect":true},`+routing(cn)+`}`, `,"rulesetDir":"/data/rs","flipDir":""`)
	eq("flipDir 空串不盖", tlRuleSets(res, "obflip-byp-"), needSets("/data/flip", "obflip-byp-geoip-cn"))

	// auto_redirect 开着时 nativeBypass 不起作用
	res = tl(t, `{"dns":{},"tun":{"autoRedirect":true},`+routing(cn)+`}`, `,"nativeBypass":{"enabled":true,"sets":["geoip-private"]}`)
	if got := bhJSON(t, tunOf(t, res)["route_exclude_address_set"]); got != `["obflip-byp-geoip-cn"]` {
		t.Errorf("auto_redirect 下 nativeBypass 无效: %s", got)
	}

	// 没开 auto_redirect:按 bypassPlan 算(默认兜底走直连、站点集走直连 → geoip-cn 整份进 route_exclude_address_set)
	res = tl(t, `{"dns":{},`+routing(cn)+`}`, "")
	m = tunOf(t, res)
	if got := bhJSON(t, m["route_exclude_address_set"]); got != `["geoip-cn"]` {
		t.Errorf("计划算出的旁路: %s", got)
	}
	if _, has := m["route_address_set"]; has {
		t.Errorf("没开 auto_redirect 不该有 route_address_set")
	}
	eq("计划算出的旁路不加旁路规则集", tlRuleSets(res, "obflip-byp-"), nil)
	eq("计划算出的旁路不加必须进内核集合", tlRuleSets(res, "obflip-need-"), nil)
	// directBypass 为 false / 兜底走代理 / 没有站点集 → 没有
	for name, p := range map[string]string{
		"directBypass 关": `{"dns":{},"directBypass":false,` + routing(cn) + `}`,
		"兜底走代理":          `{"dns":{},"routing":{"policies":[` + cn + `],"fallbackDefault":"S"}}`,
		"没有站点集":          `{"dns":{}}`,
	} {
		if _, has := tunOf(t, tl(t, p, ""))["route_exclude_address_set"]; has {
			t.Errorf("%s: 不该有 route_exclude_address_set", name)
		}
	}

	// nativeBypass 对象(没开 auto_redirect):整份在前、裁剪过的换成 obflip-byp- 在后;trimmed 取真假;非字符串的 set 丢掉
	nb := func(s string) string { return `,"nativeBypass":` + s }
	// 覆盖用的集合要在路由里有对应的规则集(真实场景里它们来自站点集),这样产出才能过官方 sing-box check
	all3 := `{"dns":{},` + routing(`{"id":"p9","name":"全","default":"direct","rulesets":["geoip-cn","geoip-private","geoip-us"]}`) + `}`
	res = tl(t, all3, nb(`{"enabled":true,"sets":["geoip-cn","geoip-private",7,"geoip-us"],"trimmed":{"geoip-private":true,"geoip-us":1,"geoip-cn":0}}`))
	if got := bhJSON(t, tunOf(t, res)["route_exclude_address_set"]); got != `["geoip-cn","obflip-byp-geoip-private","obflip-byp-geoip-us"]` {
		t.Errorf("nativeBypass 对象: %s", got)
	}
	eq("裁剪过的追加规则集", tlRuleSets(res, "obflip-byp-"), needSets("/opt/open-box/data/flip", "obflip-byp-geoip-private", "obflip-byp-geoip-us"))
	// 没写 trimmed 时都是整份
	res = tl(t, all3, nb(`{"enabled":1,"sets":["geoip-cn"]}`))
	if got := bhJSON(t, tunOf(t, res)["route_exclude_address_set"]); got != `["geoip-cn"]` {
		t.Errorf("无 trimmed: %s", got)
	}
	// 没开 / sets 空或不是数组 → 没有;对象存在时不再回头用计划(即使计划会开)
	for name, v := range map[string]string{
		"enabled 假":   `{"enabled":false,"sets":["geoip-cn"]}`,
		"没写 enabled":  `{"sets":["geoip-cn"]}`,
		"sets 空":      `{"enabled":true,"sets":[]}`,
		"sets 不是数组":   `{"enabled":true,"sets":"geoip-cn"}`,
		"sets 全是非字符串": `{"enabled":true,"sets":[1,null]}`,
	} {
		res := tl(t, `{"dns":{},`+routing(cn)+`}`, nb(v))
		if _, has := tunOf(t, res)["route_exclude_address_set"]; has {
			t.Errorf("nativeBypass %s: 不该有 route_exclude_address_set", name)
		}
	}
	// nativeBypass 是假值或字符串时不算对象,回到计划
	for _, v := range []string{`null`, `0`, `""`, `false`, `"yes"`, `5`} {
		res := tl(t, `{"dns":{},`+routing(cn)+`}`, nb(v))
		if got := bhJSON(t, tunOf(t, res)["route_exclude_address_set"]); got != `["geoip-cn"]` {
			t.Errorf("nativeBypass=%s 该回到计划: %s", v, got)
		}
	}
}

func TestBuildTailAssembly(t *testing.T) {
	// 缺省:六个顶层键、log、experimental
	res := tl(t, `{"dns":{}}`, "")
	cfg := bhCfg(res)
	keys := []string{}
	for k := range cfg {
		keys = append(keys, k)
	}
	if got := bhJSON(t, cfg["log"]); got != `{"level":"warn"}` {
		t.Errorf("log: %s", got)
	}
	if len(keys) != 6 {
		t.Errorf("顶层键: %v", keys)
	}
	if got := bhJSON(t, cfg["experimental"]); got != `{"cache_file":{"enabled":true,"path":"/opt/open-box/data/cache.db","store_fakeip":false},"clash_api":{"external_controller":"127.0.0.1:9095"}}` {
		t.Errorf("experimental: %s", got)
	}
	// clashApiSecret:写了就带(空串也带),没写不带
	for secret, want := range map[string]string{`"s3"`: `"s3"`, `""`: `""`} {
		got := bhJSON(t, bhCfg(tl(t, `{"dns":{},"clashApiSecret":`+secret+`}`, ""))["experimental"].(map[string]any)["clash_api"])
		if got != `{"external_controller":"127.0.0.1:9095","secret":`+want+`}` {
			t.Errorf("secret=%s: %s", secret, got)
		}
	}
	// 缓存路径取 cacheFilePath;空串 / 数字也按字符串用
	for in, want := range map[string]string{`"/tmp/c.db"`: "/tmp/c.db", `""`: "", `7`: "7"} {
		got := bhCfg(tl(t, `{"dns":{}}`, `,"cacheFilePath":`+in))["experimental"].(map[string]any)["cache_file"].(map[string]any)["path"]
		if got != want {
			t.Errorf("cacheFilePath=%s: %v", in, got)
		}
	}
	// FakeIP 开着(dns.split 不是假且 fakeIpForProxy 严格为 true):路径接池后缀、store_fakeip 为 true
	const fakeProfile = `{"dns":{"fakeIpForProxy":true}}`
	cf := bhCfg(tl(t, fakeProfile, `,"cacheFilePath":"/tmp/c.db"`))["experimental"].(map[string]any)["cache_file"]
	if got := bhJSON(t, cf); got != `{"enabled":true,"path":"/tmp/c.db.fakeip-198_19_0_0_16_fc00___18","store_fakeip":true}` {
		t.Errorf("FakeIP 缓存: %s", got)
	}
	// 没满足条件的写法都不开
	for _, p := range []string{`{"dns":{"fakeIpForProxy":1}}`, `{"dns":{"fakeIpForProxy":true,"split":false}}`, `{"dns":{"fakeIpForProxy":"true"}}`} {
		cf := bhCfg(tl(t, p, ""))["experimental"].(map[string]any)["cache_file"]
		if got := bhJSON(t, cf); got != `{"enabled":true,"path":"/opt/open-box/data/cache.db","store_fakeip":false}` {
			t.Errorf("%s 不该开 FakeIP: %s", p, got)
		}
	}
	// wireguard 才有 endpoints
	wg := `{"tag":"W","type":"wireguard","server":"w.example.com","server_port":51820,"fields":{"peer_public_key":"pub","private_key":"priv","local_address":["10.0.0.2"]}}`
	res = bhMust(t, `{"nodes":[`+tlA+`,`+wg+`],"profile":{"dns":{}}}`)
	if _, has := bhCfg(res)["endpoints"]; !has {
		t.Errorf("有 wireguard 该有 endpoints")
	}
}

func TestBuildTailFakeIPSelectorDefault(t *testing.T) {
	b := `{"tag":"B","type":"socks","server":"s2.example.com","server_port":2}`
	pol := `{"id":"p1","name":"流媒体","default":"S","domainSuffix":["netflix.com"]}`
	run := func(profileDNS, selections string) map[string]string {
		args := `{"nodes":[` + tlA + `,` + b + `],"userGroups":[{"id":"s","name":"S","members":["A","B"]},{"id":"u","name":"U","type":"urltest","members":["A","B"]}],"selections":` + selections +
			`,"profile":{"dns":` + profileDNS + `,"routing":{"policies":[` + pol + `]}}}`
		out := map[string]string{}
		for _, o := range bhCfg(bhMust(t, args))["outbounds"].([]any) {
			m := o.(map[string]any)
			if m["type"] == "selector" {
				d, _ := m["default"].(string) // 用户组 selector 没有 default 键,按空串记
				out[m["tag"].(string)] = d
			}
		}
		return out
	}
	// 不开 FakeIP:选择不改 default
	got := run(`{}`, `{"S":"B","流媒体":"直连"}`)
	if got["S"] != "" || got["流媒体"] != "S" || got["其他"] != "直连" {
		t.Errorf("没开 FakeIP: %v", got)
	}
	// 开 FakeIP:选择在成员里的 selector 改 default;不在成员里的、没记录的不动
	got = run(`{"fakeIpForProxy":true}`, `{"S":"B","流媒体":"直连","其他":"不存在","U":"B"}`)
	if got["S"] != "B" || got["流媒体"] != "直连" || got["其他"] != "直连" {
		t.Errorf("开 FakeIP: %v", got)
	}
	// 选择是非字符串、不等于任何成员 → 不动;urltest 不是 selector 不在 got 里
	got = run(`{"fakeIpForProxy":true}`, `{"S":1,"流媒体":null,"其他":["直连"]}`)
	if got["S"] != "" || got["流媒体"] != "S" || got["其他"] != "直连" {
		t.Errorf("非字符串选择: %v", got)
	}
	if _, has := got["U"]; has {
		t.Errorf("urltest 不该出现在 selector 里")
	}
	// selections 不是对象当没有
	got = run(`{"fakeIpForProxy":true}`, `[1]`)
	if got["S"] != "" {
		t.Errorf("selections 是数组: %v", got)
	}
}

func TestBuildTailClientRoutesWiring(t *testing.T) {
	wg := `{"tag":"W","type":"wireguard","server":"w.example.com","server_port":51820,"fields":{"peer_public_key":"pub","private_key":"priv","local_address":["10.0.0.2"]}}`
	routeRules := func(res map[string]any) string { return bhJSON(t, bhCfg(res)["route"].(map[string]any)["rules"]) }
	build := func(profile, top string) map[string]any {
		return bhMust(t, `{"nodes":[`+tlA+`,`+wg+`],"userGroups":[{"id":"s","name":"S","members":["A"]}]`+top+`,"profile":`+profile+`}`)
	}
	rt := func(out, src string) string {
		return `{"dns":{},"clientRoutes":[{"id":"r","name":"R","match":"ip","outbound":` + out + `,"sources":[` + src + `]}]}`
	}
	// 出口认出站和 endpoint 的 tag:S(组)、A(节点)、W(wireguard endpoint)都认;不存在的出口整条不写
	for _, out := range []string{`"S"`, `"A"`, `"W"`, `"直连"`, `"拒绝"`} {
		res := build(rt(out, `"192.168.1.0/24"`), "")
		if !strings.Contains(routeRules(res), `"source_ip_cidr":["192.168.1.0/24"]`) {
			t.Errorf("出口 %s 该有终端规则: %s", out, routeRules(res))
		}
	}
	res := build(rt(`"不存在"`, `"192.168.1.0/24"`), "")
	if strings.Contains(routeRules(res), "source_ip_cidr") {
		t.Errorf("未知出口不该写规则: %s", routeRules(res))
	}
	// 终端 IPv6 绑定:IPv4 来源后面跟对应的 /128
	res = build(rt(`"S"`, `"192.168.1.10","192.168.1.11"`), `,"clientBindings":{"192.168.1.10":["fd00::10"],"192.168.1.11/32":"fd00::11"}`)
	if !strings.Contains(routeRules(res), `"source_ip_cidr":["192.168.1.10/32","fd00::10/128","192.168.1.11/32","fd00::11/128"]`) {
		t.Errorf("绑定扩展: %s", routeRules(res))
	}
	// 「不进内核」的出口取当前内置直连名(改名后跟着改),而不是写死 direct
	renamed := `{"dns":{},"clientRoutes":[{"id":"b","name":"B","match":"ip","bypass":true,"sources":["192.168.1.20"]}]}`
	res = bhMust(t, `{"nodes":[`+tlA+`],"userGroups":[{"id":"builtin-direct","name":"出去"}],"profile":`+renamed+`}`)
	if !strings.Contains(routeRules(res), `"outbound":"出去","source_ip_cidr":["192.168.1.20/32"]`) {
		t.Errorf("改名后「不进内核」出口: %s", routeRules(res))
	}

	// 有共享网络入站:按来源认终端的规则(路由和 DNS 两处)带「入站不是共享网络」
	share := `"servers":[{"id":"a1","protocol":"shadowsocks","port":8388,"method":"aes-128-gcm","password":"p"}]`
	prof := `{"dns":{"split":true},` + share + `,"clientRoutes":[{"id":"r","name":"R","match":"ip","outbound":"S","sources":["192.168.1.0/24"]}]}`
	res = build(prof, "")
	guard := `{"inbound":["share-a1"],"invert":true}`
	if n := strings.Count(routeRules(res), guard); n != 1 {
		t.Errorf("路由里该有 1 处共享网络保护,得到 %d: %s", n, routeRules(res))
	}
	if n := strings.Count(bhJSON(t, bhCfg(res)["dns"].(map[string]any)["rules"]), guard); n != 1 {
		t.Errorf("DNS 里该有 1 处共享网络保护,得到 %d", n)
	}
	// 没有共享网络入站就不加
	res = build(rt(`"S"`, `"192.168.1.0/24"`), "")
	if strings.Contains(routeRules(res), `"invert":true`) {
		t.Errorf("没有共享网络入站不该有保护: %s", routeRules(res))
	}
	// 共享网络入站停用时也不加
	res = build(`{"dns":{},"servers":[{"id":"a1","enabled":false,"protocol":"shadowsocks","port":8388,"method":"aes-128-gcm","password":"p"}],"clientRoutes":[{"id":"r","name":"R","match":"ip","outbound":"S","sources":["192.168.1.0/24"]}]}`, "")
	if strings.Contains(routeRules(res), `"invert":true`) {
		t.Errorf("共享网络停用不该有保护")
	}
}

// 旁路计划的入参接线:选择、FakeIP、DNS 模式、终端分流、内置直连改名都要传到 bypassPlan。
func TestBuildTailBypassPlanWiring(t *testing.T) {
	cn := func(def string) string {
		return `{"id":"p1","name":"国内","default":` + def + `,"rulesets":["geoip-cn"]}`
	}
	dom := `{"id":"p2","name":"域名","default":"S","domainSuffix":["a.example"]}`
	exclude := func(profile, top string) string {
		v, has := tunOf(t, tl(t, profile, top))["route_exclude_address_set"]
		if !has {
			return "无"
		}
		return bhJSON(t, v)
	}
	cases := []struct {
		name, profile, top, want string
	}{
		// 站点集默认走代理 → 不放行;当前选择改成直连 → 放行(选择要传给计划)
		{"默认走代理", `{"dns":{},"routing":{"policies":[` + cn(`"S"`) + `]}}`, ``, "无"},
		{"选择改成直连", `{"dns":{},"routing":{"policies":[` + cn(`"S"`) + `]}}`, `,"selections":{"国内":"直连"}`, `["geoip-cn"]`},
		{"默认直连选择改成代理", `{"dns":{},"routing":{"policies":[` + cn(`"direct"`) + `]}}`, `,"selections":{"国内":"S"}`, "无"},
		{"默认直连", `{"dns":{},"routing":{"policies":[` + cn(`"direct"`) + `]}}`, ``, `["geoip-cn"]`},
		// 域名类站点集走代理:没开 FakeIP 就关,开着 FakeIP 不关,FakeIP 开着但 DNS 模式 off(FakeIP 用不上)又关
		{"域名走代理没开 FakeIP", `{"dns":{},"routing":{"policies":[` + cn(`"direct"`) + `,` + dom + `]}}`, ``, "无"},
		{"域名走代理开 FakeIP", `{"dns":{"fakeIpForProxy":true},"routing":{"policies":[` + cn(`"direct"`) + `,` + dom + `]}}`, ``, `["geoip-cn"]`},
		{"域名走代理 FakeIP 但 DNS off", `{"dns":{"fakeIpForProxy":true,"mode":"off"},"routing":{"policies":[` + cn(`"direct"`) + `,` + dom + `]}}`, ``, "无"},
		// 有终端走代理 → 关
		{"有终端走代理", `{"dns":{},"clientRoutes":[{"id":"r","name":"R","match":"ip","outbound":"S","sources":["192.168.1.9"]}],"routing":{"policies":[` + cn(`"direct"`) + `]}}`, ``, "无"},
		// 内置直连改名:站点集默认写新名字也是直连
		{"内置直连改名", `{"dns":{},"routing":{"policies":[` + cn(`"出去"`) + `]}}`, `,"userGroups":[{"id":"builtin-direct","name":"出去"},{"id":"s","name":"S","members":["A"]}]`, `["geoip-cn"]`},
	}
	for _, c := range cases {
		if got := exclude(c.profile, c.top); got != c.want {
			t.Errorf("%s: route_exclude_address_set 得到 %s 期望 %s", c.name, got, c.want)
		}
	}
}

// 首包预判放行(排在 sniff 前面)只在 auto_redirect 开着且「直连不进内核」没关时写。
func TestBuildTailPreMatchBypassWiring(t *testing.T) {
	routes := func(profile string) string {
		return bhJSON(t, bhCfg(tl(t, profile, ""))["route"].(map[string]any)["rules"])
	}
	const direct = `"clientRoutes":[{"id":"x","name":"X","match":"ip","outbound":"直连","sources":["192.168.1.30"]}]`
	on := routes(`{"dns":{},"tun":{"autoRedirect":true},` + direct + `}`)
	if !strings.HasPrefix(on, `[{"action":"bypass"`) || !strings.Contains(on, `{"source_ip_cidr":["192.168.1.30/32"]}`) {
		t.Errorf("开着时第一条该是 bypass 且放行直连终端: %s", on)
	}
	if i, j := strings.Index(on, `"action":"bypass"`), strings.Index(on, `"action":"sniff"`); i < 0 || j < 0 || i > j {
		t.Errorf("bypass 该排在 sniff 前面: %s", on)
	}
	for name, p := range map[string]string{
		"directBypass 关":   `{"dns":{},"directBypass":false,"tun":{"autoRedirect":true},` + direct + `}`,
		"没开 auto_redirect": `{"dns":{},` + direct + `}`,
		"DNS off":          `{"dns":{"mode":"off"},"tun":{"autoRedirect":true},` + direct + `}`,
	} {
		if got := routes(p); strings.Contains(got, `"action":"bypass"`) {
			t.Errorf("%s: 不该有 bypass 规则: %s", name, got)
		}
	}
}

// DNS 一侧的接线:名单外入站答 dns-terminal-direct;dnsmasq 模式下把直连终端转进来要 auto_redirect 与「直连不进内核」同时开着。
func TestBuildTailTerminalDNSWiring(t *testing.T) {
	dnsRules := func(profile string) string {
		return bhJSON(t, bhCfg(tl(t, profile, ""))["dns"].(map[string]any)["rules"])
	}
	admit := `{"dns":{"split":true},"tun":{"autoRedirect":true},"clientRoutes":[{"id":"a","name":"A","match":"ip","admit":true,"sources":["192.168.1.10"]}]}`
	if got := dnsRules(admit); !strings.Contains(got, `{"inbound":["dns-in-direct"],"server":"dns-terminal-direct"}`) {
		t.Errorf("名单外入站: %s", got)
	}
	noAR := strings.Replace(admit, `"tun":{"autoRedirect":true},`, ``, 1)
	if got := dnsRules(noAR); strings.Contains(got, "dns-in-direct") {
		t.Errorf("没开 auto_redirect 没有名单外入站: %s", got)
	}
	const term = `"clientRoutes":[{"id":"x","name":"X","match":"ip","outbound":"直连","sources":["192.168.1.30"]}]`
	mk := func(extra string) string {
		return `{"dns":{"split":true,"mode":"dnsmasq"},` + extra + term + `}`
	}
	const src = `"source_ip_cidr":["192.168.1.30/32"]`
	on := dnsRules(mk(`"tun":{"autoRedirect":true},`))
	if strings.Count(on, `"server":"dns-local",`+src) != 2 || !strings.Contains(on, `{"server":"dns-terminal-direct",`+src+`}`) {
		t.Errorf("dnsmasq 转进直连终端: %s", on)
	}
	for name, p := range map[string]string{
		"直连不进内核关":          mk(`"directBypass":false,"tun":{"autoRedirect":true},`),
		"没开 auto_redirect": mk(``),
	} {
		if got := dnsRules(p); strings.Contains(got, src) {
			t.Errorf("%s: 不该转进来: %s", name, got)
		}
	}
}
