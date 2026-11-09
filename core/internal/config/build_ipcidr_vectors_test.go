package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// 前置自定义分流里网段写错的 ipCidr 行,整理时就被丢掉,不会原样进 route 规则(否则 sing-box 启动 / check 会报错)。
// 期望值人工推算:合法的行照常写成 ip_cidr 规则,非法的行不留任何痕迹;只剩非法行时一条 ip_cidr 都没有。
func TestBuildDropsBadIPCidrCustomRows(t *testing.T) {
	row := func(val, out string) string {
		return `{"type":"ipCidr","value":"` + val + `","outbound":"` + out + `"}`
	}
	build := func(rows ...string) map[string]any {
		a := sk("A", "s1.example.com", 1)
		res := bhMust(t, `{"nodes":[`+a+`],"userGroups":[{"id":"s","name":"S","members":["A"]}],`+
			`"profile":{"dns":{},"routing":{"custom":{"enabled":true,"rules":[`+strings.Join(rows, ",")+`]}}}}`)
		dumpConfig(t, res)
		return res
	}
	// 自定义分流的 ip_cidr 行写成 {mode:and, rules:[{ip_cidr},{域名占位取反}], outbound};
	// tun 防回环的 reject 规则也带 ip_cidr(172.19.0.0/30),不是自定义行,跳过。
	ipCidrRules := func(res map[string]any) []string {
		var out []string
		route := res["config"].(map[string]any)["route"].(map[string]any)
		for _, r := range route["rules"].([]any) {
			m := r.(map[string]any)
			if m["action"] == "reject" {
				continue
			}
			subs := []any{m}
			if inner, ok := m["rules"].([]any); ok {
				subs = inner
			}
			for _, sr := range subs {
				if v, ok := sr.(map[string]any)["ip_cidr"]; ok {
					b, _ := json.Marshal(v)
					outbound, _ := m["outbound"].(string)
					out = append(out, string(b)+"→"+outbound)
				}
			}
		}
		return out
	}
	bad := []string{"bad", "10.2.0.0/33", "01.2.3.4", "1.2.3.4/", "::1/129", "1.1.1.1,8.8.8.8", "fe80::1%eth0", "geoip-a"}
	var rows []string
	for _, v := range bad {
		rows = append(rows, row(v, "S"))
	}

	// 合法行与非法行混写:只有合法的两条留下,顺序不变
	mixed := append(append([]string{row("1.1.1.0/24", "S")}, rows...), row("2001:db8::1", "S"))
	res := build(mixed...)
	got := ipCidrRules(res)
	want := []string{`["1.1.1.0/24"]→S`, `["2001:db8::1"]→S`}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ip_cidr 规则不对\n有: %v\n要: %v", got, want)
	}
	text, _ := json.Marshal(res["config"])
	for _, v := range bad {
		if strings.Contains(string(text), `"`+v+`"`) {
			t.Errorf("非法网段 %q 不该出现在配置里", v)
		}
	}

	// 只有非法行:没有任何 ip_cidr 规则
	if got := ipCidrRules(build(rows...)); len(got) != 0 {
		t.Errorf("只有非法行时不该有 ip_cidr 规则,有 %v", got)
	}
}
