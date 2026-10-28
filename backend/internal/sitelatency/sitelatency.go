// Package sitelatency 实现概览页的「站点延迟」:让请求经过内核的路由(面板入站)去访问几个站点,
// 量出连接建好之后的往返耗时,并从内核的连接列表里认出这条请求实际走了哪条线路(站点集 → 组 → 节点)。
// 自己的设计,依据前端 api/openbox.ts 的 OpenboxSite* 类型、composables/siteLatency.ts 和
// SiteLatencyTiles.vue 的用法,以及 core 的面板入站(127.0.0.1:7891,混合入站),没有参照上游。
//
// 做法:对每个站点各开一个只走内核面板入站的 HTTP 客户端,而且不管 http 还是 https 都先向面板入站发
// CONNECT 建一条隧道再在里面说话(http 站点如果走普通代理请求,内核每个请求结束就关掉出站连接,
// 连接列表里常常已经没有它了;隧道开着,连接就一直在)。第一次请求的总耗时是 openMs(含解析、
// 连接、握手);紧接着在同一条连接上再请求一次,这次的耗时是 ms(连接建好之后的一次往返);
// 连接保持着的时候读一次内核的 /connections,按「来源 127.0.0.1:本地端口」找到自己那条,
// 取出线路。线路读不到(内核没开控制接口、连接已被回收)不算失败,只是 via 为空。
package sitelatency

import (
	"bufio"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/latencyhistory"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

const (
	// MaxSites 一次最多测几个站点(前端「测试站点」最多 8 个)。
	MaxSites = 8
	// DefaultTimeout 是单次请求的超时,也是前端「不通」柱子的高度依据。
	DefaultTimeout = 5 * time.Second
	// PanelProxyAddr 是 core 生成的配置里面板入站(混合入站)的地址,必须与 core 的 PanelInboundPort 保持一致。
	PanelProxyAddr = "127.0.0.1:7891"

	maxURLLen   = 2048
	maxBodyRead = 1 << 20 // 响应正文最多读 1 MiB 就不读了(读不完这条连接就不能复用)
	chainSep    = " → "
)

var idRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// Site 是要测的一个站点。
type Site struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// InputError 是请求的内容有问题(400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Result 对应前端 OpenboxSiteLatencyResult。
type Result struct {
	ID     string   `json:"id"`
	URL    string   `json:"url"`
	Via    *string  `json:"via"`   // 线路的第一跳(路由选中的站点集 / 组),没认出来是 null
	Chain  []string `json:"chain"` // 完整线路:站点集 → 组 → 节点;没认出来是空数组
	MS     *int     `json:"ms"`    // 连接建好之后一次请求往返的毫秒数;不通是 null
	OpenMS *int     `json:"openMs"`
	Error  *string  `json:"error"`
}

// Run 对应前端 OpenboxSiteLatencyRun。
type Run struct {
	Sites     []Result               `json:"sites"`
	TestedAt  int64                  `json:"testedAt"`
	History   latencyhistory.History `json:"history"`
	TimeoutMS int                    `json:"timeoutMs"`
}

// History 是 GET history 的响应。
type History struct {
	History   latencyhistory.History `json:"history"`
	TimeoutMS int                    `json:"timeoutMs"`
}

// Service 测站点延迟并记历史。
type Service struct {
	// Hist 存站点历史(要用单独的 File,和节点历史分开)。
	Hist *latencyhistory.Service
	// Snapshot 读内核的连接快照;nil 或返回 error 时只是认不出线路。
	Snapshot func(ctx context.Context) (singbox.KernelSnapshot, error)
	// ProxyAddr 是面板入站地址,空 = PanelProxyAddr。
	ProxyAddr string
	// Timeout 是单次请求的超时,0 = DefaultTimeout。
	Timeout time.Duration
	Now     func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
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
	return PanelProxyAddr
}

// History 返回站点历史和「不通」柱子的超时时长。
func (s *Service) History(ctx context.Context) (*History, error) {
	h, _, err := s.Hist.Get(ctx)
	if err != nil {
		return nil, err
	}
	if h == nil {
		h = latencyhistory.History{}
	}
	return &History{History: h, TimeoutMS: int(s.timeout() / time.Millisecond)}, nil
}

// ParseSites 校验请求里的站点表:最多 8 个,id 只认小写字母数字和横线且不重复,地址是 http(s) 且不带账号密码。
// 任何一条不合法整批拒绝。id 和地址原样保留(前端靠它们对上结果)。
func ParseSites(in []Site) ([]Site, error) {
	if len(in) == 0 {
		return nil, &InputError{Msg: "没有要测的站点"}
	}
	if len(in) > MaxSites {
		return nil, &InputError{Msg: fmt.Sprintf("一次最多测 %d 个站点", MaxSites)}
	}
	seen := map[string]bool{}
	for i, x := range in {
		if !idRe.MatchString(x.ID) {
			return nil, &InputError{Msg: fmt.Sprintf("第 %d 个站点的 id 不合法(只能是小写字母、数字和横线,最长 32 个字符)", i+1)}
		}
		if seen[x.ID] {
			return nil, &InputError{Msg: "站点 id 重复:" + x.ID}
		}
		seen[x.ID] = true
		if err := checkURL(strings.TrimSpace(x.URL)); err != nil {
			return nil, &InputError{Msg: fmt.Sprintf("第 %d 个站点的地址不合法:%s", i+1, err)}
		}
	}
	return in, nil
}

func checkURL(s string) error {
	if s == "" {
		return errors.New("不能为空")
	}
	if len(s) > maxURLLen {
		return fmt.Errorf("太长(最多 %d 个字符)", maxURLLen)
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return errors.New("不是有效的网址")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("只支持 http / https")
	}
	if u.User != nil {
		return errors.New("不要在网址里带账号密码")
	}
	return nil
}

type probe struct {
	res  Result
	port string // 连到面板入站的本地端口;没连上是空
	tr   *http.Transport
}

// Run 测一轮:所有站点同时测,测完读一次内核连接认线路,记进历史,返回结果。
func (s *Service) Run(ctx context.Context, sites []Site) (*Run, error) {
	sites, err := ParseSites(sites)
	if err != nil {
		return nil, err
	}
	at := s.now()
	probes := make([]*probe, len(sites))
	var wg sync.WaitGroup
	for i := range sites {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			probes[i] = s.measure(ctx, sites[i])
		}(i)
	}
	wg.Wait()
	defer func() {
		for _, p := range probes {
			p.tr.CloseIdleConnections()
		}
	}()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.fillRoutes(ctx, probes)

	run := &Run{Sites: make([]Result, len(probes)), TestedAt: at.UnixMilli(), TimeoutMS: int(s.timeout() / time.Millisecond)}
	items := make([]latencyhistory.Item, 0, len(probes))
	stamp := at.UTC().Format(time.RFC3339Nano)
	for i, p := range probes {
		run.Sites[i] = p.res
		delay := 0 // 不通记 0(前端按超时时长画柱子)
		if p.res.MS != nil {
			delay = max(*p.res.MS, 1)
		}
		items = append(items, latencyhistory.Item{
			Name:   p.res.ID,
			Sample: latencyhistory.Sample{Time: stamp, Delay: delay, Node: strings.Join(p.res.Chain, chainSep)},
		})
	}
	hist, err := s.Hist.Record(ctx, items)
	if err != nil {
		return nil, err
	}
	run.History = hist
	return run, nil
}

// measure 测一个站点:第一次请求量 openMs,同一条连接上第二次请求量 ms。
func (s *Service) measure(ctx context.Context, site Site) *probe {
	p := &probe{res: Result{ID: site.ID, URL: site.URL, Chain: []string{}}}
	var mu sync.Mutex
	p.tr = &http.Transport{
		// 没有 Proxy:隧道由下面的 DialContext 自己建(对 http 和 https 一视同仁)
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, port, err := s.dialTunnel(ctx, addr)
			if err == nil {
				mu.Lock()
				p.port = port
				mu.Unlock()
			}
			return c, err
		},
		TLSHandshakeTimeout: s.timeout(),
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
	}
	client := &http.Client{
		Transport:     p.tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	do := func() (time.Duration, error) {
		cctx, cancel := context.WithTimeout(ctx, s.timeout())
		defer cancel()
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, strings.TrimSpace(site.URL), nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenBox site-latency)")
		start := time.Now()
		resp, err := client.Do(req)
		took := time.Since(start)
		if err != nil {
			return 0, err
		}
		// 把正文读完(有上限),这条连接才能给第二次请求复用
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyRead))
		resp.Body.Close()
		return took, nil
	}

	open, err := do()
	if err != nil {
		msg := s.describe(err)
		p.res.Error = &msg
		return p
	}
	openMS := ms(open)
	p.res.OpenMS = &openMS
	// 第二次请求:连接复用时才是「连接建好之后的往返」;复用不了(服务器关了连接、正文太大)就用第一次的耗时
	second, err := do()
	if err == nil {
		v := ms(second)
		p.res.MS = &v
	} else {
		p.res.MS = &openMS
	}
	return p
}

// fillRoutes 读一次内核连接快照,给每个量成功的站点认出线路。
func (s *Service) fillRoutes(ctx context.Context, probes []*probe) {
	if s.Snapshot == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	snap, err := s.Snapshot(cctx)
	if err != nil {
		return
	}
	for _, p := range probes {
		if p.res.Error != nil || p.port == "" {
			continue
		}
		var found *singbox.KernelConn
		for i := range snap.Conns {
			c := &snap.Conns[i]
			if c.SourcePort == p.port && isLoopback(c.Source) {
				found = c // 同一端口只会有一条;万一重复取最后一条
			}
		}
		if found == nil || len(found.Chains) == 0 {
			continue
		}
		chain := make([]string, 0, len(found.Chains))
		for i := len(found.Chains) - 1; i >= 0; i-- { // Clash API 的链是出口在前、路由选中的在最后;前端要的是反过来
			chain = append(chain, found.Chains[i])
		}
		via := chain[0]
		p.res.Via, p.res.Chain = &via, chain
	}
}

func isLoopback(ip string) bool {
	a := net.ParseIP(ip)
	return a != nil && a.IsLoopback()
}

func ms(d time.Duration) int {
	v := int((d + 500*time.Microsecond) / time.Millisecond) // 四舍五入
	return max(v, 1)
}

// describe 把请求错误翻成给人看的中文;不带网址(网址里可能有 token)。
func (s *Service) describe(err error) string {
	var pe *proxyError
	if errors.As(err, &pe) {
		return pe.msg
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "超时(" + strconv.FormatFloat(s.timeout().Seconds(), 'f', -1, 64) + " 秒内没有响应)"
	case errors.Is(err, context.Canceled):
		return "已取消"
	}
	var ce x509.CertificateInvalidError
	var ua x509.UnknownAuthorityError
	var he x509.HostnameError
	if errors.As(err, &ce) || errors.As(err, &ua) || errors.As(err, &he) {
		return "证书校验失败"
	}
	msg := err.Error()
	switch {
	case errors.Is(err, io.EOF) || strings.Contains(msg, "EOF"):
		return "连接被中断"
	case strings.Contains(msg, "connection refused"):
		return "对方拒绝连接"
	case strings.Contains(msg, "no such host"):
		return "域名解析失败"
	}
	r := []rune(msg)
	if len(r) > 100 {
		msg = string(r[:100]) + "…"
	}
	return "请求失败:" + msg
}

// proxyError 是和内核面板入站打交道时出的错(连不上、拒绝建隧道),错误文本已经是给人看的中文。
type proxyError struct{ msg string }

func (e *proxyError) Error() string { return e.msg }

// dialTunnel 连到面板入站,发 CONNECT addr,收到 200 后把这条连接交出去;同时返回本地端口(用来认线路)。
func (s *Service) dialTunnel(ctx context.Context, addr string) (net.Conn, string, error) {
	d := &net.Dialer{Timeout: s.timeout()}
	c, err := d.DialContext(ctx, "tcp", s.proxyAddr())
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", err
		}
		return nil, "", &proxyError{msg: "连不上内核的本地代理入口(内核没在运行?)"}
	}
	port := ""
	if ta, ok := c.LocalAddr().(*net.TCPAddr); ok {
		port = strconv.Itoa(ta.Port)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if err := req.Write(c); err != nil {
		c.Close()
		return nil, "", &proxyError{msg: "和内核的本地代理入口通信失败"}
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		c.Close()
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", &proxyError{msg: "内核的本地代理入口没有正常答复"}
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Close()
		return nil, "", &proxyError{msg: fmt.Sprintf("内核拒绝了这个连接(HTTP %d)", resp.StatusCode)}
	}
	_ = c.SetDeadline(time.Time{})
	return &bufConn{Conn: c, r: br}, port, nil
}

// bufConn 让已经被 bufio 多读进去的字节(CONNECT 答复后紧跟的数据)不丢。
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }
