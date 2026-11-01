package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/update"
)

func newMux(t *testing.T, gh http.Handler) *http.ServeMux {
	t.Helper()
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	mux := http.NewServeMux()
	Register(mux, Deps{Service: &update.Service{Checker: &update.Checker{APIBase: srv.URL}, Version: "v0.1.1"}})
	return mux
}

func do(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestEndpoints(t *testing.T) {
	gh := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			_, _ = w.Write([]byte(`[{"tag_name":"v0.1.5"}]`))
		case strings.Contains(r.URL.Path, "/releases/tags/v0.1.5"):
			_, _ = w.Write([]byte(`{"tag_name":"v0.1.5","body":"说明"}`))
		default:
			w.WriteHeader(404)
		}
	})
	mux := newMux(t, gh)

	rec := do(mux, "GET", "/api/openbox/update/status", "")
	var st map[string]any
	json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") == "" || st["version"] != "v0.1.1" || st["status"].(map[string]any)["running"] != false || st["logTail"] != "" {
		t.Errorf("status: %d %s", rec.Code, rec.Body)
	}
	rec = do(mux, "GET", "/api/openbox/update/check", "")
	var chk update.CheckResult
	json.Unmarshal(rec.Body.Bytes(), &chk)
	if rec.Code != 200 || chk.Latest != "v0.1.5" || !chk.HasUpdate || chk.Current != "v0.1.1" {
		t.Errorf("check: %d %s", rec.Code, rec.Body)
	}
	rec = do(mux, "GET", "/api/openbox/update/notes?latest=v0.1.5", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"body":"说明"`) {
		t.Errorf("notes: %d %s", rec.Code, rec.Body)
	}
	rec = do(mux, "GET", "/api/openbox/update/notes?latest=v9.9.9", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"note":null`) || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("notes 取不到也回 200: %d %s", rec.Code, rec.Body)
	}
	if rec := do(mux, "POST", "/api/openbox/update/cancel", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"result":"not-running"`) {
		t.Errorf("cancel: %d %s", rec.Code, rec.Body)
	}
}

func TestCheckFailureIs502WithReason(t *testing.T) {
	mux := newMux(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	rec := do(mux, "GET", "/api/openbox/update/check", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "检查更新失败") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestRun(t *testing.T) {
	mux := newMux(t, http.NotFoundHandler())
	if rec := do(mux, "POST", "/api/openbox/update/run", `{"channel":"mirror"}`); rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "还没有实现") {
		t.Errorf("run: %d %s", rec.Code, rec.Body)
	}
	if rec := do(mux, "POST", "/api/openbox/update/run", `{"channel":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("通道不对回 400: %d", rec.Code)
	}
	if rec := do(mux, "POST", "/api/openbox/update/run", `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("坏请求体回 400: %d", rec.Code)
	}
	for _, c := range [][2]string{{"POST", "/api/openbox/update/status"}, {"GET", "/api/openbox/update/run"}, {"POST", "/api/openbox/update/check"}} {
		if rec := do(mux, c[0], c[1], ""); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%v: %d", c, rec.Code)
		}
	}
}
