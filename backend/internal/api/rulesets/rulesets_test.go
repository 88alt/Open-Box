package rulesets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

func setup(t *testing.T, body string) (http.Handler, string) {
	t.Helper()
	pages := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list":
			_, _ = w.Write([]byte(body))
		case "/bin":
			_, _ = w.Write([]byte("\x00\x01\x02binary"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(pages.Close)
	mux := http.NewServeMux()
	Register(mux, Deps{Lists: rulelist.NewService(func(ctx context.Context, u string) ([]byte, error) {
		return rulelist.Fetch(ctx, nil, u)
	})})
	return mux, pages.URL
}

func get(h http.Handler, method, path string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func lines(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "DOMAIN-SUFFIX,site%03d.example.com\n", i)
	}
	return sb.String()
}

func TestPreviewPagingAndFilter(t *testing.T) {
	h, base := setup(t, lines(120))
	q := func(extra string) string {
		return "/api/openbox/rulesets/preview?url=" + url.QueryEscape(base+"/list") + extra
	}
	code, m := get(h, "GET", q(""))
	if code != 200 || m["total"] != float64(120) || m["matched"] != float64(120) || m["limit"] != float64(50) || len(m["entries"].([]any)) != 50 {
		t.Fatalf("%d %v", code, m)
	}
	if m["url"] != base+"/list" {
		t.Fatalf("url=%v", m["url"])
	}
	_, m = get(h, "GET", q("&offset=100&limit=50"))
	if len(m["entries"].([]any)) != 20 || m["offset"] != float64(100) {
		t.Fatalf("末页: %v", m)
	}
	_, m = get(h, "GET", q("&q=SITE05"))
	if m["matched"] != float64(10) || m["total"] != float64(120) {
		t.Fatalf("过滤: %v", m)
	}
	_, m = get(h, "GET", q("&limit=1"))
	if len(m["entries"].([]any)) != 1 || m["total"] != float64(120) {
		t.Fatalf("limit=1(前端用来取总数): %v", m)
	}
	_, m = get(h, "GET", q("&offset=9999&limit=99999"))
	if len(m["entries"].([]any)) != 0 || m["limit"] != float64(maxLimit) {
		t.Fatalf("越界: %v", m)
	}
}

func TestImportReturnsAll(t *testing.T) {
	h, base := setup(t, "DOMAIN,a.com\nIP-CIDR,10.0.0.0/8\nDOMAIN-KEYWORD,k\n")
	code, m := get(h, "GET", "/api/openbox/rulesets/import?url="+url.QueryEscape(base+"/list"))
	if code != 200 || len(m["entries"].([]any)) != 3 || m["total"] != float64(3) {
		t.Fatalf("%d %v", code, m)
	}
	e := m["entries"].([]any)[1].(map[string]any)
	if e["type"] != "ip_cidr" || e["value"] != "10.0.0.0/8" {
		t.Fatalf("%v", e)
	}
}

func TestErrors(t *testing.T) {
	h, base := setup(t, "# nothing\n")
	enc := url.QueryEscape
	cases := []struct {
		name, path string
		status     int
	}{
		{"缺 url", "/api/openbox/rulesets/preview", 400},
		{"非 http", "/api/openbox/rulesets/preview?url=" + enc("file:///etc/passwd"), 400},
		{"空名单", "/api/openbox/rulesets/preview?url=" + enc(base+"/list"), 400},
		{"二进制", "/api/openbox/rulesets/import?url=" + enc(base+"/bin"), 400},
		{"对端 404", "/api/openbox/rulesets/preview?url=" + enc(base+"/nope"), 502},
	}
	for _, c := range cases {
		code, m := get(h, "GET", c.path)
		if code != c.status || m["error"] == nil {
			t.Errorf("%s: %d %v", c.name, code, m)
		}
	}
}

func TestNotImplementedEndpoints(t *testing.T) {
	h, _ := setup(t, "") // setup 没接规则库和更新服务:entries、refresh 都不可用(503)
	if code, m := get(h, "GET", "/api/openbox/rulesets/entries?tag=geosite-cn"); code != 503 || m["error"] == nil {
		t.Fatalf("entries: %d %v", code, m)
	}
	if code, m := get(h, "POST", "/api/openbox/rulesets/refresh"); code != 503 || m["error"] == nil {
		t.Fatalf("refresh: %d %v", code, m)
	}
	if code, _ := get(h, "POST", "/api/openbox/rulesets/preview?url=x"); code != http.StatusMethodNotAllowed {
		t.Fatalf("method: %d", code)
	}
}

func TestImportTooMany(t *testing.T) {
	h, base := setup(t, lines(maxImport+1))
	code, m := get(h, "GET", "/api/openbox/rulesets/import?url="+url.QueryEscape(base+"/list"))
	if code != 400 || m["error"] == nil {
		t.Fatalf("%d", code)
	}
}

type fakeRefresher struct {
	res *singbox.RefreshResult
	err error
	got string
}

func (f *fakeRefresher) Refresh(_ context.Context, u string) (*singbox.RefreshResult, error) {
	f.got = u
	return f.res, f.err
}

func postRefresh(h http.Handler, body string) (int, map[string]any) {
	req := httptest.NewRequest("POST", "/api/openbox/rulesets/refresh", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func refreshHandler(f *fakeRefresher) http.Handler {
	mux := http.NewServeMux()
	Register(mux, Deps{Lists: rulelist.NewService(nil), Refresh: f})
	return mux
}

func TestRefreshOK(t *testing.T) {
	f := &fakeRefresher{res: &singbox.RefreshResult{
		URL: "https://e.com/a.list", Tag: "list-1234abcd", Total: 3,
		Counts: map[string]int{"domain_suffix": 2, "ip_cidr": 1}, NeedsRestart: true, Changed: true, Referenced: true,
	}}
	code, m := postRefresh(refreshHandler(f), `{"url":"https://e.com/a.list"}`)
	if code != 200 || f.got != "https://e.com/a.list" {
		t.Fatalf("%d %v got=%q", code, m, f.got)
	}
	// 前端 OpenboxRulesetRefreshResult 的五个字段都在,类型对得上
	if m["url"] != "https://e.com/a.list" || m["tag"] != "list-1234abcd" || m["total"] != float64(3) || m["needsRestart"] != true {
		t.Fatalf("%v", m)
	}
	c, _ := m["counts"].(map[string]any)
	if c["domain_suffix"] != float64(2) || c["ip_cidr"] != float64(1) {
		t.Fatalf("counts: %v", m["counts"])
	}
}

func TestRefreshErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"链接不合格", &singbox.RefreshInputError{Msg: "规则集链接只支持 http / https"}, 400},
		{"地址有问题", &rulelist.InputError{Msg: "地址不对"}, 400},
		{"对端拉取失败", &rulelist.FetchError{Msg: "连接超时"}, 502},
		{"二进制名单", rulelist.ErrBinary, 400},
		{"名单为空", rulelist.ErrEmpty, 400},
		{"没有 sing-box", singbox.ErrNoSingBox, 503},
		{"取消", context.Canceled, 503},
		{"超时", context.DeadlineExceeded, 503},
		{"编译失败", &singbox.RefreshBuildError{Msg: "编译规则集失败:boom"}, 500},
		{"别的错误", errors.New("x"), 500},
		{"包了一层的拉取失败", fmt.Errorf("外层: %w", &rulelist.FetchError{Msg: "连接超时"}), 502},
	}
	for _, c := range cases {
		code, m := postRefresh(refreshHandler(&fakeRefresher{err: c.err}), `{"url":"https://e.com/a.list"}`)
		if code != c.status || m["error"] == nil {
			t.Errorf("%s: %d %v", c.name, code, m)
		}
	}
	// 具体原因要带给用户
	_, m := postRefresh(refreshHandler(&fakeRefresher{err: &rulelist.FetchError{Msg: "连接超时"}}), `{"url":"https://e.com/a.list"}`)
	if m["error"] != "连接超时" {
		t.Fatalf("%v", m)
	}
}

func TestRefreshBadBodyAndMethod(t *testing.T) {
	f := &fakeRefresher{}
	h := refreshHandler(f)
	for _, body := range []string{"", "不是 JSON", `{"url":`, `{"url":"https://e.com/a"} 多余`} {
		if code, m := postRefresh(h, body); code != 400 || m["error"] == nil {
			t.Errorf("%q: %d %v", body, code, m)
		}
	}
	if code, _ := postRefresh(h, `{"url":"`+strings.Repeat("a", maxRefreshBody)+`"}`); code != 413 {
		t.Errorf("过大的请求体应 413: %d", code)
	}
	if f.got != "" {
		t.Fatalf("请求体不合格不该调用更新: %q", f.got)
	}
	if code, _ := get(h, "GET", "/api/openbox/rulesets/refresh"); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405: %d", code)
	}
}
