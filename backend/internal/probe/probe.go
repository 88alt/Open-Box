// Package probe 做节点测速(M1)。
//
// 做法:从完整配置里摘出被测节点(及它依赖的出站 / DNS 服务器)拼成一份很小的配置,
// 起一个临时的 sing-box 进程,里面只有一个仅监听 127.0.0.1 的 http 入站,每个任务一个用户名,
// 路由规则按用户名把连接送到对应出站;然后由本进程通过这个入站向测速地址发 HTTPS 请求并自己计时。
// 好处:一个进程测一批节点;计时不含 sing-box 的启动;错误(超时、连不上、握手失败)都能如实分类。
// 临时进程不碰系统(没有 tun、不改 DNS / 防火墙),结束即杀,配置文件 0600 用完删除。
package probe

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTimeout 是单个任务的默认等待上限。
	DefaultTimeout = 5 * time.Second
	// MaxTimeout 是客户端能要求的最长等待。
	MaxTimeout = 30 * time.Second
	// MaxJobs 是一次最多测多少个。
	MaxJobs = 500
	// Concurrency 是同时在测的任务数(路由器上不宜太大)。
	Concurrency = 16

	startupWait = 8 * time.Second
	maxStderr   = 4 << 10
	dnsTag      = "ob-probe-local"
)

// ErrNoSingBox:找不到 sing-box 可执行文件。
var ErrNoSingBox = errors.New("找不到 sing-box 可执行文件")

// Result 与前端 OpenboxLatencyResult 一致。
type Result struct {
	OK     bool   `json:"ok"`
	Ms     int    `json:"ms,omitempty"`
	Error  string `json:"error,omitempty"`
	Reason string `json:"reason,omitempty"` // invalid / not-found / timeout / refused / failed

	// 以下两项只在取回正文的任务(Job.Capture > 0)成功时才有,不进 JSON:由调用方自己决定怎么交给前端。
	URL  string `json:"-"` // 实际答话的地址(回退时可能不是 Job.URL)
	Body string `json:"-"` // 响应正文,最多 Job.Capture 字节,已规整成合法 UTF-8
}

// Job 是一次测速任务。
type Job struct {
	Tag string // 要测的出站
	URL string // 测速地址(http / https)

	// Capture > 0 时,任务改为「取回正文」:只认 2xx(3xx 不跟随,按失败算),最多读 Capture 字节放进 Result.Body。
	// URL 失败后按 Fallback 的顺序逐个再试,第一个成功的为准;全部失败时 Error 里按「主机: 原因」列出每一家。
	// 每一次尝试各有一份完整的超时。用于「经这条线路看出口 IP」,不是测速,Ms 只是这次请求的耗时。
	Capture  int
	Fallback []string
}

// Material 是从完整配置里摘出的、测速要用的部分。
type Material struct {
	Outbounds []any
	Endpoints []any
	DNS       []any // 被引用的 DNS 服务器
	have      map[string]string
}

// Has 报告 tag 是不是一个能被测的出站(节点或链式节点;组不算)。
func (m *Material) Has(tag string) bool {
	kind, ok := m.have[tag]
	return ok && !isGroup(kind)
}

func isGroup(t string) bool { return t == "selector" || t == "urltest" }

// Subset 从完整配置 cfg 里摘出 tags 以及它们依赖的东西:detour、组成员、domain_resolver 指向的 DNS 服务器。
// 找不到或属于组的 tag 不会出错,调用方用 Has 判断。
func Subset(cfg map[string]any, tags []string) *Material {
	out := map[string]map[string]any{} // 出站 / endpoint 按 tag
	kinds := map[string]string{}       // tag → type
	isEndpoint := map[string]bool{}
	for _, k := range []string{"outbounds", "endpoints"} {
		list, _ := cfg[k].([]any)
		for _, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			tag, _ := m["tag"].(string)
			typ, _ := m["type"].(string)
			if tag == "" {
				continue
			}
			out[tag], kinds[tag] = m, typ
			isEndpoint[tag] = k == "endpoints"
		}
	}
	dnsByTag := map[string]map[string]any{}
	if d, ok := cfg["dns"].(map[string]any); ok {
		list, _ := d["servers"].([]any)
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				if tag, _ := m["tag"].(string); tag != "" {
					dnsByTag[tag] = m
				}
			}
		}
	}

	m := &Material{have: kinds}
	seenOut, seenDNS := map[string]bool{}, map[string]bool{}
	var visitOut func(tag string)
	var visitDNS func(tag string)
	visitRefs := func(e map[string]any) {
		if d, ok := e["detour"].(string); ok && d != "" {
			visitOut(d)
		}
		for _, mem := range strList(e["outbounds"]) {
			visitOut(mem)
		}
		switch dr := e["domain_resolver"].(type) {
		case string:
			visitDNS(dr)
		case map[string]any:
			if s, _ := dr["server"].(string); s != "" {
				visitDNS(s)
			}
		}
	}
	visitOut = func(tag string) {
		e, ok := out[tag]
		if !ok || seenOut[tag] {
			return
		}
		seenOut[tag] = true
		if isEndpoint[tag] {
			m.Endpoints = append(m.Endpoints, e)
		} else {
			m.Outbounds = append(m.Outbounds, e)
		}
		visitRefs(e)
	}
	visitDNS = func(tag string) {
		e, ok := dnsByTag[tag]
		if !ok || seenDNS[tag] {
			return
		}
		seenDNS[tag] = true
		m.DNS = append(m.DNS, e)
		visitRefs(e)
	}
	for _, t := range tags {
		visitOut(t)
	}
	return m
}

func strList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Runner 运行测速。
type Runner struct {
	Bin string // sing-box 可执行文件
	Dir string // 放临时配置的目录(0700)
}

// Options 是一次运行的参数。
type Options struct {
	Timeout time.Duration
	// OnDone 每测完一个任务调用一次(下标是 jobs 的下标),可能被并发调用,实现要自己加锁。
	OnDone func(index int)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// buildConfig 生成临时配置;users[i] 对应 jobs[i]。
func buildConfig(m *Material, jobs []Job, users []string, port int) ([]byte, error) {
	rules := make([]any, 0, len(jobs))
	userTags := map[string]bool{}
	var httpUsers []any
	for i, j := range jobs {
		if !m.Has(j.Tag) || userTags[users[i]] {
			continue
		}
		userTags[users[i]] = true
		httpUsers = append(httpUsers, map[string]any{"username": users[i], "password": users[i]})
		rules = append(rules, map[string]any{"auth_user": []any{users[i]}, "outbound": j.Tag})
	}
	servers := append([]any{map[string]any{"type": "local", "tag": dnsTag}}, m.DNS...)
	cfg := map[string]any{
		"log": map[string]any{"level": "error"},
		"dns": map[string]any{"servers": servers, "final": dnsTag},
		"inbounds": []any{map[string]any{
			"type": "http", "tag": "ob-probe-in", "listen": "127.0.0.1", "listen_port": port, "users": httpUsers,
		}},
		"outbounds": m.Outbounds,
		"route":     map[string]any{"rules": rules, "default_domain_resolver": dnsTag},
	}
	if len(m.Endpoints) > 0 {
		cfg["endpoints"] = m.Endpoints
	}
	return json.Marshal(cfg)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Run 测 jobs,返回的结果与 jobs 一一对应。
// 不是节点的任务(组、已不存在)得到 not-found;配置不合法(sing-box 启动失败)时,
// 本批所有可测任务得到 invalid,并带 sing-box 的第一行报错。
func (r *Runner) Run(ctx context.Context, m *Material, jobs []Job, opt Options) ([]Result, error) {
	if len(jobs) > MaxJobs {
		return nil, fmt.Errorf("一次最多测 %d 个", MaxJobs)
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	results := make([]Result, len(jobs))
	users := make([]string, len(jobs))
	testable := 0
	for i, j := range jobs {
		users[i] = "j" + randHex(6)
		switch {
		case !validTestURL(j.URL):
			results[i] = Result{Error: "测速地址不合法", Reason: "invalid"}
			if opt.OnDone != nil {
				opt.OnDone(i)
			}
		case !m.Has(j.Tag):
			results[i] = Result{Error: "找不到这个节点", Reason: "not-found"}
			if opt.OnDone != nil {
				opt.OnDone(i)
			}
		default:
			testable++
		}
	}
	if testable == 0 {
		return results, nil
	}

	bin, err := exec.LookPath(r.Bin)
	if err != nil {
		return nil, ErrNoSingBox
	}
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("找不到空闲端口: %w", err)
	}
	// 只带可测任务的用户;不可测的在 buildConfig 里被跳过
	valid := make([]Job, len(jobs))
	copy(valid, jobs)
	for i := range valid {
		if results[i].Reason != "" {
			valid[i].Tag = "" // Has("") 为假,跳过
		}
	}
	cfg, err := buildConfig(m, valid, users, port)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(r.Dir, "probe-*.json")
	if err != nil {
		return nil, fmt.Errorf("创建临时配置失败: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Write(cfg); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(pctx, bin, "run", "-c", name)
	errOut := &tailBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = errOut, errOut
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 sing-box 失败: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() { cancel(); <-exited }()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitListening(ctx, addr, exited, startupWait); err != nil {
		msg := firstLine(strings.ReplaceAll(errOut.String(), name, "config.json"))
		if msg == "" {
			msg = err.Error()
		}
		for i := range results {
			if results[i].Reason == "" {
				results[i] = Result{Error: msg, Reason: "invalid"}
				if opt.OnDone != nil {
					opt.OnDone(i)
				}
			}
		}
		return results, nil
	}

	sem := make(chan struct{}, Concurrency)
	var wg sync.WaitGroup
	for i := range jobs {
		if results[i].Reason != "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = runJob(ctx, addr, users[i], jobs[i], timeout)
			if opt.OnDone != nil {
				opt.OnDone(i)
			}
		}(i)
	}
	wg.Wait()
	return results, ctx.Err()
}

// runJob 跑一个任务:普通测速只请求一次;取回正文的任务按 URL、Fallback 的顺序试到第一个成功。
func runJob(ctx context.Context, proxyAddr, user string, j Job, timeout time.Duration) Result {
	if j.Capture <= 0 {
		return fetch(ctx, proxyAddr, user, j.URL, timeout, 0)
	}
	urls := []string{j.URL}
	for _, f := range j.Fallback {
		if validTestURL(f) {
			urls = append(urls, f)
		}
	}
	var fails []string
	var last Result
	for _, u := range urls {
		r := fetch(ctx, proxyAddr, user, u, timeout, j.Capture)
		if r.OK {
			r.URL = u
			return r
		}
		last = r
		fails = append(fails, hostOf(u)+": "+r.Error)
		if ctx.Err() != nil {
			break
		}
	}
	if len(fails) > 1 {
		last.Error = strings.Join(fails, ";")
	}
	return last
}

func hostOf(s string) string {
	if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return "?"
}

func validTestURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func waitListening(ctx context.Context, addr string, exited <-chan struct{}, max time.Duration) error {
	deadline := time.NewTimer(max)
	defer deadline.Stop()
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return nil
		}
		select {
		case <-exited:
			return errors.New("sing-box 启动后立即退出")
		case <-deadline.C:
			return errors.New("sing-box 启动超时")
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// connectDial 经 http 入站建一条 CONNECT 隧道到 target(host:port)。
// 一律走 CONNECT(包括 http 目标):入站在出站失败时会自己回 502 之类的状态,
// 走普通代理请求的话无法和「目标站点真的回了 502」区分;CONNECT 不是 200 就一定是线路没通。
func connectDial(ctx context.Context, proxyAddr, user, target string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + user))
	if _, err := fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		c.Close()
		return nil, fmt.Errorf("线路不通(%s)", strings.TrimSpace(strings.TrimPrefix(resp.Status, fmt.Sprint(resp.StatusCode))))
	}
	_ = c.SetDeadline(time.Time{})
	if br.Buffered() > 0 { // 入站不会抢跑发数据;保险起见把已读的部分接回去
		return &bufConn{Conn: c, r: br}, nil
	}
	return c, nil
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// fetch 经临时入站(用户名 = 任务)请求 target。
// capture == 0:返回从发起请求到收到响应头的耗时,不看状态码(204 / 404 都算线路通)。
// capture > 0:要求 2xx,并读回最多 capture 字节正文;耗时算到正文读完。
// 证书不校验:测的是线路是否通,不是目标站点的证书(与节点出站的做法一致;路由器上常常没有根证书包)。
func fetch(ctx context.Context, proxyAddr, user, target string, timeout time.Duration, capture int) Result {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		return Result{Error: "测速地址不合法", Reason: "invalid"}
	}
	req.Header.Set("User-Agent", "openbox-probe/1")
	tr := &http.Transport{
		DisableKeepAlives:     true,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		ResponseHeaderTimeout: timeout,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return connectDial(ctx, proxyAddr, user, addr)
		},
	}
	defer tr.CloseIdleConnections()
	start := time.Now()
	resp, err := (&http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return classify(err, cctx)
	}
	defer resp.Body.Close()
	if capture <= 0 {
		elapsed := time.Since(start)
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return Result{OK: true, Ms: msOf(elapsed)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{Error: fmt.Sprintf("HTTP %d", resp.StatusCode), Reason: "failed"}
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, int64(capture)))
	if err != nil {
		return classify(err, cctx)
	}
	if len(buf) == 0 {
		return Result{Error: "响应是空的", Reason: "failed"}
	}
	return Result{OK: true, Ms: msOf(time.Since(start)), Body: strings.ToValidUTF8(string(buf), "\uFFFD")}
}

func msOf(d time.Duration) int {
	if ms := int(d.Milliseconds()); ms > 1 {
		return ms
	}
	return 1
}

func classify(err error, ctx context.Context) Result {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded || isTimeout(err):
		return Result{Error: "超时", Reason: "timeout"}
	case strings.Contains(msg, "connection refused"):
		return Result{Error: "连接被拒绝", Reason: "refused"}
	}
	// 代理入站把出站的失败回成 502 / 拒绝,错误文本里带有原因;不外传完整 URL 和凭据
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return Result{Error: msg, Reason: "failed"}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func firstLine(s string) string {
	s = ansi.ReplaceAllString(s, "")
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// tailBuffer 只留最后 max 字节。
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}
