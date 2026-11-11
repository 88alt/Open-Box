// Package nodeexit 实现 POST /api/openbox/node-info/{tag}/exit:一个节点的出口 IP 归属和风险评估。
// 响应形状取自前端 api/openbox.ts 的 OpenboxNodeExit,来源与取舍对齐上游 v0.1.423(见 netcoffee.go)。
//
// 做法:
//  1. 经这个节点(起一个临时 sing-box,和节点测速同一套 internal/probe)取 ip.net.coffee 的 cdn-cgi/trace,得到出口 IP 和国家;
//  2. 出口 IP 拿到后,由本进程直接(不经节点)问 ipv2/lookup(属地、ASN、类型、信任分、风险评估)
//     和 ipv2/radar(按 ASN 的近 28 天真人流量),两者合成 riskAssessment / humanTraffic;
//  3. lookup 没答就只给出口 IP 和国家,errors.netcoffee 写原因,这样的结果不缓存;
//  4. 「原生 IP」= 登记国家和实际所在国家一致;任何一边不知道就是 null(前端显示「未知」)。
package nodeexit

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	cacheTTL    = 6 * time.Hour  // 前端注释:6 小时缓存
	ipTTL       = 24 * time.Hour // 同一个出口 IP / 同一个 ASN 的第三方答话缓存(上游同为 24 小时)
	ipCacheMax  = 512
	minRefresh  = 15 * time.Second // 前端注释:15 秒内的重复刷新仍按缓存答
	cacheMax    = 256              // 最多缓存多少个节点
	runTimeout  = 40 * time.Second // 一次完整查询的上限(与调用方取消无关,免得连累等同一次结果的别人)
	maxInflight = 2                // 同时最多几个经节点的探测(每个都要起临时 sing-box,路由器上不宜多)
	lookupWait  = 6 * time.Second  // 每家第三方服务的等待上限
	bodyMax     = 512 << 10        // 第三方答话读多少字节为止
)

// ErrNoOutbound:这个 tag 在当前配置里没有可探测的出站(没启用、是组、或核心没生成)。
var ErrNoOutbound = errors.New("这个节点在当前配置里没有可用的出站(可能没启用,或是节点组)")

// ProbeError:经节点取出口 IP 失败(连不上、超时、接口都没答)。
type ProbeError struct{ Msg string }

func (e *ProbeError) Error() string { return e.Msg }

// Errors 对应前端 errors:某一路没查到的来源,值是失败原因。
type Errors struct {
	NetCoffee string `json:"netcoffee,omitempty"`
}

// Result 对应前端的 OpenboxNodeExit。
type Result struct {
	OK                bool            `json:"ok"`
	At                int64           `json:"at"` // 毫秒时间戳
	IP                string          `json:"ip"`
	IPv6              string          `json:"ipv6"`
	IPv6Checked       bool            `json:"ipv6Checked"`
	Location          string          `json:"location"`
	Country           string          `json:"country"`
	ASN               string          `json:"asn,omitempty"`
	ASNOwner          string          `json:"asnOwner,omitempty"`
	IPType            string          `json:"ipType"`
	ProxyType         string          `json:"proxyType"`
	TrustScore        *float64        `json:"trustScore"`
	Risk              *int            `json:"risk"`    // 上游固定 null,保留键
	Devices           *int            `json:"devices"` // 同上
	RegisteredCountry string          `json:"registeredCountry"`
	Native            *bool           `json:"native"`
	Source            string          `json:"source"`
	RiskAssessment    *RiskAssessment `json:"riskAssessment,omitempty"`
	HumanTraffic      *HumanTraffic   `json:"humanTraffic,omitempty"`
	Errors            *Errors         `json:"errors,omitempty"`
	Cached            bool            `json:"cached,omitempty"`
}

// ProbeFunc 经 tag 这个出站依次试 urls(第一个成功的为准),返回答话的地址和正文。
type ProbeFunc func(ctx context.Context, tag string, urls []string) (url, body string, err error)

// Service 查出口归属。零值不可用,至少要给 Probe。
type Service struct {
	Probe ProbeFunc

	// 下面几项只给测试替换;零值就是正式用法。
	Lookup Lookups
	Now    func() time.Time

	mu      sync.Mutex
	cache   map[string]*cached
	ipCache map[string]*ipEntry  // 出口 IP → lookup 结果
	asnData map[string]*asnEntry // ASN → radar 正文
	flights map[string]*flight
	semOnce sync.Once
	sem     chan struct{}
}

type cached struct {
	res Result
	at  time.Time
}

type ipEntry struct {
	info *netInfo
	at   time.Time
}

type asnEntry struct {
	text string
	at   time.Time
}

type flight struct {
	done chan struct{}
	res  *Result
	err  error
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Query 返回 tag 的出口信息。refresh 为真时跳过缓存重查,但 15 秒内刚查过的仍按缓存答。
// 同一个 tag 同时只会有一次真正的查询,别的调用等同一份结果。
func (s *Service) Query(ctx context.Context, tag string, refresh bool) (*Result, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil, ErrNoOutbound
	}
	if s.Probe == nil {
		return nil, errors.New("没有配置出口查询")
	}
	s.mu.Lock()
	if c, ok := s.cache[tag]; ok {
		age := s.now().Sub(c.at)
		if age < cacheTTL && (!refresh || age < minRefresh) {
			r := c.res
			r.Cached = true
			s.mu.Unlock()
			return &r, nil
		}
	}
	f, running := s.flights[tag]
	if !running {
		f = &flight{done: make(chan struct{})}
		if s.flights == nil {
			s.flights = map[string]*flight{}
		}
		s.flights[tag] = f
		go s.run(ctx, tag, f, refresh)
	}
	s.mu.Unlock()
	select {
	case <-f.done:
		if f.err != nil {
			return nil, f.err
		}
		r := *f.res
		return &r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) run(parent context.Context, tag string, f *flight, refresh bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), runTimeout)
	defer cancel()
	res, err := s.query(ctx, tag, refresh)
	s.mu.Lock()
	if err == nil && res.Errors == nil { // 不完整的结果(lookup 没答)不缓存
		if s.cache == nil {
			s.cache = map[string]*cached{}
		}
		s.evictLocked(tag)
		s.cache[tag] = &cached{res: *res, at: s.now()}
	}
	delete(s.flights, tag)
	s.mu.Unlock()
	f.res, f.err = res, err
	close(f.done)
}

// evictLocked 在放进新条目前保证不超过 cacheMax:先扔最旧的。
func (s *Service) evictLocked(keep string) {
	if _, exists := s.cache[keep]; exists || len(s.cache) < cacheMax {
		return
	}
	type kv struct {
		k string
		t time.Time
	}
	all := make([]kv, 0, len(s.cache))
	for k, c := range s.cache {
		all = append(all, kv{k, c.at})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	delete(s.cache, all[0].k)
}

func (s *Service) acquire(ctx context.Context) (func(), error) {
	s.semOnce.Do(func() { s.sem = make(chan struct{}, maxInflight) })
	select {
	case s.sem <- struct{}{}:
		return func() { <-s.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) query(ctx context.Context, tag string, refresh bool) (*Result, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	_, body, err := s.Probe(ctx, tag, []string{TraceURL})
	release()
	if err != nil {
		var pe *ProbeError
		if errors.As(err, &pe) {
			return nil, &ProbeError{Msg: "经这个节点访问 Net.Coffee 失败:" + pe.Msg}
		}
		return nil, err
	}
	ip, cc, ok := parseTrace(body)
	if !ok {
		return nil, &ProbeError{Msg: "Net.Coffee 返回的不是有效出口信息"}
	}
	ipStr := ip.String()
	res := &Result{OK: true, At: s.now().UnixMilli(), IP: ipStr, Country: cc, Source: "ip.net.coffee"}
	if ip.Is6() {
		res.IPv6 = ipStr // 另一个协议族的出口地址不单独查,只在出口本身是 IPv6 时给
		res.IPv6Checked = true
	}
	info := s.lookup(ctx, ipStr, refresh)
	if info == nil {
		s.mu.Lock()
		_, had := s.cache[tag]
		s.mu.Unlock()
		if had {
			return nil, &ProbeError{Msg: "Net.Coffee 查询失败,已保留上一次节点信息和风险评估"}
		}
		res.Errors = &Errors{NetCoffee: "Net.Coffee 暂未返回这个 IP 的详情"}
		return res, nil
	}
	if info.Country != "" {
		res.Country = info.Country
	}
	res.Location, res.ASN, res.ASNOwner = info.Location, info.ASN, info.ASNOwner
	res.IPType, res.ProxyType, res.TrustScore = info.IPType, info.ProxyType, info.TrustScore
	res.RegisteredCountry = info.RegisteredCountry
	ra := info.Risk
	res.RiskAssessment, res.HumanTraffic = &ra, info.Human
	if info.RegisteredCountry != "" && res.Country != "" {
		n := info.RegisteredCountry == res.Country
		res.Native = &n
	}
	return res, nil
}

// lookup 取出口 IP 的详情和真人流量,按 IP 缓存 24 小时;lookup 没有可用结果返回 nil(不缓存)。
func (s *Service) lookup(ctx context.Context, ip string, refresh bool) *netInfo {
	now := s.now()
	s.mu.Lock()
	if e, ok := s.ipCache[ip]; ok && !refresh && now.Sub(e.at) < ipTTL {
		s.mu.Unlock()
		return e.info
	}
	s.mu.Unlock()
	lk := s.Lookup.withDefaults()
	body, err := lk.get(ctx, lk.LookupURL(ip))
	if err != nil {
		return nil
	}
	info := parseLookup(body, ip)
	if info == nil {
		return nil
	}
	radar := ""
	if info.ASN != "" {
		radar = s.radar(ctx, lk, strings.TrimPrefix(info.ASN, "AS"), refresh)
	}
	info.Human = humanTraffic(body, radar, ip)
	s.mu.Lock()
	if s.ipCache == nil {
		s.ipCache = map[string]*ipEntry{}
	}
	for len(s.ipCache) >= ipCacheMax {
		for k := range s.ipCache {
			delete(s.ipCache, k)
			break
		}
	}
	s.ipCache[ip] = &ipEntry{info: info, at: now}
	s.mu.Unlock()
	return info
}

// radar 取某个 ASN 的统计正文,失败给空串;结果(含空串)按 ASN 缓存 24 小时,免得反复打一个坏接口。
func (s *Service) radar(ctx context.Context, lk Lookups, asn string, refresh bool) string {
	now := s.now()
	s.mu.Lock()
	if e, ok := s.asnData[asn]; ok && !refresh && now.Sub(e.at) < ipTTL {
		s.mu.Unlock()
		return e.text
	}
	s.mu.Unlock()
	text := ""
	if b, err := lk.get(ctx, lk.RadarURL(asn)); err == nil {
		text = string(b)
	}
	s.mu.Lock()
	if s.asnData == nil {
		s.asnData = map[string]*asnEntry{}
	}
	for len(s.asnData) >= ipCacheMax {
		for k := range s.asnData {
			delete(s.asnData, k)
			break
		}
	}
	s.asnData[asn] = &asnEntry{text: text, at: now}
	s.mu.Unlock()
	return text
}
