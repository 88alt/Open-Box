package dnsdecide

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// 样例是真实 core 对默认档案生成的 dns 段和出站(2026-10-29 用 config/preview 导出的)。
const sample = `{
 "dns": {
  "final": "dns-direct", "strategy": "ipv4_only",
  "rules": [
   {"action":"predefined","query_type":["HTTPS","SVCB"],"rcode":"NOERROR"},
   {"rule_set":["obnode-direct"],"server":"dns-direct"},
   {"mode":"and","rules":[{"rule_set":["geosite-cn"]},{"rule_set":["obflip-ff2082aa78ae"]}],"server":"dns-proxy","type":"logical"},
   {"rule_set":["geosite-cn"],"server":"dns-direct"},
   {"rule_set":["obflip-fallback"],"server":"dns-proxy"}
  ],
  "servers": [
   {"server":"223.5.5.5","tag":"dns-direct","type":"udp"},
   {"detour":"其他","server":"1.1.1.1","tag":"dns-proxy","type":"tcp"}
  ]
 },
 "outbounds": [
  {"tag":"直连","type":"direct"},{"tag":"拒绝","type":"block"},
  {"tag":"其他","type":"selector","outbounds":["直连"]},{"tag":"dnsmasq","type":"direct"}
 ]
}`

// fakeSets:tag → 命中的域名;unknown 里的 tag 返回 ErrUnknownSet。
type fakeSets struct {
	in      map[string][]string
	unknown map[string]bool
}

func (f fakeSets) Match(_ context.Context, tag, d string) (bool, error) {
	if f.unknown[tag] {
		return false, ErrUnknownSet
	}
	for _, x := range f.in[tag] {
		if x == d {
			return true, nil
		}
	}
	return false, nil
}

func view(t *testing.T, js string) *View {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(js), &cfg); err != nil {
		t.Fatal(err)
	}
	v, err := FromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func idx(d *Decision) int {
	if d.RuleIndex == nil {
		return -1
	}
	return *d.RuleIndex
}

func TestDecideSample(t *testing.T) {
	v := view(t, sample)
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		sets    fakeSets
		domain  string
		qtype   string
		rule    int
		server  string
		viaProx bool
	}{
		{"国内站走直连解析", fakeSets{in: map[string][]string{"geosite-cn": {"baidu.com"}}}, "baidu.com", "A", 3, "dns-direct", false},
		{"国内站但开关指向代理", fakeSets{in: map[string][]string{"geosite-cn": {"baidu.com"}, "obflip-ff2082aa78ae": {"baidu.com"}}}, "baidu.com", "A", 2, "dns-proxy", true},
		{"都不命中走 final", fakeSets{}, "example.org", "A", -1, "dns-direct", false},
		{"兜底开关指向代理", fakeSets{in: map[string][]string{"obflip-fallback": {"example.org"}}}, "example.org", "A", 4, "dns-proxy", true},
		{"节点域名名单优先", fakeSets{in: map[string][]string{"obnode-direct": {"hk.node.test"}, "geosite-cn": {"hk.node.test"}}}, "hk.node.test", "A", 1, "dns-direct", false},
		{"大小写和末尾的点", fakeSets{in: map[string][]string{"geosite-cn": {"baidu.com"}}}, "BAIDU.com.", "A", 3, "dns-direct", false},
	} {
		d := Decide(ctx, v, c.sets, c.domain, c.qtype)
		if idx(d) != c.rule || d.Server == nil || d.Server.Tag != c.server || d.ViaProxy == nil || *d.ViaProxy != c.viaProx || d.Rejected || len(d.Assumed) != 0 {
			t.Errorf("%s: %+v server=%+v", c.name, d, d.Server)
		}
	}
	// 服务器信息带出来
	d := Decide(ctx, v, fakeSets{in: map[string][]string{"obflip-fallback": {"x.test"}}}, "x.test", "A")
	if d.Server.Type != "tcp" || d.Server.Server != "1.1.1.1" || d.Server.Detour != "其他" {
		t.Errorf("%+v", d.Server)
	}
}

func TestQueryTypeRules(t *testing.T) {
	v := view(t, sample)
	// A 查询不命中 HTTPS/SVCB 那条;HTTPS 查询命中它,内核直接应答(NOERROR,不是拒绝)
	if d := Decide(context.Background(), v, fakeSets{}, "a.test", "HTTPS"); idx(d) != 0 || d.Server != nil || d.Rejected {
		t.Errorf("%+v", d)
	}
	if d := Decide(context.Background(), v, fakeSets{}, "a.test", ""); idx(d) != -1 { // 空 = A
		t.Errorf("%+v", d)
	}
}

func TestUnknownSetIsAnAssumptionNotAGuess(t *testing.T) {
	v := view(t, sample)
	ctx := context.Background()
	// geosite-cn 读不出来,而兜底开关明确不命中:规则 2 是「且」,有一边明确不命中就整条不命中,不用记前提;
	// 规则 3 只有 geosite-cn 一个条件,判断不了,不能当命中也不能悄悄当没命中,要记成前提
	d := Decide(ctx, v, fakeSets{unknown: map[string]bool{"geosite-cn": true}}, "baidu.com", "A")
	if idx(d) != -1 || len(d.Assumed) != 1 || d.Assumed[0].RuleIndex != 3 {
		t.Fatalf("%+v", d)
	}
	a := d.Assumed[0]
	if !reflect.DeepEqual(a.Needs, []string{"rule_set:geosite-cn"}) || a.Server != "dns-direct" || a.Action != "route" {
		t.Errorf("%+v", a)
	}
	// 结果走了 final(dns-direct),和规则 3 的服务器相同 → 这条前提对结果没有影响
	if a.SameOutcome == nil || !*a.SameOutcome {
		t.Errorf("sameOutcome 应该为真: %+v", a)
	}
	// 开关指向代理时规则 2、3 都判断不了:规则 2 走代理,和结果(final = 直连)不同 → 有影响
	d = Decide(ctx, v, fakeSets{unknown: map[string]bool{"geosite-cn": true}, in: map[string][]string{"obflip-ff2082aa78ae": {"baidu.com"}}}, "baidu.com", "A")
	if len(d.Assumed) != 2 || d.Assumed[0].RuleIndex != 2 || d.Assumed[1].RuleIndex != 3 || *d.Assumed[0].SameOutcome || !*d.Assumed[1].SameOutcome {
		t.Errorf("%+v", d.Assumed)
	}
	// 判断不了的规则排在前面、后面有确定命中的:结果取确定的那条,前提仍然记着
	d = Decide(ctx, v, fakeSets{unknown: map[string]bool{"geosite-cn": true}, in: map[string][]string{"obflip-fallback": {"baidu.com"}}}, "baidu.com", "A")
	if idx(d) != 4 || d.Server.Tag != "dns-proxy" || len(d.Assumed) != 1 || *d.Assumed[0].SameOutcome {
		t.Errorf("%+v", d)
	}
}

func TestSourceDependentRulesAreAssumptions(t *testing.T) {
	v := view(t, `{"dns":{"final":"a","rules":[
	  {"domain_suffix":["corp.test"],"source_ip_cidr":["192.168.1.0/24"],"server":"b"},
	  {"domain_suffix":["corp.test"],"source_mac_address":["aa:bb:cc:dd:ee:ff"],"action":"reject"},
	  {"domain_suffix":["other.test"],"source_ip_cidr":["10.0.0.0/8"],"server":"b"},
	  {"domain_suffix":["corp.test"],"server":"a"}
	 ],"servers":[{"tag":"a","type":"udp","server":"1.1.1.1"},{"tag":"b","type":"udp","server":"2.2.2.2"}]}}`)
	d := Decide(context.Background(), v, fakeSets{}, "x.corp.test", "A")
	if idx(d) != 3 || d.Server.Tag != "a" || len(d.Assumed) != 2 {
		t.Fatalf("%+v", d)
	}
	a0, a1 := d.Assumed[0], d.Assumed[1]
	if a0.RuleIndex != 0 || !reflect.DeepEqual(a0.Needs, []string{"source_ip_cidr"}) || !reflect.DeepEqual(a0.SourceIPCIDR, []string{"192.168.1.0/24"}) || a0.Server != "b" || *a0.SameOutcome {
		t.Errorf("%+v", a0)
	}
	if a1.RuleIndex != 1 || !reflect.DeepEqual(a1.SourceMAC, []string{"aa:bb:cc:dd:ee:ff"}) || a1.Action != "reject" || *a1.SameOutcome || a1.SourceIPCIDR == nil {
		t.Errorf("%+v", a1)
	}
	// 域名对不上的那条(规则 2)不记
	for _, a := range d.Assumed {
		if a.RuleIndex == 2 {
			t.Errorf("域名对不上的规则不该记前提")
		}
	}
	// 其它判断不了的条件(clash_mode)也一样,且按名字排序
	v = view(t, `{"dns":{"final":"a","rules":[{"domain":["x.test"],"clash_mode":"global","network_type":["wifi"],"server":"b"}],
	  "servers":[{"tag":"a","type":"udp"},{"tag":"b","type":"udp"}]}}`)
	d = Decide(context.Background(), v, fakeSets{}, "x.test", "A")
	if len(d.Assumed) != 1 || !reflect.DeepEqual(d.Assumed[0].Needs, []string{"clash_mode", "network_type"}) {
		t.Errorf("%+v", d.Assumed)
	}
}

func TestActionsAndLogic(t *testing.T) {
	cfg := `{"dns":{"final":"a","rules":[
	  {"domain":["blocked.test"],"action":"reject"},
	  {"domain_keyword":["ads"],"action":"predefined","rcode":"REFUSED"},
	  {"domain":["opts.test"],"action":"route-options","disable_cache":true},
	  {"domain":["weird.test"],"action":"evaluate","server":"b"},
	  {"type":"logical","mode":"or","rules":[{"domain":["or1.test"]},{"domain_regex":["^or2-\\d+\\.test$"]}],"server":"b"},
	  {"domain_suffix":["inv.test"],"invert":true,"server":"b"}
	 ],"servers":[{"tag":"a","type":"udp","server":"1.1.1.1"},{"tag":"b","type":"udp","server":"2.2.2.2","detour":"直连"}]},
	 "outbounds":[{"tag":"直连","type":"direct"}]}`
	v := view(t, cfg)
	ctx := context.Background()
	if d := Decide(ctx, v, fakeSets{}, "blocked.test", "A"); !d.Rejected || idx(d) != 0 || d.Server != nil {
		t.Errorf("reject: %+v", d)
	}
	if d := Decide(ctx, v, fakeSets{}, "cdn.ads1.test", "A"); !d.Rejected || idx(d) != 1 {
		t.Errorf("predefined REFUSED: %+v", d)
	}
	if d := Decide(ctx, v, fakeSets{}, "opts.test", "A"); idx(d) == 2 {
		t.Errorf("route-options 不决定去哪: %+v", d)
	}
	// 不认识的动作:记前提,不猜,继续往下走(后面取反的那条 inv.test 规则会命中它)
	d := Decide(ctx, v, fakeSets{}, "weird.test", "A")
	if len(d.Assumed) != 1 || d.Assumed[0].RuleIndex != 3 || d.Assumed[0].Needs[0] != "action:evaluate" || idx(d) != 5 {
		t.Errorf("%+v", d)
	}
	for _, dom := range []string{"or1.test", "or2-42.test"} {
		if d := Decide(ctx, v, fakeSets{}, dom, "A"); idx(d) != 4 || d.Server.Tag != "b" || *d.ViaProxy { // detour 是直连出站 → 不算走代理
			t.Errorf("or %s: %+v", dom, d)
		}
	}
	if d := Decide(ctx, v, fakeSets{}, "or2-x.test", "A"); idx(d) == 4 {
		t.Errorf("正则不该命中: %+v", d)
	}
	// invert:不在 inv.test 下的域名命中规则 5
	if d := Decide(ctx, v, fakeSets{}, "plain.test", "A"); idx(d) != 5 {
		t.Errorf("invert: %+v", d)
	}
	if d := Decide(ctx, v, fakeSets{}, "a.inv.test", "A"); idx(d) == 5 {
		t.Errorf("invert 命中了不该命中的: %+v", d)
	}
}

func TestFakeIP(t *testing.T) {
	v := view(t, `{"dns":{"final":"real","rules":[
	  {"domain_suffix":["x.test"],"server":"fk"},
	  {"domain_suffix":["x.test"],"query_type":["MX"],"server":"mx"},
	  {"domain_suffix":["y.test"],"server":"fk"}
	 ],"servers":[{"tag":"real","type":"udp","server":"1.1.1.1"},{"tag":"fk","type":"fakeip"},{"tag":"mx","type":"udp","server":"9.9.9.9"}]}}`)
	// fake-ip 规则先命中:A 拿占位地址,server 是其它查询类型用的真解析器(这里往下找到 final)
	d := Decide(context.Background(), v, fakeSets{}, "a.x.test", "A")
	if d.FakeIPRule == nil || *d.FakeIPRule != 0 || idx(d) != -1 || d.Server.Tag != "real" {
		t.Errorf("%+v", d)
	}
	d = Decide(context.Background(), v, fakeSets{}, "a.x.test", "MX")
	if d.FakeIPRule == nil || idx(d) != 1 || d.Server.Tag != "mx" {
		t.Errorf("%+v", d)
	}
	if d := Decide(context.Background(), v, fakeSets{}, "a.z.test", "A"); d.FakeIPRule != nil {
		t.Errorf("没命中不该有 fakeIpRule: %+v", d)
	}
}

func TestFinalDefaultsToFirstServer(t *testing.T) {
	v := view(t, `{"dns":{"servers":[{"tag":"first","type":"udp"},{"tag":"second","type":"udp"}]}}`)
	if d := Decide(context.Background(), v, fakeSets{}, "a.test", "A"); d.Server.Tag != "first" || idx(d) != -1 {
		t.Errorf("%+v", d)
	}
}

func TestFromConfigErrors(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"没有 dns":    {},
		"没有服务器":     {"dns": map[string]any{"rules": []any{}}},
		"服务器没有 tag": {"dns": map[string]any{"servers": []any{map[string]any{"type": "udp"}}}},
	} {
		if _, err := FromConfig(cfg); err == nil {
			t.Errorf("%s 应该报错", name)
		}
	}
}

func TestSuffixMatch(t *testing.T) {
	for _, c := range []struct {
		suffix, d string
		want      bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "a.b.example.com", true},
		{"example.com", "badexample.com", false},
		{".example.com", "example.com", false},
		{".example.com", "a.example.com", true},
		{"EXAMPLE.com", "a.example.com", true},
		{"", "a.com", false},
		{"cn", "baidu.cn", true},
	} {
		if got := SuffixMatch(c.suffix, c.d); got != c.want {
			t.Errorf("SuffixMatch(%q,%q)=%v want %v", c.suffix, c.d, got, c.want)
		}
	}
}

func TestJSONShape(t *testing.T) {
	v := view(t, sample)
	b, _ := json.Marshal(Decide(context.Background(), v, fakeSets{}, "example.org", "A"))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if val, has := m["ruleIndex"]; !has || val != nil { // 没命中规则:ruleIndex 必须是 null,不能缺键
		t.Errorf("ruleIndex 应该是 null: %s", b)
	}
	for _, k := range []string{"rejected", "fakeIpRule", "assumed"} {
		if _, has := m[k]; has {
			t.Errorf("%s 为空时不该出现: %s", k, b)
		}
	}
}
