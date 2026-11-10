package routing

import (
	"reflect"
	"testing"
)

// 自写测试向量:BuildDNS 的错误路径和「站点分流关」(dns.split 为假)的短路径。输入自己拟,期望值按
// dns.go 注释里的规则人工推算,不来自别的程序的输出。分流开着的长路径(上游竞速、站点集、终端分流、FakeIP……)在后面几步里换。
// 这条短路径只有:内核自己的一台直连解析器 + 可选的 DNS 重写服务和本地主机名 + 域名过滤规则;
// 代理侧、备用上游、站点集、终端分流在这里都不起作用。

func dnsDirect(server string) obj { return obj{"type": "udp", "tag": "dns-direct", "server": server} }

var (
	dnsLocalServer = obj{"type": "local", "tag": "dns-local"}
	dnsLocalRules  = []any{
		obj{"domain_suffix": []string{".lan", ".local", ".home", ".internal", ".home.arpa"}, "server": "dns-local"},
		obj{"domain_regex": []string{"^[^.]+$"}, "server": "dns-local"},
	}
	dnsRewriteServer = obj{"type": "udp", "tag": "dns-rewrite", "server": "127.0.0.1", "server_port": float64(7854)}
)

func TestBuildDNSErrors(t *testing.T) {
	cases := []struct {
		name string
		p    any
		want string
	}{
		{"profile 为 null", nil, "Cannot read properties of undefined (reading 'split')"},
		{"profile 是空对象:没有 dns", obj{}, "Cannot read properties of undefined (reading 'split')"},
		{"profile 不是对象", "x", "Cannot read properties of undefined (reading 'split')"},
		{"profile 是数组", []any{}, "Cannot read properties of undefined (reading 'split')"},
		{"dns 为 null", obj{"dns": nil}, "Cannot read properties of null (reading 'split')"},
	}
	for _, c := range cases {
		res, err := BuildDNS(c.p, DNSOptions{})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s:err = %v,想要 %q", c.name, err, c.want)
		}
		if res.DNS != nil || res.Resolvers.Policies != nil || res.RuleOwners != nil {
			t.Errorf("%s:出错时应返回零值结果:%#v", c.name, res)
		}
	}
	// dns 是 false / 0 这类非对象的假值不报错(JS 里读它们的 split 得 undefined)
	for _, v := range []any{false, 0.0} {
		if _, err := BuildDNS(obj{"dns": v}, DNSOptions{}); err != nil {
			t.Errorf("dns = %#v 不该报错:%v", v, err)
		}
	}
}

func TestBuildDNSNoSplit(t *testing.T) {
	rw := obj{"rules": []any{
		obj{"source": "a.example.com", "domain": "b.example.com"},
		obj{"source": "*.corp.example", "addresses": []any{"10.0.0.5"}},
		obj{"source": "off.example.com", "domain": "x.example.com", "enabled": false},
	}}
	rwRules := []any{
		obj{"domain": []any{"a.example.com"}, "server": "dns-rewrite"},
		obj{"domain_suffix": []any{".corp.example"}, "server": "dns-rewrite"},
	}
	filter := []any{obj{"domain": []string{"ads.example"}, "action": "reject"}}

	cases := []struct {
		name  string
		p     any
		o     DNSOptions
		wantS []any // servers
		wantR []any // rules;nil 表示结果里没有 rules 这个键
		strat string
	}{
		{"没写 dns 内容(默认劫持模式):直连解析器 + 本地主机名", obj{"dns": obj{}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5"), dnsLocalServer}, dnsLocalRules, "ipv4_only"},
		{"split 明确为 false", obj{"dns": obj{"split": false}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5"), dnsLocalServer}, dnsLocalRules, "ipv4_only"},
		{"split 为 0 / 空串 / null 同样是关", obj{"dns": obj{"split": 0.0}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5"), dnsLocalServer}, dnsLocalRules, "ipv4_only"},
		{"dns 是 false", obj{"dns": false}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5"), dnsLocalServer}, dnsLocalRules, "ipv4_only"},
		{"dnsmasq 模式:没有本地主机名,也就没有规则", obj{"dns": obj{"mode": "dnsmasq"}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5")}, nil, "ipv4_only"},
		{"未知模式按非劫持处理", obj{"dns": obj{"mode": "other"}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5")}, nil, "ipv4_only"},
		{"模式写成非字符串的真值:不是劫持", obj{"dns": obj{"mode": 5.0}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5")}, nil, "ipv4_only"},
		{"模式写成空串:按缺省劫持", obj{"dns": obj{"mode": ""}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5"), dnsLocalServer}, dnsLocalRules, "ipv4_only"},

		// 直连上游
		{"直连上游:自定义 IPv4", obj{"dns": obj{"mode": "dnsmasq", "direct": "119.29.29.29"}}, DNSOptions{},
			[]any{dnsDirect("119.29.29.29")}, nil, "ipv4_only"},
		{"直连上游:协议 tcp、端口 5353 带 server_port", obj{"dns": obj{"mode": "dnsmasq", "direct": "119.29.29.29", "directProtocol": "tcp", "directPort": 5353.0}}, DNSOptions{},
			[]any{obj{"type": "tcp", "tag": "dns-direct", "server": "119.29.29.29", "server_port": 5353}}, nil, "ipv4_only"},
		{"直连上游:端口 53 不写 server_port", obj{"dns": obj{"mode": "dnsmasq", "direct": "119.29.29.29", "directPort": 53.0}}, DNSOptions{},
			[]any{dnsDirect("119.29.29.29")}, nil, "ipv4_only"},
		{"直连上游:端口不合法按 53", obj{"dns": obj{"mode": "dnsmasq", "direct": "119.29.29.29", "directPort": 70000.0}}, DNSOptions{},
			[]any{dnsDirect("119.29.29.29")}, nil, "ipv4_only"},
		{"直连上游:协议不合法按 udp", obj{"dns": obj{"mode": "dnsmasq", "direct": "119.29.29.29", "directProtocol": "quic"}}, DNSOptions{},
			[]any{dnsDirect("119.29.29.29")}, nil, "ipv4_only"},
		{"直连上游:写成链接折成主机", obj{"dns": obj{"mode": "dnsmasq", "direct": "https://8.8.8.8/dns-query"}}, DNSOptions{},
			[]any{dnsDirect("8.8.8.8")}, nil, "ipv4_only"},
		{"直连上游:上游 DNS 记号取系统第一个真值", obj{"dns": obj{"mode": "dnsmasq", "direct": "wan"}}, DNSOptions{SystemDNS: []any{nil, "", "10.0.0.1", "10.0.0.2"}},
			[]any{dnsDirect("10.0.0.1")}, nil, "ipv4_only"},
		{"直连上游:上游 DNS 固定 UDP 53,档案里写的协议和端口不看", obj{"dns": obj{"mode": "dnsmasq", "direct": "wan", "directProtocol": "tcp", "directPort": 5353.0}}, DNSOptions{SystemDNS: []any{"10.0.0.1"}},
			[]any{dnsDirect("10.0.0.1")}, nil, "ipv4_only"},
		{"直连上游:读不到系统 DNS,国内退 223.5.5.5", obj{"dns": obj{"mode": "dnsmasq"}}, DNSOptions{SystemDNS: []any{nil, ""}},
			[]any{dnsDirect("223.5.5.5")}, nil, "ipv4_only"},
		{"直连上游:读不到系统 DNS,海外退 1.1.1.1", obj{"dns": obj{"mode": "dnsmasq", "region": "intl"}}, DNSOptions{},
			[]any{dnsDirect("1.1.1.1")}, nil, "ipv4_only"},
		{"直连上游:写了域名不合法,退成上游 DNS", obj{"dns": obj{"mode": "dnsmasq", "direct": "dns.example.com"}}, DNSOptions{SystemDNS: []any{"10.0.0.1"}},
			[]any{dnsDirect("10.0.0.1")}, nil, "ipv4_only"},

		// 地址族
		{"开 IPv6:prefer_ipv4", obj{"ipv6": true, "dns": obj{"mode": "dnsmasq"}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5")}, nil, "prefer_ipv4"},

		// 这条路径用不上的设置
		{"备用上游、代理上游、FakeIP 都不起作用", obj{"dns": obj{"mode": "dnsmasq", "directExtras": []any{obj{"server": "8.8.8.8"}}, "proxy": "9.9.9.9", "proxyExtras": []any{obj{"server": "1.0.0.1"}}, "fakeIpForProxy": true}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5")}, nil, "ipv4_only"},

		// DNS 重写
		{"DNS 重写:服务排在直连后,规则:精确域名一条、通配后缀一条,停用的丢掉", obj{"dns": obj{"mode": "dnsmasq", "rewrite": rw}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5"), dnsRewriteServer}, rwRules, "ipv4_only"},
		{"DNS 重写总开关关着:不生成服务和规则", obj{"dns": obj{"mode": "dnsmasq", "rewrite": obj{"enabled": false, "rules": rw["rules"]}}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5")}, nil, "ipv4_only"},
		{"DNS 重写开着但没有有效规则:不生成", obj{"dns": obj{"mode": "dnsmasq", "rewrite": obj{"rules": []any{obj{"source": "a.example.com"}, obj{"domain": "b.example.com"}}}}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5")}, nil, "ipv4_only"},
		{"DNS 重写的规则全停用:不生成", obj{"dns": obj{"mode": "dnsmasq", "rewrite": obj{"rules": []any{obj{"source": "a.example.com", "domain": "b.example.com", "enabled": false}}}}}, DNSOptions{},
			[]any{dnsDirect("223.5.5.5")}, nil, "ipv4_only"},

		// 域名过滤规则
		{"域名过滤规则原样接在后面", obj{"dns": obj{"mode": "dnsmasq"}}, DNSOptions{FilterRules: filter},
			[]any{dnsDirect("223.5.5.5")}, filter, "ipv4_only"},
		{"空的过滤规则不生成 rules", obj{"dns": obj{"mode": "dnsmasq"}}, DNSOptions{FilterRules: []any{}},
			[]any{dnsDirect("223.5.5.5")}, nil, "ipv4_only"},

		// 三类规则同时在:顺序 重写 → 本地主机名 → 过滤;服务顺序 直连 → 重写 → 本地
		{"重写 + 本地主机名 + 过滤:规则顺序与服务顺序", obj{"dns": obj{"rewrite": rw}}, DNSOptions{FilterRules: filter},
			[]any{dnsDirect("223.5.5.5"), dnsRewriteServer, dnsLocalServer},
			[]any{rwRules[0], rwRules[1], dnsLocalRules[0], dnsLocalRules[1], filter[0]}, "ipv4_only"},
		{"本地主机名 + 过滤", obj{"dns": obj{}}, DNSOptions{FilterRules: filter},
			[]any{dnsDirect("223.5.5.5"), dnsLocalServer},
			[]any{dnsLocalRules[0], dnsLocalRules[1], filter[0]}, "ipv4_only"},
	}
	for _, c := range cases {
		res, err := BuildDNS(c.p, c.o)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		want := map[string]any{"servers": c.wantS, "final": "dns-direct", "strategy": c.strat, "reverse_mapping": true}
		if c.wantR != nil {
			want["rules"] = c.wantR
		}
		if !reflect.DeepEqual(res.DNS, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, res.DNS, want)
		}
		// 解析器映射:全是空的,兜底是 dns-direct;没有规则归属
		wantRes := Resolvers{Policies: map[string]string{}, Custom: []any{}, Clients: []ClientResolver{}, Fallback: "dns-direct"}
		if !reflect.DeepEqual(res.Resolvers, wantRes) {
			t.Errorf("%s:Resolvers = %#v", c.name, res.Resolvers)
		}
		if res.RuleOwners == nil || len(res.RuleOwners) != 0 {
			t.Errorf("%s:RuleOwners 应是非 nil 空切片:%#v", c.name, res.RuleOwners)
		}
	}
}

// 这条短路径不看站点集、终端分流、选择、备用上游等其余选项。
func TestBuildDNSNoSplitIgnoresRoutingOptions(t *testing.T) {
	p := obj{"dns": obj{"mode": "dnsmasq"}, "routing": obj{"policies": []any{obj{"id": "p", "name": "流媒体", "domain": []any{"a.com"}}}}}
	o := DNSOptions{
		GroupTags:      []string{"direct", "美国"},
		Builtin:        builtinTags{"direct", "block", true, true},
		Selections:     map[string]any{"流媒体": "美国"},
		DirectDomains:  []string{"sub.example.com"},
		DirectHostSets: &DirectHostSets{Domain: "hs-domain", IP: "hs-ip"},
		KnownOutbounds: map[string]bool{},
	}
	res, err := BuildDNS(p, o)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"servers": []any{dnsDirect("223.5.5.5")}, "final": "dns-direct", "strategy": "ipv4_only", "reverse_mapping": true}
	if !reflect.DeepEqual(res.DNS, want) {
		t.Errorf("got %#v", res.DNS)
	}
	if len(res.Resolvers.Policies) != 0 || len(res.RuleOwners) != 0 {
		t.Errorf("不该有站点集解析器或规则归属:%#v %#v", res.Resolvers, res.RuleOwners)
	}
}
