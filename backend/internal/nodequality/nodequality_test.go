package nodequality

import (
	"context"
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
	return []Subscription{{ID: "s1", Name: "机场A", Mode: "subscription", Nodes: ns}}
}

// feed 在 t0 往前每 3 分钟一次、共 n 次,delay 按 f 给(<=0 失败)。
func feed(s *Service, tag string, n int, f func(i int) int) {
	for i := 0; i < n; i++ {
		at := t0.Add(-time.Duration(n-i) * 3 * time.Minute)
		s.ObserveLatency(tag, at, f(i))
	}
}

func TestObserveLatencyStatsAndJitter(t *testing.T) {
	s, _ := newSvc(t, oneSub("a"))
	s.ObserveLatency("a", t0.Add(-3*time.Minute), 100)
	s.ObserveLatency("a", t0.Add(-2*time.Minute), 130) // 抖动 30
	s.ObserveLatency("a", t0.Add(-1*time.Minute), 0)   // 失败,清掉基线
	s.ObserveLatency("a", t0, 200)                     // 失败后第一次成功,不算抖动
	r, err := s.Query(context.Background(), Query{Details: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	n := r.Subscriptions[0].Nodes[0]
	if n.Checks != 4 || *n.Availability != 75 {
		t.Fatalf("checks=%d avail=%v", n.Checks, n.Availability)
	}
	if *n.Latency != 143.3 || *n.Jitter != 30 {
		t.Fatalf("latency=%v jitter=%v", *n.Latency, *n.Jitter)
	}
	if n.State != "healthy" || n.Score != nil {
		t.Fatalf("state=%s score=%v", n.State, n.Score)
	}
}

func TestObserveIdempotentAndOutOfOrder(t *testing.T) {
	s, _ := newSvc(t, oneSub("a"))
	at := t0.Add(-time.Minute)
	s.ObserveLatency("a", at, 100)
	s.ObserveLatency("a", at, 100) // 同一时刻重复
	s.ObserveLatency("a", t0.Add(-5*time.Minute), 100)
	s.ObserveLatency("a", t0.Add(-48*time.Hour), 100) // 窗口外
	r, _ := s.Query(context.Background(), Query{})
	if got := r.Subscriptions[0].Checks; got != 2 {
		t.Fatalf("checks=%d want 2", got)
	}
}

func TestStateDownStaleUnknown(t *testing.T) {
	s, c := newSvc(t, oneSub("up", "dn", "old", "none"))
	s.ObserveLatency("up", t0.Add(-time.Minute), 80)
	s.ObserveLatency("dn", t0.Add(-time.Minute), 0)
	s.ObserveLatency("old", t0.Add(-8*time.Hour), 80)
	c.t = t0
	r, _ := s.Query(context.Background(), Query{Details: "s1", AllNodes: true})
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
	if sq.Usable != 1 || sq.Failed != 1 || sq.Unknown != 2 || sq.Total != 4 {
		t.Fatalf("%+v", sq)
	}
}

func f64(v float64) *float64 { return &v }

func TestNodeScoreGates(t *testing.T) {
	good := stats{checks: 30, ok: 30, latSum: 30 * 100, jitSum: 30, jitN: 29}
	if sc, _ := nodeScore(good, nil, nil, 2*minObserveMs); sc != nil {
		t.Fatal("没有下载样本不该给分")
	}
	if sc, _ := nodeScore(good, f64(100), nil, minObserveMs-1); sc != nil {
		t.Fatal("观察不满 1 小时不该给分")
	}
	if sc, _ := nodeScore(stats{checks: 19, ok: 19}, f64(100), nil, 2*minObserveMs); sc != nil {
		t.Fatal("检测不满 20 次不该给分")
	}
	if sc, _ := nodeScore(stats{checks: 30, ok: 9}, f64(100), nil, 2*minObserveMs); sc != nil {
		t.Fatal("成功不满 10 次不该给分")
	}
	sc, prov := nodeScore(good, f64(100), nil, 2*minObserveMs)
	if sc == nil || *sc != 100 || !prov {
		t.Fatalf("满分暂估:%v %v", sc, prov)
	}
	sc, prov = nodeScore(good, f64(100), f64(50), 2*minObserveMs)
	if sc == nil || *sc != 100 || prov {
		t.Fatalf("有上传:%v %v", sc, prov)
	}
	// 下载 50Mbps:其余满分,上传缺测 → (0.35+0.20+0.15+0.20*0.5)/0.9 = 88.9
	sc, _ = nodeScore(good, f64(50), nil, 2*minObserveMs)
	if sc == nil || *sc != 89 {
		t.Fatalf("归一:%v", sc)
	}
}

// 对应说明文字里的两个例子。
func TestSubscriptionScoreExamples(t *testing.T) {
	tags := []string{"n0", "n1", "n2", "n3", "n4", "n5", "n6", "n7", "n8", "n9"}
	s, _ := newSvc(t, oneSub(tags...))
	feed(s, "n0", 30, func(int) int { return 50 })
	s.ObserveBandwidth("n0", t0.Add(-time.Minute), 200, false)
	for _, tg := range tags[1:] {
		s.ObserveLatency(tg, t0.Add(-time.Minute), 0) // 当前失败
	}
	r, _ := s.Query(context.Background(), Query{Details: "s1", AllNodes: true})
	sq := r.Subscriptions[0]
	if sq.Failed != 9 || sq.Assessed != 10 || sq.Coverage != 1 {
		t.Fatalf("%+v", sq)
	}
	if sq.Score == nil || *sq.Score < 8 || *sq.Score > 11 {
		t.Fatalf("1 个满分 + 9 个失败应约 10 分:%v", sq.Score)
	}
	if sq.Grade != "attention" || !sq.Provisional {
		t.Fatalf("grade=%s prov=%v", sq.Grade, sq.Provisional)
	}

	// 1 个满分 + 9 个未测:待补数据
	s2, _ := newSvc(t, oneSub(tags...))
	feed(s2, "n0", 30, func(int) int { return 50 })
	s2.ObserveBandwidth("n0", t0.Add(-time.Minute), 200, false)
	r, _ = s2.Query(context.Background(), Query{})
	sq = r.Subscriptions[0]
	if sq.Assessed != 1 || sq.Coverage != 0.1 || sq.Grade != "pending" || sq.Unknown != 9 {
		t.Fatalf("%+v", sq)
	}
	if sq.Score == nil || *sq.Score < 90 {
		t.Fatalf("已测均分:%v", sq.Score)
	}
}

func TestBandwidthWindowAndFailed(t *testing.T) {
	s, _ := newSvc(t, oneSub("a"))
	s.BandwidthWindow = func(context.Context) time.Duration { return 2 * time.Hour }
	s.ObserveLatency("a", t0.Add(-time.Minute), 50)
	s.ObserveBandwidth("a", t0.Add(-3*time.Hour), 80, false) // 超过两个周期
	r, _ := s.Query(context.Background(), Query{Details: "s1"})
	if r.Subscriptions[0].Nodes[0].Download != nil || r.Subscriptions[0].BandwidthCount != 0 {
		t.Fatal("过期带宽不该参与")
	}
	s.ObserveBandwidth("a", t0.Add(-30*time.Minute), 60, false)
	s.ObserveBandwidth("a", t0.Add(-10*time.Minute), 0, true) // 失败不覆盖上一次成功值
	r, _ = s.Query(context.Background(), Query{Details: "s1", Node: "a"})
	n := r.Subscriptions[0].Nodes[0]
	if n.Download == nil || *n.Download != 60 || r.Subscriptions[0].BandwidthCount != 1 {
		t.Fatalf("download=%v", n.Download)
	}
	if n.Charts == nil || len(n.Charts.Bandwidth) != 3 || !n.Charts.Bandwidth[2].Failed || len(n.Charts.Latency) != 1 {
		t.Fatalf("charts=%+v", n.Charts)
	}
	if n.Upload != nil || r.Subscriptions[0].Upload != nil {
		t.Fatal("没有上传测速时应为 null")
	}
}

func TestSearchDetailsPaging(t *testing.T) {
	var tags []string
	for i := 0; i < 15; i++ {
		tags = append(tags, "hk-"+string(rune('a'+i)))
	}
	subs := oneSub(tags...)
	subs = append(subs, Subscription{ID: "s2", Name: "other", Mode: "node", Nodes: []NodeInfo{{Tag: "us-1", Region: "US", Protocol: "trojan"}}})
	s, _ := newSvc(t, subs)
	r, _ := s.Query(context.Background(), Query{Details: "s1", NodePageSize: 10})
	if len(r.Subscriptions) != 2 || len(r.Subscriptions[0].Nodes) != 10 || r.Subscriptions[0].Total != 15 || r.Subscriptions[1].Nodes != nil {
		t.Fatalf("分页:%d 个订阅,第一个 %d 个节点", len(r.Subscriptions), len(r.Subscriptions[0].Nodes))
	}
	r, _ = s.Query(context.Background(), Query{Details: "s1", AllNodes: true})
	if len(r.Subscriptions[0].Nodes) != 15 {
		t.Fatal("allNodes")
	}
	r, _ = s.Query(context.Background(), Query{Search: "TROJAN"})
	if len(r.Subscriptions) != 1 || r.Subscriptions[0].ID != "s2" {
		t.Fatalf("按协议搜:%+v", r.Subscriptions)
	}
	r, _ = s.Query(context.Background(), Query{Search: "机场", Details: "s1", NodePageSize: 3})
	if len(r.Subscriptions) != 1 || len(r.Subscriptions[0].Nodes) != 3 {
		t.Fatal("订阅名命中时节点不过滤")
	}
	r, _ = s.Query(context.Background(), Query{Search: "nothing"})
	if r.Subscriptions == nil || len(r.Subscriptions) != 0 {
		t.Fatal("空结果要是空数组")
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
	r, _ := s.Query(ctx, Query{Details: "s1", AllNodes: true})
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
	if got["a"].Up != 100 || got["a"].ActiveMs != 10_000 || got["b"].Down != 20 {
		t.Fatalf("a=%+v b=%+v", got["a"], got["b"])
	}
}

func TestFlushAndReload(t *testing.T) {
	s, c := newSvc(t, oneSub("a"))
	feed(s, "a", 5, func(int) int { return 90 })
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	s2 := &Service{Store: s.Store, Now: c.now, Catalog: s.Catalog}
	r, _ := s2.Query(context.Background(), Query{})
	if r.Subscriptions[0].Checks != 5 {
		t.Fatalf("重载后 checks=%d", r.Subscriptions[0].Checks)
	}
	// 一天后样本落到窗口外
	c.t = t0.Add(25 * time.Hour)
	r, _ = s2.Query(context.Background(), Query{})
	if r.Subscriptions[0].Checks != 0 || r.Subscriptions[0].Unknown != 1 {
		t.Fatalf("窗口外:%+v", r.Subscriptions[0])
	}
}
