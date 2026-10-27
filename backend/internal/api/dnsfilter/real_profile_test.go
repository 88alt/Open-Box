package dnsfilter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

type noCore struct{}

func (noCore) MigrateRouting(context.Context, map[string]any) (map[string]any, bool, error) {
	return nil, false, nil
}
func (noCore) NormalizeGroups(_ context.Context, g []any) ([]any, error) { return g, nil }

// 用真实的档案服务和存储走一遍:保存后重新打开仍在,档案里的别的 dns 设置不受影响,
// 再存一次会整体替换名单和白名单(不会和旧的合并出残留)。
func TestRealProfilePersistsAndKeepsOtherDNSSettings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	open := func() (*profile.Service, http.Handler) {
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		ps := profile.New(st, noCore{}, nil)
		mux := http.NewServeMux()
		Register(mux, Deps{Profiles: ps})
		return ps, mux
	}
	do := func(h http.Handler, method, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/dns-filter", strings.NewReader(body)))
		return rec
	}

	ps, h := open()
	before, err := ps.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proxyBefore := before["dns"].(map[string]any)["proxy"]

	first := `{"enabled":true,"lists":[{"id":"a","name":"A","url":"https://a.com/x","enabled":true},{"id":"b","name":"B","url":"https://b.com/y","enabled":true}],"allowDomains":["x.com","y.com"],"autoUpdate":{"enabled":true,"days":3,"hour":2}}`
	if rec := do(h, http.MethodPut, first); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	second := `{"enabled":true,"lists":[{"id":"b","name":"B","url":"https://b.com/y","enabled":false}],"allowDomains":["z.com"],"autoUpdate":{"enabled":false,"days":1,"hour":4}}`
	if rec := do(h, http.MethodPut, second); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}

	ps2, h2 := open() // 重新打开存储,模拟重启
	after, err := ps2.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after["dns"].(map[string]any)["proxy"] != proxyBefore {
		t.Fatal("保存过滤设置不该动别的 dns 设置")
	}
	rec := do(h2, http.MethodGet, "")
	body := rec.Body.String()
	for _, want := range []string{`"id":"b"`, `"enabled":false`, `"z.com"`} {
		if !strings.Contains(body, want) {
			t.Errorf("缺 %s:%s", want, body)
		}
	}
	for _, bad := range []string{`"id":"a"`, `"x.com"`, `"y.com"`} {
		if strings.Contains(body, bad) {
			t.Errorf("不该残留 %s:%s", bad, body)
		}
	}
}
