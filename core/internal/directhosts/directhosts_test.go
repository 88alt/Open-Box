package directhosts

import (
	"reflect"
	"strings"
	"testing"
)

// 自写测试向量:主机的取法按 WHATWG URL 的 hostname 规则(括号保留、IPv4 简写转点分十进制、
// 小写化)与「订阅和节点站点直连」的语义(域名进 domains,IP 进 /32 与 /128)人工核对,
// 不依赖 engine 基线或官方 openbox-core 输出。

type M = map[string]any

func node(server any) any { return M{"server": server} }

func collect(t *testing.T, nodes, subs any, domains, cidrs []string) {
	t.Helper()
	got := Collect(nodes, subs)
	if got.Domains == nil || got.Cidrs == nil {
		t.Fatalf("切片必须非 nil(序列化成 [] 而不是 null)")
	}
	if !reflect.DeepEqual(got, Hosts{Domains: domains, Cidrs: cidrs}) {
		t.Errorf("got %+v\nwant domains=%v cidrs=%v", got, domains, cidrs)
	}
}

func TestCollectEmptyAndInvalid(t *testing.T) {
	for _, in := range []any{nil, "x", float64(1), M{}} {
		collect(t, in, in, []string{}, []string{})
	}
	collect(t, []any{}, []any{}, []string{}, []string{})
}

func TestCollectNodeServers(t *testing.T) {
	nodes := []any{
		node(" Example.COM "), // 去空白、小写
		node("a.b-c.org"),
		node("1.2.3.4"),
		node("[2001:DB8::1]"), // 去方括号、小写
		node("::1"),
		node("::ffff:1.2.3.4"),
		node("localhost"), // 本机名不收
		node("LOCALHOST"),
		node("nodot"),     // 没有点的不是域名
		node("a_b.com"),   // 下划线不在域名字符集里
		node("中文.com"),    // 非 ASCII 不收
		node("a.com:443"), // 带端口的不收
		node("a b.com"),   // 含空格
		node(""),          // 空
		node("   "),
		node(nil),
		node(false),
		node(float64(0)),
		M{}, // 没有 server 键
		nil, // 空项
		"not-an-object",
		node("example.com"), // 重复的只留第一次
		node("10.0.0.1"),
		node("10.0.0.1"),
	}
	collect(t, nodes, nil,
		[]string{"example.com", "a.b-c.org"},
		[]string{"1.2.3.4/32", "2001:db8::1/128", "::1/128", "::ffff:1.2.3.4/128", "10.0.0.1/32"})
}

func TestCollectHexLikeNames(t *testing.T) {
	// 纯十六进制字符不带冒号不是 IPv6:没有点的丢弃,有点的当域名
	collect(t, []any{node("deadbeef"), node("abc.def"), node("fe80::1")}, nil,
		[]string{"abc.def"}, []string{"fe80::1/128"})
}

func TestCollectSubscriptionURLs(t *testing.T) {
	subs := []any{
		M{"url": "https://Sub.Example.com/path?x=1#f"},
		M{"url": "http://1.2.3.4:8080/x"},
		M{"url": "http://[2001:db8::2]:9000/"},
		M{"url": "http://0x7f.1/"},         // IPv4 简写按 WHATWG 转成 127.0.0.1
		M{"url": "http://u:p@auth.com/"},   // 用户信息不算主机
		M{"url": "  https://trim.com/a  "}, // 先去首尾空白
		M{"url": "http://localhost:8080/"},
		M{"url": "not a url"},
		M{"url": "no-scheme.com/path"},
		M{"url": ""},
		M{"url": nil},
		M{"url": float64(5)},
		M{},
		nil,
	}
	collect(t, nil, subs,
		[]string{"sub.example.com", "auth.com", "trim.com"},
		[]string{"1.2.3.4/32", "2001:db8::2/128", "127.0.0.1/32"})
}

func TestCollectMultipleURLs(t *testing.T) {
	subs := []any{
		// urls 非空时以 urls 为准,忽略 url
		M{"url": "https://old.com/", "urls": []any{"https://new1.com/", "https://new2.com/"}},
		// urls 为空列表时回落到 url
		M{"url": "https://fallback.com/", "urls": []any{}},
		// urls 不是数组时回落到 url
		M{"url": "https://fallback2.com/", "urls": "https://ignored.com/"},
		// urls 里的非字符串、空串被跳过
		M{"urls": []any{float64(1), nil, "", "  ", "https://ok.com/"}},
	}
	collect(t, nil, subs, []string{"new1.com", "new2.com", "fallback.com", "fallback2.com", "ok.com"}, []string{})
}

func TestCollectOrderAndDedupeAcrossSources(t *testing.T) {
	nodes := []any{node("shared.com"), node("9.9.9.9"), node("n-only.com")}
	subs := []any{
		M{"url": "https://s-only.com/"},
		M{"url": "https://SHARED.com/x"},
		M{"url": "http://9.9.9.9/"},
	}
	// 节点在前、订阅在后;同一个主机只留第一次
	collect(t, nodes, subs, []string{"shared.com", "n-only.com", "s-only.com"}, []string{"9.9.9.9/32"})
}

func TestIsSharedCdnCidr(t *testing.T) {
	yes := []string{
		"104.16.1.1/32", "104.16.0.0/13", "162.158.5.0/24", "173.245.48.0/20",
		"104.0.0.0/8", // 覆盖了共用段:有重叠就算
		"2606:4700::1/128", "2606:4700::/32", "2400:cb00:1::/48",
	}
	no := []string{"8.8.8.8/32", "1.1.1.1/32", "192.0.2.255/32", "103.21.248.0/22", "2001:db8::/32", "::1/128", "10.0.0.0/8"}
	for _, c := range yes {
		if !IsSharedCdnCidr(c) {
			t.Errorf("IsSharedCdnCidr(%q) = false, want true", c)
		}
	}
	for _, c := range no {
		if IsSharedCdnCidr(c) {
			t.Errorf("IsSharedCdnCidr(%q) = true, want false", c)
		}
	}
}

func TestSharedCdnCidrsTable(t *testing.T) {
	v4, v6 := 0, 0
	for _, c := range SharedCdnCidrs {
		if strings.Contains(c, ":") {
			v6++
		} else {
			v4++
		}
		if !IsSharedCdnCidr(c) {
			t.Errorf("表内的 %q 自己都不算共用段", c)
		}
	}
	if v4 != 15 || v6 != 7 {
		t.Errorf("共用段数量 v4=%d v6=%d,want 15 / 7", v4, v6)
	}
}

func TestNodeDirectNamesAndRuleSets(t *testing.T) {
	if NodeDirectDomainTag != "obnode-direct" || NodeDirectIPTag != "obnode-direct-ip" {
		t.Errorf("tag 常量不对")
	}
	if NodeDirectPlaceholderDomain != "obnode-direct.invalid" || NodeDirectPlaceholderCidr != "192.0.2.255/32" {
		t.Errorf("占位常量不对")
	}
	if got := NodeDirectFile("/data/rs", "obnode-direct"); got != "/data/rs/obnode-direct.json" {
		t.Errorf("NodeDirectFile = %q", got)
	}
	want := []map[string]any{
		{"type": "local", "tag": "obnode-direct", "format": "source", "path": "/x/obnode-direct.json"},
		{"type": "local", "tag": "obnode-direct-ip", "format": "source", "path": "/x/obnode-direct-ip.json"},
	}
	if got := NodeDirectRuleSets("/x"); !reflect.DeepEqual(got, want) {
		t.Errorf("NodeDirectRuleSets = %#v", got)
	}
}
