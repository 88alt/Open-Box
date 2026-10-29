// Package nodeexit 实现 POST /api/openbox/node-info/{tag}/exit:一个节点的出口 IP 归属和风控值。
// 响应形状取自前端 api/openbox.ts 的 OpenboxNodeExit,没有参照上游。
//
// 做法:
//  1. 经这个节点(起一个临时 sing-box,和节点测速同一套 internal/probe)去问 IP 信息接口,得到出口 IP 和国家;
//  2. 出口 IP 拿到后,由本进程直接(不经节点)并行问两家公开服务:
//     proxycheck.io(v3 接口:IP 类型、风控值、同网段设备数、ASN、属地)和 RDAP(IP 的登记国家);
//  3. 两路各自成败:一路没查到只在 errors 里写原因,另一路照常给;
//  4. 「原生 IP」= 登记国家和实际所在国家一致;任何一边不知道就是 null(前端显示「未知」)。
package nodeexit

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/regiondetect"
)

const (
	cacheTTL    = 6 * time.Hour    // 前端注释:6 小时缓存
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
	Proxycheck string `json:"proxycheck,omitempty"`
	RDAP       string `json:"rdap,omitempty"`
}

// Result 对应前端的 OpenboxNodeExit。
type Result struct {
	OK                bool    `json:"ok"`
	At                int64   `json:"at"` // 毫秒时间戳
	IP                string  `json:"ip"`
	IPv6              string  `json:"ipv6"`
	Location          string  `json:"location"`
	Country           string  `json:"country"`
	ASN               string  `json:"asn,omitempty"`
	ASNOwner          string  `json:"asnOwner,omitempty"`
	IPType            string  `json:"ipType"`
	ProxyType         string  `json:"proxyType"`
	Risk              *int    `json:"risk"`
	Devices           *int    `json:"devices"`
	RegisteredCountry string  `json:"registeredCountry"`
	Native            *bool   `json:"native"`
	Errors            *Errors `json:"errors,omitempty"`
	Cached            bool    `json:"cached,omitempty"`
}

// ProbeFunc 经 tag 这个出站依次试 urls(第一个成功的为准),返回答话的地址和正文。
type ProbeFunc func(ctx context.Context, tag string, urls []string) (url, body string, err error)

// Service 查出口归属。零值不可用,至少要给 Probe。
type Service struct {
	Probe  ProbeFunc
	IPURLs []string // 经节点问出口 IP 的接口;空 = regiondetect.DefaultURLs

	// 下面几项只给测试替换;零值就是正式用法。
	Lookup Lookups
	Now    func() time.Time

	mu      sync.Mutex
	cache   map[string]*cached
	flights map[string]*flight
	semOnce sync.Once
	sem     chan struct{}
}

type cached struct {
	res Result
	at  time.Time
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
		go s.run(ctx, tag, f)
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

func (s *Service) run(parent context.Context, tag string, f *flight) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), runTimeout)
	defer cancel()
	res, err := s.query(ctx, tag)
	s.mu.Lock()
	if err == nil {
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

func (s *Service) query(ctx context.Context, tag string) (*Result, error) {
	urls := s.IPURLs
	if len(urls) == 0 {
		urls = regiondetect.DefaultURLs
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	_, body, err := s.Probe(ctx, tag, urls)
	release()
	if err != nil {
		return nil, err
	}
	ipStr, cc, err := regiondetect.ParseBody([]byte(body))
	if err != nil {
		return nil, &ProbeError{Msg: "出口地址接口的答复看不懂:" + err.Error()}
	}
	ip := netip.MustParseAddr(ipStr).Unmap()
	res := &Result{OK: true, At: s.now().UnixMilli(), IP: ip.String(), Country: cc, ProxyType: "", IPType: ""}
	if ip.Is6() {
		res.IPv6 = ip.String() // 另一个协议族的出口地址不单独查(要再经节点问一个只走 IPv6 的接口),这里只在出口本身是 IPv6 时给
	}
	if !regiondetect.Public(ip) {
		why := "出口不是公网地址,不查归属"
		res.Errors = &Errors{Proxycheck: why, RDAP: why}
		return res, nil
	}
	lk := s.Lookup.withDefaults()
	var (
		wg   sync.WaitGroup
		pc   *proxycheckInfo
		pcE  error
		reg  string
		regE error
	)
	wg.Add(2)
	go func() { defer wg.Done(); pc, pcE = lk.proxycheck(ctx, ip) }()
	go func() { defer wg.Done(); reg, regE = lk.rdap(ctx, ip) }()
	wg.Wait()
	merge(res, pc, pcE, reg, regE)
	return res, nil
}

// merge 把两路结果并进 res。
func merge(res *Result, pc *proxycheckInfo, pcE error, reg string, regE error) {
	var errs Errors
	if pcE != nil {
		errs.Proxycheck = pcE.Error()
	} else if pc != nil {
		res.Location = pc.location()
		res.ASN, res.ASNOwner = pc.ASN, pc.Owner
		res.IPType, res.ProxyType = pc.IPType, pc.ProxyType
		res.Risk, res.Devices = pc.Risk, pc.Devices
	}
	if regE != nil {
		errs.RDAP = regE.Error()
	} else {
		res.RegisteredCountry = reg
		if reg != "" && res.Country != "" {
			n := strings.EqualFold(reg, res.Country)
			res.Native = &n
		}
	}
	if errs != (Errors{}) {
		res.Errors = &errs
	}
}
