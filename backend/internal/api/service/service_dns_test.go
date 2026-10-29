package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnssync"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// 一份共享的事件记录,检查「撤销 → 服务操作 → 对齐」的先后顺序。
type log struct{ ev []string }

func (l *log) String() string { return strings.Join(l.ev, ",") }

type recFake struct {
	fake
	l *log
}

func (f *recFake) Do(ctx context.Context, name, action string) (system.ActionResult, error) {
	f.l.ev = append(f.l.ev, action)
	return f.fake.Do(ctx, name, action)
}

type fakeDNS struct {
	l        *log
	stopErr  error
	startRes dnssync.Result
	startErr error
}

func (f *fakeDNS) BeforeKernelStop(ctx context.Context) error {
	f.l.ev = append(f.l.ev, "revert")
	return f.stopErr
}
func (f *fakeDNS) AfterKernelStart(ctx context.Context) (dnssync.Result, error) {
	f.l.ev = append(f.l.ev, "sync")
	return f.startRes, f.startErr
}

func setupDNS(f *recFake, dns DNSSync) *http.ServeMux {
	mux := http.NewServeMux()
	Register(mux, Deps{Services: f, Kind: system.InitSystemd, CoreService: "sing-box", PanelName: "openbox-backend", DNS: dns})
	return mux
}

func TestActionDNSOrder(t *testing.T) {
	cases := []struct{ action, want string }{
		{"stop", "revert,stop"},
		{"restart", "revert,restart,sync"},
		{"start", "start,sync"},
		{"enable", "enable"},
		{"disable", "disable"},
	}
	for _, c := range cases {
		l := &log{}
		f := &recFake{fake: fake{result: system.ActionResult{OK: true}}, l: l}
		rec := do(setupDNS(f, &fakeDNS{l: l}), http.MethodPost, "/api/openbox/service/core/"+c.action)
		if rec.Code != 200 || l.String() != c.want {
			t.Errorf("%s: 想要 %s,实际 %s (%d)", c.action, c.want, l, rec.Code)
		}
	}
}

func TestActionDNSRevertFailureDoesNotTouchKernel(t *testing.T) {
	for _, action := range []string{"stop", "restart"} {
		l := &log{}
		f := &recFake{fake: fake{result: system.ActionResult{OK: true}}, l: l}
		rec := do(setupDNS(f, &fakeDNS{l: l, stopErr: errors.New("撤销 DNS 接管失败: 删不掉")}), http.MethodPost, "/api/openbox/service/core/"+action)
		if rec.Code != 500 || !strings.Contains(rec.Body.String(), "为安全起见没有") || !strings.Contains(rec.Body.String(), "删不掉") {
			t.Fatalf("%s: %d %s", action, rec.Code, rec.Body.String())
		}
		if l.String() != "revert" {
			t.Fatalf("%s: 撤销没成功就不能动内核: %s", action, l)
		}
	}
}

func TestActionDNSSyncSkippedWhenCommandFailed(t *testing.T) {
	l := &log{}
	f := &recFake{fake: fake{result: system.ActionResult{OK: false, Code: 1, Stderr: "Job failed"}}, l: l}
	rec := do(setupDNS(f, &fakeDNS{l: l}), http.MethodPost, "/api/openbox/service/core/start")
	if rec.Code != 200 || l.String() != "start" {
		t.Fatalf("内核没起来就不该去接管 DNS: %d %s", rec.Code, l)
	}
	if strings.Contains(rec.Body.String(), "dnsNote") {
		t.Fatalf("%s", rec.Body.String())
	}
}

func TestActionDNSNote(t *testing.T) {
	// 对齐成功改了系统:带一句说明;结果字段不变
	l := &log{}
	f := &recFake{fake: fake{result: system.ActionResult{OK: true, DurationMs: 7}}, l: l}
	rec := do(setupDNS(f, &fakeDNS{l: l, startRes: dnssync.Result{Mode: "dnsmasq", Changed: true}}), http.MethodPost, "/api/openbox/service/core/restart")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != true || got["durationMs"] != float64(7) || !strings.Contains(got["dnsNote"].(string), "转发给内核") {
		t.Fatalf("%v", got)
	}
	// 对齐失败:服务操作仍然是成功的(内核起来了),失败原因在 dnsNote 里
	l = &log{}
	f = &recFake{fake: fake{result: system.ActionResult{OK: true}}, l: l}
	rec = do(setupDNS(f, &fakeDNS{l: l, startErr: errors.New("没找到 conf-dir")}), http.MethodPost, "/api/openbox/service/core/start")
	got = map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["ok"] != true || !strings.Contains(got["dnsNote"].(string), "DNS 保持原样") || !strings.Contains(got["dnsNote"].(string), "没找到 conf-dir") {
		t.Fatalf("%d %v", rec.Code, got)
	}
	// 没动系统、也没出错:不带说明
	l = &log{}
	f = &recFake{fake: fake{result: system.ActionResult{OK: true}}, l: l}
	rec = do(setupDNS(f, &fakeDNS{l: l, startRes: dnssync.Result{Mode: "dnsmasq"}}), http.MethodPost, "/api/openbox/service/core/start")
	if strings.Contains(rec.Body.String(), "dnsNote") {
		t.Fatalf("%s", rec.Body.String())
	}
}

func TestActionDNSBadActionNeverTouchesDNS(t *testing.T) {
	l := &log{}
	f := &recFake{fake: fake{doErr: system.ErrBadAction}, l: l}
	rec := do(setupDNS(f, &fakeDNS{l: l}), http.MethodPost, "/api/openbox/service/core/reload")
	if rec.Code != 400 || l.String() != "reload" {
		t.Fatalf("%d %s", rec.Code, l)
	}
}

func TestActionDNSSurvivesClientDisconnect(t *testing.T) {
	// 请求被取消时,DNS 的撤销 / 对齐拿到的 ctx 仍然是活的(不能因为浏览器断开就半途而废)
	l := &log{}
	f := &recFake{fake: fake{result: system.ActionResult{OK: true}}, l: l}
	var seen error
	dns := &ctxDNS{l: l, check: func(ctx context.Context) { seen = ctx.Err() }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/openbox/service/core/stop", nil).WithContext(ctx)
	setupDNS(f, dns).ServeHTTP(rec, req)
	if seen != nil {
		t.Fatalf("DNS 撤销用的 ctx 不该随请求取消: %v", seen)
	}
}

type ctxDNS struct {
	l     *log
	check func(context.Context)
}

func (c *ctxDNS) BeforeKernelStop(ctx context.Context) error {
	c.check(ctx)
	return nil
}
func (c *ctxDNS) AfterKernelStart(ctx context.Context) (dnssync.Result, error) {
	c.check(ctx)
	return dnssync.Result{}, nil
}
