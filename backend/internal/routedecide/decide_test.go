package routedecide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
)

// fakeSets:tag → 内容。domains 是后缀,cidrs 是网段;err 非空时两种查询都返回它;partial 为真表示「读得出来但有判断不了的规则」。
type fakeSet struct {
	domains []string
	cidrs   []string
	err     error
	partial bool
}

type fakeSets struct {
	m     map[string]fakeSet
	calls []string
}

func (f *fakeSets) Match(_ context.Context, tag, d string) (bool, error) {
	f.calls = append(f.calls, "d:"+tag)
	s, ok := f.m[tag]
	if !ok {
		return false, fmt.Errorf("%w: 配置里没有规则集 %s", dnsdecide.ErrUnknownSet, tag)
	}
	if s.err != nil {
		return false, s.err
	}
	for _, x := range s.domains {
		if dnsdecide.SuffixMatch(x, d) {
			return true, nil
		}
	}
	if s.partial || len(s.cidrs) > 0 {
		return false, fmt.Errorf("%w: %w: 规则集 %s 里有只看域名判断不了的规则", dnsdecide.ErrUnknownSet, dnsdecide.ErrSetPartial, tag)
	}
	return false, nil
}

func (f *fakeSets) MatchIP(_ context.Context, tag string, ip netip.Addr) (bool, error) {
	f.calls = append(f.calls, "ip:"+tag)
	s, ok := f.m[tag]
	if !ok {
		return false, fmt.Errorf("%w: 配置里没有规则集 %s", dnsdecide.ErrUnknownSet, tag)
	}
	if s.err != nil {
		return false, s.err
	}
	for _, c := range s.cidrs {
		if netip.MustParsePrefix(c).Contains(ip) {
			return true, nil
		}
	}
	if s.partial {
		return false, fmt.Errorf("%w: %w: 规则集 %s 里有只看 IP 判断不了的规则", dnsdecide.ErrUnknownSet, dnsdecide.ErrSetPartial, tag)
	}
	return false, nil
}

// 规则表的顺序和形状取自 core 的 BuildRoute:首包预判 → sniff → DNS 劫持 → 防回环 → 前置自定义 → 私网 → NTP →
// 节点站点直连 → 终端分流 → 站点集(看域名的一份,看 IP 的一份带「连接没有域名」的前提)→ 兜底。
const routeJSON = `{
 "route": {"final": "其他", "rules": [
  {"action": "sniff"},
  {"protocol": "dns", "action": "hijack-dns"},
  {"inbound": ["dns-in"], "action": "hijack-dns"},
  {"ip_cidr": ["198.18.0.0/15"], "action": "reject"},
  {"ip_is_private": true, "outbound": "direct"},
  {"network": "udp", "port": [123], "outbound": "direct"},
  {"rule_set": ["obnode-direct", "obnode-direct-ip"], "outbound": "direct"},
  {"type": "logical", "mode": "and", "rules": [{"source_ip_cidr": ["192.168.1.5/32"]}], "outbound": "hk-01"},
  {"rule_set": ["geosite-openai"], "outbound": "AI"},
  {"type": "logical", "mode": "and", "rules": [
     {"rule_set": ["geoip-cn"]}, {"domain_regex": ["^[^:]*[a-z][^:]*$"], "invert": true}], "outbound": "国内直连"},
  {"rule_set": ["geosite-cn"], "outbound": "国内直连"}
 ]},
 "outbounds": [{"tag":"direct","type":"direct"},{"tag":"AI","type":"selector"},{"tag":"其他","type":"selector"}]
}`

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

func defaultSets() *fakeSets {
	return &fakeSets{m: map[string]fakeSet{
		"obnode-direct":    {domains: []string{"airport.example"}},
		"obnode-direct-ip": {cidrs: []string{"203.0.113.0/24"}},
		"geosite-openai":   {domains: []string{"openai.com", "chatgpt.com"}},
		"geoip-cn":         {cidrs: []string{"114.114.114.0/24", "8.8.8.0/24"}},
		"geosite-cn":       {domains: []string{"baidu.com", "qq.com"}},
	}}
}

func domain(d string) Target { return Target{Domain: d} }
func ip(a string) Target     { return Target{IP: netip.MustParseAddr(a)} }

func idx(d *Decision) int {
	if d.Index == nil {
		return -1
	}
	return *d.Index
}

func assumedIdx(d *Decision) []int {
	out := []int{}
	for _, a := range d.Assumed {
		out = append(out, a.Index)
	}
	return out
}

func TestFromConfig(t *testing.T) {
	v := view(t, routeJSON)
	if len(v.Rules) != 11 || v.Final != "其他" || v.Types["direct"] != "direct" || v.Types["AI"] != "selector" {
		t.Fatalf("%+v", v)
	}
	if _, err := FromConfig(map[string]any{"dns": map[string]any{}}); err == nil {
		t.Fatal("没有 route 段应报错")
	}
}

func TestDecideDomainTargets(t *testing.T) {
	v := view(t, routeJSON)
	cases := []struct {
		name         string
		target       string
		wantIdx      int
		wantOutbound string
		wantAssumed  []int
	}{
		// 前面的 IP 条件 / 端口 / 来源条件对域名判断不了,按不满足处理并记成前提;站点集 openai 命中
		{"站点集命中", "chat.openai.com", 8, "AI", []int{3, 4, 5, 6, 7}},
		// geoip-cn 那份带「没有域名」的前提,域名连接不适用,所以不进前提;geosite-cn 命中
		{"国内站点", "www.baidu.com", 10, "国内直连", []int{3, 4, 5, 6, 7}},
		{"大小写和末尾点", "WWW.Baidu.COM.", 10, "国内直连", []int{3, 4, 5, 6, 7}},
		{"没有命中走兜底", "unknown.example", -1, "其他", []int{3, 4, 5, 6, 7}},
		{"节点站点直连", "x.airport.example", 6, "direct", []int{3, 4, 5}},
	}
	for _, c := range cases {
		d := Decide(context.Background(), v, defaultSets(), domain(c.target))
		if d.MatchError != "" || idx(d) != c.wantIdx || d.Outbound != c.wantOutbound || !reflect.DeepEqual(assumedIdx(d), c.wantAssumed) {
			t.Errorf("%s: idx=%d outbound=%q assumed=%v err=%q", c.name, idx(d), d.Outbound, assumedIdx(d), d.MatchError)
		}
	}
}

func TestDecideAssumptionContent(t *testing.T) {
	d := Decide(context.Background(), view(t, routeJSON), defaultSets(), domain("chat.openai.com"))
	want := map[int]struct {
		needs  []string
		action string
		out    string
	}{
		3: {[]string{NeedDestIP}, "reject", ""},
		4: {[]string{NeedDestIP}, "route", "direct"},
		5: {[]string{NeedPort}, "route", "direct"},
		6: {[]string{NeedDestIP}, "route", "direct"},
		7: {[]string{NeedSourceIP}, "route", "hk-01"},
	}
	for _, a := range d.Assumed {
		w := want[a.Index]
		if !reflect.DeepEqual(a.Needs, w.needs) || a.Action != w.action || a.Outbound != w.out || a.Rule == nil {
			t.Errorf("第 %d 条前提不对: %+v", a.Index, a)
		}
	}
	// JSON 形状:needs 是数组,不带 leaf / sameOutcome(调用方才知道)
	b, _ := json.Marshal(d.Assumed[0])
	if strings.Contains(string(b), "leaf") || strings.Contains(string(b), "sameOutcome") || !strings.Contains(string(b), `"needs":["destIp"]`) {
		t.Errorf("%s", b)
	}
}

func TestDecideIPTargets(t *testing.T) {
	v := view(t, routeJSON)
	cases := []struct {
		name         string
		target       string
		wantIdx      int
		wantAction   string
		wantOutbound string
		wantAssumed  []int
	}{
		{"私网", "192.168.1.8", 4, "route", "direct", nil},
		{"内核 fake-ip 段:防回环拒绝", "198.19.0.5", 3, "reject", "", nil},
		// 端口、来源判断不了;按 IP 判的 geoip-cn(带「没有域名」前提,IP 连接适用)命中
		{"国内 IP", "8.8.8.8", 9, "route", "国内直连", []int{5, 7}},
		{"节点服务器的 IP", "203.0.113.9", 6, "route", "direct", []int{5}},
		{"没有命中走兜底", "1.1.1.1", -1, "route", "其他", []int{5, 7}},
		{"IPv4 映射地址按 IPv4 比", "::ffff:192.168.1.8", 4, "route", "direct", nil},
	}
	for _, c := range cases {
		d := Decide(context.Background(), v, defaultSets(), ip(c.target))
		if d.MatchError != "" || idx(d) != c.wantIdx || d.Outbound != c.wantOutbound || !sameInts(assumedIdx(d), c.wantAssumed) {
			t.Errorf("%s: idx=%d outbound=%q assumed=%v err=%q", c.name, idx(d), d.Outbound, assumedIdx(d), d.MatchError)
			continue
		}
		if idx(d) >= 0 && d.Action != c.wantAction {
			t.Errorf("%s: action=%q 期望 %q", c.name, d.Action, c.wantAction)
		}
	}
}

func TestDecideDomainOnlySetsNeverMatchIP(t *testing.T) {
	// openai 的域名集合对没有域名的 IP 连接恒不命中,不会误报命中站点集
	d := Decide(context.Background(), view(t, routeJSON), defaultSets(), ip("104.18.2.2"))
	if idx(d) == 8 {
		t.Fatalf("IP 目标不该命中域名站点集: %+v", d)
	}
}

func TestDecideRuleSetProblemsStopThePrediction(t *testing.T) {
	boom := fmt.Errorf("%w: 规则集 geosite-openai 的文件读不到", dnsdecide.ErrUnknownSet)
	s := defaultSets()
	s.m["geosite-openai"] = fakeSet{err: boom}
	d := Decide(context.Background(), view(t, routeJSON), s, domain("chat.openai.com"))
	if d.MatchError == "" || d.Index != nil || d.Outbound != "" || !strings.Contains(d.MatchError, "geosite-openai") {
		t.Fatalf("规则集没能检查:停下来、不给「没命中」的结论: %+v", d)
	}
	for _, c := range s.calls {
		if c == "d:geosite-cn" || c == "d:geoip-cn" {
			t.Fatalf("停下之后不该再往后查: %v", s.calls)
		}
	}
	// 错误里没带规则集名的,补上
	s = defaultSets()
	s.m["geosite-openai"] = fakeSet{err: errors.New("解码失败")}
	d = Decide(context.Background(), view(t, routeJSON), s, domain("chat.openai.com"))
	if d.MatchError != "规则集 geosite-openai:解码失败" {
		t.Fatalf("%q", d.MatchError)
	}
	// 缺的规则集同理;IP 目标读不出来也一样
	s = defaultSets()
	delete(s.m, "geoip-cn")
	if d = Decide(context.Background(), view(t, routeJSON), s, ip("1.1.1.1")); d.MatchError == "" || !strings.Contains(d.MatchError, "geoip-cn") {
		t.Fatalf("%+v", d)
	}
	// IP 目标遇到「有判断不了的规则」不是缺条件,是没能检查
	s = defaultSets()
	s.m["geoip-cn"] = fakeSet{partial: true}
	if d = Decide(context.Background(), view(t, routeJSON), s, ip("1.1.1.1")); d.MatchError == "" {
		t.Fatalf("%+v", d)
	}
}

func TestDecideNoMismatchWhenAnEarlierConditionFails(t *testing.T) {
	// 一条规则里别的条件已经确定不满足时,规则集再读不出来也不影响结论
	v := view(t, `{"route":{"final":"F","rules":[
	  {"domain_suffix":["a.com"],"rule_set":["broken"],"outbound":"X"},
	  {"domain_suffix":["b.com"],"outbound":"Y"}]}}`)
	s := &fakeSets{m: map[string]fakeSet{"broken": {err: errors.New("坏了")}}}
	d := Decide(context.Background(), v, s, domain("b.com"))
	if d.MatchError != "" || d.Outbound != "Y" || idx(d) != 1 {
		t.Fatalf("%+v", d)
	}
}

func TestDecideLogicalInvertAndConditionGroups(t *testing.T) {
	v := view(t, `{"route":{"final":"F","rules":[
	  {"type":"logical","mode":"or","rules":[{"domain":["exact.com"]},{"domain_keyword":["kw"]}],"outbound":"OR"},
	  {"domain_suffix":["skip.com"],"invert":true,"outbound":"NOT"},
	  {"domain_regex":["^re[0-9]+\\.net$"],"outbound":"RE"}]}}`)
	cases := map[string]struct {
		out string
		idx int
	}{
		"exact.com": {"OR", 0}, "has-kw-in.org": {"OR", 0},
		"skip.com": {"RE", 2}, // invert 命中的取反:skip.com 不走 NOT,继续往下(正则不匹配 → 兜底)
		"other.io": {"NOT", 1},
	}
	// skip.com 不匹配正则 re[0-9]+.net → 落到兜底
	cases["skip.com"] = struct {
		out string
		idx int
	}{"F", -1}
	cases["re12.net"] = struct {
		out string
		idx int
	}{"NOT", 1} // 不在 skip.com 里,invert 规则先命中
	for target, c := range cases {
		d := Decide(context.Background(), v, defaultSets(), domain(target))
		if d.Outbound != c.out || idx(d) != c.idx {
			t.Errorf("%s: idx=%d outbound=%q,期望 %d %q", target, idx(d), d.Outbound, c.idx, c.out)
		}
	}
}

func TestDecideDefiniteNoBeatsUnknown(t *testing.T) {
	v := view(t, `{"route":{"final":"F","rules":[
	  {"domain_suffix":["a.com"],"port":[80],"outbound":"X"},
	  {"domain_suffix":["b.com"],"port":[80],"outbound":"Y"}]}}`)
	d := Decide(context.Background(), v, &fakeSets{}, domain("b.com"))
	if got := assumedIdx(d); !reflect.DeepEqual(got, []int{1}) || d.Outbound != "F" {
		t.Fatalf("域名条件已确定不满足的规则不进前提: assumed=%v outbound=%q", got, d.Outbound)
	}
}

func TestDecideNeedsAreSortedAndDeduped(t *testing.T) {
	v := view(t, `{"route":{"final":"F","rules":[
	  {"source_ip_cidr":["10.0.0.1/32"],"source_mac_address":["aa:bb:cc:dd:ee:ff"],"port":[1],"port_range":["10:20"],"network":"udp","ip_version":6,"ip_is_private":true,"outbound":"X"}]}}`)
	d := Decide(context.Background(), v, &fakeSets{}, domain("a.com"))
	want := []string{NeedSourceIP, NeedSourceMAC, NeedPort, NeedDestIP, NeedIPVersion}
	if len(d.Assumed) != 1 || !reflect.DeepEqual(d.Assumed[0].Needs, want) {
		t.Fatalf("%+v", d.Assumed)
	}
}

func TestDecideIPVersion(t *testing.T) {
	v := view(t, `{"route":{"final":"F","rules":[
	  {"ip_version":6,"action":"reject"},
	  {"ip_version":4,"outbound":"V4"}]}}`)
	if d := Decide(context.Background(), v, &fakeSets{}, ip("2001:db8::1")); d.Action != "reject" || idx(d) != 0 {
		t.Fatalf("%+v", d)
	}
	if d := Decide(context.Background(), v, &fakeSets{}, ip("1.2.3.4")); d.Outbound != "V4" || idx(d) != 1 {
		t.Fatalf("%+v", d)
	}
}

func TestDecideActionsAndSkippedRules(t *testing.T) {
	v := view(t, `{"route":{"final":"F","rules":[
	  {"action":"sniff"},
	  {"action":"resolve","strategy":"prefer_ipv4"},
	  {"domain_suffix":["a.com"],"action":"bypass"},
	  {"domain_suffix":["a.com"],"action":"route-options","override_port":1},
	  {"domain_suffix":["a.com"],"action":"some-future-action","outbound":"Z"},
	  {"domain_suffix":["a.com"]},
	  {"inbound":["x"],"outbound":"IN"},
	  {"domain_suffix":["a.com"],"action":"reject"}]}}`)
	d := Decide(context.Background(), v, &fakeSets{}, domain("a.com"))
	if !d.PreResolve {
		t.Error("有 resolve 动作应标 PreResolve")
	}
	if d.Action != "reject" || idx(d) != 7 || d.Outbound != "" || d.Rule == nil {
		t.Fatalf("前面的动作规则、没有出站的规则、带入站条件的规则都要跳过: %+v", d)
	}
	if len(d.Assumed) != 0 {
		t.Fatalf("跳过的规则不进前提: %+v", d.Assumed)
	}
	if d := Decide(context.Background(), view(t, `{"route":{"final":"F","rules":[{"action":"sniff"}]}}`), &fakeSets{}, domain("a.com")); d.PreResolve || d.Outbound != "F" || d.Index != nil {
		t.Fatalf("%+v", d)
	}
}

func TestDecideEmptyRulesAndFinal(t *testing.T) {
	d := Decide(context.Background(), view(t, `{"route":{"final":"F"}}`), &fakeSets{}, domain("a.com"))
	if d.Outbound != "F" || d.Index != nil || d.Rule != nil || d.Action != "" {
		t.Fatalf("%+v", d)
	}
}

func TestNonPublic(t *testing.T) {
	for a, want := range map[string]bool{
		"10.1.1.1": true, "192.168.0.1": true, "172.16.5.5": true, "127.0.0.1": true, "169.254.1.1": true,
		"224.0.0.1": true, "0.0.0.0": true, "fe80::1": true, "fd00::1": true, "::1": true,
		"8.8.8.8": false, "114.114.114.114": false, "2001:db8::1": false, "::ffff:10.0.0.1": true, "::ffff:8.8.8.8": false,
	} {
		if got := nonPublic(netip.MustParseAddr(a)); got != want {
			t.Errorf("%s: %v 期望 %v", a, got, want)
		}
	}
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
