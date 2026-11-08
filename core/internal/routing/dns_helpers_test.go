package routing

import (
	"reflect"
	"regexp"
	"testing"
)

// 自写测试向量(dns.go 前半的小函数:常量、缓存路径、地址族 / FakeIP / IPv6 / 模式的读取,
// 自定义行和站点集转 DNS 规则的匹配部分,合并小工具,固定规则):输入按源码注释里写的规则自己拟,
// 期望值人工推算,不来自别的程序的输出。「profile」都是 JSON 解出来的形状(map[string]any / float64 / nil)。
// BuildDNS 本体在后面几步里换;dns_test.go 里的金标准对照仍作回归。

type obj = map[string]any

func TestDNSConstants(t *testing.T) {
	if FakeIPV4 != "198.19.0.0/16" || FakeIPV6 != "fc00::/18" {
		t.Errorf("FakeIP 地址池:%q %q", FakeIPV4, FakeIPV6)
	}
	if fakeIPTag != "dns-fakeip" || ProxyResolverTag != "dns-proxy" || TerminalDirectTag != "dns-terminal-direct" {
		t.Errorf("tag 常量:%q %q %q", fakeIPTag, ProxyResolverTag, TerminalDirectTag)
	}
	if want := []string{".lan", ".local", ".home", ".internal", ".home.arpa"}; !reflect.DeepEqual(localSuffixes, want) {
		t.Errorf("localSuffixes = %v", localSuffixes)
	}
}

func TestFakeIPCachePath(t *testing.T) {
	// 地址池 "198.19.0.0/16-fc00::/18" 里每个非字母数字字符换成 _(. / - : 各一个 _,"::" 是两个)
	const suffix = ".fakeip-198_19_0_0_16_fc00___18"
	for in, want := range map[string]string{
		"/etc/openbox/cache.db": "/etc/openbox/cache.db" + suffix,
		"cache":                 "cache" + suffix,
		"":                      suffix,
		`C:\x y\c.db`:           `C:\x y\c.db` + suffix, // base 本身原样,不被替换
	} {
		if got := FakeIPCachePath(in); got != want {
			t.Errorf("FakeIPCachePath(%q) = %q,想要 %q", in, got, want)
		}
	}
}

func TestDNSStrategy(t *testing.T) {
	cases := []struct {
		name string
		p    any
		want string
	}{
		{"ipv6 为真", obj{"ipv6": true}, "prefer_ipv4"},
		{"ipv6 为真值:非空串", obj{"ipv6": "yes"}, "prefer_ipv4"},
		{"ipv6 为真值:非零数", obj{"ipv6": 1.0}, "prefer_ipv4"},
		{"ipv6 为真值:空数组在 JS 里是真", obj{"ipv6": []any{}}, "prefer_ipv4"},
		{"ipv6 为真值:空对象在 JS 里是真", obj{"ipv6": obj{}}, "prefer_ipv4"},
		{"ipv6 为 false", obj{"ipv6": false}, "ipv4_only"},
		{"ipv6 为 0", obj{"ipv6": 0.0}, "ipv4_only"},
		{"ipv6 为空串", obj{"ipv6": ""}, "ipv4_only"},
		{"ipv6 为 null", obj{"ipv6": nil}, "ipv4_only"},
		{"没写 ipv6", obj{}, "ipv4_only"},
		{"profile 是 null", nil, "ipv4_only"},
		{"profile 不是对象", "x", "ipv4_only"},
		{"profile 是数组", []any{true}, "ipv4_only"},
	}
	for _, c := range cases {
		if got := DNSStrategy(c.p); got != c.want {
			t.Errorf("%s:got %q,想要 %q", c.name, got, c.want)
		}
	}
}

func TestDNSFakeIPEnabled(t *testing.T) {
	d := func(dns any) any { return obj{"dns": dns} }
	cases := []struct {
		name string
		p    any
		want bool
	}{
		{"没开", d(obj{"fakeIpForProxy": false}), false},
		{"只开 fakeIpForProxy,split 没写(缺省按开)", d(obj{"fakeIpForProxy": true}), true},
		{"两个都明确开", d(obj{"split": true, "fakeIpForProxy": true}), true},
		{"split 明确关:FakeIP 不开", d(obj{"split": false, "fakeIpForProxy": true}), false},
		{"split 为 null 不等于 false:仍开", d(obj{"split": nil, "fakeIpForProxy": true}), true},
		{"split 为 0 不等于 false:仍开", d(obj{"split": 0.0, "fakeIpForProxy": true}), true},
		{"split 为空串不等于 false:仍开", d(obj{"split": "", "fakeIpForProxy": true}), true},
		{"fakeIpForProxy 必须严格为 true:字符串", d(obj{"fakeIpForProxy": "true"}), false},
		{"fakeIpForProxy 必须严格为 true:数字 1", d(obj{"fakeIpForProxy": 1.0}), false},
		{"fakeIpForProxy 为 null", d(obj{"fakeIpForProxy": nil}), false},
		{"dns 是空对象", d(obj{}), false},
		{"dns 为 null", d(nil), false},
		{"dns 没写", obj{}, false},
		{"dns 是真值但不是对象:字符串", d("x"), false},
		{"dns 是真值但不是对象:true", d(true), false},
		{"profile 为 null", nil, false},
	}
	for _, c := range cases {
		if got := DNSFakeIPEnabled(c.p); got != c.want {
			t.Errorf("%s:got %v,想要 %v", c.name, got, c.want)
		}
	}
}

func TestIPv6ProxyMode(t *testing.T) {
	cases := []struct {
		name string
		p    any
		want string
	}{
		{"没开 ipv6", obj{"ipv6": false, "ipv6Proxy": "ipv4"}, "off"},
		{"ipv6 没写", obj{"ipv6Proxy": "bypass"}, "off"},
		{"profile 为 null", nil, "off"},
		{"profile 不是对象", "x", "off"},
		{"开 ipv6,没写模式:交给节点", obj{"ipv6": true}, "node"},
		{"模式 node", obj{"ipv6": true, "ipv6Proxy": "node"}, "node"},
		{"模式 ipv4", obj{"ipv6": true, "ipv6Proxy": "ipv4"}, "ipv4"},
		{"模式 bypass", obj{"ipv6": true, "ipv6Proxy": "bypass"}, "bypass"},
		{"大小写敏感:IPV4 当未知,落 node", obj{"ipv6": true, "ipv6Proxy": "IPV4"}, "node"},
		{"未知字符串落 node", obj{"ipv6": true, "ipv6Proxy": "other"}, "node"},
		{"null 落 node", obj{"ipv6": true, "ipv6Proxy": nil}, "node"},
		{"数字落 node", obj{"ipv6": true, "ipv6Proxy": 1.0}, "node"},
		{"ipv6 为真值非 bool", obj{"ipv6": "on", "ipv6Proxy": "ipv4"}, "ipv4"},
	}
	for _, c := range cases {
		if got := IPv6ProxyMode(c.p); got != c.want {
			t.Errorf("%s:got %q,想要 %q", c.name, got, c.want)
		}
	}
}

func TestIPv6InTun(t *testing.T) {
	cases := []struct {
		name string
		p    any
		want bool
	}{
		{"没开 ipv6", obj{"ipv6": false}, false},
		{"profile 为 null", nil, false},
		{"node", obj{"ipv6": true}, true},
		{"ipv4(降级也还在内核里)", obj{"ipv6": true, "ipv6Proxy": "ipv4"}, true},
		{"bypass(不进内核)", obj{"ipv6": true, "ipv6Proxy": "bypass"}, false},
		{"没开 ipv6 时模式不起作用", obj{"ipv6": false, "ipv6Proxy": "node"}, false},
	}
	for _, c := range cases {
		if got := IPv6InTun(c.p); got != c.want {
			t.Errorf("%s:got %v,想要 %v", c.name, got, c.want)
		}
	}
}

func TestDNSMode(t *testing.T) {
	d := func(mode any) any { return obj{"dns": obj{"mode": mode}} }
	cases := []struct {
		name string
		p    any
		want string
	}{
		{"hijack", d("hijack"), "hijack"},
		{"dnsmasq", d("dnsmasq"), "dnsmasq"},
		{"未知字符串原样带出", d("other"), "other"},
		{"大小写不变", d("DNSMASQ"), "DNSMASQ"},
		{"空串按缺省 hijack", d(""), "hijack"},
		{"0 按缺省", d(0.0), "hijack"},
		{"false 按缺省", d(false), "hijack"},
		{"null 按缺省", d(nil), "hijack"},
		{"mode 没写", obj{"dns": obj{}}, "hijack"},
		{"dns 为 null", obj{"dns": nil}, "hijack"},
		{"dns 没写", obj{}, "hijack"},
		{"profile 为 null", nil, "hijack"},
		{"非字符串的真值:数字", d(5.0), "\x00non-string"},
		{"非字符串的真值:true", d(true), "\x00non-string"},
		{"非字符串的真值:空数组", d([]any{}), "\x00non-string"},
		{"非字符串的真值:空对象", d(obj{}), "\x00non-string"},
	}
	for _, c := range cases {
		if got := DNSMode(c.p); got != c.want {
			t.Errorf("%s:got %q,想要 %q", c.name, got, c.want)
		}
	}
}

func TestCustomDnsMatch(t *testing.T) {
	const tag = "list-67c9a30d" // https://a.com/x.list 的规则集名(FNV-1a,独立 Python 另算)
	const url = "https://a.com/x.list"
	rule := func(typ, value string) CustomRule { return CustomRule{Type: typ, Value: value, Outbound: "direct"} }
	domainOnly := ruleLists{tag: {domain: true}}
	ipOnly := ruleLists{tag: {ip: true}}
	both := ruleLists{tag: {domain: true, ip: true}}
	cases := []struct {
		name string
		r    CustomRule
		l    ruleLists
		want map[string]any
	}{
		{"域名", rule("domain", "a.com"), nil, obj{"domain": []string{"a.com"}}},
		{"域名后缀", rule("domainSuffix", "a.com"), nil, obj{"domain_suffix": []string{"a.com"}}},
		{"域名关键词", rule("domainKeyword", "ad"), nil, obj{"domain_keyword": []string{"ad"}}},
		{"geosite 按名字引规则集", rule("geosite", "cn"), nil, obj{"rule_set": []string{"geosite-cn"}}},
		{"ruleset 原样引用", rule("ruleset", "my-set"), nil, obj{"rule_set": []string{"my-set"}}},
		{"ruleset 引 geoip 名字:DNS 里不用 IP 集,不进", rule("ruleset", "geoip-cn"), nil, nil},
		{"ipCidr 不进 DNS", rule("ipCidr", "1.1.1.0/24"), nil, nil},
		{"geoip 不进 DNS", rule("geoip", "cn"), nil, nil},
		{"port 不进 DNS", rule("port", "443"), nil, nil},
		{"未知类型", rule("zzz", "a"), nil, nil},
		{"类型为空串", rule("", "a"), nil, nil},
		{"类型大小写敏感:Domain", rule("Domain", "a.com"), nil, nil},
		{"ruleUrl 没有形状信息:照引", rule("ruleUrl", url), nil, obj{"rule_set": []string{tag}}},
		{"ruleUrl 只有域名那份", rule("ruleUrl", url), domainOnly, obj{"rule_set": []string{tag}}},
		{"ruleUrl 两份都有", rule("ruleUrl", url), both, obj{"rule_set": []string{tag}}},
		{"ruleUrl 只有 IP 那份:DNS 里没得用,整条不进", rule("ruleUrl", url), ipOnly, nil},
		{"ruleUrl 前后空白先去掉", rule("ruleUrl", "  "+url+"\n"), domainOnly, obj{"rule_set": []string{tag}}},
		{"ruleUrl 别的链接不受这张表影响", rule("ruleUrl", "https://zzz.example/other"), ipOnly, obj{"rule_set": []string{ListTagForUrl("https://zzz.example/other")}}},
	}
	for _, c := range cases {
		got := customDnsMatch(c.r, c.l)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:got %#v,想要 %#v", c.name, got, c.want)
		}
	}
}

func TestPolicyDnsRule(t *testing.T) {
	l := ruleLists{"list-ip": {ip: true}, "list-dom": {domain: true}, "list-both": {domain: true, ip: true}}
	cases := []struct {
		name   string
		p      Policy
		server string
		want   map[string]any
	}{
		{"全空:空 map,不带 server", Policy{}, "", obj{}},
		{"只有 server", Policy{}, "dns-direct", obj{"server": "dns-direct"}},
		{"server 为空串不写这个键", Policy{Domain: []string{"a.com"}}, "", obj{"domain": []string{"a.com"}}},
		{"域名三类各自对应的键", Policy{Domain: []string{"a.com"}, DomainSuffix: []string{"b.com"}, DomainKeyword: []string{"kw"}}, "s",
			obj{"server": "s", "domain": []string{"a.com"}, "domain_suffix": []string{"b.com"}, "domain_keyword": []string{"kw"}}},
		{"ip_cidr 不进 DNS 规则", Policy{IPCidr: []string{"1.1.1.0/24"}}, "s", obj{"server": "s"}},
		{"规则集按顺序,geoip 的丢掉", Policy{Rulesets: []string{"geosite-b", "geoip-cn", "geosite-a"}}, "", obj{"rule_set": []string{"geosite-b", "geosite-a"}}},
		{"规则集全是 geoip:不写 rule_set", Policy{Rulesets: []string{"geoip-cn", "geoip-us"}}, "", obj{}},
		{"规则集链接只有 IP 那份:丢掉", Policy{Rulesets: []string{"list-ip"}}, "", obj{}},
		{"规则集链接有域名那份:留下", Policy{Rulesets: []string{"list-dom", "list-both"}}, "", obj{"rule_set": []string{"list-dom", "list-both"}}},
		{"规则集链接没有形状信息:留下", Policy{Rulesets: []string{"list-unknown"}}, "", obj{"rule_set": []string{"list-unknown"}}},
		{"空切片的条件不写键", Policy{Domain: []string{}, DomainSuffix: []string{}, Rulesets: []string{}}, "", obj{}},
		{"全带", Policy{Rulesets: []string{"geosite-cn"}, Domain: []string{"a"}, DomainSuffix: []string{"b"}, DomainKeyword: []string{"c"}, IPCidr: []string{"1.1.1.1/32"}}, "x",
			obj{"server": "x", "rule_set": []string{"geosite-cn"}, "domain": []string{"a"}, "domain_suffix": []string{"b"}, "domain_keyword": []string{"c"}}},
	}
	for _, c := range cases {
		got := policyDnsRule(c.p, c.server, l)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, got, c.want)
		}
	}
	if got := policyDnsRule(Policy{}, "", nil); got == nil {
		t.Error("应返回非 nil 的空 map")
	}
}

func TestHasDomainCondition(t *testing.T) {
	l := ruleLists{"list-ip": {ip: true}, "list-dom": {domain: true}}
	cases := []struct {
		name string
		p    Policy
		want bool
	}{
		{"全空", Policy{}, false},
		{"只有 IP 段", Policy{IPCidr: []string{"1.1.1.0/24"}}, false},
		{"只有 geoip 规则集", Policy{Rulesets: []string{"geoip-cn"}}, false},
		{"只有 IP 那份的规则集链接", Policy{Rulesets: []string{"list-ip"}}, false},
		{"geosite 规则集", Policy{Rulesets: []string{"geosite-cn"}}, true},
		{"有域名那份的规则集链接", Policy{Rulesets: []string{"list-dom"}}, true},
		{"没有形状信息的规则集链接", Policy{Rulesets: []string{"list-new"}}, true},
		{"geoip 与 geosite 混着,有 geosite", Policy{Rulesets: []string{"geoip-cn", "geosite-cn"}}, true},
		{"域名", Policy{Domain: []string{"a.com"}}, true},
		{"域名后缀", Policy{DomainSuffix: []string{"a.com"}}, true},
		{"域名关键词", Policy{DomainKeyword: []string{"a"}}, true},
		{"空切片不算", Policy{Domain: []string{}, DomainSuffix: []string{}, DomainKeyword: []string{}, Rulesets: []string{}}, false},
	}
	for _, c := range cases {
		if got := hasDomainCondition(c.p, l); got != c.want {
			t.Errorf("%s:got %v,想要 %v", c.name, got, c.want)
		}
	}
}

func TestWithKeysAndMerge(t *testing.T) {
	base := obj{"a": 1, "b": 2}
	got := withKeys(base, "b", 20, "c", 3)
	if want := (obj{"a": 1, "b": 20, "c": 3}); !reflect.DeepEqual(got, want) {
		t.Errorf("withKeys:got %#v,想要 %#v", got, want)
	}
	if !reflect.DeepEqual(base, obj{"a": 1, "b": 2}) {
		t.Errorf("withKeys 改了原 map:%#v", base)
	}
	if got := withKeys(base, "x", 1, "y"); !reflect.DeepEqual(got, obj{"a": 1, "b": 2, "x": 1}) {
		t.Errorf("withKeys 末尾落单的键应被忽略:%#v", got)
	}
	if got := withKeys(nil, "k", "v"); !reflect.DeepEqual(got, obj{"k": "v"}) {
		t.Errorf("withKeys(nil base):%#v", got)
	}
	if got := withKeys(nil); got == nil || len(got) != 0 {
		t.Errorf("withKeys(nil) 应是非 nil 空 map:%#v", got)
	}
	// 后面的键覆盖前面的
	if got := withKeys(nil, "k", 1, "k", 2); got["k"] != 2 {
		t.Errorf("同键后写的赢:%#v", got)
	}

	a, b := obj{"x": 1, "y": 2}, obj{"y": 20, "z": 30}
	m := merge(a, b)
	if want := (obj{"x": 1, "y": 20, "z": 30}); !reflect.DeepEqual(m, want) {
		t.Errorf("merge:got %#v,想要 %#v", m, want)
	}
	if !reflect.DeepEqual(a, obj{"x": 1, "y": 2}) || !reflect.DeepEqual(b, obj{"y": 20, "z": 30}) {
		t.Errorf("merge 改了入参:%#v %#v", a, b)
	}
	m["x"] = 99 // 结果是新 map,不连着入参
	if a["x"] != 1 {
		t.Error("merge 的结果不该和入参共用同一个 map")
	}
	if got := merge(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("merge(nil,nil) 应是非 nil 空 map:%#v", got)
	}
	if got := merge(nil, b); !reflect.DeepEqual(got, b) {
		t.Errorf("merge(nil,b):%#v", got)
	}
	if got := merge(a, nil); !reflect.DeepEqual(got, a) {
		t.Errorf("merge(a,nil):%#v", got)
	}
}

func TestExtraTag(t *testing.T) {
	for _, c := range []struct {
		tag  string
		i    int
		want string
	}{
		{"dns-proxy", 0, "dns-proxy__alt1"},
		{"dns-proxy", 1, "dns-proxy__alt2"},
		{"dns-direct", 9, "dns-direct__alt10"},
		{"", 0, "__alt1"},
	} {
		if got := extraTag(c.tag, c.i); got != c.want {
			t.Errorf("extraTag(%q,%d) = %q,想要 %q", c.tag, c.i, got, c.want)
		}
	}
}

func TestHTTPSRRRule(t *testing.T) {
	want := obj{"query_type": []string{"HTTPS", "SVCB"}, "action": "predefined", "rcode": "NOERROR"}
	got := httpsRRRule()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("httpsRRRule:got %#v,想要 %#v", got, want)
	}
	got["rcode"] = "X" // 每次返回新 map
	if again := httpsRRRule(); again["rcode"] != "NOERROR" {
		t.Error("httpsRRRule 不该返回共用的 map")
	}
}

func TestLocalNameRules(t *testing.T) {
	want := []any{
		obj{"domain_suffix": []string{".lan", ".local", ".home", ".internal", ".home.arpa"}, "server": "dns-local"},
		obj{"domain_regex": []string{"^[^.]+$"}, "server": "dns-local"},
	}
	if got := localNameRules("hijack"); !reflect.DeepEqual(got, want) {
		t.Errorf("hijack:\n got %#v\nwant %#v", got, want)
	}
	for _, mode := range []string{"dnsmasq", "", "Hijack", "other", "\x00non-string"} {
		got := localNameRules(mode)
		if got == nil || len(got) != 0 {
			t.Errorf("localNameRules(%q) = %#v,想要非 nil 空切片", mode, got)
		}
	}
	// 第二条的正则:只有不含点的单标签主机名命中
	re := regexp.MustCompile(`^[^.]+$`)
	for name, want := range map[string]bool{"nas": true, "router": true, "a.lan": false, "a.b": false, "": false} {
		if re.MatchString(name) != want {
			t.Errorf("单标签正则对 %q 应为 %v", name, want)
		}
	}
}
