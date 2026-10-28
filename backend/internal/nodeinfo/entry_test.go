package nodeinfo

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/regiondetect"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func entrySvc(addrs []string, country func(netip.Addr) (string, error), calls *int, c *clock) *Service {
	return &Service{
		Pool:   pool(vless),
		Lookup: lookup(map[string][]string{"hk.example.com": addrs}, nil),
		Country: func(_ context.Context, ip netip.Addr) (string, error) {
			*calls++
			return country(ip)
		},
		IsPublic: regiondetect.Public,
		Now:      c.now,
	}
}

func TestEntryCountryPicksV4CachesAndFeedsGet(t *testing.T) {
	calls := 0
	c := &clock{time.Unix(1000, 0)}
	s := entrySvc([]string{"2606:4700::1111", "203.0.113.9", "203.0.113.2"}, func(ip netip.Addr) (string, error) { return "JP", nil }, &calls, c)
	if info, _ := s.Get(context.Background(), "HK 01"); info.EntryCountry != "" {
		t.Fatalf("还没查过,缓存里没有时应为空: %q", info.EntryCountry)
	}
	r, err := s.EntryCountry(context.Background(), "HK 01")
	if err != nil || r.IP != "203.0.113.2" || r.Country != "JP" || calls != 1 {
		t.Fatalf("%+v %v calls=%d", r, err, calls)
	}
	if _, err := s.EntryCountry(context.Background(), "HK 01"); err != nil || calls != 1 {
		t.Errorf("第二次应走缓存: calls=%d err=%v", calls, err)
	}
	if info, _ := s.Get(context.Background(), "HK 01"); info.EntryCountry != "JP" {
		t.Errorf("查过之后 node-info 应带上国家: %q", info.EntryCountry)
	}
	c.t = c.t.Add(25 * time.Hour)
	if _, err := s.EntryCountry(context.Background(), "HK 01"); err != nil || calls != 2 {
		t.Errorf("过了一天应重查: calls=%d", calls)
	}
}

func TestEntryCountryFallsBackToV6AndSkipsNonPublic(t *testing.T) {
	calls := 0
	c := &clock{time.Unix(1000, 0)}
	s := entrySvc([]string{"192.168.1.5", "2606:4700::1111"}, func(netip.Addr) (string, error) { return "US", nil }, &calls, c)
	r, err := s.EntryCountry(context.Background(), "HK 01")
	if err != nil || r.IP != "2606:4700::1111" {
		t.Fatalf("内网 v4 应跳过,用公网 v6: %+v %v", r, err)
	}
}

func TestEntryCountryNoAddress(t *testing.T) {
	calls := 0
	c := &clock{time.Unix(1000, 0)}
	for name, addrs := range map[string][]string{"全是内网": {"10.0.0.1"}, "全是假 IP": {"198.18.0.9"}, "没有": nil} {
		s := entrySvc(addrs, func(netip.Addr) (string, error) { return "US", nil }, &calls, c)
		_, err := s.EntryCountry(context.Background(), "HK 01")
		var na *ErrNoAddress
		if !errors.As(err, &na) || na.Reason == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if calls != 0 {
		t.Errorf("没有可查地址时不该联网: %d", calls)
	}
}

func TestEntryCountryNotFoundAndUnconfigured(t *testing.T) {
	calls := 0
	c := &clock{time.Unix(1000, 0)}
	s := entrySvc([]string{"203.0.113.2"}, func(netip.Addr) (string, error) { return "US", nil }, &calls, c)
	if _, err := s.EntryCountry(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("没有这个节点: %v", err)
	}
	if _, err := (&Service{Pool: pool(vless)}).EntryCountry(context.Background(), "HK 01"); err == nil {
		t.Error("没配归属查询应报错")
	}
}

func TestEntryCountryFailureCachedBrieflyAndCancelNot(t *testing.T) {
	calls := 0
	c := &clock{time.Unix(1000, 0)}
	s := entrySvc([]string{"203.0.113.2"}, func(netip.Addr) (string, error) { return "", errors.New("查归属失败:全挂") }, &calls, c)
	for i := 0; i < 2; i++ {
		if _, err := s.EntryCountry(context.Background(), "HK 01"); err == nil {
			t.Fatal("应失败")
		}
	}
	if calls != 1 {
		t.Errorf("失败一分钟内不重查: calls=%d", calls)
	}
	if info, _ := s.Get(context.Background(), "HK 01"); info.EntryCountry != "" {
		t.Error("失败结果不能填进 entryCountry")
	}
	c.t = c.t.Add(61 * time.Second)
	_, _ = s.EntryCountry(context.Background(), "HK 01")
	if calls != 2 {
		t.Errorf("一分钟后应重查: calls=%d", calls)
	}
	// 调用方取消:不缓存
	ctx, cancel := context.WithCancel(context.Background())
	calls2 := 0
	s2 := entrySvc([]string{"203.0.113.2"}, func(netip.Addr) (string, error) { cancel(); return "", context.Canceled }, &calls2, c)
	_, _ = s2.EntryCountry(ctx, "HK 01")
	if _, err := s2.EntryCountry(context.Background(), "HK 01"); err == nil || calls2 != 2 {
		t.Errorf("取消不该被缓存成失败: calls=%d", calls2)
	}
}

func TestCountryCacheEvictsOldest(t *testing.T) {
	var cc countryCache
	base := time.Unix(1000, 0)
	for i := 0; i < countryMax; i++ {
		cc.put(netip.AddrFrom4([4]byte{8, 8, byte(i / 256), byte(i)}), countryEntry{country: "US", at: base.Add(time.Duration(i) * time.Second)})
	}
	cc.put(netip.MustParseAddr("9.9.9.9"), countryEntry{country: "US", at: base.Add(time.Hour)})
	if len(cc.m) != countryMax {
		t.Fatalf("容量: %d", len(cc.m))
	}
	if _, hit := cc.get(netip.AddrFrom4([4]byte{8, 8, 0, 0}), base.Add(time.Hour)); hit {
		t.Error("最旧的应被挤掉")
	}
}
