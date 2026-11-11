package nodequality

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newSvc(t *testing.T, subs []Subscription) (*Service, *clock) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: t0}
	return &Service{
		Store:   st,
		Now:     c.now,
		Catalog: func(context.Context) ([]Subscription, error) { return subs, nil },
	}, c
}

func oneSub(tags ...string) []Subscription {
	var ns []NodeInfo
	for _, tg := range tags {
		ns = append(ns, NodeInfo{Tag: tg, Region: "HK", Protocol: "vless"})
	}
	return []Subscription{{ID: "s1", Name: "机场A", Mode: "subscription", Enabled: true, BandwidthCheck: true, Nodes: ns}}
}

// feed 在 t0 往前每分钟一次、共 n 次(最后一次在 t0 前 1 分钟),delay 按 f 给(0 = 失败)。
func feed(s *Service, tag string, n int, f func(i int) int) {
	for i := 0; i < n; i++ {
		s.ObserveLatency(tag, t0.Add(-time.Duration(n-i)*time.Minute), f(i))
	}
}

func f64(v float64) *float64 { return &v }

func near(a *float64, want float64) bool { return a != nil && math.Abs(*a-want) < 1e-9 }

func query(t *testing.T, s *Service, q Query) Result {
	t.Helper()
	r, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRecordProbeWindowAndOrder(t *testing.T) {
	s, _ := newSvc(t, oneSub("a"))
	at := t0.Add(-time.Minute)
	s.ObserveLatency("a", at, 100)
	s.ObserveLatency("a", at, 100)                      // 同一时刻重复
	s.ObserveLatency("a", at.Add(-time.Minute), 100)    // 同一整点里比最近一次更早:乱序不记
	s.ObserveLatency("a", t0.Add(-48*time.Hour), 100)   // 窗口外
	s.ObserveLatency("a", t0.Add(5*time.Minute), 100)   // 太晚(超过现在 1 分钟)
	s.ObserveLatency("a", t0.Add(-30*time.Second), -1)  // 延时为负
	s.ObserveLatency("a", t0.Add(-90*time.Minute), 100) // 上一个整点里的老样本:记
	r := query(t, s, Query{})
	if got := r.Subscriptions[0].Checks; got != 2 {
		t.Fatalf("checks=%d want 2", got)
	}
}

func TestSummaryMedianAndJitter(t *testing.T) {
	s, _ := newSvc(t, oneSub("a"))
	// 13 次探测:前两次 1000 和 1000→100 的差会被「最近 10 个」挤掉,留下 110,120,...,200 这 10 个延时和 10 个差
	delays := []int{1000, 1000, 100, 110, 120, 130, 140, 150, 160, 170, 180, 190, 200}
	feed(s, "a", len(delays), func(i int) int { return delays[i] })
	r := query(t, s, Query{Details: "s1"})
	n := r.Subscriptions[0].Nodes[0]
	if !near(n.Latency, 155) { // 110..200 的中位数
		t.Fatalf("latency=%v", n.Latency)
	}
	if !near(n.Jitter, 10) { // 最近 10 个差值全是 10
		t.Fatalf("jitter=%v", n.Jitter)
	}
	if n.Checks != 13 || !near(n.Availability, 100) {
		t.Fatalf("checks=%d avail=%v", n.Checks, n.Availability)
	}
}

func TestJitterOnlyConsecutiveSuccessWithinTenMinutes(t *testing.T) {
	s, _ := newSvc(t, oneSub("a"))
	s.ObserveLatency("a", t0.Add(-30*time.Minute), 100)
	s.ObserveLatency("a", t0.Add(-25*time.Minute), 200) // 与上一次相隔 5 分钟:差 100
	s.ObserveLatency("a", t0.Add(-5*time.Minute), 300)  // 相隔 20 分钟:不算
	s.ObserveLatency("a", t0.Add(-4*time.Minute), 0)    // 失败
	s.ObserveLatency("a", t0.Add(-3*time.Minute), 500)  // 上一个是失败:不算
	n := query(t, s, Query{Details: "s1"}).Subscriptions[0].Nodes[0]
	if !near(n.Jitter, 100) {
		t.Fatalf("jitter=%v", n.Jitter)
	}
	if !near(n.Latency, 250) { // 成功的 100,200,300,500 的中位数
		t.Fatalf("latency=%v", n.Latency)
	}
	if !near(n.Availability, 80) {
		t.Fatalf("avail=%v", n.Availability)
	}
}

func TestStateDownStaleUnknown(t *testing.T) {
	s, _ := newSvc(t, oneSub("up", "dn", "old", "none"))
	s.ObserveLatency("up", t0.Add(-time.Minute), 80)
	s.ObserveLatency("dn", t0.Add(-time.Minute), 0)
	s.ObserveLatency("old", t0.Add(-11*time.Minute), 80) // 超过 10 分钟新鲜时限
	r := query(t, s, Query{Details: "s1", AllNodes: true})
	st := map[string]string{}
	for _, n := range r.Subscriptions[0].Nodes {
		st[n.Tag] = n.State
	}
	want := map[string]string{"up": "healthy", "dn": "down", "old": "stale", "none": "unknown"}
	for k, v := range want {
		if st[k] != v {
			t.Errorf("%s = %s want %s", k, st[k], v)
		}
	}
	sq := r.Subscriptions[0]
	// up 没有下载样本也有暂估分,所以已评估 = up + dn;未知 = stale + none
	if sq.Usable != 1 || sq.Failed != 1 || sq.Unknown != 2 || sq.Total != 4 || sq.Assessed != 2 {
		t.Fatalf("%+v", sq)
	}
	// 新鲜时限可调
	s.FreshFor = time.Hour
	r = query(t, s, Query{Details: "s1", AllNodes: true})
	for _, n := range r.Subscriptions[0].Nodes {
		if n.Tag == "old" && n.State != "healthy" {
			t.Fatalf("FreshFor 放宽后 old 应为 healthy:%s", n.State)
		}
	}
}

func TestInterpTables(t *testing.T) {
	cases := []struct {
		v    float64
		k    []knot
		want float64
	}{
		{0, latencyKnots, 100}, {50, latencyKnots, 100}, {100, latencyKnots, 90},
		{150, latencyKnots, 83}, // 90 → 75 的中点 82.5 四舍五入
		{400, latencyKnots, 50}, {700, latencyKnots, 25}, {1000, latencyKnots, 0}, {5000, latencyKnots, 0},
		{3, downloadKnots, 0}, {10, downloadKnots, 30}, {20, downloadKnots, 43}, // 30 → 55 的中点 42.5
		{50, downloadKnots, 70}, {300, downloadKnots, 100}, {9999, downloadKnots, 100},
		{5, jitterKnots, 100}, {10, jitterKnots, 90}, {40, jitterKnots, 50}, {150, jitterKnots, 0},
	}
	for _, c := range cases {
		if got := interp(c.v, c.k); got != c.want {
			t.Errorf("interp(%v) = %v want %v", c.v, got, c.want)
		}
	}
}

func okSummary(lat, jit float64, latN, jitN int) summary {
	return summary{Success: 30, FirstAt: 1, LastAt: t0.UnixMilli() - 1000, LastOK: true,
		Latency: f64(lat), LatencyCount: latN, Jitter: f64(jit), JitterCount: jitN}
}

func TestEvaluateScoreAndProvisional(t *testing.T) {
	now, period, fresh := t0.UnixMilli(), int64(12*hourMs), defaultFreshMs
	dl := &download{At: now - 1000, Mbps: 50, Count: 10}
	// 延时 100(90)、波动 10(90)、下载 50(70):(90×40 + 70×30 + 90×30) / 100 = 84
	ev := evaluate(okSummary(100, 10, 10, 10), dl, now, period, fresh)
	if ev.State != "healthy" || ev.Score == nil || *ev.Score != 84 || ev.Provisional {
		t.Fatalf("%+v score=%v", ev, ev.Score)
	}
	// 没下载:只按延时 + 波动归一,(90×40 + 90×30) / 70 = 90,且是暂估
	ev = evaluate(okSummary(100, 10, 10, 10), nil, now, period, fresh)
	if ev.Score == nil || *ev.Score != 90 || !ev.Provisional || ev.Download != nil {
		t.Fatalf("无下载:%+v score=%v", ev, ev.Score)
	}
	// 样本数不足 10 个 → 暂估
	if ev = evaluate(okSummary(100, 10, 9, 10), dl, now, period, fresh); !ev.Provisional {
		t.Fatal("延时样本不足该是暂估")
	}
	if ev = evaluate(okSummary(100, 10, 10, 9), dl, now, period, fresh); !ev.Provisional {
		t.Fatal("波动样本不足该是暂估")
	}
	if ev = evaluate(okSummary(100, 10, 10, 10), &download{At: now - 1000, Mbps: 50, Count: 9}, now, period, fresh); !ev.Provisional {
		t.Fatal("下载样本不足该是暂估")
	}
	// 没有波动(只有一次成功探测)
	sm := okSummary(100, 10, 1, 0)
	sm.Jitter = nil
	if ev = evaluate(sm, dl, now, period, fresh); ev.Score == nil || !ev.Provisional {
		t.Fatalf("没波动也该给暂估分:%+v", ev)
	}
	// 不是 healthy 不给分
	down := okSummary(100, 10, 10, 10)
	down.LastOK = false
	if ev = evaluate(down, dl, now, period, fresh); ev.State != "down" || ev.Score != nil {
		t.Fatalf("down 不该给分:%+v", ev)
	}
	// 没有延时(全失败)不给分
	none := summary{Failure: 3, LastAt: now - 1000}
	if ev = evaluate(none, dl, now, period, fresh); ev.Score != nil || ev.State != "down" {
		t.Fatalf("%+v", ev)
	}
}

func TestEvaluateDownloadFreshness(t *testing.T) {
	now, period := t0.UnixMilli(), int64(12*hourMs)
	sm := okSummary(100, 10, 10, 10)
	// 超过一个周期但不到两个:还在用,标 downloadStale
	ev := evaluate(sm, &download{At: now - 13*hourMs, Mbps: 40, Count: 10, LowerBound: true}, now, period, defaultFreshMs)
	if ev.Download == nil || !ev.DownloadStale || !ev.LowerBound || ev.DownloadAt == nil {
		t.Fatalf("%+v", ev)
	}
	// 满两个周期:不再算当前,但 downloadAt 还在
	ev = evaluate(sm, &download{At: now - 24*hourMs, Mbps: 40, Count: 10}, now, period, defaultFreshMs)
	if ev.Download != nil || ev.DownloadAt == nil {
		t.Fatalf("%+v", ev)
	}
	// 在将来的样本也不算
	ev = evaluate(sm, &download{At: now + hourMs, Mbps: 40, Count: 10}, now, period, defaultFreshMs)
	if ev.Download != nil {
		t.Fatal("将来的下载样本不该算")
	}
}

func TestAggregateGradeAndCoverage(t *testing.T) {
	healthy := func(score int) nodeRow {
		sc := score
		return nodeRow{ev: evaluated{State: "healthy", Score: &sc, Latency: f64(100), Availability: f64(100), Checks: 10}}
	}
	down := nodeRow{ev: evaluated{State: "down", Availability: f64(0), Checks: 10}}
	unknown := nodeRow{ev: evaluated{State: "unknown"}}
	stale := nodeRow{ev: evaluated{State: "stale", Checks: 3, Availability: f64(100)}}

	ag := aggregateNodes([]nodeRow{healthy(90), healthy(80), healthy(100), healthy(90), healthy(90)})
	if ag.Grade != "excellent" || *ag.Score != 90 || ag.Coverage != 1 || ag.Usable != 5 {
		t.Fatalf("%+v score=%v", ag, ag.Score)
	}
	// 4 个有分 + 1 个未知:覆盖率 0.8,刚好不是 pending
	ag = aggregateNodes([]nodeRow{healthy(80), healthy(80), healthy(80), healthy(80), unknown})
	if ag.Grade != "good" || ag.Assessed != 4 || ag.Unknown != 1 {
		t.Fatalf("%+v", ag)
	}
	// 覆盖率不足 0.8:pending(哪怕分数很高)
	ag = aggregateNodes([]nodeRow{healthy(100), healthy(100), unknown, stale, unknown})
	if ag.Grade != "pending" || ag.Unknown != 3 {
		t.Fatalf("%+v", ag)
	}
	// 有 down:attention,down 按 0 分计入平均:(100×4 + 0) / 5 = 80
	ag = aggregateNodes([]nodeRow{healthy(100), healthy(100), healthy(100), healthy(100), down})
	if ag.Grade != "attention" || *ag.Score != 80 || ag.Failed != 1 || ag.Assessed != 5 {
		t.Fatalf("%+v score=%v", ag, ag.Score)
	}
	// 分档边界 75 / 60 / 60 以下
	for sc, want := range map[int]string{85: "excellent", 84: "good", 75: "good", 74: "fair", 60: "fair", 59: "attention"} {
		if g := aggregateNodes([]nodeRow{healthy(sc)}).Grade; g != want {
			t.Errorf("score %d → %s want %s", sc, g, want)
		}
	}
	// 没有节点:pending,分数 null
	if ag = aggregateNodes(nil); ag.Grade != "pending" || ag.Score != nil || ag.Coverage != 0 {
		t.Fatalf("%+v", ag)
	}
	// 可用率是各节点可用率的平均;延时是健康节点延时的中位数
	if ag = aggregateNodes([]nodeRow{healthy(80), down}); !near(ag.Availability, 50) || !near(ag.Latency, 100) {
		t.Fatalf("avail=%v lat=%v", ag.Availability, ag.Latency)
	}
}

func TestDownloadOfAveragesLastTenSuccesses(t *testing.T) {
	now, period := t0.UnixMilli(), int64(6*hourMs)
	var list []BWSample
	for i := 0; i < 12; i++ { // 12 次成功,值 1..12,越往后越新
		list = append(list, BWSample{At: now - int64(12-i)*60_000, Mbps: float64(i + 1)})
	}
	list = append(list, BWSample{At: now - 30_000, Failed: true}, // 失败的不算
		BWSample{At: now - 20_000, Mbps: 0},                   // 0 不算
		BWSample{At: now + hourMs, Mbps: 999},                 // 将来的不算
		BWSample{At: now - 10_000, Mbps: 5, LowerBound: true}) // 最新一次,是下限
	d := downloadOf(list, now, period)
	// 最近 10 次成功:4..12 加上最新的 5(值 5)→ 4+5+...+12 = 72,再加 5 = 77,除以 10
	if d == nil || d.Count != 10 || math.Abs(d.Mbps-7.7) > 1e-9 || d.At != now-10_000 || !d.LowerBound {
		t.Fatalf("%+v", d)
	}
	if downloadOf(list[:1], now, period) != nil && downloadOf([]BWSample{{At: now - 2*period, Mbps: 3}}, now, period) != nil {
		t.Fatal("最近一次早于两个周期应视为没有")
	}
	if downloadOf(nil, now, period) != nil {
		t.Fatal("没有样本")
	}
}

func TestQueryDownloadAndCharts(t *testing.T) {
	s, _ := newSvc(t, oneSub("a"))
	nowMs := t0.UnixMilli()
	bw := []BWSample{
		{At: nowMs - int64(30*time.Minute/time.Millisecond), Mbps: 60},
		{At: nowMs - int64(10*time.Minute/time.Millisecond), Failed: true},
	}
	s.Bandwidth = func(tag string) []BWSample {
		if tag == "a" {
			return bw
		}
		return nil
	}
	s.History = func(_ context.Context, tag string) []HistSample {
		// 一条自己攒的里没有的(2 分钟前)和一条重叠的(1 分钟前,以自己攒的为准)
		return []HistSample{{At: nowMs - 2*60_000, Delay: 77}, {At: nowMs - 60_000, Delay: 12345}}
	}
	s.ObserveLatency("a", t0.Add(-time.Minute), 50)
	r := query(t, s, Query{Details: "s1", Node: "a"})
	sq := r.Subscriptions[0]
	n := sq.Nodes[0]
	if !near(n.Download, 60) || sq.BandwidthCount != 1 || n.DownloadAt == nil || n.DownloadStale {
		t.Fatalf("download=%v %+v", n.Download, n)
	}
	if n.Upload != nil || sq.Upload != nil {
		t.Fatal("没有上传测速时应为 null")
	}
	if n.Charts == nil || !n.Charts.BandwidthEnabled || len(n.Charts.Bandwidth) != 2 || !n.Charts.Bandwidth[1].Failed {
		t.Fatalf("带宽图表:%+v", n.Charts)
	}
	if len(n.Charts.Latency) != 2 || n.Charts.Latency[0].Value != 77 || n.Charts.Latency[1].Value != 50 {
		t.Fatalf("延迟图表:%+v", n.Charts.Latency)
	}
	// 没带 details 或 node 对不上:不出图表
	if r = query(t, s, Query{Details: "s1", Node: "zzz"}); r.Subscriptions[0].Nodes[0].Charts != nil {
		t.Fatal("node 对不上不该有图表")
	}
	if r = query(t, s, Query{Node: "a"}); len(r.Subscriptions[0].Nodes) != 0 {
		t.Fatal("没有 details 不展开节点")
	}
	// 停用的订阅:图表里没有带宽,节点 inactive
	subs := oneSub("a")
	subs[0].Enabled = false
	s.Catalog = func(context.Context) ([]Subscription, error) { return subs, nil }
	r = query(t, s, Query{Details: "s1", Node: "a"})
	n = r.Subscriptions[0].Nodes[0]
	if !n.Inactive || r.Subscriptions[0].Enabled || n.Charts.BandwidthEnabled || len(n.Charts.Latency) != 0 {
		t.Fatalf("停用:%+v", n)
	}
}

func TestPaginationSortAndSearch(t *testing.T) {
	var tags []string
	for i := 0; i < 25; i++ {
		tags = append(tags, "n"+string(rune('a'+i)))
	}
	subs := oneSub(tags...)
	subs = append(subs, Subscription{ID: "s2", Name: "Other", Mode: "node", Enabled: true, Nodes: []NodeInfo{{Tag: "us-1", Region: "US", Protocol: "trojan"}}})
	s, _ := newSvc(t, subs)
	// us-1 全成功、na 一半失败:可用率高的排前面
	s.ObserveLatency("us-1", t0.Add(-time.Minute), 80)
	s.ObserveLatency("na", t0.Add(-3*time.Minute), 80)
	s.ObserveLatency("na", t0.Add(-2*time.Minute), 0)
	s.ObserveLatency("nb", t0.Add(-time.Minute), 80)

	r := query(t, s, Query{Details: "s1"})
	if r.Total != 2 || r.Page != 1 || r.PageSize != 20 || r.WindowHours != 24 || r.PeriodHours != 12 {
		t.Fatalf("%+v", r)
	}
	if r.Subscriptions[0].ID != "s2" { // 可用率 100 > s1 的平均(75)
		t.Fatalf("订阅应按可用率从高到低:%s", r.Subscriptions[0].ID)
	}
	sq := r.Subscriptions[1]
	if len(sq.Nodes) != 20 || sq.NodePage != 1 || sq.NodePageSize != 20 || sq.Total != 25 {
		t.Fatalf("默认 20 个一页:%d %d %d", len(sq.Nodes), sq.NodePage, sq.NodePageSize)
	}
	if sq.Nodes[0].Tag != "nb" || sq.Nodes[len(sq.Nodes)-1].Availability != nil && sq.Nodes[19].Tag == "na" {
		t.Fatalf("节点应按可用率排:第一个 %s", sq.Nodes[0].Tag)
	}
	r = query(t, s, Query{Details: "s1", NodePageSize: 10, NodePage: 3})
	sq = r.Subscriptions[1]
	if len(sq.Nodes) != 5 || sq.NodePage != 3 || sq.NodePageSize != 10 {
		t.Fatalf("第 3 页:%d 个,page=%d", len(sq.Nodes), sq.NodePage)
	}
	r = query(t, s, Query{Details: "s1", NodePage: 99, NodePageSize: 10}) // 页码越界夹到最后一页
	if r.Subscriptions[1].NodePage != 3 {
		t.Fatalf("page=%d", r.Subscriptions[1].NodePage)
	}
	r = query(t, s, Query{Details: "s1", AllNodes: true, NodePageSize: 3})
	if len(r.Subscriptions[1].Nodes) != 25 || r.Subscriptions[1].NodePageSize != 25 {
		t.Fatal("allNodes")
	}
	r = query(t, s, Query{Details: "s1", NodePageSize: 1000})
	if r.Subscriptions[1].NodePageSize != 25 && len(r.Subscriptions[1].Nodes) != 25 {
		t.Fatalf("nodePageSize 上限 100:%d", len(r.Subscriptions[1].Nodes))
	}
	r = query(t, s, Query{PageSize: 1, Page: 2})
	if r.Total != 2 || r.Page != 2 || r.PageSize != 1 || len(r.Subscriptions) != 1 || r.Subscriptions[0].ID != "s1" {
		t.Fatalf("订阅分页:%+v", r)
	}
	r = query(t, s, Query{PageSize: 1, AllSubscriptions: true})
	if len(r.Subscriptions) != 2 || r.PageSize != 2 {
		t.Fatal("allSubscriptions")
	}
	// 搜索只按订阅名,不看节点
	if r = query(t, s, Query{Search: "OTHER"}); r.Total != 1 || r.Subscriptions[0].ID != "s2" {
		t.Fatalf("按订阅名搜:%+v", r.Subscriptions)
	}
	if r = query(t, s, Query{Search: "trojan"}); r.Total != 0 || r.Subscriptions == nil || len(r.Subscriptions) != 0 {
		t.Fatal("搜协议不该命中,且空结果要是空数组")
	}
}

func TestDuplicateTagInOneSubCountedOnce(t *testing.T) {
	subs := oneSub("a", "a", "b")
	s, _ := newSvc(t, subs)
	if r := query(t, s, Query{Details: "s1"}); r.Subscriptions[0].Total != 2 || len(r.Subscriptions[0].Nodes) != 2 {
		t.Fatalf("%+v", r.Subscriptions[0])
	}
}

func TestResponseShape(t *testing.T) {
	s, _ := newSvc(t, oneSub("a"))
	s.ObserveLatency("a", t0.Add(-time.Minute), 50)
	r := query(t, s, Query{Details: "s1", Node: "a"})
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"at", "windowHours", "periodHours", "total", "page", "pageSize", "subscriptions"} {
		if _, ok := m[k]; !ok {
			t.Errorf("响应缺 %s", k)
		}
	}
	sub := m["subscriptions"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "name", "mode", "enabled", "bandwidthCheck", "total", "usable", "failed", "unknown", "assessed",
		"coverage", "score", "grade", "checks", "provisional", "availability", "latency", "jitter", "download", "upload",
		"bandwidthCount", "up", "down", "activeMs", "nodes", "nodePage", "nodePageSize"} {
		if _, ok := sub[k]; !ok {
			t.Errorf("订阅缺 %s", k)
		}
	}
	if v, ok := sub["upload"]; !ok || v != nil {
		t.Errorf("upload 要是 null:%v", v)
	}
	node := sub["nodes"].([]any)[0].(map[string]any)
	for _, k := range []string{"availability", "latency", "jitter", "download", "upload", "state", "score", "provisional", "checks",
		"firstAt", "lastAt", "downloadAt", "lowerBound", "downloadStale", "activeMs", "up", "down", "tag", "protocol", "region", "inactive", "charts"} {
		if _, ok := node[k]; !ok {
			t.Errorf("节点缺 %s", k)
		}
	}
	if _, ok := node["charts"].(map[string]any)["bandwidthEnabled"]; !ok {
		t.Error("charts 缺 bandwidthEnabled")
	}
}

func TestTickActivityExcludesProbesAndCountsOncePerSub(t *testing.T) {
	s, c := newSvc(t, oneSub("a", "b"))
	snaps := []singbox.KernelSnapshot{
		{Conns: []singbox.KernelConn{{ID: "1", Source: "192.168.1.5", Chains: []string{"a", "grp"}}}},
		{Conns: []singbox.KernelConn{
			{ID: "1", Source: "192.168.1.5", Chains: []string{"a", "grp"}, Upload: 100, Download: 1000},
			{ID: "2", Source: "192.168.1.6", Chains: []string{"b", "grp"}, Upload: 10, Download: 20},
			{ID: "3", Source: "127.0.0.1", Chains: []string{"a", "grp"}, Upload: 5, Download: 5}, // 面板探测
			{ID: "4", Source: "192.168.1.7", Chains: []string{"DIRECT"}, Upload: 7, Download: 7},
			{ID: "5", Source: "192.168.1.8", Chains: []string{"zzz"}, Upload: 9, Download: 9}, // 不在目录里
		}, DirectTags: []string{"DIRECT"}},
	}
	i := 0
	s.Conns = func(context.Context) (singbox.KernelSnapshot, error) { sn := snaps[i]; i++; return sn, nil }
	ctx := context.Background()
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	c.t = t0.Add(10 * time.Second)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	r := query(t, s, Query{Details: "s1", AllNodes: true})
	sq := r.Subscriptions[0]
	if sq.Up != 110 || sq.Down != 1020 {
		t.Fatalf("订阅流量 up=%d down=%d", sq.Up, sq.Down)
	}
	if sq.ActiveMs != 10_000 {
		t.Fatalf("两个节点同时用,订阅时长只计一次:%d", sq.ActiveMs)
	}
	got := map[string]Node{}
	for _, n := range sq.Nodes {
		got[n.Tag] = n
	}
	if got["a"].Up != 100 || got["a"].ActiveMs != 10_000 || got["b"].Down != 20 || got["b"].ActiveMs != 10_000 {
		t.Fatalf("a=%+v b=%+v", got["a"], got["b"])
	}
}

func TestActiveMsUnionAcrossDifferentTimes(t *testing.T) {
	s, c := newSvc(t, oneSub("a", "b"))
	n := 0
	conn := func(tag string, up int64) singbox.KernelSnapshot {
		return singbox.KernelSnapshot{Conns: []singbox.KernelConn{{ID: tag, Source: "192.168.1.5", Chains: []string{tag, "g"}, Upload: up, Download: up}}}
	}
	snaps := []singbox.KernelSnapshot{conn("a", 0), conn("a", 10), conn("b", 0), conn("b", 10)}
	_ = n
	i := 0
	s.Conns = func(context.Context) (singbox.KernelSnapshot, error) { sn := snaps[i]; i++; return sn, nil }
	ctx := context.Background()
	_ = s.Tick(ctx) // a 基线
	c.t = t0.Add(10 * time.Second)
	_ = s.Tick(ctx) // a 用了 10 秒:[t0, t0+10s)
	s.have = false  // 换成 b 的快照,重新取基线
	c.t = t0.Add(time.Minute)
	_ = s.Tick(ctx) // b 基线
	c.t = t0.Add(time.Minute + 10*time.Second)
	_ = s.Tick(ctx) // b 用了 10 秒:[t0+60s, t0+70s),与 a 的不重叠
	r := query(t, s, Query{Details: "s1", AllNodes: true})
	if got := r.Subscriptions[0].ActiveMs; got != 20_000 {
		t.Fatalf("不重叠的两段加起来:%d", got)
	}
}

func TestActiveSlotsSpanHourBoundary(t *testing.T) {
	n := nodeHours{}
	at := t0.UnixMilli() + 4000 // 12:00:04,往前 10 秒跨过整点
	n.recordTraffic(at, 1, 1, 10_000)
	sm := n.summarize(at, nil)
	if sm.ActiveMs != 10_000 {
		t.Fatalf("跨整点:%d", sm.ActiveMs)
	}
	if len(n) != 2 {
		t.Fatalf("应该落在两个整点:%d", len(n))
	}
	// 超过 10 秒的增量只铺 10 秒
	m := nodeHours{}
	m.recordTraffic(at, 1, 1, 60_000)
	if ms := m.summarize(at, nil).ActiveMs; ms != 10_000 {
		t.Fatalf("上限 10 秒:%d", ms)
	}
}

func TestFlushAndReload(t *testing.T) {
	s, c := newSvc(t, oneSub("a"))
	feed(s, "a", 5, func(int) int { return 90 })
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	s2 := &Service{Store: s.Store, Now: c.now, Catalog: s.Catalog}
	if r := query(t, s2, Query{}); r.Subscriptions[0].Checks != 5 {
		t.Fatalf("重载后 checks=%d", r.Subscriptions[0].Checks)
	}
	// 一天多以后样本落到窗口外
	c.t = t0.Add(25 * time.Hour)
	r := query(t, s2, Query{})
	if r.Subscriptions[0].Checks != 0 || r.Subscriptions[0].Unknown != 1 {
		t.Fatalf("窗口外:%+v", r.Subscriptions[0])
	}
	// 落盘时丢掉窗口外的整点
	s2.dirty = true
	if err := s2.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s2.data.Nodes) != 0 {
		t.Fatalf("窗口外的节点应被清掉:%d", len(s2.data.Nodes))
	}
}

func TestOldFormatFileIsIgnored(t *testing.T) {
	s, c := newSvc(t, oneSub("a"))
	if err := s.Store.Save(fileName, map[string]any{"nodes": map[string]any{"a": map[string]any{"b": []any{}}}, "subs": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	s2 := &Service{Store: s.Store, Now: c.now, Catalog: s.Catalog}
	if r := query(t, s2, Query{}); r.Subscriptions[0].Checks != 0 {
		t.Fatal("旧格式的文件应当忽略而不是报错")
	}
}
