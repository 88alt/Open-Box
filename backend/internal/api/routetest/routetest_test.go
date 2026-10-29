package routetest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/routetest"
)

type fake struct {
	res *routetest.Result
	err error
	got routetest.Input
	ctx context.Context
}

func (f *fake) Run(ctx context.Context, in routetest.Input) (*routetest.Result, error) {
	f.got, f.ctx = in, ctx
	return f.res, f.err
}

func do(t *testing.T, f *fake, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, Deps{Service: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/route-test", strings.NewReader(body)))
	return rec
}

func TestRunShapeAndInput(t *testing.T) {
	st, ms := 200, 120
	via := true
	f := &fake{res: &routetest.Result{
		Target: "example.com",
		DNS:    map[string]any{"error": "x"},
		Exit: routetest.Exit{URL: "https://example.com/", OK: true, Status: &st, MS: &ms, Method: "GET",
			Chains: []string{"海外", "hk-01"}, Rule: "rule_set=x", ViaProxy: &via},
	}}
	rec := do(t, f, http.MethodPost, `{"target":"example.com","port":8443,"method":"HEAD"}`)
	if rec.Code != 200 {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body)
	}
	if f.got != (routetest.Input{Target: "example.com", Port: 8443, Method: "HEAD"}) {
		t.Errorf("传给服务的输入不对: %+v", f.got)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Errorf("应该是 no-store")
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	ex, _ := out["exit"].(map[string]any)
	if out["target"] != "example.com" || ex["url"] != "https://example.com/" || ex["ok"] != true || ex["status"] != float64(200) ||
		ex["viaProxy"] != true || ex["rule"] != "rule_set=x" {
		t.Errorf("响应形状不对: %s", rec.Body)
	}
	if _, has := ex["error"]; has {
		t.Errorf("空的 error 不该出现: %s", rec.Body)
	}
	if dns, _ := out["dns"].(map[string]any); dns["error"] != "x" {
		t.Errorf("dns 没原样带出: %s", rec.Body)
	}
}

func TestPortAndMethodOptional(t *testing.T) {
	f := &fake{res: &routetest.Result{}}
	if rec := do(t, f, http.MethodPost, `{"target":"1.1.1.1"}`); rec.Code != 200 {
		t.Fatalf("只给目标应该可以: %d %s", rec.Code, rec.Body)
	}
	if f.got != (routetest.Input{Target: "1.1.1.1"}) {
		t.Errorf("%+v", f.got)
	}
}

func TestErrors(t *testing.T) {
	for _, c := range []struct {
		name   string
		f      *fake
		method string
		body   string
		code   int
		msg    string
	}{
		{"输入有问题", &fake{err: &routetest.InputError{Msg: "请填写要测试的域名或 IP"}}, http.MethodPost, `{}`, 400, "请填写"},
		{"不是 JSON", &fake{}, http.MethodPost, `不是json`, 400, ""},
		{"其它错误", &fake{err: errors.New("boom")}, http.MethodPost, `{"target":"a.com"}`, 500, "boom"},
		{"只收 POST", &fake{}, http.MethodGet, ``, 405, ""},
	} {
		rec := do(t, c.f, c.method, c.body)
		if rec.Code != c.code {
			t.Errorf("%s: 状态码 %d,要 %d: %s", c.name, rec.Code, c.code, rec.Body)
		}
		if c.msg != "" && !strings.Contains(rec.Body.String(), c.msg) {
			t.Errorf("%s: 错误信息里没有 %q: %s", c.name, c.msg, rec.Body)
		}
	}
}

func TestCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fake{err: context.Canceled}
	mux := http.NewServeMux()
	Register(mux, Deps{Service: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/route-test", strings.NewReader(`{"target":"a.com"}`)).WithContext(ctx))
	if rec.Code != 503 {
		t.Errorf("取消应该回 503,得到 %d", rec.Code)
	}
}
