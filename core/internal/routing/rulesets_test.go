package routing

import (
	"encoding/json"
	"reflect"
	"regexp"
	"testing"
)

// 自写测试向量:输入按 rulesets.go 注释里写的规则自己拟,期望值人工推算,不来自别的程序的输出。
// dns_golden / route_golden 里覆盖到规则集函数的条目仍作回归。

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v) // map 按键名排序,逐字节可比
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRuleListIPTag(t *testing.T) {
	if ruleListIPSuffix != "-ip" {
		t.Errorf("后缀变了:%q", ruleListIPSuffix)
	}
	for in, want := range map[string]string{"list-ab12cd34": "list-ab12cd34-ip", "": "-ip", "geoip-cn": "geoip-cn-ip"} {
		if got := ruleListIPTag(in); got != want {
			t.Errorf("ruleListIPTag(%q) = %q,想要 %q", in, got, want)
		}
	}
}

func TestParseRuleLists(t *testing.T) {
	// 非对象:返回非 nil 的空表
	for _, src := range []string{`null`, `[]`, `["list-a"]`, `"list-a"`, `3`, `true`} {
		got := parseRuleLists(mustJSON(t, src))
		if got == nil || len(got) != 0 {
			t.Errorf("%s:got %#v", src, got)
		}
	}
	if got := parseRuleLists(nil); got == nil || len(got) != 0 {
		t.Errorf("nil:got %#v", got)
	}

	got := parseRuleLists(mustJSON(t, `{
		"list-a":{"domain":true,"ip":false},
		"list-b":{"ip":1},
		"list-c":{},
		"list-d":null,
		"list-e":false,
		"list-f":0,
		"list-g":"",
		"list-h":"x",
		"list-i":{"domain":"y","ip":"0"},
		"list-j":{"domain":0,"ip":""},
		"list-k":[],
		"list-l":{"domain":true,"ip":true}
	}`))
	want := ruleLists{
		"list-a": {domain: true, ip: false},
		"list-b": {domain: false, ip: true},
		"list-c": {},                       // 空对象是真值:有形状信息,只是两份都没有
		"list-h": {},                       // 非空字符串是真值,但读不出键
		"list-i": {domain: true, ip: true}, // 非空字符串 "0" 在 JS 里是真值
		"list-j": {},
		"list-k": {}, // 空数组也是真值
		"list-l": {domain: true, ip: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	// 假值整项不收:null / false / 0 / ""
	for _, k := range []string{"list-d", "list-e", "list-f", "list-g"} {
		if _, has := got[k]; has {
			t.Errorf("%s 是假值,不该有形状", k)
		}
	}
}

func TestRouteRulesetTags(t *testing.T) {
	lists := ruleLists{
		"list-d":    {domain: true},
		"list-i":    {ip: true},
		"list-both": {domain: true, ip: true},
		"list-none": {},
	}
	cases := []struct {
		name string
		in   []string
		l    ruleLists
		want []string
	}{
		{"非规则集链接原样,保序", []string{"geosite-cn", "geoip-cn", "mine"}, lists, []string{"geosite-cn", "geoip-cn", "mine"}},
		{"没有形状信息:按老样子引用一份", []string{"list-x"}, lists, []string{"list-x"}},
		{"表是 nil 也一样", []string{"list-x", "geosite-cn"}, nil, []string{"list-x", "geosite-cn"}},
		{"只有域名那份", []string{"list-d"}, lists, []string{"list-d"}},
		{"只有 IP 那份", []string{"list-i"}, lists, []string{"list-i-ip"}},
		{"两份都有:域名在前 IP 在后", []string{"list-both"}, lists, []string{"list-both", "list-both-ip"}},
		{"有形状但两份都没有:一份也不引用", []string{"list-none"}, lists, []string{}},
		{"混排保序", []string{"geosite-a", "list-both", "geoip-b", "list-i", "list-x", "list-none", "list-d"}, lists,
			[]string{"geosite-a", "list-both", "list-both-ip", "geoip-b", "list-i-ip", "list-x", "list-d"}},
		{"不去重", []string{"list-d", "list-d", "geosite-cn", "geosite-cn"}, lists, []string{"list-d", "list-d", "geosite-cn", "geosite-cn"}},
		{"前缀要在开头:xlist-d 不是规则集链接", []string{"xlist-d"}, lists, []string{"xlist-d"}},
		{"形状表里的名字不是规则集链接时不起作用", []string{"geosite-cn"}, ruleLists{"geosite-cn": {}}, []string{"geosite-cn"}},
		{"空输入", []string{}, lists, []string{}},
		{"nil 输入", nil, lists, []string{}},
	}
	for _, c := range cases {
		got := routeRulesetTags(c.in, c.l)
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:got %#v,想要 %#v", c.name, got, c.want)
		}
	}
}

func TestDNSRulesetTags(t *testing.T) {
	lists := ruleLists{
		"list-d":    {domain: true},
		"list-i":    {ip: true},
		"list-both": {domain: true, ip: true},
		"list-none": {},
	}
	cases := []struct {
		name string
		in   []string
		l    ruleLists
		want []string
	}{
		{"geoip-* 不进 DNS 规则", []string{"geoip-cn", "geosite-cn", "geoip-", "geoip-us"}, lists, []string{"geosite-cn"}},
		{"没有形状信息的规则集链接留着", []string{"list-x"}, lists, []string{"list-x"}},
		{"表是 nil 也留着", []string{"list-x"}, nil, []string{"list-x"}},
		{"有域名那份的留着", []string{"list-d", "list-both"}, lists, []string{"list-d", "list-both"}},
		{"只有 IP 那份的不要", []string{"list-i"}, lists, []string{}},
		{"形状两份都没有的不要", []string{"list-none"}, lists, []string{}},
		{"混排保序", []string{"geosite-a", "list-i", "geoip-b", "list-both", "mine", "list-x", "list-none"}, lists, []string{"geosite-a", "list-both", "mine", "list-x"}},
		{"只拦小写 geoip- 开头", []string{"GEOIP-cn", "geoip", "geoipx-cn", "my-geoip-cn"}, lists, []string{"GEOIP-cn", "geoip", "geoipx-cn", "my-geoip-cn"}},
		{"不去重", []string{"geosite-cn", "geosite-cn"}, lists, []string{"geosite-cn", "geosite-cn"}},
		{"形状表里的名字不是规则集链接时不起作用", []string{"geosite-cn"}, ruleLists{"geosite-cn": {}}, []string{"geosite-cn"}},
		{"空 / nil 输入", nil, lists, []string{}},
	}
	for _, c := range cases {
		got := dnsRulesetTags(c.in, c.l)
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:got %#v,想要 %#v", c.name, got, c.want)
		}
	}
}

func TestPolicyOutboundOptions(t *testing.T) {
	b := builtinTags{direct: "direct", block: "block", directEnabled: true, blockEnabled: true}
	noDirect := builtinTags{direct: "direct", block: "block", directEnabled: false, blockEnabled: true}
	noBlock := builtinTags{direct: "direct", block: "block", directEnabled: true, blockEnabled: false}
	noBoth := builtinTags{direct: "direct", block: "block"}
	renamed := builtinTags{direct: "直连", block: "拒绝", directEnabled: true, blockEnabled: true}
	renamedOff := builtinTags{direct: "直连", block: "拒绝"}

	cases := []struct {
		name string
		in   []string
		b    builtinTags
		want []string
	}{
		{"都启用:原样保序", []string{"美国", "direct", "香港", "block"}, b, []string{"美国", "direct", "香港", "block"}},
		{"停用直连:只去 direct", []string{"美国", "direct", "block"}, noDirect, []string{"美国", "block"}},
		{"停用拒绝:只去 block", []string{"美国", "direct", "block"}, noBlock, []string{"美国", "direct"}},
		{"两个都停用", []string{"direct", "美国", "block", "香港"}, noBoth, []string{"美国", "香港"}},
		{"改名后看新名字:新名字被停用时去掉", []string{"直连", "美国", "拒绝"}, renamedOff, []string{"美国"}},
		{"改名后旧名字当普通成员", []string{"direct", "block", "美国"}, renamedOff, []string{"direct", "block", "美国"}},
		{"改名且启用:原样", []string{"直连", "美国", "拒绝"}, renamed, []string{"直连", "美国", "拒绝"}},
		{"不去重", []string{"美国", "美国"}, b, []string{"美国", "美国"}},
		// 一个都不剩:回落成直连(空成员的组会让内核 FATAL);即便直连本身被停用也回落到它
		{"空表回落直连", []string{}, b, []string{"direct"}},
		{"nil 回落直连", nil, b, []string{"direct"}},
		{"只剩被停用的直连:仍回落直连", []string{"direct"}, noDirect, []string{"direct"}},
		{"只剩被停用的拒绝:回落直连", []string{"block"}, noBlock, []string{"direct"}},
		{"全被停用:回落直连", []string{"direct", "block"}, noBoth, []string{"direct"}},
		{"改名后回落到新名字", nil, renamed, []string{"直连"}},
		{"改名后全被停用:回落新名字", []string{"直连", "拒绝"}, renamedOff, []string{"直连"}},
	}
	for _, c := range cases {
		got := policyOutboundOptions(c.in, c.b)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:got %#v,想要 %#v", c.name, got, c.want)
		}
	}
}

func TestSplitRuleSetConditions(t *testing.T) {
	m := func(src string) map[string]any { return mustJSON(t, src).(map[string]any) }
	cases := []struct {
		name string
		rule string
		want string // 空串表示不拆(返回入参本身)
	}{
		{"没有 rule_set", `{"domain":["a.com"],"outbound":"x"}`, ""},
		{"只有 rule_set", `{"rule_set":["geosite-cn"],"outbound":"x"}`, ""},
		{"rule_set 是假值:不拆", `{"rule_set":null,"domain":["a.com"]}`, ""},
		{"rule_set 是空串:不拆", `{"rule_set":"","domain":["a.com"]}`, ""},
		{"rule_set 是 false:不拆", `{"rule_set":false,"ip_cidr":["1.1.1.0/24"]}`, ""},
		{"rule_set 是 0:不拆", `{"rule_set":0,"ip_cidr":["1.1.1.0/24"]}`, ""},
		{"其他条件(非域名 / IP 目的条件)不触发拆分", `{"rule_set":["a"],"source_ip_cidr":["10.0.0.0/8"],"port":[443],"invert":true,"outbound":"x"}`, ""},
		{"规则集加域名:拆成两条,其余键两边都带",
			`{"rule_set":["geosite-cn"],"domain":["a.com"],"outbound":"x","action":"route"}`,
			`[{"action":"route","outbound":"x","rule_set":["geosite-cn"]},{"action":"route","domain":["a.com"],"outbound":"x"}]`},
		{"规则集加 ip_cidr",
			`{"rule_set":["geoip-cn"],"ip_cidr":["1.1.1.0/24"],"outbound":"x"}`,
			`[{"outbound":"x","rule_set":["geoip-cn"]},{"ip_cidr":["1.1.1.0/24"],"outbound":"x"}]`},
		{"五种目的条件都进后一条",
			`{"rule_set":["a"],"domain":["d"],"domain_suffix":[".s"],"domain_keyword":["k"],"domain_regex":["^r"],"ip_cidr":["10.0.0.0/8"],"inbound":["in"]}`,
			`[{"inbound":["in"],"rule_set":["a"]},{"domain":["d"],"domain_keyword":["k"],"domain_regex":["^r"],"domain_suffix":[".s"],"inbound":["in"],"ip_cidr":["10.0.0.0/8"]}]`},
		{"目的条件只看键在不在:值是空数组也拆",
			`{"rule_set":["a"],"domain":[]}`,
			`[{"rule_set":["a"]},{"domain":[]}]`},
		{"目的条件值是 null 也拆",
			`{"rule_set":["a"],"ip_cidr":null}`,
			`[{"rule_set":["a"]},{"ip_cidr":null}]`},
		{"rule_set 是空数组(真值)加域名:照拆",
			`{"rule_set":[],"domain":["a.com"]}`,
			`[{"rule_set":[]},{"domain":["a.com"]}]`},
		{"rule_set 是非空字符串(真值)加域名:照拆",
			`{"rule_set":"a","domain":["a.com"]}`,
			`[{"rule_set":"a"},{"domain":["a.com"]}]`},
		{"其余键(invert / network 等)两边都带",
			`{"rule_set":["a"],"domain":["d"],"invert":true,"network":["tcp"]}`,
			`[{"invert":true,"network":["tcp"],"rule_set":["a"]},{"domain":["d"],"invert":true,"network":["tcp"]}]`},
	}
	for _, c := range cases {
		in := m(c.rule)
		before := jsonOf(t, in)
		got := splitRuleSetConditions(in)
		if jsonOf(t, in) != before {
			t.Errorf("%s:入参被改了:%s → %s", c.name, before, jsonOf(t, in))
		}
		if c.want == "" {
			if len(got) != 1 || reflect.ValueOf(got[0]).Pointer() != reflect.ValueOf(in).Pointer() {
				t.Errorf("%s:应原样返回入参本身:%s", c.name, jsonOf(t, got))
			}
			continue
		}
		if s := jsonOf(t, got); s != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, s, c.want)
		}
	}

	// 拆出来的两条是新对象,改它们不影响入参
	in := m(`{"rule_set":["a"],"domain":["d"],"outbound":"x"}`)
	out := splitRuleSetConditions(in)
	out[0]["outbound"] = "changed"
	out[1]["outbound"] = "changed"
	if in["outbound"] != "x" {
		t.Errorf("拆出来的规则和入参共用了同一个对象")
	}
}

func TestDomainPresentRegexAndGuard(t *testing.T) {
	if DomainPresentRegex != `^[^:]*[a-z][^:]*$` {
		t.Errorf("DomainPresentRegex = %s", DomainPresentRegex)
	}
	re := regexp.MustCompile(DomainPresentRegex)
	// 「连接有域名」:至少一个小写字母且没有冒号;IP 字面量(IPv4 无字母、IPv6 有冒号)不算
	yes := []string{"example.com", "a", "xn--abc.cn", "localhost", "1.2.3.4a", "a1", "1a", "my-host_1.lan"}
	no := []string{"", "1.2.3.4", "192.168.0.1", "::1", "2001:db8::1", "fe80::1", "[::1]", "a:b", "example.com:80", "EXAMPLE.COM", "123", "-.-"}
	for _, s := range yes {
		if !re.MatchString(s) {
			t.Errorf("%q 应算有域名", s)
		}
	}
	for _, s := range no {
		if re.MatchString(s) {
			t.Errorf("%q 不该算有域名", s)
		}
	}

	g := noDomainGuard()
	if got := jsonOf(t, g); got != `{"domain_regex":["^[^:]*[a-z][^:]*$"],"invert":true}` {
		t.Errorf("noDomainGuard = %s", got)
	}
	// 每次返回新对象
	g["invert"] = false
	if noDomainGuard()["invert"] != true {
		t.Errorf("noDomainGuard 返回了共享对象")
	}
}

func TestIsIPRuleSetTag(t *testing.T) {
	yes := []string{"geoip-cn", "geoip-", "geoip-us", "list-ab12cd34-ip", "list-x-ip"}
	no := []string{
		"geosite-cn", "geoip", "GEOIP-cn", "xgeoip-cn", "my-geoip-cn",
		"list-ab12cd34", "list-ab12cd34-ip-x", "list-ab12cd34-IP",
		"foo-ip", "ab12cd34-ip", "x-ip", "-ip", "", "mine",
	}
	for _, s := range yes {
		if !isIPRuleSetTag(s) {
			t.Errorf("%q 应是看 IP 的规则集", s)
		}
	}
	for _, s := range no {
		if isIPRuleSetTag(s) {
			t.Errorf("%q 不该是看 IP 的规则集", s)
		}
	}
}

func TestPolicyRouteMatches(t *testing.T) {
	lists := ruleLists{
		"list-both": {domain: true, ip: true},
		"list-dom":  {domain: true},
		"list-ip":   {ip: true},
		"list-none": {},
	}
	cases := []struct {
		name    string
		p       Policy
		wantDom string // 空串表示应为 nil
		wantIP  string
	}{
		{
			"全部条件:规则集按看域名 / 看 IP 分家,保持引用顺序",
			Policy{
				Rulesets:      []string{"geosite-cn", "geoip-cn", "list-both", "list-nofo", "list-ip", "list-none", "list-dom"},
				Domain:        []string{"a.com"},
				DomainSuffix:  []string{".b.com"},
				DomainKeyword: []string{"kw"},
				IPCidr:        []string{"10.0.0.0/8", "2001:db8::/32"},
			},
			`{"domain":["a.com"],"domain_keyword":["kw"],"domain_suffix":[".b.com"],"rule_set":["geosite-cn","list-both","list-nofo","list-dom"]}`,
			`{"ip_cidr":["10.0.0.0/8","2001:db8::/32"],"rule_set":["geoip-cn","list-both-ip","list-ip-ip"]}`,
		},
		{"只有域名类条件:IP 一份是 nil", Policy{Domain: []string{"a.com"}}, `{"domain":["a.com"]}`, ""},
		{"只有后缀", Policy{DomainSuffix: []string{".cn"}}, `{"domain_suffix":[".cn"]}`, ""},
		{"只有关键词", Policy{DomainKeyword: []string{"ad"}}, `{"domain_keyword":["ad"]}`, ""},
		{"只有 ip_cidr:域名一份是 nil", Policy{IPCidr: []string{"1.1.1.0/24"}}, "", `{"ip_cidr":["1.1.1.0/24"]}`},
		{"只有 geoip 规则集", Policy{Rulesets: []string{"geoip-cn"}}, "", `{"rule_set":["geoip-cn"]}`},
		{"只有 geosite 规则集", Policy{Rulesets: []string{"geosite-cn"}}, `{"rule_set":["geosite-cn"]}`, ""},
		{"规则集链接只有 IP 那份:只在 IP 一边", Policy{Rulesets: []string{"list-ip"}}, "", `{"rule_set":["list-ip-ip"]}`},
		{"规则集链接只有域名那份:只在域名一边", Policy{Rulesets: []string{"list-dom"}}, `{"rule_set":["list-dom"]}`, ""},
		{"规则集链接两份都没有:哪边都不出现", Policy{Rulesets: []string{"list-none"}}, "", ""},
		{"什么条件都没有", Policy{}, "", ""},
		{"空切片当没有", Policy{Rulesets: []string{}, Domain: []string{}, DomainSuffix: []string{}, DomainKeyword: []string{}, IPCidr: []string{}}, "", ""},
		{"两边都有 rule_set 时各带各的", Policy{Rulesets: []string{"geoip-us", "geosite-us"}}, `{"rule_set":["geosite-us"]}`, `{"rule_set":["geoip-us"]}`},
	}
	for _, c := range cases {
		dom, ip := policyRouteMatches(c.p, lists)
		check := func(side string, got map[string]any, want string) {
			if want == "" {
				if got != nil {
					t.Errorf("%s %s:应为 nil,got %s", c.name, side, jsonOf(t, got))
				}
				return
			}
			if got == nil {
				t.Errorf("%s %s:不该是 nil,想要 %s", c.name, side, want)
				return
			}
			if s := jsonOf(t, got); s != want {
				t.Errorf("%s %s:\n got  %s\n want %s", c.name, side, s, want)
			}
		}
		check("域名", dom, c.wantDom)
		check("IP", ip, c.wantIP)
	}

	// 没有形状表:规则集链接按老样子当看域名的那份
	dom, ip := policyRouteMatches(Policy{Rulesets: []string{"list-x", "geoip-cn"}}, nil)
	if jsonOf(t, dom) != `{"rule_set":["list-x"]}` || jsonOf(t, ip) != `{"rule_set":["geoip-cn"]}` {
		t.Errorf("无形状表:%s / %s", jsonOf(t, dom), jsonOf(t, ip))
	}
}
