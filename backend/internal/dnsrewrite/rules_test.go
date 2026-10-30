package dnsrewrite

import (
	"reflect"
	"testing"
)

func profile(rw map[string]any) map[string]any {
	return map[string]any{"dns": map[string]any{"rewrite": rw}}
}

func TestRulesFromProfile(t *testing.T) {
	got := RulesFromProfile(profile(map[string]any{"rules": []any{
		map[string]any{"source": "Services.GoogleAPIs.cn.", "domain": "services.googleapis.com"},
		map[string]any{"source": "*.a.com", "addresses": []any{"1.2.3.4", " ::1 ", 5, ""}},
		map[string]any{"source": "off.com", "domain": "x.com", "enabled": false},
		map[string]any{"source": "both.com", "domain": "x.com", "addresses": []any{"9.9.9.9"}},
		map[string]any{"source": "empty.com"},
		map[string]any{"domain": "nosrc.com"},
		"garbage",
	}}))
	want := []Rule{
		{Source: "services.googleapis.cn", Domain: "services.googleapis.com"},
		{Source: "*.a.com", Addresses: []string{"1.2.3.4", "::1"}},
		{Source: "both.com", Domain: "x.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if RulesFromProfile(profile(map[string]any{"enabled": false, "rules": []any{map[string]any{"source": "a.com", "domain": "b.com"}}})) != nil {
		t.Fatal("总开关关了不该有规则")
	}
	if RulesFromProfile(map[string]any{}) != nil {
		t.Fatal("没有 dns 段不该有规则")
	}
}

func TestMatch(t *testing.T) {
	rules := []Rule{
		{Source: "*.a.com", Addresses: []string{"1.1.1.1"}},
		{Source: "*.b.a.com", Domain: "t.com"},
		{Source: "x.b.a.com", Addresses: []string{"2.2.2.2"}},
	}
	cases := map[string]string{
		"x.b.a.com": "x.b.a.com", // 精确优先
		"y.b.a.com": "*.b.a.com", // 更具体的泛域名优先
		"z.a.com.":  "*.a.com",   // 末尾的点
		"Z.A.COM":   "*.a.com",   // 大小写
		"a.com":     "",          // *.a.com 只管子域,和配置里的 .a.com 后缀一致
		"other.com": "", "nota.com": "", "xa.com": "",
	}
	for d, want := range cases {
		r := Match(rules, d)
		if (want == "") != (r == nil) || (r != nil && r.Source != want) {
			t.Errorf("%s: %+v", d, r)
		}
	}
}
