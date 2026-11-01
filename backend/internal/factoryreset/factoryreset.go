// Package factoryreset 实现「恢复出厂设置」:把后端存的一切用户数据清回刚装好的样子。
//
// 自己的设计,依据前端 factoryReset / OpenboxFactoryResetResult 的类型和 FactoryResetButton.vue 里的说明(整张表清空:订阅、节点、
// 分流、面板设置、背景图、面板密码、登录会话,统计清零,按随包默认重新播种,内核停掉;不可恢复),没有参照上游。
//
// 做法(变更安全:先停、再清、密码最后):
//  1. 同一时刻只做一次;和「部署」互斥(部署在进行就不动,回 409)。
//  2. 先把内核停下来:先撤销 DNS 接管(dnsmasq 不能还指着一个停了的内核),再停服务,再确认真的停了。
//     这一步任何一处不成功,就**一份数据都不删**,如实报错——数据还在、面板还能用,用户可以排查后重试。
//     读不到服务管理器(开发机、没有 procd / systemd)时管不了内核,跳过这一步,kernelStopped 是 false。
//  3. 逐项清数据(Wipes)。一项失败不拦后面的:已经开始清了,留一半比清到底更糟。
//  4. 最后才清访问密码(Final)。Wipes 里只要有一项失败就**不清密码**:面板不能在旧数据还留着的时候变成没有密码。
//
// 「随包默认」不用写任何东西:档案、节点组在没存过时本来就按默认值算(profile.effective、nodegroups.stored),删掉存的文件就是重新播种。
package factoryreset

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// ErrBusy:已经有一次恢复出厂设置在进行,或部署正在进行(HTTP 409)。
var ErrBusy = errors.New("正在部署或已有一次恢复出厂设置在进行,请等它结束再试")

// StopError:内核没能停下来,没有删任何数据(HTTP 500)。
type StopError struct{ Msg string }

func (e *StopError) Error() string {
	return "为安全起见没有清除任何数据:" + e.Msg
}

// PartialError:清数据的过程中有项失败。已经清掉的回不来;访问密码没有清(要重试请再点一次)。
type PartialError struct {
	Done   []string // 已经清掉的项
	Failed []string // 失败的项:名称加原因
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("恢复出厂设置没有做完(访问密码没有清除,可以再试一次)。已清:%s;失败:%s",
		strings.Join(e.Done, "、"), strings.Join(e.Failed, ";"))
}

// Controller 是读 / 停内核服务的最小接口(*system.Services 满足),测试里换假的。
type Controller interface {
	Status(ctx context.Context, name string) (system.ServiceInfo, error)
	Do(ctx context.Context, name, action string) (system.ActionResult, error)
}

// DNSRevert 在停内核之前撤销 DNS 接管(*dnssync.Coordinator 满足)。
type DNSRevert interface {
	BeforeKernelStop(ctx context.Context) error
}

// Step 是清数据的一步。Name 是给人看的名字,出现在失败报告里。
type Step struct {
	Name string
	Run  func(ctx context.Context) error
}

// Result 对应前端的 OpenboxFactoryResetResult。
type Result struct {
	OK bool `json:"ok"`
	// Seeded 是回到随包默认的数据集个数(档案、节点组;它们本来就是没存过就按默认算,所以固定是 2)。前端不读这个字段。
	Seeded        int  `json:"seeded"`
	ProfileSeeded bool `json:"profileSeeded"`
	// KernelStopped:清完时内核服务确认不在运行。管不了内核(没有服务管理器)时是 false。
	KernelStopped bool `json:"kernelStopped"`
}

// seededSets 是 Result.Seeded 的值:档案和节点组。
const seededSets = 2

// Service 执行恢复出厂设置。
type Service struct {
	Services    Controller // 为空 = 不管内核
	CoreService string     // sing-box 的服务名
	DNS         DNSRevert  // 为空 = 不管 DNS 接管

	// Exclusive 在部署的锁里执行 fn;部署正在进行时不等待、返回 ErrBusy(*singbox.Deployer.Exclusive 的包装)。为空 = 不互斥。
	Exclusive func(fn func() error) error

	Wipes []Step // 清数据的步骤,按顺序,全做
	Final Step   // 最后一步(清访问密码);Wipes 有失败就不做。Run 为空 = 没有这一步
	Log   *slog.Logger

	mu sync.Mutex
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run 执行一次恢复出厂设置。错误类型:ErrBusy(409)、*StopError(没动数据)、*PartialError(清了一半)。
func (s *Service) Run(ctx context.Context) (Result, error) {
	if !s.mu.TryLock() {
		return Result{}, ErrBusy
	}
	defer s.mu.Unlock()
	if s.Exclusive == nil {
		return s.run(ctx)
	}
	var res Result
	err := s.Exclusive(func() error {
		var err error
		res, err = s.run(ctx)
		return err
	})
	return res, err
}

func (s *Service) run(ctx context.Context) (Result, error) {
	stopped, err := s.stopKernel(ctx)
	if err != nil {
		s.log().Warn("恢复出厂设置:内核没能停下来,没有清除任何数据", "error", err)
		return Result{}, &StopError{Msg: err.Error()}
	}

	var done, failed []string
	for _, st := range s.Wipes {
		if err := st.Run(ctx); err != nil {
			s.log().Error("恢复出厂设置:清除失败", "step", st.Name, "error", err)
			failed = append(failed, st.Name+":"+err.Error())
			continue
		}
		done = append(done, st.Name)
	}
	if len(failed) > 0 {
		return Result{}, &PartialError{Done: done, Failed: failed}
	}
	if s.Final.Run != nil {
		if err := s.Final.Run(ctx); err != nil {
			s.log().Error("恢复出厂设置:清除访问密码失败", "error", err)
			return Result{}, &PartialError{Done: done, Failed: []string{s.Final.Name + ":" + err.Error()}}
		}
	}
	s.log().Info("已恢复出厂设置", "kernelStopped", stopped)
	return Result{OK: true, Seeded: seededSets, ProfileSeeded: true, KernelStopped: stopped}, nil
}

// stopKernel 撤销 DNS 接管并停掉内核服务,确认停了才返回 true。管不了内核返回 (false, nil);任何一处不确定都返回 error。
func (s *Service) stopKernel(ctx context.Context) (bool, error) {
	if s.Services == nil {
		return false, nil
	}
	info, err := s.Services.Status(ctx, s.CoreService)
	if errors.Is(err, system.ErrNoInit) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读不到内核服务的状态:%w", err)
	}
	// 内核已经停了也要撤销:接管留在 dnsmasq 里指着一个没在运行的内核,整个局域网就没有 DNS
	if s.DNS != nil {
		if err := s.DNS.BeforeKernelStop(ctx); err != nil {
			return false, err
		}
	}
	if !info.Running {
		return true, nil
	}
	res, err := s.Services.Do(ctx, s.CoreService, "stop")
	if err != nil {
		return false, fmt.Errorf("停止内核失败:%w", err)
	}
	if !res.OK {
		return false, fmt.Errorf("停止内核失败(退出码 %d):%s", res.Code, res.Stderr)
	}
	after, err := s.Services.Status(ctx, s.CoreService)
	if err != nil {
		return false, fmt.Errorf("停止内核后读不到服务状态:%w", err)
	}
	if after.Running {
		return false, errors.New("已发出停止命令,内核仍在运行")
	}
	return true, nil
}

// RemoveFiles 返回一个清数据的步骤:删掉这些文件,不存在不算错,删不掉的合成一个 error。路径不能为空。
func RemoveFiles(name string, paths ...string) Step {
	return Step{Name: name, Run: func(context.Context) error {
		var errs []error
		for _, p := range paths {
			if p == "" {
				errs = append(errs, errors.New("路径为空"))
				continue
			}
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}}
}

// ClearDirs 返回一个清数据的步骤:清空这些目录里的全部内容,目录本身留着(别的代码还要往里写)。目录不存在不算错。
// 空路径和文件系统根目录一律拒绝,不会去清。
func ClearDirs(name string, dirs ...string) Step {
	return Step{Name: name, Run: func(context.Context) error {
		var errs []error
		for _, d := range dirs {
			if d == "" || filepath.Dir(filepath.Clean(d)) == filepath.Clean(d) {
				errs = append(errs, fmt.Errorf("拒绝清理目录 %q", d))
				continue
			}
			entries, err := os.ReadDir(d)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, e := range entries {
				if err := os.RemoveAll(filepath.Join(d, e.Name())); err != nil {
					errs = append(errs, err)
				}
			}
		}
		return errors.Join(errs...)
	}}
}
