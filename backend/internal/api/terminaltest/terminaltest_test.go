package terminaltest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/conntrack"
	"github.com/timeflysoon/Open-Box/backend/internal/dnsforward"
	"github.com/timeflysoon/Open-Box/backend/internal/nsprobe"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

func serve(t *testing.T, d Deps, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func fixed(c system.TerminalCapability) Deps {
	return Deps{Capability: func(context.Context) system.TerminalCapability { return c }}
}

func TestCapabilityEndpoint(t *testing.T) {
	m := 24
	rec := serve(t, fixed(system.TerminalCapability{OK: true, Missing: []string{}, LAN: &system.TerminalLAN{Device: "br-lan", Address: "192.168.5.1", Mask: &m}}), "GET", "/api/openbox/terminal-test/capability", "")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("应当不缓存")
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != true {
		t.Errorf("ok: %v", got["ok"])
	}
	if ms, ok := got["missing"].([]any); !ok || len(ms) != 0 {
		t.Errorf("missing 要是空数组: %v", got["missing"])
	}
	lan := got["lan"].(map[string]any)
	if lan["device"] != "br-lan" || lan["address"] != "192.168.5.1" || lan["mask"] != float64(24) {
		t.Errorf("lan: %v", lan)
	}
}

func TestCapabilityMissingAndNullLAN(t *testing.T) {
	rec := serve(t, fixed(system.TerminalCapability{Missing: []string{"root", "lan"}}), "GET", "/api/openbox/terminal-test/capability", "")
	var got map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if string(got["ok"]) != "false" || string(got["missing"]) != `["root","lan"]` || string(got["lan"]) != "null" {
		t.Fatalf("body: %s", rec.Body.String())
	}
}

func TestCapabilityNilMissingBecomesArray(t *testing.T) {
	rec := serve(t, fixed(system.TerminalCapability{OK: true}), "GET", "/api/openbox/terminal-test/capability", "")
	if !strings.Contains(rec.Body.String(), `"missing":[]`) {
		t.Fatalf("body: %s", rec.Body.String())
	}
}

func TestNoDepsIsNotCapable(t *testing.T) {
	rec := serve(t, Deps{}, "GET", "/api/openbox/terminal-test/capability", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestMethods(t *testing.T) {
	d := fixed(system.TerminalCapability{})
	if rec := serve(t, d, "POST", "/api/openbox/terminal-test/capability", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("capability POST: %d", rec.Code)
	}
	if rec := serve(t, d, "GET", "/api/openbox/terminal-test", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("terminal-test GET: %d", rec.Code)
	}
}

func TestRunInputValidation(t *testing.T) {
	d := fixed(system.TerminalCapability{OK: true, Missing: []string{}})
	for _, body := range []string{`{}`, `{"target":""}`, `{"target":"a b"}`, `{"target":"-x"}`, `{"target":"a.com","port":70000}`, `{"target":"a.com","method":"POST"}`} {
		if rec := serve(t, d, "POST", "/api/openbox/terminal-test", body); rec.Code != 400 {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := serve(t, d, "POST", "/api/openbox/terminal-test", `not json`); rec.Code != 400 {
		t.Errorf("坏 JSON: %d", rec.Code)
	}
}

func TestRunNotCapableReportsMissing(t *testing.T) {
	d := fixed(system.TerminalCapability{Missing: []string{"root", "netns"}})
	rec := serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":" example.com ","method":"tcp"}`)
	if rec.Code != 200 {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["target"] != "example.com" || got["mode"] != "lan" || got["capable"] != false {
		t.Errorf("%v", got)
	}
	if ms, _ := got["missing"].([]any); len(ms) != 2 || ms[0] != "root" || ms[1] != "netns" {
		t.Errorf("missing: %v", got["missing"])
	}
}

func TestRunCapableWithoutTerminalIsNotImplemented(t *testing.T) {
	d := fixed(system.TerminalCapability{OK: true, Missing: []string{}})
	rec := serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("要带 error 字段: %s", rec.Body.String())
	}
}

// ---------- 建虚拟终端并取址 ----------

type fakeTerm struct {
	mu          sync.Mutex
	ensureLAN   []string
	ensureErr   error
	acquireErr  error
	teardownErr error
	lease       system.DHCPLease
	reused      bool
	acquireHold time.Duration // 取址时多等一会儿,模拟耗时
	teardowns   int
	pointCalls  []string // PointGatewayAt 的网关参数
	pointErr    error
	running     int
	maxRunning  int
	torn        chan struct{}
}

func newFakeTerm() *fakeTerm {
	return &fakeTerm{
		lease: system.DHCPLease{IP: "192.168.5.77", Mask: 24, Gateway: "192.168.5.1", DNS: []string{"192.168.5.1"}, Ms: 42},
		torn:  make(chan struct{}, 8),
	}
}

func (f *fakeTerm) Ensure(_ context.Context, lan string) (system.VirtualTerminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureLAN = append(f.ensureLAN, lan)
	if f.ensureErr != nil {
		return system.VirtualTerminal{}, f.ensureErr
	}
	return system.VirtualTerminal{Namespace: "obterm", HostIf: "obtv-h", PeerIf: "obtv-t", LANDevice: lan, MAC: "02:00:00:aa:bb:cc", Reused: f.reused}, nil
}

func (f *fakeTerm) PointGatewayAt(_ context.Context, _ system.VirtualTerminal, gw string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pointCalls = append(f.pointCalls, gw)
	return f.pointErr
}

func (f *fakeTerm) AcquireDHCP(_ context.Context, _ system.VirtualTerminal) (system.DHCPLease, error) {
	f.mu.Lock()
	f.running++
	if f.running > f.maxRunning {
		f.maxRunning = f.running
	}
	hold, err, lease := f.acquireHold, f.acquireErr, f.lease
	f.mu.Unlock()
	time.Sleep(hold)
	f.mu.Lock()
	f.running--
	f.mu.Unlock()
	if err != nil {
		return system.DHCPLease{}, err
	}
	return lease, nil
}

func (f *fakeTerm) Teardown(context.Context) error {
	f.mu.Lock()
	f.teardowns++
	err := f.teardownErr
	f.mu.Unlock()
	f.torn <- struct{}{}
	return err
}

func (f *fakeTerm) counts() (ensures, teardowns int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ensureLAN), f.teardowns
}

func capableDeps(f *fakeTerm) Deps {
	m := 24
	d := fixed(system.TerminalCapability{OK: true, Missing: []string{}, LAN: &system.TerminalLAN{Device: "br-lan", Address: "192.168.5.1", Mask: &m}})
	d.Terminal = f
	d.IdleAfter = time.Hour // 默认不让空闲拆除干扰别的测试
	return d
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRunBuildsTerminalAndReturnsSource(t *testing.T) {
	f := newFakeTerm()
	f.reused = true
	got := decode(t, serve(t, capableDeps(f), "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	if got["target"] != "example.com" || got["mode"] != "lan" || got["capable"] != true {
		t.Fatalf("%v", got)
	}
	if got["setupError"] != nil {
		t.Fatalf("不该有 setupError: %v", got["setupError"])
	}
	if lan := f.ensureLAN; len(lan) != 1 || lan[0] != "br-lan" {
		t.Errorf("Ensure 要用能力检测挑出的 LAN 口: %v", lan)
	}
	src, _ := got["source"].(map[string]any)
	want := map[string]any{"kind": "virtual", "name": "obterm", "ip": "192.168.5.77", "mac": "02:00:00:aa:bb:cc", "via": "dhcp",
		"gateway": "192.168.5.1", "lanDevice": "br-lan", "reused": true, "dhcpMs": float64(42)}
	for k, v := range want {
		if src[k] != v {
			t.Errorf("source.%s = %v,要 %v", k, src[k], v)
		}
	}
	if dns, _ := src["dns"].([]any); len(dns) != 1 || dns[0] != "192.168.5.1" {
		t.Errorf("dns: %v", src["dns"])
	}
	if _, has := src["override"]; has {
		t.Errorf("还没做旁路由改指,不能带 override: %v", src)
	}
	// 取址之后的探测还没做:不带 exit 等字段,前端不会把它当完整结果
	for _, k := range []string{"exit", "entry", "dnsEvidence", "kernelDns", "firstLayer", "dnsForward"} {
		if _, has := got[k]; has {
			t.Errorf("不该有 %s", k)
		}
	}
	if tm, _ := got["timing"].(map[string]any); tm == nil || tm["totalMs"] == nil {
		t.Errorf("timing: %v", got["timing"])
	}
}

func TestRunLeaseWithoutGatewayOrDNSKeepsShape(t *testing.T) {
	f := newFakeTerm()
	f.lease = system.DHCPLease{IP: "10.0.0.9", Mask: 24}
	src := decode(t, serve(t, capableDeps(f), "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))["source"].(map[string]any)
	if dns, ok := src["dns"].([]any); !ok || len(dns) != 0 {
		t.Errorf("dns 要是空数组(前端要 join): %v", src["dns"])
	}
	if src["gateway"] != "" {
		t.Errorf("gateway: %v", src["gateway"])
	}
}

func TestRunEnsureFailureIsSetupError(t *testing.T) {
	f := newFakeTerm()
	f.ensureErr = errors.New("名字 obterm 已被占用,没有动它")
	got := decode(t, serve(t, capableDeps(f), "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	if got["capable"] != true || got["setupError"] != "名字 obterm 已被占用,没有动它" {
		t.Fatalf("%v", got)
	}
	if got["source"] != nil {
		t.Errorf("建不起来不该有 source")
	}
	if f.running != 0 || f.maxRunning != 0 {
		t.Errorf("建不起来就不该去取址")
	}
}

func TestRunDHCPFailureIsSetupError(t *testing.T) {
	f := newFakeTerm()
	f.acquireErr = errors.New("20 秒内没有拿到 DHCP 租约")
	got := decode(t, serve(t, capableDeps(f), "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	if got["setupError"] != "20 秒内没有拿到 DHCP 租约" || got["source"] != nil {
		t.Fatalf("%v", got)
	}
}

func TestRunWithoutLANDeviceDoesNotTouchSystem(t *testing.T) {
	f := newFakeTerm()
	d := capableDeps(f)
	d.Capability = func(context.Context) system.TerminalCapability {
		return system.TerminalCapability{OK: true, Missing: []string{}}
	}
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	if got["setupError"] == nil {
		t.Fatalf("要报 setupError: %v", got)
	}
	if n, _ := f.counts(); n != 0 {
		t.Errorf("找不到 LAN 口不该去建: Ensure %d 次", n)
	}
}

func TestRunNotCapableOrBadInputNeverTouchesSystem(t *testing.T) {
	f := newFakeTerm()
	d := capableDeps(f)
	d.Capability = func(context.Context) system.TerminalCapability {
		return system.TerminalCapability{Missing: []string{"netns"}}
	}
	_ = serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`)
	_ = serve(t, capableDeps(f), "POST", "/api/openbox/terminal-test", `{"target":"-x"}`)
	if n, _ := f.counts(); n != 0 {
		t.Errorf("缺前提 / 输入不合法都不该去建: Ensure %d 次", n)
	}
}

func waitTorn(t *testing.T, f *fakeTerm, within time.Duration) bool {
	t.Helper()
	select {
	case <-f.torn:
		return true
	case <-time.After(within):
		return false
	}
}

func TestIdleTeardownAfterRun(t *testing.T) {
	f := newFakeTerm()
	f.acquireHold = 80 * time.Millisecond // 比 IdleAfter 长:测试期间不能被拆
	d := capableDeps(f)
	d.IdleAfter = 30 * time.Millisecond
	_ = decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	if _, td := f.counts(); td != 0 {
		t.Fatalf("测试刚结束就拆了(计时应从测试结束后开始): %d", td)
	}
	if !waitTorn(t, f, 2*time.Second) {
		t.Fatal("空闲后应当自动拆除")
	}
	// 只拆一次,不会反复拆
	if waitTorn(t, f, 150*time.Millisecond) {
		t.Error("拆完不该再拆")
	}
}

func TestIdleTeardownFailureIsHarmless(t *testing.T) {
	f := newFakeTerm()
	f.teardownErr = errors.New("拆不干净")
	d := capableDeps(f)
	d.IdleAfter = 20 * time.Millisecond
	_ = decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	if !waitTorn(t, f, 2*time.Second) {
		t.Fatal("应当尝试拆除")
	}
	// 拆失败不影响之后再测
	f.mu.Lock()
	f.teardownErr = nil
	f.mu.Unlock()
	_ = decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
}

func TestRunsAreSerialized(t *testing.T) {
	f := newFakeTerm()
	f.acquireHold = 40 * time.Millisecond
	d := capableDeps(f)
	mux := http.NewServeMux()
	Register(mux, d)
	var wg sync.WaitGroup
	var bad atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/openbox/terminal-test", strings.NewReader(`{"target":"example.com"}`))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != 200 {
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("有 %d 个请求失败", bad.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxRunning != 1 {
		t.Errorf("同一时刻只能有一次测试在取址: %d", f.maxRunning)
	}
	if len(f.ensureLAN) != 4 {
		t.Errorf("四次请求都该走完: %d", len(f.ensureLAN))
	}
}

func TestCancelledWhileWaitingDoesNotRun(t *testing.T) {
	f := newFakeTerm()
	f.acquireHold = 150 * time.Millisecond
	d := capableDeps(f)
	mux := http.NewServeMux()
	Register(mux, d)
	first := make(chan struct{})
	go func() {
		defer close(first)
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/openbox/terminal-test", strings.NewReader(`{"target":"example.com"}`)))
	}()
	time.Sleep(40 * time.Millisecond) // 让第一个占住
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/api/openbox/terminal-test", strings.NewReader(`{"target":"example.com"}`)).WithContext(ctx)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	<-first
	if n, _ := f.counts(); n != 1 {
		t.Errorf("等的时候被取消的请求不该去建: Ensure %d 次", n)
	}
}

func TestNewRunRestartsIdleClock(t *testing.T) {
	f := newFakeTerm()
	d := capableDeps(f)
	d.IdleAfter = 300 * time.Millisecond
	mux := http.NewServeMux()
	Register(mux, d)
	post := func() {
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/openbox/terminal-test", strings.NewReader(`{"target":"example.com"}`)))
	}
	post()
	time.Sleep(200 * time.Millisecond)
	post() // 第二次测试之后要重新计满 IdleAfter,不能被第一次留下的计时拆掉
	if waitTorn(t, f, 200*time.Millisecond) {
		t.Fatal("第二次测试后不到 IdleAfter 就被拆了:旧的计时没停")
	}
	if !waitTorn(t, f, 2*time.Second) {
		t.Fatal("最终应当拆除")
	}
}

// ---------- 在虚拟终端里发探测 ----------

type fakeProber struct {
	mu     sync.Mutex
	ns     []string
	reqs   []nsprobe.Request
	res    nsprobe.Result
	stderr string
	err    error
}

func (f *fakeProber) Probe(_ context.Context, ns string, req nsprobe.Request) (nsprobe.Result, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ns = append(f.ns, ns)
	f.reqs = append(f.reqs, req)
	return f.res, f.stderr, f.err
}

func iptr(v int) *int { return &v }

func TestRunProbesInsideTerminalAndFillsDNSAndExit(t *testing.T) {
	f := newFakeTerm()
	f.lease.DNS = []string{"192.168.5.1", "223.5.5.5"}
	pr := &fakeProber{res: nsprobe.Result{
		OK: true, Status: iptr(204), MS: iptr(88), ConnectMs: iptr(31), LocalPort: 40123, ConnectTo: "93.184.216.34", Challenge: "cloudflare",
		DNS: &nsprobe.DNSResult{Server: "192.168.5.1", OK: true, Answers: []string{"93.184.216.34"}, MS: 12},
	}, stderr: "note"}
	d := capableDeps(f)
	d.Prober = pr
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com","port":443,"method":"head"}`))
	if len(pr.ns) != 1 || pr.ns[0] != "obterm" {
		t.Fatalf("要在虚拟终端的命名空间里探测:%v", pr.ns)
	}
	r := pr.reqs[0]
	if r.Target != "example.com" || r.Port != 443 || r.Method != "HEAD" || len(r.DNS) != 2 || r.DNS[0] != "192.168.5.1" || r.DNS[1] != "223.5.5.5" {
		t.Fatalf("请求不对:%+v", r)
	}
	dns, _ := got["dns"].(map[string]any)
	if dns["server"] != "192.168.5.1" || dns["ok"] != true || dns["ms"] != float64(12) || dns["error"] != nil {
		t.Fatalf("dns:%v", dns)
	}
	if a, _ := dns["answers"].([]any); len(a) != 1 || a[0] != "93.184.216.34" {
		t.Fatalf("answers:%v", dns["answers"])
	}
	ex, _ := got["exit"].(map[string]any)
	want := map[string]any{"url": "https://example.com/", "port": float64(443), "method": "HEAD", "ok": true, "status": float64(204), "ms": float64(88),
		"connectMs": float64(31), "localPort": float64(40123), "connectTo": "93.184.216.34", "challenge": "cloudflare"}
	for k, v := range want {
		if ex[k] != v {
			t.Errorf("exit.%s = %v,要 %v", k, ex[k], v)
		}
	}
	if ex["error"] != nil {
		t.Errorf("成功不该有 error:%v", ex["error"])
	}
	if got["probeStderr"] != "note" {
		t.Errorf("probeStderr:%v", got["probeStderr"])
	}
	// 还没做的部分不带
	for _, k := range []string{"entry", "dnsEvidence", "kernelDns", "kernel", "firstLayer", "dnsForward"} {
		if _, has := got[k]; has {
			t.Errorf("不该有 %s", k)
		}
	}
	if _, has := ex["forward"]; has {
		t.Error("不该有 exit.forward")
	}
}

func TestRunProbeIPTargetAndTCPShape(t *testing.T) {
	f := newFakeTerm()
	pr := &fakeProber{res: nsprobe.Result{OK: true, MS: iptr(5), ConnectMs: iptr(5), LocalPort: 5555}}
	d := capableDeps(f)
	d.Prober = pr
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"1.1.1.1","method":"TCP"}`))
	if dns, _ := got["dns"].(map[string]any); dns["skipped"] != true || len(dns) != 1 {
		t.Fatalf("目标是 IP:dns 应是 {skipped:true}:%v", got["dns"])
	}
	ex, _ := got["exit"].(map[string]any)
	if ex["url"] != "1.1.1.1:443" || ex["port"] != float64(443) || ex["method"] != "TCP" || ex["ok"] != true || ex["status"] != nil || ex["connectTo"] != nil {
		t.Fatalf("exit:%v", ex)
	}
	if _, has := got["probeStderr"]; has {
		t.Error("没有 stderr 就不带 probeStderr")
	}
}

func TestRunProbeFailureIsReportedNotHidden(t *testing.T) {
	f := newFakeTerm()
	pr := &fakeProber{res: nsprobe.Result{Error: "超时(8 秒内没有响应)", DNS: &nsprobe.DNSResult{Server: "192.168.5.1", OK: true, Answers: nil, MS: 3}}}
	d := capableDeps(f)
	d.Prober = pr
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	ex, _ := got["exit"].(map[string]any)
	if ex["ok"] != false || ex["error"] != "超时(8 秒内没有响应)" || ex["url"] != "https://example.com/" || ex["method"] != "GET" {
		t.Fatalf("exit:%v", ex)
	}
	if dns, _ := got["dns"].(map[string]any); dns == nil || dns["answers"] == nil {
		t.Fatalf("answers 要是数组不是 null:%v", got["dns"])
	}
	if got["source"] == nil {
		t.Error("探测失败也要带 source")
	}
}

func TestRunProberErrorBecomesExitError(t *testing.T) {
	f := newFakeTerm()
	pr := &fakeProber{err: errors.New("探测进程失败:exec format error"), stderr: "exec format error"}
	d := capableDeps(f)
	d.Prober = pr
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com","port":80}`))
	ex, _ := got["exit"].(map[string]any)
	if ex["ok"] != false || !strings.Contains(ex["error"].(string), "探测进程失败") || ex["url"] != "http://example.com/" || ex["port"] != float64(80) {
		t.Fatalf("exit:%v", ex)
	}
	if got["probeStderr"] != "exec format error" || got["setupError"] != nil {
		t.Fatalf("%v", got)
	}
}

func TestRunSetupFailureNeverProbes(t *testing.T) {
	f := newFakeTerm()
	f.acquireErr = errors.New("没取到地址")
	pr := &fakeProber{}
	d := capableDeps(f)
	d.Prober = pr
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	if got["setupError"] == nil || len(pr.ns) != 0 {
		t.Fatalf("取不到地址不该探测:%v %v", got, pr.ns)
	}
	if _, has := got["exit"]; has {
		t.Error("不该有 exit")
	}
}

// ---- dnsForward:只在终端问的是本机 LAN 地址、且接管把握十足时才给 ----

func fwdKernelAll(context.Context) *dnsforward.Result {
	return &dnsforward.Result{Forward: "kernel", Plan: "all", To: "127.0.0.1:7853"}
}

func fwdProber(server string) *fakeProber {
	return &fakeProber{res: nsprobe.Result{OK: true, DNS: &nsprobe.DNSResult{Server: server, OK: true, Answers: []string{"93.184.216.34"}, MS: 3}}}
}

func TestRunFillsDNSForwardWhenAskedLocalDnsmasq(t *testing.T) {
	d := capableDeps(newFakeTerm()) // LAN 地址 192.168.5.1
	d.Prober = fwdProber("192.168.5.1")
	d.DNSForward = fwdKernelAll
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	f, _ := got["dnsForward"].(map[string]any)
	if f["forward"] != "kernel" || f["plan"] != "all" || f["to"] != "127.0.0.1:7853" || f["suffix"] != nil || len(f) != 3 {
		t.Fatalf("dnsForward:%v", got["dnsForward"])
	}
}

func TestRunDNSForwardOmittedWhenNotSure(t *testing.T) {
	// 终端问的是别的 DNS(旁路由 / 公共 DNS):dnsmasq 没参与
	d := capableDeps(newFakeTerm())
	d.Prober = fwdProber("223.5.5.5")
	d.DNSForward = fwdKernelAll
	if _, has := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))["dnsForward"]; has {
		t.Error("问的不是本机 LAN 地址,不该带 dnsForward")
	}
	// 拿不准(DNSForward 返回 nil)
	d = capableDeps(newFakeTerm())
	d.Prober = fwdProber("192.168.5.1")
	d.DNSForward = func(context.Context) *dnsforward.Result { return nil }
	if _, has := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))["dnsForward"]; has {
		t.Error("拿不准不该带 dnsForward")
	}
	// 没接线
	d = capableDeps(newFakeTerm())
	d.Prober = fwdProber("192.168.5.1")
	if _, has := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))["dnsForward"]; has {
		t.Error("没接线不该带 dnsForward")
	}
	// 目标是 IP:没有 DNS 查询,也就没有转发可说
	d = capableDeps(newFakeTerm())
	d.Prober = &fakeProber{res: nsprobe.Result{OK: true}}
	d.DNSForward = fwdKernelAll
	if _, has := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"1.1.1.1"}`))["dnsForward"]; has {
		t.Error("目标是 IP 不该带 dnsForward")
	}
}

func TestRunDNSForwardComparesAddressesNotStrings(t *testing.T) {
	d := capableDeps(newFakeTerm())
	d.Prober = fwdProber("192.168.005.001") // 写法不同的同一个地址不会被当成同一个(ParseIP 不认前导零),不带字段
	d.DNSForward = fwdKernelAll
	if _, has := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))["dnsForward"]; has {
		t.Error("解析不出来的地址不能算命中")
	}
}

// ---- dnsEvidence:按终端这次 DNS 查询的 UDP 源端口认 conntrack 记录 ----

type fakeCT struct {
	mu      sync.Mutex
	entries []conntrack.Entry
	err     error
	queries []conntrack.Query
}

func (f *fakeCT) Find(_ context.Context, q conntrack.Query) ([]conntrack.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	return f.entries, f.err
}

func ctEntry(t *testing.T, line string) conntrack.Entry {
	t.Helper()
	e, ok := conntrack.Parse(line)
	if !ok {
		t.Fatalf("解析不了:%s", line)
	}
	return e
}

func evProber(server string, ports ...int) *fakeProber {
	return &fakeProber{res: nsprobe.Result{OK: true, DNS: &nsprobe.DNSResult{Server: server, OK: true, Answers: []string{"93.184.216.34"}, MS: 3, LocalPorts: ports}}}
}

const ctNormal = "ipv4 2 udp 17 25 src=192.168.5.77 dst=192.168.5.1 sport=39000 dport=53 src=192.168.5.1 dst=192.168.5.77 sport=53 dport=39000 mark=0 use=2"
const ctRedirected = "ipv4 2 udp 17 25 src=192.168.5.77 dst=8.8.8.8 sport=39002 dport=53 src=192.168.5.1 dst=192.168.5.77 sport=7853 dport=39002 mark=0 use=1"

func TestRunFillsDNSEvidence(t *testing.T) {
	ct := &fakeCT{entries: []conntrack.Entry{ctEntry(t, ctNormal)}}
	d := capableDeps(newFakeTerm()) // 终端地址 192.168.5.77
	d.Prober = evProber("192.168.5.1", 39000)
	d.Conntrack = ct
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	ev, _ := got["dnsEvidence"].(map[string]any)
	if ev["flows"] != float64(1) || ev["hijacked"] != false || ev["answeredBy"] != "192.168.5.1" || ev["line"] != ctNormal || len(ev) != 4 {
		t.Fatalf("dnsEvidence:%v", got["dnsEvidence"])
	}
	if len(ct.queries) != 1 || ct.queries[0] != (conntrack.Query{Proto: "udp", Src: "192.168.5.77", Dst: "192.168.5.1", Dport: 53}) {
		t.Fatalf("查询条件:%+v", ct.queries)
	}
}

func TestRunDNSEvidenceHijackedAndCustomPort(t *testing.T) {
	ct := &fakeCT{entries: []conntrack.Entry{ctEntry(t, ctRedirected)}}
	d := capableDeps(newFakeTerm())
	d.Prober = evProber("8.8.8.8", 39002)
	d.Conntrack = ct
	ev, _ := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))["dnsEvidence"].(map[string]any)
	if ev["hijacked"] != true || ev["answeredBy"] != "192.168.5.1:7853" || ev["flows"] != float64(1) {
		t.Fatalf("回复方被改写要判劫持:%v", ev)
	}
	// DHCP 给的服务器带端口
	ct = &fakeCT{}
	d = capableDeps(newFakeTerm())
	d.Prober = evProber("192.168.5.1:5353", 4000)
	d.Conntrack = ct
	decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))
	if len(ct.queries) != 1 || ct.queries[0].Dst != "192.168.5.1" || ct.queries[0].Dport != 5353 {
		t.Fatalf("端口要跟着服务器写法走:%+v", ct.queries)
	}
}

func TestRunDNSEvidenceNoMatchingFlowIsZeroNotMissing(t *testing.T) {
	d := capableDeps(newFakeTerm())
	d.Prober = evProber("192.168.5.1", 39000)
	d.Conntrack = &fakeCT{entries: []conntrack.Entry{ctEntry(t, ctRedirected)}} // 表里有别的记录,没有这次的
	ev, ok := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))["dnsEvidence"].(map[string]any)
	if !ok || ev["flows"] != float64(0) || ev["hijacked"] != false || ev["line"] != "" {
		t.Fatalf("读得到表、表里没有这次的记录:flows 应是 0:%v", ev)
	}
}

func TestRunDNSEvidenceOmittedWhenCannotTell(t *testing.T) {
	post := func(d Deps, target string) map[string]any {
		return decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"`+target+`"}`))
	}
	// 读不到表:不能说成「没有记录」
	d := capableDeps(newFakeTerm())
	d.Prober = evProber("192.168.5.1", 39000)
	d.Conntrack = &fakeCT{err: errors.New("permission denied")}
	if _, has := post(d, "example.com")["dnsEvidence"]; has {
		t.Error("读不到连接跟踪表不该带 dnsEvidence")
	}
	// 没拿到源端口(旧的探测进程 / 没发出查询)
	d = capableDeps(newFakeTerm())
	d.Prober = evProber("192.168.5.1")
	ct := &fakeCT{}
	d.Conntrack = ct
	if _, has := post(d, "example.com")["dnsEvidence"]; has || len(ct.queries) != 0 {
		t.Error("没有源端口就认不出这次的记录,不该带、也不用去查")
	}
	// 没接线
	d = capableDeps(newFakeTerm())
	d.Prober = evProber("192.168.5.1", 39000)
	if _, has := post(d, "example.com")["dnsEvidence"]; has {
		t.Error("没接线不该带 dnsEvidence")
	}
	// 目标是 IP:没有 DNS 查询
	d = capableDeps(newFakeTerm())
	d.Prober = &fakeProber{res: nsprobe.Result{OK: true}}
	d.Conntrack = &fakeCT{}
	if _, has := post(d, "1.1.1.1")["dnsEvidence"]; has {
		t.Error("目标是 IP 不该带 dnsEvidence")
	}
	// DNS 服务器写法认不出
	d = capableDeps(newFakeTerm())
	d.Prober = evProber("not-an-ip", 39000)
	ct = &fakeCT{}
	d.Conntrack = ct
	if _, has := post(d, "example.com")["dnsEvidence"]; has || len(ct.queries) != 0 {
		t.Error("认不出服务器地址不该查、不该带")
	}
}

func TestSplitDNSServer(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
	}{
		{"192.168.5.1", "192.168.5.1", 53}, {"192.168.5.1:5353", "192.168.5.1", 5353}, {"fd00::1", "fd00::1", 53},
		{"[fd00::1]:53", "fd00::1", 53}, {"", "", 0}, {"dns.example", "", 0}, {"1.1.1.1:0", "", 0}, {"1.1.1.1:70000", "", 0},
	}
	for _, c := range cases {
		if h, p := splitDNSServer(c.in); h != c.host || p != c.port {
			t.Errorf("%q → %q %d,要 %q %d", c.in, h, p, c.host, c.port)
		}
	}
}

// ---- source.override:DHCP 给的网关不是本机时,网关和 DNS 改指到本机 ----

func TestRunOverridesGatewayAndDNSWhenDHCPGatewayIsNotThisDevice(t *testing.T) {
	f := newFakeTerm()
	f.lease = system.DHCPLease{IP: "192.168.5.77", Mask: 24, Gateway: "192.168.5.2", DNS: []string{"192.168.5.2", "223.5.5.5"}, Ms: 42}
	d := capableDeps(f) // 本机 LAN 地址 192.168.5.1
	p := &fakeProber{res: nsprobe.Result{OK: true}}
	d.Prober = p
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"1.1.1.1"}`))
	src := got["source"].(map[string]any)
	ov, _ := src["override"].(map[string]any)
	dns, _ := src["dns"].([]any)
	if ov["gateway"] != "192.168.5.1" || len(ov) != 1 || src["gateway"] != "192.168.5.1" || len(dns) != 1 || dns[0] != "192.168.5.1" {
		t.Fatalf("source:%v", src)
	}
	if len(f.pointCalls) != 1 || f.pointCalls[0] != "192.168.5.1" {
		t.Fatalf("改网关的调用:%v", f.pointCalls)
	}
	if len(p.reqs) != 1 || len(p.reqs[0].DNS) != 1 || p.reqs[0].DNS[0] != "192.168.5.1" {
		t.Fatalf("探测要用改指后的 DNS:%+v", p.reqs)
	}
}

func TestRunNoOverrideWhenGatewayIsThisDeviceOrMissing(t *testing.T) {
	for name, gw := range map[string]string{"网关就是本机": "192.168.5.1", "DHCP 没给网关": ""} {
		f := newFakeTerm()
		f.lease = system.DHCPLease{IP: "192.168.5.77", Mask: 24, Gateway: gw, DNS: []string{"223.5.5.5"}, Ms: 1}
		d := capableDeps(f)
		src := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"example.com"}`))["source"].(map[string]any)
		if _, has := src["override"]; has || len(f.pointCalls) != 0 {
			t.Errorf("%s:不该改指:%v %v", name, src, f.pointCalls)
		}
		if src["gateway"] != gw || src["dns"].([]any)[0] != "223.5.5.5" {
			t.Errorf("%s:要原样报 DHCP 给的:%v", name, src)
		}
	}
}

func TestRunOverrideFailureIsSetupErrorAndNoProbe(t *testing.T) {
	f := newFakeTerm()
	f.lease = system.DHCPLease{IP: "192.168.5.77", Mask: 24, Gateway: "192.168.5.2", DNS: []string{"192.168.5.2"}, Ms: 1}
	f.pointErr = errors.New("Nexthop has invalid gateway")
	d := capableDeps(f)
	p := &fakeProber{res: nsprobe.Result{OK: true}}
	d.Prober = p
	got := decode(t, serve(t, d, "POST", "/api/openbox/terminal-test", `{"target":"1.1.1.1"}`))
	se, _ := got["setupError"].(string)
	if !strings.Contains(se, "Nexthop has invalid gateway") || len(p.reqs) != 0 {
		t.Fatalf("改不成要如实报,不能照旧去测:%v reqs=%d", got["setupError"], len(p.reqs))
	}
	if _, has := got["source"]; has {
		t.Error("没改成功就不该给 source(它说的是改指后的状态)")
	}
}
