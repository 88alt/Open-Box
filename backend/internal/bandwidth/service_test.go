package bandwidth

import (
	"context"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func TestNormalizeSettings(t *testing.T) {
	s, err := NormalizeSettings(map[string]any{"intervalHours": float64(12), "maxDownloadMB": float64(100)})
	if err != nil || s.IntervalHours != 12 || s.MaxDownloadMB != 100 {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := NormalizeSettings(map[string]any{"intervalHours": float64(0)}); err == nil {
		t.Fatal("expected error")
	}
	if s.IntervalUnit != UnitHours {
		t.Fatalf("单位默认是小时: %+v", s)
	}
	// 周期可以超过一天(面板能按「天」输入),上限 30 天
	if s, err := NormalizeSettings(map[string]any{"intervalHours": float64(72), "intervalUnit": "days"}); err != nil || s.IntervalHours != 72 || s.IntervalUnit != UnitDays {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := NormalizeSettings(map[string]any{"intervalHours": float64(721)}); err == nil {
		t.Fatal("超过 720 小时应拒绝")
	}
	for _, bad := range []any{"weeks", 3.0, nil} {
		if _, err := NormalizeSettings(map[string]any{"intervalUnit": bad}); err == nil {
			t.Fatalf("intervalUnit=%v 应拒绝", bad)
		}
	}
}

func TestQueue(t *testing.T) {
	svc := &Service{
		Nodes: func(ctx context.Context) ([]NodeRef, error) {
			return []NodeRef{{Name: "a", SubscriptionID: "s1", Enabled: true}}, nil
		},
	}
	st, err := svc.Status(context.Background())
	if err != nil || st.Nodes != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	q, err := svc.QueueSubscription(context.Background(), "s1")
	if err != nil || q.Queued != 1 {
		t.Fatalf("%+v %v", q, err)
	}
	st, _ = svc.Status(context.Background())
	if st.PendingBySubscription["s1"] != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestRunOnceWithMeasurer(t *testing.T) {
	svc := &Service{
		Nodes: func(ctx context.Context) ([]NodeRef, error) {
			return []NodeRef{{Name: "n1", SubscriptionID: "s1"}}, nil
		},
		Measure: func(ctx context.Context, node string, maxBytes int64) MeasureResult {
			return MeasureResult{Mbps: 12.5}
		},
	}
	_, _ = svc.QueueNode(context.Background(), "n1")
	svc.RunOnce(context.Background())
	st, _ := svc.Status(context.Background())
	m := st.Measurements["n1"]
	if m.Pending || m.Mbps != 12.5 || m.CompletedAt == 0 {
		t.Fatalf("%+v", m)
	}
}

func TestEnqueueDueAndPersist(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Store: st,
		Profile: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{
				"bandwidth": map[string]any{
					"intervalHours": float64(1),
					"maxDownloadMB": float64(10),
				},
			}, nil
		},
		Nodes: func(ctx context.Context) ([]NodeRef, error) {
			return []NodeRef{{Name: "n1", SubscriptionID: "s1", Enabled: true}}, nil
		},
		Measure: func(ctx context.Context, node string, maxBytes int64) MeasureResult {
			return MeasureResult{Mbps: 1}
		},
	}
	n, err := svc.EnqueueDue(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	svc.RunOnce(context.Background())
	svc2 := &Service{Store: st}
	svc2.LoadHistory()
	svc2.mu.Lock()
	m := svc2.measures["n1"]
	svc2.mu.Unlock()
	if m.Mbps != 1 {
		t.Fatalf("%+v", m)
	}
}

func TestProbeMeasurerNotFound(t *testing.T) {
	m := NewProbeMeasurer(
		MaterialFunc(func(ctx context.Context, tags []string) (*probe.Material, error) {
			return probe.Subset(map[string]any{"outbounds": []any{}}, tags), nil
		}),
		RunnerFunc(func(ctx context.Context, m *probe.Material, jobs []probe.Job, opt probe.Options) ([]probe.Result, error) {
			return []probe.Result{{Error: "找不到这个节点", Reason: "not-found"}}, nil
		}),
		nil,
	)
	r := m(context.Background(), "missing", 1024)
	if !r.Failed || r.Reason != "not-found" {
		t.Fatalf("%+v", r)
	}
}

func TestNormalizeSettingsStartHour(t *testing.T) {
	s, err := NormalizeSettings(nil)
	if err != nil || s.StartHour != DefaultStartHour || DefaultStartHour != 4 {
		t.Fatalf("默认开始时刻应是 4 点: %+v %v", s, err)
	}
	s, err = NormalizeSettings(map[string]any{"intervalHours": float64(48), "intervalUnit": "days", "startHour": float64(23)})
	if err != nil || s.StartHour != 23 {
		t.Fatalf("%+v %v", s, err)
	}
	if s, err = NormalizeSettings(map[string]any{"startHour": float64(0)}); err != nil || s.StartHour != 0 {
		t.Fatalf("0 点是合法值,不能被当成没设: %+v %v", s, err)
	}
	for _, bad := range []any{float64(-1), float64(24), 3.5, "4", nil, true} {
		if _, err := NormalizeSettings(map[string]any{"startHour": bad}); err == nil {
			t.Fatalf("startHour=%v 应拒绝", bad)
		}
	}
}

func TestSettingsDays(t *testing.T) {
	for _, c := range []struct{ hours, want int }{{1, 1}, {12, 1}, {24, 1}, {35, 1}, {36, 2}, {48, 2}, {72, 3}, {720, 30}} {
		if got := (Settings{IntervalHours: c.hours}).Days(); got != c.want {
			t.Fatalf("%d 小时应折成 %d 天,得到 %d", c.hours, c.want, got)
		}
	}
}

func TestSettingsNextDue(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	at := func(m, d, h, mi int) time.Time { return time.Date(2026, time.Month(m), d, h, mi, 0, 0, loc) }
	hourly := Settings{IntervalHours: 6, IntervalUnit: UnitHours, StartHour: 9}
	if got := hourly.NextDue(at(11, 1, 10, 30)); !got.Equal(at(11, 1, 16, 30)) {
		t.Fatalf("按小时不看开始时刻: %v", got)
	}
	daily := Settings{IntervalHours: 24, IntervalUnit: UnitDays, StartHour: 4}
	for _, c := range []struct{ done, want time.Time }{
		{at(11, 1, 5, 0), at(11, 2, 4, 0)},    // 4 点之后测完:第二天 4 点
		{at(11, 1, 1, 0), at(11, 2, 4, 0)},    // 4 点之前测完:也是第二天 4 点,不是当天
		{at(11, 30, 23, 59), at(12, 1, 4, 0)}, // 跨月
	} {
		if got := daily.NextDue(c.done); !got.Equal(c.want) {
			t.Fatalf("%v 之后: 想要 %v,得到 %v", c.done, c.want, got)
		}
	}
	three := Settings{IntervalHours: 72, IntervalUnit: UnitDays, StartHour: 0}
	if got := three.NextDue(at(12, 30, 13, 0)); !got.Equal(time.Date(2027, 1, 2, 0, 0, 0, 0, loc)) {
		t.Fatalf("三天一次、0 点: %v", got)
	}
	// 夏令时:按日历日期推算,切换那天的开始时刻仍是当地 4 点整
	ny, err := time.LoadLocation("America/New_York")
	if err == nil {
		d := time.Date(2026, 3, 7, 12, 0, 0, 0, ny)
		got := daily.NextDue(d)
		if got.Day() != 8 || got.Hour() != 4 || got.Minute() != 0 {
			t.Fatalf("夏令时前一天: %v", got)
		}
		got = daily.NextDue(time.Date(2026, 3, 8, 12, 0, 0, 0, ny))
		if got.Day() != 9 || got.Hour() != 4 {
			t.Fatalf("夏令时当天: %v", got)
		}
	}
}

func TestEnqueueDueDailyStartHour(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	clock := time.Date(2026, 11, 2, 3, 0, 0, 0, loc) // 周一 03:00,上次是周日 10:00 测完
	svc := &Service{
		Now: func() time.Time { return clock },
		Profile: func(ctx context.Context) (map[string]any, error) {
			return map[string]any{"bandwidth": map[string]any{
				"intervalHours": float64(24), "intervalUnit": "days", "startHour": float64(4), "maxDownloadMB": float64(10),
			}}, nil
		},
		Nodes: func(ctx context.Context) ([]NodeRef, error) {
			return []NodeRef{{Name: "n1", SubscriptionID: "s1", Enabled: true}}, nil
		},
		measures: map[string]Measurement{"n1": {CompletedAt: time.Date(2026, 11, 1, 10, 0, 0, 0, loc).UnixMilli()}},
	}
	if n, err := svc.EnqueueDue(context.Background()); err != nil || n != 0 {
		t.Fatalf("03:00 还没到 04:00,不该入队: %d %v", n, err)
	}
	clock = time.Date(2026, 11, 2, 4, 0, 0, 0, loc)
	if n, err := svc.EnqueueDue(context.Background()); err != nil || n != 1 {
		t.Fatalf("04:00 整该入队: %d %v", n, err)
	}
}
