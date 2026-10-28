package nodeinfo

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"
)

// 入口国家的缓存:成功的存一天(服务器 IP 的归属很少变),失败的只存一分钟(免得面板每次打开都去敲第三方)。
const (
	countryTTL    = 24 * time.Hour
	countryFailTL = time.Minute
	countryMax    = 512
)

// EntryResult 对应前端 fetchNodeEntryCountry 的响应:查的是哪个入口 IP、它的国家(两位大写)。
type EntryResult struct {
	IP      string `json:"ip"`
	Country string `json:"country"`
}

// ErrNoAddress 是节点没有可查的入口地址(解析不了、没有地址或不是公网地址)。
type ErrNoAddress struct{ Reason string }

func (e *ErrNoAddress) Error() string { return e.Reason }

type countryEntry struct {
	country string
	err     string
	at      time.Time
}

type countryCache struct {
	mu sync.Mutex
	m  map[netip.Addr]countryEntry
}

func (c *countryCache) get(ip netip.Addr, now time.Time) (countryEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[ip]
	if !ok {
		return e, false
	}
	ttl := countryTTL
	if e.err != "" {
		ttl = countryFailTL
	}
	if now.Sub(e.at) >= ttl {
		delete(c.m, ip)
		return countryEntry{}, false
	}
	return e, true
}

func (c *countryCache) put(ip netip.Addr, e countryEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[netip.Addr]countryEntry{}
	}
	if _, exists := c.m[ip]; !exists && len(c.m) >= countryMax {
		var oldest netip.Addr
		var oldAt time.Time
		first := true
		for k, v := range c.m {
			if first || v.at.Before(oldAt) {
				oldest, oldAt, first = k, v.at, false
			}
		}
		delete(c.m, oldest)
	}
	c.m[ip] = e
}

// entryAddr 取要查的入口地址:有 IPv4 用第一个(已排序,所以结果稳定),否则用第一个 IPv6;只认公网地址。
func entryAddr(info *Info, public func(netip.Addr) bool) (netip.Addr, bool) {
	for _, list := range [][]string{info.Addresses.V4, info.Addresses.V6} {
		for _, s := range list {
			if a, err := netip.ParseAddr(s); err == nil && public(a) {
				return a, true
			}
		}
	}
	return netip.Addr{}, false
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// cachedCountry 只看缓存,不联网:Get 用它填 entryCountry。
func (s *Service) cachedCountry(info *Info) {
	if s.IsPublic == nil {
		return
	}
	ip, ok := entryAddr(info, s.IsPublic)
	if !ok {
		return
	}
	if e, hit := s.countries.get(ip, s.now()); hit && e.err == "" {
		info.EntryCountry = e.country
	}
}

// EntryCountry 查节点入口 IP 的国家。节点找不到是 ErrNotFound;没有可查的地址是 *ErrNoAddress;
// 查归属失败是普通错误(失败结果缓存一分钟)。
func (s *Service) EntryCountry(ctx context.Context, tag string) (*EntryResult, error) {
	if s.Country == nil || s.IsPublic == nil {
		return nil, errors.New("没有配置归属查询")
	}
	info, err := s.Get(ctx, tag)
	if err != nil {
		return nil, err
	}
	ip, ok := entryAddr(info, s.IsPublic)
	if !ok {
		reason := info.ResolveError
		if reason == "" {
			reason = "没有可查的公网入口地址"
		}
		return nil, &ErrNoAddress{Reason: reason}
	}
	if e, hit := s.countries.get(ip, s.now()); hit {
		if e.err != "" {
			return nil, errors.New(e.err)
		}
		return &EntryResult{IP: ip.String(), Country: e.country}, nil
	}
	cc, err := s.Country(ctx, ip)
	if err != nil {
		if ctx.Err() == nil { // 调用方取消不算「查失败」,不缓存
			s.countries.put(ip, countryEntry{err: err.Error(), at: s.now()})
		}
		return nil, err
	}
	s.countries.put(ip, countryEntry{country: cc, at: s.now()})
	return &EntryResult{IP: ip.String(), Country: cc}, nil
}
