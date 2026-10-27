package dnsfilter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsfilter"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

func getPreview(t *testing.T, d Deps, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/dns-filter/preview?"+rawQuery, nil))
	return rec
}

func TestPreviewEndToEndOverHTTP(t *testing.T) {
	list := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("0.0.0.0 ads.example.com\n||t.example.net^\n@@||ok.example.net^\n##.x\n"))
	}))
	defer list.Close()
	d := Deps{Preview: dnsfilter.NewPreviewer(func(ctx context.Context, u string) ([]byte, error) {
		return rulelist.Fetch(ctx, nil, u)
	}).Preview}
	rec := getPreview(t, d, url.Values{"url": {list.URL}, "search": {""}, "action": {"block"}, "page": {"1"}, "pageSize": {"20"}}.Encode())
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	var got dnsfilter.Preview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 3 || got.Total != 2 || got.Unsupported != 1 || got.Source != "url" || len(got.Rows) != 2 {
		t.Fatalf("%+v", got)
	}
	// 字段名要和前端类型一致
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	for _, k := range []string{"rows", "count", "total", "page", "pageSize", "ruleCount", "unsupported", "unsupportedExamples", "source", "updatedAt"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("缺字段 %s", k)
		}
	}
	if raw["updatedAt"] != nil {
		t.Error("updatedAt 应为 null")
	}
	row := raw["rows"].([]any)[0].(map[string]any)
	for _, k := range []string{"type", "value", "rule", "action", "important", "conditional"} {
		if _, ok := row[k]; !ok {
			t.Errorf("行缺字段 %s", k)
		}
	}
}

func TestPreviewErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{&rulelist.InputError{Msg: "地址不对"}, 400},
		{&rulelist.FetchError{Msg: "无法连接"}, 502},
		{dnsfilter.ErrNoRules, 400},
		{dnsfilter.ErrBinary, 400},
		{dnsfilter.ErrTooMany, 400},
		{errors.New("别的"), 400},
	}
	for _, c := range cases {
		d := Deps{Preview: func(context.Context, dnsfilter.PreviewQuery) (*dnsfilter.Preview, error) { return nil, c.err }}
		rec := getPreview(t, d, "url=https://a.com/x")
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%v → %d %s", c.err, rec.Code, rec.Body)
		}
	}
}

func TestPreviewParamsAndUnavailable(t *testing.T) {
	var got dnsfilter.PreviewQuery
	d := Deps{Preview: func(_ context.Context, q dnsfilter.PreviewQuery) (*dnsfilter.Preview, error) {
		got = q
		return &dnsfilter.Preview{Rows: []dnsfilter.Entry{}}, nil
	}}
	if rec := getPreview(t, d, "url=%20https%3A%2F%2Fa.com%2Fx%20&search=Foo&action=allow&page=abc&pageSize=50"); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	want := dnsfilter.PreviewQuery{URL: "https://a.com/x", Search: "Foo", Action: "allow", Page: 0, PageSize: 50}
	if got != want {
		t.Fatalf("%+v", got)
	}
	if rec := getPreview(t, d, "search=x"); rec.Code != 400 {
		t.Fatal("缺 url 应 400:", rec.Code)
	}
	if rec := getPreview(t, Deps{}, "url=https://a.com/x"); rec.Code != 503 {
		t.Fatal("没接预览应 503:", rec.Code)
	}
}
