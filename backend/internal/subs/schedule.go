package subs

import (
	"context"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

// 订阅定时更新:按每份订阅的 autoUpdate 计划(前端 OpenboxSubscriptionAutoUpdate)定时重新拉取并替换节点。
// 自己的设计,依据前端该类型的注释(「每隔 days 天、hour 点重新拉一次」;mode 为 hours 时「每隔 N 小时」)和
// 订阅卡片对计划的显示,没有参照上游。
//
// 保守做法(变更安全):只更新订阅里的节点,**不自动部署、不重启内核**——定时重启代理会让正在用的连接断掉,
// 路由器是用户的网关,拿不准就不动。节点变了以后,服务状态的 pendingRestart(原因 nodes)会提示「配置已改、内核还没用上」,
// 由用户决定什么时候重启。

// Refresher 是调度器要用的订阅服务(*Service 满足),测试里换假的。
type Refresher interface {
	List(ctx context.Context) ([]Record, error)
	Refresh(ctx context.Context, id string, rename map[string]any) (*SaveResult, error)
}

// 调度器的默认参数。
const (
	DefaultTick       = time.Minute
	DefaultStartDelay = 90 * time.Second // 刚开机时网络可能还没好,先等一会儿再看
	DefaultTimeout    = 2 * time.Minute  // 一份订阅拉取 + 解析的总时限
)

// 拉取失败后的重试间隔(第几次失败用第几项,最后一项之后一直用最后一项)。
var retryDelays = []time.Duration{10 * time.Minute, 30 * time.Minute, time.Hour}

// NextDue 算这份订阅下一次该更新的时刻。没开定时、停用、没有可重新拉取的来源(只有粘贴内容的节点模式)时 ok 为假。
// 计划两种:mode=hours 是「上次更新后每隔 hours 小时」;否则是「上次更新那天起每隔 days 天的 hour 点」(按 loc 的本地时间)。
// 从没更新过(UpdatedAt 为 0)算马上到期。
func NextDue(rec Record, loc *time.Location) (time.Time, bool) {
	if !rec.Enabled || rec.AutoUpdate == nil {
		return time.Time{}, false
	}
	if on, _ := rec.AutoUpdate["enabled"].(bool); !on {
		return time.Time{}, false
	}
	if len(rec.source().urls) == 0 {
		return time.Time{}, false
	}
	if rec.UpdatedAt <= 0 {
		return time.Time{}, true
	}
	last := time.UnixMilli(rec.UpdatedAt)
	if mode, _ := rec.AutoUpdate["mode"].(string); mode == "hours" {
		h, ok := intField(rec.AutoUpdate, "hours")
		if !ok || h < 1 {
			return time.Time{}, false
		}
		return last.Add(time.Duration(h) * time.Hour), true
	}
	days, ok1 := intField(rec.AutoUpdate, "days")
	hour, ok2 := intField(rec.AutoUpdate, "hour")
	if !ok1 || !ok2 || days < 1 || hour < 0 || hour > 23 {
		return time.Time{}, false
	}
	if loc == nil {
		loc = time.Local
	}
	l := last.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day()+days, hour, 0, 0, 0, loc), true
}

func intField(m map[string]any, k string) (int, bool) {
	switch v := m[k].(type) {
	case float64:
		return int(v), v == float64(int(v))
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

type retryState struct {
	fails int
	next  time.Time
}

// Scheduler 定时更新订阅。零值不可用,用 NewScheduler。
type Scheduler struct {
	Svc        Refresher
	Now        func() time.Time
	Loc        *time.Location // 计划里「几点」按哪个时区,空 = time.Local
	Tick       time.Duration
	StartDelay time.Duration
	Timeout    time.Duration

	mu    sync.Mutex
	retry map[string]retryState // 只在内存里:重启后重新开始计
}

// NewScheduler 用默认参数创建调度器。
func NewScheduler(svc Refresher) *Scheduler {
	return &Scheduler{Svc: svc, Now: time.Now, Tick: DefaultTick, StartDelay: DefaultStartDelay, Timeout: DefaultTimeout, retry: map[string]retryState{}}
}

var urlRe = regexp.MustCompile(`https?://[^\s"'<>]+`)

// redact 去掉错误信息里的地址(订阅地址里常带 token,不进日志)。
func redact(s string) string { return urlRe.ReplaceAllString(s, "<url>") }

// RunOnce 更新此刻所有到期的订阅,一份接一份(同一时间只拉一份),返回尝试了几份。
// 失败的按 retryDelays 退避,不会每个轮询都重拉;成功后清掉失败记录。ctx 结束就停。
func (s *Scheduler) RunOnce(ctx context.Context) int {
	recs, err := s.Svc.List(ctx)
	if err != nil {
		slog.Warn("订阅定时更新:读订阅列表失败", "error", redact(err.Error()))
		return 0
	}
	now := s.Now()
	s.mu.Lock()
	if s.retry == nil {
		s.retry = map[string]retryState{}
	}
	live := map[string]bool{}
	for _, r := range recs {
		live[r.ID] = true
	}
	for id := range s.retry { // 删掉的订阅不再记失败
		if !live[id] {
			delete(s.retry, id)
		}
	}
	s.mu.Unlock()

	n := 0
	for _, rec := range recs {
		if ctx.Err() != nil {
			return n
		}
		due, ok := NextDue(rec, s.Loc)
		if !ok || now.Before(due) {
			continue
		}
		s.mu.Lock()
		rs := s.retry[rec.ID]
		s.mu.Unlock()
		if now.Before(rs.next) {
			continue
		}
		n++
		tctx, cancel := context.WithTimeout(ctx, s.Timeout)
		res, err := s.Svc.Refresh(tctx, rec.ID, nil)
		cancel()
		s.mu.Lock()
		if err != nil {
			rs.fails++
			d := retryDelays[min(rs.fails, len(retryDelays))-1]
			rs.next = now.Add(d)
			s.retry[rec.ID] = rs
			s.mu.Unlock()
			slog.Warn("订阅定时更新失败", "id", rec.ID, "name", rec.Name, "retryIn", d.String(), "error", redact(err.Error()))
			continue
		}
		delete(s.retry, rec.ID)
		s.mu.Unlock()
		slog.Info("订阅已按计划更新", "id", rec.ID, "name", rec.Name, "nodes", res.NodeCount, "changed", res.Changed)
	}
	return n
}

// Run 先等 StartDelay,然后每个 Tick 看一次,直到 ctx 结束。
func (s *Scheduler) Run(ctx context.Context) {
	tick, delay := s.Tick, s.StartDelay
	if tick <= 0 {
		tick = DefaultTick
	}
	if !wait(ctx, delay) {
		return
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		s.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
