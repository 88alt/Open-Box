package singbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// DNS 接管钩子:BeforeApply 在替换配置 / 重启内核之前撤销接管,AfterRunning 在内核健康之后对齐。
// 用一份共享的事件记录,检查「撤销 → 重启 → 对齐」的先后顺序。

type events struct{ list []string }

func (e *events) add(s string)   { e.list = append(e.list, s) }
func (e *events) String() string { return strings.Join(e.list, ",") }

// recSvc 把服务调用也记进同一份事件。
func recSvc(ev *events, running bool) *fakeSvc {
	svc := &fakeSvc{running: running}
	svc.doFn = func(action string, n int) (r system.ActionResult, err error, handled bool) {
		ev.add(action)
		return r, nil, false
	}
	return svc
}

func TestDeployDNSHooksOrderOnSuccess(t *testing.T) {
	ev := &events{}
	svc := recSvc(ev, true)
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	writeCfg(t, d, "old")
	d.BeforeApply = func(context.Context) error { ev.add("revert"); return nil }
	d.AfterRunning = func(context.Context) string { ev.add("sync"); return "dnsmasq 已改为把查询转发给内核" }
	res := d.Deploy(context.Background())
	if !res.OK || ev.String() != "revert,restart,sync" {
		t.Fatalf("顺序应是 撤销 → 重启 → 对齐: %s %+v", ev, res)
	}
	if !strings.HasPrefix(res.Message, "已应用并正常运行") || !strings.Contains(res.Message, "转发给内核") {
		t.Fatalf("对齐的结果要附在消息后面: %q", res.Message)
	}
}

func TestDeployDNSNoteEmptyKeepsMessage(t *testing.T) {
	svc := &fakeSvc{}
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	d.AfterRunning = func(context.Context) string { return "" }
	if res := d.Deploy(context.Background()); !res.OK || res.Message != "已应用并正常运行" {
		t.Fatalf("没话可说时消息不变: %+v", res)
	}
}

func TestDeployBeforeApplyFailureTouchesNothing(t *testing.T) {
	ev := &events{}
	svc := recSvc(ev, true)
	d := newDP(t, map[string]any{"marker": "new"}, svc)
	old := writeCfg(t, d, "old")
	d.BeforeApply = func(context.Context) error { return errors.New("撤销 DNS 接管失败: 删不掉") }
	d.AfterRunning = func(context.Context) string { ev.add("sync"); return "" }
	res := d.Deploy(context.Background())
	if res.OK || res.Stage != StageError || res.RolledBack || !strings.Contains(res.Message, "没有改动") || !strings.Contains(res.Message, "删不掉") {
		t.Fatalf("%+v", res)
	}
	if !bytes.Equal(readCfg(t, d), old) || ev.String() != "" {
		t.Fatalf("撤销失败时不能换配置、不能动内核、不能再对齐: %s %q", readCfg(t, d), ev)
	}
	if _, err := os.Stat(d.PrevPath()); err == nil {
		t.Fatal("没到替换那一步,不该产生备份")
	}
}

func TestDeployValidateFailureDoesNotRevertDNS(t *testing.T) {
	ev := &events{}
	svc := recSvc(ev, true)
	d := newDP(t, map[string]any{"marker": "BAD"}, svc) // 假 sing-box 见到 BAD 校验失败
	writeCfg(t, d, "old")
	d.BeforeApply = func(context.Context) error { ev.add("revert"); return nil }
	d.AfterRunning = func(context.Context) string { ev.add("sync"); return "" }
	if res := d.Deploy(context.Background()); res.Stage != StageValidate {
		t.Fatalf("%+v", res)
	}
	if ev.String() != "" {
		t.Fatalf("校验没过就没碰内核,不该白白撤销接管: %s", ev)
	}
}

func TestDeployRollbackRestoresDNSWhenOldKernelHealthy(t *testing.T) {
	ev := &events{}
	svc := recSvc(ev, true)
	d := newDP(t, map[string]any{"marker": "bad"}, svc) // 新配置健康检查不过
	old := writeCfg(t, d, "old")
	d.BeforeApply = func(context.Context) error { ev.add("revert"); return nil }
	d.AfterRunning = func(context.Context) string { ev.add("sync"); return "dnsmasq 已改为把查询转发给内核" }
	res := d.Deploy(context.Background())
	if res.OK || !res.RolledBack || !bytes.Equal(readCfg(t, d), old) {
		t.Fatalf("%+v", res)
	}
	if ev.String() != "revert,restart,restart,sync" {
		t.Fatalf("回滚后旧内核又健康了,撤销过的接管要接回去: %s", ev)
	}
	if !strings.Contains(res.Message, "内核已恢复运行") || !strings.Contains(res.Message, "转发给内核") {
		t.Fatalf("%q", res.Message)
	}
}

func TestDeployRollbackWithoutRunningKernelDoesNotSyncDNS(t *testing.T) {
	// 部署前内核没在运行(第一次部署):回滚后保持停止,没有内核可接,不该去对齐 DNS
	ev := &events{}
	svc := recSvc(ev, false)
	d := newDP(t, map[string]any{"marker": "bad"}, svc)
	d.BeforeApply = func(context.Context) error { ev.add("revert"); return nil }
	d.AfterRunning = func(context.Context) string { ev.add("sync"); return "" }
	res := d.Deploy(context.Background())
	if res.OK || !res.RolledBack {
		t.Fatalf("%+v", res)
	}
	if strings.Contains(ev.String(), "sync") {
		t.Fatalf("内核已回到停止状态,不能再接管 DNS: %s", ev)
	}
}
