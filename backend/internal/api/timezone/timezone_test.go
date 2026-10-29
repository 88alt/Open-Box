package timezone

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tz "github.com/timeflysoon/Open-Box/backend/internal/timezone"
)

type fake struct {
	st   tz.State
	err  error
	zone string
}

func (f *fake) Get(context.Context) (tz.State, error) { return f.st, f.err }
func (f *fake) Set(_ context.Context, z string) (tz.State, error) {
	f.zone = z
	return f.st, f.err
}

func do(f *fake, method, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	Register(mux, Deps{Service: f})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/system/timezone", strings.NewReader(body)))
	return rec
}

func TestGetAndPut(t *testing.T) {
	f := &fake{st: tz.State{Zone: "Asia/Shanghai", Offset: "+08:00", Local: "2026-10-08 10:03:52", Platform: "openwrt", Zones: []string{"Asia/Shanghai"}, Countries: map[string]string{"Asia/Shanghai": "CN"}}}
	rec := do(f, http.MethodGet, "")
	var got map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got["zone"] != "Asia/Shanghai" || got["offset"] != "+08:00" || got["countries"] == nil || got["zones"] == nil || got["platform"] != "openwrt" {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	if rec := do(f, http.MethodPut, `{"zone":"Asia/Tokyo"}`); rec.Code != 200 || f.zone != "Asia/Tokyo" {
		t.Errorf("PUT: %d %s zone=%q", rec.Code, rec.Body, f.zone)
	}
}

func TestPutErrors(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{tz.ErrUnknownZone, 400}, {tz.ErrUnsupported, 501}, {errors.New("uci 写 zonename 失败(已恢复原来的时区)"), 500},
	}
	for _, c := range cases {
		rec := do(&fake{err: c.err}, http.MethodPut, `{"zone":"x"}`)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.err.Error()) {
			t.Errorf("%v 应 %d 且带原因: %d %s", c.err, c.code, rec.Code, rec.Body)
		}
	}
	f := &fake{}
	if rec := do(f, http.MethodPut, `{bad`); rec.Code != 400 || f.zone != "" {
		t.Errorf("坏 JSON 应 400 且不改: %d", rec.Code)
	}
	if rec := do(f, http.MethodPut, `{"zone":"`+strings.Repeat("a", 5000)+`"}`); rec.Code != 413 {
		t.Errorf("过大应 413: %d", rec.Code)
	}
	if rec := do(&fake{err: errors.New("x")}, http.MethodGet, ""); rec.Code != 500 {
		t.Errorf("GET 内部错误应 500: %d", rec.Code)
	}
	if rec := do(f, http.MethodDelete, ""); rec.Code != 405 {
		t.Errorf("DELETE 应 405: %d", rec.Code)
	}
}
