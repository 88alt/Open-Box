package traffic

import (
	"context"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

const (
	// DefaultInterval 是采样间隔。
	DefaultInterval = 10 * time.Second
	flushEvery      = 60 * time.Second
	unknown         = "未知"
)

// Collector 定时读内核的连接快照,算出增量记进 Store。
//
// 规则:
//   - 第一次读到(或内核断开、再连上之后的第一次)只当基线,不记数:这之前的累计量不是这段时间发生的;
//   - 总量取累计总量的增量(比上次小说明内核重启过,按从 0 开始算);
//   - 连接按 id 记增量,第一次见到的连接记全部已传量并算一条连接,消失的连接丢掉;
//   - 在两次采样之间开了又关的短连接,进不了各节点的数,只在总量里,就是前端说的「没采样到的短连接」。
type Collector struct {
	Source func(ctx context.Context) (singbox.KernelSnapshot, error)
	Store  *Store
	Now    func() time.Time
	// KeepMonths 返回档案里的保留月数(取不到返回 0,按 12 个月)。
	KeepMonths func() int
	// OnError 收到采样 / 写盘的错误(内核没在跑是常态,调用方自己决定怎么记)。
	OnError func(error)

	have             bool
	lastUp, lastDown int64
	seen             map[string][2]int64
	maintainedDay    string
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func leaf(chains []string) string {
	if len(chains) == 0 || chains[0] == "" {
		return unknown
	}
	return chains[0]
}

// Tick 采样一次。
func (c *Collector) Tick(ctx context.Context) error {
	snap, err := c.Source(ctx)
	if err != nil {
		c.have = false // 断开期间的变化不可信,重新连上后重新建基线
		return err
	}
	seen := make(map[string][2]int64, len(snap.Conns))
	if !c.have {
		for _, k := range snap.Conns {
			seen[k.ID] = [2]int64{k.Upload, k.Download}
		}
		c.have, c.lastUp, c.lastDown, c.seen = true, snap.UploadTotal, snap.DownloadTotal, seen
		return nil
	}
	d := Delta{Up: snap.UploadTotal - c.lastUp, Down: snap.DownloadTotal - c.lastDown, DirectTags: snap.DirectTags}
	if d.Up < 0 {
		d.Up = snap.UploadTotal
	}
	if d.Down < 0 {
		d.Down = snap.DownloadTotal
	}
	direct := map[string]bool{}
	for _, t := range snap.DirectTags {
		direct[t] = true
	}
	for _, k := range snap.Conns {
		seen[k.ID] = [2]int64{k.Upload, k.Download}
		prev, old := c.seen[k.ID]
		it := Item{Client: k.Source, Node: leaf(k.Chains), Host: k.Host, Up: k.Upload - prev[0], Down: k.Download - prev[1]}
		if !old {
			it.Up, it.Down, it.Conns = k.Upload, k.Download, 1
		} else if it.Up < 0 || it.Down < 0 {
			it.Up, it.Down = k.Upload, k.Download
		}
		if it.Up == 0 && it.Down == 0 && it.Conns == 0 {
			continue
		}
		if it.Client == "" {
			it.Client = unknown
		}
		if it.Host == "" {
			it.Host = k.DestIP
		}
		if it.Host == "" {
			it.Host = unknown
		}
		it.Direct = direct[it.Node]
		d.Conns += it.Conns
		d.Items = append(d.Items, it)
	}
	c.lastUp, c.lastDown, c.seen = snap.UploadTotal, snap.DownloadTotal, seen
	if d.Up == 0 && d.Down == 0 && d.Conns == 0 && len(d.Items) == 0 {
		return nil
	}
	now := c.now()
	if err := c.Store.Record(now, d); err != nil {
		return err
	}
	c.maintainOnce(now)
	return nil
}

// maintainOnce 每天(含启动后第一次记数时)做一次清理。
func (c *Collector) maintainOnce(now time.Time) {
	day := now.Format(dayLayout)
	if c.maintainedDay == day {
		return
	}
	c.maintainedDay = day
	months := 0
	if c.KeepMonths != nil {
		months = c.KeepMonths()
	}
	if err := c.Store.Maintain(now, months); err != nil && c.OnError != nil {
		c.OnError(err)
	}
}

// Run 按 interval 采样,直到 ctx 结束;每 flushEvery 写一次盘,退出前再写一次。
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	lastFlush := time.Now()
	report := func(err error) {
		if err != nil && c.OnError != nil {
			c.OnError(err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			report(c.Store.Flush())
			return
		case <-tick.C:
			report(c.Tick(ctx))
			if time.Since(lastFlush) >= flushEvery {
				report(c.Store.Flush())
				lastFlush = time.Now()
			}
		}
	}
}
