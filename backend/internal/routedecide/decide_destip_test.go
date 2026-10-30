package routedecide

import (
	"context"
	"net/netip"
	"strings"
	"testing"
)

func withDest(d, dest string) Target {
	return Target{Domain: d, DestIP: netip.MustParseAddr(dest)}
}

func via(d *Decision) string {
	if !d.ViaIP.IsValid() {
		return ""
	}
	return d.ViaIP.String()
}

// 终端自己解析后按真实地址连进内核、内核再嗅探出域名:连接同时带域名和目的 IP,IP 条件按目的 IP 判。
func TestDecideDomainWithDestIP(t *testing.T) {
	v := view(t, routeJSON)
	for _, c := range []struct {
		name   string
		target Target
		want   int    // 命中的规则下标
		via    string // 靠哪个目的地址命中;空 = 不是靠 IP
	}{
		{"解析到私网地址 → 私网直连", withDest("nas.example", "192.168.1.9"), 4, "192.168.1.9"},
		{"IPv4 映射的 IPv6 写法也按 IPv4 记", withDest("nas.example", "::ffff:192.168.1.9"), 4, "192.168.1.9"},
		{"解析到节点服务器的地址 → 节点站点直连(规则集里的 IP 条目)", withDest("edge.example", "203.0.113.5"), 6, "203.0.113.5"},
		{"域名自己在节点站点集里 → 按域名命中,不靠 IP", withDest("a.airport.example", "8.8.4.4"), 6, ""},
		{"站点集按域名命中,地址不影响", withDest("api.openai.com", "104.18.1.1"), 8, ""},
	} {
		d := Decide(context.Background(), v, defaultSets(), c.target)
		if idx(d) != c.want || via(d) != c.via || d.MatchError != "" {
			t.Errorf("%s: 命中 %d via %q err=%q,想要 %d via %q", c.name, idx(d), via(d), d.MatchError, c.want, c.via)
		}
	}
}

// core 给「看 IP 的规则」带了「连接没有域名」的前提(domain_regex 取反):有域名的连接即使地址落进 geoip-cn 也不被它命中。
func TestDecideDomainConnectionIsNotJudgedByNoDomainIPRules(t *testing.T) {
	v := view(t, routeJSON)
	d := Decide(context.Background(), v, defaultSets(), withDest("unknown.example", "114.114.114.114"))
	if d.Index != nil || d.Outbound != "其他" || via(d) != "" {
		t.Fatalf("应落到兜底: idx=%d out=%q via=%q", idx(d), d.Outbound, via(d))
	}
	// 域名命中 geosite-cn:出站相同,但靠的是域名
	d = Decide(context.Background(), v, defaultSets(), withDest("www.baidu.com", "114.114.114.114"))
	if idx(d) != 10 || via(d) != "" {
		t.Fatalf("idx=%d via=%q", idx(d), via(d))
	}
}

// 没带 DestIP:和原来一模一样(IP 条件记成前提)。
func TestDecideDomainWithoutDestIPUnchanged(t *testing.T) {
	v := view(t, routeJSON)
	d := Decide(context.Background(), v, defaultSets(), domain("nas.example"))
	if d.Index != nil || via(d) != "" {
		t.Fatalf("idx=%d via=%q", idx(d), via(d))
	}
	// 与改动前的实测一致:ip_cidr 拒绝、私网直连、NTP(要端口)、节点站点直连、终端分流(要来源)都判断不了
	if got := assumedIdx(d); !sameInts(got, []int{3, 4, 5, 6, 7}) {
		t.Fatalf("前提的规则下标应与原来一致: %v", got)
	}
	for _, a := range d.Assumed {
		if a.Index == 4 && strings.Join(a.Needs, ",") != NeedDestIP {
			t.Errorf("%+v", a)
		}
	}
}

func TestDecideDestIPIgnoredForIPTargets(t *testing.T) {
	v := view(t, routeJSON)
	tg := Target{IP: netip.MustParseAddr("8.8.8.8"), DestIP: netip.MustParseAddr("192.168.1.9")}
	d := Decide(context.Background(), v, defaultSets(), tg)
	want := Decide(context.Background(), v, defaultSets(), ip("8.8.8.8"))
	if idx(d) != idx(want) || via(d) != "" {
		t.Fatalf("IP 目标只看自己的地址: idx=%d via=%q(想要 %d)", idx(d), via(d), idx(want))
	}
}

func TestDecideDestIPVersionAndCIDR(t *testing.T) {
	v := view(t, `{"route":{"final":"F","rules":[
	  {"ip_version": 6, "outbound": "V6"},
	  {"ip_cidr": ["203.0.113.0/24", "2001:db8::/32"], "domain_suffix": ["cdn.example"], "outbound": "CDN"}
	]},"outbounds":[{"tag":"V6","type":"selector"},{"tag":"CDN","type":"selector"},{"tag":"F","type":"selector"}]}`)
	ctx := context.Background()
	d := Decide(ctx, v, &fakeSets{}, withDest("x.example", "2001:db8::7"))
	if idx(d) != 0 || via(d) != "2001:db8::7" {
		t.Fatalf("ip_version: %d %q", idx(d), via(d))
	}
	// 同一条规则里域名和 IP 条件是「且」:两个都满足才命中
	d = Decide(ctx, v, &fakeSets{}, withDest("a.cdn.example", "203.0.113.9"))
	if idx(d) != 1 || via(d) != "203.0.113.9" {
		t.Fatalf("且: %d %q", idx(d), via(d))
	}
	d = Decide(ctx, v, &fakeSets{}, withDest("a.other.example", "203.0.113.9"))
	if d.Index != nil || d.Outbound != "F" {
		t.Fatalf("域名不满足就不命中: %d %q", idx(d), d.Outbound)
	}
	d = Decide(ctx, v, &fakeSets{}, withDest("a.cdn.example", "198.51.100.1"))
	if d.Index != nil {
		t.Fatalf("地址不在网段就不命中: %d", idx(d))
	}
}

func TestDecideDestIPRuleSetProblemsStillStopThePrediction(t *testing.T) {
	v := view(t, `{"route":{"final":"F","rules":[{"rule_set":["s1"],"outbound":"X"}]},"outbounds":[]}`)
	sets := &fakeSets{m: map[string]fakeSet{"s1": {partial: true}}}
	d := Decide(context.Background(), v, sets, withDest("a.example", "8.8.8.8"))
	if d.MatchError == "" || d.Index != nil {
		t.Fatalf("按地址也判断不了(规则集里有判断不了的规则)时不能给「没命中」的结论: %+v", d)
	}
	// 同一份规则集、不带地址:仍是「缺目的 IP」的前提,不是错误(与原来一致)
	d = Decide(context.Background(), v, sets, domain("a.example"))
	if d.MatchError != "" || len(d.Assumed) != 1 {
		t.Fatalf("%+v", d)
	}
	// 读不出来的规则集:带不带地址都是没能检查
	bad := &fakeSets{m: map[string]fakeSet{"s1": {err: context.DeadlineExceeded}}}
	if d := Decide(context.Background(), v, bad, withDest("a.example", "8.8.8.8")); d.MatchError == "" {
		t.Fatal("读不出来的规则集要报没能检查")
	}
}
