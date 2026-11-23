package nodeinfo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/nodeexit"
	ni "github.com/timeflysoon/Open-Box/backend/internal/nodeinfo"
	"github.com/timeflysoon/Open-Box/backend/internal/nodesharing"
	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

type fake struct {
	info *ni.Info
	err  error
	tag  string

	entry    *ni.EntryResult
	entryErr error
}

func (f *fake) EntryCountry(_ context.Context, tag string) (*ni.EntryResult, error) {
	f.tag = tag
	return f.entry, f.entryErr
}

func (f *fake) Get(_ context.Context, tag string) (*ni.Info, error) {
	f.tag = tag
	return f.info, f.err
}

func do(f *fake, method, path string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, Deps{Service: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestGetShapeAndEncodedTag(t *testing.T) {
	port := 443
	f := &fake{info: &ni.Info{Tag: "HK 01/备用", Type: "vless", Protocol: []string{"vless", "tls"}, Server: "a.example.com", Port: &port, Addresses: ni.Addresses{V4: []string{"203.0.113.1"}, V6: []string{}}}}
	rec := do(f, http.MethodGet, "/api/openbox/node-info/"+url.PathEscape("HK 01/备用"))
	if rec.Code != 200 || f.tag != "HK 01/备用" {
		t.Fatalf("带空格和斜杠的 tag 应被正确还原: %d tag=%q", rec.Code, f.tag)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	addrs := got["addresses"].(map[string]any)
	if got["port"] != float64(443) || got["entryCountry"] != "" || len(addrs["v6"].([]any)) != 0 || got["protocol"] == nil {
		t.Errorf("形状: %s", rec.Body)
	}
	if _, has := got["detour"]; has {
		t.Error("没有上游时不带 detour")
	}
	if _, has := got["resolveError"]; has {
		t.Error("解析成功时不带 resolveError")
	}
	nullPort := &ni.Info{Tag: "x", Addresses: ni.Addresses{V4: []string{}, V6: []string{}}, Protocol: []string{}}
	if rec := do(&fake{info: nullPort}, http.MethodGet, "/api/openbox/node-info/x"); !strings.Contains(rec.Body.String(), `"port":null`) {
		t.Errorf("端口没有时应是 null: %s", rec.Body)
	}
}

func TestErrorsAndMethods(t *testing.T) {
	if rec := do(&fake{err: ni.ErrNotFound}, http.MethodGet, "/api/openbox/node-info/nope"); rec.Code != 404 {
		t.Errorf("没有这个节点应 404: %d", rec.Code)
	}
	if rec := do(&fake{err: errors.New("读不了")}, http.MethodGet, "/api/openbox/node-info/x"); rec.Code != 500 {
		t.Errorf("内部错误应 500: %d", rec.Code)
	}
	if rec := do(&fake{}, http.MethodPost, "/api/openbox/node-info/x"); rec.Code != 405 {
		t.Errorf("POST 应 405: %d", rec.Code)
	}
}

func TestEntryCountry(t *testing.T) {
	f := &fake{entry: &ni.EntryResult{IP: "203.0.113.2", Country: "JP"}}
	rec := do(f, http.MethodGet, "/api/openbox/node-info/"+url.PathEscape("HK 01/备用")+"/entry-country")
	if rec.Code != 200 || f.tag != "HK 01/备用" || strings.TrimSpace(rec.Body.String()) != `{"ip":"203.0.113.2","country":"JP"}` {
		t.Errorf("形状: %d tag=%q %s", rec.Code, f.tag, rec.Body)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("应带 no-store")
	}
}

func TestEntryCountryErrors(t *testing.T) {
	cases := []struct {
		err  error
		code int
		has  string
	}{
		{ni.ErrNotFound, 404, "没有这个节点"},
		{&ni.ErrNoAddress{Reason: "域名不存在"}, 502, "域名不存在"},
		{errors.New("查归属失败:api.ip.sb: 超时"), 502, "查归属失败"},
	}
	for _, c := range cases {
		rec := do(&fake{entryErr: c.err}, http.MethodGet, "/api/openbox/node-info/x/entry-country")
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.has) {
			t.Errorf("%v: %d %s", c.err, rec.Code, rec.Body)
		}
	}
	if rec := do(&fake{}, http.MethodPost, "/api/openbox/node-info/x/entry-country"); rec.Code != 405 {
		t.Errorf("POST 应 405: %d", rec.Code)
	}
}

type fakeExit struct {
	res     *nodeexit.Result
	err     error
	tag     string
	refresh bool
	calls   int
}

func (f *fakeExit) Query(_ context.Context, tag string, refresh bool) (*nodeexit.Result, error) {
	f.calls++
	f.tag, f.refresh = tag, refresh
	return f.res, f.err
}

func doExit(x *fakeExit, method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, Deps{Service: &fake{}, Exit: x})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	}
	mux.ServeHTTP(rec, req)
	return rec
}

func TestExitOKAndRefreshFlag(t *testing.T) {
	trust := 61.0
	x := &fakeExit{res: &nodeexit.Result{OK: true, At: 1, IP: "203.0.113.2", Country: "JP", TrustScore: &trust, Source: "ip.net.coffee", Errors: &nodeexit.Errors{NetCoffee: "超时"}}}
	rec := doExit(x, http.MethodPost, "/api/openbox/node-info/"+url.PathEscape("HK 01/备用")+"/exit", `{"refresh":true}`)
	if rec.Code != 200 || x.tag != "HK 01/备用" || !x.refresh {
		t.Fatalf("%d tag=%q refresh=%v %s", rec.Code, x.tag, x.refresh, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != true || got["trustScore"] != float64(61) || got["source"] != "ip.net.coffee" || got["devices"] != nil || got["native"] != nil || got["errors"].(map[string]any)["netcoffee"] != "超时" {
		t.Errorf("形状(没查到的项是 null,errors 里写出错的来源): %s", rec.Body)
	}
	if _, has := got["cached"]; has {
		t.Error("不是缓存时不带 cached")
	}
	// 没有请求体、空对象都按不刷新
	x2 := &fakeExit{res: &nodeexit.Result{OK: true}}
	if rec := doExit(x2, http.MethodPost, "/api/openbox/node-info/x/exit", ""); rec.Code != 200 || x2.refresh {
		t.Errorf("无请求体: %d refresh=%v", rec.Code, x2.refresh)
	}
	if rec := doExit(x2, http.MethodPost, "/api/openbox/node-info/x/exit", `{}`); rec.Code != 200 || x2.refresh {
		t.Errorf("空对象: %d", rec.Code)
	}
	if rec := doExit(x2, http.MethodPost, "/api/openbox/node-info/x/exit", `{nope`); rec.Code != 400 {
		t.Errorf("坏 JSON 应 400: %d", rec.Code)
	}
}

func TestExitErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code int
		has  string
	}{
		{nodeexit.ErrNoOutbound, 404, "没有可用的出站"},
		{ni.ErrNotFound, 404, "没有这个节点"},
		{singbox.ErrCoreUnavailable, 503, "openbox-core"},
		{probe.ErrNoSingBox, 503, "sing-box"},
		{&nodeexit.ProbeError{Msg: "api.ip.sb: 超时"}, 502, "经这个节点没有查到出口地址:api.ip.sb: 超时"},
		{context.DeadlineExceeded, 504, "超时"},
		{errors.New("坏了"), 500, "坏了"},
	}
	for _, c := range cases {
		rec := doExit(&fakeExit{err: c.err}, http.MethodPost, "/api/openbox/node-info/x/exit", "")
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.has) {
			t.Errorf("%v: %d %s", c.err, rec.Code, rec.Body)
		}
	}
}

func TestExitMethodAndNotMounted(t *testing.T) {
	if rec := doExit(&fakeExit{}, http.MethodGet, "/api/openbox/node-info/x/exit", ""); rec.Code != 405 {
		t.Errorf("GET 应 405: %d", rec.Code)
	}
	// 没配 Exit 时不挂这条路由(交给上层的 404)
	mux := http.NewServeMux()
	Register(mux, Deps{Service: &fake{}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/node-info/x/exit", nil))
	if rec.Code != 404 {
		t.Errorf("没挂应 404: %d", rec.Code)
	}
}

type fakeSharing struct {
	rep     *nodesharing.Report
	err     error
	ip      string
	refresh bool
}

func (f *fakeSharing) Query(_ context.Context, ip string, refresh bool) (*nodesharing.Report, error) {
	f.ip, f.refresh = ip, refresh
	return f.rep, f.err
}

func doFull(d Deps, method, path string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestSharingRoute(t *testing.T) {
	rng := "1-10"
	fs := &fakeSharing{rep: &nodesharing.Report{Range: &rng, IPTypes: []string{"机房 IP"}}}
	rec := doFull(Deps{Service: &fake{}, Sharing: fs}, http.MethodGet, "/api/openbox/node-info/sharing/2001:db8::1?refresh=1")
	if rec.Code != 200 || fs.ip != "2001:db8::1" || !fs.refresh {
		t.Fatalf("%d ip=%q refresh=%v %s", rec.Code, fs.ip, fs.refresh, rec.Body)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["range"] != "1-10" || got["risk"] != nil || len(got["ipTypes"].([]any)) != 1 {
		t.Errorf("形状: %s", rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("应带 no-store")
	}
	rec = doFull(Deps{Service: &fake{}, Sharing: fs}, http.MethodGet, "/api/openbox/node-info/sharing/203.0.113.9")
	if fs.refresh {
		t.Errorf("没带 refresh=1 不该刷新")
	}
}

func TestSharingErrorsAndMethods(t *testing.T) {
	fs := &fakeSharing{err: nodesharing.ErrInvalidIP}
	if rec := doFull(Deps{Service: &fake{}, Sharing: fs}, http.MethodGet, "/api/openbox/node-info/sharing/abc"); rec.Code != 400 || !strings.Contains(rec.Body.String(), "无效的 IP 地址") {
		t.Errorf("非法 IP: %d %s", rec.Code, rec.Body)
	}
	fs.err = context.Canceled
	if rec := doFull(Deps{Service: &fake{}, Sharing: fs}, http.MethodGet, "/api/openbox/node-info/sharing/203.0.113.9"); rec.Code != http.StatusGatewayTimeout {
		t.Errorf("取消: %d", rec.Code)
	}
	if rec := doFull(Deps{Service: &fake{}, Sharing: &fakeSharing{}}, http.MethodPost, "/api/openbox/node-info/sharing/203.0.113.9"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 应 405: %d", rec.Code)
	}
	if rec := doFull(Deps{Service: &fake{}}, http.MethodGet, "/api/openbox/node-info/sharing/203.0.113.9"); rec.Code != 404 {
		t.Errorf("没接服务应 404: %d", rec.Code)
	}
}

// 节点的子路径不能被 sharing 抢走,sharing 也不能被节点路径吃掉;名字叫 sharing 的节点照常能查。
func TestSharingDoesNotShadowNodeRoutes(t *testing.T) {
	f := &fake{info: &ni.Info{Tag: "sharing"}, entry: &ni.EntryResult{IP: "203.0.113.1", Country: "US"}}
	d := Deps{Service: f, Sharing: &fakeSharing{rep: &nodesharing.Report{IPTypes: []string{}}}}
	if rec := doFull(d, http.MethodGet, "/api/openbox/node-info/sharing"); rec.Code != 200 || f.tag != "sharing" {
		t.Errorf("名叫 sharing 的节点: %d tag=%q", rec.Code, f.tag)
	}
	if rec := doFull(d, http.MethodGet, "/api/openbox/node-info/sharing/entry-country"); rec.Code != 200 || f.tag != "sharing" || !strings.Contains(rec.Body.String(), "US") {
		t.Errorf("sharing 节点的 entry-country: %d %s", rec.Code, rec.Body)
	}
	if rec := doFull(d, http.MethodGet, "/api/openbox/node-info/HK%2001/entry-country"); rec.Code != 200 || f.tag != "HK 01" {
		t.Errorf("普通节点 entry-country: %d tag=%q", rec.Code, f.tag)
	}
	if rec := doFull(d, http.MethodGet, "/api/openbox/node-info/HK/unknown"); rec.Code != 404 {
		t.Errorf("未知子路径应 404: %d", rec.Code)
	}
}
