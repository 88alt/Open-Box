// Package deploykernel 按 <ConfigDir>/router-kernel 选一条部署流程:
// sing-box 走 singbox.Deployer(写 config.json),mihomo 走 mihomo.Deployer(写 mihomo.yaml)。
//
// 为什么需要这一层:两条流程的配置文件名、控制接口地址与密钥、健康检查方式都不同。早先只有 sing-box
// 一条,于是设备跑 mihomo 时点「部署」会写错文件、重启服务、再用 sing-box 的 secret 做健康检查(必然 401),
// 回滚还要再重启一次内核——真机上把正在用的连接打断过(2026-11-25)。现在按内核分派,并且两条流程
// 各自都做「校验 → 备份 → 原子替换 → 重启 → 健康检查 → 失败回滚」。
package deploykernel

import (
	"context"

	"github.com/timeflysoon/Open-Box/backend/internal/mihomo"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// KernelReader 读当前路由器内核(system.ReadRouterKernel 经闭包传入)。
type KernelReader func() (string, error)

// Runner 是一次部署。
type Runner func(ctx context.Context) singbox.DeployResult

// Deployer 按内核分派。
type Deployer struct {
	// Kernel 读当前内核;nil 时按 sing-box。
	Kernel KernelReader
	// SingBox / Mihomo 是两条流程;缺哪条就在命中它时如实报错,而不是悄悄退回另一条。
	SingBox Runner
	Mihomo  MihomoDeployer
}

// MihomoDeployer 是 mihomo 那条流程的接口(mihomo.Deployer 满足)。
type MihomoDeployer interface {
	Deploy(ctx context.Context) mihomo.DeployResult
}

// Deploy 选一条流程跑。返回类型统一成 singbox.DeployResult(两者字段完全同形,前端只认这个形状)。
func (d *Deployer) Deploy(ctx context.Context) singbox.DeployResult {
	kernel := system.KernelSingBox
	if d.Kernel != nil {
		got, err := d.Kernel()
		if err != nil {
			return singbox.DeployResult{Stage: singbox.StageError, BadTags: []string{},
				Message: "读不到当前路由器内核,为安全起见没有改动:" + err.Error()}
		}
		if got != "" {
			kernel = got
		}
	}
	if kernel == system.KernelMihomo {
		if d.Mihomo == nil {
			return singbox.DeployResult{Stage: singbox.StageError, BadTags: []string{},
				Message: "当前路由器内核是 mihomo,但这条构建没有配置 mihomo 部署流程,没有改动"}
		}
		r := d.Mihomo.Deploy(ctx)
		return singbox.DeployResult{OK: r.OK, Stage: r.Stage, Message: r.Message, BadTags: r.BadTags, RolledBack: r.RolledBack, Busy: r.Busy}
	}
	if d.SingBox == nil {
		return singbox.DeployResult{Stage: singbox.StageError, BadTags: []string{},
			Message: "当前路由器内核是 sing-box,但这条构建没有配置 sing-box 部署流程,没有改动"}
	}
	return d.SingBox(ctx)
}
