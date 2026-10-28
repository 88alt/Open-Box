// Package latencyhistory 实现 /api/openbox/latency-history 的四个接口:
// GET 整份历史、GET version(只回 updatedAt,前端每 15 秒轮询)、POST samples(面板上报超时)、POST sync(立刻读一次内核)。
// 响应形状取自前端 api/openbox.ts,没有参照上游。
package latencyhistory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	lh "github.com/timeflysoon/Open-Box/backend/internal/latencyhistory"
)

// Service 是本分组要用的功能(*latencyhistory.Service 满足)。
type Service interface {
	Get(ctx context.Context) (lh.History, int64, error)
	Add(ctx context.Context, in []lh.Input) (lh.History, error)
	Sync(ctx context.Context) (lh.History, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

const maxBody = 256 << 10

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/latency-history", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.get}))
	mux.HandleFunc("/api/openbox/latency-history/version", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.version}))
	mux.HandleFunc("/api/openbox/latency-history/samples", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.samples}))
	mux.HandleFunc("/api/openbox/latency-history/sync", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.sync}))
}

type handlers struct{ s Service }

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	hist, up, err := h.s.Get(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"history": nonNil(hist), "updatedAt": up})
}

func (h *handlers) version(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	_, up, err := h.s.Get(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"updatedAt": up})
}

func (h *handlers) samples(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "请求体太大或读取失败")
		return
	}
	var req struct {
		Samples []lh.Input `json:"samples"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "请求内容不是有效的 JSON")
		return
	}
	hist, err := h.s.Add(r.Context(), req.Samples)
	if err != nil {
		var ie *lh.InputError
		if errors.As(err, &ie) {
			httpx.Error(w, http.StatusBadRequest, ie.Msg)
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "history": nonNil(hist)})
}

func (h *handlers) sync(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	hist, err := h.s.Sync(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"history": nonNil(hist)})
}

func nonNil(h lh.History) lh.History {
	if h == nil {
		return lh.History{}
	}
	return h
}
