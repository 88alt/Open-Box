package nodesharing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func page(ip, usecount, risk, types string) string {
	var b strings.Builder
	b.WriteString("<html><head><title>" + ip + " - ping0</title></head><body>")
	if usecount != "" {
		b.WriteString(`<div class="x usecountbar big" data-a="1" usecount="` + usecount + `"></div>`)
	}
	if risk != "" {
		b.WriteString(risk)
	}
	if types != "" {
		b.WriteString(`<div class="line line-iptype"><span>IP 类型</span></div>` + types)
	}
	b.WriteString("</body></html>")
	return b.String()
}

const riskBlock = `<div class="riskcurrent bar" title="15-25 clean"><span class="value"> 22.5 % </span></div>`
const typeBlock = `<div class="content"><span class="label">机房&nbsp;IP</span><span class="label">  </span><span class="label">  原生   IP </span></div>`

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestParseAll(t *testing.T) {
	rep := parse(page("203.0.113.9", "10 - 100 (大约)", riskBlock, typeBlock), mustAddr("203.0.113.9"))
	if rep == nil || rep.Range == nil || *rep.Range != "10-100" {
		t.Fatalf("range: %+v", rep)
	}
	if rep.Risk == nil || rep.Risk.Percent != 22.5 || rep.Risk.Tier == nil || *rep.Risk.Tier != "clean" {
		t.Errorf("risk: %+v", rep.Risk)
	}
	if len(rep.IPTypes) != 2 || rep.IPTypes[0] != "机房 IP" || rep.IPTypes[1] != "原生 IP" {
		t.Errorf("ipTypes: %q", rep.IPTypes)
	}
}

func TestParseRanges(t *testing.T) {
	cases := map[string]string{
		"1-10": "1-10", "10000+": "10000+", " 1000 - 10000（约）": "1000-10000", "100 -1000": "100-1000", "5 +": "",
		"1-10x": "", "2-3": "", "": "", "abc": "", "10000+ (a)": "10000+",
	}
	for in, want := range cases {
		got := parseRange(in)
		switch {
		case want == "" && got != nil:
			t.Errorf("%q 应为空,得到 %q", in, *got)
		case want != "" && (got == nil || *got != want):
			t.Errorf("%q 应为 %q,得到 %v", in, want, got)
		}
	}
}

func TestParseRiskTierAndBounds(t *testing.T) {
	ip := mustAddr("203.0.113.9")
	for tier, title := range map[string]string{"veryClean": "0-15", "neutral": "25-40", "slight": "40-50", "elevated": "50-70", "extreme": "70-100"} {
		rep := parse(page(ip.String(), "", `<div class='riskcurrent' title='`+title+` x'><span class="value">30%</span></div>`, ""), ip)
		if rep.Risk == nil || rep.Risk.Tier == nil || *rep.Risk.Tier != tier {
			t.Errorf("%s: %+v", title, rep.Risk)
		}
	}
	// 档位认不出:tier 为 null,百分比还在
	rep := parse(page(ip.String(), "", `<div class="riskcurrent" title="9-9"><span class="value">30%</span></div>`, ""), ip)
	if rep.Risk == nil || rep.Risk.Tier != nil || rep.Risk.Percent != 30 {
		t.Errorf("未知档位: %+v", rep.Risk)
	}
	// 超过 100 不算
	rep = parse(page(ip.String(), "", `<div class="riskcurrent" title="70-100"><span class="value">101%</span></div>`, ""), ip)
	if rep.Risk != nil {
		t.Errorf("101%% 不该有风控值: %+v", rep.Risk)
	}
}

func TestParseTitleMustMatch(t *testing.T) {
	if parse(page("203.0.113.10", "1-10", "", ""), mustAddr("203.0.113.9")) != nil {
		t.Error("标题里的 IP 不是请求的 IP 应返回 nil")
	}
	if parse("<html>没有标题</html>", mustAddr("203.0.113.9")) != nil {
		t.Error("没有标题应返回 nil")
	}
	// IPv6:写法不同但是同一个地址
	if parse(page("2001:DB8:0:0:0:0:0:1", "1-10", "", ""), mustAddr("2001:db8::1")) == nil {
		t.Error("IPv6 不同写法应认成同一个地址")
	}
	// 页面里全没有:仍是空报告(不是 nil),三项是 null / 空数组
	rep := parse(page("203.0.113.9", "", "", ""), mustAddr("203.0.113.9"))
	if rep == nil || rep.Range != nil || rep.Risk != nil || rep.IPTypes == nil || len(rep.IPTypes) != 0 {
		t.Errorf("空页面: %+v", rep)
	}
}

func TestParseIPTypeNeedsContentRightAfter(t *testing.T) {
	ip := mustAddr("203.0.113.9")
	html := page(ip.String(), "", "", "") + `<div class="line-iptype"></div> <p>隔了别的</p><div class="content"><span class="label">不该出现</span></div>`
	if rep := parse(html, ip); len(rep.IPTypes) != 0 {
		t.Errorf("content 不紧跟其后不该取: %q", rep.IPTypes)
	}
}

func publicOnly(a netip.Addr) bool { return !a.IsPrivate() && !a.IsLoopback() }

func serve(t *testing.T, h http.HandlerFunc) (*Service, *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Service{IsPublic: publicOnly, BaseURL: srv.URL + "/ip/"}, &n
}

func htmlOK(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}
}

func TestQueryHitCachedAndRefresh(t *testing.T) {
	s, n := serve(t, htmlOK(page("203.0.113.9", "1-10", riskBlock, typeBlock)))
	for i := 0; i < 3; i++ {
		rep, err := s.Query(context.Background(), "203.0.113.9", false)
		if err != nil || rep.Range == nil || *rep.Range != "1-10" {
			t.Fatalf("第 %d 次: %+v %v", i, rep, err)
		}
	}
	if *n != 1 {
		t.Errorf("有数据的结果应缓存:请求 %d 次", *n)
	}
	if _, err := s.Query(context.Background(), "203.0.113.9", true); err != nil || *n != 2 {
		t.Errorf("refresh 应重查:请求 %d 次 err=%v", *n, err)
	}
}

func TestQueryRequestShape(t *testing.T) {
	var path, ua string
	s, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		path, ua = r.URL.EscapedPath(), r.Header.Get("User-Agent")
		htmlOK(page("2001:db8::1", "1-10", "", ""))(w, r)
	})
	if _, err := s.Query(context.Background(), "2001:db8::1", false); err != nil {
		t.Fatal(err)
	}
	if path != "/ip/2001%3Adb8%3A%3A1" || ua != "Mozilla/5.0" {
		t.Errorf("path=%q ua=%q", path, ua)
	}
}

func TestQueryMissCachedShortAndExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s, n := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	s.Now = func() time.Time { return now }
	rep, err := s.Query(context.Background(), "203.0.113.9", false)
	if err != nil || rep.Range != nil || rep.Risk != nil || rep.IPTypes == nil || len(rep.IPTypes) != 0 || rep.Error != "" {
		t.Fatalf("取不到应是空报告: %+v %v", rep, err)
	}
	now = now.Add(4 * time.Minute)
	_, _ = s.Query(context.Background(), "203.0.113.9", false)
	if *n != 1 {
		t.Errorf("5 分钟内应用缓存:请求 %d 次", *n)
	}
	now = now.Add(2 * time.Minute)
	_, _ = s.Query(context.Background(), "203.0.113.9", false)
	if *n != 2 {
		t.Errorf("过了 5 分钟应重查:请求 %d 次", *n)
	}
}

func TestQueryHitExpiresAfterADay(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s, n := serve(t, htmlOK(page("203.0.113.9", "10-100", "", "")))
	s.Now = func() time.Time { return now }
	_, _ = s.Query(context.Background(), "203.0.113.9", false)
	now = now.Add(23 * time.Hour)
	_, _ = s.Query(context.Background(), "203.0.113.9", false)
	if *n != 1 {
		t.Errorf("23 小时内应用缓存:%d", *n)
	}
	now = now.Add(2 * time.Hour)
	_, _ = s.Query(context.Background(), "203.0.113.9", false)
	if *n != 2 {
		t.Errorf("超过一天应重查:%d", *n)
	}
}

func TestQueryChallenge(t *testing.T) {
	s, _ := serve(t, htmlOK(`<html><title>请稍候</title><div class="box cf-turnstile" data-sitekey="x"></div></html>`))
	rep, err := s.Query(context.Background(), "203.0.113.9", false)
	if err != nil || rep.Error != "challenge" || rep.Range != nil || rep.Risk != nil || rep.IPTypes == nil {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestQueryRejects(t *testing.T) {
	s, _ := serve(t, htmlOK(""))
	for _, bad := range []string{"", "abc", "1.2.3", "fe80::1%eth0", "203.0.113.9/24"} {
		if _, err := s.Query(context.Background(), bad, false); !errors.Is(err, ErrInvalidIP) {
			t.Errorf("%q 应是 ErrInvalidIP,得到 %v", bad, err)
		}
	}
}

func TestQueryNonPublicNotFetched(t *testing.T) {
	s, n := serve(t, htmlOK(page("192.168.1.1", "1-10", "", "")))
	rep, err := s.Query(context.Background(), "192.168.1.1", false)
	if err != nil || rep.Range != nil || *n != 0 {
		t.Errorf("内网地址不该联网:%+v 请求 %d 次 err=%v", rep, *n, err)
	}
	s.IsPublic = nil
	if _, err := s.Query(context.Background(), "203.0.113.9", false); err != nil || *n != 0 {
		t.Errorf("没设 IsPublic 不该联网:请求 %d 次 err=%v", *n, err)
	}
}

func TestQueryGuards(t *testing.T) {
	big := page("203.0.113.9", "1-10", "", "") + strings.Repeat("a", maxBody)
	cases := map[string]http.HandlerFunc{
		"非 html": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(page("203.0.113.9", "1-10", "", "")))
		},
		"重定向": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/other", http.StatusFound) },
		"太大":  htmlOK(big),
		"404": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(page("203.0.113.9", "1-10", "", "")))
		},
	}
	for name, h := range cases {
		s, _ := serve(t, h)
		rep, err := s.Query(context.Background(), "203.0.113.9", false)
		if err != nil || rep.Range != nil || rep.Error != "" {
			t.Errorf("%s 应当没查到: %+v %v", name, rep, err)
		}
	}
}

func TestQueryCoalescesAndHonoursContext(t *testing.T) {
	release := make(chan struct{})
	s, n := serve(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		htmlOK(page("203.0.113.9", "1-10", "", ""))(w, r)
	})
	done := make(chan *Report, 2)
	for i := 0; i < 2; i++ {
		go func() {
			rep, _ := s.Query(context.Background(), "203.0.113.9", true)
			done <- rep
		}()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	time.Sleep(50 * time.Millisecond)
	if _, err := s.Query(ctx, "203.0.113.9", true); !errors.Is(err, context.Canceled) {
		t.Errorf("取消的调用者应拿到 context.Canceled:%v", err)
	}
	close(release)
	for i := 0; i < 2; i++ {
		if rep := <-done; rep == nil || rep.Range == nil {
			t.Errorf("等着的调用者应拿到结果: %+v", rep)
		}
	}
	if *n != 1 {
		t.Errorf("同一个 IP 同时查应合并成一次:请求 %d 次", *n)
	}
}

func TestCacheBounded(t *testing.T) {
	s, _ := serve(t, htmlOK(""))
	s.IsPublic = func(netip.Addr) bool { return false }
	for i := 0; i < maxKeep+20; i++ {
		ip := netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 1}).String()
		_, _ = s.Query(context.Background(), ip, false)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) > maxKeep {
		t.Errorf("缓存应有上限:%d", len(s.cache))
	}
}
