package failover

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type fakeKernel struct {
	mu        sync.Mutex
	sel       map[string]string
	selErr    error
	fail      map[string]bool  // 节点 → 测不通
	errFor    map[string]error // 节点 → 返回这个错误
	delays    []string         // 测过的节点(按顺序)
	urls      []string
	selects   [][2]string
	selectErr error
	block     chan struct{} // 非 nil 时 Delay 先等它
	inflight  int
	maxFlight int
}

func newKernel() *fakeKernel {
	return &fakeKernel{sel: map[string]string{"G": "a", "sub2": "c1"}, fail: map[string]bool{}, errFor: map[string]error{}}
}

func (k *fakeKernel) Selected(context.Context) (map[string]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.selErr != nil {
		return nil, k.selErr
	}
	out := map[string]string{}
	for a, b := range k.sel {
		out[a] = b
	}
	return out, nil
}

func (k *fakeKernel) Delay(ctx context.Context, name, u string, _ time.Duration) (time.Duration, error) {
	k.mu.Lock()
	k.delays = append(k.delays, name)
	k.urls = append(k.urls, u)
	k.inflight++
	k.maxFlight = max(k.maxFlight, k.inflight)
	blk := k.block
	k.mu.Unlock()
	defer func() { k.mu.Lock(); k.inflight--; k.mu.Unlock() }()
	if blk != nil {
		select {
		case <-blk:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if e := k.errFor[name]; e != nil {
		return 0, e
	}
	if k.fail[name] {
		return 0, &singbox.KernelError{Status: 503, Message: "An error occurred in the delay test"}
	}
	return 50 * time.Millisecond, nil
}

func (k *fakeKernel) Select(_ context.Context, group, choice string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.selectErr != nil {
		return k.selectErr
	}
	k.sel[group] = choice
	k.selects = append(k.selects, [2]string{group, choice})
	return nil
}

func (k *fakeKernel) setFail(names ...string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.fail = map[string]bool{}
	for _, n := range names {
		k.fail[n] = true
	}
}

func (k *fakeKernel) selected(g string) string { k.mu.Lock(); defer k.mu.Unlock(); return k.sel[g] }
func (k *fakeKernel) calls() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.delays...)
}
func (k *fakeKernel) resetCalls() { k.mu.Lock(); k.delays, k.urls = nil, nil; k.mu.Unlock() }

// testPlan:主用 a(单节点)、备用 b(单节点)、再备用 sub2(urltest,c1 / c2)。
func testPlan() Plan {
	return Plan{{
		ID: "g1", Tag: "G", RejectTag: "reject",
		Settings: Settings{IntervalMs: 60000, TimeoutMs: 2000, FailureThreshold: 3, RestorePrimary: true, RecoveryHoldMs: 120000, TestURL: "https://t.test/204"},
		Lanes: []Lane{
			{ID: "L0", Name: "主用", Index: 0, Members: []string{"a"}, Valid: []string{"a"}, Mode: "single", Ref: sp("a")},
			{ID: "L1", Name: "备用", Index: 1, Members: []string{"b"}, Valid: []string{"b"}, Mode: "single", Ref: sp("b")},
			{ID: "L2", Name: "再备", Index: 2, Members: []string{"c1", "c2"}, Valid: []string{"c1", "c2"}, Mode: "urltest", Ref: sp("sub2"), SubTag: sp("sub2")},
		},
	}}
}

type env struct {
	t   *testing.T
	k   *fakeKernel
	clk *clock
	m   *Manager
}

func newEnv(t *testing.T, mut func(p Plan)) *env {
	t.Helper()
	k := newKernel()
	clk := &clock{t: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)}
	p := testPlan()
	if mut != nil {
		mut(p)
	}
	m := NewManager(Options{Kernel: k, Now: clk.Now, Concurrency: 4})
	m.SetPlan(p)
	return &env{t: t, k: k, clk: clk, m: m}
}

// round 开始一轮并等它做完;随后把时间推进 adv(默认一个间隔)。
func (e *env) round(adv ...time.Duration) GroupRuntime {
	e.t.Helper()
	e.m.Step(context.Background())
	e.m.Wait()
	g := e.m.Snapshot()[0]
	d := 60 * time.Second
	if len(adv) > 0 {
		d = adv[0]
	}
	e.clk.Add(d)
	return g
}

func (e *env) lane(g GroupRuntime, id string) LaneRuntime { return g.Lanes[id] }

func TestFirstRoundAllUp(t *testing.T) {
	e := newEnv(t, nil)
	if g := e.m.Snapshot()[0]; g.Status != "pending" || g.LastRoundAt != nil {
		t.Fatalf("还没测过应是 pending:%+v", g)
	}
	g := e.round(0)
	if g.Status != "ok" || g.LastRoundAt == nil || g.NextRoundAt == nil || *g.NextRoundAt-*g.LastRoundAt != 60000 {
		t.Fatalf("%+v", g)
	}
	for _, id := range []string{"L0", "L1", "L2"} {
		l := e.lane(g, id)
		if l.Health != "up" || l.FailStreak != 0 || l.UpSince == nil {
			t.Fatalf("%s:%+v", id, l)
		}
	}
	if g.CurrentLaneID == nil || *g.CurrentLaneID != "L0" || g.KernelNow == nil || *g.KernelNow != "a" || g.InFlight || g.Round != nil {
		t.Fatalf("%+v", g)
	}
	n := e.lane(g, "L0").Nodes["a"]
	if n.OK == nil || !*n.OK || n.Delay == nil || *n.Delay != 50 || n.Reason != nil {
		t.Fatalf("%+v", n)
	}
	// 多节点页签:只测了内核子组选中的 c1,通过就不再测 c2
	if _, ok := e.lane(g, "L2").Nodes["c2"]; ok {
		t.Fatal("c1 通过后不该再测 c2")
	}
	if l2 := e.lane(g, "L2"); l2.KernelNow == nil || *l2.KernelNow != "c1" || l2.Confirmed == nil || !*l2.Confirmed {
		t.Fatalf("%+v", l2)
	}
	if len(e.k.selects) != 0 {
		t.Fatal("一切正常不该切")
	}
	// 没到下一轮时间:再 Step 不会再测
	e.clk.Add(-30 * time.Second)
	e.k.resetCalls()
	e.m.Step(context.Background())
	e.m.Wait()
	if len(e.k.calls()) != 0 {
		t.Fatalf("没到时间不该测:%v", e.k.calls())
	}
}

func TestFailoverOnlyAfterThresholdThenRestoreAfterHold(t *testing.T) {
	e := newEnv(t, nil)
	e.round()
	e.k.setFail("a")
	for i := 1; i <= 2; i++ {
		g := e.round()
		if l := e.lane(g, "L0"); l.Health != "down" || l.FailStreak != i || l.UpSince != nil {
			t.Fatalf("第 %d 轮失败:%+v", i, l)
		}
		if g.Status != "failing" || e.k.selected("G") != "a" {
			t.Fatalf("没确认前不该切:%s %s", g.Status, e.k.selected("G"))
		}
	}
	g := e.round() // 第 3 轮:确认,切
	if e.k.selected("G") != "b" {
		t.Fatalf("应切到 b:%s", e.k.selected("G"))
	}
	if g.LastSwitch == nil || g.LastSwitch.From.LaneID == nil || *g.LastSwitch.From.LaneID != "L0" || *g.LastSwitch.To.LaneID != "L1" || g.LastSwitch.To.Ref != "b" || !strings.Contains(g.LastSwitch.Reason, "连续 3 轮") {
		t.Fatalf("%+v", g.LastSwitch)
	}
	if g.CurrentLaneID == nil || *g.CurrentLaneID != "L1" || g.Status != "backup" {
		t.Fatalf("%+v", g)
	}

	// a 恢复:持续可用满 120 秒才切回
	e.k.setFail()
	g = e.round() // a 开始可用(upSince = 此刻)
	if e.k.selected("G") != "b" || e.lane(g, "L0").UpSince == nil {
		t.Fatalf("刚恢复不该切回:%s", e.k.selected("G"))
	}
	g = e.round() // 60 秒
	if e.k.selected("G") != "b" {
		t.Fatal("才持续 60 秒,不该切回")
	}
	g = e.round() // 120 秒
	if e.k.selected("G") != "a" || !strings.Contains(g.LastSwitch.Reason, "切回") || g.Status != "ok" {
		t.Fatalf("持续可用满 120 秒应切回 a:%s %+v", e.k.selected("G"), g.LastSwitch)
	}
}

func TestNoRestoreWhenRestorePrimaryOff(t *testing.T) {
	e := newEnv(t, func(p Plan) { p[0].Settings.RestorePrimary = false })
	e.round()
	e.k.setFail("a")
	e.round()
	e.round()
	e.round()
	if e.k.selected("G") != "b" {
		t.Fatal("应已切到 b")
	}
	e.k.setFail()
	for i := 0; i < 6; i++ {
		e.round()
	}
	if e.k.selected("G") != "b" {
		t.Fatalf("restorePrimary 关着不该切回:%s", e.k.selected("G"))
	}
}

func TestShortFailuresDoNotSwitch(t *testing.T) {
	e := newEnv(t, nil)
	e.round()
	for i := 0; i < 5; i++ { // 失败两轮、好一轮,反复:永远到不了 3 轮
		e.k.setFail("a")
		e.round()
		e.round()
		e.k.setFail()
		e.round()
	}
	if len(e.k.selects) != 0 {
		t.Fatalf("不该切:%v", e.k.selects)
	}
}

func TestAllDownGoesToRejectThenRecovers(t *testing.T) {
	e := newEnv(t, nil)
	e.round()
	e.k.setFail("a", "b", "c1", "c2")
	var g GroupRuntime
	for i := 0; i < 3; i++ {
		g = e.round()
	}
	if e.k.selected("G") != "reject" || g.Status != "reject" || g.CurrentLaneID != nil || g.LastSwitch == nil || g.LastSwitch.To.LaneID != nil || g.LastSwitch.To.Ref != "reject" {
		t.Fatalf("%s %+v", e.k.selected("G"), g)
	}
	// 多节点页签:c1(内核选中)和 c2 都测过了
	if got := e.lane(g, "L2").Nodes; got["c1"].OK == nil || *got["c1"].OK || got["c2"].OK == nil {
		t.Fatalf("%+v", got)
	}
	// b 恢复:从拒绝出站切回(优先级最高的可用页签)
	e.k.setFail("a", "c1", "c2")
	g = e.round()
	if e.k.selected("G") != "b" || !strings.Contains(g.LastSwitch.Reason, "拒绝出站切回") {
		t.Fatalf("%s %+v", e.k.selected("G"), g.LastSwitch)
	}
}

func TestMultiNodeLaneConfirmedFalseButLaneUp(t *testing.T) {
	e := newEnv(t, nil)
	e.k.setFail("c1") // 内核子组选中的 c1 不通,c2 通
	g := e.round()
	l2 := e.lane(g, "L2")
	if l2.Health != "up" || l2.Confirmed == nil || *l2.Confirmed {
		t.Fatalf("%+v", l2)
	}
	if got := e.k.calls(); strings.Join(got, ",") != "a,b,c1,c2" && strings.Join(got, ",") != "b,a,c1,c2" && !contains3(got, "c1", "c2") {
		t.Fatalf("先测 c1 再测 c2:%v", got)
	}
	if l2.Nodes["c1"].OK == nil || *l2.Nodes["c1"].OK || l2.Nodes["c2"].OK == nil || !*l2.Nodes["c2"].OK {
		t.Fatalf("%+v", l2.Nodes)
	}
}

func contains3(all []string, first, second string) bool {
	i, j := -1, -1
	for n, v := range all {
		if v == first {
			i = n
		}
		if v == second {
			j = n
		}
	}
	return i >= 0 && j > i
}

func TestOnlyBackupLaneInMultiNodeIsUsed(t *testing.T) {
	e := newEnv(t, nil)
	e.round()
	e.k.setFail("a", "b") // 只剩多节点页签可用
	for i := 0; i < 3; i++ {
		e.round()
	}
	if e.k.selected("G") != "sub2" {
		t.Fatalf("应切到 sub2:%s", e.k.selected("G"))
	}
}

func TestAlignWhenCurrentSelectionNotInPlan(t *testing.T) {
	e := newEnv(t, nil)
	e.k.sel["G"] = "zzz" // 比如重新部署后,当前选择不在计划里
	g := e.round()
	if e.k.selected("G") != "a" || !strings.Contains(g.LastSwitch.Reason, "不在计划里") {
		t.Fatalf("按优先级选 a:%s %+v", e.k.selected("G"), g.LastSwitch)
	}

	// 都不可用:先不动,连续 3 轮才切拒绝
	e2 := newEnv(t, nil)
	e2.k.sel["G"] = "zzz"
	e2.k.setFail("a", "b", "c1", "c2")
	e2.round()
	e2.round()
	if len(e2.k.selects) != 0 {
		t.Fatal("没确认前不动")
	}
	e2.round()
	if e2.k.selected("G") != "reject" {
		t.Fatalf("%s", e2.k.selected("G"))
	}
}

func TestForcedRecheckTestsEveryNode(t *testing.T) {
	e := newEnv(t, nil)
	e.round()
	e.k.resetCalls()
	if e.m.Recheck("nope") {
		t.Fatal("不存在的组应返回 false")
	}
	if !e.m.Recheck("g1") {
		t.Fatal("g1 存在")
	}
	snap := e.m.Snapshot()[0]
	if !snap.ManualRecheck || !snap.RecheckPending {
		t.Fatalf("%+v", snap)
	}
	g := e.round(0)
	calls := e.k.calls()
	if len(calls) != 4 { // a b c1 c2 全测
		t.Fatalf("重新检测应测全部节点:%v", calls)
	}
	if g.ManualRecheck || g.RecheckPending {
		t.Fatalf("做完应清掉标记:%+v", g)
	}
	if l2 := e.lane(g, "L2"); l2.Nodes["c1"].OK == nil || l2.Nodes["c2"].OK == nil {
		t.Fatalf("%+v", l2.Nodes)
	}
}

func TestKernelUnreachableSkipsRound(t *testing.T) {
	e := newEnv(t, nil)
	e.k.selErr = errors.New("connection refused")
	g := e.round(0)
	if g.Status != "unknown" || !strings.Contains(g.LastError, "内核") || len(e.k.calls()) != 0 || g.NextRoundAt == nil || *g.NextRoundAt-*g.LastRoundAt != 30000 {
		t.Fatalf("%+v calls=%v", g, e.k.calls())
	}
	e.k.selErr = nil
	e.clk.Add(30 * time.Second)
	g = e.round(0)
	if g.LastError != "" || g.Status != "ok" {
		t.Fatalf("%+v", g)
	}
}

func TestSelectFailureIsReportedAndRetried(t *testing.T) {
	e := newEnv(t, nil)
	e.round()
	e.k.setFail("a")
	e.k.selectErr = &singbox.KernelError{Status: 400, Message: "Selector update error: not found"}
	e.round()
	e.round()
	g := e.round()
	if g.LastSwitch != nil || !strings.Contains(g.LastError, "切换失败") || e.k.selected("G") != "a" {
		t.Fatalf("%+v", g)
	}
	e.k.selectErr = nil
	g = e.round() // 下一轮重试
	if e.k.selected("G") != "b" || g.LastSwitch == nil || g.LastError != "" {
		t.Fatalf("%s %+v", e.k.selected("G"), g)
	}
}

func TestHTTPTestURLIsUpgraded(t *testing.T) {
	for in, want := range map[string]string{
		"http://www.gstatic.com/generate_204": "https://www.gstatic.com/generate_204",
		"https://x.test/a?b=1":                "https://x.test/a?b=1",
		"":                                    DefaultTestURL,
		"  http://h.test/x ":                  "https://h.test/x",
	} {
		if got := httpsURL(in); got != want {
			t.Errorf("%q → %q,想要 %q", in, got, want)
		}
	}
	e := newEnv(t, func(p Plan) { p[0].Settings.TestURL = "http://y.test/204" })
	e.round(0)
	e.k.mu.Lock()
	defer e.k.mu.Unlock()
	for _, u := range e.k.urls {
		if u != "https://y.test/204" {
			t.Fatalf("检测地址:%s", u)
		}
	}
}

func TestFailureKinds(t *testing.T) {
	e := newEnv(t, nil)
	e.k.errFor["a"] = &singbox.KernelError{Status: 504, Message: "Timeout"}
	e.k.errFor["b"] = &singbox.KernelError{Status: 404, Message: "resource not found"}
	e.k.errFor["c1"] = errors.New(strings.Repeat("x", 500))
	g := e.round(0)
	a, b, c1 := e.lane(g, "L0").Nodes["a"], e.lane(g, "L1").Nodes["b"], e.lane(g, "L2").Nodes["c1"]
	if *a.Kind != KindTimeout || *a.Reason != KindTimeout || a.OK == nil || *a.OK || a.Delay != nil {
		t.Fatalf("%+v", a)
	}
	if *b.Kind != KindNotFound {
		t.Fatalf("%+v", b)
	}
	if *c1.Kind != KindError || len([]rune(*c1.Error)) > maxErrLen+1 {
		t.Fatalf("%+v", c1)
	}
}

func TestCooldownBlocksRestoreButNotFailure(t *testing.T) {
	e := newEnv(t, func(p Plan) { p[0].Settings.RecoveryHoldMs = 0 })
	e.round()
	e.k.setFail("a")
	e.round()
	e.round()
	e.round(0) // 第 3 轮切到 b,时钟不动:记 T 为切换时刻
	if e.k.selected("G") != "b" {
		t.Fatal("应在 b")
	}
	e.k.setFail() // a 马上恢复,恢复保持时间 0:只有冷却(检测间隔的一半 = 30 秒)拦着
	e.m.Refresh()
	e.round(10 * time.Second) // T+0
	e.m.Refresh()
	e.round(10 * time.Second) // T+10
	e.m.Refresh()
	e.round(0) // T+20
	if e.k.selected("G") != "b" {
		t.Fatal("冷却期内(20 秒)不该切回")
	}
	e.clk.Add(10 * time.Second) // T+30:冷却过了
	e.m.Refresh()
	e.round(0)
	if e.k.selected("G") != "a" {
		t.Fatalf("冷却过了应切回:%s", e.k.selected("G"))
	}
	// 失败确认引起的切换不受冷却限制:刚切回就连续 3 轮失败,5 秒一轮也照样走
	e.k.setFail("a")
	for i := 0; i < 3; i++ {
		e.m.Refresh()
		e.round(5 * time.Second)
	}
	if e.k.selected("G") != "b" {
		t.Fatalf("失败确认的切换不该被冷却拦住:%s", e.k.selected("G"))
	}
}

func TestThresholdDefaultsToOne(t *testing.T) {
	e := newEnv(t, func(p Plan) { p[0].Settings.FailureThreshold = 0 })
	e.round()
	e.k.setFail("a")
	e.round()
	if e.k.selected("G") != "b" {
		t.Fatalf("阈值 0 按 1 算:%s", e.k.selected("G"))
	}
}

func TestSetPlanKeepsUnchangedAndResetsChanged(t *testing.T) {
	e := newEnv(t, nil)
	e.k.setFail("a")
	e.round()
	if e.lane(e.m.Snapshot()[0], "L0").FailStreak != 1 {
		t.Fatal("准备")
	}
	e.m.SetPlan(testPlan()) // 完全一样:保留
	if l := e.lane(e.m.Snapshot()[0], "L0"); l.FailStreak != 1 {
		t.Fatalf("没变的组该保留运行数据:%+v", l)
	}
	p := testPlan()
	p[0].Settings.FailureThreshold = 5 // 变了:从头来
	e.m.SetPlan(p)
	g := e.m.Snapshot()[0]
	if l := e.lane(g, "L0"); l.FailStreak != 0 || l.Health != "unknown" || g.Status != "pending" || g.LastRoundAt != nil {
		t.Fatalf("变了的组该重置:%+v %+v", l, g)
	}
	e.m.SetPlan(Plan{})
	if e.m.Running() || len(e.m.Snapshot()) != 0 {
		t.Fatal("没有计划就没有组")
	}
}

func TestRefreshAndSetPlanInvalidateRoundInFlight(t *testing.T) {
	e := newEnv(t, nil)
	e.k.block = make(chan struct{})
	e.m.Step(context.Background())
	waitFor(t, func() bool { return len(e.k.calls()) >= 1 })
	if g := e.m.Snapshot()[0]; !g.InFlight || g.Round == nil || g.Round.StartedAt == 0 {
		t.Fatalf("%+v", g)
	}
	e.m.Refresh() // 作废进行中的一轮
	if g := e.m.Snapshot()[0]; g.InFlight {
		t.Fatal("作废后不该还显示在检测")
	}
	close(e.k.block)
	e.m.Wait()
	if g := e.m.Snapshot()[0]; g.LastRoundAt != nil || e.lane(g, "L0").Health != "unknown" {
		t.Fatalf("被作废的一轮不该写结果:%+v", g)
	}
	// 新的一轮正常做
	e.k.block = nil
	g := e.round(0)
	if g.LastRoundAt == nil || e.lane(g, "L0").Health != "up" {
		t.Fatalf("%+v", g)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	k := newKernel()
	clk := &clock{t: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)}
	m := NewManager(Options{Kernel: k, Now: clk.Now, Concurrency: 2})
	var members []string
	for i := 0; i < 10; i++ {
		members = append(members, "n"+itoa(i))
	}
	m.SetPlan(Plan{{ID: "g", Tag: "G", RejectTag: "reject", Settings: Settings{IntervalMs: 60000, TimeoutMs: 1000, FailureThreshold: 1},
		Lanes: []Lane{{ID: "L", Name: "x", Index: 0, Members: members, Valid: members, Mode: "urltest", Ref: sp("sub"), SubTag: sp("sub")}}}})
	k.sel = map[string]string{"G": "sub", "sub": "n0"}
	k.block = make(chan struct{})
	m.Recheck("g") // 全测
	m.Step(context.Background())
	waitFor(t, func() bool { k.mu.Lock(); defer k.mu.Unlock(); return k.inflight >= 2 })
	time.Sleep(50 * time.Millisecond)
	close(k.block)
	m.Wait()
	if k.maxFlight > 2 {
		t.Fatalf("并发上限是 2,实际同时有 %d 个", k.maxFlight)
	}
	if len(k.calls()) != 10 {
		t.Fatalf("%v", k.calls())
	}
}

func TestSkippedLanesAreDownAndNeverChosen(t *testing.T) {
	e := newEnv(t, func(p Plan) {
		p[0].Lanes = append(p[0].Lanes, Lane{ID: "L3", Name: "空", Index: 3, Members: []string{"x"}, Valid: []string{}, Mode: "empty"})
	})
	g := e.round(0)
	if l := e.lane(g, "L3"); l.Health != "down" || len(l.Nodes) != 0 {
		t.Fatalf("%+v", l)
	}
	for _, c := range e.k.calls() {
		if c == "x" {
			t.Fatal("没有有效节点的页签不测")
		}
	}
}

func TestDeadlineOnShutdownLeavesNoRoundInFlight(t *testing.T) {
	e := newEnv(t, nil)
	e.k.block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	e.m.Step(ctx)
	waitFor(t, func() bool { return len(e.k.calls()) >= 1 })
	cancel() // 整体退出
	e.m.Wait()
	if g := e.m.Snapshot()[0]; g.InFlight || g.Round != nil {
		t.Fatalf("退出后不该还显示在检测:%+v", g)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("等待超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
