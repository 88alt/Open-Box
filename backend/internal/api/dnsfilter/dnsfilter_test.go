package dnsfilter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/profile"
)

// fakeProfiles 按真实 Service 的行为做深度合并:这里只需要 dns.filter 一层整体替换。
type fakeProfiles struct {
	p        map[string]any
	getErr   error
	patchErr error
	patches  []map[string]any
}

func (f *fakeProfiles) Get(context.Context) (map[string]any, error) { return f.p, f.getErr }
func (f *fakeProfiles) Patch(_ context.Context, patch map[string]any) (map[string]any, error) {
	if f.patchErr != nil {
		return nil, f.patchErr
	}
	f.patches = append(f.patches, patch)
	dns, _ := f.p["dns"].(map[string]any)
	if dns == nil {
		dns = map[string]any{}
		f.p["dns"] = dns
	}
	dns["filter"] = patch["dns"].(map[string]any)["filter"]
	return f.p, nil
}

func serve(t *testing.T, f *fakeProfiles, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, Deps{Profiles: f})
	req := httptest.NewRequest(method, "/api/openbox/dns-filter", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestGetDefaultsWhenProfileHasNoFilter(t *testing.T) {
	rec := serve(t, &fakeProfiles{p: map[string]any{}}, http.MethodGet, "")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	set := st["settings"].(map[string]any)
	if set["enabled"] != false || len(set["lists"].([]any)) != 0 || len(set["allowDomains"].([]any)) != 0 {
		t.Fatalf("%v", set)
	}
	if st["applied"] != nil || st["pending"] != false || st["connected"] != false || st["busy"] != false {
		t.Fatalf("%v", st)
	}
	if _, ok := st["lists"].(map[string]any); !ok {
		t.Fatalf("lists 要是对象:%v", st["lists"])
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("应该带 no-store")
	}
}

func TestPutSavesOnlyDnsFilter(t *testing.T) {
	f := &fakeProfiles{p: map[string]any{"dns": map[string]any{"mode": "hijack"}}}
	body := `{"enabled":true,"lists":[{"id":"a","name":" N ","url":"https://a.com/x","enabled":true}],"allowDomains":["B.com","b.com"],"autoUpdate":{"enabled":false,"days":1,"hour":4}}`
	rec := serve(t, f, http.MethodPut, body)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	if len(f.patches) != 1 || len(f.patches[0]) != 1 {
		t.Fatalf("补丁应只含 dns:%v", f.patches)
	}
	dns := f.patches[0]["dns"].(map[string]any)
	if len(dns) != 1 {
		t.Fatalf("补丁的 dns 里应只有 filter:%v", dns)
	}
	filter := dns["filter"].(map[string]any)
	if filter["enabled"] != true {
		t.Fatalf("%v", filter)
	}
	if allow := filter["allowDomains"].([]any); len(allow) != 1 || allow[0] != "b.com" {
		t.Fatalf("白名单没规整去重:%v", allow)
	}
	if name := filter["lists"].([]any)[0].(map[string]any)["name"]; name != "N" {
		t.Fatalf("名称没去空白:%v", name)
	}
	// 再读一次,拿到刚存的
	rec = serve(t, f, http.MethodGet, "")
	var st struct {
		Settings struct {
			Enabled bool `json:"enabled"`
		} `json:"settings"`
		Pending bool `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || !st.Settings.Enabled || !st.Pending {
		t.Fatalf("%v %s", err, rec.Body)
	}
}

func TestPutRejectsBadInputWithoutPatching(t *testing.T) {
	for _, body := range []string{``, `[`, `{"allowDomains":["1.2.3.4"]}`, `{"lists":[{"id":"","name":"n","url":"https://a.com/"}]}`} {
		f := &fakeProfiles{p: map[string]any{}}
		rec := serve(t, f, http.MethodPut, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q:状态 %d,想要 400", body, rec.Code)
		}
		if len(f.patches) != 0 {
			t.Errorf("%q:校验没过不该写档案", body)
		}
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error == "" {
			t.Errorf("%q:错误体要带 error:%s", body, rec.Body)
		}
	}
}

func TestPutTooLarge(t *testing.T) {
	big := `{"allowDomains":["` + strings.Repeat("a", maxBody) + `"]}`
	if rec := serve(t, &fakeProfiles{p: map[string]any{}}, http.MethodPut, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatal(rec.Code)
	}
}

func TestPutProfileErrors(t *testing.T) {
	ok := `{"enabled":false}`
	rec := serve(t, &fakeProfiles{p: map[string]any{}, patchErr: &profile.ValidationError{Msg: "档案不合法"}}, http.MethodPut, ok)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "档案不合法") {
		t.Fatal(rec.Code, rec.Body)
	}
	rec = serve(t, &fakeProfiles{p: map[string]any{}, patchErr: errors.New("磁盘满了")}, http.MethodPut, ok)
	if rec.Code != http.StatusInternalServerError {
		t.Fatal(rec.Code)
	}
	rec = serve(t, &fakeProfiles{getErr: errors.New("坏了")}, http.MethodGet, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatal(rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	if rec := serve(t, &fakeProfiles{p: map[string]any{}}, http.MethodDelete, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatal(rec.Code)
	}
}
