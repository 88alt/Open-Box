package sysres

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/sysres"
)

func newMux(t *testing.T, procDir string) *http.ServeMux {
	mux := http.NewServeMux()
	Register(mux, Deps{Service: &sysres.Service{ProcDir: procDir}})
	return mux
}

func TestGetResources(t *testing.T) {
	dir := t.TempDir()
	must := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("stat", "cpu  1 0 1 8 0 0 0 0 0 0\ncpu0 1 0 1 8 0 0 0 0 0 0\n")
	must("meminfo", "MemTotal: 1000 kB\nMemAvailable: 400 kB\n")
	rec := httptest.NewRecorder()
	newMux(t, dir).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/system/resources", nil))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") == "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	cpu, _ := got["cpu"].(map[string]any)
	mem, _ := got["memory"].(map[string]any)
	if _, ok := cpu["cores"].([]any); !ok || mem["total"] != float64(1000*1024) || got["at"] == nil {
		t.Fatalf("%s", rec.Body.String())
	}
	if _, has := got["activeTerminals"]; has {
		t.Fatal("没接连接来源时不该有 activeTerminals")
	}
}

func TestGetResourcesUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux(t, t.TempDir()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openbox/system/resources", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	newMux(t, t.TempDir()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/openbox/system/resources", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
}
