package nodeexit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 下面的样例是按上游读取 ip.net.coffee 答话时用到的字段自己拼的(没有官方文档,也没有抓到真实答话),
// 只覆盖解析逻辑读到的键;真实答话的差异要靠真机核对。
const traceSample = "fl=1\nh=ip.net.coffee\nip=185.59.221.75\nts=1\nloc=GB\n"

const lookupSample = `{"ip":"185.59.221.75","country":"United Kingdom","countryCode":"GB","registered_country_code":"GB",
"region":"England","city":"London","asn":60068,"asOrganization":"Datacamp Limited","company_type":"hosting",
"is_datacenter":true,"is_vpn":true,"is_proxy":false,"is_tor":false,"is_crawler":false,"trust_score":42.5,"rpki_status":"Valid",
"intelligence":{"abuser_level":"High","abuser_score_raw":"0.0123 (High)","rep_threat":3,
"threats":[{"label":"VPN","severity":"warn"},{"label":"x","severity":"weird"}]}}`

const radarSample = `{"ok":true,"asn":60068,"range":"28d","human":12.3,"stale":false}`

func TestParseTrace(t *testing.T) {
	ip, cc, ok := parseTrace(traceSample)
	if !ok || ip.String() != "185.59.221.75" || cc != "GB" {
		t.Fatalf("%v %q %v", ip, cc, ok)
	}
	if _, cc, ok := parseTrace("h=ip.net.coffee\r\nip=2606:4700:4700::1111\r\nloc=zz\r\n"); !ok || cc != "" {
		t.Errorf("IPv6 / CRLF / 国家码不合格: %q %v", cc, ok)
	}
	for _, bad := range []string{
		"h=example.com\nip=8.8.8.8\n",
		"ip=8.8.8.8\n",
		"h=ip.net.coffee\nip=192.168.1.2\n",
		"h=ip.net.coffee\nip=198.18.0.5\n",
		"h=ip.net.coffee\nip=100.64.1.1\n",
		"h=ip.net.coffee\nip=::1\n",
		"h=ip.net.coffee\nip=fd00::1\n",
		"h=ip.net.coffee\nip=not-an-ip\n",
		"",
	} {
		if _, _, ok := parseTrace(bad); ok {
			t.Errorf("不该认: %q", bad)
		}
	}
}

func TestParseLookupSample(t *testing.T) {
	got := parseLookup([]byte(lookupSample), "185.59.221.75")
	if got == nil {
		t.Fatal("nil")
	}
	if got.Country != "GB" || got.RegisteredCountry != "GB" || got.ASN != "AS60068" || got.ASNOwner != "Datacamp Limited" {
		t.Errorf("基本: %+v", got)
	}
	if got.Location != "United Kingdom · England · London" || got.IPType != "hosting" || got.ProxyType != "VPN" {
		t.Errorf("属地与类型: %+v", got)
	}
	if got.TrustScore == nil || *got.TrustScore != 42.5 {
		t.Errorf("信任分: %v", got.TrustScore)
	}
	r := got.Risk
	if r.AbuseLevel != "high" || r.AbuseScore == nil || *r.AbuseScore != 0.0123 || r.HTTPBlack == nil || *r.HTTPBlack != 3 {
		t.Errorf("滥用: %+v", r)
	}
	if r.RPKIReported != "valid" || r.RPKI != "" || r.VPN == nil || !*r.VPN || r.Proxy == nil || *r.Proxy || r.Tor == nil || *r.Tor {
		t.Errorf("RPKI 与标志: %+v", r)
	}
	if len(r.Threats) != 2 || r.Threats[0] != (Threat{"VPN", "warn"}) || r.Threats[1].Severity != "info" {
		t.Errorf("标签(不认识的严重度记为 info): %+v", r.Threats)
	}
}

func TestParseLookupRejectsAndNulls(t *testing.T) {
	ip := "8.8.8.8"
	for name, body := range map[string]string{
		"不是 JSON": `nope`,
		"IP 对不上":  `{"ip":"1.1.1.1","countryCode":"US"}`,
		"带 error": `{"ip":"8.8.8.8","error":"x","countryCode":"US"}`,
		"bogon":   `{"ip":"8.8.8.8","is_bogon":true,"countryCode":"US"}`,
		"什么都没有":   `{"ip":"8.8.8.8"}`,
	} {
		if parseLookup([]byte(body), ip) != nil {
			t.Errorf("%s 应为 nil", name)
		}
	}
	got := parseLookup([]byte(`{"ip":"8.8.8.8","asn":"AS15169","intelligence":{"threats":[{"label":"a"},{"nolabel":1}],"abuser_score_raw":"n/a"},"trust_score":101}`), ip)
	if got == nil || got.ASN != "AS15169" || got.TrustScore != nil {
		t.Fatalf("只有 ASN 也算有信息,越界的信任分当没有: %+v", got)
	}
	if got.Risk.Threats != nil || got.Risk.AbuseScore != nil {
		t.Errorf("标签里有一项没 label 整块为 null;写不出数字的滥用分为 null: %+v", got.Risk)
	}
	if b, _ := json.Marshal(got.Risk); !strings.Contains(string(b), `"threats":null`) || !strings.Contains(string(b), `"abuseScore":null`) {
		t.Errorf("JSON 形状: %s", b)
	}
	many := `{"ip":"8.8.8.8","countryCode":"US","intelligence":{"threats":[` + strings.Repeat(`{"label":"t","severity":"bad"},`, 25) + `{"label":"t"}]}}`
	if g := parseLookup([]byte(many), ip); len(g.Risk.Threats) != 20 {
		t.Errorf("标签最多 20 条: %d", len(g.Risk.Threats))
	}
	ab := parseLookup([]byte(`{"ip":"8.8.8.8","countryCode":"US","abuser_score":" 0.5 (Low) "}`), ip)
	if ab.Risk.AbuseScore == nil || *ab.Risk.AbuseScore != 0.5 {
		t.Error("没有 intelligence 时取顶层 abuser_score")
	}
}

func TestIPTypeOrder(t *testing.T) {
	cases := map[string]string{
		`"is_mobile":true,"is_datacenter":true`:   "mobile",
		`"isResidential":true`:                    "residential",
		`"company_type":"Residential"`:            "residential",
		`"asn_kind":"cdn"`:                        "hosting",
		`"company_type":"business","is_vpn":true`: "business",
		`"company_type":"isp","trust_score":10`:   "",
	}
	for frag, want := range cases {
		g := parseLookup([]byte(`{"ip":"8.8.8.8","countryCode":"US",`+frag+`}`), "8.8.8.8")
		if g == nil || g.IPType != want {
			t.Errorf("%s → %+v", frag, g)
		}
	}
}

func TestHumanTraffic(t *testing.T) {
	ip := "8.8.8.8"
	h := func(lookup, radar string) *HumanTraffic { return humanTraffic([]byte(lookup), radar, ip) }
	got := h(`{"ip":"8.8.8.8","asn":"AS60068","company_type":"hosting"}`, radarSample)
	if got == nil || got.Human != 12.3 || got.Bot != 87.7 || got.Basis != "asn-28d" || got.Stale {
		t.Errorf("28 天统计: %+v", got)
	}
	got = h(`{"ip":"8.8.8.8","asn":60068,"company_type":"hosting"}`, strings.Replace(radarSample, `"stale":false`, `"stale":true`, 1))
	if got == nil || !got.Stale {
		t.Errorf("stale: %+v", got)
	}
	cases := []struct {
		name, lookup, radar string
		human               float64
	}{
		{"托管", `"company_type":"hosting"`, ``, 18},
		{"托管加 VPN", `"company_type":"hosting","is_vpn":true`, ``, 10.8},
		{"Tor 比 VPN 先算", `"is_datacenter":true,"is_tor":true,"is_vpn":true`, ``, 7.2},
		{"手机", `"is_mobile":true`, ``, 93},
		{"商业", `"company_type":"business"`, ``, 65},
		{"住宅(默认)", `"isResidential":true`, ``, 88},
		{"公共服务", `"is_public_service":true,"is_datacenter":true`, ``, 2},
		{"爬虫", `"is_crawler":true`, ``, 3.8},
		{"滥用者再减 5", `"isResidential":true,"is_abuser":true`, ``, 83},
		{"ASN 对不上退回估算", `"asn":"AS1","company_type":"hosting"`, radarSample, 18},
		{"range 不是 28d", `"asn":"AS60068","is_mobile":true`, strings.Replace(radarSample, "28d", "7d", 1), 93},
		{"下限 1", `"is_tor":true,"is_public_service":true,"is_abuser":true`, ``, 1},
	}
	for _, c := range cases {
		g := h(`{"ip":"8.8.8.8",`+c.lookup+`}`, c.radar)
		if g == nil || g.Human != c.human || g.Basis != "network-estimate" && c.radar == "" {
			t.Errorf("%s: %+v 想要 %v", c.name, g, c.human)
		}
	}
	if h(`{"ip":"8.8.8.8","countryCode":"US"}`, "") != nil {
		t.Error("没有任何线索应为 nil")
	}
	if humanTraffic([]byte(`{"ip":"1.1.1.1","is_mobile":true}`), "", ip) != nil {
		t.Error("IP 对不上应为 nil")
	}
}

func TestDefaultClientRedirectPolicy(t *testing.T) {
	c := Lookups{}.withDefaults().Client
	mk := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	if err := c.CheckRedirect(mk("https://ip.net.coffee/api/x"), []*http.Request{{}}); err != nil {
		t.Errorf("本站应放行: %v", err)
	}
	for _, bad := range []string{"http://ip.net.coffee/x", "https://192.168.1.1/x", "https://evil.example/x", "https://localhost/x"} {
		if err := c.CheckRedirect(mk(bad), []*http.Request{{}}); err == nil {
			t.Errorf("不该放行: %s", bad)
		}
	}
	if err := c.CheckRedirect(mk("https://ip.net.coffee/x"), make([]*http.Request, 4)); err == nil {
		t.Error("超过 3 跳应拒绝")
	}
}

// ---- Service ----

type fixture struct {
	svc                       *Service
	probes, lookups, radars   atomic.Int32
	now                       time.Time
	mu                        sync.Mutex
	probeBody                 string
	probeErr                  error
	lookupStatus, radarStatus int
	lookupBody, radarBody     string
	probeGate                 chan struct{}
	lastProbeURLs             []string
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{now: time.Unix(1_700_000_000, 0), lookupStatus: 200, radarStatus: 200,
		probeBody: traceSample, lookupBody: lookupSample, radarBody: radarSample}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/lookup/") {
			f.lookups.Add(1)
			w.WriteHeader(f.lookupStatus)
			_, _ = w.Write([]byte(f.lookupBody))
			return
		}
		f.radars.Add(1)
		w.WriteHeader(f.radarStatus)
		_, _ = w.Write([]byte(f.radarBody))
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
			f.lastProbeURLs = urls
			return urls[0], f.probeBody, f.probeErr
		},
		Lookup: Lookups{Client: http.DefaultClient,
			LookupURL: func(ip string) string { return srv.URL + "/lookup/" + ip },
			RadarURL:  func(asn string) string { return srv.URL + "/radar/" + asn }},
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
	if len(f.lastProbeURLs) != 1 || f.lastProbeURLs[0] != TraceURL {
		t.Errorf("经节点问的是 trace: %v", f.lastProbeURLs)
	}
	if !r.OK || r.Source != "ip.net.coffee" || r.IP != "185.59.221.75" || r.IPv6 != "" || r.Country != "GB" || r.Location != "United Kingdom · England · London" {
		t.Errorf("基本: %+v", r)
	}
	if r.ASN != "AS60068" || r.ASNOwner != "Datacamp Limited" || r.IPType != "hosting" || r.ProxyType != "VPN" {
		t.Errorf("网络: %+v", r)
	}
	if r.TrustScore == nil || *r.TrustScore != 42.5 || r.Risk != nil || r.Devices != nil {
		t.Errorf("信任分;risk / devices 固定 null: %+v", r)
	}
	if r.RiskAssessment == nil || r.RiskAssessment.AbuseLevel != "high" || r.HumanTraffic == nil || r.HumanTraffic.Human != 7.4 || r.HumanTraffic.Basis != "asn-28d" {
		t.Errorf("风险评估与真人流量: %+v %+v", r.RiskAssessment, r.HumanTraffic)
	}
	if r.RegisteredCountry != "GB" || r.Native == nil || !*r.Native || r.Errors != nil || r.Cached || r.At != f.now.UnixMilli() {
		t.Errorf("登记国与原生: %+v", r)
	}
	b, _ := json.Marshal(r)
	for _, k := range []string{`"risk":null`, `"devices":null`, `"trustScore":42.5`, `"source":"ip.net.coffee"`, `"humanTraffic":{`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("JSON 缺 %s: %s", k, b)
		}
	}
}

func TestQueryNativeFalse(t *testing.T) {
	f := newFixture(t)
	f.lookupBody = strings.Replace(lookupSample, `"registered_country_code":"GB"`, `"registered_country_code":"US"`, 1)
	r, _ := f.svc.Query(context.Background(), "a", false)
	if r.RegisteredCountry != "US" || r.Native == nil || *r.Native {
		t.Errorf("登记国和所在国不同: %+v", r)
	}
	f2 := newFixture(t)
	f2.lookupBody = strings.Replace(lookupSample, `"registered_country_code":"GB",`, ``, 1)
	r, _ = f2.svc.Query(context.Background(), "a", false)
	if r.RegisteredCountry != "" || r.Native != nil || r.Errors != nil {
		t.Errorf("登记国未知应为 null 且不算出错: %+v", r)
	}
}

func TestQueryLookupFailureNotCached(t *testing.T) {
	f := newFixture(t)
	f.lookupStatus, f.lookupBody = 503, `x`
	r, err := f.svc.Query(context.Background(), "a", false)
	if err != nil || !r.OK || r.Errors == nil || r.Errors.NetCoffee == "" || r.IP != "185.59.221.75" || r.Country != "GB" {
		t.Fatalf("lookup 没答仍给出口 IP 和国家: %+v %v", r, err)
	}
	if r.RiskAssessment != nil || r.TrustScore != nil || r.Native != nil {
		t.Errorf("没查到的项为空: %+v", r)
	}
	f.mu.Lock()
	f.lookupStatus, f.lookupBody = 200, lookupSample
	f.mu.Unlock()
	r, _ = f.svc.Query(context.Background(), "a", false)
	if f.probes.Load() != 2 || r.Errors != nil || r.Cached {
		t.Errorf("不完整的结果不缓存,下次重查: probes=%d %+v", f.probes.Load(), r)
	}
}

func TestQueryLookupFailureKeepsPrevious(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Query(ctx, "a", false); err != nil {
		t.Fatal(err)
	}
	f.advance(20 * time.Second)
	f.mu.Lock()
	f.lookupStatus, f.lookupBody = 500, `x`
	f.mu.Unlock()
	_, err := f.svc.Query(ctx, "a", true)
	var pe *ProbeError
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, "已保留上一次") {
		t.Fatalf("刷新时 lookup 失败应报错并保留旧结果: %v", err)
	}
	if r, err := f.svc.Query(ctx, "a", false); err != nil || !r.Cached || r.RiskAssessment == nil {
		t.Errorf("旧结果还在缓存里: %+v %v", r, err)
	}
}

func TestQueryProbeFailures(t *testing.T) {
	f := newFixture(t)
	f.probeErr = &ProbeError{Msg: "连不上"}
	_, err := f.svc.Query(context.Background(), "a", false)
	var pe *ProbeError
	if !errors.As(err, &pe) || pe.Msg != "经这个节点访问 Net.Coffee 失败:连不上" {
		t.Fatalf("应带前缀透出: %v", err)
	}
	f.probeErr = ErrNoOutbound
	if _, err := f.svc.Query(context.Background(), "a", false); !errors.Is(err, ErrNoOutbound) {
		t.Errorf("没有出站原样透出: %v", err)
	}
	f.probeErr = nil
	f.probeBody = "not a trace"
	if _, err := f.svc.Query(context.Background(), "b", false); err == nil || !strings.Contains(err.Error(), "不是有效出口信息") {
		t.Errorf("答复看不懂: %v", err)
	}
	f.probeBody = "h=ip.net.coffee\nip=192.168.50.2\n"
	if _, err := f.svc.Query(context.Background(), "b", false); err == nil || f.lookups.Load() != 0 {
		t.Errorf("出口是内网地址不该问第三方: %v lookups=%d", err, f.lookups.Load())
	}
	f.probeBody = traceSample
	if r, err := f.svc.Query(context.Background(), "a", false); err != nil || !r.OK {
		t.Errorf("失败不缓存,下次应重查: %v", err)
	}
	if _, err := f.svc.Query(context.Background(), "  ", false); !errors.Is(err, ErrNoOutbound) {
		t.Errorf("空 tag: %v", err)
	}
	if _, err := (&Service{}).Query(context.Background(), "a", false); err == nil {
		t.Error("没配 Probe 应报错")
	}
}

func TestQueryIPv6Exit(t *testing.T) {
	f := newFixture(t)
	f.probeBody = "h=ip.net.coffee\nip=2606:4700:4700::1111\nloc=US\n"
	f.lookupBody = `{"ip":"2606:4700:4700::1111","countryCode":"US","company_type":"business"}`
	r, err := f.svc.Query(context.Background(), "a", false)
	if err != nil || r.IPv6 != r.IP || !r.IPv6Checked || r.IPType != "business" {
		t.Errorf("IPv6 出口: %+v %v", r, err)
	}
	if r.HumanTraffic == nil || r.HumanTraffic.Human != 65 {
		t.Errorf("没有 ASN 不取 radar,按类型估: %+v", r.HumanTraffic)
	}
	if f.radars.Load() != 0 {
		t.Error("没有 ASN 就不该问 radar")
	}
}

func TestThirdPartyCaches(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc.Query(ctx, "a", false)
	f.svc.Query(ctx, "b", false) // 另一个节点,同一个出口 IP
	if f.lookups.Load() != 1 || f.radars.Load() != 1 || f.probes.Load() != 2 {
		t.Errorf("同一个 IP 的 lookup / radar 只问一次: lookups=%d radars=%d", f.lookups.Load(), f.radars.Load())
	}
	f.advance(20 * time.Second)
	f.svc.Query(ctx, "a", true) // 刷新跳过第三方缓存
	if f.lookups.Load() != 2 || f.radars.Load() != 2 {
		t.Errorf("刷新应重问: lookups=%d radars=%d", f.lookups.Load(), f.radars.Load())
	}
	f.advance(25 * time.Hour)
	f.svc.Query(ctx, "c", false)
	if f.lookups.Load() != 3 {
		t.Errorf("24 小时后过期: %d", f.lookups.Load())
	}
}

func TestRadarFailureFallsBackToEstimate(t *testing.T) {
	f := newFixture(t)
	f.radarStatus, f.radarBody = 500, `x`
	r, _ := f.svc.Query(context.Background(), "a", false)
	if r.HumanTraffic == nil || r.HumanTraffic.Basis != "network-estimate" || r.HumanTraffic.Human != 10.8 || r.Errors != nil {
		t.Errorf("radar 失败按类型估,不算出错: %+v", r.HumanTraffic)
	}
	f.svc.Query(context.Background(), "b", false)
	if f.radars.Load() != 1 {
		t.Error("radar 失败的结果也缓存,免得反复打坏接口")
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
	f.advance(6 * time.Second)
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
	cancel1()
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
	s.evictLocked("Bx")
	if len(s.cache) != cacheMax-1 {
		t.Error("更新已有条目不该挤别人")
	}
}
