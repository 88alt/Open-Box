package rollback

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

type fakeSvc struct {
	mu    sync.Mutex
	calls []string
	res   map[string]system.ActionResult
	errs  map[string]error
	names []string
}

func (f *fakeSvc) Do(_ context.Context, name, action string) (system.ActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, action)
	f.names = append(f.names, name)
	if e := f.errs[action]; e != nil {
		return system.ActionResult{}, e
	}
	if r, ok := f.res[action]; ok {
		return r, nil
	}
	return system.ActionResult{OK: true}, nil
}

type fakeDNS struct {
	err    error
	calls  int
	ctxErr error // 调用时收到的 ctx 是否已取消
}

func (f *fakeDNS) BeforeKernelStop(ctx context.Context) error {
	f.calls++
	f.ctxErr = ctx.Err()
	return f.err
}

func post(t *testing.T, d Deps, method string) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/rollback", nil))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func strs(v any) []string {
	out := []string{}
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestRollbackAllOK(t *testing.T) {
	svc, dns := &fakeSvc{}, &fakeDNS{}
	code, m := post(t, Deps{Services: svc, DNS: dns, Kind: system.InitSystemd, CoreService: "sing-box"}, http.MethodPost)
	if code != 200 || m["ok"] != true {
		t.Fatalf("code=%d m=%v", code, m)
	}
	if got := strs(m["actions"]); !reflect.DeepEqual(got, []string{"restore-dns", "stop-core", "disable-autostart"}) {
		t.Fatalf("actions=%v", got)
	}
	if len(m["failures"].([]any)) != 0 {
		t.Fatalf("failures=%v", m["failures"])
	}
	if !reflect.DeepEqual(svc.calls, []string{"stop", "disable"}) || svc.names[0] != "sing-box" || dns.calls != 1 {
		t.Fatalf("calls=%v names=%v dns=%d", svc.calls, svc.names, dns.calls)
	}
}

func TestRollbackNoDNSPlatform(t *testing.T) {
	svc := &fakeSvc{}
	_, m := post(t, Deps{Services: svc, Kind: system.InitSystemd, CoreService: "x"}, http.MethodPost)
	if got := strs(m["actions"]); !reflect.DeepEqual(got, []string{"stop-core", "disable-autostart"}) || m["ok"] != true {
		t.Fatalf("m=%v", m)
	}
}

func TestRollbackDNSFailureLeavesKernelAlone(t *testing.T) {
	svc, dns := &fakeSvc{}, &fakeDNS{err: errors.New("dnsmasq 重启失败")}
	code, m := post(t, Deps{Services: svc, DNS: dns, Kind: system.InitSystemd, CoreService: "x"}, http.MethodPost)
	if code != 200 || m["ok"] != false {
		t.Fatalf("code=%d m=%v", code, m)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("撤销接管失败不该动内核: %v", svc.calls)
	}
	f := m["failures"].([]any)[0].(map[string]any)
	if f["step"] != "restore-dns" || f["message"] != "dnsmasq 重启失败" {
		t.Fatalf("failures=%v", m["failures"])
	}
	if len(m["actions"].([]any)) != 0 {
		t.Fatalf("actions 应为空数组: %v", m["actions"])
	}
}

func TestRollbackStopFailureStillDisablesAutostart(t *testing.T) {
	svc := &fakeSvc{res: map[string]system.ActionResult{"stop": {OK: false, Code: 3, Stderr: " 停不下来\n"}}}
	_, m := post(t, Deps{Services: svc, DNS: &fakeDNS{}, Kind: system.InitSystemd, CoreService: "x"}, http.MethodPost)
	if m["ok"] != false || !reflect.DeepEqual(svc.calls, []string{"stop", "disable"}) {
		t.Fatalf("m=%v calls=%v", m, svc.calls)
	}
	f := m["failures"].([]any)[0].(map[string]any)
	if f["step"] != "stop-core" || f["message"] != "停不下来" {
		t.Fatalf("failures=%v", m["failures"])
	}
	if got := strs(m["actions"]); !reflect.DeepEqual(got, []string{"restore-dns", "disable-autostart"}) {
		t.Fatalf("actions=%v", got)
	}
}

func TestRollbackDisableFailureAndCodeFallback(t *testing.T) {
	svc := &fakeSvc{res: map[string]system.ActionResult{"disable": {OK: false, Code: 5}}}
	_, m := post(t, Deps{Services: svc, Kind: system.InitSystemd, CoreService: "x"}, http.MethodPost)
	f := m["failures"].([]any)[0].(map[string]any)
	if m["ok"] != false || f["step"] != "disable-autostart" || f["message"] != "code 5" {
		t.Fatalf("m=%v", m)
	}
}

func TestRollbackServiceErrorIsFailureNotHTTPError(t *testing.T) {
	svc := &fakeSvc{errs: map[string]error{"stop": errors.New("boom")}}
	code, m := post(t, Deps{Services: svc, Kind: system.InitSystemd, CoreService: "x"}, http.MethodPost)
	f := m["failures"].([]any)[0].(map[string]any)
	if code != 200 || f["step"] != "stop-core" || f["message"] != "boom" || !reflect.DeepEqual(svc.calls, []string{"stop", "disable"}) {
		t.Fatalf("code=%d m=%v calls=%v", code, m, svc.calls)
	}
}

func TestRollbackBusy(t *testing.T) {
	svc := &fakeSvc{}
	d := Deps{Services: svc, Kind: system.InitSystemd, CoreService: "x",
		Exclusive: func(func() error) error { return ErrBusy }}
	code, m := post(t, d, http.MethodPost)
	if code != http.StatusConflict || m["ok"] != false || m["message"] == "" || len(svc.calls) != 0 {
		t.Fatalf("code=%d m=%v calls=%v", code, m, svc.calls)
	}
}

func TestRollbackRunsInsideExclusive(t *testing.T) {
	svc, inside := &fakeSvc{}, false
	d := Deps{Services: svc, Kind: system.InitSystemd, CoreService: "x",
		Exclusive: func(fn func() error) error {
			inside = true
			defer func() { inside = false }()
			if len(svc.calls) != 0 {
				t.Fatal("进锁之前就动了内核")
			}
			return fn()
		}}
	code, _ := post(t, d, http.MethodPost)
	if code != 200 || len(svc.calls) != 2 || inside {
		t.Fatalf("code=%d calls=%v inside=%v", code, svc.calls, inside)
	}
}

func TestRollbackOtherExclusiveErrorIs500(t *testing.T) {
	d := Deps{Services: &fakeSvc{}, Kind: system.InitSystemd, CoreService: "x",
		Exclusive: func(func() error) error { return errors.New("锁坏了") }}
	code, m := post(t, d, http.MethodPost)
	if code != 500 || m["ok"] != false || m["message"] != "锁坏了" {
		t.Fatalf("code=%d m=%v", code, m)
	}
}

func TestRollbackNoInit(t *testing.T) {
	for _, d := range []Deps{{Services: &fakeSvc{}, Kind: system.InitNone}, {Kind: system.InitSystemd}} {
		code, m := post(t, d, http.MethodPost)
		if code != http.StatusServiceUnavailable || m["ok"] != false {
			t.Fatalf("code=%d m=%v", code, m)
		}
	}
}

func TestRollbackMethods(t *testing.T) {
	svc := &fakeSvc{}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if code, _ := post(t, Deps{Services: svc, Kind: system.InitSystemd, CoreService: "x"}, m); code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 应 405,得到 %d", m, code)
		}
	}
	if len(svc.calls) != 0 {
		t.Fatalf("非 POST 不该动内核: %v", svc.calls)
	}
}

func TestRollbackNoStoreHeader(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, Deps{Services: &fakeSvc{}, Kind: system.InitSystemd, CoreService: "x"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/rollback", nil))
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q", cc)
	}
}

func TestRollbackFinishesEvenIfClientDisconnects(t *testing.T) {
	svc, dns := &fakeSvc{}, &fakeDNS{}
	mux := http.NewServeMux()
	Register(mux, Deps{Services: svc, DNS: dns, Kind: system.InitSystemd, CoreService: "x"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/openbox/rollback", nil).WithContext(ctx)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if dns.calls != 1 || dns.ctxErr != nil || len(svc.calls) != 2 {
		t.Fatalf("请求断开不该中断回滚: dns=%d ctxErr=%v calls=%v", dns.calls, dns.ctxErr, svc.calls)
	}
}
