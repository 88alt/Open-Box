package regiondetect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func serve(t *testing.T, code int, body string) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func TestRegionOf(t *testing.T) {
	for cc, want := range map[string]string{"CN": "cn", "cn": "cn", " CN ": "cn", "HK": "intl", "MO": "intl", "TW": "intl", "US": "intl", "": "intl"} {
		if got := RegionOf(cc); got != want {
			t.Errorf("%q: got %s want %s", cc, got, want)
		}
	}
}

func TestParseThreeShapes(t *testing.T) {
	cases := []struct{ name, body, ip, cc string }{
		{"api.ip.sb", `{"ip":"1.2.3.4","country_code":"CN","x":1}`, "1.2.3.4", "CN"},
		{"ipwho.is", `{"ip":"2001:db8::1","success":true,"country_code":"us"}`, "2001:db8::1", "US"},
		{"api.ipapi.is", `{"ip":"5.6.7.8","location":{"country_code":"JP"}}`, "5.6.7.8", "JP"},
	}
	for _, c := range cases {
		ip, cc, err := parse([]byte(c.body))
		if err != nil || ip != c.ip || cc != c.cc {
			t.Errorf("%s: %s %s %v", c.name, ip, cc, err)
		}
	}
	for _, bad := range []string{`nope`, `{"ip":"x","country_code":"CN"}`, `{"ip":"1.2.3.4"}`, `{"ip":"1.2.3.4","country_code":"CHN"}`, `{"success":false,"message":"limit"}`, `{"ip":"1.2.3.4","success":false,"country_code":"CN"}`} {
		if _, _, err := parse([]byte(bad)); err == nil {
			t.Errorf("应失败: %s", bad)
		}
	}
}

func TestDetectFallsBackInOrder(t *testing.T) {
	bad := serve(t, 500, `x`)
	junk := serve(t, 200, `{"ip":"nope"}`)
	good := serve(t, 200, `{"ip":"223.5.5.5","country_code":"CN"}`)
	never := serve(t, 200, `{"ip":"8.8.8.8","country_code":"US"}`)
	d := &Detector{URLs: []string{bad, junk, good, never}, Client: http.DefaultClient}
	r := d.Detect(context.Background())
	if r.Region == nil || *r.Region != "cn" || r.IP != "223.5.5.5" || r.Error != "" {
		t.Fatalf("%+v", r)
	}
}

func TestDetectAllFail(t *testing.T) {
	a := serve(t, 503, ``)
	b := serve(t, 200, `<html>`)
	d := &Detector{URLs: []string{a, b}, Client: http.DefaultClient}
	r := d.Detect(context.Background())
	if r.Region != nil || r.IP != "" || !strings.Contains(r.Error, "HTTP 503") || !strings.Contains(r.Error, "不是 JSON") {
		t.Fatalf("%+v", r)
	}
	// 错误里不带完整 URL(可能有 token 之类),只有主机
	if strings.Contains(r.Error, "http://") {
		t.Fatalf("不该带完整地址: %s", r.Error)
	}
}

func TestDetectIntl(t *testing.T) {
	u := serve(t, 200, `{"ip":"8.8.4.4","country_code":"HK"}`)
	r := (&Detector{URLs: []string{u}, Client: http.DefaultClient}).Detect(context.Background())
	if r.Region == nil || *r.Region != "intl" {
		t.Fatalf("%+v", r)
	}
}

func TestDetectCancelled(t *testing.T) {
	u := serve(t, 200, `{"ip":"1.1.1.1","country_code":"US"}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := (&Detector{URLs: []string{u}, Client: http.DefaultClient}).Detect(ctx)
	if r.Region != nil || r.Error == "" {
		t.Fatalf("%+v", r)
	}
}

func TestPendingWhileRunning(t *testing.T) {
	gate := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-gate
		_, _ = w.Write([]byte(`{"ip":"1.1.1.1","country_code":"US"}`))
	}))
	defer s.Close()
	d := &Detector{URLs: []string{s.URL}, Client: http.DefaultClient}
	if d.Pending() {
		t.Fatal("一开始不该是 pending")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); d.Detect(context.Background()) }()
	deadline := time.Now().Add(2 * time.Second)
	for !d.Pending() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !d.Pending() {
		t.Fatal("进行中应为 pending")
	}
	close(gate)
	wg.Wait()
	if d.Pending() {
		t.Fatal("做完应不再 pending")
	}
}

func TestBodyLimit(t *testing.T) {
	// 超过上限的正文被截断后不是合法 JSON,按失败处理,不会把内存吃光
	u := serve(t, 200, `{"ip":"1.1.1.1","country_code":"US","pad":"`+strings.Repeat("a", bodyMax)+`"}`)
	r := (&Detector{URLs: []string{u}, Client: http.DefaultClient}).Detect(context.Background())
	if r.Region != nil {
		t.Fatalf("%+v", r)
	}
}

func TestDetectExposesCountryCode(t *testing.T) {
	d := &Detector{URLs: []string{serve(t, 200, `{"ip":"203.0.113.7","country_code":"jp"}`)}}
	r := d.Detect(context.Background())
	if r.Region == nil || *r.Region != "intl" || r.Country != "JP" || r.IP != "203.0.113.7" {
		t.Fatalf("%+v", r)
	}
	// Country 只给后端内部用,不进 JSON(dns/region-detect 的响应形状不变)
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "JP") || strings.Contains(string(b), "ountry") {
		t.Fatalf("Country 不该进 JSON: %s", b)
	}
}
