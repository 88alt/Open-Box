package clientroutes

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 自写测试向量:期望值按 CIDR / MAC 的写法规则与本包注释写明的终端分流语义人工核对,
// 不依赖 engine 基线或官方 openbox-core 输出。

type M = map[string]any

func TestNormalizeCidr(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"192.168.1.0/24", "192.168.1.0/24"},
		{"10.0.0.1", "10.0.0.1/32"},
		{"  10.0.0.1  ", "10.0.0.1/32"},
		{"0.0.0.0/0", "0.0.0.0/0"},
		{"1.2.3.4/32", "1.2.3.4/32"},
		{"1.2.3.4/33", ""},
		{"1.2.3.4/08", "1.2.3.4/8"}, // 前导零的前缀长度归一成数字
		{"1.2.3.4/", ""},
		{"1.2.3.4/-1", ""},
		{"1.2.3.4/ 8", ""},
		{"1.2.3.4/8/8", ""},
		{"1.2.3.4/99999999999999999999", ""},
		{"256.1.1.1", ""},
		{"1.2.3", ""},
		{"::1", "::1/128"},
		{"2001:db8::/32", "2001:db8::/32"},
		{"::/128", "::/128"},
		{"::/129", ""},
		{"fe80::1%eth0", ""}, // 带 zone 的不当网段
		{"fe80::1%eth0/64", ""},
		{"abc", ""},
		{"", ""},
		{"   ", ""},
		{nil, ""},
		{false, ""},
		{float64(0), ""},
	}
	for _, c := range cases {
		if got := NormalizeCidr(c.in); got != c.want {
			t.Errorf("NormalizeCidr(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeMac(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff"},
		{"aa-bb-cc-dd-ee-ff", "aa:bb:cc:dd:ee:ff"},
		{"aa:bb-cc:dd-ee:ff", "aa:bb:cc:dd:ee:ff"},
		{"  01:23:45:67:89:ab ", "01:23:45:67:89:ab"},
		{"aabbccddeeff", ""},
		{"aa:bb:cc:dd:ee", ""},
		{"aa:bb:cc:dd:ee:ff:00", ""},
		{"gg:bb:cc:dd:ee:ff", ""},
		{"a:b:c:d:e:f", ""},
		{"aa:bb:cc:dd:ee:ff\n", "aa:bb:cc:dd:ee:ff"},
		{"", ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := NormalizeMac(c.in); got != c.want {
			t.Errorf("NormalizeMac(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNormalizeClientRoutesNonArray(t *testing.T) {
	for _, in := range []any{nil, "x", float64(1), M{}} {
		got := NormalizeClientRoutes(in, nil)
		if got == nil || len(got) != 0 {
			t.Errorf("%v: want 非 nil 空切片, got %#v", in, got)
		}
	}
}

func TestNormalizeClientRoutesIPAndMacShapes(t *testing.T) {
	list := []any{
		M{"id": "r1", "name": "phone", "match": "ip", "outbound": " proxy ",
			"sources": []any{"192.168.1.10", "192.168.1.10/32", "bad", "10.0.0.0/8", nil}},
		M{"id": "r2", "name": "tv", "match": "mac", "outbound": "direct",
			"macs": []any{"AA-BB-CC-DD-EE-FF", "aa:bb:cc:dd:ee:ff", "bad"}, "sources": []any{"1.1.1.1"}},
	}
	got := mustJSON(t, NormalizeClientRoutes(list, nil))
	want := `[{"id":"r1","name":"phone","match":"ip","outbound":"proxy","sources":["192.168.1.10/32","10.0.0.0/8"]},` +
		`{"id":"r2","name":"tv","match":"mac","outbound":"direct","sources":[],"macs":["aa:bb:cc:dd:ee:ff"]}]`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestNormalizeClientRoutesDropping(t *testing.T) {
	src := []any{"1.1.1.1"}
	list := []any{
		M{"id": "dis", "outbound": "p", "sources": src, "enabled": false}, // 关闭
		M{"id": "adm", "outbound": "p", "sources": src, "admit": true},    // 白名单不是分流规则
		nil, "str", float64(3), true, // 非对象
		M{"id": "noout", "sources": src},                                        // 没有出口
		M{"id": "emptyout", "outbound": "   ", "sources": src},                  // 空白出口
		M{"id": "numout", "outbound": float64(5), "sources": src},               // 出口不是字符串
		M{"id": "nosrc", "outbound": "p", "sources": []any{"bad"}},              // ip 模式没有合法网段
		M{"id": "nomac", "outbound": "p", "match": "mac", "macs": []any{"bad"}}, // mac 模式没有合法 MAC
		M{"id": "ok1", "outbound": "p", "sources": src, "enabled": "false"},     // 只有布尔 false 才算关闭
		M{"id": "ok2", "outbound": "p", "sources": src, "enabled": float64(0)},
		M{"id": "ok3", "outbound": "p", "sources": src, "admit": "true"}, // 只有布尔 true 才算 admit
	}
	var ids []string
	for _, r := range NormalizeClientRoutes(list, nil) {
		ids = append(ids, r.ID)
	}
	if want := []string{"ok1", "ok2", "ok3"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func TestNormalizeClientRoutesIDAndName(t *testing.T) {
	list := []any{
		M{"outbound": "p", "sources": []any{"1.1.1.1"}},
		M{"id": float64(5), "name": float64(0), "outbound": "p", "sources": []any{"1.1.1.1"}},
		M{"id": false, "name": nil, "outbound": "p", "sources": []any{"1.1.1.1"}},
	}
	got := NormalizeClientRoutes(list, nil)
	if len(got) != 3 || got[0].ID != "" || got[0].Name != "" || got[1].ID != "5" || got[1].Name != "" || got[2].ID != "" || got[2].Name != "" {
		t.Errorf("id/name 转换不对: %+v", got)
	}
}

func TestNormalizeClientRoutesBypassOutbound(t *testing.T) {
	mk := func(extra M) []any {
		r := M{"id": "b", "bypass": true, "sources": []any{"1.1.1.1"}}
		for k, v := range extra {
			r[k] = v
		}
		return []any{r}
	}
	outbound := func(list []any, opts any) string {
		got := NormalizeClientRoutes(list, opts)
		if len(got) != 1 {
			t.Fatalf("want 1 route, got %+v", got)
		}
		if !got[0].Bypass {
			t.Errorf("bypass 应为真")
		}
		return got[0].Outbound
	}
	if g := outbound(mk(nil), nil); g != "direct" {
		t.Errorf("没有任何出口信息:want direct, got %q", g)
	}
	if g := outbound(mk(M{"outbound": " x "}), nil); g != "x" {
		t.Errorf("没有 directTag 时用自带出口(去空白):got %q", g)
	}
	if g := outbound(mk(M{"outbound": "   "}), nil); g != "direct" {
		t.Errorf("空白出口回落 direct:got %q", g)
	}
	if g := outbound(mk(M{"outbound": "x"}), "直连"); g != "直连" {
		t.Errorf("旧传法(字符串)directTag 优先:got %q", g)
	}
	if g := outbound(mk(M{"outbound": "x"}), M{"directTag": "直连"}); g != "直连" {
		t.Errorf("map 传法 directTag 优先:got %q", g)
	}
	if g := outbound(mk(M{"outbound": "x"}), M{"directTag": ""}); g != "x" {
		t.Errorf("directTag 为空串当没有:got %q", g)
	}
	if g := outbound(mk(M{"outbound": "x"}), M{}); g != "x" {
		t.Errorf("map 里没有 directTag 当没有:got %q", g)
	}
	// bypass 只认布尔 true;字符串 "true" 不算,走普通规则(没有出口就丢弃)
	if got := NormalizeClientRoutes(mk(M{"bypass": "true"}), "直连"); len(got) != 0 {
		t.Errorf(`bypass:"true" 不算 bypass,没有出口应丢弃: %+v`, got)
	}
	// bypass 路由 JSON 里带 "bypass":true,普通路由不带
	js := mustJSON(t, NormalizeClientRoutes(mk(nil), nil))
	if !strings.Contains(js, `"bypass":true`) {
		t.Errorf("bypass 路由 JSON 应含 bypass:true: %s", js)
	}
}

func TestNormalizeClientRoutesInferMacForBypass(t *testing.T) {
	list := []any{M{"id": "old", "bypass": true, "macs": []any{"AA-BB-CC-DD-EE-FF"}}}
	got := NormalizeClientRoutes(list, "direct")
	if len(got) != 1 || got[0].Match != "mac" || !reflect.DeepEqual(got[0].Macs, []string{"aa:bb:cc:dd:ee:ff"}) || len(got[0].Sources) != 0 || got[0].Sources == nil {
		t.Errorf("老档案 bypass 带 macs 应算 mac: %+v", got)
	}
}

func TestNormalizeClientRoutesBindingsAlias(t *testing.T) {
	list := []any{M{"id": "r", "outbound": "p", "sources": []any{"192.168.1.10"}}}
	want := []string{"192.168.1.10/32", "fd00::10/128"}
	b := M{"192.168.1.10": []any{"fd00::10"}}
	if got := NormalizeClientRoutes(list, M{"clientBindings": b}); len(got) != 1 || !reflect.DeepEqual(got[0].Sources, want) {
		t.Errorf("clientBindings 键名: %+v", got)
	}
	// ipv6Bindings 为 null 时回落到 clientBindings
	if got := NormalizeClientRoutes(list, M{"ipv6Bindings": nil, "clientBindings": b}); len(got) != 1 || !reflect.DeepEqual(got[0].Sources, want) {
		t.Errorf("ipv6Bindings=null 回落: %+v", got)
	}
	// ipv6Bindings 有值时优先
	other := M{"192.168.1.10": []any{"fd00::99"}}
	if got := NormalizeClientRoutes(list, M{"ipv6Bindings": other, "clientBindings": b}); len(got) != 1 || !reflect.DeepEqual(got[0].Sources, []string{"192.168.1.10/32", "fd00::99/128"}) {
		t.Errorf("ipv6Bindings 优先: %+v", got)
	}
	// mac 路由不展开
	macList := []any{M{"match": "mac", "outbound": "p", "macs": []any{"aa:bb:cc:dd:ee:ff"}, "sources": []any{"192.168.1.10"}}}
	if got := NormalizeClientRoutes(macList, M{"ipv6Bindings": b}); len(got) != 1 || len(got[0].Sources) != 0 {
		t.Errorf("mac 路由不展开绑定: %+v", got)
	}
}

func TestSourceMatch(t *testing.T) {
	if got := SourceMatch(Route{Match: "ip", Sources: []string{"1.1.1.1/32"}}); !reflect.DeepEqual(got, M{"source_ip_cidr": []string{"1.1.1.1/32"}}) {
		t.Errorf("ip: %#v", got)
	}
	if got := SourceMatch(Route{Match: "mac", Macs: []string{"aa:bb:cc:dd:ee:ff"}, Sources: []string{}}); !reflect.DeepEqual(got, M{"source_mac_address": []string{"aa:bb:cc:dd:ee:ff"}}) {
		t.Errorf("mac: %#v", got)
	}
	if got := SourceMatch(Route{Match: "ip"}); got != nil {
		t.Errorf("ip 无来源应为 nil: %#v", got)
	}
	if got := SourceMatch(Route{Match: "mac", Sources: []string{"1.1.1.1/32"}}); got != nil {
		t.Errorf("mac 无 MAC 应为 nil(不借用 sources): %#v", got)
	}
	if got := SourceMatch(Route{Match: "ip", Macs: []string{"aa:bb:cc:dd:ee:ff"}}); got != nil {
		t.Errorf("ip 无来源应为 nil(不借用 macs): %#v", got)
	}
}

func TestIsDirectAndProxyClientRoute(t *testing.T) {
	cases := []struct {
		name          string
		r             Route
		direct, block string
		isDirect      bool
		isProxy       bool
	}{
		{"直连出口", Route{Outbound: "direct"}, "direct", "block", true, false},
		{"拒绝出口", Route{Outbound: "block"}, "direct", "block", false, false},
		{"节点出口", Route{Outbound: "nodeA"}, "direct", "block", false, true},
		{"bypass 即使出口等于直连也不算直连", Route{Outbound: "direct", Bypass: true}, "direct", "block", false, false},
		{"bypass 的节点出口不算代理", Route{Outbound: "nodeA", Bypass: true}, "direct", "block", false, false},
		{"没有 directTag 时不可能是直连", Route{Outbound: ""}, "", "block", false, false},
		{"没有 blockTag 时节点仍是代理", Route{Outbound: "nodeA"}, "direct", "", false, true},
		{"出口为空不是代理", Route{Outbound: ""}, "direct", "", false, false},
		{"出口为空且两个 tag 都有值也不是代理", Route{Outbound: ""}, "direct", "block", false, false},
	}
	for _, c := range cases {
		if got := IsDirectClientRoute(c.r, c.direct); got != c.isDirect {
			t.Errorf("%s: IsDirect = %v, want %v", c.name, got, c.isDirect)
		}
		if got := IsProxyClientRoute(c.r, c.direct, c.block); got != c.isProxy {
			t.Errorf("%s: IsProxy = %v, want %v", c.name, got, c.isProxy)
		}
	}
}

func TestTerminalDNSRedirected(t *testing.T) {
	base := TerminalDNSOptions{DNSMode: "dnsmasq", AutoRedirect: true, DirectBypass: true, SplitDNS: true, DirectTag: "direct", BlockTag: "block"}
	with := func(f func(o *TerminalDNSOptions)) TerminalDNSOptions { o := base; f(&o); return o }
	bypass := Route{Outbound: "direct", Bypass: true}
	direct := Route{Outbound: "direct"}
	proxy := Route{Outbound: "nodeA"}
	block := Route{Outbound: "block"}
	cases := []struct {
		name string
		r    Route
		o    TerminalDNSOptions
		want bool
	}{
		{"没有 nft 重定向:一律不转", bypass, with(func(o *TerminalDNSOptions) { o.AutoRedirect = false }), false},
		{"没开分流解析:一律不转", bypass, with(func(o *TerminalDNSOptions) { o.SplitDNS = false }), false},
		{"dns 模式 off:不转", bypass, with(func(o *TerminalDNSOptions) { o.DNSMode = "off" }), false},
		{"dns 模式空:不转", bypass, with(func(o *TerminalDNSOptions) { o.DNSMode = "" }), false},
		{"dnsmasq + bypass:转", bypass, base, true},
		{"hijack + bypass:转", bypass, with(func(o *TerminalDNSOptions) { o.DNSMode = "hijack" }), true},
		{"hijack + 节点:不转", proxy, with(func(o *TerminalDNSOptions) { o.DNSMode = "hijack" }), false},
		{"hijack + 直连:不转", direct, with(func(o *TerminalDNSOptions) { o.DNSMode = "hijack" }), false},
		{"dnsmasq + 节点:转", proxy, base, true},
		{"dnsmasq + 直连 + 直连不进内核开:转", direct, base, true},
		{"dnsmasq + 直连 + 直连不进内核关:不转", direct, with(func(o *TerminalDNSOptions) { o.DirectBypass = false }), false},
		{"dnsmasq + 拒绝:不转", block, base, false},
		{"dnsmasq + 节点 + 关直连不进内核:仍转", proxy, with(func(o *TerminalDNSOptions) { o.DirectBypass = false }), true},
	}
	for _, c := range cases {
		if got := TerminalDNSRedirected(c.r, c.o); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
