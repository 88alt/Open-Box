package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	authapi "github.com/timeflysoon/Open-Box/backend/internal/api/auth"
	"github.com/timeflysoon/Open-Box/backend/internal/auth"
	"github.com/timeflysoon/Open-Box/backend/internal/controllerproxy"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// 控制接口等于内核的完全控制权:两个前缀必须在登录保护之内,没登录的请求到不了内核。
func TestBothPrefixesRequireLogin(t *testing.T) {
	var hits int32
	kernel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer kernel.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := auth.NewManager(st, auth.WithIterations(1000))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, controllerproxy.New(func() (controllerproxy.Target, error) {
		return controllerproxy.Target{Addr: strings.TrimPrefix(kernel.URL, "http://")}, nil
	}))
	srv := httptest.NewServer(authapi.Require(m)(mux))
	defer srv.Close()

	get := func(path, cookie string) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: authapi.CookieName, Value: cookie})
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	paths := []string{"/api/controller/proxies", "/api/controller", "/api/controller-ws/traffic"}
	// 还没设访问密码
	for _, p := range paths {
		if code := get(p, ""); code != 403 {
			t.Errorf("没设密码时 %s 应 403: %d", p, code)
		}
	}
	token, err := m.Setup("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if code := get(p, ""); code != 401 {
			t.Errorf("没登录 %s 应 401: %d", p, code)
		}
		if code := get(p, "伪造的会话"); code != 401 {
			t.Errorf("伪造会话 %s 应 401: %d", p, code)
		}
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("没登录的请求不该到达内核,实际 %d 次", hits)
	}
	if code := get("/api/controller/proxies", token); code != 200 || atomic.LoadInt32(&hits) != 1 {
		t.Errorf("登录后应通: %d hits=%d", code, hits)
	}
}
