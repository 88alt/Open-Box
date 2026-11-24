package routing

import (
	"encoding/json"
	"reflect"
	"testing"
)

// 自写测试向量:mihomo/mihomoDnsUpstreams(见 dnsupstream_export.go)。期望值按 dnsupstream.go 的规则人工推算:
// 直连侧默认走「上游 DNS」(systemDns 的第一台,读不到按地区退回 223.5.5.5 / 1.1.1.1),代理侧默认 tcp 1.1.1.1;
// 地址不合法时直连退成上游 DNS、代理退成 1.1.1.1;域名 DoT / DoH 带上 bootstrap(没指定就是上游 DNS 那台 udp 53)。

func TestDNSUpstreamsDefaults(t *testing.T) {
	got := DNSUpstreamsFor(map[string]any{}, nil)
	want := DNSUpstreamSet{
		Direct:       DNSUpstreamServer{Protocol: "udp", Server: "223.5.5.5", Port: 53, Wan: true},
		Proxy:        DNSUpstreamServer{Protocol: "tcp", Server: "1.1.1.1", Port: 53},
		DirectExtras: []DNSUpstreamServer{},
		ProxyExtras:  []DNSUpstreamServer{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("缺省档案:\n got %+v\nwant %+v", got, want)
	}

	// 地区 intl:上游 DNS 读不到时退回 1.1.1.1(直连侧也跟着地区走,这里只钉「上游 DNS 的退路」)。
	intl := DNSUpstreamsFor(map[string]any{"dns": map[string]any{"region": "intl"}}, nil)
	if intl.Direct.Server != "1.1.1.1" || !intl.Direct.Wan {
		t.Fatalf("intl 地区的上游 DNS 退路 = %+v", intl.Direct)
	}

	// systemDns 有值时直连侧用它第一台非空项。
	withSys := DNSUpstreamsFor(map[string]any{}, []any{nil, "", "192.168.1.1", "9.9.9.9"})
	if withSys.Direct.Server != "192.168.1.1" || withSys.Direct.Port != 53 || withSys.Direct.Protocol != "udp" {
		t.Fatalf("systemDns 首项 = %+v", withSys.Direct)
	}
	// systemDns 里的非字符串按 JS 的 String() 转,不崩、不丢。
	odd := DNSUpstreamsFor(map[string]any{}, []any{"", 8.8})
	if odd.Direct.Server != "8.8" {
		t.Fatalf("systemDns 非字符串 = %+v", odd.Direct)
	}
}

func TestDNSUpstreamsExplicit(t *testing.T) {
	profile := map[string]any{"dns": map[string]any{
		"direct":         "8.8.4.4",
		"directProtocol": "tcp",
		"directPort":     5353.0,
		"proxy":          "tls://9.9.9.9",
		"proxyProtocol":  "tls",
	}}
	got := DNSUpstreamsFor(profile, []any{"192.168.1.1"})
	if got.Direct != (DNSUpstreamServer{Protocol: "tcp", Server: "8.8.4.4", Port: 5353}) {
		t.Fatalf("直连侧 = %+v", got.Direct)
	}
	// tls 的默认端口 853(档案没写 proxyPort),地址里的协议头取主机。
	if got.Proxy != (DNSUpstreamServer{Protocol: "tls", Server: "9.9.9.9", Port: 853}) {
		t.Fatalf("代理侧 = %+v", got.Proxy)
	}

	// 代理侧写 wan:取自路由器上游 DNS,标记 wan(后端据此不给它加 #RULES)。
	wanProxy := DNSUpstreamsFor(map[string]any{"dns": map[string]any{"proxy": "wan"}}, []any{"192.168.1.1"})
	if wanProxy.Proxy != (DNSUpstreamServer{Protocol: "udp", Server: "192.168.1.1", Port: 53, Wan: true}) {
		t.Fatalf("代理侧 wan = %+v", wanProxy.Proxy)
	}

	// 地址写坏:直连退成上游 DNS,代理退成 1.1.1.1,协议 / 端口按各自的缺省。
	bad := DNSUpstreamsFor(map[string]any{"dns": map[string]any{"direct": "不是地址", "proxy": "0.0.0.0"}}, []any{"192.168.1.1"})
	if bad.Direct != (DNSUpstreamServer{Protocol: "udp", Server: "192.168.1.1", Port: 53, Wan: true}) {
		t.Fatalf("直连写坏 = %+v", bad.Direct)
	}
	if bad.Proxy != (DNSUpstreamServer{Protocol: "tcp", Server: "1.1.1.1", Port: 53}) {
		t.Fatalf("代理写坏 = %+v", bad.Proxy)
	}
}

// sameServer:比较两台上游(带 bootstrap 时按值比,不按指针)。
func sameServer(t *testing.T, got, want DNSUpstreamServer) {
	t.Helper()
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Fatalf("\n got %s\nwant %s", gb, wb)
	}
}

func TestDNSUpstreamsDomainAndDoH(t *testing.T) {
	// 域名 DoT:没指定 bootstrap → 上游 DNS 那台 udp 53。
	dot := DNSUpstreamsFor(map[string]any{"dns": map[string]any{
		"direct":         "dns.google",
		"directProtocol": "tls",
	}}, []any{"192.168.1.1"})
	wantDot := DNSUpstreamServer{Protocol: "tls", Server: "dns.google", Port: 853,
		Bootstrap: &DNSUpstreamBoot{Protocol: "udp", Server: "192.168.1.1", Port: 53}}
	sameServer(t, dot.Direct, wantDot)

	// 指定 bootstrap(tcp 5353)时用它。
	boot := DNSUpstreamsFor(map[string]any{"dns": map[string]any{
		"proxy":          "dns.google",
		"proxyProtocol":  "tls",
		"proxyBootstrap": map[string]any{"server": "1.1.1.1", "protocol": "tcp", "port": 5353.0},
	}}, nil)
	wantBoot := DNSUpstreamServer{Protocol: "tls", Server: "dns.google", Port: 853,
		Bootstrap: &DNSUpstreamBoot{Protocol: "tcp", Server: "1.1.1.1", Port: 5353}}
	sameServer(t, boot.Proxy, wantBoot)

	// DoH:地址里的端口与路径带出来;主机是域名 → 同样带 bootstrap。
	doh := DNSUpstreamsFor(map[string]any{"dns": map[string]any{
		"direct":         "https://dns.example.com:8443/resolve?x=1",
		"directProtocol": "https",
	}}, []any{"192.168.1.1"})
	if doh.Direct.Protocol != "https" || doh.Direct.Server != "dns.example.com" || doh.Direct.Port != 8443 ||
		doh.Direct.Path != "/resolve?x=1" || doh.Direct.Bootstrap == nil {
		t.Fatalf("DoH = %+v", doh.Direct)
	}
	// DoH 写的是 IP 时不需要 bootstrap。
	dohIP := DNSUpstreamsFor(map[string]any{"dns": map[string]any{
		"direct":         "https://1.1.1.1/dns-query",
		"directProtocol": "https",
	}}, nil)
	if dohIP.Direct.Bootstrap != nil || dohIP.Direct.Path != "/dns-query" || dohIP.Direct.Port != 443 {
		t.Fatalf("DoH 用 IP = %+v", dohIP.Direct)
	}
}

func TestDNSUpstreamsExtras(t *testing.T) {
	// 备用上游:与主上游同一台去掉、写坏的跳过、最多 3 个、保持顺序;空列表是 []。
	profile := map[string]any{"dns": map[string]any{
		"direct": "8.8.8.8",
		"directExtras": []any{
			map[string]any{"server": "8.8.8.8"},                 // 与主上游重复
			map[string]any{"server": "1.1.1.1"},                 // 留
			map[string]any{"server": "9.9.9.9", "port": 5353.0}, // 留
			map[string]any{"server": "不是地址"},                    // 跳过
			map[string]any{"server": "223.5.5.5"},               // 第 4 个,被截断(先截断再筛)
		},
	}}
	got := DNSUpstreamsFor(profile, nil)
	want := []DNSUpstreamServer{
		{Protocol: "udp", Server: "1.1.1.1", Port: 53},
		{Protocol: "udp", Server: "9.9.9.9", Port: 5353},
	}
	if !reflect.DeepEqual(got.DirectExtras, want) {
		t.Fatalf("直连备用 = %+v", got.DirectExtras)
	}
	if got.ProxyExtras == nil || len(got.ProxyExtras) != 0 {
		t.Fatalf("代理备用应为空数组,%+v", got.ProxyExtras)
	}

	// 「先截前 3 个再筛」:写坏的那项占了一个名额,后面的好项不会顶上来。
	trunc := map[string]any{"dns": map[string]any{
		"direct": "8.8.8.8",
		"directExtras": []any{
			map[string]any{"server": "不是地址"},
			map[string]any{"server": "1.1.1.1"},
			map[string]any{"server": "9.9.9.9"},
			map[string]any{"server": "223.5.5.5"},
		},
	}}
	tr := DNSUpstreamsFor(trunc, nil)
	if len(tr.DirectExtras) != 2 || tr.DirectExtras[0].Server != "1.1.1.1" || tr.DirectExtras[1].Server != "9.9.9.9" {
		t.Fatalf("截断后再筛 = %+v", tr.DirectExtras)
	}
}

func TestDNSUpstreamsJSONShape(t *testing.T) {
	// 键序固定为 direct / proxy / directExtras / proxyExtras;空列表序列化成 [] 而不是 null;
	// 可选键(wan / bootstrap / path)为空时不出现。
	got := DNSUpstreamsFor(map[string]any{"dns": map[string]any{"direct": "8.8.8.8", "proxy": "1.1.1.1"}}, nil)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"direct":{"protocol":"udp","server":"8.8.8.8","port":53},"proxy":{"protocol":"tcp","server":"1.1.1.1","port":53},"directExtras":[],"proxyExtras":[]}`
	if string(b) != want {
		t.Fatalf("JSON:\n got %s\nwant %s", b, want)
	}

	doh := DNSUpstreamsFor(map[string]any{"dns": map[string]any{
		"direct":         "https://dns.example.com/dns-query",
		"directProtocol": "https",
	}}, nil)
	b2, _ := json.Marshal(doh.Direct)
	want2 := `{"protocol":"https","server":"dns.example.com","port":443,"path":"/dns-query","bootstrap":{"protocol":"udp","server":"223.5.5.5","port":53}}`
	if string(b2) != want2 {
		t.Fatalf("DoH JSON:\n got %s\nwant %s", b2, want2)
	}
}
