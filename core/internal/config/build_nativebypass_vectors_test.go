package config

import "testing"

// 自写测试向量:BuildDetailed 返回的 nativeBypass({enabled, sets, reason},自研多带,基线没有),
// 给后端 terminal-test 的 firstLayer 用。期望值人工推算,与同一份配置里 tun 入站实际写进去的旁路集合对照:
//   - auto_redirect 开着:旁路走热切换集合,名单是站点集里走直连的 geoip- 候选(此例 geoip-cn),与 route_exclude_address_set
//     里的 obflip-byp-geoip-cn 一一对应;入参 nativeBypass 此时不起作用,所以不影响结果;没有候选就是关、名单为空数组(入参没给 nativeBypass 时 reason 仍取旁路计划给的原因);
//   - 没开 auto_redirect、入参没给 nativeBypass:取旁路计划(默认兜底直连、站点集直连 → geoip-cn 整份进 route_exclude_address_set);
//     directBypass 关着或兜底走代理时没有可放行的集合 → 关,且 reason 非空(计划给的原因);
//   - 没开 auto_redirect、入参给了 nativeBypass 对象:enabled 真值且名单里有字符串项才算生效,非字符串项丢掉,reason 为空串;
//   - 结果键序固定 enabled / reason / sets(map 序列化按字母序),名单永远是数组、不是 null。

func TestBuildNativeBypass(t *testing.T) {
	cn := `{"id":"p1","name":"国内","default":"direct","rulesets":["geoip-cn"]}`
	rt := `,"routing":{"policies":[` + cn + `]}`
	cases := []struct{ name, profile, top, want string }{
		{"auto_redirect:取直连候选", `{"dns":{},"tun":{"autoRedirect":true}` + rt + `}`, ``, `{"enabled":true,"reason":"","sets":["geoip-cn"]}`},
		{"auto_redirect:入参 nativeBypass 不起作用", `{"dns":{},"tun":{"autoRedirect":true}` + rt + `}`, `,"nativeBypass":{"enabled":true,"sets":["geoip-private"]}`, `{"enabled":true,"reason":"","sets":["geoip-cn"]}`},
		{"auto_redirect:没有候选", `{"dns":{},"tun":{"autoRedirect":true}}`, ``, `{"enabled":false,"reason":"走直连的站点集里没有 geoip 规则集,入口没有可用的 IP 集合","sets":[]}`},
		{"无 auto_redirect:取计划", `{"dns":{}` + rt + `}`, ``, `{"enabled":true,"reason":"","sets":["geoip-cn"]}`},
		{"入参对象:生效", `{"dns":{}}`, `,"nativeBypass":{"enabled":true,"sets":["geoip-cn","geoip-us"]}`, `{"enabled":true,"reason":"","sets":["geoip-cn","geoip-us"]}`},
		{"入参对象:非字符串项丢掉", `{"dns":{}}`, `,"nativeBypass":{"enabled":true,"sets":["geoip-cn",5,null]}`, `{"enabled":true,"reason":"","sets":["geoip-cn"]}`},
		{"入参对象:enabled 假", `{"dns":{}}`, `,"nativeBypass":{"enabled":false,"sets":["geoip-cn"]}`, `{"enabled":false,"reason":"","sets":[]}`},
		{"入参对象:名单空", `{"dns":{}}`, `,"nativeBypass":{"enabled":true,"sets":[]}`, `{"enabled":false,"reason":"","sets":[]}`},
	}
	for _, c := range cases {
		res := tl(t, c.profile, c.top)
		if got := bhJSON(t, res["nativeBypass"]); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

// 没有生效的旁路时 reason 必须有内容(计划给的原因),sets 是空数组;原因文字本身由 bypass 的自写向量覆盖,这里只钉「非空」。
func TestBuildNativeBypassOffHasReason(t *testing.T) {
	cn := `{"id":"p1","name":"国内","default":"direct","rulesets":["geoip-cn"]}`
	for name, p := range map[string]string{
		"directBypass 关": `{"dns":{},"directBypass":false,"routing":{"policies":[` + cn + `]}}`,
		"兜底走代理":          `{"dns":{},"routing":{"policies":[` + cn + `],"fallbackDefault":"S"}}`,
		"没有站点集":          `{"dns":{}}`,
	} {
		res := tl(t, p, "")
		nb := res["nativeBypass"].(map[string]any)
		if nb["enabled"] != false {
			t.Errorf("%s: enabled=%v", name, nb["enabled"])
		}
		if got := bhJSON(t, nb["sets"]); got != `[]` {
			t.Errorf("%s: sets=%s", name, got)
		}
		if r, _ := nb["reason"].(string); r == "" {
			t.Errorf("%s: reason 为空", name)
		}
	}
}

// nativeBypass 与 tun 入站里实际写的旁路集合必须一致(没开 auto_redirect 时整份集合原样出现在 route_exclude_address_set)。
func TestBuildNativeBypassMatchesTun(t *testing.T) {
	cn := `{"id":"p1","name":"国内","default":"direct","rulesets":["geoip-cn"]}`
	res := tl(t, `{"dns":{},"routing":{"policies":[`+cn+`]}}`, "")
	nb := res["nativeBypass"].(map[string]any)
	if got, want := bhJSON(t, nb["sets"]), bhJSON(t, tunOf(t, res)["route_exclude_address_set"]); got != want {
		t.Errorf("nativeBypass.sets=%s 与 tun route_exclude_address_set=%s 不一致", got, want)
	}
}
