package routing

import (
	"math"
	"reflect"
	"testing"
)

// 自写测试向量:jsval.go 里对 JS 动态语义的小层。期望值按 JS 的规则人工推算:
//   - trim / \s 用的空白集:制表、换行、垂直制表、换页、回车、空格、U+00A0、U+1680、U+2000..U+200A、U+2028、U+2029、
//     U+202F、U+205F、U+3000、U+FEFF;不算的有 U+0085、U+180E、U+200B、U+2060 等;
//   - isNonEmptyString:是字符串且 trim 后非空,取 trim 后的值;
//   - strList:数组里的非空字符串逐个 trim,其余(数字、null、数组、空白串)丢掉,不去重,结果永不为 nil;
//   - v !== false:只有恰好是布尔 false 才为假;
//   - Number.isInteger:只收数字类型(字符串 "3" 不算),NaN / ±Infinity / 小数不算,-0 记成 0;
//   - customIconScale:数组 / 对象不转(当 0),其余先 Number() 再要求是整数且在 int64 范围内,否则 0,-0 也是 0。

func TestIsJSSpace(t *testing.T) {
	yes := []rune{'\t', '\n', '\v', '\f', '\r', ' ', 0x00A0, 0x1680, 0x2000, 0x2005, 0x200A, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF}
	no := []rune{0, 'a', '0', 0x0085, 0x00A1, 0x180E, 0x1FFF, 0x200B, 0x200C, 0x2010, 0x202E, 0x2060, 0x3001, 0xFEFE, 0xFF00, 0x1F600}
	for _, r := range yes {
		if !isJSSpace(r) {
			t.Errorf("U+%04X 应是空白", r)
		}
	}
	for _, r := range no {
		if isJSSpace(r) {
			t.Errorf("U+%04X 不应是空白", r)
		}
	}
}

func TestJSTrim(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{" a ", "a"},
		{"\u3000 a \ufeff", "a"},
		{"\u00a0x\u00a0", "x"},
		{"\u2003\u2028a\u2029\u202f", "a"},
		{"a b", "a b"},
		{" a  b ", "a  b"},
		{"\u200b a ", "\u200b a"},
		{"\u0085a\u0085", "\u0085a\u0085"},
		{"\u180ea\u180e", "\u180ea\u180e"},
		{"中 文", "中 文"},
	} {
		if got := jsTrim(c.in); got != c.want {
			t.Errorf("jsTrim(%q)=%q 要 %q", c.in, got, c.want)
		}
	}
}

func TestGetAsObjectAsArray(t *testing.T) {
	obj := map[string]any{"a": 1.0, "n": nil, "s": "x"}
	if got := get(obj, "a"); got != 1.0 {
		t.Errorf("get a=%v", got)
	}
	if got := get(obj, "n"); got != nil {
		t.Errorf("get n=%v", got)
	}
	for _, in := range []any{obj, nil, "a", 5.0, []any{1.0}, true} {
		if got := get(in, "missing"); got != nil {
			t.Errorf("get(%v, missing)=%v 要 nil", in, got)
		}
	}
	for _, in := range []any{nil, "a", 5.0, []any{"a"}, true} {
		if got := get(in, "a"); got != nil {
			t.Errorf("非对象 get(%v)=%v 要 nil", in, got)
		}
	}
	if m, ok := asObject(obj); !ok || len(m) != 3 {
		t.Error("asObject 对象")
	}
	for _, in := range []any{nil, []any{}, "x", 1.0, true} {
		if _, ok := asObject(in); ok {
			t.Errorf("asObject(%v) 不该成功", in)
		}
	}
	if a, ok := asArray([]any{1.0, "x"}); !ok || len(a) != 2 {
		t.Error("asArray 数组")
	}
	if a, ok := asArray([]any{}); !ok || len(a) != 0 {
		t.Error("asArray 空数组")
	}
	for _, in := range []any{nil, map[string]any{}, "x", 1.0, true} {
		if _, ok := asArray(in); ok {
			t.Errorf("asArray(%v) 不该成功", in)
		}
	}
}

func TestNonEmptyAndStrList(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
		ok   bool
	}{
		{"a", "a", true}, {" a ", "a", true}, {"\u00a0x\u00a0", "x", true}, {"a b", "a b", true},
		{"", "", false}, {"  ", "", false}, {"\u3000", "", false}, {"\u200b", "\u200b", true},
		{5.0, "", false}, {nil, "", false}, {true, "", false}, {[]any{"a"}, "", false}, {map[string]any{"a": "b"}, "", false},
	} {
		got, ok := nonEmpty(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("nonEmpty(%v)=%q %v 要 %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
	for _, c := range []struct {
		in   any
		want []string
	}{
		{[]any{"a", " b ", "", 5.0, nil, []any{"c"}, "  ", true, map[string]any{}}, []string{"a", "b"}},
		{[]any{"b", "a", "b"}, []string{"b", "a", "b"}},
		{[]any{}, []string{}},
		{nil, []string{}},
		{"abc", []string{}},
		{map[string]any{"a": "b"}, []string{}},
		{5.0, []string{}},
		{[]any{"\u3000x\u3000", "y"}, []string{"x", "y"}},
	} {
		got := strList(c.in)
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("strList(%v)=%#v 要 %#v(且非 nil)", c.in, got, c.want)
		}
	}
}

func TestNotFalseIsIntegerInList(t *testing.T) {
	for _, c := range []struct {
		in   any
		want bool
	}{
		{false, false}, {true, true}, {nil, true}, {0.0, true}, {"", true}, {"false", true}, {[]any{}, true}, {map[string]any{}, true},
	} {
		if got := notFalse(c.in); got != c.want {
			t.Errorf("notFalse(%v)=%v 要 %v", c.in, got, c.want)
		}
	}
	for _, c := range []struct {
		in   any
		want float64
		ok   bool
	}{
		{3.0, 3, true}, {-3.0, -3, true}, {0.0, 0, true}, {1e300, 1e300, true}, {9007199254740992.0, 9007199254740992, true},
		{1.5, 0, false}, {-0.5, 0, false}, {math.NaN(), 0, false}, {math.Inf(1), 0, false}, {math.Inf(-1), 0, false},
		{"3", 0, false}, {nil, 0, false}, {true, 0, false}, {[]any{3.0}, 0, false}, {3, 0, false},
	} {
		got, ok := isInteger(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("isInteger(%v)=%v %v 要 %v %v", c.in, got, ok, c.want, c.ok)
		}
	}
	// -0 记成 0(JSON.stringify(-0) 是 "0")
	if got, ok := isInteger(math.Copysign(0, -1)); !ok || math.Signbit(got) {
		t.Errorf("isInteger(-0)=%v %v 要正零", got, ok)
	}
	list := []string{"a", "", "B"}
	for s, want := range map[string]bool{"a": true, "": true, "B": true, "b": false, "A": false, "ab": false, "c": false} {
		if got := inList(s, list); got != want {
			t.Errorf("inList(%q)=%v 要 %v", s, got, want)
		}
	}
	if inList("a", nil) || inList("", []string{}) {
		t.Error("空名单不含任何项")
	}
}

func TestDedupeToAnyIdx(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"b", "a", "b", "c", "a"}, []string{"b", "a", "c"}},
		{[]string{"a", "a", "a"}, []string{"a"}},
		{[]string{"a", "A", ""}, []string{"a", "A", ""}},
		{[]string{"", "", "x"}, []string{"", "x"}},
		{[]string{}, []string{}},
		{nil, []string{}},
	} {
		got := dedupe(c.in)
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("dedupe(%v)=%#v 要 %#v(且非 nil)", c.in, got, c.want)
		}
	}
	if got := toAny([]string{"b", "a"}); !reflect.DeepEqual(got, []any{"b", "a"}) {
		t.Errorf("toAny=%#v", got)
	}
	if got := toAny(nil); got == nil || len(got) != 0 {
		t.Errorf("toAny(nil)=%#v 要非 nil 的空切片", got)
	}
	if got := idx("policy-%d", 3); got != "policy-3" {
		t.Errorf("idx=%q", got)
	}
	if got := idx("策略-%d", 0); got != "策略-0" {
		t.Errorf("idx=%q", got)
	}
}

func TestCustomIconScale(t *testing.T) {
	for _, c := range []struct {
		name string
		in   any
		want float64
	}{
		{"整数", 2.0, 2}, {"负整数", -3.0, -3}, {"零", 0.0, 0}, {"负零", math.Copysign(0, -1), 0},
		{"小数", 1.5, 0}, {"负小数", -2.5, 0}, {"真", true, 1}, {"假", false, 0}, {"null", nil, 0},
		{"数字串", "2", 2}, {"带空白的数字串", " 3 ", 3}, {"十六进制串", "0x10", 16}, {"指数串", "1e2", 100}, {"小数点零串", "1.0", 1},
		{"小数串", "2.5", 0}, {"负零串", "-0", 0}, {"非数字串", "abc", 0}, {"空串", "", 0}, {"空白串", "  ", 0},
		{"无穷串", "Infinity", 0}, {"溢出串", "1e400", 0},
		{"空数组不转", []any{}, 0}, {"单元素数组不转", []any{5.0}, 0}, {"数字串数组不转", []any{"5"}, 0}, {"对象不转", map[string]any{}, 0},
		{"2 的 53 次方", 9007199254740992.0, 9007199254740992}, {"1e18", 1e18, 1e18},
		{"最小 int64", -9223372036854775808.0, -9223372036854775808},
		{"2 的 63 次方越界", 9223372036854775808.0, 0}, {"更大越界", 9.3e18, 0}, {"比最小 int64 还小", -9.3e18, 0},
		{"NaN", math.NaN(), 0}, {"正无穷", math.Inf(1), 0}, {"负无穷", math.Inf(-1), 0},
	} {
		got := customIconScale(c.in)
		if got != c.want || (c.want == 0 && math.Signbit(got)) {
			t.Errorf("%s: customIconScale(%v)=%v 要 %v", c.name, c.in, got, c.want)
		}
	}
}
