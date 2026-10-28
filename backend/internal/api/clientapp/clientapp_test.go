package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/clientapp"
)

type fake struct {
	info clientapp.Info
	err  error
}

func (f fake) Info(context.Context) (clientapp.Info, error) { return f.info, f.err }

func call(t *testing.T, d Deps, method string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/openbox/client-app/info", strings.NewReader("")))
	return rec
}

func TestInfoOK(t *testing.T) {
	rec := call(t, Deps{Service: fake{info: clientapp.Info{
		RouterID: "0123456789abcdef0123456789abcdef", RouterName: "Open-Box", PanelPort: 3036,
		LanAddresses: []string{"192.168.5.1"}, LanSubnets: []string{"192.168.5.0/24"},
		ShareRegions: clientapp.DefaultShareRegions(), DefaultShareRegions: clientapp.DefaultShareRegions(),
	}}}, http.MethodGet)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q", cc)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["routerId"] != "0123456789abcdef0123456789abcdef" || m["panelPort"] != float64(3036) {
		t.Errorf("%s", rec.Body)
	}
	if v, ok := m["egressCountry"]; !ok || v != nil {
		t.Errorf("egressCountry 应在且为 null: %s", rec.Body)
	}
}

func TestInfoErrors(t *testing.T) {
	if rec := call(t, Deps{Service: fake{err: errors.New("磁盘满了")}}, http.MethodGet); rec.Code != 500 || !strings.Contains(rec.Body.String(), "磁盘满了") {
		t.Errorf("服务出错: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, Deps{}, http.MethodGet); rec.Code != 503 {
		t.Errorf("没接服务: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, Deps{Service: fake{}}, http.MethodPost); rec.Code != 405 {
		t.Errorf("POST: %d", rec.Code)
	}
}
