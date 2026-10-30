package dnsdecide

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func inlineSets(rules ...any) *Bound {
	f := &FileSets{}
	return f.Bind(map[string]SetDef{"s": {Type: "inline", Rules: rules}})
}

func TestMatchIPCIDRRules(t *testing.T) {
	b := inlineSets(
		map[string]any{"domain_suffix": []any{"example.com"}},
		map[string]any{"ip_cidr": []any{"10.0.0.0/8", "192.0.2.1", "2001:db8::/32"}},
	)
	ctx := context.Background()
	for _, c := range []struct {
		ip   string
		want bool
	}{
		{"10.1.2.3", true}, {"192.0.2.1", true}, {"192.0.2.2", false}, {"8.8.8.8", false},
		{"2001:db8::5", true}, {"2001:dead::1", false},
		{"::ffff:10.9.9.9", true}, // IPv4 映射地址按 IPv4 比
	} {
		got, err := b.MatchIP(ctx, "s", netip.MustParseAddr(c.ip))
		if err != nil || got != c.want {
			t.Errorf("%s: got %v %v, want %v", c.ip, got, err, c.want)
		}
	}
}

func TestMatchIPDomainOnlyRulesNeverMatchAnIP(t *testing.T) {
	b := inlineSets(map[string]any{"domain": []any{"a.com"}, "domain_suffix": []any{"b.com"}})
	if got, err := b.MatchIP(context.Background(), "s", netip.MustParseAddr("1.2.3.4")); got || err != nil {
		t.Fatalf("没有域名的连接对纯域名规则恒不命中,且这是确定的结论: %v %v", got, err)
	}
}

func TestMatchIPUndecidableShapes(t *testing.T) {
	cases := map[string][]any{
		"logical":     {map[string]any{"type": "logical", "mode": "and", "rules": []any{}}},
		"invert":      {map[string]any{"ip_cidr": []any{"10.0.0.0/8"}, "invert": true}},
		"域名和 IP 混在一条": {map[string]any{"domain": []any{"a.com"}, "ip_cidr": []any{"10.0.0.0/8"}}},
		"带端口":         {map[string]any{"ip_cidr": []any{"10.0.0.0/8"}, "port": []any{float64(443)}}},
		"写不对的网段":      {map[string]any{"ip_cidr": []any{"not-a-cidr"}}},
		"别的条件":        {map[string]any{"process_name": []any{"x"}}},
	}
	for name, rules := range cases {
		b := inlineSets(rules...)
		_, err := b.MatchIP(context.Background(), "s", netip.MustParseAddr("8.8.8.8"))
		if !errors.Is(err, ErrUnknownSet) || !errors.Is(err, ErrSetPartial) {
			t.Errorf("%s: 判断不了应同时是 ErrUnknownSet 和 ErrSetPartial: %v", name, err)
		}
	}
	// 已经命中就不再说判断不了
	b := inlineSets(
		map[string]any{"type": "logical", "rules": []any{}},
		map[string]any{"ip_cidr": []any{"8.8.8.0/24"}},
	)
	if got, err := b.MatchIP(context.Background(), "s", netip.MustParseAddr("8.8.8.8")); !got || err != nil {
		t.Fatalf("命中优先: %v %v", got, err)
	}
}

func TestMatchIPUnknownSetIsNotPartial(t *testing.T) {
	b := (&FileSets{}).Bind(map[string]SetDef{"remote": {Type: "remote"}})
	for _, tag := range []string{"nope", "remote"} {
		_, err := b.MatchIP(context.Background(), tag, netip.MustParseAddr("1.1.1.1"))
		if !errors.Is(err, ErrUnknownSet) || errors.Is(err, ErrSetPartial) {
			t.Errorf("%s: 规则集本身读不出来是 ErrUnknownSet、不是 ErrSetPartial(调用方靠它区分): %v", tag, err)
		}
	}
}

func TestMatchDomainPartialIsErrSetPartial(t *testing.T) {
	// 按域名查、规则集里只有 ip_cidr(例如 geoip-cn):不是读不出来,而是缺一个 IP
	b := inlineSets(map[string]any{"ip_cidr": []any{"10.0.0.0/8"}})
	_, err := b.Match(context.Background(), "s", "a.com")
	if !errors.Is(err, ErrUnknownSet) || !errors.Is(err, ErrSetPartial) {
		t.Fatalf("%v", err)
	}
	// 域名命中优先
	b = inlineSets(map[string]any{"ip_cidr": []any{"10.0.0.0/8"}}, map[string]any{"domain": []any{"a.com"}})
	if got, err := b.Match(context.Background(), "s", "a.com"); !got || err != nil {
		t.Fatalf("%v %v", got, err)
	}
}
