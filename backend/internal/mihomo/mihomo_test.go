package mihomo

import (
	"errors"
	"reflect"
	"testing"
)

func TestParsePorts(t *testing.T) {
	good := map[string]Ports{
		"8443":                {Port: []int{8443}},
		"80, 8080-8090 ,9000": {Port: []int{80, 9000}, Range: []string{"8080:8090"}},
		"1-1":                 {Port: []int{1}}, // 起止相同算单个端口
		"1-65535":             {Range: []string{"1:65535"}},
		"80\t81\n82":          {Port: []int{80, 81, 82}},
		",,80,,":              {Port: []int{80}},
		"00080":               {Port: []int{80}},
	}
	for in, want := range good {
		got, ok := ParsePorts(in)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("ParsePorts(%q) = %+v, %v;想要 %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", " ", ",", "0", "65536", "80-", "-80", "a", "80-70", "1.5", "+80", "80-90-100", "123456", "80;81"} {
		if p, ok := ParsePorts(in); ok {
			t.Errorf("ParsePorts(%q) 应失败,得到 %+v", in, p)
		}
	}
}

func TestNormalizePorts(t *testing.T) {
	cases := map[string]string{
		"8080-8090, 80,  9000": "80, 9000, 8080-8090",
		"443":                  "443",
		"bad":                  "",
		"":                     "",
	}
	for in, want := range cases {
		if got := NormalizePorts(in); got != want {
			t.Errorf("NormalizePorts(%q) = %q,想要 %q", in, got, want)
		}
	}
}

func TestSniffPorts(t *testing.T) {
	cases := []struct {
		in   string
		want []any
	}{
		{"8443, 443, 80, 8443", []any{80, 8443}},                          // 去重、去 443、数值升序
		{"1000, 99", []any{99, 1000}},                                     // 数值排序,不是字典序
		{"9000-9100, 8000-8100, 80", []any{80, "8000-8100", "9000-9100"}}, // 范围在后
		{"100-200, 20-30", []any{"100-200", "20-30"}},                     // 范围按「起:止」字典序:"100:200" < "20:30"
		{"443", nil}, // 只有 443 = 没有
		{"443-443, 8443", []any{8443}},
		{"", nil},
		{"oops", nil},
	}
	for _, c := range cases {
		got := SniffPorts(map[string]any{"mihomoTlsSniffPorts": c.in})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SniffPorts(%q) = %#v,想要 %#v", c.in, got, c.want)
		}
	}
	if SniffPorts(map[string]any{}) != nil || SniffPorts(map[string]any{"mihomoTlsSniffPorts": 80}) != nil {
		t.Errorf("没配或类型不对应返回空")
	}
}

func TestBypassPortsOf(t *testing.T) {
	p := map[string]any{"bypassPorts": "22", "bypassPortsWhitelist": "443"}
	if got := BypassPortsOf(p); got != (BypassPorts{"22", "blacklist"}) {
		t.Errorf("默认黑名单: %+v", got)
	}
	p["bypassPortsMode"] = "whitelist"
	if got := BypassPortsOf(p); got != (BypassPorts{"443", "whitelist"}) {
		t.Errorf("白名单: %+v", got)
	}
	p["bypassPortsMode"] = "whatever"
	if got := BypassPortsOf(p); got.Mode != "blacklist" || got.Ports != "22" {
		t.Errorf("别的值按黑名单: %+v", got)
	}
	if got := BypassPortsOf(nil); got != (BypassPorts{"", "blacklist"}) {
		t.Errorf("nil: %+v", got)
	}
}

func TestServerURL(t *testing.T) {
	cases := []struct {
		in   DNSServer
		want string
	}{
		{DNSServer{Protocol: "udp", Server: "223.5.5.5", Port: 53}, "udp://223.5.5.5:53"},
		{DNSServer{Protocol: "https", Server: "dns.google", Port: 443, Path: "/dns-query"}, "https://dns.google:443/dns-query"},
		{DNSServer{Protocol: "tls", Server: "2001:4860:4860::8888", Port: 853}, "tls://[2001:4860:4860::8888]:853"},
		{DNSServer{Protocol: "udp", Server: "::ffff:1.2.3.4", Port: 53}, "udp://[::ffff:1.2.3.4]:53"},
	}
	for _, c := range cases {
		if got := ServerURL(c.in); got != c.want {
			t.Errorf("ServerURL(%+v) = %q,想要 %q", c.in, got, c.want)
		}
	}
}

func TestServerURLs(t *testing.T) {
	a := DNSServer{Protocol: "udp", Server: "223.5.5.5", Port: 53}
	wan := DNSServer{Protocol: "udp", Server: "192.168.1.1", Port: 53, WAN: true}
	doh := DNSServer{Protocol: "https", Server: "dns.example", Port: 443, Path: "/dns-query",
		Bootstrap: &DNSServer{Protocol: "udp", Server: "8.8.8.8", Port: 53}}

	if got := ServerURLs([]DNSServer{a}, URLOptions{}); !reflect.DeepEqual(got, []string{"udp://223.5.5.5:53"}) {
		t.Errorf("单台无参数: %v", got)
	}
	// 走路由:非 WAN 的加 RULES;IPv4 only 加 disable-ipv6;多台加 race
	got := ServerURLs([]DNSServer{a, wan}, URLOptions{Route: true, IPv4Only: true})
	want := []string{
		"udp://223.5.5.5:53#RULES&disable-ipv6=true&openbox-race=true",
		"udp://192.168.1.1:53#disable-ipv6=true&openbox-race=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("路由 + ipv4Only + race:\n%v\n%v", got, want)
	}
	// DoH + bootstrap:参数顺序 RULES、disable-ipv6、bootstrap、node-post、race;bootstrap 整个 URL 做 encodeURIComponent
	got = ServerURLs([]DNSServer{doh}, URLOptions{Route: true, IPv4Only: true})
	want = []string{"https://dns.example:443/dns-query#RULES&disable-ipv6=true&openbox-bootstrap=udp%3A%2F%2F8.8.8.8%3A53&openbox-node-post=true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DoH:\n%v\n%v", got, want)
	}
	if got := ServerURLs(nil, URLOptions{}); got == nil || len(got) != 0 {
		t.Errorf("空列表应得空切片: %#v", got)
	}
}

func TestEncodeURIComponent(t *testing.T) {
	cases := map[string]string{
		"abc-_.!~*'()XYZ019": "abc-_.!~*'()XYZ019", // 这些 encodeURIComponent 不转义
		"udp://[::1]:53":     "udp%3A%2F%2F%5B%3A%3A1%5D%3A53",
		"a b&c=d/é":          "a%20b%26c%3Dd%2F%C3%A9",
		"":                   "",
	}
	for in, want := range cases {
		if got := EncodeURIComponent(in); got != want {
			t.Errorf("EncodeURIComponent(%q) = %q,想要 %q", in, got, want)
		}
	}
}

func TestNodeDNSOf(t *testing.T) {
	subs := []map[string]any{
		{"id": "a", "nodeDns": map[string]any{"url": " https://dns.example/dns-query ", "bootstrap": " 223.5.5.5 "}},
		{"id": "ip", "nodeDns": map[string]any{"url": "https://1.1.1.1/dns-query", "bootstrap": "8.8.8.8"}}, // 主机是 IP:不用 bootstrap
		{"id": "off", "enabled": false, "nodeDns": map[string]any{"url": "https://x.example/dns-query", "bootstrap": "1.1.1.1"}},
		{"id": "none"},
		{"id": "empty", "nodeDns": map[string]any{"url": "  "}},
		{"id": "v6", "nodeDns": map[string]any{"url": "https://dns.example2/q", "bootstrap": "2001:db8::1"}},
		nil,
		{"id": "a", "nodeDns": map[string]any{"url": "https://dns.example/new"}}, // 同 id 覆盖,位置不变
	}
	got := NodeDNSOf(subs, nil)
	want := []NodeDNS{
		{ID: "a", Nameserver: "https://dns.example/new"},
		{ID: "ip", Nameserver: "https://1.1.1.1/dns-query"},
		{ID: "v6", Nameserver: "https://dns.example2/q", Bootstrap: "udp://[2001:db8::1]:53", HasBootstrap: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NodeDNSOf:\n%+v\n%+v", got, want)
	}
	// 第一条(未被覆盖时)的 bootstrap 写成 udp://IP:53 且 url 去空白
	got = NodeDNSOf(subs[:1], nil)
	if len(got) != 1 || got[0].Nameserver != "https://dns.example/dns-query" || got[0].Bootstrap != "udp://223.5.5.5:53" {
		t.Errorf("单条: %+v", got)
	}
	// 校验函数不通过的跳过
	reject := func(m map[string]any) error { return errors.New("bad") }
	if got := NodeDNSOf(subs, reject); len(got) != 0 {
		t.Errorf("校验不过应跳过: %+v", got)
	}
}

func TestIPv6Mode(t *testing.T) {
	cases := []struct {
		p    map[string]any
		want string
	}{
		{map[string]any{}, "off"},
		{map[string]any{"ipv6": false, "ipv6Proxy": "ipv4"}, "off"},
		{map[string]any{"ipv6": true}, "node"},
		{map[string]any{"ipv6": true, "ipv6Proxy": "ipv4"}, "ipv4"},
		{map[string]any{"ipv6": true, "ipv6Proxy": "bypass"}, "bypass"},
		{map[string]any{"ipv6": true, "ipv6Proxy": "node"}, "node"},
		{map[string]any{"ipv6": true, "ipv6Proxy": "???"}, "node"},
	}
	for _, c := range cases {
		if got := IPv6Mode(c.p); got != c.want {
			t.Errorf("IPv6Mode(%v) = %q,想要 %q", c.p, got, c.want)
		}
	}
}

func TestTunOf(t *testing.T) {
	tun := func(m map[string]any) map[string]any { return map[string]any{"tun": m} }
	cases := []struct {
		name string
		p    map[string]any
		want Tun
	}{
		{"没有 tun", map[string]any{}, Tun{Stack: "mixed"}},
		{"tun 类型不对", map[string]any{"tun": "x"}, Tun{Stack: "mixed"}},
		{"合法值", tun(map[string]any{"stack": "gvisor", "mtu": float64(1500), "tcpMss": float64(1360)}), Tun{"gvisor", 1500, 1360}},
		{"system + int", tun(map[string]any{"stack": "system", "mtu": 9000, "tcpMss": 536}), Tun{"system", 9000, 536}},
		{"未知 stack 退回 mixed", tun(map[string]any{"stack": "lwip"}), Tun{Stack: "mixed"}},
		{"mtu 低于 1280 退回 0", tun(map[string]any{"mtu": float64(1279)}), Tun{Stack: "mixed"}},
		{"mtu 上下界", tun(map[string]any{"mtu": float64(1280)}), Tun{Stack: "mixed", MTU: 1280}},
		{"mtu 超上界", tun(map[string]any{"mtu": float64(65536)}), Tun{Stack: "mixed"}},
		{"mtu 小数", tun(map[string]any{"mtu": 1500.5}), Tun{Stack: "mixed"}},
		{"mtu 字符串", tun(map[string]any{"mtu": "1500"}), Tun{Stack: "mixed"}},
		{"tcpMss 上界 65495", tun(map[string]any{"tcpMss": float64(65495)}), Tun{Stack: "mixed", TCPMSS: 65495}},
		{"tcpMss 超上界", tun(map[string]any{"tcpMss": float64(65496)}), Tun{Stack: "mixed"}},
		{"tcpMss 低于 536", tun(map[string]any{"tcpMss": float64(535)}), Tun{Stack: "mixed"}},
	}
	for _, c := range cases {
		if got := TunOf(c.p); got != c.want {
			t.Errorf("%s: %+v,想要 %+v", c.name, got, c.want)
		}
	}
}

func TestHostsOf(t *testing.T) {
	nodes := []map[string]any{
		{"server": "HK.Example.com."},
		{"server": "[2001:db8::1]"},
		{"server": "1.2.3.4"},
		{"server": "chain.example.com", "chain": true}, // 链式节点跳过
		{"server": "localhost"},
		{"server": "nodot"},
		{"server": "bad_host.example.com"}, // 下划线不算
		{"server": "hk.example.com"},       // 重复
		nil,
	}
	subs := []map[string]any{
		{"url": "https://sub.example.org/api?token=x", "account": map[string]any{"url": "https://panel.example.net/login", "apiOrigin": "https://api.example.net"}},
		{"urls": []any{"http://a.example.org/x", "ftp://skip.example.org", "not a url"}},
		{"urlEntries": []any{
			map[string]any{"enabled": true, "url": "https://on.example.org/s"},
			map[string]any{"enabled": false, "url": "https://off.example.org/s"},
		}, "url": "https://ignored.example.org"},
		{"url": "https://203.0.113.9:8443/sub"},
		nil,
	}
	got := HostsOf(nodes, subs)
	wantD := []string{"hk.example.com", "sub.example.org", "panel.example.net", "api.example.net", "a.example.org", "on.example.org"}
	wantC := []string{"2001:db8::1/128", "1.2.3.4/32", "203.0.113.9/32"}
	if !reflect.DeepEqual(got.Domains, wantD) || !reflect.DeepEqual(got.CIDRs, wantC) {
		t.Errorf("HostsOf:\n域名 %v\n想要 %v\nCIDR %v\n想要 %v", got.Domains, wantD, got.CIDRs, wantC)
	}
	if h := HostsOf(nil, nil); h.Domains != nil || h.CIDRs != nil {
		t.Errorf("空输入: %+v", h)
	}
}

func TestFingerprint(t *testing.T) {
	prof := func() map[string]any {
		return map[string]any{"ipv6": true, "dns": map[string]any{"mode": "hijack"}, "bypassPorts": "22",
			"mihomoTlsSniffPorts": "8443", "businessPreference": map[string]any{"x": 1}, "unrelatedKey": "z"}
	}
	subs := []map[string]any{{"id": "a", "url": "https://s.example.org/x", "nodeDns": map[string]any{"url": "https://d.example/q", "bootstrap": "1.1.1.1"}}}
	nodes := []any{map[string]any{"name": "n1"}}
	groups := []any{}
	base := Fingerprint(prof(), nodes, groups, subs, nil)
	if len(base) != 64 || base != Fingerprint(prof(), nodes, groups, subs, nil) {
		t.Fatalf("指纹应是稳定的 sha256 十六进制: %q", base)
	}
	same := func(name string, p map[string]any, n, g any, s []map[string]any) {
		t.Helper()
		if Fingerprint(p, n, g, s, nil) != base {
			t.Errorf("%s: 不该影响指纹", name)
		}
	}
	diff := func(name string, p map[string]any, n, g any, s []map[string]any) {
		t.Helper()
		if Fingerprint(p, n, g, s, nil) == base {
			t.Errorf("%s: 该影响指纹", name)
		}
	}
	// 不影响
	p := prof()
	p["unrelatedKey"] = "changed"
	same("无关键", p, nodes, groups, subs)
	p = prof()
	p["businessPreference"] = map[string]any{"x": 2}
	same("业务优选整体不做,不参与指纹", p, nodes, groups, subs)
	p = prof()
	p["mihomoTlsSniffPorts"] = "8443, 443, 8443" // 规整后一样
	same("嗅探端口写法不同、规整后一样", p, nodes, groups, subs)
	// 影响
	p = prof()
	p["ipv6"] = false
	diff("ipv6", p, nodes, groups, subs)
	p = prof()
	p["dns"] = map[string]any{"mode": "dnsmasq"}
	diff("dns", p, nodes, groups, subs)
	p = prof()
	p["mihomoTlsSniffPorts"] = "9443"
	diff("嗅探端口", p, nodes, groups, subs)
	p = prof()
	p["mihomoTlsSniffPorts"] = "443" // 规整后是空 = 等于没配
	diff("嗅探端口去掉后没了", p, nodes, groups, subs)
	p = prof()
	p["directForNodes"] = false
	diff("directForNodes=false", p, nodes, groups, subs)
	diff("节点", prof(), []any{map[string]any{"name": "n2"}}, groups, subs)
	diff("分组", prof(), nodes, []any{map[string]any{"name": "g"}}, subs)
	diff("订阅 nodeDns", prof(), nodes, groups, []map[string]any{{"id": "a", "url": "https://s.example.org/x", "nodeDns": map[string]any{"url": "https://d2.example/q", "bootstrap": "1.1.1.1"}}})
	diff("订阅主机", prof(), nodes, groups, []map[string]any{{"id": "a", "url": "https://other.example.org/x", "nodeDns": subs[0]["nodeDns"]}})
	// null 和没写不同
	p = prof()
	p["tun"] = nil
	diff("tun 写成 null", p, nodes, groups, subs)
}
