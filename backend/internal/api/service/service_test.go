package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

type fake struct {
	statuses map[string]system.ServiceInfo
	statErr  error
	did      []string
	result   system.ActionResult
	doErr    error
}

func (f *fake) Status(_ context.Context, name string) (system.ServiceInfo, error) {
	if f.statErr != nil {
		return system.ServiceInfo{}, f.statErr
	}
	return f.statuses[name], nil
}

func (f *fake) Do(_ context.Context, name, action string) (system.ActionResult, error) {
	f.did = append(f.did, name+" "+action)
	return f.result, f.doErr
}

func setup(f *fake, kind system.InitKind) *http.ServeMux {
	mux := http.NewServeMux()
	Register(mux, Deps{Services: f, Kind: kind, CoreService: "sing-box", PanelName: "openbox-backend", Arch: "arm64"})
	return mux
}

func do(mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestStatus(t *testing.T) {
	yes := true
	f := &fake{statuses: map[string]system.ServiceInfo{
		"sing-box":        {Running: true, Raw: "running", Autostart: &yes},
		"openbox-backend": {Autostart: &yes},
	}}
	rec := do(setup(f, system.InitOpenWrt), http.MethodGet, "/api/openbox/service/status")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") == "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	core := got["core"].(map[string]any)
	panel := got["panel"].(map[string]any)
	if core["running"] != true || core["autostart"] != true {
		t.Fatalf("core=%v", core)
	}
	if panel["running"] != true || panel["autostart"] != true {
		t.Fatalf("panel=%v", panel)
	}
	if got["platform"] != "openwrt" || got["arch"] != "arm64" {
		t.Fatalf("%v", got)
	}
	if c, ok := got["conflicts"].([]any); !ok || len(c) != 0 {
		t.Fatalf("conflicts 应是空数组: %v", got["conflicts"])
	}
	pr := got["pendingRestart"].(map[string]any)
	if pr["pending"] != false {
		t.Fatalf("%v", pr)
	}
	if r, ok := pr["reasons"].([]any); !ok || len(r) != 0 {
		t.Fatalf("reasons 应是空数组: %v", pr["reasons"])
	}
}

func TestStatusNoInitSystem(t *testing.T) {
	rec := do(setup(&fake{}, system.InitNone), http.MethodGet, "/api/openbox/service/status")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unsupported"`) || !strings.Contains(rec.Body.String(), `"platform":"none"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestStatusError(t *testing.T) {
	rec := do(setup(&fake{statErr: errors.New("boom")}, system.InitSystemd), http.MethodGet, "/api/openbox/service/status")
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestAction(t *testing.T) {
	f := &fake{result: system.ActionResult{OK: true, Code: 0, DurationMs: 12}}
	rec := do(setup(f, system.InitSystemd), http.MethodPost, "/api/openbox/service/core/restart")
	if rec.Code != 200 || len(f.did) != 1 || f.did[0] != "sing-box restart" || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("%d %s %v", rec.Code, rec.Body.String(), f.did)
	}
	// 命令失败仍是 200,前端看 ok / stderr
	f = &fake{result: system.ActionResult{OK: false, Code: 1, Stderr: "Job failed"}}
	rec = do(setup(f, system.InitSystemd), http.MethodPost, "/api/openbox/service/core/start")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "Job failed") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestActionErrors(t *testing.T) {
	rec := do(setup(&fake{doErr: system.ErrBadAction}, system.InitSystemd), http.MethodPost, "/api/openbox/service/core/reload")
	if rec.Code != 400 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(setup(&fake{doErr: errors.New("x")}, system.InitSystemd), http.MethodPost, "/api/openbox/service/core/start")
	if rec.Code != 500 {
		t.Fatalf("%d", rec.Code)
	}
	f := &fake{}
	rec = do(setup(f, system.InitNone), http.MethodPost, "/api/openbox/service/core/start")
	if rec.Code != 503 || len(f.did) != 0 {
		t.Fatalf("%d %v", rec.Code, f.did)
	}
	// 只接受 POST
	rec = do(setup(f, system.InitSystemd), http.MethodGet, "/api/openbox/service/core/start")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("%d", rec.Code)
	}
}

func TestKernelVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 sing-box 是 /bin/sh 脚本,Windows 上跑不了")
	}
	bin := filepath.Join(t.TempDir(), "sing-box")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'sing-box version 1.14.4'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, Deps{Services: &fake{}, Kind: system.InitNone, SingBox: bin})
	rec := do(mux, http.MethodGet, "/api/openbox/kernel/version")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"1.14.4"`) || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	mux = http.NewServeMux()
	Register(mux, Deps{Services: &fake{}, Kind: system.InitNone, SingBox: "no-such-sing-box"})
	rec = do(mux, http.MethodGet, "/api/openbox/kernel/version")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), `"version":""`) {
		t.Fatalf("没有 sing-box 应 200 且 ok:false: %d %s", rec.Code, rec.Body.String())
	}
}

type fakeConflicts struct {
	found []system.Conflict
	err   error
}

func (f fakeConflicts) Find(context.Context) ([]system.Conflict, error) { return f.found, f.err }

func statusWith(t *testing.T, c ConflictFinder, f *fake) map[string]any {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, Deps{Services: f, Conflicts: c, Kind: system.InitOpenWrt, CoreService: "sing-box", PanelName: "openbox-backend", Arch: "arm64"})
	rec := do(mux, http.MethodGet, "/api/openbox/service/status")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestStatusConflicts(t *testing.T) {
	f := &fake{statuses: map[string]system.ServiceInfo{"sing-box": {Running: true, Raw: "running"}}}
	// 前端 OpenboxConflictService:{id, label, running}
	got := statusWith(t, fakeConflicts{found: []system.Conflict{{ID: "openclash", Label: "OpenClash", Running: true}}}, f)
	c, _ := got["conflicts"].([]any)
	if len(c) != 1 {
		t.Fatalf("%v", got["conflicts"])
	}
	m := c[0].(map[string]any)
	if m["id"] != "openclash" || m["label"] != "OpenClash" || m["running"] != true {
		t.Fatalf("%v", m)
	}
	// 检测出错:尽力而为,状态接口照常 200、conflicts 是空数组(不是 null)
	got = statusWith(t, fakeConflicts{err: errors.New("boom")}, f)
	if c, ok := got["conflicts"].([]any); !ok || len(c) != 0 {
		t.Fatalf("%v", got["conflicts"])
	}
	// 出错但查到了一部分:用查到的
	got = statusWith(t, fakeConflicts{found: []system.Conflict{{ID: "passwall", Label: "PassWall", Running: true}}, err: errors.New("部分失败")}, f)
	if c, _ := got["conflicts"].([]any); len(c) != 1 {
		t.Fatalf("%v", got["conflicts"])
	}
	// 不接检测:空数组
	got = statusWith(t, nil, f)
	if c, ok := got["conflicts"].([]any); !ok || len(c) != 0 {
		t.Fatalf("%v", got["conflicts"])
	}
}

// ---------- pendingRestart ----------

type fakePending struct {
	res   singbox.PendingRestart
	err   error
	calls int
}

func (f *fakePending) Pending(context.Context) (singbox.PendingRestart, error) {
	f.calls++
	return f.res, f.err
}

func statusWithPending(t *testing.T, running bool, p PendingFinder) map[string]any {
	t.Helper()
	f := &fake{statuses: map[string]system.ServiceInfo{"sing-box": {Running: running, Raw: "x"}}}
	mux := http.NewServeMux()
	Register(mux, Deps{Services: f, Pending: p, Kind: system.InitOpenWrt, CoreService: "sing-box", PanelName: "openbox-backend", Arch: "arm64"})
	rec := do(mux, http.MethodGet, "/api/openbox/service/status")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func pendingOf(t *testing.T, got map[string]any) (bool, []any) {
	t.Helper()
	pr, ok := got["pendingRestart"].(map[string]any)
	if !ok {
		t.Fatalf("pendingRestart 必须是对象: %v", got["pendingRestart"])
	}
	reasons, ok := pr["reasons"].([]any)
	if !ok {
		t.Fatalf("reasons 必须是数组而不是 null: %v", pr["reasons"])
	}
	return pr["pending"] == true, reasons
}

func TestStatusPendingRestartWhenRunning(t *testing.T) {
	p := &fakePending{res: singbox.PendingRestart{Pending: true, Reasons: []string{"nodes", "dns"}}}
	pending, reasons := pendingOf(t, statusWithPending(t, true, p))
	if !pending || len(reasons) != 2 || reasons[0] != "nodes" || reasons[1] != "dns" || p.calls != 1 {
		t.Fatalf("pending=%v reasons=%v calls=%d", pending, reasons, p.calls)
	}
}

func TestStatusPendingRestartNotAskedWhenStopped(t *testing.T) {
	p := &fakePending{res: singbox.PendingRestart{Pending: true, Reasons: []string{"nodes"}}}
	pending, reasons := pendingOf(t, statusWithPending(t, false, p))
	if pending || len(reasons) != 0 || p.calls != 0 {
		t.Fatalf("内核没在运行就没有「还没生效」,也不必去算: pending=%v reasons=%v calls=%d", pending, reasons, p.calls)
	}
}

func TestStatusPendingRestartNoChangeAndFailureAndNotWired(t *testing.T) {
	for name, p := range map[string]PendingFinder{
		"没有改动":   &fakePending{res: singbox.PendingRestart{Reasons: []string{}}},
		"判断失败":   &fakePending{err: errors.New("读不到档案"), res: singbox.PendingRestart{Pending: true, Reasons: []string{"dns"}}},
		"没接判断":   nil,
		"没有原因的真": &fakePending{res: singbox.PendingRestart{Pending: false, Reasons: []string{"dns"}}},
	} {
		pending, reasons := pendingOf(t, statusWithPending(t, true, p))
		if pending || len(reasons) != 0 {
			t.Errorf("%s: 状态接口不能因此报错或误报: pending=%v reasons=%v", name, pending, reasons)
		}
	}
}

func TestStatusPendingRestartReasonsAreCopied(t *testing.T) {
	// 响应里的 reasons 不能和判断结果共用底层数组:后续谁改了它都不该影响已发出的响应
	src := []string{"route"}
	p := &fakePending{res: singbox.PendingRestart{Pending: true, Reasons: src}}
	got := statusWithPending(t, true, p)
	src[0] = "changed"
	if _, reasons := pendingOf(t, got); reasons[0] != "route" {
		t.Fatalf("%v", reasons)
	}
}
