package routing

import (
	"encoding/json"
	"reflect"
	"testing"
)

// 自写测试向量:输入是按 dnsupstream.go 顶部注释里的规则设计的,期望值逐条人工推算,
// 不来自任何别的程序的输出。dns_golden.json 里的 normalizeDnsUpstream / isValidDnsUpstream 条目仍作回归。

func TestIsValidDNSUpstream(t *testing.T) {
	yes := []string{
		"1.1.1.1", "223.5.5.5", "255.255.255.255", "0.0.0.1", "10.0.0.10",
		"  8.8.8.8  ", "\n8.8.4.4\t", // 首尾空白按 JS trim 去掉
		"::1", "2001:db8::1", "fe80::1", "1:2:3:4:5:6:7:8", "1:2:3:4:5:6::", "2606:4700:4700::1111",
	}
	for _, s := range yes {
		if !isValidDNSUpstream(s) {
			t.Errorf("%q 应合法", s)
		}
	}
	no := []string{
		"", "   ", "\t\n",
		"0.0.0.0", "::", // 全零地址
		"256.1.1.1", "1.1.1.256", "1.1.1", "1.1.1.1.1", "01.1.1.1", "1.1.1.01", "1.1.1.-1", "a.b.c.d",
		"dns.google", "example.com", "wan", // 不收域名,wan 记号不是地址
		"1.1.1.1/dns-query", "1.1.1.1:53", "https://1.1.1.1",
		"1::2::3",           // 两处 ::
		"1:2:3:4:5:6:7:8:9", // 超过 8 段
		"g::1", "fe80::1%eth0", "::ffff:1.2.3.4", "[::1]",
	}
	for _, s := range no {
		if isValidDNSUpstream(s) {
			t.Errorf("%q 应不合法", s)
		}
	}
	for _, v := range []any{nil, 1.0, 0.0, true, false, []any{"1.1.1.1"}, map[string]any{"a": "1.1.1.1"}} {
		if isValidDNSUpstream(v) {
			t.Errorf("非字符串 %#v 应不合法", v)
		}
	}
}

func TestNormalizeDNSUpstream(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		// 裸地址原样(去首尾空白)
		{"1.1.1.1", "1.1.1.1"},
		{"  8.8.8.8  ", "8.8.8.8"},
		{"2001:db8::1", "2001:db8::1"},
		{"::1", "::1"},
		// 方括号去掉(前后各一个,可单边)
		{"[2001:db8::1]", "2001:db8::1"},
		{"[1.1.1.1]", "1.1.1.1"},
		{"[1.1.1.1", "1.1.1.1"},
		{"1.1.1.1]", "1.1.1.1"},
		// 方括号里的空白原样带出去(去括号之后不再 trim)
		{"[ 1.1.1.1]", " 1.1.1.1"},
		// 带协议头:取 URL 主机,端口 / 路径 / 账号都丢掉
		{"https://1.1.1.1/dns-query", "1.1.1.1"},
		{"tls://8.8.4.4", "8.8.4.4"},
		{"HTTPS://8.8.8.8", "8.8.8.8"}, // 协议头不分大小写
		{"http://1.1.1.1:8053/x?y=1#z", "1.1.1.1"},
		{"https://user:pw@9.9.9.9/dns-query", "9.9.9.9"},
		{"https://[2606:4700:4700::1111]/dns-query", "2606:4700:4700::1111"},
		{"quic://[2001:db8::1]:853", "2001:db8::1"},
		// 折不出合法 IP → 空串
		{"https://dns.google/dns-query", ""},
		{"https://", ""},
		{"https:///dns-query", ""},
		{"https://0.0.0.0", ""},
		{"0.0.0.0", ""},
		{"::", ""},
		{"1.1.1.1/dns-query", ""}, // 没协议头不当 URL 解析
		{"1.1.1.1:53", ""},
		{"dns.google", ""},
		{"wan", ""},
		{"", ""},
		{"   ", ""},
		{"[]", ""},
		{"[[1.1.1.1]]", ""}, // 只去一层括号
		{"[[1.1.1.1]", ""},  // 开头只去一个 [
		{"[1.1.1.1]]", ""},  // 末尾只去一个 ]
		// 协议头必须带 //;缺 // 的串不走 URL 解析,按裸地址判(这里是纯十六进制加冒号,过得了字符类)
		{"dead:beef", "dead:beef"},
		// 非字符串
		{nil, ""}, {1.0, ""}, {true, ""}, {[]any{"1.1.1.1"}, ""},
	}
	for _, c := range cases {
		if got := normalizeDNSUpstream(c.in); got != c.want {
			t.Errorf("normalizeDNSUpstream(%#v) = %q,想要 %q", c.in, got, c.want)
		}
	}
}

func TestDNSProtocolAndPort(t *testing.T) {
	for _, p := range []string{"udp", "tcp", "tls", "https"} {
		if !isDNSProtocol(p) {
			t.Errorf("%q 应是合法协议", p)
		}
		if got := dnsProtocolOr(p, "x"); got != p {
			t.Errorf("dnsProtocolOr(%q) = %q", p, got)
		}
	}
	for _, v := range []any{"UDP", "Tcp", "TLS", "HTTPS", "quic", "", " udp", nil, 1.0, true, []any{"udp"}} {
		if isDNSProtocol(v) {
			t.Errorf("%#v 不应是合法协议", v)
		}
		if got := dnsProtocolOr(v, "fb"); got != "fb" {
			t.Errorf("dnsProtocolOr(%#v) = %q,想要退回 fb", v, got)
		}
	}

	good := map[float64]int{1: 1, 53: 53, 853: 853, 5353: 5353, 65535: 65535}
	for f, want := range good {
		if p, ok := isValidDNSPort(f); !ok || p != want {
			t.Errorf("端口 %v:got (%d,%v)", f, p, ok)
		}
		if got := dnsPortOr(f, "udp"); got != want {
			t.Errorf("dnsPortOr(%v,udp) = %d", f, got)
		}
	}
	for _, v := range []any{0.0, -1.0, 65536.0, 100000.0, 53.5, 0.1, "53", "", nil, true, []any{53.0}} {
		if _, ok := isValidDNSPort(v); ok {
			t.Errorf("端口 %#v 应不合法", v)
		}
		if got := dnsPortOr(v, "udp"); got != DefaultDNSPort {
			t.Errorf("dnsPortOr(%#v,udp) = %d,想要默认 53", v, got)
		}
		if got := dnsPortOr(v, "tls"); got != DefaultDoTPort {
			t.Errorf("dnsPortOr(%#v,tls) = %d,想要默认 853", v, got)
		}
	}
	if dnsDefaultPort("tls") != 853 || dnsDefaultPort("udp") != 53 || dnsDefaultPort("tcp") != 53 || dnsDefaultPort("https") != 443 {
		t.Fatal("dnsDefaultPort")
	}
}

func TestDNSRegionAndWanFallback(t *testing.T) {
	region := map[string]string{
		`{"dns":{"region":"intl"}}`:  "intl",
		`{"dns":{"region":"cn"}}`:    "cn",
		`{"dns":{"region":"INTL"}}`:  "cn", // 只认小写 intl
		`{"dns":{"region":" intl"}}`: "cn",
		`{"dns":{"region":1}}`:       "cn",
		`{"dns":{"region":null}}`:    "cn",
		`{"dns":{}}`:                 "cn",
		`{"dns":"intl"}`:             "cn",
		`{"dns":null}`:               "cn",
		`{}`:                         "cn",
		`[]`:                         "cn",
		`"intl"`:                     "cn",
		`null`:                       "cn",
	}
	for src, want := range region {
		var v any
		if err := json.Unmarshal([]byte(src), &v); err != nil {
			t.Fatal(err)
		}
		if got := dnsRegionOf(v); got != want {
			t.Errorf("dnsRegionOf(%s) = %q,想要 %q", src, got, want)
		}
	}
	if got := wanFallbackUpstream("cn"); got != "223.5.5.5" {
		t.Errorf("cn 退回 %q", got)
	}
	if got := wanFallbackUpstream("intl"); got != "1.1.1.1" {
		t.Errorf("intl 退回 %q", got)
	}
	if got := wanFallbackUpstream("anything"); got != "223.5.5.5" {
		t.Errorf("未知地区应按 cn 退回,got %q", got)
	}
	if DefaultDNSPort != 53 || MaxDNSExtras != 3 || WanUpstream != "wan" || DefaultDirectUpstream != "223.5.5.5" || DefaultProxyUpstream != "1.1.1.1" {
		t.Errorf("常量变了")
	}
}

func TestWanServer(t *testing.T) {
	cn := mustJSON(t, `{}`)
	intl := mustJSON(t, `{"dns":{"region":"intl"}}`)
	cases := []struct {
		name    string
		profile any
		sys     []any
		want    any
	}{
		{"取第一台", cn, []any{"192.168.1.1", "10.0.0.1"}, "192.168.1.1"},
		{"跳过假值", cn, []any{"", nil, 0.0, false, "10.0.0.1", "10.0.0.2"}, "10.0.0.1"},
		{"空表按 cn 退回", cn, nil, "223.5.5.5"},
		{"全是假值按 cn 退回", cn, []any{"", nil}, "223.5.5.5"},
		{"空表按 intl 退回", intl, []any{}, "1.1.1.1"},
		{"非字符串真值原样", cn, []any{7.0}, 7.0},
		{"空白串是真值", cn, []any{" "}, " "},
	}
	for _, c := range cases {
		if got := wanServer(c.profile, c.sys); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:got %#v,想要 %#v", c.name, got, c.want)
		}
	}
}

func TestServerKey(t *testing.T) {
	same := [][2]any{{"1.1.1.1", "1.1.1.1"}, {5.0, 5.0}, {true, true}, {nil, nil}}
	for _, p := range same {
		if serverKey(p[0]) != serverKey(p[1]) {
			t.Errorf("%#v 与 %#v 应同键", p[0], p[1])
		}
	}
	// 字符串 "5"、数字 5、布尔 true、字符串 "true"、null、字符串 "null" 彼此不能撞键
	vals := []any{"5", 5.0, true, "true", nil, "null", "", false, "1.1.1.1", 1.0, "1", "n:5", "o:true", "s:5"}
	seen := map[string]int{}
	for i, v := range vals {
		k := serverKey(v)
		if j, dup := seen[k]; dup {
			t.Errorf("%#v 与 %#v 撞键 %q", vals[j], v, k)
		}
		seen[k] = i
	}
}

func up(proto string, server any, port int, wan bool) upstream {
	return upstream{Protocol: proto, Server: server, Port: port, Wan: wan}
}

func TestUpstreamOf(t *testing.T) {
	cnProfile := mustJSON(t, `{}`)
	intlProfile := mustJSON(t, `{"dns":{"region":"intl"}}`)
	sys := []any{"192.168.1.1"}
	cases := []struct {
		name             string
		profile          any
		raw, proto, port any
		fbProto, invalid string
		sys              []any
		want             upstream
	}{
		{"wan 固定 udp 53,不看档案协议和端口", cnProfile, "wan", "tcp", 5353.0, "tcp", "1.1.1.1", sys, up("udp", "192.168.1.1", 53, true)},
		{"wan 读不到系统上游按 cn 退回", cnProfile, "wan", "tcp", 5353.0, "tcp", "1.1.1.1", nil, up("udp", "223.5.5.5", 53, true)},
		{"wan 读不到系统上游按 intl 退回", intlProfile, "wan", nil, nil, "udp", "wan", nil, up("udp", "1.1.1.1", 53, true)},
		{"普通地址", cnProfile, "8.8.8.8", "tcp", 5353.0, "udp", "wan", sys, up("tcp", "8.8.8.8", 5353, false)},
		{"地址从 URL 折出来", cnProfile, "https://9.9.9.9/dns-query", nil, nil, "udp", "wan", sys, up("udp", "9.9.9.9", 53, false)},
		{"协议非法退回 fallback", cnProfile, "8.8.8.8", "quic", nil, "tcp", "wan", sys, up("tcp", "8.8.8.8", 53, false)},
		{"tls 合法,默认端口 853", cnProfile, "8.8.8.8", "tls", nil, "tcp", "wan", sys, up("tls", "8.8.8.8", 853, false)},
		{"端口非法退回 53", cnProfile, "8.8.8.8", "udp", 70000.0, "tcp", "wan", sys, up("udp", "8.8.8.8", 53, false)},
		{"地址非法且 invalid=wan:退成 wan", cnProfile, "dns.google", "tcp", 5353.0, "udp", "wan", sys, up("udp", "192.168.1.1", 53, true)},
		{"地址缺失且 invalid=wan:退成 wan", cnProfile, nil, nil, nil, "udp", "wan", sys, up("udp", "192.168.1.1", 53, true)},
		{"地址非法且 invalid=1.1.1.1:保留协议和端口", cnProfile, "dns.google", "udp", 5353.0, "tcp", "1.1.1.1", sys, up("udp", "1.1.1.1", 5353, false)},
		{"地址缺失且 invalid=1.1.1.1:协议退回 fallback", cnProfile, nil, nil, nil, "tcp", "1.1.1.1", sys, up("tcp", "1.1.1.1", 53, false)},
		{"非字符串地址当非法", cnProfile, 8.0, nil, nil, "tcp", "1.1.1.1", sys, up("tcp", "1.1.1.1", 53, false)},
		{"wan 字符串大小写敏感:WAN 是非法地址", cnProfile, "WAN", nil, nil, "tcp", "1.1.1.1", sys, up("tcp", "1.1.1.1", 53, false)},
	}
	for _, c := range cases {
		got := upstreamOf(c.profile, c.raw, c.proto, c.port, c.fbProto, c.invalid, c.sys)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got  %+v\n want %+v", c.name, got, c.want)
		}
	}
}

func TestDirectAndProxyUpstream(t *testing.T) {
	sys := []any{"192.168.1.1"}
	profile := func(s string) any { return mustJSON(t, s) }
	cases := []struct {
		name         string
		profile      any
		direct, prox upstream
	}{
		{"全新安装:直连走上游 DNS,代理 1.1.1.1 tcp", profile(`{}`), up("udp", "192.168.1.1", 53, true), up("tcp", "1.1.1.1", 53, false)},
		{"dns 不是对象", profile(`{"dns":"x"}`), up("udp", "192.168.1.1", 53, true), up("tcp", "1.1.1.1", 53, false)},
		{"显式 wan", profile(`{"dns":{"direct":"wan","proxy":"wan"}}`), up("udp", "192.168.1.1", 53, true), up("udp", "192.168.1.1", 53, true)},
		{"直连地址与协议端口", profile(`{"dns":{"direct":"114.114.114.114","directProtocol":"tcp","directPort":5353}}`), up("tcp", "114.114.114.114", 5353, false), up("tcp", "1.1.1.1", 53, false)},
		{"代理地址与协议端口", profile(`{"dns":{"proxy":"8.8.8.8","proxyProtocol":"udp","proxyPort":853}}`), up("udp", "192.168.1.1", 53, true), up("udp", "8.8.8.8", 853, false)},
		{"直连地址非法退成 wan,代理非法退成 1.1.1.1 并保留协议端口", profile(`{"dns":{"direct":"bad.host","directProtocol":"tcp","proxy":"bad.host","proxyProtocol":"udp","proxyPort":5300}}`), up("udp", "192.168.1.1", 53, true), up("udp", "1.1.1.1", 5300, false)},
		{"直连协议非法退回 udp;代理协议非法退回 tcp", profile(`{"dns":{"direct":"1.2.3.4","directProtocol":"quic","proxy":"5.6.7.8","proxyProtocol":"quic"}}`), up("udp", "1.2.3.4", 53, false), up("tcp", "5.6.7.8", 53, false)},
		{"intl:直连 wan 读不到时退成 1.1.1.1", profile(`{"dns":{"region":"intl"}}`), up("udp", "192.168.1.1", 53, true), up("tcp", "1.1.1.1", 53, false)},
		{"两侧分别读各自的键", profile(`{"dns":{"direct":"1.1.1.1","proxy":"2.2.2.2","directPort":1001,"proxyPort":2002}}`), up("udp", "1.1.1.1", 1001, false), up("tcp", "2.2.2.2", 2002, false)},
	}
	for _, c := range cases {
		if got := directUpstream(c.profile, sys); !reflect.DeepEqual(got, c.direct) {
			t.Errorf("%s 直连:\n got  %+v\n want %+v", c.name, got, c.direct)
		}
		if got := proxyUpstream(c.profile, sys); !reflect.DeepEqual(got, c.prox) {
			t.Errorf("%s 代理:\n got  %+v\n want %+v", c.name, got, c.prox)
		}
	}
	// 读不到系统上游:cn 退 223.5.5.5,intl 退 1.1.1.1
	if got := directUpstream(profile(`{}`), nil); !reflect.DeepEqual(got, up("udp", "223.5.5.5", 53, true)) {
		t.Errorf("cn 无系统上游:%+v", got)
	}
	if got := directUpstream(profile(`{"dns":{"region":"intl"}}`), nil); !reflect.DeepEqual(got, up("udp", "1.1.1.1", 53, true)) {
		t.Errorf("intl 无系统上游:%+v", got)
	}
}

func TestExtraUpstreams(t *testing.T) {
	sys := []any{"192.168.1.1"}
	build := func(dns string) any { return mustJSON(t, `{"dns":`+dns+`}`) }

	t.Run("缺失 / 非数组:返回非 nil 的空切片", func(t *testing.T) {
		for _, dns := range []string{`{}`, `{"directExtras":null}`, `{"directExtras":"8.8.8.8"}`, `{"directExtras":{"server":"8.8.8.8"}}`, `{"directExtras":[]}`} {
			got := directExtraUpstreams(build(dns), sys)
			if got == nil || len(got) != 0 {
				t.Errorf("%s:got %#v", dns, got)
			}
		}
	})

	t.Run("基本形态与各自的默认协议", func(t *testing.T) {
		p := build(`{"direct":"223.5.5.5","proxy":"1.1.1.1","directExtras":[{"server":"114.114.114.114"},{"server":"119.29.29.29","protocol":"tcp","port":5353}],"proxyExtras":[{"server":"8.8.8.8"},{"server":"9.9.9.9","protocol":"udp","port":853}]}`)
		wantD := []upstream{up("udp", "114.114.114.114", 53, false), up("tcp", "119.29.29.29", 5353, false)}
		wantP := []upstream{up("tcp", "8.8.8.8", 53, false), up("udp", "9.9.9.9", 853, false)}
		if got := directExtraUpstreams(p, sys); !reflect.DeepEqual(got, wantD) {
			t.Errorf("直连备用:got %+v", got)
		}
		if got := proxyExtraUpstreams(p, sys); !reflect.DeepEqual(got, wantP) {
			t.Errorf("代理备用:got %+v", got)
		}
	})

	t.Run("两侧各读各的键", func(t *testing.T) {
		p := build(`{"direct":"1.1.1.1","proxy":"2.2.2.2","directExtras":[{"server":"3.3.3.3"}]}`)
		if got := proxyExtraUpstreams(p, sys); len(got) != 0 {
			t.Errorf("代理侧不该读 directExtras:%+v", got)
		}
		p = build(`{"direct":"1.1.1.1","proxy":"2.2.2.2","proxyExtras":[{"server":"3.3.3.3"}]}`)
		if got := directExtraUpstreams(p, sys); len(got) != 0 {
			t.Errorf("直连侧不该读 proxyExtras:%+v", got)
		}
	})

	t.Run("先截前 3 个再筛", func(t *testing.T) {
		p := build(`{"direct":"1.1.1.1","directExtras":[{"server":"bad"},{"server":"bad2"},{"server":"bad3"},{"server":"4.4.4.4"}]}`)
		if got := directExtraUpstreams(p, sys); len(got) != 0 {
			t.Errorf("第 4 个在截断之外,应被丢掉:%+v", got)
		}
		p = build(`{"direct":"1.1.1.1","directExtras":[{"server":"2.2.2.2"},{"server":"3.3.3.3"},{"server":"4.4.4.4"},{"server":"5.5.5.5"},{"server":"6.6.6.6"}]}`)
		got := directExtraUpstreams(p, sys)
		if len(got) != 3 || got[0].Server != "2.2.2.2" || got[1].Server != "3.3.3.3" || got[2].Server != "4.4.4.4" {
			t.Errorf("最多 3 个,按原顺序:%+v", got)
		}
		// 恰好 3 个不截
		p = build(`{"direct":"1.1.1.1","directExtras":[{"server":"2.2.2.2"},{"server":"3.3.3.3"},{"server":"4.4.4.4"}]}`)
		if got := directExtraUpstreams(p, sys); len(got) != 3 {
			t.Errorf("恰好 3 个都留:%+v", got)
		}
	})

	t.Run("跳过假值、非对象和非法地址", func(t *testing.T) {
		p := build(`{"direct":"1.1.1.1","directExtras":[null,0,"",false,"x",{"server":"dns.google"}]}`)
		if got := directExtraUpstreams(p, sys); len(got) != 0 {
			t.Errorf("got %+v", got)
		}
		// 没有 server 键、server 非字符串
		p = build(`{"direct":"1.1.1.1","directExtras":[{},{"protocol":"tcp"},{"server":5},{"server":null},{"server":["2.2.2.2"]}]}`)
		if got := directExtraUpstreams(p, sys); len(got) != 0 {
			t.Errorf("got %+v", got)
		}
		// 非法的不会被「退成 wan」捡回来
		p = build(`{"direct":"1.1.1.1","directExtras":[{"server":"bad.host"},{"server":"2.2.2.2"}]}`)
		got := directExtraUpstreams(p, sys)
		if len(got) != 1 || got[0].Server != "2.2.2.2" || got[0].Wan {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("去重:和主上游、和前面的备用落到同一台就去掉", func(t *testing.T) {
		p := build(`{"direct":"8.8.8.8","directExtras":[{"server":"8.8.8.8"},{"server":"https://8.8.8.8/dns-query"},{"server":"8.8.4.4"},{"server":"8.8.4.4","port":5353},{"server":" 8.8.4.4 "},{"server":"9.9.9.9"}]}`)
		want := []upstream{up("udp", "8.8.4.4", 53, false)}
		// 前 3 个里:8.8.8.8(同主上游)、URL 形式的 8.8.8.8(同主上游)、8.8.4.4(留下)
		if got := directExtraUpstreams(p, sys); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
		p = build(`{"direct":"8.8.8.8","directExtras":[{"server":"8.8.4.4"},{"server":"8.8.4.4","port":5353},{"server":"[::1]"}]}`)
		want = []upstream{up("udp", "8.8.4.4", 53, false), up("udp", "::1", 53, false)}
		if got := directExtraUpstreams(p, sys); !reflect.DeepEqual(got, want) {
			t.Errorf("同台不同端口也算重复:got %+v", got)
		}
	})

	t.Run("wan 作备用", func(t *testing.T) {
		// 主上游显式、备用写 wan:加进来,Wan 标出,固定 udp 53;第二个 wan 是重复
		p := build(`{"direct":"8.8.8.8","directExtras":[{"server":"wan","protocol":"tcp","port":5353},{"server":"wan"}]}`)
		want := []upstream{up("udp", "192.168.1.1", 53, true)}
		if got := directExtraUpstreams(p, sys); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
		// 主上游本身就是 wan:备用写 wan 解析出同一台,去掉
		p = build(`{"directExtras":[{"server":"wan"}]}`)
		if got := directExtraUpstreams(p, sys); len(got) != 0 {
			t.Errorf("got %+v", got)
		}
		// 主上游是 wan,备用里写了和系统上游同一个地址:同样去掉
		p = build(`{"directExtras":[{"server":"192.168.1.1"},{"server":"192.168.1.2"}]}`)
		want = []upstream{up("udp", "192.168.1.2", 53, false)}
		if got := directExtraUpstreams(p, sys); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
		// 代理侧的 wan 备用同样固定 udp(代理默认协议是 tcp,但 wan 不看)
		p = build(`{"proxy":"1.1.1.1","proxyExtras":[{"server":"wan","protocol":"tcp"}]}`)
		want = []upstream{up("udp", "192.168.1.1", 53, true)}
		if got := proxyExtraUpstreams(p, sys); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("协议端口非法时退回各自默认", func(t *testing.T) {
		p := build(`{"direct":"1.1.1.1","proxy":"2.2.2.2","directExtras":[{"server":"3.3.3.3","protocol":"quic","port":0}],"proxyExtras":[{"server":"4.4.4.4","protocol":"quic","port":70000}]}`)
		if got := directExtraUpstreams(p, sys); !reflect.DeepEqual(got, []upstream{up("udp", "3.3.3.3", 53, false)}) {
			t.Errorf("直连:%+v", got)
		}
		if got := proxyExtraUpstreams(p, sys); !reflect.DeepEqual(got, []upstream{up("tcp", "4.4.4.4", 53, false)}) {
			t.Errorf("代理:%+v", got)
		}
	})

	t.Run("tls 备用上游合法,默认端口 853", func(t *testing.T) {
		p := build(`{"direct":"1.1.1.1","directExtras":[{"server":"3.3.3.3","protocol":"tls"}]}`)
		if got := directExtraUpstreams(p, sys); !reflect.DeepEqual(got, []upstream{up("tls", "3.3.3.3", 853, false)}) {
			t.Errorf("直连 tls:%+v", got)
		}
	})

	t.Run("主上游本身非法退成 wan 时,备用按退回后的主上游去重", func(t *testing.T) {
		p := build(`{"direct":"bad.host","directExtras":[{"server":"192.168.1.1"},{"server":"5.5.5.5"}]}`)
		want := []upstream{up("udp", "5.5.5.5", 53, false)}
		if got := directExtraUpstreams(p, sys); !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
	})
}

func TestDNSServerEntry(t *testing.T) {
	enc := func(m map[string]any) string {
		b, err := json.Marshal(m) // map 按键名排序,逐字节可比
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := []struct {
		name        string
		up          upstream
		tag, detour string
		want        string
	}{
		{"默认端口不写 server_port,无 detour", up("udp", "223.5.5.5", 53, false), "dns-direct", "", `{"server":"223.5.5.5","tag":"dns-direct","type":"udp"}`},
		{"非默认端口", up("tcp", "1.1.1.1", 5353, false), "dns-proxy", "", `{"server":"1.1.1.1","server_port":5353,"tag":"dns-proxy","type":"tcp"}`},
		{"带 detour", up("tcp", "1.1.1.1", 53, false), "dns-proxy", "proxy", `{"detour":"proxy","server":"1.1.1.1","tag":"dns-proxy","type":"tcp"}`},
		{"端口加 detour", up("udp", "8.8.8.8", 853, false), "d", "出口", `{"detour":"出口","server":"8.8.8.8","server_port":853,"tag":"d","type":"udp"}`},
		{"端口 1 也写", up("udp", "8.8.8.8", 1, false), "d", "", `{"server":"8.8.8.8","server_port":1,"tag":"d","type":"udp"}`},
		{"IPv6 地址", up("udp", "2001:db8::1", 53, false), "d6", "", `{"server":"2001:db8::1","tag":"d6","type":"udp"}`},
		{"Wan 标记不进配置", up("udp", "192.168.1.1", 53, true), "dns-wan", "", `{"server":"192.168.1.1","tag":"dns-wan","type":"udp"}`},
		{"非字符串 server 原样带出", up("udp", 7.0, 53, false), "d", "", `{"server":7,"tag":"d","type":"udp"}`},
	}
	for _, c := range cases {
		if got := enc(dnsServerEntry(c.up, c.tag, c.detour)); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

func TestDoTUpstreamAndDomain(t *testing.T) {
	for _, h := range []string{"dns.google", "one.one.one.one", "DNS.Google"} {
		if !isDNSDomainHost(h) {
			t.Errorf("%q 应是域名主机", h)
		}
	}
	for _, h := range []string{"1.1.1.1", "wan", "", "localhost", "a..b", "-a.com", "a-.com", "a b.com", "a.com/x", "a.com:853", "例子.com"} {
		if isDNSDomainHost(h) {
			t.Errorf("%q 不应是域名主机", h)
		}
	}
	if got := normalizeDNSDomain("tls://DNS.Google:853"); got != "dns.google" {
		t.Errorf("带协议头折域名:%q", got)
	}

	prof := map[string]any{"dns": map[string]any{"direct": "dns.google", "directProtocol": "tls"}}
	up := directUpstream(prof, []any{"8.8.8.8"})
	if up.Protocol != "tls" || up.Server != "dns.google" || !up.Domain || up.Port != 853 {
		t.Fatalf("直连 DoT 域名:%+v", up)
	}
	ents := dnsServerEntries(up, "dns-direct", "", prof, []any{"8.8.8.8"})
	if len(ents) != 2 {
		t.Fatalf("条目数 %d", len(ents))
	}
	if ents[0]["type"] != "tls" || ents[0]["domain_resolver"] != "dns-direct-bootstrap" || ents[0]["server"] != "dns.google" {
		t.Fatalf("主条目 %+v", ents[0])
	}
	if _, has := ents[0]["server_port"]; has {
		t.Fatalf("默认 853 不应写 server_port:%+v", ents[0])
	}
	if ents[1]["type"] != "udp" || ents[1]["tag"] != "dns-direct-bootstrap" || ents[1]["server"] != "8.8.8.8" {
		t.Fatalf("bootstrap %+v", ents[1])
	}

	// 非默认端口 + detour:bootstrap 不带 detour
	up3 := upstream{Protocol: "tls", Server: "dns.example", Port: 8853, Domain: true}
	e3 := dnsServerEntries(up3, "dns-proxy", "proxy", prof, []any{"8.8.8.8"})
	if e3[0]["server_port"] != 8853 || e3[0]["detour"] != "proxy" {
		t.Fatalf("端口与 detour:%+v", e3[0])
	}
	if _, has := e3[1]["detour"]; has {
		t.Fatalf("bootstrap 不应带 detour:%+v", e3[1])
	}

	// IP + tls:不带 bootstrap
	up2 := upstreamOf(prof, "1.1.1.1", "tls", nil, "udp", "1.1.1.1", nil)
	if up2.Domain || up2.Port != 853 {
		t.Fatalf("tls IP:%+v", up2)
	}
	if n := len(dnsServerEntries(up2, "t", "", prof, nil)); n != 1 {
		t.Fatalf("tls IP 条目数 %d", n)
	}

	// 域名但协议不是 tls:仍按非法退回(直连退成上游 DNS)
	prof2 := map[string]any{"dns": map[string]any{"direct": "dns.google", "directProtocol": "udp"}}
	if u := directUpstream(prof2, []any{"8.8.8.8"}); !u.Wan || u.Domain {
		t.Fatalf("udp + 域名应退成 wan:%+v", u)
	}
}
