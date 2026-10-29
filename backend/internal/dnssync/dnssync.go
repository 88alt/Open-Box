// Package dnssync 把「DNS 接管」(system.DNSTakeover)接进内核的生命周期。
// 自己的设计,依据 STATUS.md「DNS 接管接进部署与启动流程」和 DESIGN 第四节「变更安全」,没有参照上游。
//
// 规则只有三条,都是为了不让局域网失去 DNS:
//   - 内核停止 / 重启 / 被新配置替换之前,先撤销接管(内核停了而 dnsmasq 还指着它,整个局域网就没有 DNS);
//   - 内核(重新)起来之后,按档案 dns.mode 对齐系统:dnsmasq 转发就接管,off / hijack 就保持撤销。
//     内核刚起来时 DNS 入站可能还没应答,这时一个字都没改,等一会儿再试,等不到就放弃并如实说明;
//   - 进程启动时先清上次异常退出遗留的接管(Recover)。
//
// 本包不碰系统:所有系统改动都在 system.DNSTakeover 里(带撤销、带探测),这里只决定「什么时候调它」。
package dnssync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Taker 是用到的接管操作(*system.DNSTakeover 满足),测试里换假的。
type Taker interface {
	Sync(ctx context.Context, mode string) (changed bool, err error)
	Revert(ctx context.Context) error
	Recover(ctx context.Context) error
}

// Coordinator 决定什么时候调 Taker。Taker 和 Mode 必填。
type Coordinator struct {
	Taker Taker
	// Mode 读档案的 dns.mode(off / hijack / dnsmasq)。读不到就返回 error:不确定就不动。
	Mode func(ctx context.Context) (string, error)
	Log  *slog.Logger

	// Wait 是内核起来后等它的 DNS 入站应答的上限(默认 20 秒),Poll 是重试间隔(默认 1 秒)。
	Wait time.Duration
	Poll time.Duration
}

func (c *Coordinator) wait() time.Duration {
	if c.Wait > 0 {
		return c.Wait
	}
	return 20 * time.Second
}

func (c *Coordinator) poll() time.Duration {
	if c.Poll > 0 {
		return c.Poll
	}
	return time.Second
}

func (c *Coordinator) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// BeforeKernelStop 在停止 / 重启内核、或用新配置替换内核之前调用:撤销接管。
// 返回 error 表示没撤销成功(dnsmasq 可能还指着内核),调用方必须不再动内核。没接管过时什么都不做。
func (c *Coordinator) BeforeKernelStop(ctx context.Context) error {
	if err := c.Taker.Revert(ctx); err != nil {
		return fmt.Errorf("撤销 DNS 接管失败: %w", err)
	}
	return nil
}

// Result 是 AfterKernelStart 的结果。
type Result struct {
	// Mode 是对齐时用的 dns.mode(读不到时为空)。
	Mode string
	// Changed 为真表示这次真的改了系统(接管或撤销了)。
	Changed bool
}

// AfterKernelStart 在内核(重新)起来、确认在运行之后调用:按 dns.mode 对齐。
// 出错时系统保持(或已自动恢复到)接管之前的状态,错误文字可以直接给用户看。
func (c *Coordinator) AfterKernelStart(ctx context.Context) (Result, error) {
	mode, err := c.Mode(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("读不到 DNS 模式,没有改 DNS: %w", err)
	}
	res := Result{Mode: mode}
	deadline := time.Now().Add(c.wait())
	for {
		changed, err := c.Taker.Sync(ctx, mode)
		if err == nil {
			res.Changed = changed
			return res, nil
		}
		// 只有「内核的 DNS 入站还没应答」值得等:别的失败(不支持、写文件失败、探测失败已回滚……)重试也没用
		if !errors.Is(err, system.ErrKernelDNSDown) || time.Now().After(deadline) {
			return res, err
		}
		t := time.NewTimer(c.poll())
		select {
		case <-ctx.Done():
			t.Stop()
			return res, ctx.Err()
		case <-t.C:
		}
	}
}

// ModeFromProfile 把「读完整档案」(*profile.Service.Get)变成 Coordinator.Mode:取档案里的 dns.mode。
// 档案读不到、或里面没有 dns.mode 都返回 error(档案默认值保证正常情况下一定有;没有就是出了问题,不确定就不动)。
func ModeFromProfile(get func(ctx context.Context) (map[string]any, error)) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		p, err := get(ctx)
		if err != nil {
			return "", err
		}
		d, _ := p["dns"].(map[string]any)
		m, _ := d["mode"].(string)
		if m == "" {
			return "", errors.New("档案里没有 dns.mode")
		}
		return m, nil
	}
}

// Recover 给进程启动时用:清掉上次异常退出遗留的接管。失败只记日志,不挡启动。
func (c *Coordinator) Recover(ctx context.Context) {
	if err := c.Taker.Recover(ctx); err != nil {
		c.log().Warn("启动时清理 DNS 接管遗留失败", "error", err)
	}
}

// Note 把一次对齐的结果写成给用户看的一句话(附在部署 / 服务操作的结果后面)。
// 成功且没改系统、或模式是 off / hijack 且本来就没接管:返回空串,不打扰。
func Note(res Result, err error) string {
	switch {
	case err != nil:
		return "注意:DNS 接管没有成功,DNS 保持原样,局域网设备的查询暂时不会经过内核(" + err.Error() + ")"
	case res.Changed && res.Mode == "dnsmasq":
		return "dnsmasq 已改为把查询转发给内核"
	case res.Changed:
		return "已撤销 dnsmasq 对内核的转发"
	}
	return ""
}
