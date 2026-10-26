package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "assets"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>INDEX</html>"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "assets", "app-abc123.js"), []byte("console.log(1)"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte("<svg/>"), 0o644))
	return dir
}

func get(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestStaticServing(t *testing.T) {
	h := StaticHandler(writeSite(t))

	cases := []struct {
		name, method, target string
		status               int
		bodyHas, cache       string
	}{
		{"根路径", "GET", "/", 200, "INDEX", "no-cache"},
		{"index.html", "GET", "/index.html", 200, "INDEX", "no-cache"},
		{"带哈希资源长缓存", "GET", "/assets/app-abc123.js", 200, "console.log", "public, max-age=31536000, immutable"},
		{"根目录文件", "GET", "/favicon.svg", 200, "<svg/>", "no-cache"},
		{"前端路由回落", "GET", "/dashboard/nodes", 200, "INDEX", "no-cache"},
		{"缺失资源 404", "GET", "/assets/missing.js", 404, "", ""},
		{"缺失带扩展名 404", "GET", "/nope.png", 404, "", ""},
		{"目录回落 index", "GET", "/assets", 200, "INDEX", "no-cache"},
		{"路径穿越被规整", "GET", "/../../etc/passwd", 200, "INDEX", "no-cache"},
		{"HEAD", "HEAD", "/", 200, "", "no-cache"},
		{"POST 不允许", "POST", "/", 405, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := get(h, c.method, c.target)
			if rec.Code != c.status {
				t.Fatalf("状态 %d, 期望 %d (%s)", rec.Code, c.status, rec.Body)
			}
			if c.bodyHas != "" && !strings.Contains(rec.Body.String(), c.bodyHas) {
				t.Fatalf("内容 %q 不含 %q", rec.Body, c.bodyHas)
			}
			if c.cache != "" && rec.Header().Get("Cache-Control") != c.cache {
				t.Fatalf("Cache-Control = %q, 期望 %q", rec.Header().Get("Cache-Control"), c.cache)
			}
		})
	}
}

func TestStaticMissingIndex(t *testing.T) {
	h := StaticHandler(t.TempDir())
	rec := get(h, "GET", "/")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "--web-dir") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestStaticMountedBesideAPI(t *testing.T) {
	s := New(Options{Logger: quietLogger()})
	s.Mux().Handle("/", StaticHandler(writeSite(t)))
	if rec := get(s.Handler(), "GET", "/some/route"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "INDEX") {
		t.Fatalf("%d", rec.Code)
	}
	// /api 下的未知路径必须仍是 JSON 404,不能被回落成 index.html
	rec := get(s.Handler(), "GET", "/api/unknown")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
