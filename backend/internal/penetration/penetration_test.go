package penetration

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
	"github.com/timeflysoon/Open-Box/backend/internal/routedecide"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

const cfgJSON = `{
 "route": {"final": "其他", "rules": [
  {"action": "sniff"},
  {"ip_is_private": true, "outbound": "direct"},
  {"network": "udp", "port": [123], "outbound": "direct"},
  {"rule_set": ["obnode-direct"], "outbound": "direct"},
  {"rule_set": ["geosite-openai"], "outbound": "AI"},
  {"domain_suffix": ["ads.example"], "action": "reject"},
  {"rule_set": ["geosite-cn"], "outbound": "国内"}
 ]},
 "outbounds": [{"tag":"direct","type":"direct"},{"tag":"AI","type":"selector"},{"tag":"其他","type":"selector"},{"tag":"国内","type":"selector"},{"tag":"hk-01","type":"shadowsocks"},{"tag":"jp-01","type":"shadowsocks"}]
}`

type sets map[string][]string

func (s sets) Match(_ context.Context, tag, d string) (bool, error) {
	l, ok := s[tag]
	if !ok {
		return false, errors.New("规则集读不到")
	}
	for _, x := range l {
		if dnsdecide.SuffixMatch(x, d) {
			return true, nil
		}
	}
	return false, nil
}
func (s sets) MatchIP(context.Context, string, netip.Addr) (bool, error) { return false, nil }

type dns struct {
	d   *dnsdecide.Decision
	err error
}

func (f dns) Predict(context.Context, string) (*dnsdecide.Decision, error) { return f.d, f.err }

func svc(t *testing.T, ps []singbox.KernelProxy, perr error) *Service {
	t.Helper()
	return &Service{
		Config: func() (map[string]any, error) {
			var m map[string]any
			return m, json.Unmarshal([]byte(cfgJSON), &m)
		},
		Sets: func(map[string]any) routedecide.Sets {
			return sets{"obnode-direct": {"airport.example"}, "geosite-openai": {"openai.com"}, "geosite-cn": {"baidu.com"}}
		},
		DNS:     dns{d: &dnsdecide.Decision{}},
		Proxies: func(context.Context) ([]singbox.KernelProxy, error) { return ps, perr },
	}
}

var proxies = []singbox.KernelProxy{
	{Name: "AI", Now: "hk-01"}, {Name: "其他", Now: "jp-01"}, {Name: "国内", Now: "direct"},
	{Name: "hk-01"}, {Name: "jp-01"},
}

func TestQueryMatchedChainAndOwner(t *testing.T) {
	r, err := svc(t, proxies, nil).Query(context.Background(), "Chat.OpenAI.com.")
	if err != nil {
		t.Fatal(err)
	}
	if r.Matched == nil || r.Matched.Index != 4 || r.Matched.Outbound != "AI" {
		t.Fatalf("%+v", r.Matched)
	}
	if r.Owner == nil || r.Owner.Kind != "policy" || r.Owner.Name != "AI" {
		t.Fatalf("%+v", r.Owner)
	}
	if strings.Join(r.Chain, ">") != "AI>hk-01" || r.FinalOutbound == nil || *r.FinalOutbound != "hk-01" {
		t.Fatalf("%v %v", r.Chain, r.FinalOutbound)
	}
	// 前面判断不了的规则(私网 / NTP / 节点站点里按 IP 的)进前提,带叶子和是否同结果
	if len(r.Assumed) != 2 {
		t.Fatalf("%+v", r.Assumed)
	}
	for _, a := range r.Assumed {
		if a.Leaf != "direct" || a.SameOutcome == nil || *a.SameOutcome {
			t.Errorf("前提的叶子是直连,和 hk-01 不同: %+v", a)
		}
	}
	if r.DNS == nil || r.RoutingStale {
		t.Fatalf("%+v", r)
	}
}

func TestQueryFallthroughToFinal(t *testing.T) {
	r, err := svc(t, proxies, nil).Query(context.Background(), "unknown.example")
	if err != nil {
		t.Fatal(err)
	}
	if r.Matched != nil || r.Owner == nil || r.Owner.Name != "other" || strings.Join(r.Chain, ">") != "其他>jp-01" {
		t.Fatalf("%+v %v", r, r.Chain)
	}
}

func TestQueryRejectHasNoChain(t *testing.T) {
	r, _ := svc(t, proxies, nil).Query(context.Background(), "x.ads.example")
	if r.Matched == nil || r.Matched.Action != "reject" || len(r.Chain) != 0 || r.FinalOutbound != nil {
		t.Fatalf("%+v", r)
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"chain":[]`) || !strings.Contains(string(b), `"finalOutbound":null`) {
		t.Fatalf("chain 恒为数组、finalOutbound 为 null: %s", b)
	}
}

func TestQueryIPTargetAndBuiltinOwners(t *testing.T) {
	r, _ := svc(t, proxies, nil).Query(context.Background(), "192.168.1.9")
	if r.Matched.Index != 1 || r.Owner == nil || r.Owner.Kind != "builtin" || r.Owner.Name != "private" {
		t.Fatalf("%+v %+v", r.Matched, r.Owner)
	}
	if m, _ := r.DNS.(map[string]any); m["skipped"] != true {
		t.Fatalf("IP 目标不做 DNS 判定: %+v", r.DNS)
	}
	r, _ = svc(t, proxies, nil).Query(context.Background(), "x.airport.example")
	if r.Owner == nil || r.Owner.Name != "nodes" {
		t.Fatalf("%+v", r.Owner)
	}
}

func TestQueryRuleSetFailureStopsEverything(t *testing.T) {
	s := svc(t, proxies, nil)
	s.Sets = func(map[string]any) routedecide.Sets { return sets{} }
	r, err := s.Query(context.Background(), "chat.openai.com")
	if err != nil {
		t.Fatal(err)
	}
	if r.MatchError == "" || r.Matched != nil || r.FinalOutbound != nil || len(r.Chain) != 0 || r.Assumed != nil {
		t.Fatalf("没能检查:不给结论、不给线路: %+v", r)
	}
}

func TestQueryProxyErrorKeepsFirstLayer(t *testing.T) {
	r, _ := svc(t, nil, errors.New("内核没在跑")).Query(context.Background(), "chat.openai.com")
	if r.ChainError == "" || strings.Join(r.Chain, ">") != "AI" || r.FinalOutbound != nil || r.Matched == nil {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.ChainError, "内核没在跑") {
		t.Fatalf("%q", r.ChainError)
	}
}

func TestQueryDNSErrorAndStale(t *testing.T) {
	s := svc(t, proxies, nil)
	s.DNS = dns{err: errors.New("boom")}
	s.Stale = func(context.Context) bool { return true }
	r, _ := s.Query(context.Background(), "chat.openai.com")
	m, _ := r.DNS.(map[string]any)
	if !strings.Contains(m["error"].(string), "boom") || !r.RoutingStale {
		t.Fatalf("%+v", r)
	}
	s.DNS = nil
	if r, _ = s.Query(context.Background(), "chat.openai.com"); r.DNS.(map[string]any)["error"] == "" {
		t.Fatalf("%+v", r.DNS)
	}
}

func TestQueryConfigErrors(t *testing.T) {
	s := svc(t, proxies, nil)
	s.Config = func() (map[string]any, error) { return nil, errors.New("读不到当前配置(还没有部署过?)") }
	if _, err := s.Query(context.Background(), "a.com"); err == nil {
		t.Fatal("读不到配置应返回 error")
	}
	s.Config = func() (map[string]any, error) { return map[string]any{}, nil }
	if _, err := s.Query(context.Background(), "a.com"); err == nil {
		t.Fatal("没有 route 段应返回 error")
	}
}

func TestParseTarget(t *testing.T) {
	ok := map[string]routedecide.Target{
		"Example.COM.":   {Domain: "example.com"},
		"1.2.3.4":        {IP: netip.MustParseAddr("1.2.3.4")},
		"::ffff:1.2.3.4": {IP: netip.MustParseAddr("1.2.3.4")},
		"2001:db8::1":    {IP: netip.MustParseAddr("2001:db8::1")},
		"  a.b-c.com ":   {Domain: "a.b-c.com"},
	}
	for in, want := range ok {
		got, err := ParseTarget(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "  ", "-a.com", "a b.com", "a.com/x", "host:80", "a_b.com;rm", "..", ".", strings.Repeat("a", 254)} {
		_, err := ParseTarget(in)
		var ie *InputError
		if !errors.As(err, &ie) {
			t.Errorf("%q 应是输入错误: %v", in, err)
		}
	}
}

func TestDrillStopsOnLoops(t *testing.T) {
	now := map[string]string{"a": "b", "b": "a"}
	known := map[string]bool{"a": true, "b": true}
	if got := drill("a", now, known); len(got) != maxChain+1 {
		t.Fatalf("环要在 %d 层停下: %v", maxChain, got)
	}
	// 选中的叶子不在代理列表里:记进去、停下
	if got := drill("g", map[string]string{"g": "direct"}, map[string]bool{"g": true}); strings.Join(got, ">") != "g>direct" {
		t.Fatalf("%v", got)
	}
}
