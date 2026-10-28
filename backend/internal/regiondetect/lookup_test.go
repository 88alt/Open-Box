package regiondetect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPublic(t *testing.T) {
	for s, want := range map[string]bool{
		"8.8.8.8": true, "203.0.113.9": true, "2606:4700::1111": true, "::ffff:8.8.8.8": true,
		"10.0.0.1": false, "192.168.1.1": false, "172.16.0.1": false, "127.0.0.1": false, "169.254.1.1": false,
		"100.64.0.1": false, "198.18.0.5": false, "0.0.0.0": false, "224.0.0.1": false, "::1": false, "fe80::1": false, "fd00::1": false,
	} {
		if got := Public(netip.MustParseAddr(s)); got != want {
			t.Errorf("%s: got %v want %v", s, got, want)
		}
	}
	if Public(netip.Addr{}) {
		t.Error("零值地址不是公网地址")
	}
}

func TestLookupURLsShape(t *testing.T) {
	got := LookupURLs(netip.MustParseAddr("2606:4700::1111"))
	want := []string{"https://api.ip.sb/geoip/2606:4700::1111", "https://ipwho.is/2606:4700::1111", "https://api.ipapi.is/?q=2606%3A4700%3A%3A1111"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %s", i, got[i])
		}
	}
}

func TestLookupCountryFallbackAndSameIP(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.9")
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/fail":
			w.WriteHeader(500)
		case "/other": // 答的是别的 IP:不能采信
			_, _ = w.Write([]byte(`{"ip":"198.51.100.1","country_code":"CN"}`))
		default:
			_, _ = w.Write([]byte(`{"ip":"203.0.113.9","country_code":"jp"}`))
		}
	}))
	defer srv.Close()
	d := &Detector{Client: http.DefaultClient, LookupURLs: func(netip.Addr) []string {
		return []string{srv.URL + "/fail", srv.URL + "/other", srv.URL + "/ok", srv.URL + "/never"}
	}}
	cc, err := d.LookupCountry(context.Background(), ip)
	if err != nil || cc != "JP" || len(asked) != 3 {
		t.Fatalf("cc=%q err=%v asked=%v", cc, err, asked)
	}
	// 全部失败:错误里逐家列出原因,不带完整 URL
	d.LookupURLs = func(netip.Addr) []string { return []string{srv.URL + "/fail", srv.URL + "/other"} }
	_, err = d.LookupCountry(context.Background(), ip)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "答的不是这个 IP") || strings.Contains(err.Error(), "http://") {
		t.Errorf("错误: %v", err)
	}
}

func TestLookupCountryNeverAsksAboutPrivate(t *testing.T) {
	hit := false
	d := &Detector{Client: http.DefaultClient, LookupURLs: func(netip.Addr) []string { hit = true; return nil }}
	for _, s := range []string{"192.168.1.1", "10.1.2.3", "127.0.0.1", "198.18.0.2"} {
		if _, err := d.LookupCountry(context.Background(), netip.MustParseAddr(s)); err == nil {
			t.Errorf("%s 不该去查", s)
		}
	}
	if hit {
		t.Error("非公网地址不能发请求")
	}
}

func TestLookupCountryCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &Detector{Client: http.DefaultClient, LookupURLs: func(netip.Addr) []string { return []string{"http://127.0.0.1:1/x"} }}
	if _, err := d.LookupCountry(ctx, netip.MustParseAddr("8.8.8.8")); err == nil || !strings.Contains(err.Error(), "已取消") {
		t.Errorf("取消应报已取消: %v", err)
	}
}
