package system

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFindConflictsOpenWrt(t *testing.T) {
	var ran []string
	installed := map[string]bool{"/etc/init.d/openclash": true, "/etc/init.d/passwall": true, "/etc/init.d/sing-box": true, "/etc/init.d/clash-meta": true}
	run := func(_ context.Context, name string, args ...string) (RunResult, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		if name == "/etc/init.d/openclash" || name == "/etc/init.d/clash-meta" || name == "/etc/init.d/sing-box" {
			return RunResult{Code: 0}, nil // 在跑
		}
		return RunResult{Code: 1}, nil
	}
	d := &ConflictDetector{
		Svc:     &Services{Kind: InitOpenWrt, Run: run},
		Exists:  func(p string) bool { return installed[p] },
		Exclude: []string{"sing-box", "openbox-backend"},
	}
	got, err := d.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// openclash 在跑;passwall 装了但没跑不算;clash 的别名 clash-meta 在跑;sing-box 被排除
	if len(got) != 2 || got[0] != (Conflict{"openclash", "OpenClash", true}) || got[1].ID != "clash" {
		t.Fatalf("%+v", got)
	}
	// 只问装了的服务,而且只问 running(不动任何东西)
	for _, r := range ran {
		if !strings.HasSuffix(r, " running") || strings.Contains(r, "sing-box") || strings.Contains(r, "homeproxy") {
			t.Errorf("不该运行的命令: %s", r)
		}
	}
	if len(ran) != 3 {
		t.Fatalf("应只查 openclash / passwall / clash-meta: %v", ran)
	}
}

func TestFindConflictsOpenWrtErrorsAreBestEffort(t *testing.T) {
	run := func(_ context.Context, name string, _ ...string) (RunResult, error) {
		if name == "/etc/init.d/openclash" {
			return RunResult{}, errors.New("exec 失败")
		}
		return RunResult{Code: 0}, nil
	}
	d := &ConflictDetector{
		Svc:        &Services{Kind: InitOpenWrt, Run: run},
		Exists:     func(p string) bool { return p == "/etc/init.d/openclash" || p == "/etc/init.d/passwall" },
		Candidates: KnownConflicts,
	}
	got, err := d.Find(context.Background())
	if err == nil || len(got) != 1 || got[0].ID != "passwall" {
		t.Fatalf("一个查不到不该影响别的: %+v %v", got, err)
	}
	// 没有 Exists:一个都不查
	d.Exists = nil
	if got, err := d.Find(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestFindConflictsSystemdOneCommand(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, name string, args ...string) (RunResult, error) {
		calls = append(calls, append([]string{name}, args...))
		// 按查询顺序每个单元一行;名单里第 7 个是 mihomo
		var sb strings.Builder
		for _, a := range args[1:] {
			if a == "mihomo" || a == "xray" {
				sb.WriteString("active\n")
			} else {
				sb.WriteString("inactive\n")
			}
		}
		return RunResult{Stdout: sb.String(), Code: 3}, nil // 有不在跑的单元时 systemctl 退出码非 0,不是错误
	}
	d := &ConflictDetector{Svc: &Services{Kind: InitSystemd, Run: run}, Exclude: []string{"nikki"}}
	got, err := d.Find(context.Background())
	if err != nil || len(got) != 2 || got[0].ID != "mihomo" || got[1].ID != "xray" {
		t.Fatalf("%+v %v", got, err)
	}
	if len(calls) != 1 || calls[0][0] != "systemctl" || calls[0][1] != "is-active" {
		t.Fatalf("应只调一次 systemctl is-active: %v", calls)
	}
	for _, a := range calls[0] {
		if a == "nikki" {
			t.Fatal("被排除的名字不该查")
		}
	}
}

func TestFindConflictsSystemdBadOutputAndNone(t *testing.T) {
	bad := func(context.Context, string, ...string) (RunResult, error) { return RunResult{Stdout: "active\n"}, nil }
	got, err := (&ConflictDetector{Svc: &Services{Kind: InitSystemd, Run: bad}}).Find(context.Background())
	if err == nil || len(got) != 0 {
		t.Fatalf("输出对不上不能当作有冲突: %+v %v", got, err)
	}
	for _, d := range []*ConflictDetector{nil, {}, {Svc: &Services{Kind: InitNone}}} {
		if got, err := d.Find(context.Background()); err != nil || got == nil || len(got) != 0 {
			t.Fatalf("没有服务管理器应返回空数组: %+v %v", got, err)
		}
	}
}

func TestFindConflictsSkipsInvalidNames(t *testing.T) {
	run := func(context.Context, string, ...string) (RunResult, error) {
		t.Fatal("名字不合法不该运行命令")
		return RunResult{}, nil
	}
	d := &ConflictDetector{Svc: &Services{Kind: InitSystemd, Run: run}, Candidates: []ConflictCandidate{{ID: "x", Label: "X", Names: []string{"--now", "a b", ""}}}}
	if got, err := d.Find(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestFindConflictsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	run := func(context.Context, string, ...string) (RunResult, error) { cancel(); return RunResult{}, ctx.Err() }
	d := &ConflictDetector{Svc: &Services{Kind: InitOpenWrt, Run: run}, Exists: func(string) bool { return true }}
	if _, err := d.Find(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}
