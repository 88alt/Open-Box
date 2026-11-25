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

// details(上游 v0.1.486 的待重启明细)原样带出;没有明细时是空数组而不是 null,前端据此回退到按 reasons 显示
func TestStatusPendingRestartDetails(t *testing.T) {
	p := &fakePending{res: singbox.PendingRestart{Pending: true, Reasons: []string{"dns", "inbounds"}, Details: []string{"dns_mode", "tun_settings"}}}
	pr := statusWithPending(t, true, p)["pendingRestart"].(map[string]any)
	d, ok := pr["details"].([]any)
	if !ok || len(d) != 2 || d[0] != "dns_mode" || d[1] != "tun_settings" {
		t.Fatalf("details 应原样带出: %v", pr["details"])
	}
	q := &fakePending{res: singbox.PendingRestart{Pending: true, Reasons: []string{"system"}}}
	pr = statusWithPending(t, true, q)["pendingRestart"].(map[string]any)
	if d, ok := pr["details"].([]any); !ok || len(d) != 0 {
		t.Fatalf("没有明细时 details 必须是空数组而不是 null: %v", pr["details"])
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

func writeBin(t *testing.T, name, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func kernelVersionBody(t *testing.T, d Deps) (int, map[string]any) {
	t.Helper()
	d.Services = &fake{}
	d.Kind = system.InitNone
	mux := http.NewServeMux()
	Register(mux, d)
	rec := do(mux, http.MethodGet, "/api/openbox/kernel/version")
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return rec.Code, got
}

func TestKernelVersionSelection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假内核是 /bin/sh 脚本,Windows 上跑不了")
	}
	sb := writeBin(t, "sing-box", "echo 'sing-box version 1.14.4'")
	mh := writeBin(t, "mihomo", "echo 'Mihomo Meta v1.19.0 linux arm64'")
	sel := filepath.Join(t.TempDir(), "router-kernel")

	// 没有选择文件:sing-box,Mihomo 在 PATH 里能找到 → mihomoAvailable
	code, got := kernelVersionBody(t, Deps{SingBox: sb, Mihomo: mh, KernelFile: sel})
	if code != 200 || got["kernel"] != "sing-box" || got["version"] != "1.14.4" || got["ok"] != true || got["mihomoAvailable"] != true {
		t.Fatalf("%d %v", code, got)
	}
	// 选了 mihomo:版本取 mihomo -v 的第一行
	if err := os.WriteFile(sel, []byte("mihomo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, got = kernelVersionBody(t, Deps{SingBox: sb, Mihomo: mh, KernelFile: sel})
	if code != 200 || got["kernel"] != "mihomo" || got["version"] != "Mihomo Meta v1.19.0 linux arm64" || got["ok"] != true {
		t.Fatalf("%d %v", code, got)
	}
	// 安装包没带 Mihomo:mihomoAvailable=false(没配置路径、或路径找不到都一样)
	for _, mihomo := range []string{"", "no-such-mihomo"} {
		code, got = kernelVersionBody(t, Deps{SingBox: sb, Mihomo: mihomo})
		if code != 200 || got["kernel"] != "sing-box" || got["mihomoAvailable"] != false {
			t.Fatalf("%q: %d %v", mihomo, code, got)
		}
	}
	// 选了 mihomo 但没有 Mihomo:仍是 200,ok=false,version 为空
	code, got = kernelVersionBody(t, Deps{SingBox: sb, Mihomo: "no-such-mihomo", KernelFile: sel})
	if code != 200 || got["kernel"] != "mihomo" || got["ok"] != false || got["version"] != "" || got["mihomoAvailable"] != false {
		t.Fatalf("%d %v", code, got)
	}
	// 选择文件无效:500,不假装成 sing-box
	if err := os.WriteFile(sel, []byte("clash"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, got = kernelVersionBody(t, Deps{SingBox: sb, Mihomo: mh, KernelFile: sel}); code != 500 {
		t.Fatalf("无效选择应 500: %d %v", code, got)
	}
}

func TestStatusCoreKernel(t *testing.T) {
	sel := filepath.Join(t.TempDir(), "router-kernel")
	f := &fake{statuses: map[string]system.ServiceInfo{"sing-box": {Running: true, Raw: "running"}}}
	status := func(d Deps) map[string]any {
		t.Helper()
		d.Services, d.Kind, d.CoreService, d.PanelName, d.Arch = f, system.InitOpenWrt, "sing-box", "openbox-backend", "arm64"
		mux := http.NewServeMux()
		Register(mux, d)
		rec := do(mux, http.MethodGet, "/api/openbox/service/status")
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got["core"].(map[string]any)
	}
	// 原有字段不变,多一个 kernel
	if core := status(Deps{KernelFile: sel}); core["kernel"] != "sing-box" || core["running"] != true || core["raw"] != "running" {
		t.Fatalf("%v", core)
	}
	if err := os.WriteFile(sel, []byte("mihomo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if core := status(Deps{KernelFile: sel}); core["kernel"] != "mihomo" {
		t.Fatalf("%v", core)
	}
	// 选择文件无效:状态接口照常返回,只是不带 kernel
	if err := os.WriteFile(sel, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if core := status(Deps{KernelFile: sel}); core["running"] != true {
		t.Fatalf("%v", core)
	} else if _, has := core["kernel"]; has {
		t.Fatalf("无效选择不该带 kernel: %v", core)
	}
	// 没有服务管理器:也带 kernel
	d := Deps{Services: f, Kind: system.InitNone, KernelFile: filepath.Join(t.TempDir(), "none")}
	mux := http.NewServeMux()
	Register(mux, d)
	rec := do(mux, http.MethodGet, "/api/openbox/service/status")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kernel":"sing-box"`) || !strings.Contains(rec.Body.String(), `"raw":"unsupported"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
