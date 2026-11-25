package deploykernel

import (
	"context"
	"errors"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/mihomo"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

type fakeMihomo struct {
	res  mihomo.DeployResult
	call int
}

func (f *fakeMihomo) Deploy(context.Context) mihomo.DeployResult { f.call++; return f.res }

func okRunner(res *int) Runner {
	return func(context.Context) singbox.DeployResult {
		*res++
		return singbox.DeployResult{OK: true, Stage: singbox.StageRunning}
	}
}

// 选中 mihomo 时必须走 mihomo 那条,不能还写 sing-box 的 config.json(真机上踩过)。
func TestDispatchMihomo(t *testing.T) {
	sbCalls := 0
	mh := &fakeMihomo{res: mihomo.DeployResult{OK: true, Stage: mihomo.MihomoStageRunning}}
	d := &Deployer{Kernel: func() (string, error) { return system.KernelMihomo, nil }, SingBox: okRunner(&sbCalls), Mihomo: mh}
	r := d.Deploy(context.Background())
	if !r.OK || r.Stage != singbox.StageRunning || mh.call != 1 || sbCalls != 0 {
		t.Fatalf("%+v mihomo=%d singbox=%d", r, mh.call, sbCalls)
	}
}

// 选中 sing-box(或文件不存在 = 默认)时走原流程。
func TestDispatchSingBox(t *testing.T) {
	for _, k := range []string{system.KernelSingBox, ""} {
		sbCalls := 0
		mh := &fakeMihomo{}
		d := &Deployer{Kernel: func() (string, error) { return k, nil }, SingBox: okRunner(&sbCalls), Mihomo: mh}
		r := d.Deploy(context.Background())
		if !r.OK || sbCalls != 1 || mh.call != 0 {
			t.Fatalf("kernel=%q %+v sb=%d mh=%d", k, r, sbCalls, mh.call)
		}
	}
}

// 读不出内核就拒绝,不猜。
func TestDispatchKernelUnreadable(t *testing.T) {
	sbCalls := 0
	mh := &fakeMihomo{}
	d := &Deployer{Kernel: func() (string, error) { return "", errors.New("坏文件") }, SingBox: okRunner(&sbCalls), Mihomo: mh}
	r := d.Deploy(context.Background())
	if r.OK || r.Stage != singbox.StageError || sbCalls != 0 || mh.call != 0 {
		t.Fatalf("%+v sb=%d mh=%d", r, sbCalls, mh.call)
	}
}

// 缺少对应那条流程时如实报错,不悄悄退回另一条(退回会写错文件、打断内核)。
func TestDispatchMissingPipeline(t *testing.T) {
	d := &Deployer{Kernel: func() (string, error) { return system.KernelMihomo, nil }}
	if r := d.Deploy(context.Background()); r.OK || r.Stage != singbox.StageError {
		t.Fatalf("mihomo 缺流程应报错: %+v", r)
	}
	d2 := &Deployer{Kernel: func() (string, error) { return system.KernelSingBox, nil }}
	if r := d2.Deploy(context.Background()); r.OK || r.Stage != singbox.StageError {
		t.Fatalf("sing-box 缺流程应报错: %+v", r)
	}
}
