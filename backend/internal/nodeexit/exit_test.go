package nodeexit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// proxycheck v3 官方文档里的示例(一个被识别为 VPN 的托管地址)。
const pcSample = `{"status":"ok","185.59.221.75":{
 "network":{"asn":"AS60068","range":"185.59.221.0/24","hostname":null,"provider":"Datacamp Limited","organisation":"CDN77 - London POP","type":"Hosting"},
 "location":{"country_name":"United Kingdom","country_code":"GB","region_name":"England","city_name":"London"},
 "device_estimate":{"address":50,"subnet":1890},
 "detections":{"proxy":false,"vpn":true,"compromised":true,"scraper":false,"tor":false,"hosting":true,"anonymous":true,"risk":100},
 "operator":{"name":"IVPN"},"last_updated":"2025-10-12T00:43:53Z"},"query_time":5}`

func TestParseProxycheckSample(t *testing.T) {
	ip := netip.MustParseAddr("185.59.221.75")
	got, err := parseProxycheck(200, []byte(pcSample), ip)
	if err != nil {
		t.Fatal(err)
	}
	if got.ASN != "AS60068" || got.Owner != "CDN77 - London POP" || got.IPType != "hosting" || got.ProxyType != "VPN / Compromised" {
		t.Errorf("网络与类型: %+v", got)
	}
	if got.Risk == nil || *got.Risk != 100 || got.Devices == nil || *got.Devices != 1890 {
		t.Errorf("风控值与同网段设备数(取 subnet): %+v", got)
	}
	if got.location() != "London, England, United Kingdom" {
		t.Errorf("属地: %q", got.location())
	}
}

func TestParseProxycheckNullsAndTypes(t *testing.T) {
	ip := netip.MustParseAddr("8.8.8.8")
	body := `{"status":"ok","8.8.8.8":{"network":{"asn":null,"provider":"Google LLC","organisation":null,"type":"Wireless"},"location":{"country_name":null,"city_name":"Tokyo","region_name":"Tokyo"},"device_estimate":{"address":null,"subnet":null},"detections":{"proxy":false,"vpn":false,"risk":0}}}`
	got, err := parseProxycheck(200, []byte(body), ip)
	if err != nil {
		t.Fatal(err)
	}
	if got.IPType != "mobile" || got.ProxyType != "" || got.Owner != "Google LLC" || got.ASN != "" || got.Devices != nil {
		t.Errorf("%+v", got)
	}
	if got.Risk == nil || *got.Risk != 0 {
		t.Error("risk 为 0 是有效值,不能当没有")
	}
	for in, want := range map[string]string{"Residential": "residential", "business": "business", "": "", "Satellite": ""} {
		if ipTypeOf(in) != want {
			t.Errorf("ipTypeOf(%q)", in)
		}
	}
}

func TestParseProxycheckFailures(t *testing.T) {
	ip := netip.MustParseAddr("8.8.8.8")
	cases := []struct {
		name string
		code int
		body string
		has  string
	}{
		{"次数用完", 429, `{"status":"denied","message":"100 queries exhausted, sign up"}`, "被拒绝:100 queries"},
		{"错误", 400, `{"status":"error","message":"No valid IP addresses supplied."}`, "被拒绝"},
		{"没带消息", 503, `{"status":"denied"}`, "HTTP 503"},
		{"不是 JSON", 502, `<html>`, "HTTP 502"},
		{"200 但不是 JSON", 200, `nope`, "不是 JSON"},
		{"没有这个 IP", 200, `{"status":"ok"}`, "没有这个 IP"},
		{"结果对象坏了", 200, `{"status":"ok","8.8.8.8":"x"}`, "格式不对"},
	}
	for _, c := range cases {
		if _, err := parseProxycheck(c.code, []byte(c.body), ip); err == nil || !strings.Contains(err.Error(), c.has) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// 长消息要截断
	long := `{"status":"denied","message":"` + strings.Repeat("长", 300) + `"}`
	if _, err := parseProxycheck(429, []byte(long), ip); err == nil || len([]rune(err.Error())) > 140 {
		t.Errorf("消息应截断: %v", err)
	}
}

func TestParseProxycheckV6KeyFallback(t *testing.T) {
	ip := netip.MustParseAddr("2606:4700:4700::1111")
	body := `{"status":"ok","2606:4700:4700:0:0:0:0:1111":{"network":{"type":"Business"},"detections":{"risk":3}}}`
	got, err := parseProxycheck(200, []byte(body), ip)
	if err != nil || got.IPType != "business" {
		t.Errorf("键写法不同时应能找到唯一的结果对象: %+v %v", got, err)
	}
}

func TestLookupsRDAP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ip/1.1.1.1":
			_, _ = w.Write([]byte(`{"objectClassName":"ip network","country":"au"}`))
		case "/ip/2.2.2.2": // ARIN 风格:顶层没有 country
			_, _ = w.Write([]byte(`{"objectClassName":"ip network","name":"X"}`))
		case "/ip/3.3.3.3":
			_, _ = w.Write([]byte(`{"country":"Australia"}`)) // 不是两位代码:不采信
		case "/ip/4.4.4.4":
			w.WriteHeader(404)
		default:
			_, _ = w.Write([]byte(`<html>`))
		}
	}))
	defer srv.Close()
	l := Lookups{Client: http.DefaultClient, RDAPURL: func(ip netip.Addr) string { return srv.URL + "/ip/" + ip.String() }}.withDefaults()
	for ip, want := range map[string]string{"1.1.1.1": "AU", "2.2.2.2": "", "3.3.3.3": ""} {
		if got, err := l.rdap(context.Background(), netip.MustParseAddr(ip)); err != nil || got != want {
			t.Errorf("%s: %q %v", ip, got, err)
		}
	}
	if _, err := l.rdap(context.Background(), netip.MustParseAddr("4.4.4.4")); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("404: %v", err)
	}
	if _, err := l.rdap(context.Background(), netip.MustParseAddr("5.5.5.5")); err == nil || !strings.Contains(err.Error(), "不是 JSON") {
		t.Errorf("非 JSON: %v", err)
	}
}

func TestDefaultClientRedirectPolicy(t *testing.T) {
	c := Lookups{}.withDefaults().Client
	mk := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	if err := c.CheckRedirect(mk("https://rdap.db.ripe.net/ip/1.1.1.1"), []*http.Request{{}}); err != nil {
		t.Errorf("RIR 应放行: %v", err)
	}
	for _, bad := range []string{"http://rdap.db.ripe.net/x", "https://192.168.1.1/x", "https://evil.example/x", "https://localhost/x"} {
		if err := c.CheckRedirect(mk(bad), []*http.Request{{}}); err == nil {
			t.Errorf("不该放行: %s", bad)
		}
	}
	if err := c.CheckRedirect(mk("https://rdap.arin.net/x"), make([]*http.Request, 4)); err == nil {
		t.Error("超过 3 跳应拒绝")
	}
}

// ---- Service ----

type fixture struct {
	svc                      *Service
	probes, pcHits, rdapHits atomic.Int32
	now                      time.Time
	mu                       sync.Mutex
	probeBody                string
	probeErr                 error
	pcStatus, rdapStatus     int
	pcBody, rdapBody         string
	probeGate                chan struct{}
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{now: time.Unix(1_700_000_000, 0), pcStatus: 200, rdapStatus: 200,
		probeBody: `{"ip":"185.59.221.75","country_code":"GB"}`, pcBody: pcSample, rdapBody: `{"country":"GB"}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/pc/") {
			f.pcHits.Add(1)
			w.WriteHeader(f.pcStatus)
			_, _ = w.Write([]byte(f.pcBody))
			return
		}
		f.rdapHits.Add(1)
		w.WriteHeader(f.rdapStatus)
		_, _ = w.Write([]byte(f.rdapBody))
	}))
	t.Cleanup(srv.Close)
	f.svc = &Service{
		Probe: func(ctx context.Context, tag string, urls []string) (string, string, error) {
			f.probes.Add(1)
			if f.probeGate != nil {
				<-f.probeGate
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return urls[0], f.probeBody, f.probeErr
		},
		Lookup: Lookups{Client: http.DefaultClient,
			ProxycheckURL: func(ip netip.Addr) string { return srv.URL + "/pc/" + ip.String() },
			RDAPURL:       func(ip netip.Addr) string { return srv.URL + "/rdap/" + ip.String() }},
		Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
	}
	return f
}

func (f *fixture) advance(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }

func TestQueryFullResult(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.Query(context.Background(), "HK 01", false)
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || r.IP != "185.59.221.75" || r.IPv6 != "" || r.Country != "GB" || r.Location != "London, England, United Kingdom" {
		t.Errorf("基本: %+v", r)
	}
	if r.ASN != "AS60068" || r.ASNOwner != "CDN77 - London POP" || r.IPType != "hosting" || r.ProxyType != "VPN / Compromised" {
		t.Errorf("网络: %+v", r)
	}
	if r.Risk == nil || *r.Risk != 100 || r.Devices == nil || *r.Devices != 1890 {
		t.Errorf("风控: %+v", r)
	}
	if r.RegisteredCountry != "GB" || r.Native == nil || !*r.Native || r.Errors != nil || r.Cached || r.At != f.now.UnixMilli() {
		t.Errorf("登记国与原生: %+v", r)
	}
}

func TestQueryNativeFalseAndUnknown(t *testing.T) {
	f := newFixture(t)
	f.rdapBody = `{"country":"US"}`
	r, _ := f.svc.Query(context.Background(), "a", false)
	if r.RegisteredCountry != "US" || r.Native == nil || *r.Native {
		t.Errorf("登记国和所在国不同应为非原生: %+v", r)
	}
	f.rdapBody = `{"name":"ARIN style"}`
	r, _ = f.svc.Query(context.Background(), "b", false)
	if r.RegisteredCountry != "" || r.Native != nil || r.Errors != nil {
		t.Errorf("登记国未知应为 null 且不算出错: %+v", r)
	}
}

func TestQueryPartialFailures(t *testing.T) {
	f := newFixture(t)
	f.pcStatus, f.pcBody = 429, `{"status":"denied","message":"100 queries exhausted"}`
	r, err := f.svc.Query(context.Background(), "a", false)
	if err != nil || !r.OK || r.Errors == nil || !strings.Contains(r.Errors.Proxycheck, "被拒绝") || r.Errors.RDAP != "" {
		t.Fatalf("proxycheck 失败时另一路照常: %+v %v", r, err)
	}
	if r.Risk != nil || r.Devices != nil || r.IPType != "" || r.RegisteredCountry != "GB" || r.Native == nil || !*r.Native || r.Country != "GB" {
		t.Errorf("没查到的项为空,查到的照给: %+v", r)
	}
	f2 := newFixture(t)
	f2.rdapStatus, f2.rdapBody = 500, `x`
	r, _ = f2.svc.Query(context.Background(), "a", false)
	if r.Errors == nil || r.Errors.RDAP != "HTTP 500" || r.Errors.Proxycheck != "" || r.Risk == nil || r.Native != nil {
		t.Errorf("RDAP 失败: %+v", r)
	}
}

func TestQueryExitNotPublicAndIPv6(t *testing.T) {
	f := newFixture(t)
	f.probeBody = `{"ip":"192.168.50.2","country_code":"CN"}`
	r, err := f.svc.Query(context.Background(), "a", false)
	if err != nil || r.Errors == nil || r.Errors.Proxycheck == "" || r.Errors.RDAP == "" || f.pcHits.Load()+f.rdapHits.Load() != 0 {
		t.Errorf("出口是内网地址时不该问第三方: %+v %v", r, err)
	}
	f2 := newFixture(t)
	f2.probeBody = `{"ip":"2606:4700:4700::1111","country_code":"US"}`
	f2.pcBody = `{"status":"ok","2606:4700:4700::1111":{"network":{"type":"Business"},"detections":{"risk":0}}}`
	r, _ = f2.svc.Query(context.Background(), "a", false)
	if r.IP != "2606:4700:4700::1111" || r.IPv6 != r.IP || r.IPType != "business" {
		t.Errorf("IPv6 出口: %+v", r)
	}
}

func TestQueryProbeFailureNotCached(t *testing.T) {
	f := newFixture(t)
	f.probeErr = &ProbeError{Msg: "连不上"}
	_, err := f.svc.Query(context.Background(), "a", false)
	var pe *ProbeError
	if !errors.As(err, &pe) {
		t.Fatalf("应透出 ProbeError: %v", err)
	}
	f.mu.Lock()
	f.probeErr = nil
	f.mu.Unlock()
	if r, err := f.svc.Query(context.Background(), "a", false); err != nil || !r.OK || f.probes.Load() != 2 {
		t.Errorf("失败不缓存,下次应重查: %v probes=%d", err, f.probes.Load())
	}
	f.probeBody = `not json`
	if _, err := f.svc.Query(context.Background(), "b", false); err == nil || !strings.Contains(err.Error(), "看不懂") {
		t.Errorf("接口答复看不懂: %v", err)
	}
	if _, err := f.svc.Query(context.Background(), "  ", false); !errors.Is(err, ErrNoOutbound) {
		t.Errorf("空 tag: %v", err)
	}
	if _, err := (&Service{}).Query(context.Background(), "a", false); err == nil {
		t.Error("没配 Probe 应报错")
	}
}

func TestCacheTTLAndRefreshWindow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r1, _ := f.svc.Query(ctx, "a", false)
	r2, _ := f.svc.Query(ctx, "a", false)
	if f.probes.Load() != 1 || !r2.Cached || r2.At != r1.At {
		t.Fatalf("第二次应走缓存: probes=%d %+v", f.probes.Load(), r2)
	}
	f.advance(10 * time.Second)
	if r, _ := f.svc.Query(ctx, "a", true); f.probes.Load() != 1 || !r.Cached {
		t.Errorf("15 秒内的刷新仍按缓存答: probes=%d", f.probes.Load())
	}
	f.advance(6 * time.Second) // 距上次 16 秒
	if r, _ := f.svc.Query(ctx, "a", true); f.probes.Load() != 2 || r.Cached {
		t.Errorf("超过 15 秒的刷新应重查: probes=%d", f.probes.Load())
	}
	f.advance(5*time.Hour + 59*time.Minute)
	if f.svc.Query(ctx, "a", false); f.probes.Load() != 2 {
		t.Errorf("不到 6 小时仍用缓存: probes=%d", f.probes.Load())
	}
	f.advance(2 * time.Minute)
	if r, _ := f.svc.Query(ctx, "a", false); f.probes.Load() != 3 || r.Cached {
		t.Errorf("过 6 小时应重查: probes=%d", f.probes.Load())
	}
	if f.svc.Query(ctx, "other", false); f.probes.Load() != 4 {
		t.Error("不同节点各自缓存")
	}
}

func TestConcurrentCallsShareOneProbeAndCancelDoesNotPoison(t *testing.T) {
	f := newFixture(t)
	f.probeGate = make(chan struct{})
	ctx1, cancel1 := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	results := make([]*Result, 3)
	errs := make([]error, 3)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := context.Background()
			if i == 0 {
				c = ctx1
			}
			results[i], errs[i] = f.svc.Query(c, "a", false)
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	cancel1() // 第一个调用方走了,不该连累别人
	close(f.probeGate)
	wg.Wait()
	if f.probes.Load() != 1 {
		t.Errorf("同时的查询只该探测一次: %d", f.probes.Load())
	}
	for i := 1; i < 3; i++ {
		if errs[i] != nil || results[i] == nil || !results[i].OK {
			t.Errorf("调用 %d: %v", i, errs[i])
		}
	}
	if errs[0] != nil && !errors.Is(errs[0], context.Canceled) {
		t.Errorf("取消的调用方应拿到取消错误: %v", errs[0])
	}
}

func TestCacheEvictsOldest(t *testing.T) {
	s := &Service{cache: map[string]*cached{}}
	base := time.Unix(1000, 0)
	for i := 0; i < cacheMax; i++ {
		s.cache[string(rune('A'+i%26))+strings.Repeat("x", i)] = &cached{at: base.Add(time.Duration(i) * time.Second)}
	}
	oldest := "A"
	s.evictLocked("new")
	if _, ok := s.cache[oldest]; ok || len(s.cache) != cacheMax-1 {
		t.Errorf("应挤掉最旧的: len=%d", len(s.cache))
	}
	s.evictLocked("Bx") // 已有的键不挤
	if len(s.cache) != cacheMax-1 {
		t.Error("更新已有条目不该挤别人")
	}
}
