package routing

import (
	"strings"
	"testing"
)

// 自写测试向量:能安全写进 dnsmasq `server=/域名/` 的域名。规则见 dnsnames.go 注释;
// punycode 期望值取自公开的 IDN 例子(例え → xn--r8jz45g、bücher → xn--bcher-kva、é → xn--9ca),人工核对。

func TestJSLen(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{{"", 0}, {"abc", 3}, {"é", 1}, {"你好", 2}, {"😀", 2}, {"a😀b", 4}} {
		if got := jsLen(c.in); got != c.want {
			t.Errorf("jsLen(%q) = %d,想要 %d", c.in, got, c.want)
		}
	}
}

func TestDomainLabelOK(t *testing.T) {
	yes := []string{"a", "A", "0", "a-b", "a--b", "ABC-123", "xn--a", strings.Repeat("a", 63), strings.Repeat("Z", 63)}
	no := []string{"", "-", "-a", "a-", "-a-", "a_b", "a.b", "a b", "é", "a*", strings.Repeat("a", 64), "a\n"}
	for _, s := range yes {
		if !domainLabelOK(s) {
			t.Errorf("%q 应是合法标签", s)
		}
	}
	for _, s := range no {
		if domainLabelOK(s) {
			t.Errorf("%q 不该是合法标签", s)
		}
	}
}

func TestDnsmasqSafeDomainVectors(t *testing.T) {
	long := func(n int) string { // n 个字符、每段不超过 63 的合法域名
		var parts []string
		for n > 0 {
			l := 63
			if n < l {
				l = n
			}
			parts = append(parts, strings.Repeat("a", l))
			n -= l + 1
		}
		return strings.Join(parts, ".")
	}
	ok := []struct{ in, want string }{
		{"example.com", "example.com"},
		{"EXAMPLE.Com", "example.com"},
		{"  example.com  ", "example.com"},
		{"\u3000example.com\ufeff", "example.com"}, // JS trim 的空白
		{"*.example.com", "example.com"},
		{".example.com", "example.com"},
		{"example.com.", "example.com"},
		{"..a.com", "a.com"},
		{"a.com...", "a.com"},
		{"*..a.com", "a.com"}, // 去掉 *. 后再去前导点
		{"  *.Foo.Bar.  ", "foo.bar"},
		{"localhost", "localhost"},
		{"1.2.3.4", "1.2.3.4"}, // 只看字符集,数字串也算
		{"a-b.c-d.com", "a-b.c-d.com"},
		{"xn--r8jz45g.jp", "xn--r8jz45g.jp"},
		{"例え.jp", "xn--r8jz45g.jp"},
		{"*.例え.jp", "xn--r8jz45g.jp"},
		{"BÜCHER.de", "xn--bcher-kva.de"},
		{"É.com", "xn--9ca.com"},
		{strings.Repeat("a", 63) + ".com", strings.Repeat("a", 63) + ".com"},
		{long(253), long(253)},
	}
	for _, c := range ok {
		got, good := dnsmasqSafeDomain(c.in)
		if !good || got != c.want {
			t.Errorf("dnsmasqSafeDomain(%q) = (%q,%v),想要 %q", c.in, got, good, c.want)
		}
	}
	if len(long(253)) != 253 || len(long(254)) != 254 {
		t.Fatalf("测试数据长度不对:%d %d", len(long(253)), len(long(254)))
	}

	bad := []string{
		"", "   ", "*.", ".", "...", "*..", "*.*.", "\t\n",
		"*.*.a.com", // 只去一个 *.
		"a..b.com", "a.com..b",
		"-a.com", "a-.com", "a.-b.com", "a.b-.com",
		"a_b.com", "a b.com", "a/b.com", "a:b", "a@b.com", "a*b.com", "a,b.com", "a;b.com", "a\\b.com",
		"http://a.com", "example.com:80", "example.com/path", "user@example.com",
		"a\nb.com", "a\x00b.com", "a\x7fb.com",
		strings.Repeat("a", 64) + ".com",
		long(254),
		strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 63) + ".e",
		strings.Repeat("é", 254),               // 码元数超过 253
		strings.Repeat("😀", 127),               // 每个 2 个码元,共 254
		"例え" + strings.Repeat("a", 70) + ".jp", // 转出来的标签超过 63
	}
	for _, s := range bad {
		if got, good := dnsmasqSafeDomain(s); good {
			t.Errorf("dnsmasqSafeDomain(%q) = %q,应不可写", s, got)
		}
	}
}
