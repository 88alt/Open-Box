package dnsfilter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

type fakeProf struct{ p map[string]any }

func (f *fakeProf) Get(context.Context) (map[string]any, error) { return f.p, nil }

func (f *fakeProf) set(s Settings) { f.p = map[string]any{"dns": map[string]any{"filter": s.ToMap()}} }

type harness struct {
	t       *testing.T
	sv      *Service
	prof    *fakeProf
	bodies  map[string]string // url → 内容
	fetchE  map[string]error
	fetches []string
	written [][]Group
	pruned  int
	running bool
	runErr  error
	deployE error
	deploys int
	order   []string
}

func newHarness(t *testing.T, s Settings) *harness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, prof: &fakeProf{}, bodies: map[string]string{}, fetchE: map[string]error{}, running: true}
	h.prof.set(s)
	root := t.TempDir()
	h.sv = &Service{
		Profiles: h.prof, Store: st, CacheDir: filepath.Join(root, "cache"), RulesDir: filepath.Join(root, "rules"),
		Fetch: func(_ context.Context, u string) ([]byte, error) {
			h.fetches = append(h.fetches, u)
			if err := h.fetchE[u]; err != nil {
				return nil, err
			}
			b, ok := h.bodies[u]
			if !ok {
				return nil, errors.New("404")
			}
			return []byte(b), nil
		},
		Write: func(_ context.Context, _ string, g []Group) error {
			h.written = append(h.written, g)
			h.order = append(h.order, "write")
			return nil
		},
		Prune:   func(string, []Group) { h.pruned++; h.order = append(h.order, "prune") },
		Running: func(context.Context) (bool, error) { return h.running, h.runErr },
		Deploy: func(context.Context) error {
			h.deploys++
			h.order = append(h.order, "deploy")
			return h.deployE
		},
		Now: func() time.Time { return time.UnixMilli(1_700_000_000_000) },
	}
	return h
}

func twoLists() Settings {
	s := settingsWith(
		List{ID: "a", Name: "A", URL: "https://x/a", Enabled: true},
		List{ID: "b", Name: "B", URL: "https://x/b", Enabled: true},
	)
	return s
}

func (h *harness) apply(update bool, id string) (Status, error) {
	h.t.Helper()
	return h.sv.Apply(context.Background(), update, id)
}

func TestApplyHappyPath(t *testing.T) {
	h := newHarness(t, twoLists())
	h.bodies["https://x/a"] = "||a.test^\n"
	h.bodies["https://x/b"] = "||b.test^\n@@||ok.b.test^\n"
	st, err := h.apply(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Pending || st.Applied == nil || !st.Applied.Enabled || st.Applied.Key != SettingsKey(twoLists()) || st.Busy {
		t.Fatalf("应用后状态不对: %+v", st)
	}
	if st.Lists["a"].Count != 1 || st.Lists["b"].Count != 2 || st.Lists["a"].UpdatedAt != 1_700_000_000_000 || st.Lists["a"].Error != "" {
		t.Errorf("名单统计: %+v", st.Lists)
	}
	if !reflect.DeepEqual(h.order, []string{"write", "deploy", "prune"}) {
		t.Errorf("顺序应是 编译 → 部署 → 清理(清理不能早于部署成功): %v", h.order)
	}
	// 产物进了状态,core 能取到
	art, key, enabled, ready, err := h.sv.CoreArtifact(context.Background(), h.prof.p)
	if err != nil || !enabled || !ready || art["key"] != key || len(art["blocks"].([]any)) != 2 {
		t.Fatalf("CoreArtifact: %v %v %v %v %v", art, key, enabled, ready, err)
	}
	// 缓存原文落了盘,第二次不带 update 不再下载
	n := len(h.fetches)
	if _, err := h.apply(false, ""); err != nil {
		t.Fatal(err)
	}
	if len(h.fetches) != n {
		t.Errorf("没要求更新不该重新下载: %v", h.fetches)
	}
}

func TestApplyUpdateOnlyOneList(t *testing.T) {
	h := newHarness(t, twoLists())
	h.bodies["https://x/a"] = "||a.test^\n"
	h.bodies["https://x/b"] = "||b.test^\n"
	if _, err := h.apply(false, ""); err != nil {
		t.Fatal(err)
	}
	h.fetches = nil
	h.bodies["https://x/b"] = "||b.test^\n||b2.test^\n"
	st, err := h.apply(true, "b")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.fetches, []string{"https://x/b"}) || st.Lists["b"].Count != 2 || st.Lists["a"].Count != 1 {
		t.Fatalf("只该重下 b: %v %+v", h.fetches, st.Lists)
	}
	h.fetches = nil
	if _, err := h.apply(true, ""); err != nil || len(h.fetches) != 2 {
		t.Fatalf("update 不带 listId 应重下全部: %v %v", h.fetches, err)
	}
	var in *InputError
	if _, err := h.apply(true, "nope"); !errors.As(err, &in) {
		t.Errorf("不存在的 listId 应是 InputError: %v", err)
	}
}

func TestApplyFetchFailureKeepsCache(t *testing.T) {
	h := newHarness(t, twoLists())
	h.bodies["https://x/a"] = "||a.test^\n"
	h.bodies["https://x/b"] = "||b.test^\n"
	if _, err := h.apply(false, ""); err != nil {
		t.Fatal(err)
	}
	h.fetchE["https://x/b"] = errors.New("下载失败 502")
	st, err := h.apply(true, "")
	if err != nil {
		t.Fatalf("有缓存的名单下载失败不该让整体失败: %v", err)
	}
	if st.Lists["b"].Count != 1 || !strings.Contains(st.Lists["b"].Error, "沿用上次") {
		t.Errorf("b 应沿用缓存并记错误: %+v", st.Lists["b"])
	}
	if st.Lists["a"].Error != "" {
		t.Errorf("a 不该有错误: %+v", st.Lists["a"])
	}
}

func TestApplyBadDownloadDoesNotReplaceCache(t *testing.T) {
	h := newHarness(t, settingsWith(List{ID: "a", URL: "https://x/a", Enabled: true}))
	h.bodies["https://x/a"] = "||a.test^\n"
	if _, err := h.apply(false, ""); err != nil {
		t.Fatal(err)
	}
	h.bodies["https://x/a"] = "<html>not a list</html>\n" // 解析不出规则
	st, err := h.apply(true, "")
	if err != nil || st.Lists["a"].Count != 1 || st.Lists["a"].Error == "" {
		t.Fatalf("解析失败应沿用旧缓存: %+v %v", st.Lists["a"], err)
	}
	b, _ := os.ReadFile(h.sv.cachePath("a"))
	if string(b) != "||a.test^\n" {
		t.Errorf("缓存被坏内容覆盖了: %q", b)
	}
}

func TestApplyNoUsableList(t *testing.T) {
	h := newHarness(t, twoLists())
	st, err := h.apply(false, "") // 两份都下载不到(404),又没有缓存
	if !errors.Is(err, ErrNoUsableList) {
		t.Fatalf("应报 ErrNoUsableList: %v", err)
	}
	if h.deploys != 0 || len(h.written) != 0 {
		t.Error("没有可用名单不该写规则集、不该部署")
	}
	cur, _ := h.sv.Status(context.Background())
	if cur.Lists["a"].Error == "" || cur.Applied != nil || !cur.Pending {
		t.Errorf("失败原因应记在状态里,且仍是待应用: %+v %+v", cur, st)
	}
}

func TestApplyKernelNotRunning(t *testing.T) {
	h := newHarness(t, twoLists())
	h.bodies["https://x/a"] = "||a.test^\n"
	h.bodies["https://x/b"] = "||b.test^\n"
	h.running = false
	if _, err := h.apply(false, ""); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("内核没在运行应报 ErrNotRunning: %v", err)
	}
	if h.deploys != 0 || h.pruned != 0 {
		t.Error("内核没在运行不该部署、不该清理")
	}
	cur, _ := h.sv.Status(context.Background())
	if cur.Applied != nil || !cur.Pending || cur.Lists["a"].Count != 1 {
		t.Errorf("名单已更新但没应用: %+v", cur)
	}
	if _, _, _, ready, _ := h.sv.CoreArtifact(context.Background(), h.prof.p); !ready {
		t.Error("产物已保存,之后任何一次部署都能带上")
	}
}

func TestApplyCannotReadServiceTouchesNothing(t *testing.T) {
	h := newHarness(t, twoLists())
	h.bodies["https://x/a"] = "||a.test^\n"
	h.runErr = errors.New("init.d 没有")
	_, err := h.apply(false, "")
	var ae *ApplyError
	if !errors.As(err, &ae) || len(h.fetches) != 0 || len(h.written) != 0 || h.deploys != 0 {
		t.Fatalf("读不到服务状态应一点不动: %v fetch=%v", err, h.fetches)
	}
}

func TestApplyDeployFailureKeepsOldApplied(t *testing.T) {
	s := twoLists()
	h := newHarness(t, s)
	h.bodies["https://x/a"] = "||a.test^\n"
	h.bodies["https://x/b"] = "||b.test^\n"
	if _, err := h.apply(false, ""); err != nil {
		t.Fatal(err)
	}
	first, _ := h.sv.Status(context.Background())

	s.AllowDomains = []string{"keep.test"} // 改设置,key 变
	h.prof.set(s)
	h.pruned = 0
	h.deployE = &ApplyError{Msg: "健康检查没通过,已回滚"}
	_, err := h.apply(false, "")
	var ae *ApplyError
	if !errors.As(err, &ae) || ae.Msg != "健康检查没通过,已回滚" {
		t.Fatalf("部署失败应原样返回 ApplyError: %v", err)
	}
	cur, _ := h.sv.Status(context.Background())
	if !reflect.DeepEqual(cur.Applied, first.Applied) || !cur.Pending {
		t.Errorf("部署失败不该改「已应用」,且应仍是待应用: %+v", cur)
	}
	if h.pruned != 0 {
		t.Error("部署失败不能清理旧规则集文件(回滚后旧配置还要用)")
	}
}

func TestApplyDisableDeploysWithoutFilter(t *testing.T) {
	s := twoLists()
	h := newHarness(t, s)
	h.bodies["https://x/a"] = "||a.test^\n"
	h.bodies["https://x/b"] = "||b.test^\n"
	if _, err := h.apply(false, ""); err != nil {
		t.Fatal(err)
	}
	s.Enabled = false
	h.prof.set(s)
	cur, _ := h.sv.Status(context.Background())
	if !cur.Pending {
		t.Fatal("关掉过滤后应是待应用(要把规则撤下来)")
	}
	h.fetches = nil
	st, err := h.apply(false, "")
	if err != nil || len(h.fetches) != 0 {
		t.Fatalf("关闭时不该下载名单: %v %v", err, h.fetches)
	}
	if st.Pending || st.Applied == nil || st.Applied.Enabled {
		t.Errorf("关闭后的状态: %+v", st)
	}
	last := h.written[len(h.written)-1]
	if len(last) != 0 {
		t.Errorf("关闭时规则集应为空(旧文件由清理删掉): %v", last)
	}
}

func TestApplyBusy(t *testing.T) {
	h := newHarness(t, twoLists())
	h.bodies["https://x/a"] = "||a.test^\n"
	h.bodies["https://x/b"] = "||b.test^\n"
	gate, entered := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.sv.Deploy = func(context.Context) error {
		once.Do(func() { close(entered) })
		<-gate
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := h.apply(false, ""); done <- err }()
	<-entered
	if _, err := h.apply(false, ""); !errors.Is(err, ErrBusy) {
		t.Errorf("第二次应报 ErrBusy: %v", err)
	}
	if st, _ := h.sv.Status(context.Background()); !st.Busy {
		t.Error("应用期间 busy 应为真")
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st, _ := h.sv.Status(context.Background()); st.Busy {
		t.Error("应用完 busy 应为假")
	}
}

func TestPendingAndCoreArtifactReadiness(t *testing.T) {
	s := twoLists()
	h := newHarness(t, s)
	ctx := context.Background()
	if st, _ := h.sv.Status(ctx); !st.Pending || st.Applied != nil || st.Busy || st.Connected {
		t.Errorf("从没应用过且开着过滤应是待应用: %+v", st)
	}
	if _, _, enabled, ready, _ := h.sv.CoreArtifact(ctx, h.prof.p); !enabled || ready {
		t.Error("开着过滤但没产物:enabled 真、ready 假")
	}
	off := Default()
	h.prof.set(off)
	if st, _ := h.sv.Status(ctx); st.Pending {
		t.Error("关着且从没应用过不算待应用")
	}
	if _, _, enabled, ready, _ := h.sv.CoreArtifact(ctx, h.prof.p); enabled || !ready {
		t.Error("没开过滤:enabled 假")
	}
	// 改设置之后旧产物对不上 → 不 ready
	h.prof.set(s)
	h.bodies["https://x/a"] = "||a.test^\n"
	h.bodies["https://x/b"] = "||b.test^\n"
	if _, err := h.apply(false, ""); err != nil {
		t.Fatal(err)
	}
	s.Lists[0].URL = "https://x/a2"
	h.prof.set(s)
	if _, _, _, ready, _ := h.sv.CoreArtifact(ctx, h.prof.p); ready {
		t.Error("名单地址变了,旧产物不该再当作可用")
	}
	if st, _ := h.sv.Status(ctx); !st.Pending {
		t.Error("名单地址变了应是待应用")
	}
	if st, _ := h.sv.Status(ctx); st.Lists["a"].Count != 0 {
		t.Errorf("地址变了,旧地址的统计不该显示: %+v", st.Lists["a"])
	}
}

func TestStatusConnectedFollowsQueryLog(t *testing.T) {
	h := newHarness(t, Settings{Enabled: true})
	if st, err := h.sv.Status(context.Background()); err != nil || st.Connected {
		t.Fatalf("没接查询日志时 connected 应为假: %v %v", st.Connected, err)
	}
	on := false
	h.sv.Connected = func() bool { return on }
	if st, _ := h.sv.Status(context.Background()); st.Connected {
		t.Error("日志没在读时 connected 应为假")
	}
	on = true
	if st, _ := h.sv.Status(context.Background()); !st.Connected {
		t.Error("日志在读时 connected 应为真")
	}
}

func TestStatusQueryLogReason(t *testing.T) {
	h := newHarness(t, Settings{Enabled: true})
	ctx := context.Background()
	if st, err := h.sv.Status(ctx); err != nil || st.QueryLog != nil {
		t.Fatalf("没接时不该有 queryLog: %+v %v", st.QueryLog, err)
	}
	reason := ""
	h.sv.QueryLogReason = func() string { return reason }
	if st, _ := h.sv.Status(ctx); st.QueryLog != nil {
		t.Fatalf("没有原因不该有 queryLog: %+v", st.QueryLog)
	}
	for _, r := range []string{"user", "failed"} {
		reason = r
		st, _ := h.sv.Status(ctx)
		if st.QueryLog == nil || st.QueryLog.Reason != r || st.QueryLog.Syslog {
			t.Fatalf("reason=%s: %+v", r, st.QueryLog)
		}
		b, _ := json.Marshal(st)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		ql, _ := m["queryLog"].(map[string]any)
		if len(ql) != 1 || ql["reason"] != r {
			t.Fatalf("JSON 里的 queryLog 应只有 reason: %v", m["queryLog"])
		}
	}
	// 没有原因时整个字段不出现(前端把它当 undefined)
	reason = ""
	b, _ := json.Marshal(mustStatus(t, h))
	if strings.Contains(string(b), "queryLog") {
		t.Fatalf("不该带 queryLog: %s", b)
	}
}

func mustStatus(t *testing.T, h *harness) Status {
	t.Helper()
	st, err := h.sv.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}
