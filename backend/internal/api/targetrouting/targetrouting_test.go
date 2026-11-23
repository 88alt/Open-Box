package targetrouting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/targetrouting"
)

type fakeSrc struct{ err error }

func (f fakeSrc) Routing(context.Context) (policyentries.Routing, error) {
	return policyentries.Routing{Policies: []policyentries.Policy{
		{ID: "a", Name: "流媒体", Default: "美国", Enabled: true},
		{ID: "b", Name: "国内", Default: "direct", Enabled: true},
	}}, f.err
}

func (fakeSrc) ExpandPolicy(_ context.Context, p policyentries.Policy) ([]policyentries.Entry, []string, error) {
	if p.ID == "a" {
		return []policyentries.Entry{{Type: "domain_suffix", Content: "netflix.com", Source: policyentries.SourceCustom}}, nil, nil
	}
	return nil, nil, nil
}

type memStore struct {
	list []any
	err  error
}

func (m *memStore) UpdatePolicies(_ context.Context, change func([]any) ([]any, error)) ([]any, error) {
	if m.err != nil {
		return nil, m.err
	}
	next, err := change(profile.Clone(m.list).([]any))
	if err != nil {
		return nil, err
	}
	m.list = next
	return next, nil
}

func setup(t *testing.T, src fakeSrc, ms *memStore, pending PendingFunc) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, Deps{Service: targetrouting.New(src, ms), Pending: pending})
	return mux
}

func call(mux http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON: %v\n%s", err, rec.Body.String())
	}
	return m
}

func lst() []any {
	return []any{
		map[string]any{"id": "a", "name": "流媒体", "domainSuffix": []any{"netflix.com"}},
		map[string]any{"id": "b", "name": "国内"},
	}
}

func q(kv ...string) string {
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return "/api/openbox/profile/target-routing?" + v.Encode()
}

func TestGetLookupShape(t *testing.T) {
	mux := setup(t, fakeSrc{}, &memStore{list: lst()}, nil)
	rec := call(mux, http.MethodGet, q("target", "www.netflix.com", "type", "domain", "original", "www.netflix.com"), "")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("状态 %d / %v", rec.Code, rec.Header())
	}
	m := decode(t, rec)
	if _, has := m["first"]; has {
		t.Errorf("普通查询不该带 first")
	}
	if len(m["policies"].([]any)) != 2 {
		t.Errorf("policies = %v", m["policies"])
	}
	match := m["matches"].([]any)[0].(map[string]any)
	if match["source"] != "policy" || match["policyId"] != "a" || match["type"] != "domain_suffix" ||
		match["value"] != "netflix.com" || match["outbound"] != "美国" || match["removable"] != true {
		t.Errorf("match = %v", match)
	}
	add := m["addition"].(map[string]any)
	if add["type"] != "domain" || add["value"] != "www.netflix.com" {
		t.Errorf("addition = %v", add)
	}
	// 没命中:matches 是 [] 不是 null
	m = decode(t, call(mux, http.MethodGet, q("target", "none.example", "type", "domain"), ""))
	if ms, ok := m["matches"].([]any); !ok || len(ms) != 0 {
		t.Errorf("matches = %#v", m["matches"])
	}
}

func TestGetFirst(t *testing.T) {
	mux := setup(t, fakeSrc{}, &memStore{list: lst()}, nil)
	m := decode(t, call(mux, http.MethodGet, q("target", "a.netflix.com", "first", "1"), ""))
	f, ok := m["first"].(map[string]any)
	if !ok || f["policyId"] != "a" {
		t.Errorf("first = %#v", m["first"])
	}
	if _, has := m["addition"]; has {
		t.Errorf("first 模式不该带 addition")
	}
	m = decode(t, call(mux, http.MethodGet, q("target", "none.example", "first", "1"), ""))
	if v, has := m["first"]; !has || v != nil {
		t.Errorf("没命中 first 应是 null(键在): %#v has=%v", v, has)
	}
}

func TestGetErrors(t *testing.T) {
	mux := setup(t, fakeSrc{}, &memStore{list: lst()}, nil)
	for _, target := range []string{q("target", "not valid", "type", "domain"), q("target", "", "type", "domain"), q("target", "a.com", "type", "weird")} {
		rec := call(mux, http.MethodGet, target, "")
		if rec.Code != 400 || decode(t, rec)["error"] == "" {
			t.Errorf("%s → %d %s", target, rec.Code, rec.Body.String())
		}
	}
	mux = setup(t, fakeSrc{err: targetrouting.ErrCoreUnavailable}, &memStore{}, nil)
	if rec := call(mux, http.MethodGet, q("target", "a.com", "type", "domain"), ""); rec.Code != 503 {
		t.Errorf("核心不可用应 503,得到 %d", rec.Code)
	}
	if rec := call(mux, http.MethodPut, q("target", "a.com"), "{}"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT 应 405,得到 %d", rec.Code)
	}
}

func TestPostApplied(t *testing.T) {
	ms := &memStore{list: lst()}
	pending := false
	mux := setup(t, fakeSrc{}, ms, func(context.Context) (bool, []string) {
		if pending {
			return true, []string{"分流"}
		}
		return false, nil
	})
	body := `{"target":"api.example.com","type":"domain","policyIds":["b"]}`

	// 内核没在跑(没有待重启):已应用
	m := decode(t, call(mux, http.MethodPost, "/api/openbox/profile/target-routing", body))
	ap := m["applied"].(map[string]any)
	if ap["ok"] != true || ap["skipped"] != nil || ap["pendingRestart"].(map[string]any)["pending"] != false {
		t.Errorf("applied = %v", ap)
	}
	if rs := ap["pendingRestart"].(map[string]any)["reasons"]; rs == nil {
		t.Errorf("reasons 不能是 null")
	}
	// 重复添加:unchanged
	m = decode(t, call(mux, http.MethodPost, "/api/openbox/profile/target-routing", body))
	if m["applied"].(map[string]any)["skipped"] != "unchanged" {
		t.Errorf("重复添加 applied = %v", m["applied"])
	}
	// 内核在跑且配置待重启:ok=false + pending
	pending = true
	m = decode(t, call(mux, http.MethodPost, "/api/openbox/profile/target-routing", `{"target":"x.example.com","type":"domain","policyIds":["b"]}`))
	ap = m["applied"].(map[string]any)
	pr := ap["pendingRestart"].(map[string]any)
	if ap["ok"] != false || pr["pending"] != true || pr["reasons"].([]any)[0] != "分流" {
		t.Errorf("applied = %v", ap)
	}
}

func TestPostErrors(t *testing.T) {
	ms := &memStore{list: lst()}
	mux := setup(t, fakeSrc{}, ms, nil)
	for name, body := range map[string]string{
		"不是 JSON": `nope`,
		"没选分流组":   `{"target":"a.com","type":"domain","policyIds":[]}`,
		"分流组不存在":  `{"target":"a.com","type":"domain","policyIds":["zzz"]}`,
		"目标不合法":   `{"target":"a b","type":"domain","policyIds":["a"]}`,
		"类型不认识":   `{"target":"a.com","type":"x","policyIds":["a"]}`,
	} {
		rec := call(mux, http.MethodPost, "/api/openbox/profile/target-routing", body)
		// 注意:不能回 404,前端把 404 当成「后端不支持此操作」
		if rec.Code != 400 {
			t.Errorf("%s: 状态 %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// 档案校验没过 → 400 带原因;存储坏了 → 500
	ms.err = &profile.ValidationError{Msg: "站点集太多"}
	if rec := call(mux, http.MethodPost, "/api/openbox/profile/target-routing", `{"target":"a.com","type":"domain","policyIds":["a"]}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "站点集太多") {
		t.Errorf("校验错误 → %d %s", rec.Code, rec.Body.String())
	}
	ms.err = context.DeadlineExceeded
	if rec := call(mux, http.MethodPost, "/api/openbox/profile/target-routing", `{"target":"a.com","type":"domain","policyIds":["a"]}`); rec.Code != 500 {
		t.Errorf("存储错误 → %d", rec.Code)
	}
}

func TestDelete(t *testing.T) {
	ms := &memStore{list: lst()}
	mux := setup(t, fakeSrc{}, ms, nil)
	// 前端把整条 match 原样发来(多出来的字段忽略)
	body := `{"source":"policy","name":"流媒体","policyId":"a","type":"domain_suffix","value":"netflix.com","outbound":"美国","removable":true}`
	m := decode(t, call(mux, http.MethodDelete, "/api/openbox/profile/target-routing", body))
	if m["applied"].(map[string]any)["ok"] != true {
		t.Errorf("applied = %v", m["applied"])
	}
	if got := ms.list[0].(map[string]any)["domainSuffix"].([]any); len(got) != 0 {
		t.Errorf("没删掉: %v", got)
	}
	m = decode(t, call(mux, http.MethodDelete, "/api/openbox/profile/target-routing", body))
	if m["applied"].(map[string]any)["skipped"] != "unchanged" {
		t.Errorf("再删应 unchanged: %v", m["applied"])
	}
	rec := call(mux, http.MethodDelete, "/api/openbox/profile/target-routing", `{"source":"ruleset","policyId":"a","type":"domain_suffix","value":"x.com"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "规则集") {
		t.Errorf("规则集条目不能删: %d %s", rec.Code, rec.Body.String())
	}
}
