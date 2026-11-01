package backup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/backup"
	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

type fake struct {
	got backup.Options
	b   *backup.Backup
	err error
}

func (f *fake) Export(_ context.Context, o backup.Options) (*backup.Backup, error) {
	f.got = o
	return f.b, f.err
}

func do(f *fake, method, target string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, Deps{Exporter: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestQueryParsing(t *testing.T) {
	cases := []struct {
		q    string
		want backup.Options
	}{
		{"subscriptions=1&chainProxies=1&clientRoutes=1&servers=1", backup.Options{Subscriptions: true, ChainProxies: true, ClientRoutes: true, Servers: true}},
		{"subscriptions=0&chainProxies=0&clientRoutes=0&servers=0", backup.Options{}},
		{"", backup.Options{ChainProxies: true}}, // 没传:链式代理按带上算,其它不带
		{"subscriptions=true&clientRoutes=TRUE&servers=false&chainProxies=false", backup.Options{Subscriptions: true, ClientRoutes: true}},
		{"subscriptions=yes&chainProxies=maybe", backup.Options{ChainProxies: true}}, // 认不得的取值按默认
	}
	for _, c := range cases {
		f := &fake{b: &backup.Backup{Format: backup.Format}}
		rec := do(f, http.MethodGet, "/api/openbox/backup?"+c.q)
		if rec.Code != 200 || f.got != c.want {
			t.Errorf("%q → %d %+v,想要 %+v", c.q, rec.Code, f.got, c.want)
		}
	}
}

func TestExportResponseAndErrors(t *testing.T) {
	f := &fake{b: &backup.Backup{Format: backup.Format, Version: 1, Profile: map[string]any{"region": "cn"}, Groups: []any{}, PanelSettings: map[string]string{}}}
	rec := do(f, http.MethodGet, "/api/openbox/backup")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"format":"open-box-backup"`) || rec.Header().Get("Cache-Control") == "" {
		t.Fatal(rec.Code, rec.Header(), rec.Body)
	}
	f.err = errors.New("读取档案失败: x")
	rec = do(f, http.MethodGet, "/api/openbox/backup")
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "读取档案失败") {
		t.Fatal(rec.Code, rec.Body)
	}
}

type fakeImporter struct {
	f    *backup.ImportFile
	mode subs.RestoreMode
	res  *backup.ImportResult
	err  error
}

func (i *fakeImporter) Import(_ context.Context, f *backup.ImportFile, m subs.RestoreMode) (*backup.ImportResult, error) {
	i.f, i.mode = f, m
	return i.res, i.err
}

func post(i Importer, target, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, Deps{Exporter: &fake{}, Importer: i})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)))
	return rec
}

const goodFile = `{"format":"open-box-backup","version":1,"profile":{"region":"cn"},"groups":[],"subscriptions":[{"id":"a","name":"A","enabled":true}],"nodes":[{"subscription":"a","tag":"n1","type":"ss","server":"s","server_port":1,"fields":{}}],"panelSettings":{"config/x":"1"},"backgroundImage":""}`

func TestImportWithoutImporterIs501AndMethods(t *testing.T) {
	if rec := do(&fake{}, http.MethodPost, "/api/openbox/backup/import?subscriptions=replace"); rec.Code != 501 {
		t.Fatal(rec.Code, rec.Body)
	}
	if rec := do(&fake{}, http.MethodPost, "/api/openbox/backup"); rec.Code != 405 {
		t.Fatal(rec.Code)
	}
	if rec := do(&fake{}, http.MethodGet, "/api/openbox/backup/import"); rec.Code != 405 {
		t.Fatal(rec.Code)
	}
}

func TestImportPassesFileAndModeAndReturnsResult(t *testing.T) {
	imp := &fakeImporter{res: &backup.ImportResult{OK: true, Imported: backup.Imported{Profile: true, Groups: 2, Subscriptions: 1, Nodes: 1}}}
	rec := post(imp, "/api/openbox/backup/import?subscriptions=append", goodFile)
	if rec.Code != 200 || imp.mode != subs.RestoreAppend {
		t.Fatal(rec.Code, imp.mode, rec.Body)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Fatal("导入响应应是 no-store")
	}
	if imp.f.Format != backup.Format || imp.f.Profile["region"] != "cn" || imp.f.Groups == nil ||
		imp.f.Subscriptions == nil || (*imp.f.Subscriptions)[0].ID != "a" || (*imp.f.Nodes)[0].Subscription != "a" || (*imp.f.Nodes)[0].Tag != "n1" ||
		imp.f.PanelSettings["config/x"] != "1" || imp.f.BackgroundImage == nil {
		t.Fatalf("文件没有原样交给导入: %+v", imp.f)
	}
	// 前端读的形状
	for _, want := range []string{`"ok":true`, `"imported":{`, `"profile":true`, `"groups":2`, `"subscriptions":1`, `"nodes":1`, `"subscriptionsMode":null`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("响应里应有 %s:%s", want, rec.Body)
		}
	}
	// 不传方式 = replace
	imp = &fakeImporter{res: &backup.ImportResult{OK: true}}
	if rec := post(imp, "/api/openbox/backup/import", goodFile); rec.Code != 200 || imp.mode != subs.RestoreReplace {
		t.Fatal(rec.Code, imp.mode)
	}
}

func TestImportErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		sub  string
	}{
		{"文件有问题", &backup.InputError{Msg: "档案:端口不对"}, 400, "档案:端口不对"},
		{"忙", backup.ErrBusy, 409, "已有一次导入"},
		{"写了一半", &backup.PartialError{Step: "节点组", Done: []string{"档案"}, Err: errors.New("磁盘满了")}, 500, "已经写入:档案"},
		{"核心不可用", nodegroups.ErrCoreUnavailable, 503, "openbox-core"},
		{"被包了一层的 InputError", errors.Join(errors.New("x"), &backup.InputError{Msg: "坏文件"}), 400, "坏文件"},
		{"其它", errors.New("读取现有节点失败"), 500, "读取现有节点失败"},
	}
	for _, c := range cases {
		rec := post(&fakeImporter{err: c.err}, "/api/openbox/backup/import", goodFile)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.sub) {
			t.Errorf("%s: %d %s,想要 %d 含 %q", c.name, rec.Code, rec.Body, c.code, c.sub)
		}
	}
}

func TestImportBadRequests(t *testing.T) {
	imp := &fakeImporter{res: &backup.ImportResult{OK: true}}
	for name, c := range map[string]struct {
		target, body string
		code         int
		sub          string
	}{
		"方式不认识":   {"/api/openbox/backup/import?subscriptions=merge", goodFile, 400, "replace 或 append"},
		"不是 JSON": {"/api/openbox/backup/import", `{"format":`, 400, "不是有效的 JSON"},
		"后面还有内容":  {"/api/openbox/backup/import", goodFile + `{}`, 400, "不是有效的 JSON"},
		"类型不对":    {"/api/openbox/backup/import", `{"format":"open-box-backup","version":1,"profile":[],"groups":[]}`, 400, "profile"},
		"订阅类型不对":  {"/api/openbox/backup/import", `{"format":"open-box-backup","version":1,"profile":{},"groups":[],"subscriptions":[{"id":1}]}`, 400, "类型不对"},
		"空体":      {"/api/openbox/backup/import", ``, 400, "不是有效的 JSON"},
		"太大":      {"/api/openbox/backup/import", `{"x":"` + strings.Repeat("a", maxImportBody) + `"}`, 413, "太大"},
	} {
		rec := post(imp, c.target, c.body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.sub) {
			t.Errorf("%s: %d %s,想要 %d 含 %q", name, rec.Code, rec.Body, c.code, c.sub)
		}
	}
	if imp.f != nil {
		t.Fatal("请求有问题时不该调到导入")
	}
}
