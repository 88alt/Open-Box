package routetest

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// fakeProxy 是假的面板入站:收 CONNECT,按 status 答复;200 时把连接接到 upstream(真实的本地服务器)。
type fakeProxy struct {
	ln       net.Listener
	status   int
	upstream string
	mu       sync.Mutex
	ports    []string // 每个连进来的客户端本地端口
	connects []string // 每次 CONNECT 的目标
}

func newFakeProxy(t *testing.T, status int, upstream string) *fakeProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProxy{ln: ln, status: status, upstream: upstream}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c)
		}
	}()
	return p
}

func (p *fakeProxy) addr() string { return p.ln.Addr().String() }

func (p *fakeProxy) lastPort() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.ports) == 0 {
		return ""
	}
	return p.ports[len(p.ports)-1]
}

func (p *fakeProxy) serve(c net.Conn) {
	defer c.Close()
	_, port, _ := net.SplitHostPort(c.RemoteAddr().String())
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		return
	}
	p.mu.Lock()
	p.ports = append(p.ports, port)
	p.connects = append(p.connects, req.Host)
	p.mu.Unlock()
	if p.status != http.StatusOK {
		_, _ = io.WriteString(c, "HTTP/1.1 "+strconv.Itoa(p.status)+" X\r\nContent-Length: 0\r\n\r\n")
		return
	}
	up, err := net.Dial("tcp", p.upstream)
	if err != nil {
		return
	}
	defer up.Close()
	_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

// tlsServer 起一个本地 TLS 服务器;返回它的端口和一个信任它证书的 tls.Config。
func tlsServer(t *testing.T, h http.HandlerFunc) (port int, cfg *tls.Config, addr string) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	_, ps, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ = strconv.Atoi(ps)
	return port, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, srv.Listener.Addr().String()
}

func snapshotFor(p *fakeProxy, c *singbox.KernelConn, direct ...string) func(context.Context) (singbox.KernelSnapshot, error) {
	return func(context.Context) (singbox.KernelSnapshot, error) {
		cc := *c
		cc.SourcePort = p.lastPort()
		cc.Source = "127.0.0.1"
		// 再放一条端口不同的,确认不会认错
		other := singbox.KernelConn{ID: "x", Chains: []string{"node-z", "other"}, Source: "127.0.0.1", SourcePort: "1"}
		return singbox.KernelSnapshot{Conns: []singbox.KernelConn{other, cc}, DirectTags: direct}, nil
	}
}

func TestParse(t *testing.T) {
	ok := []struct {
		in   Input
		want Input
	}{
		{Input{Target: " example.com "}, Input{Target: "example.com", Method: "GET"}},
		{Input{Target: "1.1.1.1", Port: 443, Method: "tls"}, Input{Target: "1.1.1.1", Port: 443, Method: "TLS"}},
		{Input{Target: "2001:db8::1", Method: "head"}, Input{Target: "2001:db8::1", Method: "HEAD"}},
		{Input{Target: "a_b-c.d", Port: 65535, Method: "TCP"}, Input{Target: "a_b-c.d", Port: 65535, Method: "TCP"}},
	}
	for _, c := range ok {
		got, err := Parse(c.in)
		if err != nil || got != c.want {
			t.Errorf("Parse(%+v) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	bad := []Input{
		{Target: ""}, {Target: "   "}, {Target: "-evil.com"}, {Target: "a b.com"}, {Target: "a/b"},
		{Target: "a.com?x=1"}, {Target: "例子.com"}, {Target: strings.Repeat("a", 254)},
		{Target: "a.com", Port: -1}, {Target: "a.com", Port: 65536}, {Target: "a.com", Method: "POST"},
	}
	for _, in := range bad {
		_, err := Parse(in)
		var ie *InputError
		if !errors.As(err, &ie) {
			t.Errorf("Parse(%+v) 应该是 InputError,得到 %v", in, err)
		}
	}
}

func TestProbeURL(t *testing.T) {
	for _, c := range []struct {
		target string
		port   int
		want   string
	}{
		{"example.com", 0, "https://example.com/"},
		{"example.com", 443, "https://example.com/"},
		{"example.com", 80, "http://example.com/"},
		{"example.com", 8443, "https://example.com:8443/"},
		{"1.2.3.4", 8080, "https://1.2.3.4:8080/"},
		{"2001:db8::1", 0, "https://[2001:db8::1]/"},
		{"2001:db8::1", 8443, "https://[2001:db8::1]:8443/"},
	} {
		if got := ProbeURL(c.target, c.port); got != c.want {
			t.Errorf("ProbeURL(%q,%d) = %q, want %q", c.target, c.port, got, c.want)
		}
	}
}

func TestRunGETFindsRoute(t *testing.T) {
	port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	p := newFakeProxy(t, 200, up)
	flushed := 0
	s := &Service{
		ProxyAddr: p.addr(), TLSConfig: tlsCfg, Timeout: 3 * time.Second,
		FlushDNS: func(context.Context) error { flushed++; return nil },
		Snapshot: snapshotFor(p, &singbox.KernelConn{
			ID: "1", Chains: []string{"hk-01", "自动选择", "海外"}, Rule: "rule_set=geosite-geolocation-!cn", RulePayload: "海外", DestIP: "93.184.216.34",
		}, "direct"),
	}
	res, err := s.Run(context.Background(), Input{Target: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	ex := res.Exit
	if !ex.OK || ex.Status == nil || *ex.Status != 204 || ex.MS == nil || ex.Error != "" {
		t.Fatalf("探测应该成功: %+v", ex)
	}
	if ex.Method != "GET" || !strings.HasPrefix(ex.URL, "https://127.0.0.1:") {
		t.Errorf("method/url 不对: %q %q", ex.Method, ex.URL)
	}
	if got := strings.Join(ex.Chains, ","); got != "海外,自动选择,hk-01" {
		t.Errorf("线路应该是路由选中的在前、出口在后,得到 %q", got)
	}
	if ex.Rule != "rule_set=geosite-geolocation-!cn" || ex.RulePayload != "海外" || ex.DestinationIP != "93.184.216.34" {
		t.Errorf("规则 / 目标 IP 不对: %+v", ex)
	}
	if ex.ViaProxy == nil || !*ex.ViaProxy {
		t.Errorf("末尾是节点,viaProxy 应该为 true: %+v", ex.ViaProxy)
	}
	if ex.NotSeen || ex.ConnectionsError != "" {
		t.Errorf("不该有 notSeen / connectionsError: %+v", ex)
	}
	if flushed != 1 {
		t.Errorf("探测前应该清一次 DNS 缓存,清了 %d 次", flushed)
	}
	if res.Target != "127.0.0.1" || asMap(res.DNS)["skipped"] != true {
		t.Errorf("IP 目标的 dns 应该是 skipped: %+v", res.DNS)
	}
}

func TestRunViaDirectAndDNSForDomain(t *testing.T) {
	port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {})
	p := newFakeProxy(t, 200, up)
	tlsCfg.ServerName = "127.0.0.1"
	s := &Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, Timeout: 3 * time.Second,
		Snapshot: snapshotFor(p, &singbox.KernelConn{ID: "1", Chains: []string{"direct", "国内直连"}}, "direct")}
	res, err := s.Run(context.Background(), Input{Target: "127.0.0.1", Port: port, Method: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exit.OK || res.Exit.Method != "HEAD" {
		t.Fatalf("HEAD 应该成功: %+v", res.Exit)
	}
	if res.Exit.ViaProxy == nil || *res.Exit.ViaProxy {
		t.Errorf("出口是直连,viaProxy 应该为 false")
	}

	// 域名目标:dns 如实回 error(还没做),不能是 skipped
	res, _ = (&Service{ProxyAddr: newFakeProxy(t, 403, "").addr(), DNSAddr: closedAddr(t), Timeout: time.Second}).Run(context.Background(), Input{Target: "example.com"})
	if _, ok := asMap(res.DNS)["error"].(string); !ok || asMap(res.DNS)["skipped"] != nil {
		t.Errorf("域名目标的 dns 应该是 error: %+v", res.DNS)
	}
}

func TestRunTLS(t *testing.T) {
	port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {})
	p := newFakeProxy(t, 200, up)
	s := &Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, Timeout: 3 * time.Second,
		Snapshot: snapshotFor(p, &singbox.KernelConn{ID: "1", Chains: []string{"jp-02", "海外"}})}
	res, err := s.Run(context.Background(), Input{Target: "127.0.0.1", Port: port, Method: "TLS"})
	if err != nil {
		t.Fatal(err)
	}
	ex := res.Exit
	if !ex.OK || ex.Status != nil || ex.MS == nil || ex.Method != "TLS" {
		t.Fatalf("TLS 握手应该成功且没有状态码: %+v", ex)
	}
	if strings.Join(ex.Chains, ",") != "海外,jp-02" {
		t.Errorf("线路不对: %v", ex.Chains)
	}
	if got := p.connects[len(p.connects)-1]; got != "127.0.0.1:"+strconv.Itoa(port) {
		t.Errorf("CONNECT 目标不对: %q", got)
	}

	// 握手失败:对端不是 TLS(假代理接到明文 HTTP 服务器)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer plain.Close()
	p2 := newFakeProxy(t, 200, plain.Listener.Addr().String())
	_, ps, _ := net.SplitHostPort(plain.Listener.Addr().String())
	pp, _ := strconv.Atoi(ps)
	res, _ = (&Service{ProxyAddr: p2.addr(), Timeout: 2 * time.Second}).Run(context.Background(), Input{Target: "127.0.0.1", Port: pp, Method: "TLS"})
	if res.Exit.OK || res.Exit.Error == "" || res.Exit.MS != nil {
		t.Errorf("对端不是 TLS 应该失败: %+v", res.Exit)
	}
}

func TestRunTCPNotSupported(t *testing.T) {
	flushed := false
	s := &Service{ProxyAddr: "127.0.0.1:1", FlushDNS: func(context.Context) error { flushed = true; return nil }}
	res, err := s.Run(context.Background(), Input{Target: "example.com", Port: 22, Method: "TCP"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit.OK || !strings.Contains(res.Exit.Error, "TCP") || res.Exit.Method != "TCP" {
		t.Errorf("TCP 应该如实报暂不支持: %+v", res.Exit)
	}
	if flushed {
		t.Errorf("没有探测就不该清 DNS 缓存")
	}
}

func TestRunFailures(t *testing.T) {
	// 内核入口连不上
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	res, err := (&Service{ProxyAddr: dead, Timeout: time.Second}).Run(context.Background(), Input{Target: "1.1.1.1"})
	if err != nil || res.Exit.OK || !strings.Contains(res.Exit.Error, "内核") {
		t.Errorf("入口连不上应该提示内核没运行: %+v %v", res.Exit, err)
	}
	// 内核拒绝建隧道
	res, _ = (&Service{ProxyAddr: newFakeProxy(t, 403, "").addr(), DNSAddr: closedAddr(t), Timeout: time.Second}).Run(context.Background(), Input{Target: "1.1.1.1"})
	if res.Exit.OK || !strings.Contains(res.Exit.Error, "403") {
		t.Errorf("拒绝应该带状态码: %+v", res.Exit)
	}
	// 证书校验失败(没给信任的证书)
	port, _, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {})
	res, _ = (&Service{ProxyAddr: newFakeProxy(t, 200, up).addr(), Timeout: 2 * time.Second}).Run(context.Background(), Input{Target: "127.0.0.1", Port: port})
	if res.Exit.OK || res.Exit.Error != "证书校验失败" {
		t.Errorf("证书不被信任应该失败: %+v", res.Exit)
	}
	// 失败时不读连接表
	called := false
	s := &Service{ProxyAddr: dead, Timeout: time.Second, Snapshot: func(context.Context) (singbox.KernelSnapshot, error) {
		called = true
		return singbox.KernelSnapshot{}, nil
	}}
	_, _ = s.Run(context.Background(), Input{Target: "1.1.1.1"})
	if called {
		t.Errorf("探测失败时不该读连接表")
	}
}

func TestRunConnectionLookupProblems(t *testing.T) {
	port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {})
	mk := func(snap func(context.Context) (singbox.KernelSnapshot, error)) Exit {
		p := newFakeProxy(t, 200, up)
		res, err := (&Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, Timeout: 3 * time.Second, Snapshot: snap}).Run(context.Background(), Input{Target: "127.0.0.1", Port: port, Method: "TLS"})
		if err != nil {
			t.Fatal(err)
		}
		return res.Exit
	}
	// 控制接口读不到:探测仍成功,原因写进 connectionsError
	ex := mk(func(context.Context) (singbox.KernelSnapshot, error) {
		return singbox.KernelSnapshot{}, errors.New("控制接口连不上(内核没在运行?)")
	})
	if !ex.OK || ex.ConnectionsError == "" || len(ex.Chains) != 0 {
		t.Errorf("读不到连接表应该只记 connectionsError: %+v", ex)
	}
	// 表里没有自己那条:notSeen
	ex = mk(func(context.Context) (singbox.KernelSnapshot, error) {
		return singbox.KernelSnapshot{Conns: []singbox.KernelConn{{ID: "x", Chains: []string{"a"}, Source: "127.0.0.1", SourcePort: "1"}}}, nil
	})
	if !ex.OK || !ex.NotSeen || len(ex.Chains) != 0 {
		t.Errorf("找不到应该 notSeen: %+v", ex)
	}
	// 来源不是本机的同端口连接不能认(别的终端恰好用了同一个端口)
	var pp *fakeProxy
	pp = newFakeProxy(t, 200, up)
	res, _ := (&Service{ProxyAddr: pp.addr(), TLSConfig: tlsCfg, Timeout: 3 * time.Second,
		Snapshot: func(context.Context) (singbox.KernelSnapshot, error) {
			return singbox.KernelSnapshot{Conns: []singbox.KernelConn{{ID: "x", Chains: []string{"a"}, Source: "192.168.1.5", SourcePort: pp.lastPort()}}}, nil
		}}).Run(context.Background(), Input{Target: "127.0.0.1", Port: port, Method: "TLS"})
	if !res.Exit.NotSeen {
		t.Errorf("非本机来源不能认作自己的连接: %+v", res.Exit)
	}
	// 没接快照:不报错也不写 notSeen
	p := newFakeProxy(t, 200, up)
	res, _ = (&Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, Timeout: 3 * time.Second}).Run(context.Background(), Input{Target: "127.0.0.1", Port: port, Method: "TLS"})
	if !res.Exit.OK || res.Exit.NotSeen || res.Exit.ConnectionsError != "" {
		t.Errorf("没接快照应该什么都不说: %+v", res.Exit)
	}
}

func TestRunChallengeHeader(t *testing.T) {
	port, tlsCfg, up := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(403)
	})
	p := newFakeProxy(t, 200, up)
	res, err := (&Service{ProxyAddr: p.addr(), TLSConfig: tlsCfg, Timeout: 3 * time.Second}).Run(context.Background(), Input{Target: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exit.OK || res.Exit.Status == nil || *res.Exit.Status != 403 || res.Exit.Challenge != "cloudflare" {
		t.Errorf("人机验证应该标出来,请求本身算通: %+v", res.Exit)
	}
}

func TestRunRejectsBadInputAndCancel(t *testing.T) {
	var ie *InputError
	if _, err := (&Service{}).Run(context.Background(), Input{Target: "a b"}); !errors.As(err, &ie) {
		t.Errorf("坏输入应该是 InputError: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Service{ProxyAddr: "127.0.0.1:1"}).Run(ctx, Input{Target: "1.1.1.1"}); !errors.Is(err, context.Canceled) {
		t.Errorf("已取消应该返回 context.Canceled: %v", err)
	}
}

// asMap 取响应里 dns 的 {skipped} / {error} 形态;别的形态返回 nil。
func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
