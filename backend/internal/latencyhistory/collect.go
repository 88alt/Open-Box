package latencyhistory

import (
	"context"
	"log/slog"
	"time"
)

// 定时采集:自动择优(urltest)组由内核自己按 interval 定时测速,内核只留每个节点最近一次的结果;
// 后端每隔一小段时间读一次内核把新结果合进历史,浏览器没开着也不丢样本(前端「手动测完立刻 sync」只是不想等下一轮)。
// 自己的设计,依据前端 api/openbox.ts 里 syncLatencyHistory 的注释(「让服务端立刻读一次内核把新结果记下来,不用等它下一个 tick」)
// 和 OpenboxLatencyHistory 的说明,没有参照上游。
//
// 只读内核、只写历史文件:内核没起来(读不到控制接口)时什么都不记、不报错;没有新结果时不写盘(路由器闪存)。

// DefaultCollectInterval 是读内核的间隔。比内核 urltest 的最小间隔短得多,不会漏掉一轮的结果;读本机控制接口很便宜。
const DefaultCollectInterval = 30 * time.Second

// collectTimeout 是一次读内核加合并的时限。
const collectTimeout = 15 * time.Second

// Collect 每隔 interval(<=0 用默认值)调一次 Sync,直到 ctx 结束。Kernel 为空时立刻返回(没有来源可读)。
func (s *Service) Collect(ctx context.Context, interval time.Duration) {
	if s.Kernel == nil {
		return
	}
	if interval <= 0 {
		interval = DefaultCollectInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cctx, cancel := context.WithTimeout(ctx, collectTimeout)
		if _, err := s.Sync(cctx); err != nil {
			slog.Warn("延迟历史定时采集:写入失败", "error", err)
		}
		cancel()
	}
}
