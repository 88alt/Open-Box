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
