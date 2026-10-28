package clients

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

func call(t *testing.T, d Deps, method string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/clients", strings.NewReader("")))
	return rec
}

func TestListShape(t *testing.T) {
	d := Deps{List: func() []system.Client {
		return []system.Client{
			{IP: "192.168.1.5", Name: "", MAC: "aa:bb:cc:dd:ee:02"},
			{IP: "192.168.1.20", Name: "phone"},
		}
	}}
	rec := call(t, d, http.MethodGet)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q", cc)
	}
	var got struct {
		Clients []map[string]any `json:"clients"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("%s", rec.Body)
	}
	// 没有主机名时 name 仍然在(空串);没有 MAC 时不带 mac 字段
	if got.Clients[0]["name"] != "" || got.Clients[0]["mac"] != "aa:bb:cc:dd:ee:02" {
		t.Errorf("第一条: %v", got.Clients[0])
	}
	if _, has := got.Clients[1]["mac"]; has || got.Clients[1]["name"] != "phone" {
		t.Errorf("第二条: %v", got.Clients[1])
	}
}

func TestListEmptyIsArrayNotNull(t *testing.T) {
	for name, d := range map[string]Deps{
		"没接依赖":   {},
		"返回 nil": {List: func() []system.Client { return nil }},
	} {
		rec := call(t, d, http.MethodGet)
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"clients":[]}` {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestListRejectsOtherMethods(t *testing.T) {
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if rec := call(t, Deps{}, m); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
}
