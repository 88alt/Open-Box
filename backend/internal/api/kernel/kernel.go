// Package kernel 实现双内核切换的 HTTP 接口:
//
//	GET /api/openbox/kernel/switch/preflight?kernel=   (JSON 或 NDJSON 流式)
//	GET /api/openbox/kernel/mihomo/compatibility
//
// 形状对齐上游 v0.1.482:预检 8 项、固定顺序,NDJSON 每查完一项写一行 `{"check":{...}}`,
// 最后 `{"done":true,"kernel":"…"}`;JSON 回 `{kernel,checks:[8 项]}`。
package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/kernelswitch"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Deps 是本分组的依赖。
type Deps struct {
	Engine     *kernelswitch.Engine
	Compatible func(ctx context.Context) []string // 返回 Mihomo 兼容性问题;nil = 没有
	Switch     func(ctx context.Context, kernel string) kernelswitch.Result
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{d: d}
	mux.HandleFunc("/api/openbox/kernel/switch/preflight", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.preflight,
	}))
	mux.HandleFunc("/api/openbox/kernel/mihomo/compatibility", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.compatibility,
	}))
	mux.HandleFunc("/api/openbox/kernel/switch", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: h.switchKernel,
	}))
}

type handlers struct{ d Deps }

func (h *handlers) preflight(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	w.Header().Set("Vary", "Accept")
	kernel := r.URL.Query().Get("kernel")
	if !system.ValidKernel(kernel) {
		httpx.Error(w, http.StatusBadRequest, "不支持的路由器内核")
		return
	}
	if h.d.Engine == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "内核切换预检不可用")
		return
	}
	if !acceptsNDJSON(r) {
		checks := h.d.Engine.Preflight(r.Context(), kernel, nil)
		if checks == nil {
			checks = []kernelswitch.Check{}
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"kernel": kernel, "checks": checks})
		return
	}
	// 流式:每查完一项写一行。请求断开后不再写;引擎本身会因为 ctx 取消提前收工。
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	write := func(v any) bool {
		if err := enc.Encode(v); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	ok := true
	h.d.Engine.Preflight(r.Context(), kernel, func(c kernelswitch.Check) {
		if ok && !write(map[string]any{"check": c}) {
			ok = false
		}
	})
	if ok {
		write(map[string]any{"done": true, "kernel": kernel})
	}
}

// switchKernel 切换内核。预检没过 / 起不来都回 200 + `{ok:false,code:1,stderr}`(与上游一致,
// 前端按 ok / stderr 显示),只有引擎没接上才是 503。
func (h *handlers) switchKernel(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if h.d.Switch == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "内核切换不可用")
		return
	}
	var body struct {
		Kernel string `json:"kernel"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if !system.ValidKernel(body.Kernel) {
		httpx.Error(w, http.StatusBadRequest, "不支持的路由器内核")
		return
	}
	httpx.JSON(w, http.StatusOK, h.d.Switch(r.Context(), body.Kernel))
}

func (h *handlers) compatibility(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	problems := []string{}
	if h.d.Compatible != nil {
		problems = h.d.Compatible(r.Context())
	}
	if problems == nil {
		problems = []string{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"blockers": []string{}, "problems": problems})
}

// acceptsNDJSON 判断客户端要不要流式:Accept 里明确要 application/x-ndjson 才算。
// 浏览器直接打开时 Accept 是 text/html 或 */*,按 JSON 回。
func acceptsNDJSON(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mt := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if mt == "application/x-ndjson" || mt == "application/ndjson" {
			return true
		}
	}
	return false
}
