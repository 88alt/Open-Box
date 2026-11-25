package update

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// 自动升级调度(M6 第七节末段 / 第十二节第 7 项)。
//
// 计划在档案的 profile.updates.openbox 里:auto(开关)、hour(0~23,按路由器本地时区)、
// days(每隔几天,1 = 每天)、channel(auto / direct / mirror)。
// 两条硬规矩:
//   - 一天最多一次;
//   - 同一个版本失败过一次就不再自动重试(用户手动点了才试),避免来回刷。
//
// 记录放 <data-dir>/update/auto.json,和升级日志同一个目录。

// Schedule 是档案里的自更新计划。
type Schedule struct {
	Auto    bool
	Hour    int
	Days    int
	Channel string
}

// ParseSchedule 从档案(map)里取自更新计划。取不到的字段用默认值:不自动更新、4 点、每天、auto 通道。
// 档案本身已经过校验(internal/profile),这里只做取值,不重复报错。
func ParseSchedule(profile map[string]any) Schedule {
	s := Schedule{Hour: 4, Days: 1, Channel: "auto"}
	up, ok := profile["updates"].(map[string]any)
	if !ok {
		return s
	}
	ob, ok := up["openbox"].(map[string]any)
	if !ok {
		return s
	}
	if v, ok := ob["auto"].(bool); ok {
		s.Auto = v
	}
	if v, ok := num(ob["hour"]); ok && v >= 0 && v <= 23 {
		s.Hour = v
	}
	if v, ok := num(ob["days"]); ok && v >= 1 {
		s.Days = v
	}
	if v, ok := ob["channel"].(string); ok && v != "" {
		s.Channel = v
	}
	return s
}

func num(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

// LastRun 是上一次自动升级的记录。
type LastRun struct {
	// Date 是本地日期 YYYY-MM-DD,用来算「一天最多一次」与「每隔几天」。
	Date string `json:"date"`
	// Version 是那次要装的版本;失败过就不再自动重试同一个版本。
	Version string `json:"version"`
	OK      bool   `json:"ok"`
	Reason  string `json:"reason,omitempty"`
}

// ReadLastRun 读上一次的记录。文件不在或读不动时返回零值(当作从没跑过)。
func ReadLastRun(path string) LastRun {
	var l LastRun
	b, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	_ = json.Unmarshal(b, &l)
	return l
}

// WriteLastRun 写记录。
func WriteLastRun(path string, l LastRun) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Due 判断现在该不该自动升级。now 用**本地时区**(路由器时区),钟点按它算。
func (s Schedule) Due(now time.Time, last LastRun) bool {
	if !s.Auto {
		return false
	}
	if now.Hour() != s.Hour {
		return false
	}
	today := now.Format("2006-01-02")
	if last.Date == today {
		return false // 一天最多一次
	}
	days := s.Days
	if days <= 0 {
		days = 1
	}
	if last.Date != "" {
		if d, err := time.ParseInLocation("2006-01-02", last.Date, now.Location()); err == nil {
			if now.Sub(d) < time.Duration(days)*24*time.Hour {
				return false // 还没到「每隔几天」
			}
		}
	}
	return true
}

// Scheduler 按计划自动升级。
type Scheduler struct {
	Service *Service
	// Schedule 读当前计划;为空 = 不自动升级。
	Schedule func(ctx context.Context) Schedule
	// StatePath 是 <data-dir>/update/auto.json。
	StatePath string
	// Now 取当前时间,为空用 time.Now(测试里固定)。
	Now func() time.Time
	// Interval 是多久看一眼,默认 1 分钟。
	Interval time.Duration
	Log      *slog.Logger
}

func (s *Scheduler) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run 起循环,随 ctx 结束。到点才真的做事,平时只是每分钟看一眼。
func (s *Scheduler) Run(ctx context.Context) {
	if s.Service == nil || s.Schedule == nil || s.StatePath == "" {
		return
	}
	interval := s.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// Tick 看一眼该不该升级,该就开一次。导出是为了测试能直接调,不用等 ticker。
func (s *Scheduler) Tick(ctx context.Context) {
	if s.Service == nil || s.Schedule == nil || s.StatePath == "" {
		return
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	sch := s.Schedule(ctx)
	last := ReadLastRun(s.StatePath)
	if !sch.Due(now, last) {
		return
	}
	if st := s.Service.Status(ctx); st.Status.Running {
		return // 已经在升级(可能是用户手动点的)
	}
	// 先探一版:已是最新就不占用今天这一次机会
	plan, err := s.Service.Checker.Plan(ctx, sch.Channel, "")
	if err != nil {
		s.log().Warn("自动升级:探测最新版失败,这次先跳过", "err", err)
		return
	}
	if cur, ok := ParseVersion(s.Service.Version); ok {
		if lv, ok := ParseVersion(plan.Tag); ok && lv.Compare(cur) <= 0 {
			return
		}
	}
	if last.Version == plan.Tag && !last.OK {
		// 同一个版本失败过:不再自动重试,等用户手动点
		s.log().Info("自动升级:这个版本上次失败了,跳过", "version", plan.Tag, "reason", last.Reason)
		return
	}

	// 先记「已开始」:进程中途被重启也不会在同一天再来一次
	rec := LastRun{Date: now.Format("2006-01-02"), Version: plan.Tag, OK: false, Reason: "已开始"}
	if err := WriteLastRun(s.StatePath, rec); err != nil {
		s.log().Warn("自动升级:写记录失败", "err", err)
	}
	if err := s.Service.Run(sch.Channel); err != nil {
		rec.Reason = "起不来:" + err.Error()
		_ = WriteLastRun(s.StatePath, rec)
		s.log().Warn("自动升级:开不了", "err", err)
		return
	}
	s.log().Info("自动升级:已开始", "version", plan.Tag, "channel", sch.Channel)

	// 等它跑完再记结果。提交那一步会让本进程重启,那就等不到了——
	// 记录停在「已开始」,下一天再看,不会同一天反复试。
	deadline := now.Add(30 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		st := s.Service.Status(ctx)
		if st.Status.Running {
			if time.Now().After(deadline) {
				return
			}
			continue
		}
		rec.OK = st.Status.Stage == StageDone
		rec.Reason = st.Status.Message
		if err := WriteLastRun(s.StatePath, rec); err != nil {
			s.log().Warn("自动升级:写记录失败", "err", err)
		}
		return
	}
}
