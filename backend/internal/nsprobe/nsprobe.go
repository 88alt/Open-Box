// Package nsprobe 是 terminal-test 里「虚拟终端自己发的那次探测」:在虚拟终端的网络命名空间里,
// 像一台普通 LAN 设备那样直接发 GET / HEAD / TCP / TLS——先用 DHCP 给的 DNS 解析域名,再按解析出的第一个 IPv4
// 去连(Host / SNI / 证书校验仍用域名),不经过内核的面板入站,所以流量走的是命名空间的默认路由,
// 由路由器按真实的转发路径处理(被 TUN / 透明代理接走与否,正是 terminal-test 要看的)。
// 自己的设计,依据前端 api/openbox.ts 的 OpenboxTerminalTest(dns / exit 两部分)的字段与注释,没有参照上游。
//
// 做法:本程序用 `ip netns exec <命名空间> <本程序> ns-probe <请求 JSON>` 重新调起自己的一个隐藏子命令(Main),
// 子命令在命名空间里跑完探测、把结果 JSON 打到标准输出。这样不用在 Go 里切换命名空间(切线程命名空间容易污染别的 goroutine),
// 探测进程用完即退,不留任何常驻物。
package nsprobe

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/routetest"
	"github.com/timeflysoon/Open-Box/backend/internal/sitelatency"
)

const (
	// DefaultTimeout 是探测本身(解析之后的连接 + 请求或握手)的超时,与 route-test 一致。
	DefaultTimeout = routetest.DefaultTimeout
	// dnsTimeout 是向每台 DNS 服务器问一次的超时。
	dnsTimeout     = 3 * time.Second
	defaultTCPPort = 443
	maxBodyRead    = 64 << 10
)

// Request 是传给子命令的请求。DNS 是 DHCP 给的服务器(IPv4 地址,可写成 "ip:端口",默认 53),按顺序问。
type Request struct {
	Target    string   `json:"target"`
	Port      int      `json:"port"`
	Method    string   `json:"method"`
	DNS       []string `json:"dns"`
	TimeoutMs int      `json:"timeoutMs,omitempty"`
	// HoldMs > 0:探测出结果(且连上了)之后先不关连接,等标准输入关闭或这么多毫秒(最长 MaxHoldMs)再退出。
	// 后端趁这段时间读内核的连接表和 conntrack,认出这条连接;连接已经关了,内核连接表里就没有它了。
	HoldMs int `json:"holdMs,omitempty"`
}

// MaxHoldMs 是 HoldMs 的上限,防止探测进程被拖着不退出。
const MaxHoldMs = 10000

// DNSResult 对应前端 terminal-test 响应的 dns(目标是域名时)。Server 是给出答案(或最后一台试过)的服务器。
type DNSResult struct {
	Server  string   `json:"server"`
	OK      bool     `json:"ok"`
	Answers []string `json:"answers"`
	MS      int      `json:"ms"`
	Error   string   `json:"error,omitempty"`
	// LocalPorts 是问 Server 这一次用过的 UDP 源端口(一次解析可能换端口重试,所以是列表),
	// 给 conntrack 取证精确认出这次的查询;不进前端的 dns 字段。
	LocalPorts []int `json:"localPorts,omitempty"`
}

// Result 是子命令的输出。DNS 为空表示目标是 IP,没有解析这一步。
type Result struct {
	URL       string     `json:"url"`
	Port      int        `json:"port"`
	Method    string     `json:"method"`
	DNS       *DNSResult `json:"dns,omitempty"`
	ConnectTo string     `json:"connectTo,omitempty"`
	LocalPort int        `json:"localPort,omitempty"`
	ConnectMs *int       `json:"connectMs,omitempty"` // 只算 TCP 连上用了多久
	OK        bool       `json:"ok"`
	Status    *int       `json:"status,omitempty"`
	MS        *int       `json:"ms,omitempty"`
	Challenge string     `json:"challenge,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// prober 带着可被测试替换的部分。
type prober struct {
	tls  *tls.Config
	hold bool     // 探测成功后连接留着,由 release 一起关
	held []func() // hold 时要关的东西
}

func (p *prober) release() {
	for _, f := range p.held {
		f()
	}
	p.held = nil
}

// Run 在当前网络命名空间里做一次探测(子命令调用它;测试里直接调)。请求内容有问题返回 error。
func Run(ctx context.Context, req Request) (Result, error) {
	res, release, err := RunHold(ctx, req)
	release()
	return res, err
}

// RunHold 与 Run 一样,但探测成功后连接不关,返回的 release 才关(可以重复调用)。
// 后端要在连接还开着的时候去内核连接表里认它。
func RunHold(ctx context.Context, req Request) (Result, func(), error) {
	p := &prober{hold: true}
	res, err := p.run(ctx, req)
	return res, p.release, err
}

func (p *prober) run(ctx context.Context, req Request) (Result, error) {
	in, err := routetest.Parse(routetest.Input{Target: req.Target, Port: req.Port, Method: req.Method})
	if err != nil {
		return Result{}, err
	}
	timeout := DefaultTimeout
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	port := in.Port
	if port == 0 {
		port = defaultTCPPort
	}
	res := Result{Method: in.Method, Port: port}
	if in.Method == routetest.MethodTCP {
		res.URL = net.JoinHostPort(in.Target, strconv.Itoa(port))
	} else {
		res.URL = routetest.ProbeURL(in.Target, in.Port)
	}

	if _, err := netip.ParseAddr(in.Target); err != nil {
		// 域名:先像终端那样问 DHCP 给的 DNS
		d, ip := resolve(ctx, in.Target, req.DNS)
		res.DNS = d
		if ip == "" {
			res.Error = d.Error
			return res, nil
		}
		res.ConnectTo = ip
	}

	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var perr error
	switch in.Method {
	case routetest.MethodTCP:
		perr = p.probeTCP(pctx, in.Target, port, &res)
	case routetest.MethodTLS:
		perr = p.probeTLS(pctx, in.Target, port, &res)
	default:
		perr = p.probeHTTP(pctx, in, &res)
	}
	if perr != nil {
		res.OK = false
		res.Error = sitelatency.DescribeError(perr, timeout)
	}
	return res, nil
}

func ms(d time.Duration) *int {
	v := max(int((d+500*time.Microsecond)/time.Millisecond), 1)
	return &v
}

// dialer 直连:目标是域名时连的是解析出的 IPv4(res.ConnectTo),端口不变;记下本地端口和连接耗时。
func dial(ctx context.Context, res *Result, addr string) (net.Conn, error) {
	if res.ConnectTo != "" {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addr = net.JoinHostPort(res.ConnectTo, port)
	}
	start := time.Now()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	res.ConnectMs = ms(time.Since(start))
	if ta, ok := c.LocalAddr().(*net.TCPAddr); ok {
		res.LocalPort = ta.Port
	}
	return c, nil
}

func (p *prober) probeTCP(ctx context.Context, target string, port int, res *Result) error {
	start := time.Now()
	c, err := dial(ctx, res, net.JoinHostPort(target, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	if p.hold {
		p.held = append(p.held, func() { _ = c.Close() })
	} else {
		_ = c.Close()
	}
	res.OK, res.MS = true, ms(time.Since(start))
	return nil
}

// probeTLS 连上之后做一次 TLS 握手,耗时是「连接 + 握手」。目标是 IP 时证书通常对不上,不校验(只测连通与握手)。
func (p *prober) probeTLS(ctx context.Context, target string, port int, res *Result) error {
	start := time.Now()
	c, err := dial(ctx, res, net.JoinHostPort(target, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = c.Close()
		}
	}()
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if p.tls != nil {
		cfg = p.tls.Clone()
	} else if _, e := netip.ParseAddr(target); e == nil {
		cfg.InsecureSkipVerify = true
	}
	if cfg.ServerName == "" {
		cfg.ServerName = target
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if err := tls.Client(c, cfg).HandshakeContext(ctx); err != nil {
		return err
	}
	if p.hold {
		keep = true
		p.held = append(p.held, func() { _ = c.Close() })
	}
	res.OK, res.MS = true, ms(time.Since(start))
	return nil
}

// probeHTTP 发一次 GET 或 HEAD,证书校验照常,3xx 不跟随,拿到什么状态码报什么(与 route-test 一致)。
func (p *prober) probeHTTP(ctx context.Context, in routetest.Input, res *Result) error {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dial(ctx, res, addr)
		},
		TLSClientConfig:     p.tls,
		TLSHandshakeTimeout: DefaultTimeout,
		DisableCompression:  true,
		DisableKeepAlives:   !p.hold, // 要留着连接时不能让它在读完后被关掉
		Proxy:               nil,     // 终端不走代理设置
	}
	kept := false
	defer func() {
		if !kept {
			tr.CloseIdleConnections()
		}
	}()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	method := http.MethodGet
	if in.Method == routetest.MethodHEAD {
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(ctx, method, res.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenBox terminal-test)")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	took := time.Since(start)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyRead))
	if p.hold {
		// 响应体没读完时关 Body 会把连接一起关掉,所以留到 release 再关
		kept = true
		p.held = append(p.held, func() { _ = resp.Body.Close(); tr.CloseIdleConnections() })
	} else {
		_ = resp.Body.Close()
	}
	res.OK, res.MS = true, ms(took)
	st := resp.StatusCode
	res.Status = &st
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("cf-mitigated")), "challenge") {
		res.Challenge = "cloudflare" // 人机验证:请求本身是通的,403 / 503 不是线路问题
	}
	return nil
}

// resolve 按顺序问每台 DNS 服务器一次 A 记录,第一台给出 IPv4 答案的算数。
// 返回的 DNSResult 里 Server 是给出答案的那台(都失败时是最后一台试过的),MS 是那一次的耗时。
// 返回的 ip 为空表示没解析出来,DNSResult.Error 是原因。
func resolve(ctx context.Context, name string, servers []string) (*DNSResult, string) {
	if len(servers) == 0 {
		return &DNSResult{Answers: []string{}, Error: "DHCP 没有给 DNS 服务器,虚拟终端没法解析域名"}, ""
	}
	var last *DNSResult
	for _, s := range servers {
		addr := s
		if _, _, err := net.SplitHostPort(s); err != nil {
			addr = net.JoinHostPort(s, "53")
		}
		var pmu sync.Mutex
		var ports []int
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, "udp", addr)
			if err == nil {
				if ua, ok := c.LocalAddr().(*net.UDPAddr); ok {
					pmu.Lock()
					ports = append(ports, ua.Port)
					pmu.Unlock()
				}
			}
			return c, err
		}}
		qctx, cancel := context.WithTimeout(ctx, dnsTimeout)
		start := time.Now()
		ips, err := r.LookupIP(qctx, "ip4", name)
		cancel()
		pmu.Lock()
		got := append([]int(nil), ports...)
		pmu.Unlock()
		d := &DNSResult{Server: s, Answers: []string{}, MS: int(time.Since(start).Milliseconds()), LocalPorts: got}
		last = d
		if err != nil {
			d.Error = describeDNS(err)
			if ctx.Err() != nil {
				return d, ""
			}
			continue
		}
		for _, ip := range ips {
			d.Answers = append(d.Answers, ip.String())
		}
		if len(d.Answers) == 0 {
			d.Error = "没有 IPv4 答案"
			continue
		}
		d.OK = true
		return d, d.Answers[0]
	}
	return last, ""
}

func describeDNS(err error) string {
	var de *net.DNSError
	switch {
	case errors.As(err, &de) && de.IsNotFound:
		return "域名不存在(NXDOMAIN)或没有 A 记录"
	case errors.As(err, &de) && de.IsTimeout, errors.Is(err, context.DeadlineExceeded):
		return "DNS 服务器没有响应(超时)"
	case errors.Is(err, context.Canceled):
		return "已取消"
	}
	msg := err.Error()
	if r := []rune(msg); len(r) > 100 {
		msg = string(r[:100]) + "…"
	}
	return "DNS 解析失败:" + msg
}

// Main 是隐藏子命令 `ns-probe <请求 JSON>` 的入口:跑一次探测,结果 JSON 打到 stdout。
// 请求有问题打错误到 stderr、返回 2;探测本身失败不算错(写在结果的 error 里,返回 0)。
func Main(args []string, stdout, stderr io.Writer) int {
	return MainIO(args, os.Stdin, stdout, stderr)
}

// MainIO 同 Main,标准输入可换(测试用)。请求带 HoldMs 且连上了(结果里有 localPort)时,
// 结果打出去之后连接继续留着,直到 stdin 关闭或 HoldMs 到(最长 MaxHoldMs)才退出。
func MainIO(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "用法:ns-probe <请求 JSON>")
		return 2
	}
	var req Request
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		fmt.Fprintln(stderr, "请求不是合法的 JSON:", err)
		return 2
	}
	hold := req.HoldMs > 0
	run := Run
	var release func()
	if hold {
		run = func(ctx context.Context, r Request) (Result, error) {
			res, rel, err := RunHold(ctx, r)
			release = rel
			return res, err
		}
	}
	res, err := run(context.Background(), req)
	if release != nil {
		defer release()
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := json.NewEncoder(stdout).Encode(res); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if hold && res.LocalPort != 0 {
		wait := time.Duration(min(req.HoldMs, MaxHoldMs)) * time.Millisecond
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, stdin)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(wait):
		}
	}
	return 0
}
