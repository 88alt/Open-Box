package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// 自写测试向量:build.go 的 BuildDetailed 里「tun 入站」与「入站」两块(源文件顺序里「tun 入站」「入口原生旁路」之前的部分,
// 入口原生旁路与 clientRoutes 的 MAC 白 / 黑名单留给后面一段)。输入自己拟,期望值按下面的规则推算:
//   - tun 固定字段:type tun、interface_name openbox-tun、tag tun-in、auto_route、udp_timeout 60s;
//     address 只有 IPv4(172.19.0.1/30),IPv6 进内核(profile.ipv6 真值且 ipv6Proxy 不是 "bypass")时再加 fdfe:dcba:9876::1/126;
//     strict_route = ipv6Proxy 不是 "bypass";stack 取 mixed / gvisor / system 之一否则 mixed;mtu 在 1280–65535 的整数才写;
//   - auto_redirect = profile.tun 为真值、tun.autoRedirect 真值、DNS 模式不是 "off"(缺省模式是 hijack);开着时 dns_mode = hijack,
//     dns_address = tun 对端(v6 进内核时再加 v6 对端),没开时 dns_mode = disabled 且没有 dns_address;
//   - route_exclude_address = 私网 / 链路本地 / 组播这几段(v6 进内核再加 fc00::/7、fe80::/10、ff00::/8)减去「洞」:
//     洞默认是 tun 自己的两个网段;auto_redirect 且模式是 hijack 时再加 localSubnets;开着 FakeIP 且 ipv6Proxy 是 node(含缺省)时再加 fc00::/18;
//     自定义分流(routing.custom 启用)里类型 ipCidr、值是合法网段、目标是已有的非内置出站、且与排除段重叠的行,
//     把「该网段减去受保护网段(localSubnets、tun 网段、127/8、169.254/16、::1/128、fe80::/10)」的剩余也加进洞;
//     auto_redirect 时再挖掉地址空间最后一个地址(255.255.255.255/32 与 v6 全 1 的 /128);
//   - 入站顺序:tun、panel-in(mixed 127.0.0.1:7891)、dns-in(direct,监听 0.0.0.0,profile.ipv6 真值时 ::,端口 7853)。
// route_exclude_address 的期望值在 testdata/config_tun_exclude_oracle.json,由 testdata/gen_tun_exclude_oracle.py 用 Python ipaddress 独立算出。

func tunOracle(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/config_tun_exclude_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for k, v := range all {
		if strings.HasPrefix(k, "_") {
			continue
		}
		var l []string
		if err := json.Unmarshal(v, &l); err != nil {
			t.Fatal(err)
		}
		out[k] = l
	}
	return out
}

func tunOf(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	m := bhCfg(res)["inbounds"].([]any)[0].(map[string]any)
	if m["tag"] != "tun-in" {
		t.Fatalf("第一个入站不是 tun-in: %v", m["tag"])
	}
	return m
}

func tunBuild(t *testing.T, profileExtra, top string) map[string]any {
	t.Helper()
	a := sk("A", "s1.example.com", 1)
	return tunOf(t, bhMust(t, `{"nodes":[`+a+`],"userGroups":[{"id":"s","name":"S","members":["A"]}]`+top+`,"profile":{"dns":{}`+profileExtra+`}}`))
}

func tunExclude(t *testing.T, m map[string]any) []string {
	t.Helper()
	var out []string
	for _, v := range m["route_exclude_address"].([]any) {
		out = append(out, v.(string))
	}
	return out
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuildTunDefaults(t *testing.T) {
	o := tunOracle(t)
	m := tunBuild(t, "", "")
	ex, _ := json.Marshal(o["v4"])
	want := `{"address":["172.19.0.1/30"],"auto_route":true,"dns_mode":"disabled","interface_name":"openbox-tun","route_exclude_address":` + string(ex) + `,"stack":"mixed","strict_route":true,"tag":"tun-in","type":"tun","udp_timeout":"60s"}`
	if got := bhJSON(t, m); got != want {
		t.Errorf("默认 tun:\n得到 %s\n期望 %s", got, want)
	}
}

func TestBuildTunInboundOrder(t *testing.T) {
	a := sk("A", "s1.example.com", 1)
	for profile, listen := range map[string]string{`{"dns":{}}`: "0.0.0.0", `{"dns":{},"ipv6":false}`: "0.0.0.0", `{"dns":{},"ipv6":true}`: "::", `{"dns":{},"ipv6":true,"ipv6Proxy":"bypass"}`: "::", `{"dns":{},"ipv6":1}`: "::"} {
		res := bhMust(t, `{"nodes":[`+a+`],"profile":`+profile+`}`)
		ins := bhCfg(res)["inbounds"].([]any)
		if len(ins) != 3 {
			t.Fatalf("%s 入站数 %d", profile, len(ins))
		}
		if got := bhJSON(t, ins[1]); got != `{"listen":"127.0.0.1","listen_port":7891,"tag":"panel-in","type":"mixed"}` {
			t.Errorf("panel-in: %s", got)
		}
		if got := bhJSON(t, ins[2]); got != `{"listen":"`+listen+`","listen_port":7853,"tag":"dns-in","type":"direct"}` {
			t.Errorf("%s 的 dns-in: %s", profile, got)
		}
	}
}

func TestBuildTunIPv6AndStrictRoute(t *testing.T) {
	o := tunOracle(t)
	cases := []struct {
		name, extra string
		v6, strict  bool
	}{
		{"没开 ipv6", ``, false, true},
		{"ipv6 假值", `,"ipv6":0,"ipv6Proxy":"node"`, false, true},
		{"node", `,"ipv6":true,"ipv6Proxy":"node"`, true, true},
		{"缺省按 node", `,"ipv6":true`, true, true},
		{"别的取值按 node", `,"ipv6":true,"ipv6Proxy":"x"`, true, true},
		{"ipv4", `,"ipv6":true,"ipv6Proxy":"ipv4"`, true, true},
		{"bypass", `,"ipv6":true,"ipv6Proxy":"bypass"`, false, false},
		{"没开 ipv6 时 bypass 无意义", `,"ipv6Proxy":"bypass"`, false, true},
	}
	for _, c := range cases {
		m := tunBuild(t, c.extra, "")
		wantAddr := `["172.19.0.1/30"]`
		wantEx := o["v4"]
		if c.v6 {
			wantAddr = `["172.19.0.1/30","fdfe:dcba:9876::1/126"]`
			wantEx = o["v46"]
		}
		if got := bhJSON(t, m["address"]); got != wantAddr {
			t.Errorf("%s address=%s", c.name, got)
		}
		if got := tunExclude(t, m); !sameList(got, wantEx) {
			t.Errorf("%s route_exclude_address 条数 %d 期望 %d", c.name, len(got), len(wantEx))
		}
		if m["strict_route"] != c.strict {
			t.Errorf("%s strict_route=%v", c.name, m["strict_route"])
		}
	}
}

func TestBuildTunStackAndMTU(t *testing.T) {
	for stack, want := range map[string]string{`"mixed"`: "mixed", `"gvisor"`: "gvisor", `"system"`: "system", `"nope"`: "mixed", `""`: "mixed", `5`: "mixed", `null`: "mixed"} {
		m := tunBuild(t, `,"tun":{"stack":`+stack+`}`, "")
		if m["stack"] != want {
			t.Errorf("stack=%s 得到 %v 期望 %s", stack, m["stack"], want)
		}
	}
	for mtu, want := range map[string]any{`1500`: float64(1500), `1280`: float64(1280), `65535`: float64(65535), `1279`: nil, `65536`: nil, `0`: nil, `-1`: nil, `null`: nil, `"x"`: nil, `1500.5`: nil} {
		m := tunBuild(t, `,"tun":{"mtu":`+mtu+`}`, "")
		got, has := m["mtu"]
		if want == nil && has {
			t.Errorf("mtu=%s 不该写 mtu,得到 %v", mtu, got)
		}
		if want != nil && got != want {
			t.Errorf("mtu=%s 得到 %v 期望 %v", mtu, got, want)
		}
	}
	// tun 不是对象:当没写(字符串 / 数组 / 数字都不报错)
	for _, bad := range []string{`null`, `"x"`, `[1]`, `5`, `true`} {
		m := tunBuild(t, `,"tun":`+bad, "")
		if m["stack"] != "mixed" || m["dns_mode"] != "disabled" {
			t.Errorf("tun=%s: %v", bad, m)
		}
		if _, has := m["auto_redirect"]; has {
			t.Errorf("tun=%s 不该开 auto_redirect", bad)
		}
	}
}

func TestBuildTunAutoRedirect(t *testing.T) {
	o := tunOracle(t)
	on := `,"tun":{"autoRedirect":true}`
	// 开启:缺省 DNS 模式是 hijack
	m := tunBuild(t, on, "")
	if m["auto_redirect"] != true || m["dns_mode"] != "hijack" || bhJSON(t, m["dns_address"]) != `["172.19.0.2"]` {
		t.Errorf("auto_redirect 开启: %v", m)
	}
	if got := tunExclude(t, m); !sameList(got, o["v4"]) {
		t.Errorf("v4 下 auto_redirect 的排除段该和默认一致(v4 里没有地址空间末尾的地址),条数 %d", len(got))
	}
	// v6 进内核:dns_address 多一个 v6 对端;排除段把 ff00::/8 里最后一个地址也挖掉
	m = tunBuild(t, on+`,"ipv6":true`, "")
	if bhJSON(t, m["dns_address"]) != `["172.19.0.2","fdfe:dcba:9876::2"]` {
		t.Errorf("v6 dns_address: %s", bhJSON(t, m["dns_address"]))
	}
	if got := tunExclude(t, m); !sameList(got, o["v46_end"]) {
		t.Errorf("v6 下 auto_redirect 排除段条数 %d 期望 %d", len(got), len(o["v46_end"]))
	}
	// v6 但 ipv6Proxy=bypass:dns_address 只有 v4 对端
	m = tunBuild(t, on+`,"ipv6":true,"ipv6Proxy":"bypass"`, "")
	if bhJSON(t, m["dns_address"]) != `["172.19.0.2"]` {
		t.Errorf("bypass 下 dns_address: %s", bhJSON(t, m["dns_address"]))
	}
	// 不开的各种情形:tun 缺失 / autoRedirect 假值 / DNS 模式 off(即使 autoRedirect 为真)
	for name, extra := range map[string]string{
		"autoRedirect 假值": `,"tun":{"autoRedirect":false}`, "autoRedirect 0": `,"tun":{"autoRedirect":0}`,
		"autoRedirect 缺失": `,"tun":{}`, "tun 假值": `,"tun":null`,
	} {
		m := tunBuild(t, extra, "")
		if _, has := m["auto_redirect"]; has || m["dns_mode"] != "disabled" {
			t.Errorf("%s 不该开: %v", name, m)
		}
		if _, has := m["dns_address"]; has {
			t.Errorf("%s 不该有 dns_address", name)
		}
	}
	a := sk("A", "s1.example.com", 1)
	res := bhMust(t, `{"nodes":[`+a+`],"profile":{"dns":{"mode":"off"},"tun":{"autoRedirect":true}}}`)
	m = tunOf(t, res)
	if _, has := m["auto_redirect"]; has || m["dns_mode"] != "disabled" {
		t.Errorf("DNS 模式 off 时不该开 auto_redirect: %v", m)
	}
	// autoRedirect 为真值(1 / "x")也算开
	for _, v := range []string{`1`, `"x"`, `{}`} {
		m := tunBuild(t, `,"tun":{"autoRedirect":`+v+`}`, "")
		if m["auto_redirect"] != true {
			t.Errorf("autoRedirect=%s 该开", v)
		}
	}
	// localSubnets:只有 auto_redirect 且模式 hijack 时才挖洞;dnsmasq / 别的模式不挖,没开 auto_redirect 也不挖
	ls := `,"localSubnets":["192.168.1.0/24"]`
	a1 := func(mode, tun string) []string {
		res := bhMust(t, `{"nodes":[`+a+`]`+ls+`,"profile":{"dns":{"mode":"`+mode+`"}`+tun+`}}`)
		return tunExclude(t, tunOf(t, res))
	}
	if got := a1("hijack", `,"tun":{"autoRedirect":true}`); !sameList(got, o["v4_local"]) {
		t.Errorf("hijack + auto_redirect 该挖掉 localSubnets,条数 %d 期望 %d", len(got), len(o["v4_local"]))
	}
	for name, got := range map[string][]string{
		"dnsmasq": a1("dnsmasq", `,"tun":{"autoRedirect":true}`), "别的模式": a1("x", `,"tun":{"autoRedirect":true}`),
		"没开 auto_redirect": a1("hijack", ``),
	} {
		if !sameList(got, o["v4"]) {
			t.Errorf("%s 不该挖 localSubnets,条数 %d", name, len(got))
		}
	}
	// dnsmasq 模式下 auto_redirect 照开,dns_mode 也是 hijack
	res = bhMust(t, `{"nodes":[`+a+`],"profile":{"dns":{"mode":"dnsmasq"},"tun":{"autoRedirect":true}}}`)
	if m := tunOf(t, res); m["auto_redirect"] != true || m["dns_mode"] != "hijack" {
		t.Errorf("dnsmasq + auto_redirect: %v", m)
	}
}

func TestBuildTunHoles(t *testing.T) {
	o := tunOracle(t)
	custom := func(rows ...string) string {
		return `"custom":{"enabled":true,"rules":[` + strings.Join(rows, ",") + `]}`
	}
	row := func(val, out string) string {
		return `{"type":"ipCidr","value":"` + val + `","outbound":"` + out + `"}`
	}
	build := func(rows []string, enabled string, top string) []string {
		c := custom(rows...)
		if enabled != "" {
			c = strings.Replace(c, `"enabled":true`, `"enabled":`+enabled, 1)
		}
		return tunExclude(t, tunBuild(t, `,"routing":{`+c+`}`, top))
	}
	// 目标是已有的非内置出站、与排除段重叠:网段挖出来
	if got := build([]string{row("10.1.0.0/16", "S")}, "", ""); !sameList(got, o["v4_hole10"]) {
		t.Errorf("10.1.0.0/16 → S: 条数 %d 期望 %d", len(got), len(o["v4_hole10"]))
	}
	// 没写前缀按单个地址(/32);首尾空白会被去掉
	if got := build([]string{row("10.2.0.0", "S")}, "", ""); !sameList(got, o["v4_hole32"]) {
		t.Errorf("10.2.0.0 → S 该按 /32 挖洞,条数 %d 期望 %d", len(got), len(o["v4_hole32"]))
	}
	// 不挖的:目标是直连 / 拒绝 / 不存在的出站、不重叠、不是合法网段、自定义分流没启用、被 tun 网段整个盖住
	skip := [][]string{
		{row("10.2.0.0/16", "direct")}, {row("10.2.0.0/16", "block")}, {row("10.2.0.0/16", "直连")}, {row("10.2.0.0/16", "拒绝")},
		{row("10.2.0.0/16", "nope")}, {row("8.8.8.0/24", "S")}, {row("bad", "S")}, {row("10.2.0.0/33", "S")}, {row("", "S")},
		{row("172.19.0.0/30", "S")}, {row("172.19.0.1/32", "S")},
	}
	for _, rows := range skip {
		if got := build(rows, "", ""); !sameList(got, o["v4"]) {
			t.Errorf("%v 不该挖洞,条数 %d", rows, len(got))
		}
	}
	if got := build([]string{row("10.1.0.0/16", "S")}, "false", ""); !sameList(got, o["v4"]) {
		t.Errorf("自定义分流停用时不该挖洞,条数 %d", len(got))
	}
	// 只有一部分在受保护网段里:剩余部分挖出来(172.19.0.0/29 去掉 tun 网段后剩 172.19.0.4/30)
	if got := build([]string{row("172.19.0.0/29", "S")}, "", ""); !sameList(got, o["v4_hole172"]) {
		t.Errorf("172.19.0.0/29 → S: 条数 %d 期望 %d", len(got), len(o["v4_hole172"]))
	}
	// 受保护网段还包括 localSubnets:整个在里面的不挖,一半在里面的挖另一半
	if got := build([]string{row("192.168.1.0/24", "S")}, "", `,"localSubnets":["192.168.1.0/24"]`); !sameList(got, o["v4"]) {
		t.Errorf("整个在 localSubnets 里不该挖洞,条数 %d", len(got))
	}
	if got := build([]string{row("192.168.1.0/24", "S")}, "", `,"localSubnets":["192.168.1.128/25"]`); !sameList(got, o["v4_hole_local"]) {
		t.Errorf("一半在 localSubnets 里该挖另一半,条数 %d 期望 %d", len(got), len(o["v4_hole_local"]))
	}
	// 受保护网段里的链路本地地址:v4 的 169.254/16 与 v6 的 fe80::/10 本来就在排除段里,命中它们的规则不再挖洞
	if got := build([]string{row("169.254.1.0/24", "S")}, "", ""); !sameList(got, o["v4"]) {
		t.Errorf("169.254.1.0/24 → S 不该挖洞,条数 %d", len(got))
	}
	v6rules := func(rows ...string) []string {
		return tunExclude(t, tunBuild(t, `,"ipv6":true,"routing":{`+custom(rows...)+`}`, ""))
	}
	if got := v6rules(row("fe80::1/128", "S")); !sameList(got, o["v46"]) {
		t.Errorf("fe80::1/128 → S 不该挖洞,条数 %d", len(got))
	}
	// v6 的规则:在 fc00::/7 里且不在受保护网段里的会挖洞(fd00::/16 → S);这里只看条数变化的方向,期望值同样由 Python 算
	if got := v6rules(row("fd00::/16", "S")); !sameList(got, o["v46_hole_fd00"]) {
		t.Errorf("fd00::/16 → S: 条数 %d 期望 %d", len(got), len(o["v46_hole_fd00"]))
	}
	// 其它类型的行不挖(domainSuffix 等)
	if got := tunExclude(t, tunBuild(t, `,"routing":{"custom":{"enabled":true,"rules":[{"type":"domainSuffix","value":"10.1.0.0/16","outbound":"S"}]}}`, "")); !sameList(got, o["v4"]) {
		t.Errorf("非 ipCidr 行不该挖洞,条数 %d", len(got))
	}
	// FakeIP:ipv6 进内核且 ipv6Proxy 是 node(含缺省)时再挖 fc00::/18;别的情形不挖
	fake := `,"dns":{"fakeIpForProxy":true}`
	build2 := func(extra string) []string {
		a := sk("A", "s1.example.com", 1)
		res := bhMust(t, `{"nodes":[`+a+`],"profile":{"dns":{"fakeIpForProxy":true}`+extra+`}}`)
		return tunExclude(t, tunOf(t, res))
	}
	_ = fake
	if got := build2(`,"ipv6":true`); !sameList(got, o["v46_fake"]) {
		t.Errorf("FakeIP + ipv6 node: 条数 %d 期望 %d", len(got), len(o["v46_fake"]))
	}
	if got := build2(`,"ipv6":true,"ipv6Proxy":"node"`); !sameList(got, o["v46_fake"]) {
		t.Errorf("FakeIP + ipv6Proxy=node: 条数 %d", len(got))
	}
	for name, extra := range map[string]string{"ipv6Proxy=ipv4": `,"ipv6":true,"ipv6Proxy":"ipv4"`} {
		if got := build2(extra); !sameList(got, o["v46"]) {
			t.Errorf("FakeIP + %s 不该挖 fc00::/18,条数 %d", name, len(got))
		}
	}
	if got := build2(``); !sameList(got, o["v4"]) {
		t.Errorf("FakeIP 没开 ipv6 时不该有 v6 洞,条数 %d", len(got))
	}
	// FakeIP 没开(split 为 false / fakeIpForProxy 不是严格 true):不挖
	a := sk("A", "s1.example.com", 1)
	for _, d := range []string{`{"split":false,"fakeIpForProxy":true}`, `{"fakeIpForProxy":"true"}`, `{"fakeIpForProxy":1}`, `{}`} {
		res := bhMust(t, `{"nodes":[`+a+`],"profile":{"dns":`+d+`,"ipv6":true}}`)
		if got := tunExclude(t, tunOf(t, res)); !sameList(got, o["v46"]) {
			t.Errorf("dns=%s 不该挖 fc00::/18,条数 %d", d, len(got))
		}
	}
}
