package policies

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

type fakeProfiles struct{}

func (fakeProfiles) Get(context.Context) (map[string]any, error) {
	return map[string]any{"routing": map[string]any{}}, nil
}

type fakeCore struct {
	rt  policyentries.Routing
	err error
}

func (f fakeCore) NormalizeRouting(context.Context, map[string]any) (policyentries.Routing, error) {
	return f.rt, f.err
}

type noLists struct{}

func (noLists) Load(context.Context, string) (*rulelist.Result, error) {
	return nil, errors.New("不会被调用")
}

func server(c policyentries.Core) *http.ServeMux {
	mux := http.NewServeMux()
	Register(mux, Deps{Entries: policyentries.New(fakeProfiles{}, c, noLists{})})
	return mux
}

func okRouting() policyentries.Routing {
	var rt policyentries.Routing
	rt.Custom.Name = "前置自定义分流"
	rt.Fallback.Name = "其他"
	rt.Policies = []policyentries.Policy{{
		Name: "p", Domain: []string{"A.com", "b.com"}, IPCidr: []string{"10.0.0.0/8"}, Rulesets: []string{"geosite-cn"},
	}}
	return rt
}

func get(t *testing.T, h http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/policies/entries?"+query, nil))
	return rec
}

func TestEntriesOK(t *testing.T) {
	h := server(fakeCore{rt: okRouting()})
	q := url.Values{"name": {"p"}, "tab": {"domain"}, "q": {"a.com"}, "sort": {"content"}, "dir": {"desc"}, "limit": {"1"}}
	rec := get(t, h, q.Encode())
	if rec.Code != 200 {
		t.Fatalf("状态 %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("应带 no-store")
	}
	var got struct {
		Name     string `json:"name"`
		Fallback bool   `json:"fallback"`
		Counts   struct{ All, Domain, IP int }
		Total    int
		Matched  int
		Offset   int
		Limit    int
		HasMore  bool
		Entries  []struct{ Type, Family, Content, Source string }
		Missing  []string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "p" || got.Fallback || got.Counts.All != 3 || got.Counts.Domain != 2 || got.Counts.IP != 1 ||
		got.Total != 2 || got.Matched != 1 || got.Limit != 1 || got.HasMore ||
		len(got.Entries) != 1 || got.Entries[0].Content != "A.com" || got.Entries[0].Source != "custom" ||
		len(got.Missing) != 1 || got.Missing[0] != "geosite-cn" {
		t.Fatalf("响应不符: %s", rec.Body)
	}
	// 前端读这些键名,不能变
	for _, k := range []string{`"name"`, `"fallback"`, `"counts"`, `"total"`, `"matched"`, `"offset"`, `"limit"`, `"hasMore"`, `"entries"`, `"missing"`,
		`"type"`, `"family"`, `"content"`, `"source"`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Errorf("响应缺键 %s", k)
		}
	}
}

func TestEntriesEmptyArraysNotNull(t *testing.T) {
	rec := get(t, server(fakeCore{rt: okRouting()}), "name="+url.QueryEscape("其他"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"entries":[]`) || !strings.Contains(rec.Body.String(), `"missing":[]`) {
		t.Fatalf("兜底应返回空数组: %d %s", rec.Code, rec.Body)
	}
}

func TestEntriesErrors(t *testing.T) {
	ok := server(fakeCore{rt: okRouting()})
	for _, tc := range []struct {
		name  string
		h     http.Handler
		query string
		want  int
	}{
		{"缺 name", ok, "", 400},
		{"tab 非法", ok, "name=p&tab=x", 400},
		{"sort 非法", ok, "name=p&sort=x", 400},
		{"dir 非法", ok, "name=p&dir=sideways", 400},
		{"站点集不存在", ok, "name=nope", 404},
		{"无核心", server(nil), "name=p", 503},
		{"核心报错", server(fakeCore{err: &core.CoreError{Message: "bad"}}), "name=p", 400},
		{"其它错误", server(fakeCore{err: errors.New("boom")}), "name=p", 500},
	} {
		rec := get(t, tc.h, tc.query)
		if rec.Code != tc.want {
			t.Errorf("%s: 状态 %d,想要 %d(%s)", tc.name, rec.Code, tc.want, rec.Body)
			continue
		}
		var e struct{ Error string }
		if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error == "" {
			t.Errorf("%s: 错误应是 {error:\"...\"}: %s", tc.name, rec.Body)
		}
	}
}

func TestLimitClampingAndMethod(t *testing.T) {
	h := server(fakeCore{rt: okRouting()})
	var res struct{ Limit int }
	for q, want := range map[string]int{"name=p&limit=99999": maxLimit, "name=p&limit=0": 1, "name=p&limit=abc": defaultLimit, "name=p": defaultLimit} {
		rec := get(t, h, q)
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Limit != want {
			t.Errorf("%s: limit=%d,想要 %d", q, res.Limit, want)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/policies/entries?name=p", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 应返回 405,得到 %d", rec.Code)
	}
}
