package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/clientapp"
)

type fake struct {
	info clientapp.Info
	err  error
}

func (f fake) Info(context.Context) (clientapp.Info, error) { return f.info, f.err }

func call(t *testing.T, d Deps, method string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/client-app/info", strings.NewReader("")))
	return rec
}

func TestInfoOK(t *testing.T) {
	rec := call(t, Deps{Service: fake{info: clientapp.Info{
		RouterID: "0123456789abcdef0123456789abcdef", RouterName: "Open-Box", PanelPort: 3036,
		LanAddresses: []string{"192.168.5.1"}, LanSubnets: []string{"192.168.5.0/24"},
		ShareRegions: clientapp.DefaultShareRegions(), DefaultShareRegions: clientapp.DefaultShareRegions(),
	}}}, http.MethodGet)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q", cc)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["routerId"] != "0123456789abcdef0123456789abcdef" || m["panelPort"] != float64(3036) {
		t.Errorf("%s", rec.Body)
	}
	if v, ok := m["egressCountry"]; !ok || v != nil {
		t.Errorf("egressCountry 应在且为 null: %s", rec.Body)
	}
}

func TestInfoErrors(t *testing.T) {
	if rec := call(t, Deps{Service: fake{err: errors.New("磁盘满了")}}, http.MethodGet); rec.Code != 500 || !strings.Contains(rec.Body.String(), "磁盘满了") {
		t.Errorf("服务出错: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, Deps{}, http.MethodGet); rec.Code != 503 {
		t.Errorf("没接服务: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, Deps{Service: fake{}}, http.MethodPost); rec.Code != 405 {
		t.Errorf("POST: %d", rec.Code)
	}
}

type fakeConfig struct {
	is      clientapp.Issue
	err     error
	enabled bool
	setArg  *bool
}

func (f *fakeConfig) ClientConfig(context.Context) (clientapp.Issue, error) { return f.is, f.err }
func (f *fakeConfig) SetClientConfigEnabled(_ context.Context, on bool) (bool, error) {
	f.setArg = &on
	return on, f.err
}
func (f *fakeConfig) RotateClientConfig(context.Context) (clientapp.Issue, error) {
	f.is.PairCode = "ROTATED2"
	return f.is, f.err
}

func callPath(t *testing.T, d Deps, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestClientConfigGetShape(t *testing.T) {
	f := &fakeConfig{is: clientapp.Issue{Enabled: true, PairCode: "ABCD2345", Nonce: "0123456789abcdef", LastSyncAt: 5,
		RouterID: "0123456789abcdef0123456789abcdef", RouterName: "Open-Box", LanAddresses: []string{"192.168.5.1"}, PanelPort: 3036}}
	rec := callPath(t, Deps{Config: f}, http.MethodGet, "/api/openbox/client-config", "")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	// 前端 OpenboxClientConfigIssue 的八个字段一个不少
	for _, k := range []string{"enabled", "pairCode", "nonce", "lastSyncAt", "routerId", "routerName", "lanAddresses", "panelPort"} {
		if _, ok := m[k]; !ok {
			t.Errorf("缺字段 %s: %s", k, rec.Body)
		}
	}
	if m["pairCode"] != "ABCD2345" || m["lastSyncAt"] != float64(5) || m["enabled"] != true {
		t.Errorf("%s", rec.Body)
	}
}

func TestClientConfigEnabled(t *testing.T) {
	f := &fakeConfig{}
	rec := callPath(t, Deps{Config: f}, http.MethodPut, "/api/openbox/client-config/enabled", `{"enabled":true}`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"enabled":true}` || f.setArg == nil || !*f.setArg {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = callPath(t, Deps{Config: f}, http.MethodPut, "/api/openbox/client-config/enabled", `{"enabled":false}`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"enabled":false}` || *f.setArg {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// 坏请求体一律 400,且不能调到服务
	for _, bad := range []string{``, `{}`, `{"enabled":null}`, `{"enabled":"true"}`, `{"enabled":1}`, `[]`, `{"enabled":true} x`, `not json`} {
		f2 := &fakeConfig{}
		rec = callPath(t, Deps{Config: f2}, http.MethodPut, "/api/openbox/client-config/enabled", bad)
		if rec.Code != 400 || f2.setArg != nil {
			t.Errorf("%q: %d %s called=%v", bad, rec.Code, rec.Body, f2.setArg != nil)
		}
	}
	rec = callPath(t, Deps{Config: &fakeConfig{}}, http.MethodPut, "/api/openbox/client-config/enabled", `{"enabled":true,"pad":"`+strings.Repeat("x", 2000)+`"}`)
	if rec.Code != 413 {
		t.Errorf("过大: %d", rec.Code)
	}
}

func TestClientConfigRotate(t *testing.T) {
	f := &fakeConfig{is: clientapp.Issue{PairCode: "ABCD2345", LanAddresses: []string{}}}
	rec := callPath(t, Deps{Config: f}, http.MethodPost, "/api/openbox/client-config/rotate", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ROTATED2") || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestClientConfigErrorsAndMethods(t *testing.T) {
	boom := &fakeConfig{err: errors.New("磁盘满了")}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/openbox/client-config", ""},
		{http.MethodPut, "/api/openbox/client-config/enabled", `{"enabled":true}`},
		{http.MethodPost, "/api/openbox/client-config/rotate", ""},
	} {
		if rec := callPath(t, Deps{Config: boom}, c.method, c.path, c.body); rec.Code != 500 || !strings.Contains(rec.Body.String(), "磁盘满了") {
			t.Errorf("%s %s 服务出错: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
		if rec := callPath(t, Deps{}, c.method, c.path, c.body); rec.Code != 503 {
			t.Errorf("%s %s 没接服务: %d", c.method, c.path, rec.Code)
		}
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/openbox/client-config"},
		{http.MethodGet, "/api/openbox/client-config/enabled"},
		{http.MethodPost, "/api/openbox/client-config/enabled"},
		{http.MethodGet, "/api/openbox/client-config/rotate"},
		{http.MethodPut, "/api/openbox/client-config/rotate"},
	} {
		if rec := callPath(t, Deps{Config: &fakeConfig{}}, c.method, c.path, ""); rec.Code != 405 {
			t.Errorf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
	// 出错响应里不能带配对码
	leak := &fakeConfig{is: clientapp.Issue{PairCode: "SECRET22"}, err: errors.New("失败")}
	if rec := callPath(t, Deps{Config: leak}, http.MethodGet, "/api/openbox/client-config", ""); strings.Contains(rec.Body.String(), "SECRET22") {
		t.Errorf("错误响应泄露了配对码: %s", rec.Body)
	}
}

type fakeEgress struct {
	e   clientapp.EgressCountry
	err error
}

func (f fakeEgress) DetectEgressCountry(context.Context) (clientapp.EgressCountry, error) {
	return f.e, f.err
}

func TestEgressCountryHandler(t *testing.T) {
	const path = "/api/openbox/client-app/egress-country"
	ok := fakeEgress{e: clientapp.EgressCountry{IP: "203.0.113.7", Country: "JP", At: 1700000000000}}
	rec := callPath(t, Deps{Egress: ok}, http.MethodPost, path, "")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	// 前端 OpenboxEgressCountry:ip / country / at(毫秒)
	if len(m) != 3 || m["ip"] != "203.0.113.7" || m["country"] != "JP" || m["at"] != float64(1700000000000) {
		t.Fatalf("%s", rec.Body)
	}
	for _, c := range []struct {
		err  error
		want int
	}{
		{clientapp.ErrNotRunning, 503},
		{clientapp.ErrUnavailable, 503},
		{&clientapp.DetectError{Msg: "判断地区失败:api.ip.sb: 超时"}, 502},
		{context.Canceled, 503},
		{errors.New("磁盘满了"), 500},
	} {
		rec := callPath(t, Deps{Egress: fakeEgress{err: c.err}}, http.MethodPost, path, "")
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), "error") {
			t.Errorf("%v: %d %s(想要 %d)", c.err, rec.Code, rec.Body, c.want)
		}
	}
	// 被包了一层的错误也认得
	wrapped := fmt.Errorf("外层: %w", clientapp.ErrNotRunning)
	if rec := callPath(t, Deps{Egress: fakeEgress{err: wrapped}}, http.MethodPost, path, ""); rec.Code != 503 {
		t.Errorf("包一层: %d", rec.Code)
	}
	if rec := callPath(t, Deps{}, http.MethodPost, path, ""); rec.Code != 503 {
		t.Errorf("没接服务: %d", rec.Code)
	}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if rec := callPath(t, Deps{Egress: ok}, m, path, ""); rec.Code != 405 {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
}
