package subscriptions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

// newUpdatesEnv 和 newEnv 一样,只是长轮询最多挂 wait。
func newUpdatesEnv(t *testing.T, wait time.Duration) *env {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pages: map[string]string{}}
	e.svc = subs.New(st, func(_ context.Context, u, _ string) (*sub.Fetched, error) {
		p, ok := e.pages[u]
		if !ok {
			return nil, &sub.FetchError{Msg: "HTTP 404"}
		}
		return &sub.Fetched{Body: []byte(p)}, nil
	})
	mux := http.NewServeMux()
	Register(mux, Deps{Subs: e.svc, UpdatesWait: wait})
	e.h = mux
	return e
}

type rev struct{ Revision, NodesRevision string }

func (e *env) poll(t *testing.T, since string) (int, rev) {
	t.Helper()
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/openbox/subscriptions/updates?since="+since, nil))
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("updates 必须 no-store: %v", rec.Header())
	}
	var r rev
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatalf("响应不是 JSON: %v %s", err, rec.Body.String())
		}
		if len(r.Revision) < 8 || len(r.NodesRevision) < 8 {
			t.Fatalf("revision / nodesRevision 必须是非空字符串: %s", rec.Body.String())
		}
	}
	return rec.Code, r
}

func TestUpdatesImmediate(t *testing.T) {
	e := newUpdatesEnv(t, 5*time.Second)
	start := time.Now()
	code, r0 := e.poll(t, "") // 没带版本:立刻回
	if code != 200 || time.Since(start) > time.Second {
		t.Fatalf("since 为空该立刻回: %d %v", code, time.Since(start))
	}
	if _, r := e.poll(t, "old-revision"); r != r0 { // 版本不同:立刻回当前的
		t.Errorf("同样的数据版本该一样: %v %v", r, r0)
	}
	// 路由没被 {id} 接走(接走会回 405 / 404)
	if code, _ := e.do("GET", "/api/openbox/subscriptions/updates?since=", nil); code != 200 {
		t.Errorf("updates 被别的路由接走了: %d", code)
	}
	if code, _ := e.do("POST", "/api/openbox/subscriptions/updates", nil); code != http.StatusMethodNotAllowed {
		t.Errorf("POST 该 405: %d", code)
	}
}

func TestUpdatesTimeoutKeepsRevision(t *testing.T) {
	e := newUpdatesEnv(t, 150*time.Millisecond)
	_, r0 := e.poll(t, "")
	start := time.Now()
	_, r1 := e.poll(t, r0.Revision)
	if d := time.Since(start); d < 100*time.Millisecond || d > 2*time.Second {
		t.Errorf("没变化应挂到超时(≈150ms),实际 %v", d)
	}
	if r1 != r0 {
		t.Errorf("没变化 revision 原样返回: %v %v", r1, r0)
	}
}

func TestUpdatesWakesOnChange(t *testing.T) {
	e := newUpdatesEnv(t, 10*time.Second)
	e.pages["https://a.example/sub"] = links("HK-1", "US-1")
	_, r0 := e.poll(t, "")

	type res struct {
		r rev
		d time.Duration
	}
	out := make(chan res, 1)
	start := time.Now()
	go func() {
		_, r := e.poll(t, r0.Revision)
		out <- res{r, time.Since(start)}
	}()
	time.Sleep(100 * time.Millisecond) // 让它先挂住
	if code, m := e.do("POST", "/api/openbox/subscriptions", map[string]any{"name": "A", "url": "https://a.example/sub"}); code != 200 {
		t.Fatal(code, m)
	}
	select {
	case got := <-out:
		if got.d > 3*time.Second {
			t.Errorf("订阅一变就该返回,实际等了 %v", got.d)
		}
		if got.r.Revision == r0.Revision || got.r.NodesRevision == r0.NodesRevision {
			t.Errorf("新增了订阅和节点,两个版本都该变: %v → %v", r0, got.r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("订阅变了长轮询没醒")
	}
}

// 刷新到同样的节点:节点版本不变(面板就不重拉节点),订阅元数据变了(更新时间)revision 才变。
func TestUpdatesNodesRevisionOnlyFollowsNodes(t *testing.T) {
	e := newUpdatesEnv(t, time.Second)
	e.pages["https://a.example/sub"] = links("HK-1")
	_, m := e.do("POST", "/api/openbox/subscriptions", map[string]any{"name": "A", "url": "https://a.example/sub"})
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatal(m)
	}
	_, r0 := e.poll(t, "")

	// 改名只动订阅元数据:revision 变,nodesRevision 不变
	if code, m := e.do("PATCH", "/api/openbox/subscriptions/"+id, map[string]any{"name": "A2"}); code != 200 {
		t.Fatal(code, m)
	}
	_, r1 := e.poll(t, "x")
	if r1.Revision == r0.Revision || r1.NodesRevision != r0.NodesRevision {
		t.Errorf("改名: %v → %v", r0, r1)
	}
	// 节点内容变了:nodesRevision 变
	e.pages["https://a.example/sub"] = links("HK-1", "JP-1")
	if code, m := e.do("POST", "/api/openbox/subscriptions/"+id+"/refresh", map[string]any{}); code != 200 {
		t.Fatal(code, m)
	}
	_, r2 := e.poll(t, "x")
	if r2.NodesRevision == r1.NodesRevision {
		t.Errorf("节点变了 nodesRevision 该变: %v → %v", r1, r2)
	}
	// 删除也要唤醒并变版本
	if code, _ := e.do("DELETE", "/api/openbox/subscriptions/"+id, nil); code != 200 {
		t.Fatal(code)
	}
	_, r3 := e.poll(t, "x")
	if r3.Revision == r2.Revision || r3.NodesRevision == r2.NodesRevision {
		t.Errorf("删除: %v → %v", r2, r3)
	}
	// 同一份数据重复读,版本稳定
	if _, r4 := e.poll(t, "x"); r4 != r3 {
		t.Errorf("没变化版本该稳定: %v %v", r3, r4)
	}
}

func TestUpdatesClientGoneReturns(t *testing.T) {
	e := newUpdatesEnv(t, 30*time.Second)
	_, r0 := e.poll(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/openbox/subscriptions/updates?since="+r0.Revision, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { e.h.ServeHTTP(rec, req); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("客户端断开后处理函数没退出")
	}
	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Errorf("断开后不该再写响应: %q", rec.Body.String())
	}
}
