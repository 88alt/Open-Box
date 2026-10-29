// Package routetest 实现 POST /api/openbox/route-test 的「真实访问」部分(响应里的 exit):
// 让一次真实的访问经内核的面板入站走一遍路由,再从内核的连接列表里认出这条连接,告诉用户它命中了哪条规则、
// 实际走了哪条线路(站点集 → 组 → 节点)、连到了哪个 IP。
// 自己的设计,依据前端 api/openbox.ts 的 OpenboxRouteTest / OpenboxProbeMethod 类型和注释、RouteComparison.vue 的用法,
// 以及 sing-box 官方 Clash API 的 GET /connections,没有参照上游。
//
// dns(按已部署配置里的 DNS 规则预测这个域名会问哪台服务器)见 dns.go。这一部分没有做、响应里如实标出的:
//   - context(按终端预测),以及 exit.connectTo / noDomain / debug
//     (resolve 做了:见 resolve.go 和 dns.go 的 annotateResolve;exit6 做了:见 exit6.go)
//   - TCP 探测方式:面板入站回 200 只说明内核接了请求,不说明它连上了目标,单凭隧道没法确认,所以如实报「暂不支持」
//
// 做法与 sitelatency 相同(复用它的 DialTunnel):先向面板入站发 CONNECT 建隧道,连接开着的时候读一次 /connections,
// 按「来源 127.0.0.1:本地端口」找到自己那条。探测前先让内核丢掉 DNS 缓存(只是让缓存失效,不改配置),
// 这样看到的是此刻线路真的问出来的结果;清不掉也照常测。
package routetest

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/sitelatency"
)

const (
	// DefaultTimeout 是整次探测(建隧道 + 请求或握手)的超时。
	DefaultTimeout = 8 * time.Second

	maxTargetLen = 253
	maxBodyRead  = 64 << 10 // GET 读到 64 KiB 就够了,不用把整页拉完
)

// 前端 OpenboxProbeMethod 的四个取值。
const (
	MethodGET  = "GET"
	MethodHEAD = "HEAD"
	MethodTCP  = "TCP"
	MethodTLS  = "TLS"
)

// InputError 是请求的内容有问题(400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Input 是请求体。Port 为 0 表示没指定;Method 为空按 GET。
type Input struct {
	Target string `json:"target"`
	Port   int    `json:"port"`
	Method string `json:"method"`
}

// Exit 对应前端 OpenboxRouteTest.exit(只列这一部分实现了的字段)。
type Exit struct {
	URL              string   `json:"url"`
	OK               bool     `json:"ok"`
	Status           *int     `json:"status,omitempty"`
	MS               *int     `json:"ms,omitempty"`
	Error            string   `json:"error,omitempty"`
	Chains           []string `json:"chains,omitempty"`
	Rule             string   `json:"rule,omitempty"`
	RulePayload      string   `json:"rulePayload,omitempty"`
	DestinationIP    string   `json:"destinationIP,omitempty"`
	ViaProxy         *bool    `json:"viaProxy,omitempty"`
	Method           string   `json:"method,omitempty"`
	Challenge        string   `json:"challenge,omitempty"`
	NotSeen          bool     `json:"notSeen,omitempty"`
	ConnectionsError string   `json:"connectionsError,omitempty"`
}

// Result 对应前端 OpenboxRouteTest。DNS 是 {skipped:true} 或 {error:"..."},见包注释。
type Result struct {
	Target  string   `json:"target"`
	DNS     any      `json:"dns"`               // {skipped:true} / {error} / *dnsdecide.Decision
	Resolve *Resolve `json:"resolve,omitempty"` // 目标是域名时才有
	Exit6   *Exit6   `json:"exit6,omitempty"`   // 有 AAAA 答案时才有,见 exit6.go
	Exit    Exit     `json:"exit"`
}

// Service 做一次真实路由探测。
type Service struct {
	// Snapshot 读内核的连接快照;nil 或返回 error 时只是认不出线路(exit.connectionsError 说明原因)。
	Snapshot func(ctx context.Context) (singbox.KernelSnapshot, error)
	// FlushDNS 在探测前让内核丢掉 DNS 缓存;nil 或失败都只是不清,照常探测。
	FlushDNS func(ctx context.Context) error
	// ProxyAddr 是面板入站地址,空 = sitelatency.PanelProxyAddr。
	ProxyAddr string
	// DNS 预测响应里的 dns(按已部署配置里的 DNS 规则判断这个域名问哪台服务器);nil = 不预测,回「未接入」。
	DNS *DNSPredictor
	// DNSAddr 是内核的 DNS 入站地址,空 = system.DefaultKernelDNSAddr。
	DNSAddr string
	// IPv6 返回档案是否开了 IPv6(开了才再查一次 AAAA);nil 当作没开。
	IPv6 func(ctx context.Context) bool
	// Timeout 是整次探测的超时,0 = DefaultTimeout。
	Timeout time.Duration
	// TLSConfig 给 GET / HEAD / TLS 用,nil = 系统默认(只有测试会设)。
	TLSConfig *tls.Config
}

func (s *Service) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return DefaultTimeout
}

func (s *Service) proxyAddr() string {
	if s.ProxyAddr != "" {
		return s.ProxyAddr
	}
	return sitelatency.PanelProxyAddr
}

// Parse 校验并补默认值:目标只认字母、数字和 . _ : -(与前端 isValidPenetrationTarget 一致,不能以 - 开头),
// 端口 0 或 1–65535,方式是 GET / HEAD / TCP / TLS(空按 GET,大小写不分)。
func Parse(in Input) (Input, error) {
	in.Target = strings.TrimSpace(in.Target)
	if in.Target == "" {
		return in, &InputError{Msg: "请填写要测试的域名或 IP"}
	}
	if len(in.Target) > maxTargetLen {
		return in, &InputError{Msg: "目标太长"}
	}
	if !validTarget(in.Target) {
		return in, &InputError{Msg: "目标只能包含字母、数字和 . _ : -,且不能以 - 开头"}
	}
	if in.Port < 0 || in.Port > 65535 {
		return in, &InputError{Msg: "端口要在 1–65535 之间"}
	}
	switch m := strings.ToUpper(strings.TrimSpace(in.Method)); m {
	case "":
		in.Method = MethodGET
	case MethodGET, MethodHEAD, MethodTCP, MethodTLS:
		in.Method = m
	default:
		return in, &InputError{Msg: "探测方式只能是 GET、HEAD、TCP 或 TLS"}
	}
	return in, nil
}

func validTarget(v string) bool {
	if v == "" || v[0] == '-' {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == ':', r == '-':
		default:
			return false
		}
	}
	return true
}

func isIP(target string) bool {
	_, err := netip.ParseAddr(target)
	return err == nil
}

// ProbeURL 是这次探测的地址:没指定端口按 https;指定 80 用 http;其它端口一律 https(TLS 方式本来就是 https)。
// 前端把它显示成「方式 + 地址」。
func ProbeURL(target string, port int) string {
	scheme, host := "https", target
	if isIP(target) && strings.Contains(target, ":") {
		host = "[" + target + "]" // IPv6 字面量
	}
	if port == 80 {
		scheme = "http"
	}
	if port == 0 || (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		return scheme + "://" + host + "/"
	}
	return scheme + "://" + host + ":" + strconv.Itoa(port) + "/"
}

// Run 做一次探测。请求内容有问题返回 *InputError;探测本身失败(不通、超时)不算错,写在 exit.error 里。
func (s *Service) Run(ctx context.Context, in Input) (*Result, error) {
	in, err := Parse(in)
	if err != nil {
		return nil, err
	}
	res := &Result{Target: in.Target, DNS: s.predictDNS(ctx, in.Target)}
	res.Exit = Exit{URL: ProbeURL(in.Target, in.Port), Method: in.Method}

	if in.Method == MethodTCP {
		res.Exit.Error = "暂不支持 TCP 方式(只建隧道无法确认内核已连上目标),请改用 TLS 或 GET / HEAD"
		return res, nil
	}

	if s.FlushDNS != nil {
		fctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = s.FlushDNS(fctx)
		cancel()
	}

	// 目标是域名时,先经内核的 DNS 入站真问一次(AAAA 只在档案开了 IPv6 时才问)
	if !isIP(in.Target) {
		ipv6 := s.IPv6 != nil && s.IPv6(ctx)
		res.Resolve = s.resolve(ctx, in.Target, ipv6)
		annotateResolve(res.Resolve, res.DNS)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	pctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	var probed *probed
	if in.Method == MethodTLS {
		probed = s.probeTLS(pctx, in, "")
	} else {
		probed = s.probeHTTP(pctx, in, "")
	}
	defer probed.close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	res.Exit.OK, res.Exit.Status, res.Exit.MS, res.Exit.Challenge = probed.ok, probed.status, probed.ms, probed.challenge
	if probed.err != nil {
		res.Exit.Error = sitelatency.DescribeError(probed.err, s.timeout())
	} else {
		s.fillRoute(ctx, &res.Exit, probed.port)
	}
	// AAAA 再访问与上面主探测的成败无关:v4 不通时 v6 的结果恰恰是用户想看的
	res.Exit6 = s.probeExit6(ctx, in, res.Resolve)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return res, nil
}

func dnsPlaceholder(target string) map[string]any {
	if isIP(target) {
		return map[string]any{"skipped": true}
	}
	return map[string]any{"error": "DNS 判定暂未实现(这次只做了真实访问和线路)"}
}

type probed struct {
	ok        bool
	status    *int
	ms        *int
	challenge string
	port      string // 连到面板入站的本地端口,认线路用
	err       error
	closers   []func()
}

func (p *probed) close() {
	for _, f := range p.closers {
		f()
	}
}

func millis(d time.Duration) *int {
	v := max(int((d+500*time.Microsecond)/time.Millisecond), 1)
	return &v
}

// probeHTTP 发一次 GET 或 HEAD。证书校验照常(校验失败就是失败);3xx 不跟随,拿到什么状态码报什么。
// connectTo 非空时,隧道连的是这个 IP(端口不变),请求的 Host、TLS 的 SNI 和证书校验仍然用域名,像终端拿到 DNS 答案后那样。
func (s *Service) probeHTTP(ctx context.Context, in Input, connectTo string) *probed {
	p := &probed{}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			c, port, err := sitelatency.DialTunnel(ctx, s.proxyAddr(), s.timeout(), withConnectTo(addr, connectTo))
			if err == nil {
				p.port = port
			}
			return c, err
		},
		TLSClientConfig:     s.TLSConfig,
		TLSHandshakeTimeout: s.timeout(),
		DisableCompression:  true,
		DisableKeepAlives:   false, // 连接要在读连接表时还开着
	}
	p.closers = append(p.closers, tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	method := http.MethodGet
	if in.Method == MethodHEAD {
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(ctx, method, ProbeURL(in.Target, in.Port), nil)
	if err != nil {
		p.err = err
		return p
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenBox route-test)")
	start := time.Now()
	resp, err := client.Do(req)
	took := time.Since(start)
	if err != nil {
		p.err = err
		return p
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyRead)) // 读掉正文,连接才会留在空闲池里
	_ = resp.Body.Close()
	p.ok, p.ms = true, millis(took)
	st := resp.StatusCode
	p.status = &st
	// Cloudflare 的人机验证:请求本身是通的,403 / 503 不是线路问题。官方用响应头 cf-mitigated: challenge 标明。
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("cf-mitigated")), "challenge") {
		p.challenge = "cloudflare"
	}
	return p
}

// probeTLS 建隧道后做一次 TLS 握手,耗时是「建隧道 + 握手」。目标是 IP 时证书通常对不上,所以不校验证书
// (只测连通与握手);目标是域名时照常校验。
// connectTo 的含义同 probeHTTP:只换连接的地址,SNI 和证书校验仍用域名。
func (s *Service) probeTLS(ctx context.Context, in Input, connectTo string) *probed {
	p := &probed{}
	port := in.Port
	if port == 0 {
		port = 443
	}
	addr := withConnectTo(net.JoinHostPort(in.Target, strconv.Itoa(port)), connectTo)
	start := time.Now()
	c, lport, err := sitelatency.DialTunnel(ctx, s.proxyAddr(), s.timeout(), addr)
	if err != nil {
		p.err = err
		return p
	}
	p.port = lport
	p.closers = append(p.closers, func() { _ = c.Close() })
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.TLSConfig != nil {
		cfg = s.TLSConfig.Clone()
	}
	if cfg.ServerName == "" {
		cfg.ServerName = in.Target // 目标是 IP 时 Go 比对证书里的 IP 项
	}
	if isIP(in.Target) && s.TLSConfig == nil {
		cfg.InsecureSkipVerify = true // 默认配置下 IP 目标的证书通常对不上;这里只测连通和握手
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	tc := tls.Client(c, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		p.err = err
		return p
	}
	_ = c.SetDeadline(time.Time{})
	p.ok, p.ms = true, millis(time.Since(start))
	return p
}

// fillRoute 读一次连接快照,按本地端口认出这次探测的那条连接,填线路、规则、目标 IP。认不出不算失败。
func (s *Service) fillRoute(ctx context.Context, ex *Exit, port string) {
	if s.Snapshot == nil || port == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	snap, err := s.Snapshot(cctx)
	if err != nil {
		ex.ConnectionsError = err.Error()
		return
	}
	var found *singbox.KernelConn
	for i := range snap.Conns {
		c := &snap.Conns[i]
		if c.SourcePort == port && isLoopback(c.Source) {
			found = c // 同一端口只会有一条;万一重复取最后一条
		}
	}
	if found == nil || len(found.Chains) == 0 {
		ex.NotSeen = true
		return
	}
	chains := make([]string, 0, len(found.Chains))
	for i := len(found.Chains) - 1; i >= 0; i-- { // Clash API 的链出口在前、路由选中的在最后;前端要的是反过来
		chains = append(chains, found.Chains[i])
	}
	ex.Chains, ex.Rule, ex.RulePayload, ex.DestinationIP = chains, found.Rule, found.RulePayload, found.DestIP
	leaf := chains[len(chains)-1]
	via := !contains(snap.DirectTags, leaf)
	ex.ViaProxy = &via
}

// withConnectTo 把 host:port 里的主机换成 ip(端口不变);ip 为空原样返回。
func withConnectTo(addr, ip string) string {
	if ip == "" {
		return addr
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return net.JoinHostPort(ip, port)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func isLoopback(ip string) bool {
	a := net.ParseIP(ip)
	return a != nil && a.IsLoopback()
}
