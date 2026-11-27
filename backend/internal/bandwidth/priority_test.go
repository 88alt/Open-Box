package bandwidth

import (
	"context"
	"testing"
)

// 上游 v0.1.493 #1010:手动测速要优先插队。
// 以前 RunOnce 直接 range 取 map 第一个,Go 的遍历顺序随机,手动点的那台可能排在
// 几十个后台巡检项后面;现在必须先把 Manual 的处理掉。
func TestPickNextManualFirst(t *testing.T) {
	svc := &Service{
		pending: map[string]queueItem{
			"bg1":  {SubID: "s1", RequestedAt: 100, Manual: false},
			"bg2":  {SubID: "s1", RequestedAt: 101, Manual: false},
			"man1": {SubID: "s1", RequestedAt: 200, Manual: true},
		},
	}
	// 即使手动项的 RequestedAt 更晚,也要先挑它
	name, item := svc.pickNextLocked()
	if name != "man1" || !item.Manual {
		t.Fatalf("应先挑手动项,拿到 %q (manual=%v)", name, item.Manual)
	}
}

// 两个手动项之间按请求时间先到先测。
func TestPickNextManualEarliestFirst(t *testing.T) {
	svc := &Service{
		pending: map[string]queueItem{
			"man-late":  {RequestedAt: 300, Manual: true},
			"man-early": {RequestedAt: 100, Manual: true},
		},
	}
	for i := 0; i < 3; i++ { // 多跑几次,确认不是碰巧
		name, _ := svc.pickNextLocked()
		if name != "man-early" {
			t.Fatalf("同级应取最早的,第 %d 次拿到 %q", i, name)
		}
	}
}

// 没有手动项时按后台项的先到先测。
func TestPickNextBackgroundByTime(t *testing.T) {
	svc := &Service{
		pending: map[string]queueItem{
			"bg-late":  {RequestedAt: 200},
			"bg-early": {RequestedAt: 50},
		},
	}
	name, _ := svc.pickNextLocked()
	if name != "bg-early" {
		t.Fatalf("没有手动项时应取最早的,拿到 %q", name)
	}
}

// 队列空时返回空名字。
func TestPickNextEmpty(t *testing.T) {
	svc := &Service{pending: map[string]queueItem{}}
	if name, _ := svc.pickNextLocked(); name != "" {
		t.Fatalf("空队列应返回空名字,拿到 %q", name)
	}
}

// 端到端:手动项插在后台项后面进队,RunOnce 也要先测它。
func TestRunOncePrefersManual(t *testing.T) {
	var measured []string
	svc := &Service{
		Nodes: func(context.Context) ([]NodeRef, error) {
			return []NodeRef{
				{Name: "bg", SubscriptionID: "s1", Enabled: true},
				{Name: "man", SubscriptionID: "s1", Enabled: true},
			}, nil
		},
		Measure: func(_ context.Context, node string, _ int64) MeasureResult {
			measured = append(measured, node)
			return MeasureResult{Mbps: 10}
		},
		pending: map[string]queueItem{},
	}
	// 先排一个后台项(巡检),再手动排另一个
	svc.mu.Lock()
	svc.pending["bg"] = queueItem{SubID: "s1", RequestedAt: 1, Manual: false}
	svc.pending["man"] = queueItem{SubID: "s1", RequestedAt: 2, Manual: true}
	svc.mu.Unlock()

	svc.RunOnce(context.Background())
	if len(measured) != 1 || measured[0] != "man" {
		t.Fatalf("第一次应测手动项,实际测了 %v", measured)
	}
}

// 测到一半又被重新排队(用户手动点了重测):这次的结果要丢弃,
// 不能把新请求的 pending 清掉、也不能覆盖它的时间戳 —— 否则界面会「刚点重测就蹦出旧数」。
func TestRecordDiscardsStaleResult(t *testing.T) {
	svc := &Service{
		pending:  map[string]queueItem{},
		measures: map[string]Measurement{},
	}
	// 排队项是「旧请求」(RequestedAt=100),但队列里同名节点已经有「新请求」(RequestedAt=200)
	svc.pending["n1"] = queueItem{RequestedAt: 200, Manual: true}
	svc.record("n1", queueItem{RequestedAt: 100}, MeasureResult{Mbps: 1})

	if _, ok := svc.measures["n1"]; ok {
		t.Fatalf("旧请求的结果不该被写入: %+v", svc.measures["n1"])
	}
	if q := svc.pending["n1"]; q.RequestedAt != 200 || !q.Manual {
		t.Fatalf("新请求的排队项不该被动过: %+v", q)
	}
}

// 没有更新的请求时,结果照常写入。
func TestRecordWritesWhenNoNewerRequest(t *testing.T) {
	svc := &Service{pending: map[string]queueItem{}, measures: map[string]Measurement{}}
	svc.record("n1", queueItem{RequestedAt: 100}, MeasureResult{Mbps: 42})
	m, ok := svc.measures["n1"]
	if !ok || m.Mbps != 42 || m.Pending {
		t.Fatalf("%+v", m)
	}
}

// 上游 v0.1.493 #1009:限流/封禁时降档重试。
// 第一次按完整体积被 429,降到 1/4 能拿到数 → 采用它,并标 LowerBound。
func TestRunOnceDowngradesOnRateLimit(t *testing.T) {
	var sizes []int64
	svc := &Service{
		Nodes: func(context.Context) ([]NodeRef, error) {
			return []NodeRef{{Name: "n1", SubscriptionID: "s1", Enabled: true}}, nil
		},
		Measure: func(_ context.Context, _ string, maxBytes int64) MeasureResult {
			sizes = append(sizes, maxBytes)
			if len(sizes) == 1 {
				return MeasureResult{Failed: true, Reason: "rate_limited"}
			}
			return MeasureResult{Mbps: 8}
		},
		pending:  map[string]queueItem{"n1": {RequestedAt: 1, Manual: true}},
		measures: map[string]Measurement{},
	}
	svc.RunOnce(context.Background())

	if len(sizes) < 2 {
		t.Fatalf("限流后应降档重试,实际只测了 %d 次: %v", len(sizes), sizes)
	}
	if sizes[1] >= sizes[0] {
		t.Fatalf("第二次体积应更小: %v", sizes)
	}
	m := svc.measures["n1"]
	if m.Mbps != 8 || m.Pending {
		t.Fatalf("应采用降档后拿到的数: %+v", m)
	}
	if !m.LowerBound {
		t.Fatal("降档测出来的值应标成 LowerBound(只保证至少这么快)")
	}
}

// 非限流类失败(比如超时)不降档,避免无谓地多打流量。
func TestRunOnceNoDowngradeOnOtherFailure(t *testing.T) {
	var sizes []int64
	svc := &Service{
		Nodes: func(context.Context) ([]NodeRef, error) {
			return []NodeRef{{Name: "n1", SubscriptionID: "s1", Enabled: true}}, nil
		},
		Measure: func(_ context.Context, _ string, maxBytes int64) MeasureResult {
			sizes = append(sizes, maxBytes)
			return MeasureResult{Failed: true, Reason: "timeout"}
		},
		pending:  map[string]queueItem{"n1": {RequestedAt: 1, Manual: true}},
		measures: map[string]Measurement{},
	}
	svc.RunOnce(context.Background())
	if len(sizes) != 1 {
		t.Fatalf("超时不该降档重试,实际测了 %d 次", len(sizes))
	}
}
