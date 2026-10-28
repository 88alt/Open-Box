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

	ni "github.com/timeflysoon/Open-Box/backend/internal/nodeinfo"
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
