package kernelswitch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// newEngine 造一个 target 一定能过的引擎:内核文件指向「当前是 sing-box」,版本用桩函数,内核可执行文件随便给个存在的文件。
func newEngine(t *testing.T, _ string, mut func(*Deps)) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "kernel")
	if err := os.WriteFile(bin, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	kf := filepath.Join(dir, "router-kernel")
	if err := os.WriteFile(kf, []byte("sing-box\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tun := filepath.Join(dir, "tun")
	if err := os.WriteFile(tun, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "openbox")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := Deps{
		KernelFile: kf, SingBox: bin, Mihomo: bin, InitdCore: script, TunPath: tun,
		Version: func(_ context.Context, k string) (string, bool) {
			if k == "mihomo" {
				return "Mihomo Meta v1.19.0 linux amd64", true
			}
			return "sing-box version 1.14.4", true
		},
	}
	if mut != nil {
		mut(&d)
	}
	return New(d), dir
}

func ids(checks []Check) string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.ID
	}
	return strings.Join(out, ",")
}

func TestPreflightTargetIsCurrent(t *testing.T) {
	e, _ := newEngine(t, "mihomo", nil)
	checks := e.Preflight(context.Background(), "sing-box", nil)
	if got := ids(checks); got != strings.Join(Order, ",") {
		t.Fatalf("顺序 = %s", got)
	}
	if checks[0].Status != StatusFailed || !strings.Contains(checks[0].Details[0], "已经是当前内核") {
		t.Fatalf("target = %+v", checks[0])
	}
	// target 失败后:system / conflicts / compatibility 仍会跑,rulesets / config / firewall / dnsGuard 被挡。
	for _, id := range []string{"rulesets", "config", "firewall", "dnsGuard"} {
		for _, c := range checks {
			if c.ID == id && c.Status != StatusBlocked {
				t.Errorf("%s = %s,想要 blocked", id, c.Status)
			}
		}
	}
}

func TestPreflightConflictsBlockLaterChecks(t *testing.T) {
	e, _ := newEngine(t, "mihomo", func(d *Deps) {
		d.Conflicts = func(context.Context) ([]string, error) { return []string{"Nikki", "ShellCrash"}, nil }
		d.Compat = func(context.Context) ([]string, error) { return nil, nil }
	})
	checks := e.Preflight(context.Background(), "mihomo", nil)
	if checks[0].Status != StatusPassed {
		t.Fatalf("target = %+v", checks[0])
	}
	var conflict Check
	for _, c := range checks {
		if c.ID == "conflicts" {
			conflict = c
		}
	}
	if conflict.Status != StatusFailed || !strings.Contains(conflict.Details[0], "Nikki、ShellCrash") {
		t.Fatalf("conflicts = %+v", conflict)
	}
	if conflict.Details[0] != "检测到 Nikki、ShellCrash 正在运行。同一台路由器上不能同时运行两个代理工具,会互相冲突,Open-Box 不启动内核。请先停用 Nikki、ShellCrash(关掉它的主开关或卸载)再启动。" {
		t.Errorf("冲突文案 = %s", conflict.Details[0])
	}
	for _, c := range checks {
		if c.ID == "rulesets" && c.Status != StatusBlocked {
			t.Errorf("rulesets = %s", c.Status)
		}
	}
}

func TestPreflightCompatibilityNotApplicableForSingbox(t *testing.T) {
	e, _ := newEngine(t, "mihomo", func(d *Deps) {
		if err := os.WriteFile(d.KernelFile, []byte("mihomo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		d.Compat = func(context.Context) ([]string, error) {
			t.Error("切到 sing-box 不该问兼容性")
			return nil, nil
		}
		d.DNSGuard = func(context.Context) (bool, []string, error) { return false, nil, nil }
	})
	checks := e.Preflight(context.Background(), "sing-box", nil)
	for _, c := range checks {
		if c.ID == "compatibility" && c.Status != StatusNotApplicable {
			t.Errorf("compatibility = %s", c.Status)
		}
		if c.ID == "dnsGuard" && c.Status != StatusNotApplicable {
			t.Errorf("dnsGuard = %s", c.Status)
		}
	}
}

func TestPreflightCompatibilityProblemsBlock(t *testing.T) {
	e, _ := newEngine(t, "sing-box", func(d *Deps) {
		d.Compat = func(context.Context) ([]string, error) { return []string{"节点用了 Mihomo 不认的协议"}, nil }
	})
	checks := e.Preflight(context.Background(), "mihomo", nil)
	for _, c := range checks {
		switch c.ID {
		case "compatibility":
			if c.Status != StatusFailed || c.Details[0] != "节点用了 Mihomo 不认的协议" {
				t.Errorf("compatibility = %+v", c)
			}
		case "rulesets":
			if c.Status != StatusBlocked {
				t.Errorf("rulesets = %s", c.Status)
			}
		}
	}
}

func TestPreflightWithoutGeneratorsFailsRulesets(t *testing.T) {
	e, dir := newEngine(t, "mihomo", func(d *Deps) {
		if err := os.WriteFile(d.KernelFile, []byte("mihomo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	_ = dir
	checks := e.Preflight(context.Background(), "sing-box", nil)
	var rules Check
	for _, c := range checks {
		if c.ID == "rulesets" {
			rules = c
		}
	}
	if rules.Status != StatusFailed || len(rules.Details) == 0 || !strings.Contains(rules.Details[0], "没有 sing-box 配置生成器") {
		t.Fatalf("rulesets = %+v, 全部 = %+v", rules, checks)
	}
}

func TestPreflightUnknownKernelFallsBack(t *testing.T) {
	e, _ := newEngine(t, "mihomo", nil)
	checks := e.Preflight(context.Background(), "bogus", nil)
	if checks[0].Status != StatusFailed {
		t.Fatalf("target = %+v", checks[0])
	}
	if len(checks) != len(Order) {
		t.Fatalf("项数 = %d", len(checks))
	}
}

func TestPreflightContextCanceled(t *testing.T) {
	e, _ := newEngine(t, "mihomo", func(d *Deps) {
		d.Conflicts = func(context.Context) ([]string, error) { return nil, errors.New("boom") }
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checks := e.Preflight(ctx, "mihomo", nil)
	if len(checks) != len(Order) {
		t.Fatalf("项数 = %d", len(checks))
	}
	for _, c := range checks {
		if c.Status == StatusPassed {
			t.Errorf("%s 在取消后不该 passed", c.ID)
		}
	}
}

func TestServiceScriptPath(t *testing.T) {
	if p := ServiceScriptPath(system.InitNone, ""); p != "" {
		t.Errorf("没有服务管理器时 = %q", p)
	}
}
