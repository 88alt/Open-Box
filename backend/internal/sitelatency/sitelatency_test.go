package sitelatency

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/latencyhistory"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func TestParseSites(t *testing.T) {
	ok := []Site{{ID: "baidu", URL: "https://www.baidu.com/favicon.ico"}, {ID: "a-1", URL: " http://x.test/ "}}
	if got, err := ParseSites(ok); err != nil || got[1].URL != " http://x.test/ " {
		t.Fatalf("%v %v", got, err) // 原样保留,前端靠它对结果
	}
	var nine []Site
	for i := 0; i < 9; i++ {
		nine = append(nine, Site{ID: "s" + string(rune('a'+i)), URL: "https://a.test/"})
	}
	bad := map[string][]Site{
		"空":       nil,
		"太多":      nine,
		"id 大写":   {{ID: "Baidu", URL: "https://a.test/"}},
		"id 空":    {{ID: "", URL: "https://a.test/"}},
		"id 太长":   {{ID: strings.Repeat("a", 33), URL: "https://a.test/"}},
		"id 带下划线": {{ID: "a_b", URL: "https://a.test/"}},
		"id 重复":   {{ID: "a", URL: "https://a.test/"}, {ID: "a", URL: "https://b.test/"}},
		"地址空":     {{ID: "a", URL: " "}},
		"地址 ftp":  {{ID: "a", URL: "ftp://a.test/"}},
		"地址无主机":   {{ID: "a", URL: "https:///x"}},
		"地址带账号":   {{ID: "a", URL: "https://u:p@a.test/"}},
		"地址太长":    {{ID: "a", URL: "https://a.test/" + strings.Repeat("x", 2100)}},
		"地址不是网址":  {{ID: "a", URL: "not a url"}},
	}
	for name, in := range bad {
		_, err := ParseSites(in)
		var ie *InputError
		if !errors.As(err, &ie) {
			t.Errorf("%s:应该是 InputError,得到 %v", name, err)
		}
	}
}

// fakeProxy 是个假的内核面板入站:只认 CONNECT,回 200 后在隧道里当一个最小的 HTTP 源站
// (对每个请求回 204)。记下每个请求的来源地址;origin 决定每个目标主机的行为(延迟 / 直接断开)。
type fakeProxy struct {
	ln     net.Listener
	origin func(host string) (delay time.Duration, drop bool)
	refuse int // 非 0:对 CONNECT 回这个状态码
	mu     sync.Mutex
	conns  map[string][]string // 目标主机(不含端口)→ 来源地址(每个请求一条)
}

func newFakeProxy(origin func(host string) (time.Duration, bool)) *fakeProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	fp := &fakeProxy{ln: ln, origin: origin, conns: map[string][]string{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go fp.serve(c)
		}
	}()
	return fp
}

func (fp *fakeProxy) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		_, _ = c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}
	host, _, _ := net.SplitHostPort(req.Host)
	if fp.refuse != 0 {
		_, _ = fmt.Fprintf(c, "HTTP/1.1 %d X\r\nContent-Length: 0\r\n\r\n", fp.refuse)
		return
	}
	_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	for {
		if _, err := http.ReadRequest(br); err != nil {
			return
		}
		fp.mu.Lock()
		fp.conns[host] = append(fp.conns[host], c.RemoteAddr().String())
		fp.mu.Unlock()
		delay, drop := time.Duration(0), false
		if fp.origin != nil {
			delay, drop = fp.origin(host)
		}
		if drop {
			return
		}
		time.Sleep(delay)
		if _, err := c.Write([]byte("HTTP/1.1 204 No Content\r\n\r\n")); err != nil {
			return
		}
	}
}

func (fp *fakeProxy) addr() string { return fp.ln.Addr().String() }
func (fp *fakeProxy) Close()       { fp.ln.Close() }

// snapshot 造一份内核连接快照:每个目标主机一条连接,来源端口取自它收到的请求,线路由 chains 给。
func (fp *fakeProxy) snapshot(chains map[string][]string) func(context.Context) (singbox.KernelSnapshot, error) {
	return func(context.Context) (singbox.KernelSnapshot, error) {
		fp.mu.Lock()
		defer fp.mu.Unlock()
		var snap singbox.KernelSnapshot
		for host, from := range fp.conns {
			ip, port, _ := net.SplitHostPort(from[len(from)-1])
			snap.Conns = append(snap.Conns, singbox.KernelConn{ID: host, Source: ip, SourcePort: port, Host: host, Chains: chains[host]})
		}
		// 再放两条不相干的,认线路时不能认错
		snap.Conns = append(snap.Conns,
			singbox.KernelConn{ID: "x1", Source: "192.168.1.5", SourcePort: "1", Chains: []string{"别人"}},
			singbox.KernelConn{ID: "x2", Source: "127.0.0.1", SourcePort: "2", Chains: []string{"别的"}})
		return snap, nil
	}
}

func newService(t *testing.T, fp *fakeProxy, snap func(context.Context) (singbox.KernelSnapshot, error)) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		Hist:      &latencyhistory.Service{Store: st, File: "site-latency-history"},
		Snapshot:  snap,
		ProxyAddr: fp.addr(),
		Timeout:   time.Second,
		Now:       func() time.Time { return time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC) },
	}
}

func TestRunMeasuresReusesConnectionAndFindsRoute(t *testing.T) {
	fp := newFakeProxy(func(string) (time.Duration, bool) { return 20 * time.Millisecond, false })
	defer fp.Close()
	svc := newService(t, fp, fp.snapshot(map[string][]string{
		"a.test": {"HK-01", "自动选择", "国外站点"},
		"b.test": {"直连"},
	}))
	run, err := svc.Run(context.Background(), []Site{{ID: "a", URL: "http://a.test/ping"}, {ID: "b", URL: "http://b.test/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Sites) != 2 || run.TestedAt != time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC).UnixMilli() || run.TimeoutMS != 1000 {
		t.Fatalf("%+v", run)
	}
	a, b := run.Sites[0], run.Sites[1]
	if a.ID != "a" || a.URL != "http://a.test/ping" || a.Error != nil || a.MS == nil || a.OpenMS == nil || *a.MS < 15 || *a.OpenMS < 15 {
		t.Fatalf("%+v", a)
	}
	if a.Via == nil || *a.Via != "国外站点" || strings.Join(a.Chain, "|") != "国外站点|自动选择|HK-01" {
		t.Fatalf("线路要反过来(站点集 → 组 → 节点):%+v %v", a.Via, a.Chain)
	}
	if b.Via == nil || *b.Via != "直连" || len(b.Chain) != 1 {
		t.Fatalf("%+v %v", b.Via, b.Chain)
	}
	// 同一个站点两次请求来自同一条连接(第二次复用)
	fp.mu.Lock()
	got := fp.conns["a.test"]
	fp.mu.Unlock()
	if len(got) != 2 || got[0] != got[1] {
		t.Fatalf("第二次请求应复用同一条连接:%v", got)
	}
	// 历史:每个站点一条,线路用 " → " 连起来
	h := run.History["a"]
	if len(h) != 1 || h[0].Delay != *a.MS || h[0].Node != "国外站点 → 自动选择 → HK-01" || h[0].Time != "2026-10-28T08:00:00Z" {
		t.Fatalf("%+v", h)
	}
	if run.History["b"][0].Node != "直连" {
		t.Fatalf("%+v", run.History["b"])
	}
	// GET history 读到同样的东西
	gh, err := svc.History(context.Background())
	if err != nil || len(gh.History["a"]) != 1 || gh.TimeoutMS != 1000 {
		t.Fatalf("%+v %v", gh, err)
	}
}

func TestRunNoSnapshotStillMeasures(t *testing.T) {
	fp := newFakeProxy(nil)
	defer fp.Close()
	for name, snap := range map[string]func(context.Context) (singbox.KernelSnapshot, error){
		"没接": nil,
		"读不到": func(context.Context) (singbox.KernelSnapshot, error) {
			return singbox.KernelSnapshot{}, errors.New("控制接口连不上")
		},
		"没有这条": func(context.Context) (singbox.KernelSnapshot, error) { return singbox.KernelSnapshot{}, nil },
	} {
		svc := newService(t, fp, snap)
		run, err := svc.Run(context.Background(), []Site{{ID: "a", URL: "http://a.test/"}})
		if err != nil {
			t.Fatal(name, err)
		}
		r := run.Sites[0]
		if r.MS == nil || r.Error != nil || r.Via != nil || r.Chain == nil || len(r.Chain) != 0 {
			t.Fatalf("%s:%+v", name, r)
		}
		if h := run.History["a"]; len(h) != 1 || h[0].Node != "" || h[0].Delay < 1 {
			t.Fatalf("%s:%+v", name, h)
		}
	}
}

func TestRunFailures(t *testing.T) {
	// 站点超时
	slow := newFakeProxy(func(string) (time.Duration, bool) { return 600 * time.Millisecond, false })
	defer slow.Close()
	svc := newService(t, slow, nil)
	svc.Timeout = 200 * time.Millisecond
	run, err := svc.Run(context.Background(), []Site{{ID: "a", URL: "http://a.test/"}})
	if err != nil {
		t.Fatal(err)
	}
	r := run.Sites[0]
	if r.MS != nil || r.OpenMS != nil || r.Error == nil || !strings.HasPrefix(*r.Error, "超时(0.2 秒") {
		t.Fatalf("%+v %v", r, r.Error)
	}
	if h := run.History["a"]; len(h) != 1 || h[0].Delay != 0 {
		t.Fatalf("不通要记 0:%+v", h)
	}

	// 内核的面板入站没在听
	dead := newFakeProxy(nil)
	addr := dead.addr()
	dead.Close()
	svc2 := newService(t, dead, nil)
	svc2.ProxyAddr = addr
	run, err = svc2.Run(context.Background(), []Site{{ID: "a", URL: "http://a.test/"}})
	if err != nil {
		t.Fatal(err)
	}
	if e := run.Sites[0].Error; e == nil || !strings.Contains(*e, "本地代理入口") {
		t.Fatalf("%v", e)
	}
	if strings.Contains(*run.Sites[0].Error, "a.test") {
		t.Fatal("错误信息不该带网址")
	}

	// 内核不让建隧道(例如路由规则拒绝)
	deny := newFakeProxy(nil)
	defer deny.Close()
	deny.refuse = 403
	run, err = newService(t, deny, nil).Run(context.Background(), []Site{{ID: "a", URL: "http://a.test/"}})
	if err != nil {
		t.Fatal(err)
	}
	if e := run.Sites[0].Error; e == nil || *e != "内核拒绝了这个连接(HTTP 403)" {
		t.Fatalf("%v", e)
	}
}

func TestRunMixedOneFailsOthersOK(t *testing.T) {
	fp := newFakeProxy(func(host string) (time.Duration, bool) { return 0, host == "bad.test" }) // bad.test 直接断开
	defer fp.Close()
	svc := newService(t, fp, fp.snapshot(map[string][]string{"ok.test": {"节点"}}))
	run, err := svc.Run(context.Background(), []Site{{ID: "ok", URL: "http://ok.test/"}, {ID: "bad", URL: "http://bad.test/"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Sites[0].MS == nil || run.Sites[1].MS != nil || run.Sites[1].Error == nil {
		t.Fatalf("%+v", run.Sites)
	}
	if *run.Sites[1].Error != "连接被中断" {
		t.Fatalf("%q", *run.Sites[1].Error)
	}
	if run.Sites[1].Chain == nil || len(run.Sites[1].Chain) != 0 {
		t.Fatal("失败的站点 chain 要是空数组")
	}
}

func TestRunRejectsBadInputAndCancel(t *testing.T) {
	fp := newFakeProxy(nil)
	defer fp.Close()
	svc := newService(t, fp, nil)
	var ie *InputError
	if _, err := svc.Run(context.Background(), nil); !errors.As(err, &ie) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.Run(ctx, []Site{{ID: "a", URL: "http://a.test/"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 ctx 错误:%v", err)
	}
	if h, _ := svc.History(context.Background()); len(h.History) != 0 {
		t.Fatal("取消的一轮不该记历史")
	}
}

func TestDescribe(t *testing.T) {
	s := &Service{Timeout: 5 * time.Second}
	cases := map[string]string{
		"context deadline exceeded": "超时(5 秒内没有响应)",
	}
	for in, want := range cases {
		if got := s.describe(context.DeadlineExceeded); got != want {
			t.Errorf("%s → %s", in, got)
		}
	}
	long := s.describe(errors.New(strings.Repeat("长", 300)))
	if n := len([]rune(long)); n > 110 {
		t.Fatalf("太长:%d", n)
	}
}
