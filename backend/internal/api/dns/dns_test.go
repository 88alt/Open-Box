package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/regiondetect"
)

func call(d Deps, method string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/dns/wan", nil))
	return rec
}

func TestWAN(t *testing.T) {
	cases := []struct {
		name string
		wan  func() []string
		want []string
	}{
		{"有上游", func() []string { return []string{"202.96.128.86", "2400:3200::1"} }, []string{"202.96.128.86", "2400:3200::1"}},
		{"没有上游", func() []string { return []string{} }, []string{}},
		{"返回 nil 也是空数组", func() []string { return nil }, []string{}},
		{"没接读取函数", nil, []string{}},
	}
	for _, c := range cases {
		rec := call(Deps{WAN: c.wan}, http.MethodGet)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %v", c.name, rec.Code, rec.Header())
		}
		var got struct {
			Servers []string `json:"servers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v %s", c.name, err, rec.Body.String())
		}
		// 必须是 [] 而不是 null,前端按数组处理
		if got.Servers == nil || !reflect.DeepEqual(got.Servers, c.want) {
			t.Errorf("%s: got %#v want %#v (%s)", c.name, got.Servers, c.want, rec.Body.String())
		}
	}
}

func TestWANMethodNotAllowed(t *testing.T) {
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if rec := call(Deps{}, m); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
}

type fakeDet struct {
	pending bool
	res     regiondetect.Result
	calls   int
}

func (f *fakeDet) Pending() bool { return f.pending }
func (f *fakeDet) Detect(context.Context) regiondetect.Result {
	f.calls++
	return f.res
}

func callPath(d Deps, method, path string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestRegionState(t *testing.T) {
	cases := []struct {
		name    string
		region  func(context.Context) string
		det     Detector
		want    string
		pending bool
	}{
		{"档案是 intl", func(context.Context) string { return "intl" }, &fakeDet{}, "intl", false},
		{"档案是 cn", func(context.Context) string { return "cn" }, &fakeDet{}, "cn", false},
		{"别的值按 cn", func(context.Context) string { return "xx" }, &fakeDet{}, "cn", false},
		{"没接读取函数", nil, nil, "cn", false},
		{"判断进行中", func(context.Context) string { return "cn" }, &fakeDet{pending: true}, "cn", true},
	}
	for _, c := range cases {
		rec := callPath(Deps{Region: c.region, Detector: c.det}, http.MethodGet, "/api/openbox/dns/region-detect")
		var got struct {
			Pending bool   `json:"pending"`
			Region  string `json:"region"`
		}
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
		if got.Region != c.want || got.Pending != c.pending {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestRegionDetect(t *testing.T) {
	cn := "cn"
	f := &fakeDet{res: regiondetect.Result{Region: &cn, IP: "1.2.3.4"}}
	rec := callPath(Deps{Detector: f}, http.MethodPost, "/api/openbox/dns/region-detect")
	if rec.Code != 200 || f.calls != 1 || !strings.Contains(rec.Body.String(), `"region":"cn"`) || !strings.Contains(rec.Body.String(), `"ip":"1.2.3.4"`) {
		t.Fatalf("%d %s calls=%d", rec.Code, rec.Body.String(), f.calls)
	}
	// 判不出来:200,region 必须是 null(不是缺字段),error 带原因
	f = &fakeDet{res: regiondetect.Result{Error: "判断地区失败:x"}}
	rec = callPath(Deps{Detector: f}, http.MethodPost, "/api/openbox/dns/region-detect")
	var got map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if v, ok := got["region"]; !ok || v != nil || got["ip"] != "" || got["error"] == nil {
		t.Fatalf("%v", got)
	}
	// 没接判断器:503
	if rec := callPath(Deps{}, http.MethodPost, "/api/openbox/dns/region-detect"); rec.Code != 503 {
		t.Fatalf("%d", rec.Code)
	}
	for _, m := range []string{http.MethodPut, http.MethodDelete} {
		if rec := callPath(Deps{Detector: f}, m, "/api/openbox/dns/region-detect"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
}
