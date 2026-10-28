package latencyhistory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lh "github.com/timeflysoon/Open-Box/backend/internal/latencyhistory"
)

type fake struct {
	hist   lh.History
	up     int64
	err    error
	added  []lh.Input
	synced int
}

func (f *fake) Get(context.Context) (lh.History, int64, error) { return f.hist, f.up, f.err }
func (f *fake) Add(_ context.Context, in []lh.Input) (lh.History, error) {
	f.added = in
	return f.hist, f.err
}
func (f *fake) Sync(context.Context) (lh.History, error) { f.synced++; return f.hist, f.err }

func do(f *fake, method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, Deps{Service: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestGetAndVersionShapes(t *testing.T) {
	f := &fake{hist: lh.History{"HK": {{Time: "2026-10-28T10:00:00Z", Delay: 120}}, "自动选择": {{Time: "2026-10-28T10:00:01Z", Delay: 130, Node: "HK"}}}, up: 42}
	var got struct {
		History   map[string][]map[string]any `json:"history"`
		UpdatedAt int64                       `json:"updatedAt"`
	}
	rec := do(f, http.MethodGet, "/api/openbox/latency-history", "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.UpdatedAt != 42 || got.History["自动选择"][0]["node"] != "HK" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, has := got.History["HK"][0]["node"]; has {
		t.Error("节点自己的样本不带 node")
	}
	if rec := do(f, http.MethodGet, "/api/openbox/latency-history/version", ""); rec.Body.String() != `{"updatedAt":42}`+"\n" && rec.Body.String() != `{"updatedAt":42}` {
		t.Errorf("version: %s", rec.Body)
	}
	// 从没记过:history 是 {} 而不是 null
	empty := do(&fake{}, http.MethodGet, "/api/openbox/latency-history", "")
	if !strings.Contains(empty.Body.String(), `"history":{}`) || !strings.Contains(empty.Body.String(), `"updatedAt":0`) {
		t.Errorf("空状态: %s", empty.Body)
	}
}

func TestSamplesAndSync(t *testing.T) {
	f := &fake{hist: lh.History{"HK": {{Time: "2026-10-28T10:00:00Z", Delay: 0}}}}
	rec := do(f, http.MethodPost, "/api/openbox/latency-history/samples", `{"samples":[{"name":"HK","time":"2026-10-28T10:00:00Z","delay":0}]}`)
	if rec.Code != 200 || len(f.added) != 1 || f.added[0].Name != "HK" || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, f.added)
	}
	if rec := do(f, http.MethodPost, "/api/openbox/latency-history/sync", `{}`); rec.Code != 200 || f.synced != 1 || !strings.Contains(rec.Body.String(), `"history"`) {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body)
	}
}

func TestErrorMapping(t *testing.T) {
	if rec := do(&fake{err: &lh.InputError{Msg: "第 1 条的名字为空或太长"}}, http.MethodPost, "/api/openbox/latency-history/samples", `{"samples":[{}]}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "第 1 条") {
		t.Errorf("InputError 应 400: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct{ m, p, b string }{
		{http.MethodGet, "/api/openbox/latency-history", ""},
		{http.MethodGet, "/api/openbox/latency-history/version", ""},
		{http.MethodPost, "/api/openbox/latency-history/samples", `{"samples":[]}`},
		{http.MethodPost, "/api/openbox/latency-history/sync", `{}`},
	} {
		if rec := do(&fake{err: errors.New("磁盘坏了")}, c.m, c.p, c.b); rec.Code != 500 {
			t.Errorf("%s %s 内部错误应 500: %d", c.m, c.p, rec.Code)
		}
	}
	f := &fake{}
	if rec := do(f, http.MethodPost, "/api/openbox/latency-history/samples", `{bad`); rec.Code != 400 || f.added != nil {
		t.Errorf("坏 JSON 应 400 且不记: %d", rec.Code)
	}
	if rec := do(f, http.MethodPost, "/api/openbox/latency-history/samples", `{"samples":[],"x":"`+strings.Repeat("a", 300<<10)+`"}`); rec.Code != 413 {
		t.Errorf("过大应 413: %d", rec.Code)
	}
	for _, c := range []struct{ m, p string }{
		{http.MethodPost, "/api/openbox/latency-history"}, {http.MethodGet, "/api/openbox/latency-history/samples"},
		{http.MethodGet, "/api/openbox/latency-history/sync"}, {http.MethodPost, "/api/openbox/latency-history/version"},
	} {
		if rec := do(f, c.m, c.p, ""); rec.Code != 405 {
			t.Errorf("%s %s 应 405: %d", c.m, c.p, rec.Code)
		}
	}
}
