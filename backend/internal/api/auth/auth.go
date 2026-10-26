// Package authapi 实现 /api/auth/* 接口和保护其它 /api 接口的鉴权中间件。
//
// 响应格式以前端 panel/src/store/auth.ts 为准:
//   - 状态:{ enabled, authenticated, passwordSet };首次运行(没设过密码)为 {false, true, false}
//   - 没设密码时访问受保护接口:403 {"error":"PASSWORD_SETUP_REQUIRED"}
//   - 没登录:401 {"code":"ACCESS_PASSWORD_REQUIRED"}
//   - 密码错误:401 {"code":"ACCESS_PASSWORD_INVALID"};设置 / 修改密码的失败码放在 error 字段
package authapi

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/auth"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// CookieName 是会话 Cookie 的名字。
const CookieName = "openbox_session"

// 与前端 store/auth.ts 里的常量一一对应。
const (
	CodeAccessRequired = "ACCESS_PASSWORD_REQUIRED"
	CodeAccessInvalid  = "ACCESS_PASSWORD_INVALID"
	CodeAccessLocked   = "ACCESS_PASSWORD_LOCKED"
	CodeSetupRequired  = "PASSWORD_SETUP_REQUIRED"
	CodeAlreadySet     = "PASSWORD_ALREADY_SET"
	CodeTooShort       = "PASSWORD_TOO_SHORT"
	CodeTooLong        = "PASSWORD_TOO_LONG"
)

const maxBody = 4 << 10 // 登录 / 设置密码的请求体很小

// Deps 是本分组的依赖。
type Deps struct {
	Auth *auth.Manager
	Log  *slog.Logger
}

type handlers struct {
	auth *auth.Manager
	log  *slog.Logger
}

// Register 把 /api/auth/* 挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{auth: d.Auth, log: d.Log}
	if h.log == nil {
		h.log = slog.Default()
	}
	mux.HandleFunc("/api/auth/status", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.status}))
	mux.HandleFunc("/api/auth/setup", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.setup}))
	mux.HandleFunc("/api/auth/login", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.login}))
	mux.HandleFunc("/api/auth/logout", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.logout}))
	mux.HandleFunc("/api/auth/change-password", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.changePassword}))
}

type statusBody struct {
	OK            bool `json:"ok,omitempty"`
	Enabled       bool `json:"enabled"`
	Authenticated bool `json:"authenticated"`
	PasswordSet   bool `json:"passwordSet"`
}

func (h *handlers) currentStatus(r *http.Request) statusBody {
	if !h.auth.PasswordSet() {
		return statusBody{Enabled: false, Authenticated: true, PasswordSet: false}
	}
	return statusBody{Enabled: true, Authenticated: h.authenticated(r), PasswordSet: true}
}

func (h *handlers) authenticated(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && h.auth.Authenticated(c.Value)
}

func (h *handlers) status(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	httpx.JSON(w, http.StatusOK, h.currentStatus(r))
}

type passwordBody struct {
	Password string `json:"password"`
}

func (h *handlers) setup(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var body passwordBody
	if !httpx.DecodeJSON(w, r, &body, maxBody) {
		return
	}
	token, err := h.auth.Setup(body.Password)
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.log.Info("已设置访问密码", "client", clientKey(r))
	setSession(w, r, token)
	httpx.JSON(w, http.StatusOK, statusBody{OK: true, Enabled: true, Authenticated: true, PasswordSet: true})
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var body passwordBody
	if !httpx.DecodeJSON(w, r, &body, maxBody) {
		return
	}
	token, err := h.auth.Login(body.Password, clientKey(r))
	if err != nil {
		if errors.Is(err, auth.ErrInvalidPassword) {
			h.log.Warn("登录失败:密码错误", "client", clientKey(r))
		}
		h.writeError(w, err)
		return
	}
	setSession(w, r, token)
	httpx.JSON(w, http.StatusOK, statusBody{OK: true, Enabled: true, Authenticated: true, PasswordSet: true})
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r),
	})
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *handlers) changePassword(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !httpx.DecodeJSON(w, r, &body, maxBody) {
		return
	}
	// 修改密码要求先登录:没设密码 → 403;没会话 → 401
	if !h.auth.PasswordSet() {
		h.writeError(w, auth.ErrNotSetup)
		return
	}
	if !h.authenticated(r) {
		httpx.CodeError(w, http.StatusUnauthorized, CodeAccessRequired, "需要先登录")
		return
	}
	token, err := h.auth.ChangePassword(body.CurrentPassword, body.NewPassword, clientKey(r))
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.log.Info("已修改访问密码", "client", clientKey(r))
	setSession(w, r, token)
	httpx.JSON(w, http.StatusOK, statusBody{OK: true, Enabled: true, Authenticated: true, PasswordSet: true})
}

func (h *handlers) writeError(w http.ResponseWriter, err error) {
	var locked *auth.LockedError
	switch {
	case errors.Is(err, auth.ErrNotSetup):
		httpx.ErrorCode(w, http.StatusForbidden, CodeSetupRequired, "尚未设置访问密码,请先设置")
	case errors.Is(err, auth.ErrAlreadySet):
		httpx.ErrorCode(w, http.StatusConflict, CodeAlreadySet, "访问密码已经设置过了")
	case errors.Is(err, auth.ErrTooShort):
		httpx.ErrorCode(w, http.StatusBadRequest, CodeTooShort, "密码至少需要 4 个字符")
	case errors.Is(err, auth.ErrTooLong):
		httpx.ErrorCode(w, http.StatusBadRequest, CodeTooLong, "密码太长(最多 256 字节)")
	case errors.Is(err, auth.ErrInvalidPassword):
		httpx.CodeError(w, http.StatusUnauthorized, CodeAccessInvalid, "密码错误")
	case errors.As(err, &locked):
		secs := int(locked.RetryAfter.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		httpx.CodeError(w, http.StatusTooManyRequests, CodeAccessLocked, "登录尝试次数过多,请 "+strconv.Itoa(secs)+" 秒后再试")
	default:
		h.log.Error("鉴权接口内部错误", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "服务内部错误")
	}
}

// Require 保护所有 /api/ 下的接口(/api/auth/*、/api/health 除外);静态文件不受影响。
func Require(m *auth.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			if (p != "/api" && !strings.HasPrefix(p, "/api/")) || strings.HasPrefix(p, "/api/auth/") || p == "/api/health" {
				next.ServeHTTP(w, r)
				return
			}
			if !m.PasswordSet() {
				httpx.NoStore(w)
				httpx.ErrorCode(w, http.StatusForbidden, CodeSetupRequired, "尚未设置访问密码,请先设置")
				return
			}
			if c, err := r.Cookie(CookieName); err != nil || !m.Authenticated(c.Value) {
				httpx.NoStore(w)
				httpx.CodeError(w, http.StatusUnauthorized, CodeAccessRequired, "需要先登录")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func setSession(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: int(auth.SessionTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r),
	})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// clientKey 取连接对端 IP 用于限速。不信任 X-Forwarded-For:任何人都能伪造它来绕过限速。
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
