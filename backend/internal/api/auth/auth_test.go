package authapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/auth"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

type env struct {
	t   *testing.T
	srv *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := auth.NewManager(st, auth.WithIterations(1000))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, Deps{Auth: m, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	mux.HandleFunc("/api/private", func(w http.ResponseWriter, _ *http.Request) { httpx.JSON(w, 200, map[string]bool{"ok": true}) })
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) { httpx.JSON(w, 200, map[string]bool{"ok": true}) })
	mux.HandleFunc("/index.html", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("page")) })
	srv := httptest.NewServer(Require(m)(mux))
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv}
}

func (e *env) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

type result struct {
	status int
	body   map[string]any
	resp   *http.Response
}

func (e *env) do(c *http.Client, method, path, body string) result {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return result{status: resp.StatusCode, body: m, resp: resp}
}

func (r result) want(t *testing.T, status int, key, val string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("状态 %d, 期望 %d, 响应 %v", r.status, status, r.body)
	}
	if key != "" && r.body[key] != val {
		t.Fatalf("%s = %v, 期望 %q (响应 %v)", key, r.body[key], val, r.body)
	}
}

func (r result) wantStatus(t *testing.T, enabled, authed, set bool) {
	t.Helper()
	if r.status != 200 || r.body["enabled"] != enabled || r.body["authenticated"] != authed || r.body["passwordSet"] != set {
		t.Fatalf("状态不符: %d %v, 期望 enabled=%v authenticated=%v passwordSet=%v", r.status, r.body, enabled, authed, set)
	}
}

func TestFullFlow(t *testing.T) {
	e := newEnv(t)
	c := e.client()

	// 首次运行:没设密码
	e.do(c, "GET", "/api/auth/status", "").wantStatus(t, false, true, false)
	e.do(c, "GET", "/api/private", "").want(t, 403, "error", "PASSWORD_SETUP_REQUIRED")
	e.do(c, "POST", "/api/auth/login", `{"password":"abcd"}`).want(t, 403, "error", "PASSWORD_SETUP_REQUIRED")
	e.do(c, "POST", "/api/auth/change-password", `{"currentPassword":"a","newPassword":"bbbb"}`).want(t, 403, "error", "PASSWORD_SETUP_REQUIRED")
	if r := e.do(c, "GET", "/index.html", ""); r.resp.StatusCode != 200 {
		t.Fatal("静态页面不应被鉴权拦截")
	}

	// 设置密码
	e.do(c, "POST", "/api/auth/setup", `{"password":"abc"}`).want(t, 400, "error", "PASSWORD_TOO_SHORT")
	e.do(c, "POST", "/api/auth/setup", `not json`).want(t, 400, "error", "请求体不是有效的 JSON")
	r := e.do(c, "POST", "/api/auth/setup", `{"password":"secret-1"}`)
	r.wantStatus(t, true, true, true)
	var session *http.Cookie
	for _, ck := range r.resp.Cookies() {
		if ck.Name == CookieName {
			session = ck
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.Path != "/" || session.MaxAge <= 0 {
		t.Fatalf("会话 Cookie 属性不对: %+v", session)
	}
	if session.Secure {
		t.Fatal("HTTP 访问时不应带 Secure(浏览器会丢掉 Cookie)")
	}
	e.do(c, "POST", "/api/auth/setup", `{"password":"another"}`).want(t, 409, "error", "PASSWORD_ALREADY_SET")

	// 已登录
	e.do(c, "GET", "/api/private", "").want(t, 200, "", "")
	e.do(c, "GET", "/api/auth/status", "").wantStatus(t, true, true, true)

	// 另一个浏览器:未登录
	other := e.client()
	e.do(other, "GET", "/api/auth/status", "").wantStatus(t, true, false, true)
	e.do(other, "GET", "/api/private", "").want(t, 401, "code", "ACCESS_PASSWORD_REQUIRED")
	e.do(other, "GET", "/api/health", "").want(t, 200, "", "")
	e.do(other, "POST", "/api/auth/login", `{"password":"nope"}`).want(t, 401, "code", "ACCESS_PASSWORD_INVALID")
	e.do(other, "GET", "/api/private", "").want(t, 401, "code", "ACCESS_PASSWORD_REQUIRED")
	e.do(other, "POST", "/api/auth/login", `{"password":"secret-1"}`).wantStatus(t, true, true, true)
	e.do(other, "GET", "/api/private", "").want(t, 200, "", "")

	// 修改密码:必须先登录,必须给对当前密码
	e.do(e.client(), "POST", "/api/auth/change-password", `{"currentPassword":"secret-1","newPassword":"newpass1"}`).want(t, 401, "code", "ACCESS_PASSWORD_REQUIRED")
	e.do(c, "POST", "/api/auth/change-password", `{"currentPassword":"bad","newPassword":"newpass1"}`).want(t, 401, "code", "ACCESS_PASSWORD_INVALID")
	e.do(c, "POST", "/api/auth/change-password", `{"currentPassword":"secret-1","newPassword":"abc"}`).want(t, 400, "error", "PASSWORD_TOO_SHORT")
	e.do(c, "POST", "/api/auth/change-password", `{"currentPassword":"secret-1","newPassword":"newpass1"}`).wantStatus(t, true, true, true)
	// 本机会话因重发 Cookie 保持有效,另一个浏览器的会话失效
	e.do(c, "GET", "/api/private", "").want(t, 200, "", "")
	e.do(other, "GET", "/api/private", "").want(t, 401, "code", "ACCESS_PASSWORD_REQUIRED")
	e.do(e.client(), "POST", "/api/auth/login", `{"password":"secret-1"}`).want(t, 401, "code", "ACCESS_PASSWORD_INVALID")
	e.do(other, "POST", "/api/auth/login", `{"password":"newpass1"}`).wantStatus(t, true, true, true)

	// 登出
	lo := e.do(c, "POST", "/api/auth/logout", "")
	lo.want(t, 200, "", "")
	cleared := false
	for _, ck := range lo.resp.Cookies() {
		if ck.Name == CookieName && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("登出应清除 Cookie")
	}
	e.do(c, "GET", "/api/private", "").want(t, 401, "code", "ACCESS_PASSWORD_REQUIRED")
	e.do(c, "GET", "/api/auth/status", "").wantStatus(t, true, false, true)
}

func TestLoginRateLimitOverHTTP(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	e.do(c, "POST", "/api/auth/setup", `{"password":"secret-1"}`)
	other := e.client()
	for i := 0; i < 5; i++ {
		e.do(other, "POST", "/api/auth/login", `{"password":"bad"}`).want(t, 401, "code", "ACCESS_PASSWORD_INVALID")
	}
	r := e.do(other, "POST", "/api/auth/login", `{"password":"secret-1"}`)
	r.want(t, 429, "code", "ACCESS_PASSWORD_LOCKED")
	if r.resp.Header.Get("Retry-After") == "" || !strings.Contains(r.body["message"].(string), "秒") {
		t.Fatalf("429 应带 Retry-After 和中文提示: %v", r.body)
	}
}

func TestSecureCookieBehindHTTPSProxy(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/auth/setup", strings.NewReader(`{"password":"secret-1"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var secure bool
	for _, ck := range resp.Cookies() {
		secure = secure || (ck.Name == CookieName && ck.Secure)
	}
	if !secure {
		t.Fatal("经 HTTPS 反代访问时 Cookie 应带 Secure")
	}
}

func TestMethodsAndResponseHeaders(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	e.do(c, "GET", "/api/auth/login", "").want(t, 405, "", "")
	e.do(c, "GET", "/api/auth/setup", "").want(t, 405, "", "")
	e.do(c, "POST", "/api/auth/status", "").want(t, 405, "", "")
	if cc := e.do(c, "GET", "/api/auth/status", "").resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("鉴权响应不应被缓存: %q", cc)
	}
}

func TestOversizedBodyRejected(t *testing.T) {
	e := newEnv(t)
	big := `{"password":"` + strings.Repeat("x", 10_000) + `"}`
	e.do(e.client(), "POST", "/api/auth/setup", big).want(t, 413, "", "")
}
