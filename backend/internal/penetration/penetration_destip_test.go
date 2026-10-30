package penetration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
	"github.com/timeflysoon/Open-Box/backend/internal/routedecide"
)

// 带 IP 能力的假规则集:domains 是后缀,cidrs 是网段;Entries 记下被问的 (规则集, 域名, IP)。
type ipSets struct {
	mu      sync.Mutex
	domains map[string][]string
	cidrs   map[string][]string
	asked   []string
}

func (s *ipSets) Match(_ context.Context, tag, d string) (bool, error) {
	for _, x := range s.domains[tag] {
		if dnsdecide.SuffixMatch(x, d) {
			return true, nil
		}
	}
	return false, nil
}

func (s *ipSets) MatchIP(_ context.Context, tag string, ip netip.Addr) (bool, error) {
	for _, c := range s.cidrs[tag] {
		if netip.MustParsePrefix(c).Contains(ip) {
			return true, nil
		}
	}
	return false, nil
}

func (s *ipSets) Entries(_ context.Context, tag, domain string, ip netip.Addr) ([]dnsdecide.Entry, error) {
	s.mu.Lock()
	s.asked = append(s.asked, fmt.Sprintf("%s|%s|%s", tag, domain, ip))
	s.mu.Unlock()
	return []dnsdecide.Entry{{Type: "ip_cidr", Value: "203.0.113.0/24"}}, nil
}

type fakeResolver struct {
	fake  bool
	addrs []string
	err   string
	calls []string
}

func (f *fakeResolver) ResolveName(_ context.Context, name string) (bool, []string, string) {
	f.calls = append(f.calls, name)
	return f.fake, f.addrs, f.err
}

func withResolver(t *testing.T, r Resolver) (*Service, *ipSets) {
	t.Helper()
	s := svc(t, proxies, nil)
	is := &ipSets{
		domains: map[string][]string{"obnode-direct": {"airport.example"}, "geosite-openai": {"openai.com"}, "geosite-cn": {"baidu.com"}},
		cidrs:   map[string][]string{"obnode-direct": {"203.0.113.0/24"}},
	}
	s.Sets = func(map[string]any) routedecide.Sets { return is }
	s.Resolver = r
	return s, is
}

func TestQueryDomainResolvedToPrivateAddress(t *testing.T) {
	r := &fakeResolver{addrs: []string{"192.168.1.9"}}
	s, _ := withResolver(t, r)
	res, err := s.Query(context.Background(), "NAS.example.")
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched == nil || res.Matched.Index != 1 || res.Matched.ViaIP != "192.168.1.9" || res.Matched.Outbound != "direct" {
		t.Fatalf("解析到私网地址应命中私网直连并记下地址: %+v", res.Matched)
	}
	if res.Resolved == nil || res.Resolved.FakeIP || res.Resolved.Error != "" || strings.Join(res.Resolved.Addresses, ",") != "192.168.1.9" {
		t.Fatalf("%+v", res.Resolved)
	}
	if res.Owner == nil || res.Owner.Name != "private" {
		t.Fatalf("%+v", res.Owner)
	}
	if strings.Join(r.calls, ",") != "nas.example" {
		t.Fatalf("解析的应是规范化后的域名: %v", r.calls)
	}
}

func TestQueryViaIPEntriesIncludeTheAddress(t *testing.T) {
	r := &fakeResolver{addrs: []string{"2001:db8::1", "203.0.113.5"}}
	s, is := withResolver(t, r)
	res, err := s.Query(context.Background(), "edge.example")
	if err != nil {
		t.Fatal(err)
	}
	// 第一个 IPv4(不是列表里第一个)当目的 IP;命中节点站点直连(规则集里的 IP 条目)
	if res.Matched == nil || res.Matched.Index != 3 || res.Matched.ViaIP != "203.0.113.5" {
		t.Fatalf("%+v", res.Matched)
	}
	if len(is.asked) != 1 || is.asked[0] != "obnode-direct|edge.example|203.0.113.5" {
		t.Fatalf("靠地址命中的,条目要把 IP 一起问: %v", is.asked)
	}
	if res.Matched.EntriesTotal != 1 || res.Matched.Entries[0].Source != "obnode-direct" {
		t.Fatalf("%+v", res.Matched)
	}
}

func TestQueryMatchedByDomainHasNoViaIP(t *testing.T) {
	r := &fakeResolver{addrs: []string{"104.18.1.1"}}
	s, is := withResolver(t, r)
	res, _ := s.Query(context.Background(), "chat.openai.com")
	if res.Matched == nil || res.Matched.Index != 4 || res.Matched.ViaIP != "" {
		t.Fatalf("%+v", res.Matched)
	}
	if len(is.asked) != 1 || !strings.HasSuffix(is.asked[0], "|invalid IP") {
		t.Fatalf("按域名命中的,条目只按域名问: %v", is.asked)
	}
}

func TestQueryFakeIPAddressesAreNotUsedAsDestination(t *testing.T) {
	// 占位地址:连接进内核后按域名找回目标,没有真实的目的 IP,IP 条件仍是前提;fakeIp 要标出来
	r := &fakeResolver{fake: true, addrs: []string{"198.19.0.7"}}
	s, _ := withResolver(t, r)
	res, _ := s.Query(context.Background(), "nas.example")
	if res.Matched != nil || res.Resolved == nil || !res.Resolved.FakeIP || len(res.Assumed) == 0 {
		t.Fatalf("%+v / %+v / %+v", res.Matched, res.Resolved, res.Assumed)
	}
}

func TestQueryResolveFailureBehavesLikeBefore(t *testing.T) {
	r := &fakeResolver{err: "域名不存在 (NXDOMAIN)"}
	s, _ := withResolver(t, r)
	res, _ := s.Query(context.Background(), "gone.example")
	if res.Resolved == nil || res.Resolved.Error == "" || res.Resolved.Addresses == nil || len(res.Resolved.Addresses) != 0 {
		t.Fatalf("没有地址时 addresses 是空数组、error 写原因: %+v", res.Resolved)
	}
	if res.Matched != nil || len(res.Assumed) == 0 {
		t.Fatalf("%+v %+v", res.Matched, res.Assumed)
	}
	b, _ := json.Marshal(res.Resolved)
	if !strings.Contains(string(b), `"addresses":[]`) || strings.Contains(string(b), "fakeIp") {
		t.Fatalf("JSON 形状:%s", b)
	}
}

func TestQueryIPv6OnlyAnswerIsUsed(t *testing.T) {
	r := &fakeResolver{addrs: []string{"fd00::9"}}
	s, _ := withResolver(t, r)
	res, _ := s.Query(context.Background(), "v6.example")
	if res.Matched == nil || res.Matched.Index != 1 || res.Matched.ViaIP != "fd00::9" {
		t.Fatalf("ULA 属于私网:%+v", res.Matched)
	}
}

func TestQueryNoResolverOrIPTarget(t *testing.T) {
	// 没接解析器:没有 resolved 字段,行为与原来一致
	s, _ := withResolver(t, nil)
	s.Resolver = nil
	res, _ := s.Query(context.Background(), "nas.example")
	if res.Resolved != nil || res.Matched != nil {
		t.Fatalf("%+v %+v", res.Resolved, res.Matched)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), "resolved") || strings.Contains(string(b), "viaIp") {
		t.Fatalf("没有解析时不该出现这两个字段:%s", b)
	}
	// IP 目标:不解析
	r := &fakeResolver{addrs: []string{"1.1.1.1"}}
	s, _ = withResolver(t, r)
	res, _ = s.Query(context.Background(), "192.168.3.3")
	if len(r.calls) != 0 || res.Resolved != nil || res.Matched == nil || res.Matched.ViaIP != "" {
		t.Fatalf("%v %+v %+v", r.calls, res.Resolved, res.Matched)
	}
}

func TestPickDest(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"2001:db8::1", "1.2.3.4", "5.6.7.8"}, "1.2.3.4"},
		{[]string{"junk", "", "2001:db8::2", "2001:db8::3"}, "2001:db8::2"},
		{[]string{"::ffff:9.9.9.9"}, "9.9.9.9"},
		{nil, ""},
		{[]string{"junk"}, ""},
	} {
		got := pickDest(c.in)
		if (c.want == "") == got.IsValid() || (c.want != "" && got.String() != c.want) {
			t.Errorf("%v → %v,想要 %q", c.in, got, c.want)
		}
	}
}
