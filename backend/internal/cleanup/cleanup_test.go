package cleanup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

type fakeCtl struct {
	statusErr error
	doErr     error
	doRes     system.ActionResult
	// running 是当前状态;stop 成功后置 false
	running bool
	// stick 为真时 stop 之后仍报 running(模拟停不下来)
	stick bool
	calls []string
}

func (f *fakeCtl) Status(context.Context, string) (system.ServiceInfo, error) {
	if f.statusErr != nil {
		return system.ServiceInfo{}, f.statusErr
	}
	return system.ServiceInfo{Running: f.running}, nil
}

func (f *fakeCtl) Do(_ context.Context, name, action string) (system.ActionResult, error) {
	f.calls = append(f.calls, name+":"+action)
	if f.doErr != nil {
		return system.ActionResult{}, f.doErr
	}
	if f.doRes.OK && action == "stop" && !f.stick {
		f.running = false
	}
	return f.doRes, nil
}

type fakeDNS struct {
	err   error
	calls int
}

func (f *fakeDNS) BeforeKernelStop(context.Context) error {
	f.calls++
	return f.err
}

type fakeVTerm struct {
	err   error
	calls int
}

func (f *fakeVTerm) Teardown(context.Context) error {
	f.calls++
	return f.err
}

func TestRunStopsEverything(t *testing.T) {
	ctl := &fakeCtl{running: true, doRes: system.ActionResult{OK: true}}
	dns := &fakeDNS{}
	vt := &fakeVTerm{}

	res, err := (&Service{Services: ctl, CoreService: "openbox", DNS: dns, VTerm: vt}).Run(context.Background())
	if err != nil {
		t.Fatalf("不该失败:%v", err)
	}
	if !res.Managed || !res.KernelWasRunning || !res.KernelStopped || !res.DNSReverted || !res.TerminalRemoved {
		t.Fatalf("结果不对:%+v", res)
	}
	if len(ctl.calls) != 1 || ctl.calls[0] != "openbox:stop" {
		t.Fatalf("应该只停一次内核:%v", ctl.calls)
	}
	if dns.calls != 1 || vt.calls != 1 {
		t.Fatalf("DNS 撤销 %d 次、拆终端 %d 次,都该是 1", dns.calls, vt.calls)
	}
}

// 内核本来就没跑:不发 stop,但 DNS 接管仍然要撤(dnsmasq 可能还指着内核)
func TestRunRevertsDNSWhenKernelAlreadyStopped(t *testing.T) {
	ctl := &fakeCtl{running: false}
	dns := &fakeDNS{}

	res, err := (&Service{Services: ctl, CoreService: "openbox", DNS: dns}).Run(context.Background())
	if err != nil {
		t.Fatalf("不该失败:%v", err)
	}
	if res.KernelWasRunning || !res.KernelStopped || !res.DNSReverted {
		t.Fatalf("结果不对:%+v", res)
	}
	if len(ctl.calls) != 0 {
		t.Fatalf("内核没跑就不该发命令:%v", ctl.calls)
	}
	if dns.calls != 1 {
		t.Fatalf("DNS 该撤销 1 次,实际 %d", dns.calls)
	}
}

// 没有服务管理器(开发机 / 容器):跳过内核那两步,不算失败,DNS 与虚拟终端照做
func TestRunWithoutInit(t *testing.T) {
	ctl := &fakeCtl{statusErr: system.ErrNoInit}
	dns := &fakeDNS{}
	vt := &fakeVTerm{}

	res, err := (&Service{Services: ctl, CoreService: "openbox", DNS: dns, VTerm: vt}).Run(context.Background())
	if err != nil {
		t.Fatalf("没有服务管理器不该算失败:%v", err)
	}
	if res.Managed || res.KernelStopped {
		t.Fatalf("管不了内核时这两个应为 false:%+v", res)
	}
	if !res.DNSReverted || !res.TerminalRemoved {
		t.Fatalf("DNS 与终端仍该处理:%+v", res)
	}
	if len(ctl.calls) != 0 {
		t.Fatalf("不该发命令:%v", ctl.calls)
	}
}

// DNS 撤销失败:立刻返回,不停内核、不拆终端(卸载脚本要在这里停下)
func TestRunStopsAtDNSFailure(t *testing.T) {
	ctl := &fakeCtl{running: true, doRes: system.ActionResult{OK: true}}
	dns := &fakeDNS{err: errors.New("dnsmasq 起不来")}
	vt := &fakeVTerm{}

	_, err := (&Service{Services: ctl, CoreService: "openbox", DNS: dns, VTerm: vt}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "dnsmasq 起不来") {
		t.Fatalf("应该带着原因失败,实际 %v", err)
	}
	if len(ctl.calls) != 0 {
		t.Fatalf("DNS 没撤成功就不该动内核:%v", ctl.calls)
	}
	if vt.calls != 0 {
		t.Fatalf("DNS 没撤成功就不该拆终端")
	}
}

func TestRunStopFailures(t *testing.T) {
	t.Run("命令报错", func(t *testing.T) {
		ctl := &fakeCtl{running: true, doErr: errors.New("init.d 不存在")}
		_, err := (&Service{Services: ctl, CoreService: "openbox", DNS: &fakeDNS{}}).Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "停止内核失败") {
			t.Fatalf("应该报停止失败,实际 %v", err)
		}
	})
	t.Run("退出码非 0", func(t *testing.T) {
		ctl := &fakeCtl{running: true, doRes: system.ActionResult{Code: 1, Stderr: "no such service"}}
		_, err := (&Service{Services: ctl, CoreService: "openbox", DNS: &fakeDNS{}}).Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no such service") {
			t.Fatalf("应该带上 stderr,实际 %v", err)
		}
	})
	t.Run("停了还在跑", func(t *testing.T) {
		ctl := &fakeCtl{running: true, doRes: system.ActionResult{OK: true}, stick: true}
		_, err := (&Service{Services: ctl, CoreService: "openbox", DNS: &fakeDNS{}}).Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "仍在运行") {
			t.Fatalf("应该报仍在运行,实际 %v", err)
		}
	})
}

// 拆终端失败:前面两步已经做了,如实报错
func TestRunTerminalFailure(t *testing.T) {
	ctl := &fakeCtl{running: true, doRes: system.ActionResult{OK: true}}
	dns := &fakeDNS{}
	vt := &fakeVTerm{err: errors.New("ip 命令失败")}

	res, err := (&Service{Services: ctl, CoreService: "openbox", DNS: dns, VTerm: vt}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ip 命令失败") {
		t.Fatalf("应该带着原因失败,实际 %v", err)
	}
	if !res.DNSReverted || !res.KernelStopped {
		t.Fatalf("失败前做过的事要如实反映:%+v", res)
	}
}

// 什么都不接(纯本地跑):不 panic、不报错
func TestRunNothingConfigured(t *testing.T) {
	res, err := (&Service{}).Run(context.Background())
	if err != nil {
		t.Fatalf("不该失败:%v", err)
	}
	if res.Managed || res.DNSReverted || res.TerminalRemoved {
		t.Fatalf("什么都没接时结果应为空:%+v", res)
	}
}
