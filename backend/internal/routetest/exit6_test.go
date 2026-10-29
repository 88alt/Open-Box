package routetest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// v6DNS 是假的内核 DNS 入站:A 答 93.184.216.34,AAAA 答 v6(为空则 AAAA 答「没有记录」)。
func v6DNS(t *testing.T, v6 ...string) *fakeDNS {
	t.Helper()
	return newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		if qtypeOf(req) == qtypeAAAA {
			var rrs []rr
			for _, a := range v6 {
				rrs = append(rrs, a6(a, 60))
			}
			return reply(req, 0, false, rrs...)
		}
		return reply(req, 0, false, a4("93.184.216.34", 60))
	})
}

func ipv6On(context.Context) bool { return true }

func TestExit6ProbesFirstV6WithDomainHostAndCert(t *testing.T) {
	var mu sync.Mutex
	var hosts []string
	port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		mu.Unlock()
		w.WriteHeader(204)
	})
	p := newFakeProxy(t, 200, up)
	f := v6DNS(t, "2001:db8::1", "2001:db8::2")
	s := &Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, DNSAddr: f.addr, IPv6: ipv6On, Timeout: 3 * time.Second}

	res, err := s.Run(context.Background(), Input{Target: "example.com", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exit.OK {
		t.Fatalf("前提:主探测应成功: %+v", res.Exit)
	}
	e6 := res.Exit6
	if e6 == nil || e6.ConnectTo != "2001:db8::1" || !e6.OK || e6.Status == nil || *e6.Status != 204 || e6.MS == nil || e6.Error != "" {
		t.Fatalf("exit6 应按第一个 v6 地址探测成功: %+v", e6)
	}
	// 隧道连的是 v6 地址;主探测连的仍是域名
	want6 := "[2001:db8::1]:" + strconv.Itoa(port)
	var saw6, sawDomain bool
	p.mu.Lock()
	for _, c := range p.connects {
		saw6 = saw6 || c == want6
		sawDomain = sawDomain || c == "example.com:"+strconv.Itoa(port)
	}
	p.mu.Unlock()
	if !saw6 || !sawDomain {
		t.Fatalf("CONNECT 目标应有 %s 和域名那条: %v", want6, p.connects)
	}
	// 请求的 Host 仍是域名;而且证书是按域名校验通过的(服务器证书里没有 2001:db8::1)
	mu.Lock()
	defer mu.Unlock()
	if len(hosts) != 2 {
		t.Fatalf("应有两次请求: %v", hosts)
	}
	for _, h := range hosts {
		if !strings.HasPrefix(h, "example.com") {
			t.Errorf("Host 应是域名: %q", h)
		}
	}
}

func TestExit6TLSAndHEAD(t *testing.T) {
	for _, method := range []string{"TLS", "HEAD"} {
		port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {})
		p := newFakeProxy(t, 200, up)
		s := &Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, DNSAddr: v6DNS(t, "2001:db8::9").addr, IPv6: ipv6On, Timeout: 3 * time.Second}
		res, err := s.Run(context.Background(), Input{Target: "example.com", Port: port, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		e6 := res.Exit6
		if e6 == nil || !e6.OK || e6.ConnectTo != "2001:db8::9" || e6.MS == nil {
			t.Fatalf("%s: %+v", method, e6)
		}
		if method == "TLS" && e6.Status != nil {
			t.Errorf("TLS 没有状态码: %+v", e6)
		}
		if method == "HEAD" && (e6.Status == nil || *e6.Status != 200) {
			t.Errorf("HEAD 应有状态码: %+v", e6)
		}
	}
}

func TestExit6NotApplicable(t *testing.T) {
	port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {})
	p := newFakeProxy(t, 200, up)
	run := func(name string, s *Service, in Input) {
		t.Helper()
		in.Port = port
		res, err := s.Run(context.Background(), in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Exit6 != nil {
			t.Errorf("%s: 不该有 exit6: %+v", name, res.Exit6)
		}
	}
	base := func(f *fakeDNS, ipv6 func(context.Context) bool) *Service {
		return &Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, DNSAddr: f.addr, IPv6: ipv6, Timeout: 3 * time.Second}
	}
	run("档案没开 IPv6", base(v6DNS(t, "2001:db8::1"), nil), Input{Target: "example.com"})
	run("没有 AAAA 记录", base(v6DNS(t), ipv6On), Input{Target: "example.com"})
	run("第一个就是 fake-ip", base(v6DNS(t, "fc00::1", "2001:db8::1"), ipv6On), Input{Target: "example.com"})
	run("IP 目标", base(v6DNS(t, "2001:db8::1"), ipv6On), Input{Target: "127.0.0.1"})
	run("TCP 方式", base(v6DNS(t, "2001:db8::1"), ipv6On), Input{Target: "example.com", Method: "TCP"})
	servfail := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		if qtypeOf(req) == qtypeAAAA {
			return reply(req, 2, false)
		}
		return reply(req, 0, false, a4("93.184.216.34", 60))
	})
	run("AAAA 查询失败", base(servfail, ipv6On), Input{Target: "example.com"})
}

func TestExit6IsIndependentOfMainProbe(t *testing.T) {
	// 面板入站拒绝建隧道:主探测和 exit6 都失败,但 exit6 仍如实给出(带原因),主探测的失败不吞掉它
	p := newFakeProxy(t, 403, "")
	s := &Service{ProxyAddr: p.addr(), DNSAddr: v6DNS(t, "2001:db8::1").addr, IPv6: ipv6On, Timeout: 2 * time.Second}
	res, err := s.Run(context.Background(), Input{Target: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit.OK || res.Exit.Error == "" {
		t.Fatalf("前提:主探测应失败: %+v", res.Exit)
	}
	e6 := res.Exit6
	if e6 == nil || e6.OK || e6.Error == "" || e6.ConnectTo != "2001:db8::1" || e6.MS != nil {
		t.Fatalf("exit6 应带着失败原因: %+v", e6)
	}
	if strings.Contains(e6.Error, "2001:db8") {
		t.Errorf("错误里不该带出地址细节以外的内部信息: %q", e6.Error)
	}
}

func TestExit6JSONShape(t *testing.T) {
	port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	p := newFakeProxy(t, 200, up)
	s := &Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, DNSAddr: v6DNS(t, "2001:db8::1").addr, IPv6: ipv6On, Timeout: 3 * time.Second}
	res, _ := s.Run(context.Background(), Input{Target: "example.com", Port: port})
	b, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	e6, ok := m["exit6"].(map[string]any)
	if !ok || e6["connectTo"] != "2001:db8::1" || e6["ok"] != true || e6["status"] != float64(200) {
		t.Fatalf("exit6 的字段:%s", b)
	}
	if _, has := e6["error"]; has {
		t.Errorf("成功时不带 error: %v", e6)
	}
	// 不适用时整个字段不出现,不是 null
	res2, _ := (&Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, Timeout: time.Second}).Run(context.Background(), Input{Target: "127.0.0.1", Port: port})
	b2, _ := json.Marshal(res2)
	if strings.Contains(string(b2), "exit6") {
		t.Errorf("不适用时不该有 exit6 键:%s", b2)
	}
}

func TestExit6CancelDiscardsResult(t *testing.T) {
	p := newFakeProxy(t, 403, "")
	ctx, cancel := context.WithCancel(context.Background())
	f := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		if qtypeOf(req) == qtypeAAAA {
			return reply(req, 0, false, a6("2001:db8::1", 60))
		}
		return reply(req, 0, false, a4("93.184.216.34", 60))
	})
	s := &Service{ProxyAddr: p.addr(), DNSAddr: f.addr, IPv6: func(context.Context) bool { cancel(); return true }, Timeout: time.Second}
	if res, err := s.Run(ctx, Input{Target: "example.com"}); err == nil {
		t.Fatalf("取消后应返回错误,而不是结果: %+v", res)
	}
}

func TestFirstV6(t *testing.T) {
	r := func(a ...string) *Resolve { return &Resolve{Answers6: a} }
	cases := []struct {
		name string
		in   *Resolve
		want string
	}{
		{"没有解析", nil, ""},
		{"空", r(), ""},
		{"取第一个", r("2001:db8::1", "2001:db8::2"), "2001:db8::1"},
		{"跳过写不对的和 v4", r("bad", "1.2.3.4", "2001:db8::3"), "2001:db8::3"},
		{"跳过 v4 映射", r("::ffff:1.2.3.4", "2001:db8::4"), "2001:db8::4"},
		{"第一个有效的是 fake-ip 就整组不测", r("fc00::5", "2001:db8::6"), ""},
		{"fake-ip 在后面不影响", r("2001:db8::7", "fc00::8"), "2001:db8::7"},
	}
	for _, c := range cases {
		got, ok := firstV6(c.in)
		if c.want == "" {
			if ok {
				t.Errorf("%s: 不该有结果,得到 %v", c.name, got)
			}
			continue
		}
		if !ok || got != netip.MustParseAddr(c.want) {
			t.Errorf("%s: 期望 %s,得到 %v %v", c.name, c.want, got, ok)
		}
	}
}

func TestWithConnectTo(t *testing.T) {
	cases := [][3]string{
		{"example.com:443", "", "example.com:443"},
		{"example.com:443", "2001:db8::1", "[2001:db8::1]:443"},
		{"example.com:8443", "203.0.113.7", "203.0.113.7:8443"},
		{"[2001:db8::9]:80", "2001:db8::1", "[2001:db8::1]:80"},
		{"没有端口", "2001:db8::1", "没有端口"},
	}
	for _, c := range cases {
		if got := withConnectTo(c[0], c[1]); got != c[2] {
			t.Errorf("withConnectTo(%q,%q)=%q 期望 %q", c[0], c[1], got, c[2])
		}
	}
}
