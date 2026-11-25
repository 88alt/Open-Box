package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSchedule(t *testing.T) {
	// 档案里没有 updates 时:不自动更新、4 点、每天、auto 通道
	s := ParseSchedule(map[string]any{})
	if s.Auto || s.Hour != 4 || s.Days != 1 || s.Channel != "auto" {
		t.Errorf("默认值: %+v", s)
	}
	// JSON 解出来的数字是 float64
	s = ParseSchedule(map[string]any{"updates": map[string]any{"openbox": map[string]any{
		"auto": true, "hour": float64(3), "days": float64(7), "channel": "mirror",
	}}})
	if !s.Auto || s.Hour != 3 || s.Days != 7 || s.Channel != "mirror" {
		t.Errorf("%+v", s)
	}
	// 越界的值不用(档案已经校验过,这里只兜底)
	s = ParseSchedule(map[string]any{"updates": map[string]any{"openbox": map[string]any{
		"auto": true, "hour": float64(99), "days": float64(0),
	}}})
	if s.Hour != 4 || s.Days != 1 {
		t.Errorf("越界值该退回默认: %+v", s)
	}
}

func TestScheduleDue(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	at := func(day, hour int) time.Time { return time.Date(2026, 11, day, hour, 30, 0, 0, loc) }
	base := Schedule{Auto: true, Hour: 4, Days: 1, Channel: "auto"}

	cases := []struct {
		name string
		s    Schedule
		now  time.Time
		last LastRun
		want bool
	}{
		{"关掉就不跑", Schedule{Auto: false, Hour: 4, Days: 1}, at(25, 4), LastRun{}, false},
		{"不到钟点", base, at(25, 3), LastRun{}, false},
		{"到钟点、没跑过", base, at(25, 4), LastRun{}, true},
		{"今天已经跑过", base, at(25, 4), LastRun{Date: "2026-11-25"}, false},
		{"昨天跑过、今天到点", base, at(25, 4), LastRun{Date: "2026-11-24"}, true},
		{"每隔三天:才过一天", Schedule{Auto: true, Hour: 4, Days: 3}, at(25, 4), LastRun{Date: "2026-11-24"}, false},
		{"每隔三天:过满三天", Schedule{Auto: true, Hour: 4, Days: 3}, at(27, 4), LastRun{Date: "2026-11-24"}, true},
		{"上次日期是坏的:当作没跑过", base, at(25, 4), LastRun{Date: "不是日期"}, true},
	}
	for _, c := range cases {
		if got := c.s.Due(c.now, c.last); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestLastRunRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "auto.json")
	if l := ReadLastRun(p); l != (LastRun{}) {
		t.Errorf("没有文件时是零值: %+v", l)
	}
	want := LastRun{Date: "2026-11-25", Version: "v0.1.484-main.2", OK: false, Reason: "下载失败"}
	if err := WriteLastRun(p, want); err != nil {
		t.Fatal(err)
	}
	if got := ReadLastRun(p); got != want {
		t.Errorf("%+v", got)
	}
	if err := os.WriteFile(p, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := ReadLastRun(p); l != (LastRun{}) {
		t.Errorf("坏文件当作没跑过: %+v", l)
	}
}

// newScheduler 用一个假发布服务器搭好调度器。
func newScheduler(t *testing.T, now time.Time, files map[string]string) (*Scheduler, *Service, *fakeUpgrade, string) {
	t.Helper()
	svc, f := newUpgradeService(t, files)
	state := filepath.Join(t.TempDir(), "auto.json")
	s := &Scheduler{
		Service:   svc,
		Schedule:  func(context.Context) Schedule { return Schedule{Auto: true, Hour: 4, Days: 1, Channel: "direct"} },
		StatePath: state,
		Now:       func() time.Time { return now },
	}
	return s, svc, f, state
}

func TestTickStartsUpgradeAndRecords(t *testing.T) {
	now := time.Date(2026, 11, 25, 4, 0, 0, 0, time.UTC)
	s, svc, f, state := newScheduler(t, now, files())
	s.Tick(context.Background())
	// 跑完(流水线停在提交之前),记录要写下来:同一天不会再来一次
	waitJob(t, svc)
	rec := ReadLastRun(state)
	if rec.Date != "2026-11-25" || rec.Version != f.tag || rec.OK {
		t.Errorf("%+v", rec)
	}
	if rec.Reason == "" {
		t.Error("失败要有原因")
	}
	// 同一天再 Tick 一次:不再开
	s.Tick(context.Background())
	if st := svc.Status(context.Background()); st.Status.Running {
		t.Error("一天最多一次")
	}
}

func TestTickSkipsSameVersionAfterFailure(t *testing.T) {
	now := time.Date(2026, 11, 26, 4, 0, 0, 0, time.UTC)
	s, svc, f, state := newScheduler(t, now, files())
	// 昨天这个版本失败过
	if err := WriteLastRun(state, LastRun{Date: "2026-11-25", Version: f.tag, OK: false, Reason: "下载失败"}); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if st := svc.Status(context.Background()); st.Status.Running {
		t.Fatal("同一个版本失败过就不该自动重试")
	}
	if rec := ReadLastRun(state); rec.Date != "2026-11-25" {
		t.Errorf("跳过时不该改记录: %+v", rec)
	}
}

func TestTickSkipsWhenAlreadyLatest(t *testing.T) {
	now := time.Date(2026, 11, 25, 4, 0, 0, 0, time.UTC)
	s, svc, f, state := newScheduler(t, now, files())
	svc.Version = f.tag
	s.Tick(context.Background())
	if st := svc.Status(context.Background()); st.Status.Running {
		t.Error("已是最新就不该升级")
	}
	if rec := ReadLastRun(state); rec != (LastRun{}) {
		t.Errorf("没升级就不该占掉今天这次机会: %+v", rec)
	}
}

func TestTickSkipsWhenNotDue(t *testing.T) {
	now := time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC) // 不是 4 点
	s, svc, _, _ := newScheduler(t, now, files())
	s.Tick(context.Background())
	if st := svc.Status(context.Background()); st.Status.Running {
		t.Error("不到钟点不该升级")
	}
	// 没接线时什么都不做
	(&Scheduler{}).Tick(context.Background())
}
