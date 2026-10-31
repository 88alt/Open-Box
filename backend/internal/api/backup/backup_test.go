package backup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/backup"
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

func TestImportNotReadyAndMethods(t *testing.T) {
	f := &fake{}
	if rec := do(f, http.MethodPost, "/api/openbox/backup/import?subscriptions=replace"); rec.Code != 501 || !strings.Contains(rec.Body.String(), "导入备份还没实现") {
		t.Fatal(rec.Code, rec.Body)
	}
	if rec := do(f, http.MethodPost, "/api/openbox/backup"); rec.Code != 405 {
		t.Fatal(rec.Code)
	}
	if rec := do(f, http.MethodGet, "/api/openbox/backup/import"); rec.Code != 405 {
		t.Fatal(rec.Code)
	}
}
