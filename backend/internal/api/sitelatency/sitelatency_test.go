package sitelatency

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/latencyhistory"
	"github.com/timeflysoon/Open-Box/backend/internal/sitelatency"
)

type fake struct {
	run     *sitelatency.Run
	runErr  error
	hist    *sitelatency.History
	histErr error
	got     []sitelatency.Site
}

func (f *fake) History(context.Context) (*sitelatency.History, error) { return f.hist, f.histErr }
func (f *fake) Run(_ context.Context, s []sitelatency.Site) (*sitelatency.Run, error) {
	f.got = s
	return f.run, f.runErr
}

func do(t *testing.T, f *fake, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, Deps{Service: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestRunShape(t *testing.T) {
	via := "站点集"
	ms := 42
	e := "超时(5 秒内没有响应)"
	f := &fake{run: &sitelatency.Run{
		Sites: []sitelatency.Result{
			{ID: "a", URL: "https://a.test/", Via: &via, Chain: []string{"站点集", "节点"}, MS: &ms, OpenMS: &ms},
			{ID: "b", URL: "https://b.test/", Chain: []string{}, Error: &e},
		},
		TestedAt: 1, History: latencyhistory.History{"a": {{Time: "2026-10-28T08:00:00Z", Delay: 42, Node: "站点集 → 节点"}}}, TimeoutMS: 5000,
	}}
	rec := do(t, f, http.MethodPost, "/api/openbox/site-latency", `{"sites":[{"id":"a","url":"https://a.test/"},{"id":"b","url":"https://b.test/"}]}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	if len(f.got) != 2 || f.got[1].ID != "b" {
		t.Fatalf("%+v", f.got)
	}
	var raw struct {
		Sites     []map[string]any `json:"sites"`
		TestedAt  float64          `json:"testedAt"`
		History   map[string]any   `json:"history"`
		TimeoutMS float64          `json:"timeoutMs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.TimeoutMS != 5000 || raw.TestedAt != 1 || raw.History["a"] == nil {
		t.Fatalf("%s", rec.Body)
	}
	bad := raw.Sites[1]
	for _, k := range []string{"id", "url", "via", "chain", "ms", "openMs", "error"} {
		if _, ok := bad[k]; !ok {
			t.Errorf("缺字段 %s", k)
		}
	}
	if bad["via"] != nil || bad["ms"] != nil || bad["openMs"] != nil || bad["error"] != e {
		t.Errorf("失败的站点 via / ms / openMs 要是 null:%v", bad)
	}
	if c, ok := bad["chain"].([]any); !ok || len(c) != 0 {
		t.Errorf("chain 要是空数组:%v", bad["chain"])
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("要带 no-store")
	}
}

func TestRunErrors(t *testing.T) {
	cases := []struct {
		name string
		f    *fake
		body string
		code int
	}{
		{"请求体不是 JSON", &fake{}, `[`, 400},
		{"输入不合法", &fake{runErr: &sitelatency.InputError{Msg: "没有要测的站点"}}, `{"sites":[]}`, 400},
		{"别的错误", &fake{runErr: errors.New("磁盘满了")}, `{"sites":[{"id":"a","url":"https://a.test/"}]}`, 500},
	}
	for _, c := range cases {
		rec := do(t, c.f, http.MethodPost, "/api/openbox/site-latency", c.body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s:%d %s", c.name, rec.Code, rec.Body)
		}
	}
	if rec := do(t, &fake{}, http.MethodGet, "/api/openbox/site-latency", ""); rec.Code != 405 {
		t.Errorf("GET 应 405:%d", rec.Code)
	}
}

func TestHistory(t *testing.T) {
	f := &fake{hist: &sitelatency.History{History: latencyhistory.History{}, TimeoutMS: 5000}}
	rec := do(t, f, http.MethodGet, "/api/openbox/site-latency/history", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"history":{}`) || !strings.Contains(rec.Body.String(), `"timeoutMs":5000`) {
		t.Fatal(rec.Code, rec.Body)
	}
	f.histErr = errors.New("坏了")
	if rec := do(t, f, http.MethodGet, "/api/openbox/site-latency/history", ""); rec.Code != 500 {
		t.Fatal(rec.Code)
	}
	if rec := do(t, f, http.MethodPost, "/api/openbox/site-latency/history", "{}"); rec.Code != 405 {
		t.Fatal(rec.Code)
	}
}
