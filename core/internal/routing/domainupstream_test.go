package routing

import (
	"reflect"
	"strings"
	"testing"
)

// 自写测试向量:档案 dns.domainUpstreams(面板 DNS 页「域名分流上游」)。期望值按 domainupstream.go 顶部注释的规则
// 人工推算:按规则顺序写在重写 / 本地主机名之后,命中域名交给规则自己那台上游;同一台上游只建一个 server;
// 域名规整与面板同一口径(小写、去末尾点、domainSuffix 去掉开头的 "*." / "."),不合格的丢掉。

func duRule(mt string, domains []any, proto, server string, extra obj) obj {
	r := obj{"id": "du-1", "enabled": true, "matchType": mt, "domains": domains, "server": server}
	if proto != "" {
		r["protocol"] = proto
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func duProfile(rules []any, enabled bool) obj {
	return skProfile(obj{"domainUpstreams": obj{"enabled": enabled, "rules": rules}}, nil)
}

// duRulesOf:取 dns.rules 里以 dns-du- 开头的 server 那几条(域名指定上游生成的)。
func duRulesOf(t *testing.T, res DNSResult) []any {
	t.Helper()
	out := []any{}
	rules, _ := res.DNS["rules"].([]any)
	for _, r := range rules {
		if s, ok := r.(obj)["server"].(string); ok && strings.HasPrefix(s, "dns-du-") {
			out = append(out, r)
		}
	}
	return out
}

func duServersOf(t *testing.T, res DNSResult) []obj {
	t.Helper()
	out := []obj{}
	servers, _ := res.DNS["servers"].([]any)
	for _, s := range servers {
		if m, ok := s.(obj); ok {
			if tag, _ := m["tag"].(string); strings.HasPrefix(tag, "dns-du-") && !strings.HasSuffix(tag, "-bootstrap") {
				out = append(out, m)
			}
		}
	}
	return out
}

func TestDomainUpstreamsBasics(t *testing.T) {
	// 没有这个键、enabled 为假、rules 不是数组:一条都不写(配置与没有这个功能时完全一致)。
	base, _ := BuildDNS(skProfile(obj{}, nil), skOpts())
	for _, p := range []any{
		skProfile(obj{}, nil),
		duProfile([]any{duRule("domain", []any{"a.example"}, "", "8.8.8.8", nil)}, false),
		skProfile(obj{"domainUpstreams": obj{"enabled": true, "rules": "x"}}, nil),
		skProfile(obj{"domainUpstreams": obj{"enabled": true}}, nil),
	} {
		res, err := BuildDNS(p, skOpts())
		if err != nil {
			t.Fatal(err)
		}
		if len(duRulesOf(t, res)) != 0 || len(duServersOf(t, res)) != 0 {
			t.Errorf("不该生成域名指定上游:%+v", res.DNS)
		}
		if !reflect.DeepEqual(res.DNS["rules"], base.DNS["rules"]) {
			t.Errorf("规则应与没有这个功能时一致")
		}
	}

	// 一条规则:server 建在 dns-du-1,规则写在本地主机名之后、HTTPS 空应答之前。
	p := duProfile([]any{duRule("domain", []any{"a.example"}, "", "8.8.8.8", nil)}, true)
	res, err := BuildDNS(p, skOpts())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := duServersOf(t, res), []obj{{"type": "udp", "tag": "dns-du-1", "server": "8.8.8.8"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("server = %+v,想要 %+v", got, want)
	}
	if got, want := duRulesOf(t, res), []any{obj{"domain": []string{"a.example"}, "server": "dns-du-1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %+v,想要 %+v", got, want)
	}
	rules := res.DNS["rules"].([]any)
	if !reflect.DeepEqual(rules[2], obj{"domain": []string{"a.example"}, "server": "dns-du-1"}) {
		t.Errorf("应排在两条本地主机名规则之后,实际第 3 条是 %+v", rules[2])
	}
	if !reflect.DeepEqual(rules[3], skHTTPS) {
		t.Errorf("应排在 HTTPS 空应答之前")
	}
}

func TestDomainUpstreamsNormalize(t *testing.T) {
	// 域名规整:小写、去首尾空白、去末尾点;domainSuffix 另外去掉开头的 "*." / ".";带 * 的、空串丢掉;
	// 同一规则里重复的只留一个;matchType 不认识按 domain 处理。
	p := duProfile([]any{
		duRule("domain", []any{"  A.Example.  ", "A.example", "", "  ", "*.wild.example", "bad*name", 5.0}, "", "8.8.8.8", nil),
		duRule("domainSuffix", []any{"*.corp.example.", ".other.example", "plain.example", "*."}, "", "8.8.8.8", nil),
		duRule("weird", []any{"x.example"}, "", "8.8.8.8", nil),
	}, true)
	res, err := BuildDNS(p, skOpts())
	if err != nil {
		t.Fatal(err)
	}
	want := []any{
		obj{"domain": []string{"a.example"}, "server": "dns-du-1"},
		obj{"domain_suffix": []string{"corp.example", "other.example", "plain.example"}, "server": "dns-du-1"},
		obj{"domain": []string{"x.example"}, "server": "dns-du-1"},
	}
	if got := duRulesOf(t, res); !reflect.DeepEqual(got, want) {
		t.Fatalf("rules:\n got %+v\nwant %+v", got, want)
	}
	// 同一台上游只用建一个 server。
	if got := len(duServersOf(t, res)); got != 1 {
		t.Errorf("同一台上游应只有一个 server,实际 %d 个", got)
	}

	// 单条规则最多 64 个域名(与面板一致)。
	domains := []any{}
	for i := 0; i < 70; i++ {
		domains = append(domains, "d"+string(rune('a'+i%26))+string(rune('0'+i/26))+".example")
	}
	res, _ = BuildDNS(duProfile([]any{duRule("domain", domains, "", "8.8.8.8", nil)}, true), skOpts())
	got := duRulesOf(t, res)[0].(obj)["domain"].([]string)
	if len(got) != MaxDomainUpstreamDomains {
		t.Errorf("单条规则最多 %d 个域名,实际 %d", MaxDomainUpstreamDomains, len(got))
	}
}

func TestDomainUpstreamsTargets(t *testing.T) {
	// 不同上游各建一个 server;协议 / 端口照搬档案;域名 DoT 带 bootstrap。
	p := duProfile([]any{
		duRule("domain", []any{"a.example"}, "", "8.8.8.8", nil),
		duRule("domain", []any{"b.example"}, "tcp", "9.9.9.9", obj{"port": 5353.0}),
		duRule("domain", []any{"c.example"}, "tls", "dns.google", nil),
		duRule("domain", []any{"d.example"}, "tls", "dns.google", nil), // 同一台,复用上一个 server
	}, true)
	res, err := BuildDNS(p, skOpts())
	if err != nil {
		t.Fatal(err)
	}
	servers := duServersOf(t, res)
	if len(servers) != 3 {
		t.Fatalf("应有 3 个 server(第四条的与第三条同一台),实际 %+v", servers)
	}
	if !reflect.DeepEqual(servers[1], obj{"type": "tcp", "tag": "dns-du-2", "server": "9.9.9.9", "server_port": 5353}) {
		t.Errorf("tcp + 自定义端口 = %+v", servers[1])
	}
	if !reflect.DeepEqual(servers[2], obj{"type": "tls", "tag": "dns-du-3", "server": "dns.google", "domain_resolver": "dns-du-3-bootstrap"}) {
		t.Errorf("域名 DoT = %+v", servers[2])
	}
	// bootstrap 的 server 是「上游 DNS」此刻那台(systemDns 没给 → 地区默认 223.5.5.5)。
	var boot obj
	for _, s := range res.DNS["servers"].([]any) {
		if m := s.(obj); m["tag"] == "dns-du-3-bootstrap" {
			boot = m
		}
	}
	if !reflect.DeepEqual(boot, obj{"type": "udp", "tag": "dns-du-3-bootstrap", "server": "223.5.5.5"}) {
		t.Errorf("bootstrap = %+v", boot)
	}
	if got := duRulesOf(t, res); len(got) != 4 || got[3].(obj)["server"] != "dns-du-3" {
		t.Errorf("第四条应复用 dns-du-3:%+v", got)
	}

	// 停用的规则整条跳过;写坏的上游退回「上游 DNS」而不是把配置弄坏。
	p2 := duProfile([]any{
		obj{"id": "x", "enabled": false, "matchType": "domain", "domains": []any{"skip.example"}, "server": "8.8.8.8"},
		duRule("domain", []any{"bad.example"}, "", "不是地址", nil),
		duRule("domain", []any{"noserver.example"}, "", "", nil),
		duRule("domain", []any{}, "", "8.8.8.8", nil),
	}, true)
	res, err = BuildDNS(p2, skOpts())
	if err != nil {
		t.Fatal(err)
	}
	got := duRulesOf(t, res)
	if len(got) != 1 || got[0].(obj)["server"] != "dns-du-1" {
		t.Fatalf("只应留下写坏地址那条(退回上游 DNS):%+v", got)
	}
	if servers := duServersOf(t, res); !reflect.DeepEqual(servers, []obj{{"type": "udp", "tag": "dns-du-1", "server": "223.5.5.5"}}) {
		t.Errorf("写坏的地址应退回上游 DNS:%+v", servers)
	}
}

func TestDomainUpstreamsNoSplit(t *testing.T) {
	// 分流关着(短路径)也要生效:上游建在 servers 里,规则排在重写之后、域名过滤之前。
	p := skProfile(obj{
		"split":           false,
		"mode":            "dnsmasq",
		"domainUpstreams": obj{"enabled": true, "rules": []any{duRule("domain", []any{"a.example"}, "", "8.8.8.8", nil)}},
	}, nil)
	res, err := BuildDNS(p, skOpts())
	if err != nil {
		t.Fatal(err)
	}
	if got := duServersOf(t, res); len(got) != 1 {
		t.Fatalf("短路径也应有 server:%+v", got)
	}
	rules := res.DNS["rules"].([]any)
	if len(rules) != 1 || !reflect.DeepEqual(rules[0], obj{"domain": []string{"a.example"}, "server": "dns-du-1"}) {
		t.Fatalf("短路径的规则 = %+v", rules)
	}
}
