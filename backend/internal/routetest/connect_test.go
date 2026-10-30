package routetest

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// connectDNS:A 记录按 answers 依次回,没有 AAAA。
func connectDNS(t *testing.T, answers ...string) *fakeDNS {
	t.Helper()
	return newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		if qtypeOf(req) == qtypeAAAA {
			return reply(req, 0, false)
		}
		var rrs []rr
		for _, a := range answers {
			rrs = append(rrs, a4(a, 60))
		}
		return reply(req, 0, false, rrs...)
	})
}

func connectsOf(p *fakeProxy) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.connects...)
}

func TestMainProbeConnectsToFirstRealA(t *testing.T) {
	var mu sync.Mutex
	var hosts []string
	port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		mu.Unlock()
		w.WriteHeader(204)
	})
	p := newFakeProxy(t, 200, up)
	f := connectDNS(t, "93.184.216.34", "93.184.216.35")
	s := &Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, DNSAddr: f.addr, Timeout: 3 * time.Second}
	for _, method := range []string{MethodGET, MethodTLS} {
		before := len(connectsOf(p))
		res, err := s.Run(context.Background(), Input{Target: "example.com", Port: port, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Exit.OK || res.Exit.ConnectTo != "93.184.216.34" {
			t.Fatalf("%s: 应按第一个真实 A 答案去连: %+v", method, res.Exit)
		}
		got := connectsOf(p)[before:]
		if len(got) != 1 || got[0] != "93.184.216.34:"+strconv.Itoa(port) {
			t.Errorf("%s: 隧道应连 IP 而不是域名: %v", method, got)
		}
	}
	// 请求的 Host 仍是域名,证书是按域名校验通过的(测试服务器的证书里没有这个 IP)
	mu.Lock()
	defer mu.Unlock()
	if len(hosts) != 1 || !strings.HasPrefix(hosts[0], "example.com") {
		t.Errorf("Host 应是域名: %v", hosts)
	}
}

func TestMainProbeKeepsDomainWhenNoUsableAnswer(t *testing.T) {
	cases := map[string][]string{
		"第一个是占位地址": {"198.19.0.7", "93.184.216.34"},
		"没有 A 答案":  nil,
	}
	for name, answers := range cases {
		port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
		p := newFakeProxy(t, 200, up)
		f := connectDNS(t, answers...)
		s := &Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, DNSAddr: f.addr, Timeout: 3 * time.Second}
		res, err := s.Run(context.Background(), Input{Target: "example.com", Port: port})
		if err != nil {
			t.Fatal(err)
		}
		if res.Exit.ConnectTo != "" {
			t.Errorf("%s: 不该有 connectTo: %+v", name, res.Exit)
		}
		if got := connectsOf(p); len(got) != 1 || got[0] != "example.com:"+strconv.Itoa(port) {
			t.Errorf("%s: 应让内核按域名去连: %v", name, got)
		}
	}
}

func TestMainProbeIPTargetHasNoConnectTo(t *testing.T) {
	port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	p := newFakeProxy(t, 200, up)
	s := &Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, Timeout: 3 * time.Second}
	res, err := s.Run(context.Background(), Input{Target: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Exit)
	if res.Exit.ConnectTo != "" || strings.Contains(string(b), "connectTo") {
		t.Errorf("目标本身是 IP 时没有 connectTo: %s", b)
	}
}

func TestFirstV4(t *testing.T) {
	cases := []struct {
		name string
		r    *Resolve
		want string
	}{
		{"nil", nil, ""},
		{"没查成", &Resolve{OK: false, Answers: []string{"93.184.216.34"}}, ""},
		{"第一个", &Resolve{OK: true, Answers: []string{"93.184.216.34", "1.1.1.1"}}, "93.184.216.34"},
		{"占位地址打头:整组不用", &Resolve{OK: true, Answers: []string{"198.19.1.1", "1.1.1.1"}}, ""},
		{"跳过解析不了的", &Resolve{OK: true, Answers: []string{"nope", "8.8.8.8"}}, "8.8.8.8"},
		{"跳过 v6", &Resolve{OK: true, Answers: []string{"2001:db8::1", "8.8.4.4"}}, "8.8.4.4"},
		{"没有答案", &Resolve{OK: true}, ""},
	}
	for _, c := range cases {
		ad, ok := firstV4(c.r)
		if (c.want == "") == ok || (ok && ad.String() != c.want) {
			t.Errorf("%s: %v %v", c.name, ad, ok)
		}
	}
}
