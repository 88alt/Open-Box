package dns

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
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
