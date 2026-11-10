package nsprobe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// 测试二进制自己兼当 ns-probe 子命令(Exec 的 Extra 为空时,Self 就是它)。
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "ns-probe" {
		os.Exit(Main(os.Args[2:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// tlsServer 起一个本机 TLS 服务器(证书含 example.com 和 127.0.0.1),返回端口和信任它的 tls.Config。
func tlsServer(t *testing.T, h http.HandlerFunc) (int, *tls.Config) {
	t.Helper()
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	_, p, _ := net.SplitHostPort(s.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	return port, &tls.Config{RootCAs: pool}
}

// fakeDNS 起一个 UDP DNS 服务器:A 查询回 ip,rcode 非 0 时回那个错误码,drop 时不理。返回 "127.0.0.1:端口"。
func fakeDNS(t *testing.T, ip string, rcode int, drop bool) (string, *int) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	queries := new(int)
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			*queries++
			if drop || n < 12 {
				continue
			}
			q := append([]byte(nil), buf[:n]...)
			resp := append([]byte(nil), q...)
			flags := uint16(0x8180) | uint16(rcode&0xf)
			binary.BigEndian.PutUint16(resp[2:], flags)
			// 问题段结束位置
			i := 12
			for i < len(q) && q[i] != 0 {
				i += int(q[i]) + 1
			}
			end := i + 1 + 4
			resp = resp[:end]
			qtype := binary.BigEndian.Uint16(q[i+1:])
			if rcode == 0 && qtype == 1 && ip != "" {
				binary.BigEndian.PutUint16(resp[6:], 1)
				resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
				resp = append(resp, net.ParseIP(ip).To4()...)
			}
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	return pc.LocalAddr().String(), queries
}

func TestRunHTTPDomainViaDHCPDNS(t *testing.T) {
	port, cfg := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com:"+strconv.Itoa(portOf(r)) && !strings.HasPrefix(r.Host, "example.com") {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	dns, _ := fakeDNS(t, "127.0.0.1", 0, false)
	p := &prober{tls: cfg}
	res, err := p.run(context.Background(), Request{Target: "example.com", Port: port, Method: "GET", DNS: []string{dns}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Status == nil || *res.Status != 204 || res.MS == nil || res.ConnectMs == nil || res.LocalPort == 0 {
		t.Fatalf("%+v", res)
	}
	if res.ConnectTo != "127.0.0.1" || res.DNS == nil || !res.DNS.OK || res.DNS.Server != dns || len(res.DNS.Answers) != 1 || res.DNS.Answers[0] != "127.0.0.1" {
		t.Fatalf("解析不对:%+v dns=%+v", res, res.DNS)
	}
	if res.Port != port || res.Method != "GET" || !strings.HasPrefix(res.URL, "https://example.com:") {
		t.Fatalf("%+v", res)
	}
}

func portOf(r *http.Request) int {
	_, p, _ := net.SplitHostPort(r.Context().Value(http.LocalAddrContextKey).(net.Addr).String())
	n, _ := strconv.Atoi(p)
	return n
}

func TestRunMethods(t *testing.T) {
	port, cfg := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Method", r.Method)
		if r.URL.Path == "/" {
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
		}
	})
	p := &prober{tls: cfg}
	ctx := context.Background()
	for _, c := range []struct {
		method string
		check  func(Result) bool
	}{
		{"GET", func(r Result) bool { return r.OK && *r.Status == 403 && r.Challenge == "cloudflare" }},
		{"head", func(r Result) bool { return r.OK && *r.Status == 403 && r.Method == "HEAD" }},
		{"TCP", func(r Result) bool {
			return r.OK && r.Status == nil && r.URL == "127.0.0.1:"+strconv.Itoa(port) && r.ConnectMs != nil
		}},
		{"TLS", func(r Result) bool { return r.OK && r.Status == nil && r.MS != nil }},
	} {
		res, err := p.run(ctx, Request{Target: "127.0.0.1", Port: port, Method: c.method})
		if err != nil || !c.check(res) {
			t.Fatalf("%s: err=%v res=%+v", c.method, err, res)
		}
		if res.DNS != nil {
			t.Fatalf("目标是 IP 不该有 dns:%+v", res.DNS)
		}
	}
}

func TestRunFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("端口没人听:对方拒绝连接", func(t *testing.T) {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		for _, m := range []string{"GET", "TCP", "TLS"} {
			res, _ := Run(ctx, Request{Target: "127.0.0.1", Port: port, Method: m})
			if res.OK || res.Error != "对方拒绝连接" || res.ConnectMs != nil {
				t.Fatalf("%s: %+v", m, res)
			}
		}
	})
	t.Run("证书对不上:证书校验失败", func(t *testing.T) {
		port, _ := tlsServer(t, func(http.ResponseWriter, *http.Request) {})
		res, _ := Run(ctx, Request{Target: "127.0.0.1", Port: port, Method: "GET"})
		if res.OK || res.Error != "证书校验失败" {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("超时", func(t *testing.T) {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		defer l.Close()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close() // 收下但不回
			}
		}()
		res, _ := Run(ctx, Request{Target: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, Method: "TLS", TimeoutMs: 300})
		if res.OK || !strings.HasPrefix(res.Error, "超时") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("DNS:没给服务器 / NXDOMAIN / 不响应 / 第一台坏第二台好", func(t *testing.T) {
		res, _ := Run(ctx, Request{Target: "a.test", Method: "TCP"})
		if res.OK || res.DNS == nil || !strings.Contains(res.Error, "没有给 DNS") || res.ConnectMs != nil {
			t.Fatalf("%+v", res)
		}
		nx, _ := fakeDNS(t, "", 3, false)
		res, _ = Run(ctx, Request{Target: "a.test", Method: "TCP", DNS: []string{nx}})
		if res.OK || res.DNS == nil || res.DNS.OK || !strings.Contains(res.DNS.Error, "NXDOMAIN") || res.Error != res.DNS.Error {
			t.Fatalf("%+v dns=%+v", res, res.DNS)
		}
		dead, q := fakeDNS(t, "", 0, true)
		start := time.Now()
		res, _ = Run(ctx, Request{Target: "a.test", Method: "TCP", DNS: []string{dead}})
		if res.OK || !strings.Contains(res.Error, "没有响应") || *q == 0 || time.Since(start) < 2*time.Second {
			t.Fatalf("%+v q=%d", res, *q)
		}
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		defer l.Close()
		good, _ := fakeDNS(t, "127.0.0.1", 0, false)
		res, _ = Run(ctx, Request{Target: "a.test", Port: l.Addr().(*net.TCPAddr).Port, Method: "TCP", DNS: []string{nx, good}})
		if !res.OK || res.DNS == nil || res.DNS.Server != good || res.ConnectTo != "127.0.0.1" {
			t.Fatalf("第二台该接上:%+v dns=%+v", res, res.DNS)
		}
	})
	t.Run("请求有问题", func(t *testing.T) {
		if _, err := Run(ctx, Request{Target: "-bad", Method: "GET"}); err == nil {
			t.Fatal("应报错")
		}
		if _, err := Run(ctx, Request{Target: "a.b", Method: "PUT"}); err == nil {
			t.Fatal("应报错")
		}
	})
}

func TestMain_Command(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{`not json`}, &out, &errb); code != 2 || errb.Len() == 0 {
		t.Fatalf("code=%d err=%s", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := Main(nil, &out, &errb); code != 2 {
		t.Fatalf("code=%d", code)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	out.Reset()
	errb.Reset()
	body := `{"target":"127.0.0.1","port":` + strconv.Itoa(l.Addr().(*net.TCPAddr).Port) + `,"method":"TCP"}`
	if code := Main([]string{body}, &out, &errb); code != 0 || !strings.Contains(out.String(), `"ok":true`) {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errb.String())
	}
	out.Reset()
	if code := Main([]string{`{"target":"x y"}`}, &out, &errb); code != 2 || out.Len() != 0 {
		t.Fatalf("坏请求不该有输出:%d %s", code, out.String())
	}
}

func TestExecProbe(t *testing.T) {
	ctx := context.Background()
	t.Run("命令行与解析", func(t *testing.T) {
		var got []string
		e := &Exec{Self: "/usr/bin/openbox-backend", Run: func(_ context.Context, name string, args ...string) (system.RunResult, error) {
			got = append([]string{name}, args...)
			return system.RunResult{Stdout: `{"url":"https://a.test/","port":443,"method":"GET","ok":true,"status":204,"ms":12}` + "\n", Stderr: "warn\nlast line\n"}, nil
		}}
		res, se, err := e.Probe(ctx, "obterm", Request{Target: "a.test", Method: "GET", DNS: []string{"10.0.0.1"}})
		if err != nil || !res.OK || *res.Status != 204 || se != "last line" {
			t.Fatalf("%+v %q %v", res, se, err)
		}
		if got[0] != "ip" || strings.Join(got[1:6], " ") != "netns exec obterm /usr/bin/openbox-backend ns-probe" || !strings.Contains(got[6], `"target":"a.test"`) || !strings.Contains(got[6], `"dns":["10.0.0.1"]`) {
			t.Fatalf("命令不对:%q", got)
		}
	})
	t.Run("起不来 / 非零退出 / 输出看不懂", func(t *testing.T) {
		run := func(r system.RunResult, err error) *Exec {
			return &Exec{Self: "x", Run: func(context.Context, string, ...string) (system.RunResult, error) { return r, err }}
		}
		if _, _, err := run(system.RunResult{}, errors.New("boom")).Probe(ctx, "n", Request{Target: "a.b"}); err == nil || !strings.Contains(err.Error(), "没能在命名空间里启动") {
			t.Fatal(err)
		}
		if _, se, err := run(system.RunResult{Code: 2, Stderr: "请求不对\n"}, nil).Probe(ctx, "n", Request{Target: "a.b"}); err == nil || se != "请求不对" {
			t.Fatalf("%q %v", se, err)
		}
		if _, _, err := run(system.RunResult{Stdout: "garbage"}, nil).Probe(ctx, "n", Request{Target: "a.b"}); err == nil || !strings.Contains(err.Error(), "看不懂") {
			t.Fatal(err)
		}
	})
}

// 真实命名空间:建 veth,一头留在本机(10.254.99.1,上面起 HTTP 和 DNS),另一头放进命名空间(10.254.99.2),
// 用测试二进制自己当 ns-probe,从命名空间里发 GET 和 TCP。要 root、ip、netns,缺一样就跳过。
func TestRealNetns(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("要 root")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("没有 ip 命令")
	}
	ns, hostIf, peerIf := "obnsp-t", "obnsp-h", "obnsp-p"
	cleanup := func() {
		_ = exec.Command("ip", "link", "del", hostIf).Run()
		_ = exec.Command("ip", "netns", "del", ns).Run()
	}
	cleanup()
	if out, err := exec.Command("ip", "netns", "add", ns).CombinedOutput(); err != nil {
		t.Skipf("建不了命名空间:%v %s", err, out)
	}
	t.Cleanup(cleanup)
	for _, a := range [][]string{
		{"link", "add", hostIf, "type", "veth", "peer", "name", peerIf},
		{"link", "set", peerIf, "netns", ns},
		{"addr", "add", "10.254.99.1/24", "dev", hostIf},
		{"link", "set", hostIf, "up"},
		{"-n", ns, "addr", "add", "10.254.99.2/24", "dev", peerIf},
		{"-n", ns, "link", "set", peerIf, "up"},
		{"-n", ns, "link", "set", "lo", "up"},
	} {
		if out, err := exec.Command("ip", a...).CombinedOutput(); err != nil {
			t.Skipf("配 veth 失败:%v %s", err, out)
		}
	}
	// 本机一侧:80 端口的 HTTP(记下来源地址)、53 端口的 DNS
	l, err := net.Listen("tcp", "10.254.99.1:80")
	if err != nil {
		t.Skip(err)
	}
	var from string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from = r.RemoteAddr
		w.WriteHeader(http.StatusNoContent)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { _ = srv.Close() })
	pc, err := net.ListenPacket("udp", "10.254.99.1:53")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, a, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			i := 12
			for q[i] != 0 {
				i += int(q[i]) + 1
			}
			resp := append([]byte(nil), q[:i+5]...)
			binary.BigEndian.PutUint16(resp[2:], 0x8180)
			binary.BigEndian.PutUint16(resp[6:], 1)
			resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 10, 254, 99, 1)
			_, _ = pc.WriteTo(resp, a)
		}
	}()

	self, _ := os.Executable()
	e := &Exec{Self: self}
	res, _, err := e.Probe(context.Background(), ns, Request{Target: "terminal.test", Port: 80, Method: "GET", DNS: []string{"10.254.99.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || *res.Status != 204 || res.ConnectTo != "10.254.99.1" || res.DNS == nil || !res.DNS.OK || res.DNS.Server != "10.254.99.1" {
		t.Fatalf("%+v dns=%+v", res, res.DNS)
	}
	if !strings.HasPrefix(from, "10.254.99.2:") {
		t.Fatalf("来源该是命名空间里的地址,实际 %q", from)
	}
	res, _, err = e.Probe(context.Background(), ns, Request{Target: "10.254.99.1", Port: 80, Method: "TCP"})
	if err != nil || !res.OK || res.URL != "10.254.99.1:80" {
		t.Fatalf("%+v %v", res, err)
	}
	// 命名空间里连不到的地址:如实失败,不是本机能连到的 127.0.0.1(本机的 80 端口只绑在 veth 地址上)
	res, _, err = e.Probe(context.Background(), ns, Request{Target: "127.0.0.1", Port: 80, Method: "TCP", TimeoutMs: 800})
	if err != nil || res.OK {
		t.Fatalf("命名空间的 127.0.0.1 不是本机的:%+v %v", res, err)
	}
}

// 每次问 DNS 用过的 UDP 源端口要报出来(给 conntrack 取证认「这一次」的查询用),且与服务器看到的来源端口一致。
func TestResolveReportsLocalPorts(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer pc.Close()
	seen := make(chan int, 8)
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			seen <- from.(*net.UDPAddr).Port
			if n < 12 {
				continue
			}
			q := append([]byte(nil), buf[:n]...)
			resp := append([]byte(nil), q...)
			binary.BigEndian.PutUint16(resp[2:], 0x8180)
			i := 12
			for i < len(q) && q[i] != 0 {
				i += int(q[i]) + 1
			}
			resp = resp[:i+1+4]
			binary.BigEndian.PutUint16(resp[6:], 1)
			resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 10, 0, 0, 1)
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	d, ip := resolve(context.Background(), "example.com", []string{pc.LocalAddr().String()})
	if ip != "10.0.0.1" || d == nil || !d.OK {
		t.Fatalf("解析:%+v ip=%s", d, ip)
	}
	if len(d.LocalPorts) == 0 {
		t.Fatalf("没有报本地端口:%+v", d)
	}
	got := <-seen
	found := false
	for _, p := range d.LocalPorts {
		if p == got {
			found = true
		}
	}
	if !found {
		t.Fatalf("报的端口 %v 里没有服务器看到的 %d", d.LocalPorts, got)
	}
}

// 失败的那次也要带上用过的端口(超时 / 被拒,conntrack 里照样有记录)。
func TestResolveFailureStillReportsLocalPorts(t *testing.T) {
	dns, _ := fakeDNS(t, "", 0, true) // 只收不回
	d, ip := resolve(context.Background(), "example.com", []string{dns})
	if ip != "" || d == nil || d.OK || len(d.LocalPorts) == 0 {
		t.Fatalf("%+v ip=%q", d, ip)
	}
}

// ---- hold:出结果后连接先留着,release / stdin 关闭后才关 ----

// closeWatcher 是个只会按行回一个 204 的 HTTP / TCP 服务,并报告每条连接什么时候被对端关掉。
func closeWatcher(t *testing.T, httpReply bool) (port int, closed chan struct{}) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	closed = make(chan struct{}, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if httpReply {
					buf := make([]byte, 4096)
					_, _ = c.Read(buf)
					_, _ = c.Write([]byte("HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n"))
				}
				_, _ = io.Copy(io.Discard, c) // 读到 EOF = 对端关了
				closed <- struct{}{}
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, closed
}

func expectOpenThenClosed(t *testing.T, closed chan struct{}, release func()) {
	t.Helper()
	select {
	case <-closed:
		t.Fatal("连接不该在 release 之前被关掉")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("release 之后连接该关掉")
	}
}

func TestRunHoldKeepsConnectionUntilRelease(t *testing.T) {
	t.Run("TCP", func(t *testing.T) {
		port, closed := closeWatcher(t, false)
		res, release, err := RunHold(context.Background(), Request{Target: "127.0.0.1", Port: port, Method: "TCP"})
		if err != nil || !res.OK || res.LocalPort == 0 {
			t.Fatalf("%+v %v", res, err)
		}
		expectOpenThenClosed(t, closed, release)
		release() // 可重复调用
	})
	for _, m := range []string{"GET", "HEAD"} {
		t.Run(m, func(t *testing.T) {
			closed := make(chan struct{}, 4)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
			srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
				if st == http.StateClosed {
					closed <- struct{}{}
				}
			}
			srv.StartTLS()
			t.Cleanup(srv.Close)
			port, cfg := tlsFor(srv)
			p := &prober{hold: true, tls: cfg}
			res, err := p.run(context.Background(), Request{Target: "127.0.0.1", Port: port, Method: m})
			if err != nil || !res.OK || res.LocalPort == 0 {
				t.Fatalf("%+v %v", res, err)
			}
			expectOpenThenClosed(t, closed, p.release)
		})
	}
}

func TestRunStillClosesRightAway(t *testing.T) {
	port, closed := closeWatcher(t, false)
	if res, err := Run(context.Background(), Request{Target: "127.0.0.1", Port: port, Method: "TCP"}); err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 不留连接,探测完就该关")
	}
	// HTTP 也一样(不留连接)
	hclosed := make(chan struct{}, 4)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateClosed {
			hclosed <- struct{}{}
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	hport, cfg := tlsFor(srv)
	p := &prober{tls: cfg}
	if res, err := p.run(context.Background(), Request{Target: "127.0.0.1", Port: hport, Method: "GET"}); err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	select {
	case <-hclosed:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP 探测完该关掉连接")
	}
}

func TestMainHoldWaitsForStdinClose(t *testing.T) {
	port, closed := closeWatcher(t, false)
	stdinR, stdinW := io.Pipe()
	var out, errb bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- MainIO([]string{`{"target":"127.0.0.1","port":` + strconv.Itoa(port) + `,"method":"TCP","holdMs":8000}`}, stdinR, &out, &errb)
	}()
	select {
	case <-done:
		t.Fatal("stdin 没关,进程不该退出")
	case <-closed:
		t.Fatal("连接不该被关")
	case <-time.After(300 * time.Millisecond):
	}
	_ = stdinW.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code=%d %s", code, errb.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stdin 关了进程该退出")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("进程退出前该关掉连接")
	}
	if !strings.Contains(out.String(), `"localPort"`) {
		t.Fatalf("结果:%s", out.String())
	}
}

func TestMainHoldDoesNotWaitWhenNotConnected(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // 没人听:连不上
	stdinR, _ := io.Pipe()
	var out, errb bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- MainIO([]string{`{"target":"127.0.0.1","port":` + strconv.Itoa(port) + `,"method":"TCP","holdMs":8000,"timeoutMs":1000}`}, stdinR, &out, &errb)
	}()
	select {
	case code := <-done:
		if code != 0 || !strings.Contains(out.String(), `"ok":false`) {
			t.Fatalf("code=%d out=%s", code, out.String())
		}
	case <-time.After(4 * time.Second):
		t.Fatal("没连上就没有要留的连接,不该等")
	}
}

// ---- Exec.ProbeHold:读到结果后调用 during,during 返回才关 stdin ----

type fakeProc struct {
	stdout      io.Reader
	stdinOpen   bool
	stdinClosed chan struct{}
	stderr      string
	code        int
}

type nopWC struct{ closed chan struct{} }

func (n *nopWC) Write(p []byte) (int, error) { return len(p), nil }
func (n *nopWC) Close() error                { close(n.closed); return nil }

func holdExec(fp *fakeProc, started *[]string) *Exec {
	return &Exec{Self: "/bin/self", Start: func(_ context.Context, name string, args ...string) (*Proc, error) {
		*started = append([]string{name}, args...)
		in := &nopWC{closed: fp.stdinClosed}
		return &Proc{Stdin: in, Stdout: fp.stdout, Wait: func() (string, int, error) {
			<-fp.stdinClosed // 进程是 stdin 关了才退出的
			return fp.stderr, fp.code, nil
		}}, nil
	}}
}

func TestExecProbeHold(t *testing.T) {
	line := `{"url":"a","port":80,"method":"TCP","ok":true,"localPort":40001,"ms":3}` + "\n"
	t.Run("结果出来后调 during,during 之前 stdin 没关", func(t *testing.T) {
		fp := &fakeProc{stdout: strings.NewReader(line), stdinClosed: make(chan struct{})}
		var started []string
		var duringSaw Result
		stdinClosedInDuring := true
		res, _, err := holdExec(fp, &started).ProbeHold(context.Background(), "obterm", Request{Target: "a.b", Method: "TCP"}, func(r Result) {
			duringSaw = r
			select {
			case <-fp.stdinClosed:
			default:
				stdinClosedInDuring = false
			}
		})
		if err != nil || !res.OK || duringSaw.LocalPort != 40001 || stdinClosedInDuring {
			t.Fatalf("res=%+v err=%v during=%+v stdinClosedInDuring=%v", res, err, duringSaw, stdinClosedInDuring)
		}
		if started[0] != "ip" || strings.Join(started[1:5], " ") != "netns exec obterm /bin/self" || !strings.Contains(started[6], `"holdMs":6000`) {
			t.Fatalf("命令:%q", started)
		}
	})
	t.Run("没连上不调 during", func(t *testing.T) {
		fp := &fakeProc{stdout: strings.NewReader(`{"url":"a","port":80,"method":"TCP","ok":false,"error":"拒绝"}` + "\n"), stdinClosed: make(chan struct{})}
		var started []string
		called := false
		res, _, err := holdExec(fp, &started).ProbeHold(context.Background(), "n", Request{Target: "a.b"}, func(Result) { called = true })
		if err != nil || res.OK || called {
			t.Fatalf("%+v %v called=%v", res, err, called)
		}
	})
	t.Run("进程没输出就退出 / 非零退出 / 输出看不懂", func(t *testing.T) {
		var started []string
		fp := &fakeProc{stdout: strings.NewReader(""), stdinClosed: make(chan struct{}), stderr: "请求不对\n", code: 2}
		if _, se, err := holdExec(fp, &started).ProbeHold(context.Background(), "n", Request{Target: "a.b"}, nil); err == nil || se != "请求不对" {
			t.Fatalf("%q %v", se, err)
		}
		fp = &fakeProc{stdout: strings.NewReader("garbage\n"), stdinClosed: make(chan struct{})}
		if _, _, err := holdExec(fp, &started).ProbeHold(context.Background(), "n", Request{Target: "a.b"}, nil); err == nil || !strings.Contains(err.Error(), "看不懂") {
			t.Fatal(err)
		}
		fp = &fakeProc{stdout: strings.NewReader(line), stdinClosed: make(chan struct{}), stderr: "boom", code: 1}
		if _, se, err := holdExec(fp, &started).ProbeHold(context.Background(), "n", Request{Target: "a.b"}, nil); err == nil || se != "boom" {
			t.Fatalf("%q %v", se, err)
		}
	})
	t.Run("起不来", func(t *testing.T) {
		e := &Exec{Self: "x", Start: func(context.Context, string, ...string) (*Proc, error) { return nil, errors.New("boom") }}
		if _, _, err := e.ProbeHold(context.Background(), "n", Request{Target: "a.b"}, nil); err == nil || !strings.Contains(err.Error(), "没能在命名空间里启动") {
			t.Fatal(err)
		}
	})
	t.Run("等不到结果就按 ctx 超时收场", func(t *testing.T) {
		pr, pw := io.Pipe()
		defer pw.Close()
		fp := &fakeProc{stdout: pr, stdinClosed: make(chan struct{})}
		var started []string
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		// ctx 先到期:ProbeHold 内部的 total 更长,所以由外层 ctx 触发
		e := holdExec(fp, &started)
		if _, _, err := e.ProbeHold(ctx, "n", Request{Target: "a.b"}, nil); err == nil || !strings.Contains(err.Error(), "超时") {
			t.Fatalf("%v", err)
		}
	})
}

// tlsFor 取一个已启动的 TLS 测试服务器的端口和信任它的 tls.Config。
func tlsFor(s *httptest.Server) (int, *tls.Config) {
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	_, p, _ := net.SplitHostPort(s.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	return port, &tls.Config{RootCAs: pool}
}
