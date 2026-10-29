package diagnostics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fake struct{}

func (fake) Collect(context.Context) map[string]any {
	return map[string]any{"backend": map[string]any{"version": "v1"}}
}

func TestGetAndMethods(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, Deps{Service: fake{}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/diagnostics", nil))
	var got map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got["backend"] == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") == "" {
		t.Error("诊断包不该被缓存")
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/diagnostics", nil))
	if rec.Code != 405 {
		t.Errorf("POST 应 405: %d", rec.Code)
	}
}
