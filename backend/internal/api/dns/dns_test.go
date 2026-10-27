package dns

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnscache"
	"github.com/timeflysoon/Open-Box/backend/internal/dnstest"
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

func TestFlushCache(t *testing.T) {
	// 有一层成功:200,三层各自如实回报
	rec := callPath(Deps{Flush: func(context.Context) dnscache.Result { return dnscache.Result{Dnsmasq: true} }}, http.MethodPost, "/api/openbox/dns/flush-cache")
	var got dnscache.Result
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got != (dnscache.Result{Dnsmasq: true}) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, k := range []string{`"kernel":false`, `"dnsmasq":true`, `"resolved":false`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Errorf("缺 %s: %s", k, rec.Body.String())
		}
	}
	// 全都清不掉 / 没接实现:503 + error
	for name, d := range map[string]Deps{
		"全失败":  {Flush: func(context.Context) dnscache.Result { return dnscache.Result{} }},
		"没接实现": {},
	} {
		rec := callPath(d, http.MethodPost, "/api/openbox/dns/flush-cache")
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// 清缓存会动系统,只接受 POST
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if rec := callPath(Deps{Flush: func(context.Context) dnscache.Result { return dnscache.Result{Kernel: true} }}, m, "/api/openbox/dns/flush-cache"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
}

func postTest(d Deps, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/dns/upstream-test", strings.NewReader(body)))
	return rec
}

func TestUpstreamTest(t *testing.T) {
	var got dnstest.Request
	ok := func(_ context.Context, r dnstest.Request) (dnstest.Result, error) {
		got = r
		return dnstest.Result{OK: true, Ms: 12, Server: r.Server, Protocol: r.Protocol, Port: 53, Via: "节点", Chain: []string{"头", "节点"}, Policy: "其他"}, nil
	}
	// 请求体按前端的字段名解析,结果的字段名也和前端类型一致
	rec := postTest(Deps{Test: ok}, `{"side":"proxy","server":"1.1.1.1","protocol":"tcp","port":53}`)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if got != (dnstest.Request{Side: "proxy", Server: "1.1.1.1", Protocol: "tcp", Port: 53}) {
		t.Errorf("请求解析不对: %+v", got)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["ok"] != true || m["ms"] != float64(12) || m["via"] != "节点" || m["policy"] != "其他" || m["port"] != float64(53) || len(m["chain"].([]any)) != 2 {
		t.Errorf("结果不对: %s", rec.Body.String())
	}
	if _, has := m["error"]; has {
		t.Errorf("成功时不该有 error: %s", rec.Body.String())
	}

	// 测了没通:200 + ok:false + error;via 字段始终在(前端当字符串用)
	rec = postTest(Deps{Test: func(context.Context, dnstest.Request) (dnstest.Result, error) {
		return dnstest.Result{Error: "超时:8 秒内没有答复", Server: "10.0.0.9", Protocol: "udp", Port: 53}, nil
	}}, `{"side":"direct","server":"10.0.0.9"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), `"via":""`) || !strings.Contains(rec.Body.String(), "超时") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}

	// 错误映射
	for name, c := range map[string]struct {
		err  error
		code int
	}{
		"请求不合法":     {&dnstest.InputError{Msg: "side 只能是 direct 或 proxy"}, 400},
		"现在测不了":     {&dnstest.UnavailableError{Msg: "找不到 sing-box 可执行文件,无法测试"}, 503},
		"别的错误":      {errors.New("boom"), 500},
		"包了一层的 400": {errors.Join(errors.New("外层"), &dnstest.InputError{Msg: "x"}), 400},
	} {
		c := c
		rec := postTest(Deps{Test: func(context.Context, dnstest.Request) (dnstest.Result, error) { return dnstest.Result{}, c.err }}, `{"side":"x"}`)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	// 没接实现 → 503;坏 JSON → 400;只接受 POST
	if rec := postTest(Deps{}, `{}`); rec.Code != 503 {
		t.Errorf("没接实现: %d", rec.Code)
	}
	if rec := postTest(Deps{Test: ok}, `{`); rec.Code != 400 {
		t.Errorf("坏 JSON: %d", rec.Code)
	}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if rec := callPath(Deps{Test: ok}, m, "/api/openbox/dns/upstream-test"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
}
