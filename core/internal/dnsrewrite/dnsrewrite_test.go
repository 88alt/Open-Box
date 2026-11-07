package dnsrewrite

import (
	"reflect"
	"strings"
	"testing"
)

// 自写测试向量:输入自己设计,期望值按 DNS 主机名规则(RFC 1035/1123 的标签与长度上限)
// 与本包注释写明的语义人工核对,不依赖 engine 基线或官方 openbox-core 输出。

func TestNormalizeDomain(t *testing.T) {
	l63 := strings.Repeat("a", 63)
	l61 := strings.Repeat("b", 61)
	d253 := l63 + "." + l63 + "." + l63 + "." + l61 // 63*3 + 61 + 3 个点 = 253
	d254 := l63 + "." + l63 + "." + l63 + "." + l61 + "c"
	cases := []struct {
		in   any
		wild bool
		want string
	}{
		{"Example.COM", false, "example.com"},
		{"  a.b. ", false, "a.b"},
		{"a.b..", false, "a.b"},
		{"a_b.com", false, "a_b.com"},
		{"1-2.com", false, "1-2.com"},
		{"*.Foo.com", true, "*.foo.com"},
		{"*.Foo.com", false, ""},
		{"*.", true, ""},
		{"*.*.a.com", true, ""},
		{"a*b.com", true, ""},
		{"-a.com", false, ""},
		{"a-.com", false, ""},
		{"a..com", false, ""},
		{".a.com", false, ""},
		{"中文.com", false, ""},
		{"a b.com", false, ""},
		{"", false, ""},
		{"   ", false, ""},
		{nil, false, ""},
		{l63 + ".com", false, l63 + ".com"},
		{l63 + "a.com", false, ""},
		{d253, false, d253},
		{d254, false, ""},
	}
	for _, c := range cases {
		if got := NormalizeDomain(c.in, c.wild); got != c.want {
			t.Errorf("NormalizeDomain(%q, %v) = %q, want %q", c.in, c.wild, got, c.want)
		}
	}
}

func TestNormalizeDnsRewriteDefaults(t *testing.T) {
	want := Rewrite{Enabled: true, Initialized: false, Rules: []Rule{}}
	for _, in := range []any{
		nil,
		map[string]any{},
		map[string]any{"rewrite": nil},
		map[string]any{"rewrite": "oops"},
		map[string]any{"rewrite": float64(3)},
		map[string]any{"rewrite": map[string]any{"rules": "x"}},
	} {
		got := NormalizeDnsRewrite(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("NormalizeDnsRewrite(%v) = %+v, want %+v", in, got, want)
		}
		if got.Rules == nil {
			t.Errorf("Rules 必须是非 nil 切片(序列化成 [] 而不是 null)")
		}
	}
}

func TestNormalizeDnsRewriteFlags(t *testing.T) {
	mk := func(rw map[string]any) Rewrite {
		return NormalizeDnsRewrite(map[string]any{"rewrite": rw})
	}
	if mk(map[string]any{"enabled": false}).Enabled {
		t.Errorf("enabled=false 应关闭")
	}
	for _, v := range []any{"false", float64(0), nil, "no"} {
		if !mk(map[string]any{"enabled": v}).Enabled {
			t.Errorf("enabled=%v 只有布尔 false 才关闭,应保持开启", v)
		}
	}
	for _, v := range []any{true, float64(1), "x"} {
		if !mk(map[string]any{"initialized": v}).Initialized {
			t.Errorf("initialized=%v 应为真", v)
		}
	}
	for _, v := range []any{false, float64(0), "", nil} {
		if mk(map[string]any{"initialized": v}).Initialized {
			t.Errorf("initialized=%v 应为假", v)
		}
	}
}

func TestNormalizeDnsRewriteRules(t *testing.T) {
	in := map[string]any{"rewrite": map[string]any{"rules": []any{
		map[string]any{"source": "bad..name", "domain": "x.com"},                                                                                             // 0 源非法,丢弃
		map[string]any{"source": "A.com", "domain": "B.com", "addresses": []any{"1.1.1.1"}},                                                                  // 1 域名型压过 IP 型
		map[string]any{"source": "*.Lan", "addresses": []any{"1.2.3.4", "1.2.3.4", "::1", "FE80::1", "bad", float64(5)}, "enabled": false, "note": "  hi  "}, // 2
		map[string]any{"source": "c.com"},                                                              // 3 没有目标,丢弃
		map[string]any{"source": "d.com", "domain": "*.e.com"},                                         // 4 目标不许泛域名,丢弃
		map[string]any{"id": "mine", "source": "f.com", "addresses": []any{"::2"}, "note": float64(7)}, // 5
		map[string]any{"id": "", "source": "g.com", "domain": "h.com"},                                 // 6 空 id 回落到序号
		"not-an-object", // 7
	}}}
	want := []Rule{
		{ID: "rw-2", Enabled: true, Source: "a.com", Domain: "b.com", Addresses: []string{}, Note: ""},
		{ID: "rw-3", Enabled: false, Source: "*.lan", Domain: "", Addresses: []string{"1.2.3.4", "::1", "fe80::1"}, Note: "hi"},
		{ID: "mine", Enabled: true, Source: "f.com", Domain: "", Addresses: []string{"::2"}, Note: ""},
		{ID: "rw-7", Enabled: true, Source: "g.com", Domain: "h.com", Addresses: []string{}, Note: ""},
	}
	got := NormalizeDnsRewrite(in).Rules
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rules =\n%+v\nwant\n%+v", got, want)
	}
}

func TestNormalizeNoteLimit(t *testing.T) {
	raw := func(note string) any {
		return map[string]any{"source": "a.com", "domain": "b.com", "note": note}
	}
	n200 := strings.Repeat("x", 200)
	if got := NormalizeRuleForTest(raw(n200+"yyy"), 0).Note; got != n200 {
		t.Errorf("备注应截到 200 个 UTF-16 单位,得到 %d 字节", len(got))
	}
	if got := NormalizeRuleForTest(raw(strings.Repeat("字", 201)), 0).Note; got != strings.Repeat("字", 200) {
		t.Errorf("中文备注应截到 200 个字")
	}
	// 先 trim 再截:前面的空白不占额度
	if got := NormalizeRuleForTest(raw("   "+n200), 0).Note; got != n200 {
		t.Errorf("trim 应先于截断")
	}
}

func TestRewriteDnsRules(t *testing.T) {
	rules := []Rule{
		{Enabled: true, Source: "exact.com", Domain: "x.com"},
		{Enabled: true, Source: "*.wild.com", Addresses: []string{"1.1.1.1"}},
		{Enabled: false, Source: "off.com", Domain: "x.com"},
		{Enabled: true, Source: "", Domain: "x.com"},
		{Enabled: true, Source: "empty.com"},
		{Enabled: true, Source: "exact2.com", Addresses: []string{"::1"}},
		{Enabled: true, Source: "*.wild2.com", Domain: "y.com"},
	}
	want := []any{
		map[string]any{"domain": []any{"exact.com", "exact2.com"}, "server": "dns-rewrite"},
		map[string]any{"domain_suffix": []any{".wild.com", ".wild2.com"}, "server": "dns-rewrite"},
	}
	if got := RewriteDnsRules(rules); !reflect.DeepEqual(got, want) {
		t.Errorf("RewriteDnsRules = %#v, want %#v", got, want)
	}
	// 只有泛域名:只出 domain_suffix 一条
	only := RewriteDnsRules([]Rule{{Enabled: true, Source: "*.z.com", Domain: "y.com"}})
	if !reflect.DeepEqual(only, []any{map[string]any{"domain_suffix": []any{".z.com"}, "server": "dns-rewrite"}}) {
		t.Errorf("只有泛域名时 = %#v", only)
	}
	// 只有一条精确域名:只出 domain 一条,不带 domain_suffix
	one := RewriteDnsRules([]Rule{{Enabled: true, Source: "one.com", Domain: "y.com"}})
	if !reflect.DeepEqual(one, []any{map[string]any{"domain": []any{"one.com"}, "server": "dns-rewrite"}}) {
		t.Errorf("只有一条精确域名时 = %#v", one)
	}
	// 没有可用规则:空切片而不是 nil
	none := RewriteDnsRules([]Rule{{Enabled: false, Source: "a.com", Domain: "b.com"}})
	if none == nil || len(none) != 0 {
		t.Errorf("无可用规则应返回非 nil 空切片,得到 %#v", none)
	}
}

func TestRewriteDnsServer(t *testing.T) {
	want := map[string]any{"type": "udp", "tag": "dns-rewrite", "server": "127.0.0.1", "server_port": float64(7854)}
	if got := RewriteDnsServer(); !reflect.DeepEqual(got, want) {
		t.Errorf("RewriteDnsServer = %#v, want %#v", got, want)
	}
}
