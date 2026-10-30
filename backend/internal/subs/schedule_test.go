package subs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

var cst = time.FixedZone("CST", 8*3600)

func at(y int, mo time.Month, d, h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, cst) }

func recOf(id string, updated time.Time, plan map[string]any) Record {
	return Record{ID: id, Name: id, URL: "https://x.example/" + id, URLs: []string{"https://x.example/" + id}, Enabled: true, AutoUpdate: plan, UpdatedAt: updated.UnixMilli()}
}

func TestNextDue(t *testing.T) {
	daily := map[string]any{"enabled": true, "days": float64(1), "hour": float64(4)}
	cases := []struct {
		name string
		rec  Record
		want time.Time
		ok   bool
	}{
		{"每天 4 点:下午更新,次日 4 点", recOf("a", at(2026, 10, 28, 15, 0), daily), at(2026, 10, 29, 4, 0), true},
		{"每天 4 点:凌晨 3 点更新,也是次日 4 点", recOf("a", at(2026, 10, 28, 3, 0), daily), at(2026, 10, 29, 4, 0), true},
		{"每 3 天 23 点,跨月", recOf("a", at(2026, 10, 30, 8, 0), map[string]any{"enabled": true, "days": float64(3), "hour": float64(23)}), at(2026, 11, 2, 23, 0), true},
		{"每隔 6 小时", recOf("a", at(2026, 10, 28, 10, 30), map[string]any{"enabled": true, "mode": "hours", "hours": float64(6)}), at(2026, 10, 28, 16, 30), true},
		{"没开", recOf("a", at(2026, 10, 28, 1, 0), map[string]any{"enabled": false, "days": float64(1), "hour": float64(4)}), time.Time{}, false},
		{"没有计划", recOf("a", at(2026, 10, 28, 1, 0), nil), time.Time{}, false},
		{"hours 模式缺 hours", recOf("a", at(2026, 10, 28, 1, 0), map[string]any{"enabled": true, "mode": "hours"}), time.Time{}, false},
		{"days 非法", recOf("a", at(2026, 10, 28, 1, 0), map[string]any{"enabled": true, "days": float64(0), "hour": float64(4)}), time.Time{}, false},
		{"hour 越界", recOf("a", at(2026, 10, 28, 1, 0), map[string]any{"enabled": true, "days": float64(1), "hour": float64(24)}), time.Time{}, false},
		{"从没更新过:马上到期", Record{ID: "a", Enabled: true, URLs: []string{"https://x"}, AutoUpdate: daily}, time.Time{}, true},
	}
	for _, c := range cases {
		got, ok := NextDue(c.rec, cst)
		if ok != c.ok || (ok && !got.Equal(c.want)) {
			t.Errorf("%s: got (%v,%v) want (%v,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
	stopped := recOf("a", at(2026, 10, 28, 1, 0), daily)
	stopped.Enabled = false
	if _, ok := NextDue(stopped, cst); ok {
		t.Error("停用的订阅不更新")
	}
	pasted := recOf("a", at(2026, 10, 28, 1, 0), daily)
	pasted.URL, pasted.URLs, pasted.Content = "", nil, "vmess://..."
	if _, ok := NextDue(pasted, cst); ok {
		t.Error("粘贴内容的节点模式没有可重新拉取的来源,不更新")
	}
}

type fakeSvc struct {
	mu      sync.Mutex
	recs    []Record
	listErr error
	failIDs map[string]bool
	calls   []string
	renames []map[string]any
}

func (f *fakeSvc) List(context.Context) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Record(nil), f.recs...), f.listErr
}

func (f *fakeSvc) Refresh(_ context.Context, id string, rename map[string]any) (*SaveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	f.renames = append(f.renames, rename)
	if f.failIDs[id] {
		return nil, errors.New("拉取失败: Get \"https://x.example/secret-token\": timeout")
	}
	return &SaveResult{ID: id, NodeCount: 3}, nil
}

func newSched(f *fakeSvc, now *time.Time) *Scheduler {
	s := NewScheduler(f)
	s.Loc = cst
	s.Now = func() time.Time { return *now }
	return s
}

func TestRunOnceRefreshesOnlyDue(t *testing.T) {
	daily := map[string]any{"enabled": true, "days": float64(1), "hour": float64(4)}
	now := at(2026, 10, 29, 4, 1)
	f := &fakeSvc{recs: []Record{
		recOf("due", at(2026, 10, 28, 15, 0), daily), // 今天 4 点到期
		recOf("later", at(2026, 10, 28, 15, 0), map[string]any{"enabled": true, "days": float64(1), "hour": float64(9)}), // 9 点才到
		recOf("off", at(2026, 10, 1, 0, 0), map[string]any{"enabled": false, "days": float64(1), "hour": float64(4)}),
	}}
	s := newSched(f, &now)
	if n := s.RunOnce(context.Background()); n != 1 || len(f.calls) != 1 || f.calls[0] != "due" {
		t.Fatalf("n=%d calls=%v", n, f.calls)
	}
	if f.renames[0] != nil {
		t.Error("定时更新不改改名规则")
	}
}

func TestRunOnceBackoffAndRecovery(t *testing.T) {
	plan := map[string]any{"enabled": true, "mode": "hours", "hours": float64(1)}
	now := at(2026, 10, 29, 12, 0)
	f := &fakeSvc{recs: []Record{recOf("a", at(2026, 10, 29, 10, 0), plan)}, failIDs: map[string]bool{"a": true}}
	s := newSched(f, &now)
	step := func(d time.Duration) int { now = now.Add(d); return s.RunOnce(context.Background()) }
	if s.RunOnce(context.Background()) != 1 {
		t.Fatal("第一次应尝试")
	}
	if step(5*time.Minute) != 0 {
		t.Error("10 分钟内不重试")
	}
	if step(6*time.Minute) != 1 { // 距第一次失败 11 分钟
		t.Error("10 分钟后应重试")
	}
	if step(20*time.Minute) != 0 { // 第二次失败后要等 30 分钟
		t.Error("第二次失败后应等 30 分钟")
	}
	if step(11*time.Minute) != 1 {
		t.Error("30 分钟后应重试")
	}
	if step(59*time.Minute) != 0 || step(2*time.Minute) != 1 { // 第三次起固定 60 分钟
		t.Error("第三次起每 60 分钟重试")
	}
	f.mu.Lock()
	f.failIDs["a"] = false
	f.mu.Unlock()
	step(61 * time.Minute) // 成功:清掉失败记录
	if len(s.retry) != 0 {
		t.Errorf("成功后应清掉失败记录: %v", s.retry)
	}
	// 订阅被删:失败记录一并清掉
	f.mu.Lock()
	f.failIDs["a"] = true
	f.mu.Unlock()
	step(2 * time.Hour)
	f.mu.Lock()
	f.recs = nil
	f.mu.Unlock()
	s.RunOnce(context.Background())
	if len(s.retry) != 0 {
		t.Errorf("订阅删了失败记录也该清: %v", s.retry)
	}
}

func TestRunOnceListErrorAndCancel(t *testing.T) {
	now := at(2026, 10, 29, 12, 0)
	f := &fakeSvc{listErr: errors.New("boom")}
	if n := newSched(f, &now).RunOnce(context.Background()); n != 0 {
		t.Error(n)
	}
	plan := map[string]any{"enabled": true, "mode": "hours", "hours": float64(1)}
	f = &fakeSvc{recs: []Record{recOf("a", at(2026, 10, 1, 0, 0), plan)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n := newSched(f, &now).RunOnce(ctx); n != 0 || len(f.calls) != 0 {
		t.Errorf("ctx 已取消不应拉取: %d %v", n, f.calls)
	}
}

func TestRedact(t *testing.T) {
	if got := redact(`Get "https://x.example/sub?token=abc": timeout`); got != `Get "<url>": timeout` {
		t.Errorf("订阅地址应被去掉: %q", got)
	}
	if got := redact("没有地址"); got != "没有地址" {
		t.Error(got)
	}
}

func TestRunWaitsStartDelayThenStops(t *testing.T) {
	plan := map[string]any{"enabled": true, "mode": "hours", "hours": float64(1)}
	f := &fakeSvc{recs: []Record{recOf("a", time.Now().Add(-5*time.Hour), plan)}}
	s := NewScheduler(f)
	s.StartDelay, s.Tick = 40*time.Millisecond, 10*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	time.Sleep(15 * time.Millisecond)
	f.mu.Lock()
	early := len(f.calls)
	f.mu.Unlock()
	if early != 0 {
		t.Error("启动延迟内不应更新")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.calls)
		f.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 Run 应退出")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Error("延迟过后应更新到期的订阅")
	}
}
