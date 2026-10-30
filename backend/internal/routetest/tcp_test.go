package routetest

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// fakeSocks 是假的面板入站(只说 SOCKS5):按 rep 应答 CONNECT,记下客户端请求的地址类型、地址和端口。
// rep 为 0 时应答成功并保持连接直到客户端关;greeting 不为空时把它原样当作问候应答(用来模拟「不是 SOCKS5」)。
type fakeSocks struct {
	ln       net.Listener
	rep      byte
	greeting []byte
	silent   bool // 握手完成后不回应答(模拟内核还在连目标)
	mu       sync.Mutex
	ports    []string
	reqs     []socksReq
}

type socksReq struct {
	atyp byte
	host string // 域名原样;IP 用文本形式
	port int
}

func newFakeSocks(t *testing.T) *fakeSocks {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSocks{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSocks) addr() string { return f.ln.Addr().String() }

func (f *fakeSocks) lastPort() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ports) == 0 {
		return ""
	}
	return f.ports[len(f.ports)-1]
}

func (f *fakeSocks) last() socksReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		return socksReq{}
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeSocks) serve(c net.Conn) {
	defer c.Close()
	_, port, _ := net.SplitHostPort(c.RemoteAddr().String())
	var h [2]byte
	if _, err := io.ReadFull(c, h[:]); err != nil || h[0] != 5 {
		return
	}
	if _, err := io.ReadFull(c, make([]byte, h[1])); err != nil {
		return
	}
	if f.greeting != nil {
		_, _ = c.Write(f.greeting)
		return
	}
	_, _ = c.Write([]byte{5, 0})
	var q [4]byte
	if _, err := io.ReadFull(c, q[:]); err != nil || q[1] != 1 {
		return
	}
	var host string
	switch q[3] {
	case 1:
		b := make([]byte, 4)
		_, _ = io.ReadFull(c, b)
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		_, _ = io.ReadFull(c, b)
		host = net.IP(b).String()
	case 3:
		var l [1]byte
		_, _ = io.ReadFull(c, l[:])
		b := make([]byte, l[0])
		_, _ = io.ReadFull(c, b)
		host = string(b)
	}
	var pb [2]byte
	_, _ = io.ReadFull(c, pb[:])
	f.mu.Lock()
	f.ports = append(f.ports, port)
	f.reqs = append(f.reqs, socksReq{atyp: q[3], host: host, port: int(binary.BigEndian.Uint16(pb[:]))})
	f.mu.Unlock()
	if f.silent {
		_, _ = io.Copy(io.Discard, c)
		return
	}
	// 应答:VER REP RSV ATYP(IPv4) BND.ADDR(出站的本地地址) BND.PORT
	_, _ = c.Write([]byte{5, f.rep, 0, 1, 127, 0, 0, 1, 0x30, 0x39})
	if f.rep == 0 {
		_, _ = io.Copy(io.Discard, c) // 保持连接开着,供读连接表
	}
}

func TestTCPSuccessWithDomainAndRoute(t *testing.T) {
	f := newFakeSocks(t)
	s := &Service{ProxyAddr: f.addr(), Timeout: 3 * time.Second,
		Snapshot: snapshotForPort(f.lastPort, &singbox.KernelConn{ID: "1", Chains: []string{"hk-01", "自动选择", "海外"}, Rule: "rule_set=geosite-x", DestIP: "93.184.216.34"})}
	res, err := s.Run(context.Background(), Input{Target: "example.com", Port: 22, Method: "TCP"})
	if err != nil {
		t.Fatal(err)
	}
	ex := res.Exit
	if !ex.OK || ex.Error != "" || ex.MS == nil || ex.Status != nil || ex.Method != "TCP" {
		t.Fatalf("TCP 应该成功,没有状态码: %+v", ex)
	}
	if ex.URL != "example.com:22" {
		t.Errorf("TCP 的 url 应该是 主机:端口,不是网址: %q", ex.URL)
	}
	if r := f.last(); r.atyp != 3 || r.host != "example.com" || r.port != 22 {
		t.Errorf("域名目标应该用域名地址类型交给内核解析: %+v", r)
	}
	if strings.Join(ex.Chains, ",") != "海外,自动选择,hk-01" || ex.Rule != "rule_set=geosite-x" || ex.DestinationIP != "93.184.216.34" || ex.ViaProxy == nil || !*ex.ViaProxy {
		t.Errorf("成功后要按本地端口认出连接: %+v", ex)
	}
}

func TestTCPDefaultPortAndAddressTypes(t *testing.T) {
	f := newFakeSocks(t)
	s := &Service{ProxyAddr: f.addr(), Timeout: 3 * time.Second}
	ctx := context.Background()
	for _, c := range []struct {
		name, target, connectTo string
		port                    int
		atyp                    byte
		host                    string
		wantPort                int
		wantURL                 string
	}{
		{"没指定端口按 443", "example.com", "", 0, 3, "example.com", 443, "example.com:443"},
		{"IPv4 字面量", "1.2.3.4", "", 80, 1, "1.2.3.4", 80, "1.2.3.4:80"},
		{"IPv6 字面量", "2001:db8::1", "", 8443, 4, "2001:db8::1", 8443, "[2001:db8::1]:8443"},
		{"连 DNS 答案的 IP", "example.com", "93.184.216.34", 443, 1, "93.184.216.34", 443, ""},
		{"连 v6 答案", "example.com", "2001:db8::2", 443, 4, "2001:db8::2", 443, ""},
	} {
		p := s.probeTCP(ctx, Input{Target: c.target, Port: c.port, Method: "TCP"}, c.connectTo)
		ok, perr := p.ok, p.err
		p.close()
		if !ok || perr != nil {
			t.Errorf("%s: %v", c.name, perr)
			continue
		}
		if r := f.last(); r.atyp != c.atyp || r.host != c.host || r.port != c.wantPort {
			t.Errorf("%s: %+v", c.name, r)
		}
		if c.wantURL != "" {
			if got := tcpAddr(Input{Target: c.target, Port: c.port}); got != c.wantURL {
				t.Errorf("%s: url %q, want %q", c.name, got, c.wantURL)
			}
		}
	}
}

func TestTCPFailureCodes(t *testing.T) {
	for _, c := range []struct {
		rep  byte
		want string
	}{
		{1, "一般性失败"}, {2, "规则不允许"}, {3, "网络不可达"}, {4, "主机不可达"}, {5, "拒绝了连接"},
		{6, "超时"}, {7, "不支持这个操作"}, {8, "不支持这个地址类型"}, {0x42, "0x42"},
	} {
		f := newFakeSocks(t)
		f.rep = c.rep
		called := false
		s := &Service{ProxyAddr: f.addr(), Timeout: 2 * time.Second,
			Snapshot: func(context.Context) (singbox.KernelSnapshot, error) {
				called = true
				return singbox.KernelSnapshot{}, nil
			}}
		res, err := s.Run(context.Background(), Input{Target: "1.2.3.4", Port: 22, Method: "TCP"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Exit.OK || res.Exit.MS != nil || !strings.Contains(res.Exit.Error, c.want) || !strings.HasPrefix(res.Exit.Error, "内核") {
			t.Errorf("应答码 %d:%+v", c.rep, res.Exit)
		}
		if called {
			t.Errorf("应答码 %d:没连上不该读连接表", c.rep)
		}
	}
}

func TestTCPProtocolAndConnectionProblems(t *testing.T) {
	// 对方不是 SOCKS5(例如 HTTP 服务)
	f := newFakeSocks(t)
	f.greeting = []byte("HT")
	res, _ := (&Service{ProxyAddr: f.addr(), Timeout: 2 * time.Second}).Run(context.Background(), Input{Target: "1.2.3.4", Port: 22, Method: "TCP"})
	if res.Exit.OK || res.Exit.Error != "内核的本地代理入口没有按 SOCKS5 应答" {
		t.Errorf("%+v", res.Exit)
	}
	// 问候后就关连接
	f2 := newFakeSocks(t)
	f2.greeting = []byte{}
	res, _ = (&Service{ProxyAddr: f2.addr(), Timeout: 2 * time.Second}).Run(context.Background(), Input{Target: "1.2.3.4", Port: 22, Method: "TCP"})
	if res.Exit.OK || res.Exit.Error != "内核的本地代理入口没有按 SOCKS5 应答" {
		t.Errorf("%+v", res.Exit)
	}
	// 入口连不上
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	res, _ = (&Service{ProxyAddr: dead, Timeout: time.Second}).Run(context.Background(), Input{Target: "1.2.3.4", Port: 22, Method: "TCP"})
	if res.Exit.OK || !strings.Contains(res.Exit.Error, "内核没在运行") {
		t.Errorf("%+v", res.Exit)
	}
	// 内核一直没应答(还在连目标):按超时报,不能当成功
	f3 := newFakeSocks(t)
	f3.silent = true
	start := time.Now()
	res, _ = (&Service{ProxyAddr: f3.addr(), Timeout: 400 * time.Millisecond}).Run(context.Background(), Input{Target: "1.2.3.4", Port: 22, Method: "TCP"})
	if res.Exit.OK || !strings.HasPrefix(res.Exit.Error, "超时") || time.Since(start) > 3*time.Second {
		t.Errorf("%+v 耗时 %v", res.Exit, time.Since(start))
	}
}

func TestTCPCancelStopsPromptly(t *testing.T) {
	f := newFakeSocks(t)
	f.silent = true
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := (&Service{ProxyAddr: f.addr(), Timeout: 10 * time.Second}).Run(ctx, Input{Target: "1.2.3.4", Port: 22, Method: "TCP"})
	if err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("取消应该很快返回错误: %v 耗时 %v", err, time.Since(start))
	}
}

func TestTCPRunDoesFlushAndResolveAndExit6(t *testing.T) {
	f := newFakeSocks(t)
	dns := v6DNS(t, "2001:db8::1")
	flushed := 0
	s := &Service{ProxyAddr: f.addr(), DNSAddr: dns.addr, IPv6: ipv6On, Timeout: 3 * time.Second,
		FlushDNS: func(context.Context) error { flushed++; return nil }}
	res, err := s.Run(context.Background(), Input{Target: "example.com", Port: 22, Method: "TCP"})
	if err != nil {
		t.Fatal(err)
	}
	if flushed != 1 || res.Resolve == nil || !res.Resolve.OK {
		t.Errorf("TCP 也要清缓存、做 resolve: flushed=%d %+v", flushed, res.Resolve)
	}
	// 像终端一样连 DNS 给的第一个 IPv4
	if !res.Exit.OK || res.Exit.ConnectTo != "93.184.216.34" || res.Exit.URL != "example.com:22" {
		t.Errorf("%+v", res.Exit)
	}
	// exit6:第一个 v6 地址再连一次,方式也是 TCP
	if res.Exit6 == nil || !res.Exit6.OK || res.Exit6.ConnectTo != "2001:db8::1" || res.Exit6.Status != nil {
		t.Fatalf("%+v", res.Exit6)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 2 || f.reqs[0].host != "93.184.216.34" || f.reqs[0].atyp != 1 || f.reqs[1].host != "2001:db8::1" || f.reqs[1].atyp != 4 {
		t.Errorf("两次 CONNECT 的地址不对: %+v", f.reqs)
	}
	_ = strconv.Itoa
}

func TestSocksReplyTextNeverEmpty(t *testing.T) {
	for i := 1; i < 256; i++ {
		if socksReplyText(byte(i)) == "" {
			t.Errorf("应答码 %d 没有文字", i)
		}
	}
}

// snapshotForPort 和 snapshotFor 一样,只是本地端口从函数取(假 SOCKS5 入口记的端口)。
func snapshotForPort(port func() string, c *singbox.KernelConn, direct ...string) func(context.Context) (singbox.KernelSnapshot, error) {
	return func(context.Context) (singbox.KernelSnapshot, error) {
		cc := *c
		cc.SourcePort = port()
		cc.Source = "127.0.0.1"
		other := singbox.KernelConn{ID: "x", Chains: []string{"node-z", "other"}, Source: "127.0.0.1", SourcePort: "1"}
		return singbox.KernelSnapshot{Conns: []singbox.KernelConn{other, cc}, DirectTags: direct}, nil
	}
}
