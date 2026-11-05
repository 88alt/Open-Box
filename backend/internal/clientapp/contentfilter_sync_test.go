package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/contentfilter"
)

// 同步明文里的内容过滤快照:导出文件和公开同步接口共用同一份明文,所以两条路都测。

func fakeSnapshot() contentfilter.Snapshot {
	return contentfilter.Snapshot{
		SHA256: "abc123", Size: 77,
		Sets: []contentfilter.SnapshotSet{
			{ID: "cf1", Name: "去广告", Dialect: "surge", RuleCount: 2, ScriptCount: 1, UpdatedAt: "2026-11-05T01:02:03Z", Text: "[Rule]\nDOMAIN,a.com,REJECT\n"},
			{ID: "cf2", Name: "重写", Dialect: "quantumultx", RuleCount: 1, UpdatedAt: "2026-11-05T02:00:00Z", Text: "[rewrite_local]\n^https://x url reject\n"},
		},
	}
}

// openPlain 解开封包并按「通用 JSON」读出,用来看键在不在。
func openPlain(t *testing.T, env Envelope, is Issue) map[string]json.RawMessage {
	t.Helper()
	plain, err := OpenEnvelope(env, is.PairCode, testIter, testIter)
	if err != nil {
		t.Fatalf("配对码应能解开: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(plain, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestExportCarriesContentFilterSnapshot(t *testing.T) {
	s := fileSvc(t)
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) { return fakeSnapshot(), nil }
	ctx := context.Background()
	b, err := s.ExportFile(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	is, _ := s.ClientConfig(ctx)
	plain, err := OpenEnvelope(b.Seal(), is.PairCode, testIter, testIter)
	if err != nil {
		t.Fatal(err)
	}
	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		t.Fatal(err)
	}
	if p.V != 1 || p.Kind != "client-config" || len(p.ShareRegions) != 3 {
		t.Fatalf("原有内容不该变: %+v", p)
	}
	cf := p.ContentFilter
	if cf == nil || cf.SHA256 != "abc123" || cf.Size != 77 || len(cf.Sets) != 2 {
		t.Fatalf("快照: %+v", cf)
	}
	if got := cf.Sets[0]; got.ID != "cf1" || got.Name != "去广告" || got.Dialect != "surge" || got.RuleCount != 2 ||
		got.ScriptCount != 1 || got.UpdatedAt != "2026-11-05T01:02:03Z" || got.Text != "[Rule]\nDOMAIN,a.com,REJECT\n" {
		t.Fatalf("第一份: %+v", got)
	}
	if cf.Sets[1].Dialect != "quantumultx" || cf.Sets[1].ScriptCount != 0 {
		t.Fatalf("第二份: %+v", cf.Sets[1])
	}
}

func TestSyncCarriesSameContentFilterSnapshot(t *testing.T) {
	s, is, _ := syncSvc(t)
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) { return fakeSnapshot(), nil }
	res, err := s.Sync(context.Background(), is.Nonce, "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	m := openPlain(t, res.Seal(), is)
	var cf contentfilter.Snapshot
	if err := json.Unmarshal(m["contentFilter"], &cf); err != nil || cf.SHA256 != "abc123" || len(cf.Sets) != 2 || cf.Sets[1].ID != "cf2" {
		t.Fatalf("公开同步的明文也要带快照: %v %+v", err, cf)
	}
}

func TestPayloadOmitsContentFilterWhenNotWired(t *testing.T) {
	s := fileSvc(t) // ContentFilter 为 nil:整个键不出现,旧行为不变
	b, err := s.ExportFile(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	is, _ := s.ClientConfig(context.Background())
	if _, ok := openPlain(t, b.Seal(), is)["contentFilter"]; ok {
		t.Fatal("没接内容过滤服务时不该有 contentFilter 键")
	}
}

func TestPayloadEmptySnapshotIsEmptyArrayNotNull(t *testing.T) {
	s := fileSvc(t)
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) {
		return contentfilter.Snapshot{SHA256: "e3b0", Size: 0, Sets: []contentfilter.SnapshotSet{}}, nil
	}
	b, err := s.ExportFile(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	is, _ := s.ClientConfig(context.Background())
	var raw struct {
		Sets json.RawMessage `json:"sets"`
	}
	if err := json.Unmarshal(openPlain(t, b.Seal(), is)["contentFilter"], &raw); err != nil || string(raw.Sets) != "[]" {
		t.Fatalf("没有启用的集合时 sets 应是 [],App 才能分清「路由器上一份都没有」和「旧路由器没这项」: %s %v", raw.Sets, err)
	}
}

func TestTooLargeReportedNotTruncatedAndSyncStaysUntouched(t *testing.T) {
	s, is, _ := syncSvc(t)
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) {
		return contentfilter.Snapshot{}, &contentfilter.TooLargeError{Size: 5 << 20, Max: 4 << 20}
	}
	ctx := context.Background()
	var tl *ContentFilterTooLargeError
	if _, err := s.ExportFile(ctx, "", ""); !errors.As(err, &tl) || tl.Size != 5<<20 || tl.Max != 4<<20 {
		t.Fatalf("导出应如实报太大: %v", err)
	}
	if msg := tl.Error(); msg == "" || !hasAll(msg, "5.0 MiB", "4.0 MiB", "停用或删除") {
		t.Fatalf("错误信息要让人知道多大、上限多少、怎么办: %q", msg)
	}
	// 公开同步:同样报错,不更新 lastSyncAt,也不算「猜 nonce 失败」(nonce 是对的)
	for i := 0; i < 15; i++ {
		if _, err := s.Sync(ctx, is.Nonce, "10.0.0.9"); !errors.As(err, &tl) {
			t.Fatalf("第 %d 次同步: %v", i, err)
		}
	}
	after, _ := s.ClientConfig(ctx)
	if after.LastSyncAt != 0 {
		t.Fatalf("没同步成功不该记 lastSyncAt: %d", after.LastSyncAt)
	}
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) { return fakeSnapshot(), nil }
	if _, err := s.Sync(ctx, is.Nonce, "10.0.0.9"); err != nil {
		t.Fatalf("太大不是 nonce 错,15 次之后这个 IP 不该被锁: %v", err)
	}
}

func TestOtherContentFilterErrorIsNotTooLarge(t *testing.T) {
	s := fileSvc(t)
	boom := errors.New("磁盘坏了")
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) { return contentfilter.Snapshot{}, boom }
	_, err := s.ExportFile(context.Background(), "", "")
	var tl *ContentFilterTooLargeError
	if err == nil || errors.As(err, &tl) || !errors.Is(err, boom) {
		t.Fatalf("别的错误原样带上、不当成太大: %v", err)
	}
}

// 与真实的 contentfilter.Service 联调:同一个状态库、真实文本文件、真实摘要。
func TestWithRealContentFilterService(t *testing.T) {
	s := fileSvc(t)
	cf := &contentfilter.Service{Store: s.Store, Dir: filepath.Join(t.TempDir(), "cf")}
	text := "[Rule]\nDOMAIN,ad.example.com,REJECT\n"
	l, err := cf.Save(context.Background(), contentfilter.SaveInput{Name: "广告", Dialect: "surge", Text: &text})
	if err != nil {
		t.Fatal(err)
	}
	s.ContentFilter = func(ctx context.Context) (contentfilter.Snapshot, error) { return cf.Snapshot(ctx, 0) }
	ctx := context.Background()
	b, err := s.ExportFile(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	is, _ := s.ClientConfig(ctx)
	var got contentfilter.Snapshot
	if err := json.Unmarshal(openPlain(t, b.Seal(), is)["contentFilter"], &got); err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != l.SHA256 || got.Size != l.Size || len(got.Sets) != 1 || got.Sets[0].Text != text || got.Sets[0].Name != "广告" {
		t.Fatalf("快照应与 GET /content-filter 的摘要一致: %+v vs %+v", got, l)
	}
	// 停用后再导出:快照里没有,摘要跟着变
	if _, err := cf.SetEnabled(ctx, l.Sets[0].ID, false); err != nil {
		t.Fatal(err)
	}
	b2, _ := s.ExportFile(ctx, "", "")
	var got2 contentfilter.Snapshot
	if err := json.Unmarshal(openPlain(t, b2.Seal(), is)["contentFilter"], &got2); err != nil || len(got2.Sets) != 0 || got2.SHA256 == got.SHA256 {
		t.Fatalf("停用后快照应为空且摘要变了: %+v %v", got2, err)
	}
	// 真实服务的上限:用很小的上限走一遍 TooLargeError 的转换
	s.ContentFilter = func(ctx context.Context) (contentfilter.Snapshot, error) { return cf.Snapshot(ctx, 1) }
	if _, err := cf.SetEnabled(ctx, l.Sets[0].ID, true); err != nil {
		t.Fatal(err)
	}
	var tl *ContentFilterTooLargeError
	if _, err := s.ExportFile(ctx, "", ""); !errors.As(err, &tl) || tl.Max != 1 {
		t.Fatalf("真实服务超限应转成 *ContentFilterTooLargeError: %v", err)
	}
}

func TestSealConcurrencyIsBounded(t *testing.T) {
	s := fileSvc(t)
	var cur, peak atomic.Int32
	gate := make(chan struct{})
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-gate
		cur.Add(-1)
		return fakeSnapshot(), nil
	}
	ctx := context.Background()
	if _, err := s.ClientConfig(ctx); err != nil { // 先补齐配对码,免得并发里同时初始化
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ExportFile(ctx, "", ""); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(200 * time.Millisecond) // 让能进的都进来、卡在 gate 上
	if got := cur.Load(); got != maxConcurrentSeals {
		t.Fatalf("同时在生成的应正好是上限 %d,现在是 %d", maxConcurrentSeals, got)
	}
	close(gate)
	wg.Wait()
	if peak.Load() > maxConcurrentSeals {
		t.Fatalf("峰值 %d 超过上限 %d", peak.Load(), maxConcurrentSeals)
	}
}

func TestAcquireSealWaitsAndCancels(t *testing.T) {
	s := &Service{}
	var rel []func()
	for i := 0; i < maxConcurrentSeals; i++ {
		r, err := s.acquireSeal(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		rel = append(rel, r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.acquireSeal(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("满了又等不到应随 ctx 取消: %v", err)
	}
	rel[0]() // 放掉一个,新的就能进
	r, err := s.acquireSeal(context.Background())
	if err != nil {
		t.Fatalf("放掉一个后应能进: %v", err)
	}
	r()
	rel[1]()
}

func hasAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
