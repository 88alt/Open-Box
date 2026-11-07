package routing

import (
	"reflect"
	"testing"
	"unicode/utf16"
)

// 自写测试向量:geoRulesetAvailable / setAvailableGeoTags / normalizeNotes / sliceUTF16 / nilIfEmpty。
// geoTags 为 nil 时不过滤(测试/预览路径);有名单时只放行名单内的 geoip-/geosite- 前缀,其它 tag 一律放行。
// normalizeNotes 只收非空字符串键值、跳过 __proto__、值按 UTF-16 码元截到 60。

func TestGeoRulesetAvailable(t *testing.T) {
	// 无名单:全部可用
	setAvailableGeoTags(nil)
	for _, tag := range []string{"geoip-cn", "geosite-google", "custom", "geoip-private", ""} {
		if !geoRulesetAvailable(tag) {
			t.Errorf("nil 名单时 %q 应可用", tag)
		}
	}

	// 有名单
	tags := []string{"geoip-cn", "geosite-geolocation-cn", "geosite-category-ads-all"}
	setAvailableGeoTags(&tags)
	cases := []struct {
		tag  string
		want bool
	}{
		{"geoip-cn", true},
		{"geosite-geolocation-cn", true},
		{"geosite-category-ads-all", true},
		{"geoip-us", false},
		{"geosite-google", false},
		{"geoip-private", false},
		{"custom-rule", true}, // 非 geo 前缀
		{"rule-set-foo", true},
		{"", true},
		{"Geoip-cn", true}, // 正则只匹配小写前缀,大写不算 geo 前缀
		{"GEOIP-CN", true},
	}
	for _, c := range cases {
		if got := geoRulesetAvailable(c.tag); got != c.want {
			t.Errorf("geoRulesetAvailable(%q) = %v, 想要 %v", c.tag, got, c.want)
		}
	}

	// 空名单: geo 前缀全部不可用,非前缀可用
	empty := []string{}
	setAvailableGeoTags(&empty)
	if geoRulesetAvailable("geoip-cn") {
		t.Error("空名单时 geoip-cn 应不可用")
	}
	if !geoRulesetAvailable("custom") {
		t.Error("空名单时 custom 应可用")
	}

	// 恢复
	setAvailableGeoTags(nil)
}

func TestSliceUTF16(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 5, "hello"},
		{"hello", 3, "hel"},
		{"hello", 0, ""},
		{"hello", 10, "hello"},
		{"你好世界", 2, "你好"},
		{"你好世界", 4, "你好世界"},
		{"a😀b", 2, "a\uFFFD"}, // 😀 是两个 UTF-16 码元
		{"a😀b", 3, "a😀"},
		{"a😀b", 1, "a"},
		{"", 5, ""},
		// 截在代理对中间时,utf16.Decode 会产出替换符
		{"🚀", 1, "\uFFFD"},
		{"🚀", 2, "🚀"},
	}
	for _, c := range cases {
		if got := sliceUTF16(c.s, c.n); got != c.want {
			t.Errorf("sliceUTF16(%q, %d) = %q (%v), 想要 %q", c.s, c.n, got, []rune(got), c.want)
		}
	}
}

func TestNormalizeNotes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want map[string]string
	}{
		{"nil", nil, map[string]string{}},
		{"非对象", "x", map[string]string{}},
		{"空对象", map[string]any{}, map[string]string{}},
		{"正常", map[string]any{"domain:example.com": "备注一", "ip:1.2.3.4": "备注二"}, map[string]string{"domain:example.com": "备注一", "ip:1.2.3.4": "备注二"}},
		{"跳过空键空值", map[string]any{"": "a", "k": "", "  ": "b", "ok": "  c  "}, map[string]string{"ok": "c"}},
		{"跳过 __proto__", map[string]any{"__proto__": "bad", "good": "yes"}, map[string]string{"good": "yes"}},
		{"非字符串值丢掉", map[string]any{"k": 1, "m": true, "ok": "v"}, map[string]string{"ok": "v"}},
	}
	for _, c := range cases {
		got := normalizeNotes(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: normalizeNotes = %#v, 想要 %#v", c.name, got, c.want)
		}
	}

	// 超长按 UTF-16 码元截到 60
	longRunes := make([]rune, 70)
	for i := range longRunes {
		longRunes[i] = 'x'
	}
	long := string(longRunes)
	got := normalizeNotes(map[string]any{"k": long})
	want := string(longRunes[:60])
	if got["k"] != want {
		t.Errorf("超长截断: got len=%d, want 60", len([]rune(got["k"])))
	}

	// emoji 超长
	emojiLong := ""
	for i := 0; i < 40; i++ {
		emojiLong += "🚀"
	}
	got = normalizeNotes(map[string]any{"k": emojiLong})
	u := utf16.Encode([]rune(got["k"]))
	if len(u) > 60 {
		t.Errorf("emoji 截断后 UTF-16 码元数 %d > 60", len(u))
	}
}

func TestNilIfEmpty(t *testing.T) {
	if got := nilIfEmpty(nil); got != nil {
		t.Errorf("nilIfEmpty(nil) = %v, 想要 nil", got)
	}
	if got := nilIfEmpty([]string{}); got != nil {
		t.Errorf("nilIfEmpty([]) = %v, 想要 nil", got)
	}
	in := []string{"a"}
	if got := nilIfEmpty(in); !reflect.DeepEqual(got, in) {
		t.Errorf("nilIfEmpty([a]) = %v, 想要 [a]", got)
	}
}
