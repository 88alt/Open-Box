package dnsfilter

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseLineTable(t *testing.T) {
	type want struct {
		typ, val, act string
		imp           bool
	}
	cases := []struct {
		line string
		ents []want // nil + ok=false 表示不支持
		ok   bool
	}{
		{"||example.org^", []want{{"domain_suffix", "example.org", "block", false}}, true},
		{"||EXAMPLE.org^", []want{{"domain_suffix", "example.org", "block", false}}, true},
		{"|example.org^", []want{{"domain", "example.org", "block", false}}, true},
		{"@@||example.org^", []want{{"domain_suffix", "example.org", "allow", false}}, true},
		{"||example.org^$important", []want{{"domain_suffix", "example.org", "block", true}}, true},
		{"@@||example.org^$important", []want{{"domain_suffix", "example.org", "allow", true}}, true},
		{"/^ad[0-9]+\\.example\\.com$/", []want{{"domain_regex", "^ad[0-9]+\\.example\\.com$", "block", false}}, true},
		{"0.0.0.0 ads.example.com", []want{{"domain", "ads.example.com", "block", false}}, true},
		{"127.0.0.1 a.example.com b.example.com # 注释", []want{{"domain", "a.example.com", "block", false}, {"domain", "b.example.com", "block", false}}, true},
		{":: ip6.example.com", []want{{"domain", "ip6.example.com", "block", false}}, true},
		{"ads.example.com", []want{{"domain", "ads.example.com", "block", false}}, true},
		{"ADS.Example.com.", []want{{"domain", "ads.example.com", "block", false}}, true},
		// 不支持
		{"||example.org^$dnsrewrite=1.2.3.4", nil, false},
		{"||example.org^$client=1.2.3.4", nil, false},
		{"||example.org^$important,dnstype=A", nil, false},
		{"||example.org^$badfilter", nil, false},
		{"||example.org", nil, false},       // 没有 ^
		{"||example.org/path^", nil, false}, // 带路径
		{"||*.example.org^", nil, false},    // 通配符
		{"||ex*.org^", nil, false},
		{"||1.2.3.4^", nil, false},   // IP
		{"/[unclosed/", nil, false},  // 坏正则
		{"##.ad-banner", nil, false}, // 页面元素规则
		{"example.org##.ad", nil, false},
		{"@@ads.example.com", nil, false}, // @@ 后不是 || 或 |
		{"localhost", nil, false},         // 单段名
		{"-bad.example.com", nil, false},
		{"0.0.0.0 -bad.example.com", nil, false},
	}
	for _, c := range cases {
		ents, ok := parseLine(c.line)
		if ok != c.ok {
			t.Errorf("%q:ok=%v,想要 %v", c.line, ok, c.ok)
			continue
		}
		var got []want
		for _, e := range ents {
			if e.Rule != c.line {
				t.Errorf("%q:rule 应是原行,得到 %q", c.line, e.Rule)
			}
			if e.Conditional {
				t.Errorf("%q:目前不会有 conditional", c.line)
			}
			got = append(got, want{e.Type, e.Value, e.Action, e.Important})
		}
		if !reflect.DeepEqual(got, c.ents) {
			t.Errorf("%q:\n得到 %v\n想要 %v", c.line, got, c.ents)
		}
	}
}

func TestParseListMixed(t *testing.T) {
	body := "\ufeff[Adblock Plus 2.0]\n! Title: test\n# hosts 注释\n\n" +
		"127.0.0.1 localhost\n::1 localhost ip6-localhost\n0.0.0.0 0.0.0.0\n" +
		"0.0.0.0 ads.example.com\n||tracker.example.net^\n@@||ok.example.net^\n" +
		"||tracker.example.net^\n" + // 重复
		"example.org##.banner\n||bad.example.org^$dnsrewrite=NOERROR;A;1.1.1.1\n"
	p, err := ParseList([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 3 {
		t.Fatalf("条目 %d,想要 3:%+v", len(p.Entries), p.Entries)
	}
	if p.Unsupported != 2 || len(p.UnsupportedExamples) != 2 || p.RuleCount() != 5 {
		t.Fatalf("不支持 %d 样例 %v 规则数 %d", p.Unsupported, p.UnsupportedExamples, p.RuleCount())
	}
	if p.UnsupportedExamples[0] != "example.org##.banner" {
		t.Fatal(p.UnsupportedExamples)
	}
}

func TestParseListErrors(t *testing.T) {
	for name, body := range map[string]string{"空": "", "只有注释": "! a\n# b\n[x]\n", "只有 localhost": "127.0.0.1 localhost\n"} {
		if _, err := ParseList([]byte(body)); !errors.Is(err, ErrNoRules) {
			t.Errorf("%s:%v", name, err)
		}
	}
	if _, err := ParseList([]byte("abc\x00def")); !errors.Is(err, ErrBinary) {
		t.Errorf("二进制:%v", err)
	}
	if p, err := ParseList([]byte(strings.Repeat("a", 10000))); err != nil || p.Unsupported != 1 {
		t.Errorf("一般的超长行按不支持算:%v %+v", err, p)
	}
	if _, err := ParseList([]byte(strings.Repeat("a", 100<<10))); err == nil {
		t.Error("远超上限的行应报错")
	}
	// 全是不支持的规则:不报错,让预览能显示「跳过了多少条」
	p, err := ParseList([]byte("##.a\n##.b\n"))
	if err != nil || len(p.Entries) != 0 || p.Unsupported != 2 {
		t.Errorf("%v %+v", err, p)
	}
}

func TestParseListExampleCapAndClip(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("##.x" + strings.Repeat("y", 200) + "\n")
	}
	p, err := ParseList([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if p.Unsupported != 20 || len(p.UnsupportedExamples) != MaxUnsupportedExamples {
		t.Fatalf("%d %d", p.Unsupported, len(p.UnsupportedExamples))
	}
	if n := len([]rune(p.UnsupportedExamples[0])); n > maxExampleLen+1 {
		t.Fatalf("样例没截断:%d", n)
	}
}

func TestParseListTooMany(t *testing.T) {
	var b strings.Builder
	for i := 0; i <= MaxEntries; i++ {
		b.WriteString("h")
		b.WriteString(strings.Repeat("a", i%7))
		b.WriteString(itoa(i))
		b.WriteString(".example.com\n")
	}
	if _, err := ParseList([]byte(b.String())); !errors.Is(err, ErrTooMany) {
		t.Fatalf("%v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}
