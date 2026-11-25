package update

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecoverDeps 是启动时恢复 / 自检要的东西。除了 Journal / Root,别的都可以为空(为空就跳过那一步)。
type RecoverDeps struct {
	Journal   string
	Root      string
	StateDir  string
	Ops       Ops
	Health    func(ctx context.Context) error
	CoreHello func(ctx context.Context) error
	// KernelRestart 在升级前内核在跑时把它拉起来(沿用现有的安全部署路径)。
	KernelRestart func(ctx context.Context) error
	// Exit 自检失败时用,默认 os.Exit。
	Exit func(code int)
	Log  *slog.Logger
	// HealthTimeout 是等 HTTP 健康接口的时间,默认 30 秒。
	HealthTimeout time.Duration
}

func (d RecoverDeps) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// Recover 在进程启动后调用一次(M6 第七节第 2、5 条):
//
//	① 看门狗回滚过(有 rollback.flag)→ 把日志改成 rolled-back,清掉标记;
//	② 日志停在 committing(提交中崩溃)→ 先按日志回滚再正常启动;
//	③ 日志停在 restarting(刚被重启)→ 自检:通过写 healthy 并清掉 .old;
//	   不通过写 failed 并退出,让看门狗尽早回滚,不用等满 90 秒。
//
// 返回的错误只用于打日志:恢复失败不该拦住进程启动(拦住等于把设备锁死)。
func Recover(ctx context.Context, d RecoverDeps) error {
	if d.Journal == "" || d.Root == "" {
		return nil
	}
	d.Ops = d.Ops.withDefaults()
	j, err := ReadJournal(d.Journal)
	if err != nil {
		return err
	}
	if j == nil {
		return nil
	}

	// ① 看门狗已经回滚过了
	flag := filepath.Join(filepath.Dir(d.Journal), RollbackFlag)
	if b, err := os.ReadFile(flag); err == nil {
		j.Status = JournalRolledBack
		j.Message = strings.TrimSpace(string(b))
		_ = d.Ops.Write(d.Journal, j)
		_ = os.Remove(flag)
		d.log().Warn("上次升级被看门狗回滚", "from", j.From, "to", j.To, "reason", j.Message)
		cleanupBackups(d.Root, j.Entries, d.Ops)
		return nil
	}

	switch j.Status {
	case JournalCommitting:
		// ② 提交中崩溃:文件可能换了一半
		d.log().Warn("上次升级在提交中中断,正在回滚", "from", j.From, "to", j.To)
		if err := Rollback(ctx, d.Journal, j, d.Ops); err != nil {
			return fmt.Errorf("回滚失败:%w", err)
		}
		cleanupBackups(d.Root, j.Entries, d.Ops)
		return nil

	case JournalRestarting:
		// ③ 新版本刚被拉起来:自检
		if err := d.selfCheck(ctx); err != nil {
			j.Status = JournalFailed
			j.Message = "新版本自检没过:" + err.Error()
			_ = d.Ops.Write(d.Journal, j)
			d.log().Error("新版本自检没过,交给看门狗回滚", "err", err)
			if d.Exit != nil {
				d.Exit(1)
			}
			return err
		}
		j.Status = JournalHealthy
		j.Message = "已升级到 " + j.To
		if err := d.Ops.Write(d.Journal, j); err != nil {
			return err
		}
		cleanupBackups(d.Root, j.Entries, d.Ops)
		d.log().Info("升级自检通过", "from", j.From, "to", j.To)
		return nil
	}
	return nil
}

// selfCheck 是新进程起来后的自检:HTTP 健康接口通、core 握手通,
// 升级前内核在跑的话再把它拉起来。
func (d RecoverDeps) selfCheck(ctx context.Context) error {
	if d.Health != nil {
		timeout := d.HealthTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		deadline := time.Now().Add(timeout)
		for {
			err := d.Health(ctx)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("健康接口 %s 内没通:%w", timeout, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	if d.CoreHello != nil {
		if err := d.CoreHello(ctx); err != nil {
			return fmt.Errorf("core 握手失败:%w", err)
		}
	}
	if d.KernelRestart != nil {
		if err := d.KernelRestart(ctx); err != nil {
			return fmt.Errorf("拉起内核失败:%w", err)
		}
	}
	return nil
}

// cleanupBackups 自检通过后清掉 .old / .new 与暂存目录,省空间。
func cleanupBackups(root string, entries []JournalEntry, ops Ops) {
	for _, e := range entries {
		target := filepath.Join(root, filepath.FromSlash(e.Path))
		_ = ops.RemoveAll(target + ".old")
		_ = ops.RemoveAll(target + ".new")
	}
}
