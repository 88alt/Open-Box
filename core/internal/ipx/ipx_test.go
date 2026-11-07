package ipx

import (
	"reflect"
	"testing"
)

// 自写测试向量:对照 WHATWG / Node net.isIPv4|isIPv6 语义与 CIDR 运算常识,
// 不依赖 engine 基线或官方 openbox-core 输出。

func TestIsIPv4(t *testing.T) {
	yes := []string{"0.0.0.0", "127.0.0.1", "192.168.1.1", "255.255.255.255", "1.2.3.4"}
	no := []string{"", "1", "1.2", "1.2.3", "1.2.3.4.5", "256.0.0.1", "1.2.3.256",
		"01.2.3.4", "1.02.3.4", "1.2.3.04", // 前导零 Node 不认
		"1.2.3.4/32", "::1", "2001:db8::1", " 1.2.3.4", "1.2.3.4 ",
		"1.2.3.-1", "a.b.c.d", "1.2.3.4.", ".1.2.3.4"}
	for _, s := range yes {
		if !IsIPv4(s) {
			t.Errorf("IsIPv4(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if IsIPv4(s) {
			t.Errorf("IsIPv4(%q) = true, want false", s)
		}
	}
}

func TestIsIPv6(t *testing.T) {
	yes := []string{
		"::", "::1", "2001:db8::1", "2001:db8:0:0:0:0:0:1",
		"fe80::1", "FE80::1", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
		"::ffff:1.2.3.4", "2001:db8::1.2.3.4",
		"fe80::1%eth0", "fe80::1%1", // zone id
	}
	no := []string{
		"", "1.2.3.4", ":::1", "2001:db8::1::2",
		"2001:db8:0:0:0:0:0:0:1", // 过多段
		"g001::1", "2001:db8::1/64", " 2001:db8::1",
	}
	for _, s := range yes {
		if !IsIPv6(s) {
			t.Errorf("IsIPv6(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if IsIPv6(s) {
			t.Errorf("IsIPv6(%q) = true, want false", s)
		}
	}
}

func TestIsIP(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"127.0.0.1", 4},
		{"::1", 6},
		{"2001:db8::1", 6},
		{"not-an-ip", 0},
		{"", 0},
		{"1.2.3.4.5", 0},
	}
	for _, c := range cases {
		if g := IsIP(c.in); g != c.want {
			t.Errorf("IsIP(%q)=%d want %d", c.in, g, c.want)
		}
	}
}

func TestCidrsOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"10.0.0.0/8", "10.1.0.0/16", true},
		{"10.0.0.0/8", "11.0.0.0/8", false},
		{"192.168.1.0/24", "192.168.1.128/25", true},
		{"192.168.1.0/25", "192.168.1.128/25", false},
		{"2001:db8::/32", "2001:db8:1::/48", true},
		{"2001:db8::/32", "2001:db9::/32", false},
		{"10.0.0.0/8", "2001:db8::/32", false}, // 跨族
		{"10.0.0.1", "10.0.0.1/32", true},      // 裸 IP 当 /32
		{"not-cidr", "10.0.0.0/8", false},
	}
	for _, c := range cases {
		if g := CidrsOverlap(c.a, c.b); g != c.want {
			t.Errorf("CidrsOverlap(%q,%q)=%v want %v", c.a, c.b, g, c.want)
		}
		// 对称
		if g := CidrsOverlap(c.b, c.a); g != c.want {
			t.Errorf("CidrsOverlap(%q,%q)=%v want %v (sym)", c.b, c.a, g, c.want)
		}
	}
}

func TestCidrContains(t *testing.T) {
	cases := []struct {
		outer, inner string
		want         bool
	}{
		{"10.0.0.0/8", "10.1.2.3/32", true},
		{"10.0.0.0/8", "10.0.0.0/8", true},
		{"10.0.0.0/8", "11.0.0.0/8", false},
		{"10.0.0.0/16", "10.0.0.0/8", false}, // 更大的网段不被包含
		{"2001:db8::/32", "2001:db8::1/128", true},
		{"10.0.0.0/8", "2001:db8::/128", false},
	}
	for _, c := range cases {
		if g := CidrContains(c.outer, c.inner); g != c.want {
			t.Errorf("CidrContains(%q,%q)=%v want %v", c.outer, c.inner, g, c.want)
		}
	}
}

func TestSubtractCidrs(t *testing.T) {
	// 从 10.0.0.0/8 挖掉 10.1.0.0/16,结果应不含 10.1.x,且覆盖其余
	got := SubtractCidrs([]any{"10.0.0.0/8"}, []any{"10.1.0.0/16"})
	if len(got) == 0 {
		t.Fatal("subtract empty")
	}
	for _, p := range got {
		if CidrsOverlap(p, "10.1.0.0/16") {
			t.Fatalf("result %v still overlaps hole", got)
		}
		if !CidrContains("10.0.0.0/8", p) {
			t.Fatalf("result %q not in original", p)
		}
	}
	// 挖掉全部
	got2 := SubtractCidrs([]any{"10.0.0.0/8"}, []any{"10.0.0.0/8"})
	if len(got2) != 0 {
		t.Fatalf("full subtract want empty, got %v", got2)
	}
	// 无交集:原样(可能规范化)
	got3 := SubtractCidrs([]any{"10.0.0.0/8"}, []any{"192.168.0.0/16"})
	if len(got3) == 0 {
		t.Fatal("no-op subtract emptied")
	}
	// 空挖空
	if g := SubtractCidrs([]any{}, []any{"10.0.0.0/8"}); !reflect.DeepEqual(g, []string{}) && len(g) != 0 {
		t.Fatalf("empty base: %v", g)
	}
}
