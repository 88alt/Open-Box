// Package httpx 放各接口分组共用的 HTTP 小工具:JSON 响应、统一错误格式、请求体读取。
//
// 错误格式以前端 requestJson / extractErrorMessage 为准:失败返回非 2xx,
// 响应体里 `error`(优先)或 `message` 是给人看的中文信息。
// 鉴权相关接口另有约定(见 CodeError / ErrorCode)。
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
)

// JSON 写出 JSON 响应。
func JSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status, body = http.StatusInternalServerError, []byte(`{"error":"响应编码失败"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// Error 写出统一错误响应 {"error":"..."}。
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

// CodeError 写出带机器码的错误:{"code":"X","error":"信息","message":"信息"}。
// 前端据 code 判断(例如 401 + ACCESS_PASSWORD_REQUIRED),据 error / message 展示。
func CodeError(w http.ResponseWriter, status int, code, msg string) {
	JSON(w, status, map[string]string{"code": code, "error": msg, "message": msg})
}

// ErrorCode 写出把机器码放在 error 字段的错误:{"error":"X","message":"信息"}。
// 前端的密码设置流程(PASSWORD_ALREADY_SET、PASSWORD_TOO_SHORT、PASSWORD_SETUP_REQUIRED)用这种格式。
func ErrorCode(w http.ResponseWriter, status int, code, msg string) {
	JSON(w, status, map[string]string{"error": code, "message": msg})
}

// NotFound 是 /api/ 下未知路径的统一响应。
func NotFound(w http.ResponseWriter, _ *http.Request) {
	Error(w, http.StatusNotFound, "接口不存在")
}

// Methods 按 HTTP 方法分发;其它方法返回 JSON 格式的 405(标准库默认返回纯文本)。
// 注册 HEAD 以外的方法时,GET 会自动兼容 HEAD。
func Methods(m map[string]http.HandlerFunc) http.HandlerFunc {
	allow := make([]string, 0, len(m)+1)
	for k := range m {
		allow = append(allow, k)
	}
	if _, ok := m[http.MethodGet]; ok {
		if _, has := m[http.MethodHead]; !has {
			allow = append(allow, http.MethodHead)
		}
	}
	sort.Strings(allow)
	allowHeader := strings.Join(allow, ", ")
	return func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		h, ok := m[method]
		if !ok && method == http.MethodHead {
			h, ok = m[http.MethodGet]
		}
		if !ok {
			w.Header().Set("Allow", allowHeader)
			Error(w, http.StatusMethodNotAllowed, "不支持的请求方法")
			return
		}
		h(w, r)
	}
}

// DecodeJSON 读取并解析 JSON 请求体。失败时已经写好响应,返回 false。
// maxBytes 是请求体上限(所有外部输入都要设上限)。
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	err := dec.Decode(v)
	if err == nil {
		// 不允许 JSON 后面还跟着别的内容
		if _, extra := dec.Token(); !errors.Is(extra, io.EOF) {
			err = errors.New("trailing data")
		}
	}
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		Error(w, http.StatusRequestEntityTooLarge, "请求体过大")
		return false
	}
	Error(w, http.StatusBadRequest, "请求体不是有效的 JSON")
	return false
}

// NoStore 禁止缓存响应(鉴权、配置类接口用)。
func NoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}
