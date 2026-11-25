// Package cleanup 实现 `openbox-backend cleanup`:卸载前把后端对系统做过的改动撤销掉。
//
// 为什么单独做一个子命令(M6 第五节):卸载脚本要先把「DNS 还指着内核」「虚拟终端还在」这类改动收干净,
// 再删二进制。如果脚本自己去拆,顺序和判断条件就和后端里的那份逻辑各写一遍,两边一定会漂;
// 让后端自己拆,脚本只看退出码。
//
// 顺序(与 internal/factoryreset 停内核那一步同源,不能颠倒):
//  1. 撤销 DNS 接管。内核已经停了也要撤——dnsmasq 还指着一个不存在的上游,整个局域网就没 DNS 了。
//  2. 停内核服务并确认真的停了(读一次状态,不信命令的退出码)。
//  3. 拆虚拟终端(网络命名空间 + veth + 状态文件),可重复执行。
//
// 任何一步失败都立刻返回 error,后面的步骤不再做:卸载脚本看到非 0 就停下,不继续删文件。
// 读不到服务管理器(开发机、没有 procd / systemd)时跳过 1~2 里的停服务部分,不算失败。
package cleanup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Controller 是读 / 停内核服务的最小接口(*system.Services 满足),测试里换假的。
type Controller interface {
	Status(ctx context.Context, name string) (system.ServiceInfo, error)
	Do(ctx context.Context, name, action string) (system.ActionResult, error)
}

// DNSRevert 撤销 DNS 接管(*dnssync.Coordinator 满足)。
type DNSRevert interface {
	BeforeKernelStop(ctx context.Context) error
}

// Teardowner 拆虚拟终端(*system.VTermNet 满足)。可重复执行,没有遗留什么也不做。
type Teardowner interface {
	Teardown(ctx context.Context) error
}

// Result 是这次清理做了什么,给命令行输出用。
type Result struct {
	// DNSReverted:调过撤销 DNS 接管并且成功(没有接管过也算成功)。
	DNSReverted bool `json:"dnsReverted"`
	// KernelWasRunning:动手前内核在运行。
	KernelWasRunning bool `json:"kernelWasRunning"`
	// KernelStopped:结束时确认内核不在运行。管不了服务管理器时是 false。
	KernelStopped bool `json:"kernelStopped"`
	// TerminalRemoved:拆虚拟终端这一步跑过并且成功。
	TerminalRemoved bool `json:"terminalRemoved"`
	// Managed:读到了服务管理器(OpenWrt 的 init.d / systemd)。false 表示内核服务管不了,已跳过。
	Managed bool `json:"managed"`
}

// Service 执行一次卸载前清理。
type Service struct {
	Services    Controller // 为空 = 不管内核服务
	CoreService string     // 内核的服务名
	DNS         DNSRevert  // 为空 = 不管 DNS 接管
	VTerm       Teardowner // 为空 = 不管虚拟终端
	Log         *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run 执行清理。错误已经带上是哪一步失败的,调用方直接打到 stderr 即可。
func (s *Service) Run(ctx context.Context) (Result, error) {
	var res Result

	// 1. 先看一眼内核在不在跑:要在撤销 DNS 之前读,撤销本身可能让内核重载配置
	var (
		running bool
		managed bool
	)
	if s.Services != nil {
		info, err := s.Services.Status(ctx, s.CoreService)
		switch {
		case errors.Is(err, system.ErrNoInit):
			// 没有服务管理器:开发机 / 容器里跑,内核服务这一步管不了,不算失败
			s.log().Warn("没有服务管理器,跳过内核服务", "err", err)
		case err != nil:
			return res, fmt.Errorf("读不到内核服务的状态:%w", err)
		default:
			managed, running = true, info.Running
		}
	}
	res.Managed = managed
	res.KernelWasRunning = running

	// 2. 撤销 DNS 接管。内核已经停了也要撤:接管留在 dnsmasq 里指着一个没在运行的内核,整个局域网就没有 DNS
	if s.DNS != nil {
		if err := s.DNS.BeforeKernelStop(ctx); err != nil {
			return res, err
		}
		res.DNSReverted = true
	}

	// 3. 停内核并确认
	if managed {
		if running {
			out, err := s.Services.Do(ctx, s.CoreService, "stop")
			if err != nil {
				return res, fmt.Errorf("停止内核失败:%w", err)
			}
			if !out.OK {
				return res, fmt.Errorf("停止内核失败(退出码 %d):%s", out.Code, out.Stderr)
			}
		}
		after, err := s.Services.Status(ctx, s.CoreService)
		if err != nil {
			return res, fmt.Errorf("停止内核后读不到服务状态:%w", err)
		}
		if after.Running {
			return res, errors.New("已发出停止命令,内核仍在运行")
		}
		res.KernelStopped = true
	}

	// 4. 拆虚拟终端。放在最后:拆之前内核已经停了,不会再有探测占着命名空间
	if s.VTerm != nil {
		if err := s.VTerm.Teardown(ctx); err != nil {
			return res, fmt.Errorf("拆虚拟终端失败:%w", err)
		}
		res.TerminalRemoved = true
	}

	s.log().Info("卸载前清理完成", "dns", res.DNSReverted, "kernelStopped", res.KernelStopped, "terminal", res.TerminalRemoved)
	return res, nil
}
