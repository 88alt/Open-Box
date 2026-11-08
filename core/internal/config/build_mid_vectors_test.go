package config

import (
	"fmt"
	"strings"
	"testing"
)

// 自写测试向量:build.go 的 BuildDetailed 第二段里「出口类别 / 阻断 IPv6 与 QUIC 的接线 / 域名过滤 / DNS 的入参接线」几块
// (各规则内部形态已在 routing / dnsfilter 包的向量里钉住,这里只钉 build 怎么把入参喂进去)。输入自己拟,期望值按规则人工推算:
//   - systemDns 取数组(非数组 / 空数组当没写):第一个当 dns-direct 的地址,没写用 223.5.5.5;
//   - 阻断 IPv6 只在 profile.ipv6 为真值且 ipv6Proxy 恰为 "ipv4" 时生效,阻断 QUIC 只在 profile.rejectQuic 严格等于 true 时生效;
//     站点集与兜底带热切换开关,规则按开关写(与此刻选中什么无关);没有开关的出站(自定义规则的目标)按「不是内置直连 / 拒绝(当前名字)」判代理,
//     出口类别表的键恰好是有开关的那批 tag,所以表里的类别在这里查不到、不影响结果;
//   - 域名过滤:profile.dns.filter.enabled 为真值才启用;启用时名单文件必须带上与 filterExpectedKey 相同的 key,否则报「域名过滤名单尚未准备好…」;
//     名单里的规则集写进 route.rule_set(排在站点集 / 节点直连规则集前面),阻断规则写进 dns.rules。

func TestBuildMidSystemDNS(t *testing.T) {
	a := sk("A", "s1.example.com", 1)
	direct := func(res map[string]any) string {
		for _, s := range bhCfg(res)["dns"].(map[string]any)["servers"].([]any) {
			m := s.(map[string]any)
			if m["tag"] == "dns-direct" {
				return m["server"].(string)
			}
		}
		return ""
	}
	for extra, want := range map[string]string{
		`"systemDns":["119.29.29.29","1.1.1.1"]`: "119.29.29.29",
		`"systemDns":["1.1.1.1"]`:                "1.1.1.1",
		`"systemDns":[]`:                         "223.5.5.5",
		`"systemDns":null`:                       "223.5.5.5",
		`"systemDns":"119.29.29.29"`:             "223.5.5.5",
		``:                                       "223.5.5.5",
	} {
		if got := direct(bhMust(t, bhArgs(extra, a))); got != want {
			t.Errorf("%q: dns-direct=%s 期望 %s", extra, got, want)
		}
	}
}

func TestBuildMidRejectWiring(t *testing.T) {
	ug := `"userGroups":[{"id":"s","name":"S","members":["A"]}]`
	a := sk("A", "s1.example.com", 1)
	pols := `[{"id":"p1","name":"P1","default":"S","domainSuffix":["p1.example"]},{"id":"p2","name":"P2","default":"direct","domainSuffix":["p2.example"]}]`
	custom := `"custom":{"enabled":true,"rules":[{"type":"domainSuffix","value":"c1.example","outbound":"S"},{"type":"domainSuffix","value":"c2.example","outbound":"direct"},{"type":"domainSuffix","value":"c3.example","outbound":"block"}]}`
	mk := func(profileExtra, top string) string {
		return `{"nodes":[` + a + `],` + ug + top + `,"profile":{"dns":{},"routing":{"policies":` + pols + `,` + custom + `}` + profileExtra + `}}`
	}
	// 数「阻断规则」里同时含某个域名 / 开关和某个标记的条数(站点集 / 兜底带热切换开关,自定义规则的目标没有开关)
	count := func(res map[string]any, key, marker string) int {
		n := 0
		for _, r := range bhCfg(res)["route"].(map[string]any)["rules"].([]any) {
			m := r.(map[string]any)
			if m["action"] != "reject" {
				continue
			}
			js := bhJSON(t, m)
			if strings.Contains(js, key) && strings.Contains(js, marker) {
				n++
			}
		}
		return n
	}
	v6, quic := `"ip_version":6`, `"port":443`
	// 缺省:都不阻断
	res := bhMust(t, mk("", ""))
	for _, key := range []string{"p1.example", "p2.example", "c1.example", "c2.example", "c3.example", "obflip-fallback"} {
		if count(res, key, v6)+count(res, key, quic) != 0 {
			t.Errorf("缺省不该有阻断规则(%s)", key)
		}
	}
	// ipv6Proxy=ipv4 且 ipv6 开:站点集 / 兜底都带开关阻断 IPv6(不看此刻选中什么,开关翻到代理时才生效);
	// 自定义规则的目标没有开关,目标是组(代理)的阻断,目标是直连 / 拒绝的不阻断;QUIC 不受影响
	res = bhMust(t, mk(`,"ipv6":true,"ipv6Proxy":"ipv4"`, ""))
	for key, want := range map[string]int{"p1.example": 1, "p2.example": 1, "obflip-fallback": 1, "c1.example": 1, "c2.example": 0, "c3.example": 0} {
		if got := count(res, key, v6); got != want {
			t.Errorf("ipv4 模式 %s 的 IPv6 阻断条数 %d 期望 %d", key, got, want)
		}
		if got := count(res, key, quic); got != 0 {
			t.Errorf("ipv4 模式 %s 不该阻断 QUIC,得到 %d", key, got)
		}
	}
	// 别的模式 / 没开 ipv6 都不阻断
	for _, p := range []string{`,"ipv6":true,"ipv6Proxy":"node"`, `,"ipv6":true,"ipv6Proxy":"bypass"`, `,"ipv6":true`, `,"ipv6Proxy":"ipv4"`, `,"ipv6":false,"ipv6Proxy":"ipv4"`} {
		res := bhMust(t, mk(p, ""))
		if n := count(res, "p1.example", v6) + count(res, "c1.example", v6) + count(res, "obflip-fallback", v6); n != 0 {
			t.Errorf("%s 不该阻断 IPv6,得到 %d", p, n)
		}
	}
	// rejectQuic 严格等于 true:作用范围同上
	res = bhMust(t, mk(`,"rejectQuic":true`, ""))
	for key, want := range map[string]int{"p1.example": 1, "p2.example": 1, "obflip-fallback": 1, "c1.example": 1, "c2.example": 0, "c3.example": 0} {
		if got := count(res, key, quic); got != want {
			t.Errorf("rejectQuic 时 %s 的 QUIC 阻断条数 %d 期望 %d", key, got, want)
		}
		if got := count(res, key, v6); got != 0 {
			t.Errorf("rejectQuic 时 %s 不该阻断 IPv6,得到 %d", key, got)
		}
	}
	for _, v := range []string{`"true"`, `1`, `false`, `null`} {
		res := bhMust(t, mk(`,"rejectQuic":`+v, ""))
		if n := count(res, "p1.example", quic) + count(res, "c1.example", quic) + count(res, "obflip-fallback", quic); n != 0 {
			t.Errorf("rejectQuic=%s 不该阻断 QUIC,得到 %d", v, n)
		}
	}
	// 内置直连改名后,自定义规则里的 direct 记号换成新名字,仍被认成直连不阻断;新名字之外的组照常阻断
	ugr := `"userGroups":[{"id":"builtin-direct","name":"DIRECT"},{"id":"s","name":"S","members":["A"]}]`
	res = bhMust(t, `{"nodes":[`+a+`],`+ugr+`,"profile":{"dns":{},"rejectQuic":true,"routing":{`+custom+`}}}`)
	if count(res, "c1.example", quic) != 1 || count(res, "c2.example", quic) != 0 || count(res, "c3.example", quic) != 0 {
		t.Errorf("直连改名: c1=%d c2=%d c3=%d", count(res, "c1.example", quic), count(res, "c2.example", quic), count(res, "c3.example", quic))
	}
	// selections 不是对象:当没有(不影响阻断);是对象时的取值由站点集默认值 / DNS 那边消费
	for _, bad := range []string{`null`, `[1]`, `"x"`, `5`} {
		res := bhMust(t, mk(`,"rejectQuic":true`, `,"selections":`+bad))
		if count(res, "c1.example", quic) != 1 || count(res, "p1.example", quic) != 1 {
			t.Errorf("selections=%s 影响了阻断", bad)
		}
	}
}

func TestBuildMidDNSFilter(t *testing.T) {
	a := sk("A", "s1.example.com", 1)
	artifact := `{"key":"k1","sets":[{"type":"local","tag":"f-block","format":"binary","path":"/f/block.srs"}],"blocks":[{"tag":"f-block","important":false}],"allow":[],"allowImportant":[],"userAllow":[]}`
	mk := func(filter, extra string) string {
		return `{"nodes":[` + a + `],"profile":{"dns":{` + filter + `}}` + extra + `}`
	}
	// 没启用(缺失 / 假值):不管名单文件是什么都不报错,配置里没有过滤痕迹
	for _, f := range []string{``, `"filter":{}`, `"filter":{"enabled":false}`, `"filter":{"enabled":0}`, `"filter":null`} {
		res := bhMust(t, mk(f, `,"dnsFilter":`+artifact+`,"filterExpectedKey":"zzz"`))
		if strings.Contains(bhJSON(t, bhCfg(res)), "f-block") {
			t.Errorf("%q 没启用不该有过滤痕迹", f)
		}
	}
	// 启用但名单没准备好(缺失 / key 不同 / 没有期望 key / key 不是字符串):报固定文字
	notReady := "域名过滤名单尚未准备好,请更新名单后应用 DNS 设置"
	on := `"filter":{"enabled":true}`
	for _, extra := range []string{
		``, `,"dnsFilter":null`, `,"filterExpectedKey":"k1"`, `,"dnsFilter":` + artifact + `,"filterExpectedKey":"k2"`,
		`,"dnsFilter":` + artifact, `,"dnsFilter":` + artifact + `,"filterExpectedKey":5`, `,"dnsFilter":{"key":5,"sets":[]},"filterExpectedKey":5`,
	} {
		if _, err := bhRun(t, mk(on, extra)); err == nil || err.Error() != notReady {
			t.Errorf("%q 该报「%s」,得到 %v", extra, notReady, err)
		}
	}
	// 启用且就绪:规则集写进 route.rule_set(在节点直连规则集前面),阻断规则写进 dns.rules
	res := bhMust(t, mk(on, `,"dnsFilter":`+artifact+`,"filterExpectedKey":"k1"`))
	var tags []string
	for _, e := range bhCfg(res)["route"].(map[string]any)["rule_set"].([]any) {
		tags = append(tags, e.(map[string]any)["tag"].(string))
	}
	if got := strings.Join(tags, ","); got != "f-block,obnode-direct,obnode-direct-ip,obflip-fallback" {
		t.Errorf("rule_set 顺序: %s", got)
	}
	dnsRules := bhJSON(t, bhCfg(res)["dns"].(map[string]any)["rules"])
	if !strings.Contains(dnsRules, `"rcode":"NXDOMAIN"`) || !strings.Contains(dnsRules, `"f-block"`) {
		t.Errorf("dns.rules 里该有 f-block 的 NXDOMAIN 阻断: %s", dnsRules)
	}
	// 名单里有重要规则集时重要的排在前面;名单文件格式错(sets 不是数组)报错而不是悄悄略过
	if _, err := bhRun(t, mk(on, `,"dnsFilter":{"key":"k1","sets":"x"},"filterExpectedKey":"k1"`)); err == nil {
		t.Errorf("sets 不是数组应报错")
	}
	_ = fmt.Sprint
}
