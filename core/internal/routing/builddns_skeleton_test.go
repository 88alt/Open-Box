package routing

import (
	"reflect"
	"testing"
)

// 自写测试向量:BuildDNS 分流开着(dns.split 为真)、但没有生效的站点集 / 自定义分流 / 终端分流时的骨架。
// 输入自己拟,期望值按 dns.go 里的规则人工推算,不来自别的程序的输出。这条路径只剩:
//   服务器:直连解析器 → 直连备用 → DNS 重写 → 代理解析器 → 代理备用 → FakeIP → 本地主机名;
//   规则:DNS 重写 → 本地主机名 → HTTPS/SVCB 空应答 → 域名过滤 → 兜底段(代理 v6 降级的 AAAA 空应答 → FakeIP 占位 →
//        挂着兜底开关的代理支 → 直连备用的无条件竞速)。
// 站点集、前置自定义分流、终端分流、订阅 / 节点站点直连的规则块在后面几步里换。

const skFlag = "obflip-fallback"

var skHTTPS = obj{"query_type": []string{"HTTPS", "SVCB"}, "action": "predefined", "rcode": "NOERROR"}

func skSwitch() obj { return obj{"rule_set": []string{skFlag}} }

// skAnd:代理 / 占位那类挂着开关的 logical AND 规则。
func skAnd(tail obj, subs ...obj) obj {
	rules := []any{}
	for _, s := range subs {
		rules = append(rules, s)
	}
	out := obj{"type": "logical", "mode": "and", "rules": rules}
	for k, v := range tail {
		out[k] = v
	}
	return out
}

func skEval(match obj, server, tag string) obj {
	out := obj{"action": "evaluate", "server": server, "tag": tag}
	for k, v := range match {
		out[k] = v
	}
	return out
}

func skRespond(tag string) obj {
	return obj{"match_response": tag, "response_rcode": "NOERROR", "action": "respond", "race": true}
}

func skProfile(dns obj, top obj) obj {
	d := obj{"split": true}
	for k, v := range dns {
		d[k] = v
	}
	p := obj{"dns": d}
	for k, v := range top {
		p[k] = v
	}
	return p
}

func skDM(extra obj) obj { // dnsmasq 模式:没有本地主机名,骨架最短
	d := obj{"mode": "dnsmasq"}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

var (
	skProxy      = obj{"type": "tcp", "tag": "dns-proxy", "server": "1.1.1.1", "detour": "其他"}
	skFbProxy    = obj{"rule_set": []string{skFlag}, "server": "dns-proxy"}
	skFake4      = obj{"type": "fakeip", "tag": "dns-fakeip", "inet4_range": "198.19.0.0/16"}
	skFake46     = obj{"type": "fakeip", "tag": "dns-fakeip", "inet4_range": "198.19.0.0/16", "inet6_range": "fc00::/18"}
	skFakeRuleA  = skAnd(obj{"server": "dns-fakeip"}, skSwitch(), obj{"query_type": []string{"A"}})
	skFakeRuleAA = skAnd(obj{"server": "dns-fakeip"}, skSwitch(), obj{"query_type": []string{"A", "AAAA"}})
	skEmptyAAAA  = skAnd(obj{"action": "predefined", "rcode": "NOERROR"}, skSwitch(), obj{"query_type": []string{"AAAA"}})
	skFilter     = obj{"domain": []string{"ads.example"}, "action": "reject"}
	skRW         = obj{"rules": []any{
		obj{"source": "a.example.com", "domain": "b.example.com"},
		obj{"source": "*.corp.example", "addresses": []any{"10.0.0.5"}},
	}}
	skRWRules = []any{
		obj{"domain": []any{"a.example.com"}, "server": "dns-rewrite"},
		obj{"domain_suffix": []any{".corp.example"}, "server": "dns-rewrite"},
	}
)

func skOwner(from, to int, name string) []RuleOwner {
	return []RuleOwner{{From: from, To: to, Kind: "fallback", Index: 0, Name: name}}
}

func skDirectTag(server, tag string) obj { return obj{"type": "udp", "tag": tag, "server": server} }

func skOpts() DNSOptions { return DNSOptions{Builtin: defaultBuiltin} }

func TestBuildDNSSplitSkeleton(t *testing.T) {
	cases := []struct {
		name  string
		p     any
		o     DNSOptions
		wantS []any
		wantR []any
		strat string
		fb    string // Resolvers.Fallback
		owns  []RuleOwner
	}{
		// ---- 基本骨架 ----
		{"劫持模式缺省:本地主机名两条规则在前,HTTPS 空应答,兜底代理支;本地服务器排在最后",
			skProfile(obj{}, nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, dnsLocalServer},
			[]any{dnsLocalRules[0], dnsLocalRules[1], skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(3, 4, "其他")},
		{"dnsmasq 模式:没有本地规则也没有本地服务器",
			skProfile(skDM(nil), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy},
			[]any{skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 2, "其他")},
		{"未知模式与非字符串模式按非劫持",
			skProfile(obj{"mode": "other"}, nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy},
			[]any{skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 2, "其他")},
		{"开 IPv6 且代理 v6 交给节点、没开 FakeIP:只改地址族",
			skProfile(skDM(nil), obj{"ipv6": true}), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy},
			[]any{skHTTPS, skFbProxy},
			"prefer_ipv4", "dns-direct", skOwner(1, 2, "其他")},

		// ---- DNS 重写、域名过滤 ----
		{"DNS 重写服务排在直连之后、代理之前;重写规则排最前",
			skProfile(skDM(obj{"rewrite": skRW}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), dnsRewriteServer, skProxy},
			[]any{skRWRules[0], skRWRules[1], skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(3, 4, "其他")},
		{"域名过滤规则接在 HTTPS 空应答之后、兜底段之前",
			skProfile(skDM(nil), nil), DNSOptions{Builtin: defaultBuiltin, FilterRules: []any{skFilter}},
			[]any{dnsDirect("223.5.5.5"), skProxy},
			[]any{skHTTPS, skFilter, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(2, 3, "其他")},
		{"劫持模式下重写 + 本地主机名 + HTTPS + 过滤 + 兜底的整体顺序",
			skProfile(obj{"rewrite": skRW}, nil), DNSOptions{Builtin: defaultBuiltin, FilterRules: []any{skFilter}},
			[]any{dnsDirect("223.5.5.5"), dnsRewriteServer, skProxy, dnsLocalServer},
			[]any{skRWRules[0], skRWRules[1], dnsLocalRules[0], dnsLocalRules[1], skHTTPS, skFilter, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(6, 7, "其他")},

		// ---- 直连 / 代理上游 ----
		{"直连上游自定义:tcp 5353 带 server_port;代理解析器不受影响",
			skProfile(skDM(obj{"direct": "119.29.29.29", "directProtocol": "tcp", "directPort": 5353.0}), nil), skOpts(),
			[]any{obj{"type": "tcp", "tag": "dns-direct", "server": "119.29.29.29", "server_port": 5353}, skProxy},
			[]any{skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 2, "其他")},
		{"代理上游自定义:udp 5353,detour 仍是兜底名",
			skProfile(skDM(obj{"proxy": "8.8.4.4", "proxyProtocol": "udp", "proxyPort": 5353.0}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), obj{"type": "udp", "tag": "dns-proxy", "server": "8.8.4.4", "server_port": 5353, "detour": "其他"}},
			[]any{skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 2, "其他")},
		{"代理上游端口 53 不写 server_port;协议缺省 tcp",
			skProfile(skDM(obj{"proxy": "9.9.9.9", "proxyPort": 53.0}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), obj{"type": "tcp", "tag": "dns-proxy", "server": "9.9.9.9", "detour": "其他"}},
			[]any{skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 2, "其他")},
		{"代理上游写了域名(不合法):退成 1.1.1.1",
			skProfile(skDM(obj{"proxy": "dns.example.com"}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy},
			[]any{skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 2, "其他")},
		{"代理上游写 wan:取系统第一个真值,固定 udp 53,不写 detour;直连缺省也是 wan",
			skProfile(skDM(obj{"proxy": "wan"}), nil), DNSOptions{Builtin: defaultBuiltin, SystemDNS: []any{nil, "10.0.0.1", "10.0.0.2"}},
			[]any{dnsDirect("10.0.0.1"), obj{"type": "udp", "tag": "dns-proxy", "server": "10.0.0.1"}},
			[]any{skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 2, "其他")},

		// ---- 兜底改名 ----
		{"兜底改名:代理解析器的 detour、规则归属的名字都跟着改",
			skProfile(skDM(nil), obj{"routing": obj{"fallbackName": "兜底"}}), skOpts(),
			[]any{dnsDirect("223.5.5.5"), obj{"type": "tcp", "tag": "dns-proxy", "server": "1.1.1.1", "detour": "兜底"}},
			[]any{skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 2, "兜底")},

		// ---- FakeIP 与 IPv6 分层 ----
		{"FakeIP 开、没开 IPv6:占位服务只有 v4 段,占位规则只管 A,排在兜底代理支前",
			skProfile(skDM(obj{"fakeIpForProxy": true}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, skFake4},
			[]any{skHTTPS, skFakeRuleA, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 3, "其他")},
		{"FakeIP 开、劫持模式:占位服务排在本地服务器前",
			skProfile(obj{"fakeIpForProxy": true}, nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, skFake4, dnsLocalServer},
			[]any{dnsLocalRules[0], dnsLocalRules[1], skHTTPS, skFakeRuleA, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(3, 5, "其他")},
		{"fakeIpForProxy 必须严格等于 true:字符串 / 1 都不算",
			skProfile(skDM(obj{"fakeIpForProxy": "true"}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy},
			[]any{skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 2, "其他")},
		{"FakeIP + IPv6 交给节点:占位服务带 v6 段,规则管 A 和 AAAA",
			skProfile(skDM(obj{"fakeIpForProxy": true}), obj{"ipv6": true}), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, skFake46},
			[]any{skHTTPS, skFakeRuleAA, skFbProxy},
			"prefer_ipv4", "dns-direct", skOwner(1, 3, "其他")},
		{"ipv6Proxy 写别的值按交给节点",
			skProfile(skDM(obj{"fakeIpForProxy": true}), obj{"ipv6": true, "ipv6Proxy": "whatever"}), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, skFake46},
			[]any{skHTTPS, skFakeRuleAA, skFbProxy},
			"prefer_ipv4", "dns-direct", skOwner(1, 3, "其他")},
		{"FakeIP + IPv6 降为 IPv4:先 AAAA 空应答,再只管 A 的占位,占位服务没有 v6 段",
			skProfile(skDM(obj{"fakeIpForProxy": true}), obj{"ipv6": true, "ipv6Proxy": "ipv4"}), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, skFake4},
			[]any{skHTTPS, skEmptyAAAA, skFakeRuleA, skFbProxy},
			"prefer_ipv4", "dns-direct", skOwner(1, 4, "其他")},
		{"FakeIP + IPv6 不进内核:不给 v6 占位,也不做 AAAA 空应答",
			skProfile(skDM(obj{"fakeIpForProxy": true}), obj{"ipv6": true, "ipv6Proxy": "bypass"}), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, skFake4},
			[]any{skHTTPS, skFakeRuleA, skFbProxy},
			"prefer_ipv4", "dns-direct", skOwner(1, 3, "其他")},
		{"没开 FakeIP、IPv6 降为 IPv4:只有 AAAA 空应答",
			skProfile(skDM(nil), obj{"ipv6": true, "ipv6Proxy": "ipv4"}), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy},
			[]any{skHTTPS, skEmptyAAAA, skFbProxy},
			"prefer_ipv4", "dns-direct", skOwner(1, 3, "其他")},
		{"没开 IPv6 时 ipv6Proxy 不起作用",
			skProfile(skDM(obj{"fakeIpForProxy": true}), obj{"ipv6Proxy": "ipv4"}), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, skFake4},
			[]any{skHTTPS, skFakeRuleA, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 3, "其他")},

		// ---- 备用上游竞速 ----
		{"直连备用 1 个:服务排在主直连后;兜底段末尾补无条件竞速(evaluate 各一条,respond 各一条)",
			skProfile(skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"), skProxy},
			[]any{skHTTPS, skFbProxy,
				skEval(nil, "dns-direct", "race-0-0"), skEval(nil, "dns-direct__alt1", "race-0-1"),
				skRespond("race-0-0"), skRespond("race-0-1")},
			"ipv4_only", "dns-direct", skOwner(1, 6, "其他")},
		{"直连备用 2 个:第二个带自己的协议端口,三家一起竞速",
			skProfile(skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}, obj{"server": "114.114.114.114", "protocol": "tcp", "port": 5353.0}}}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"),
				obj{"type": "tcp", "tag": "dns-direct__alt2", "server": "114.114.114.114", "server_port": 5353}, skProxy},
			[]any{skHTTPS, skFbProxy,
				skEval(nil, "dns-direct", "race-0-0"), skEval(nil, "dns-direct__alt1", "race-0-1"), skEval(nil, "dns-direct__alt2", "race-0-2"),
				skRespond("race-0-0"), skRespond("race-0-1"), skRespond("race-0-2")},
			"ipv4_only", "dns-direct", skOwner(1, 8, "其他")},
		{"直连备用与主上游同一台:去重后等于没配,不生成竞速",
			skProfile(skDM(obj{"directExtras": []any{obj{"server": "223.5.5.5"}}}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy},
			[]any{skHTTPS, skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 2, "其他")},
		{"代理备用 1 个:兜底代理支自己竞速(带开关),末尾再补一条挂开关的普通规则;没有直连竞速",
			skProfile(skDM(obj{"proxyExtras": []any{obj{"server": "9.9.9.9"}}}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skProxy, obj{"type": "tcp", "tag": "dns-proxy__alt1", "server": "9.9.9.9", "detour": "其他"}},
			[]any{skHTTPS,
				skEval(skSwitch(), "dns-proxy", "race-0-0"), skEval(skSwitch(), "dns-proxy__alt1", "race-0-1"),
				skRespond("race-0-0"), skRespond("race-0-1"), skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 6, "其他")},
		{"代理备用写 wan:取系统 DNS、udp、不写 detour",
			skProfile(skDM(obj{"proxyExtras": []any{obj{"server": "wan"}}}), nil), DNSOptions{Builtin: defaultBuiltin, SystemDNS: []any{"10.0.0.1"}},
			[]any{dnsDirect("10.0.0.1"), skProxy, obj{"type": "udp", "tag": "dns-proxy__alt1", "server": "10.0.0.1"}},
			[]any{skHTTPS,
				skEval(skSwitch(), "dns-proxy", "race-0-0"), skEval(skSwitch(), "dns-proxy__alt1", "race-0-1"),
				skRespond("race-0-0"), skRespond("race-0-1"), skFbProxy},
			"ipv4_only", "dns-direct", skOwner(1, 6, "其他")},
		{"两侧都有备用:代理竞速占 race-0,直连竞速占 race-1,响应 tag 不撞",
			skProfile(skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}, "proxyExtras": []any{obj{"server": "9.9.9.9"}}}), nil), skOpts(),
			[]any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"), skProxy,
				obj{"type": "tcp", "tag": "dns-proxy__alt1", "server": "9.9.9.9", "detour": "其他"}},
			[]any{skHTTPS,
				skEval(skSwitch(), "dns-proxy", "race-0-0"), skEval(skSwitch(), "dns-proxy__alt1", "race-0-1"),
				skRespond("race-0-0"), skRespond("race-0-1"), skFbProxy,
				skEval(nil, "dns-direct", "race-1-0"), skEval(nil, "dns-direct__alt1", "race-1-1"),
				skRespond("race-1-0"), skRespond("race-1-1")},
			"ipv4_only", "dns-direct", skOwner(1, 10, "其他")},

		// ---- 全部叠在一起:检查整体顺序 ----
		{"劫持 + 重写 + 过滤 + 两侧备用 + FakeIP + 代理降 IPv4",
			skProfile(obj{"rewrite": skRW, "fakeIpForProxy": true,
				"directExtras": []any{obj{"server": "8.8.8.8"}}, "proxyExtras": []any{obj{"server": "9.9.9.9"}}},
				obj{"ipv6": true, "ipv6Proxy": "ipv4"}),
			DNSOptions{Builtin: defaultBuiltin, FilterRules: []any{skFilter}},
			[]any{dnsDirect("223.5.5.5"), skDirectTag("8.8.8.8", "dns-direct__alt1"), dnsRewriteServer, skProxy,
				obj{"type": "tcp", "tag": "dns-proxy__alt1", "server": "9.9.9.9", "detour": "其他"}, skFake4, dnsLocalServer},
			[]any{skRWRules[0], skRWRules[1], dnsLocalRules[0], dnsLocalRules[1], skHTTPS, skFilter,
				skEmptyAAAA, skFakeRuleA,
				skEval(skSwitch(), "dns-proxy", "race-0-0"), skEval(skSwitch(), "dns-proxy__alt1", "race-0-1"),
				skRespond("race-0-0"), skRespond("race-0-1"), skFbProxy,
				skEval(nil, "dns-direct", "race-1-0"), skEval(nil, "dns-direct__alt1", "race-1-1"),
				skRespond("race-1-0"), skRespond("race-1-1")},
			"prefer_ipv4", "dns-direct", skOwner(6, 17, "其他")},
	}
	for _, c := range cases {
		res, err := BuildDNS(c.p, c.o)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		want := map[string]any{"servers": c.wantS, "rules": c.wantR, "final": "dns-direct", "strategy": c.strat, "reverse_mapping": true}
		if !reflect.DeepEqual(res.DNS, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.name, res.DNS, want)
		}
		wantRes := Resolvers{Policies: map[string]string{}, Custom: []any{}, Clients: []ClientResolver{}, Fallback: c.fb}
		if !reflect.DeepEqual(res.Resolvers, wantRes) {
			t.Errorf("%s:Resolvers = %#v", c.name, res.Resolvers)
		}
		if !reflect.DeepEqual(res.RuleOwners, c.owns) {
			t.Errorf("%s:RuleOwners = %#v,想要 %#v", c.name, res.RuleOwners, c.owns)
		}
	}
}

// 兜底此刻走直连还是走代理:只影响 Resolvers.Fallback,规则结构两支都写、不变。
func TestBuildDNSSplitSkeletonFallbackResolver(t *testing.T) {
	cases := []struct {
		name string
		top  obj // profile 顶层(routing)
		o    DNSOptions
		want string
	}{
		{"没传组、没写默认:成员只有直连 → 直连", nil, DNSOptions{Builtin: defaultBuiltin}, "dns-direct"},
		{"默认空、成员第一项是直连 → 直连", nil, DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}}, "dns-direct"},
		{"默认空、成员第一项是节点组 → 代理", nil, DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"美国", "direct"}}, "dns-proxy"},
		{"默认写 direct,即使它不在第一位", obj{"routing": obj{"fallbackDefault": "direct"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"美国", "direct"}}, "dns-direct"},
		{"默认写某个组 → 代理", obj{"routing": obj{"fallbackDefault": "美国"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}}, "dns-proxy"},
		{"默认写 proxy:取第一个非内置成员 → 代理", obj{"routing": obj{"fallbackDefault": "proxy"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国", "日本"}}, "dns-proxy"},
		{"默认写 proxy 但成员只有内置:落到第一项直连", obj{"routing": obj{"fallbackDefault": "proxy"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "block"}}, "dns-direct"},
		{"默认指向已不存在的组:落到成员第一项", obj{"routing": obj{"fallbackDefault": "已删除"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"美国", "direct"}}, "dns-proxy"},
		{"默认是 block:不是直连就记代理侧", obj{"routing": obj{"fallbackDefault": "block"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "block"}}, "dns-proxy"},
		{"内核里的当前选择优先于默认:选了节点组", obj{"routing": obj{"fallbackDefault": "direct"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"其他": "美国"}}, "dns-proxy"},
		{"当前选择优先于默认:选了直连", obj{"routing": obj{"fallbackDefault": "美国"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"其他": "direct"}}, "dns-direct"},
		{"选择顺着 selector 链下钻到叶子:美国 → 直连", nil,
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"美国", "direct"}, Selections: map[string]any{"其他": "美国", "美国": "direct"}}, "dns-direct"},
		{"选择值不是字符串:不会等于直连", nil,
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct"}, Selections: map[string]any{"其他": 5.0}}, "dns-proxy"},
		{"选择里只有别的站点集的键:不影响兜底", obj{"routing": obj{"fallbackDefault": "direct"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"流媒体": "美国"}}, "dns-direct"},
		{"兜底改名后选择按新名字取", obj{"routing": obj{"fallbackName": "兜底", "fallbackDefault": "direct"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"兜底": "美国"}}, "dns-proxy"},
		{"兜底改名后旧名字的选择不再起作用", obj{"routing": obj{"fallbackName": "兜底", "fallbackDefault": "direct"}},
			DNSOptions{Builtin: defaultBuiltin, GroupTags: []string{"direct", "美国"}, Selections: map[string]any{"其他": "美国"}}, "dns-direct"},
		{"直连被停用:成员里没有它,落到第一项节点组", obj{"routing": obj{"fallbackDefault": "direct"}},
			DNSOptions{Builtin: builtinTags{"direct", "block", false, true}, GroupTags: []string{"direct", "美国"}}, "dns-proxy"},
		{"内置直连改了名:默认 direct 指向改名后的直连", obj{"routing": obj{"fallbackDefault": "direct"}},
			DNSOptions{Builtin: builtinTags{"直连", "拒绝", true, true}, GroupTags: []string{"美国", "直连"}}, "dns-direct"},
	}
	for _, c := range cases {
		res, err := BuildDNS(skProfile(skDM(nil), c.top), c.o)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		if res.Resolvers.Fallback != c.want {
			t.Errorf("%s:Fallback = %q,想要 %q", c.name, res.Resolvers.Fallback, c.want)
		}
		// 规则结构不随兜底走向变化:HTTPS 空应答 + 挂开关的代理支
		wantRules := []any{skHTTPS, obj{"rule_set": []string{skFlag}, "server": "dns-proxy"}}
		if !reflect.DeepEqual(res.DNS["rules"], wantRules) {
			t.Errorf("%s:规则变了:%#v", c.name, res.DNS["rules"])
		}
	}
}

// 有配置但不生效的站点集 / 自定义分流:骨架与什么都没有时逐项一致,Policies / Custom 都是空。
func TestBuildDNSSplitSkeletonInertRouting(t *testing.T) {
	inert := []struct {
		name    string
		routing obj
	}{
		{"只有 IP 条件的站点集(没有域名类条件,DNS 不写规则也不登记解析器)", obj{"policies": []any{
			obj{"id": "p1", "name": "内网", "ipCidr": []any{"10.0.0.0/8"}},
		}}},
		{"停用的站点集", obj{"policies": []any{
			obj{"id": "p2", "name": "停用", "enabled": false, "domain": []any{"a.example"}},
		}}},
		{"自定义分流停用(有规则)", obj{"custom": obj{"enabled": false, "rules": []any{
			obj{"type": "domain", "value": "x.example", "outbound": "direct"},
		}}}},
		{"自定义分流开着但没有规则", obj{"custom": obj{"enabled": true, "rules": []any{}}}},
	}
	for _, c := range inert {
		for _, mode := range []string{"hijack", "dnsmasq"} {
			dns := obj{"mode": mode}
			res, err := BuildDNS(skProfile(dns, obj{"routing": c.routing}), skOpts())
			if err != nil {
				t.Errorf("%s/%s:%v", c.name, mode, err)
				continue
			}
			base, _ := BuildDNS(skProfile(dns, nil), skOpts())
			if !reflect.DeepEqual(res.DNS, base.DNS) {
				t.Errorf("%s/%s:骨架应与没有配置时一致:\n got %#v\nwant %#v", c.name, mode, res.DNS, base.DNS)
			}
			if len(res.Resolvers.Policies) != 0 || len(res.Resolvers.Custom) != 0 || len(res.Resolvers.Clients) != 0 {
				t.Errorf("%s/%s:不该登记解析器:%#v", c.name, mode, res.Resolvers)
			}
			if !reflect.DeepEqual(res.RuleOwners, base.RuleOwners) || len(res.RuleOwners) != 1 || res.RuleOwners[0].Kind != "fallback" {
				t.Errorf("%s/%s:规则归属只该有兜底一段:%#v", c.name, mode, res.RuleOwners)
			}
		}
	}
}

// 入参不被写穿:过滤规则切片原样带出,调用方的 profile 不变。
func TestBuildDNSSplitSkeletonDoesNotMutateInputs(t *testing.T) {
	filter := []any{skFilter}
	p := skProfile(skDM(obj{"directExtras": []any{obj{"server": "8.8.8.8"}}}), nil)
	before := jsonOf(t, p)
	res, err := BuildDNS(p, DNSOptions{Builtin: defaultBuiltin, FilterRules: filter})
	if err != nil {
		t.Fatal(err)
	}
	if jsonOf(t, p) != before {
		t.Errorf("profile 被改了:%s", jsonOf(t, p))
	}
	if len(filter) != 1 || !reflect.DeepEqual(filter[0], skFilter) {
		t.Errorf("FilterRules 被改了:%#v", filter)
	}
	// 同一份入参再来一遍,结果相同(没有跨调用的残留状态,比如竞速序号从 0 重新数)
	res2, _ := BuildDNS(p, DNSOptions{Builtin: defaultBuiltin, FilterRules: filter})
	if !reflect.DeepEqual(res.DNS, res2.DNS) {
		t.Errorf("两次结果不同:\n%#v\n%#v", res.DNS, res2.DNS)
	}
}
