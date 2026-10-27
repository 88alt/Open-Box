package system

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type call struct {
	name string
	args string
}

// fakeRunner 按「命令 + 参数」查预设的结果,并记录调用。
func fakeRunner(res map[string]RunResult, calls *[]call) Runner {
	return func(ctx context.Context, name string, args ...string) (RunResult, error) {
		key := name + " " + strings.Join(args, " ")
		*calls = append(*calls, call{name, strings.Join(args, " ")})
		if r, ok := res[key]; ok {
			return r, nil
		}
		return RunResult{Code: 1}, nil
	}
}

func TestValidServiceName(t *testing.T) {
	for _, ok := range []string{"sing-box", "openbox-backend", "a", "a.b_c@d", "sing-box@1"} {
		if err := ValidServiceName(ok); err != nil {
			t.Errorf("%q 应合法: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-rf", "a b", "a;b", "a/b", "a$(x)", "a\nb", strings.Repeat("a", 65), "../x"} {
		if err := ValidServiceName(bad); !errors.Is(err, ErrBadName) {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
}

func TestDetectInit(t *testing.T) {
	has := func(paths ...string) func(string) bool {
		m := map[string]bool{}
		for _, p := range paths {
			m[p] = true
		}
		return func(p string) bool { return m[p] }
	}
	found := func(string) (string, error) { return "/bin/systemctl", nil }
	missing := func(string) (string, error) { return "", errors.New("no") }
	if got := DetectInit(has("/etc/openwrt_release", "/etc/init.d"), missing); got != InitOpenWrt {
		t.Errorf("openwrt: %s", got)
	}
	if got := DetectInit(has("/run/systemd/system"), found); got != InitSystemd {
		t.Errorf("systemd: %s", got)
	}
	// 装了 systemctl 但没在用 systemd 跑(比如容器)→ none
	if got := DetectInit(has(), found); got != InitNone {
		t.Errorf("none: %s", got)
	}
	// OpenWrt 判断优先
	if got := DetectInit(has("/etc/openwrt_release", "/etc/init.d", "/run/systemd/system"), found); got != InitOpenWrt {
		t.Errorf("openwrt 优先: %s", got)
	}
}

func TestStatusOpenWrt(t *testing.T) {
	var calls []call
	s := &Services{Kind: InitOpenWrt, Run: fakeRunner(map[string]RunResult{
		"/etc/init.d/sing-box running": {Code: 0},
		"/etc/init.d/sing-box enabled": {Code: 0},
	}, &calls)}
	info, err := s.Status(context.Background(), "sing-box")
	if err != nil || !info.Running || info.Raw != "running" || info.Autostart == nil || !*info.Autostart {
		t.Fatalf("%+v %v", info, err)
	}
	// 没在跑、没开自启
	s.Run = fakeRunner(map[string]RunResult{}, &calls)
	info, _ = s.Status(context.Background(), "sing-box")
	if info.Running || info.Raw != "inactive" || info.Autostart == nil || *info.Autostart {
		t.Fatalf("%+v", info)
	}
}

func TestStatusSystemd(t *testing.T) {
	var calls []call
	s := &Services{Kind: InitSystemd, ProcUptime: func() (float64, error) { return 1000.5, nil }, Run: fakeRunner(map[string]RunResult{
		"systemctl is-active sing-box":                                             {Stdout: "active\n"},
		"systemctl is-enabled sing-box":                                            {Stdout: "enabled\n"},
		"systemctl show sing-box --property=ActiveEnterTimestampMonotonic --value": {Stdout: "400000000\n"},
	}, &calls)}
	info, err := s.Status(context.Background(), "sing-box")
	if err != nil || !info.Running || info.Raw != "active" {
		t.Fatalf("%+v %v", info, err)
	}
	if info.UptimeSeconds == nil || *info.UptimeSeconds != 600 {
		t.Fatalf("uptime=%v", info.UptimeSeconds)
	}
	// 非 active:不查运行时长
	calls = nil
	s.Run = fakeRunner(map[string]RunResult{"systemctl is-active sing-box": {Stdout: "failed\n", Code: 3}}, &calls)
	info, _ = s.Status(context.Background(), "sing-box")
	if info.Running || info.Raw != "failed" || info.UptimeSeconds != nil {
		t.Fatalf("%+v", info)
	}
	for _, c := range calls {
		if strings.Contains(c.args, "show") {
			t.Fatalf("没在跑不该查运行时长: %+v", calls)
		}
	}
}

func TestStatusNoInitAndBadName(t *testing.T) {
	s := &Services{Kind: InitNone, Run: func(context.Context, string, ...string) (RunResult, error) { return RunResult{}, nil }}
	if _, err := s.Status(context.Background(), "sing-box"); !errors.Is(err, ErrNoInit) {
		t.Fatalf("err=%v", err)
	}
	if _, err := s.Status(context.Background(), "a;b"); !errors.Is(err, ErrBadName) {
		t.Fatalf("err=%v", err)
	}
}

func TestDo(t *testing.T) {
	var calls []call
	run := fakeRunner(map[string]RunResult{
		"/etc/init.d/sing-box restart": {Code: 0},
		"systemctl start sing-box":     {Code: 1, Stderr: "Job failed\n"},
	}, &calls)
	ow := &Services{Kind: InitOpenWrt, Run: run}
	r, err := ow.Do(context.Background(), "sing-box", "restart")
	if err != nil || !r.OK || r.Code != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	sd := &Services{Kind: InitSystemd, Run: run}
	r, err = sd.Do(context.Background(), "sing-box", "start")
	if err != nil || r.OK || r.Code != 1 || r.Stderr != "Job failed" {
		t.Fatalf("失败应带退出码和 stderr: %+v %v", r, err)
	}
	if calls[len(calls)-1].name != "systemctl" || calls[len(calls)-1].args != "start sing-box" {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestDoRejects(t *testing.T) {
	ran := false
	s := &Services{Kind: InitSystemd, Run: func(context.Context, string, ...string) (RunResult, error) { ran = true; return RunResult{}, nil }}
	for _, bad := range []string{"", "reload", "kill", "start;reboot", "RESTART", "mask"} {
		if _, err := s.Do(context.Background(), "sing-box", bad); !errors.Is(err, ErrBadAction) {
			t.Errorf("%q 应拒绝: %v", bad, err)
		}
	}
	if _, err := s.Do(context.Background(), "--now", "start"); !errors.Is(err, ErrBadName) {
		t.Errorf("以 - 开头的服务名应拒绝: %v", err)
	}
	if _, err := (&Services{Kind: InitNone, Run: s.Run}).Do(context.Background(), "x", "start"); !errors.Is(err, ErrNoInit) {
		t.Errorf("没有服务管理器: %v", err)
	}
	if ran {
		t.Fatal("被拒绝的请求不该运行任何命令")
	}
}

func TestDoCommandCannotStartAndTimeout(t *testing.T) {
	s := &Services{Kind: InitSystemd, Run: func(context.Context, string, ...string) (RunResult, error) {
		return RunResult{}, errors.New("exec: not found")
	}}
	r, err := s.Do(context.Background(), "sing-box", "start")
	if err != nil || r.OK || r.Code != -1 || !strings.Contains(r.Stderr, "not found") {
		t.Fatalf("%+v %v", r, err)
	}
	slow := &Services{Kind: InitSystemd, ActionTimeout: 20 * time.Millisecond, Run: func(ctx context.Context, _ string, _ ...string) (RunResult, error) {
		<-ctx.Done()
		return RunResult{}, ctx.Err()
	}}
	r, _ = slow.Do(context.Background(), "sing-box", "restart")
	if r.OK || !strings.Contains(r.Stderr, "超时") {
		t.Fatalf("超时: %+v", r)
	}
}

func TestExecRunnerExitCode(t *testing.T) {
	res, err := ExecRunner(context.Background(), "sh", "-c", "echo out; echo err >&2; exit 3")
	if err != nil || res.Code != 3 || strings.TrimSpace(res.Stdout) != "out" || strings.TrimSpace(res.Stderr) != "err" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := ExecRunner(context.Background(), "/no/such/binary"); err == nil {
		t.Fatal("启动不了应返回 error")
	}
}
