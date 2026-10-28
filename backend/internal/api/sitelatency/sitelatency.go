// Package sitelatency 实现 /api/openbox/site-latency(POST 测一轮)和 /site-latency/history(GET 历史)。
// 响应形状取自前端 api/openbox.ts 的 OpenboxSiteLatencyRun / OpenboxSiteLatencyHistory,没有参照上游。
package sitelatency

import (
	"context"
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/sitelatency"
)

// Service 是本分组要用的功能(*sitelatency.Service 满足)。
type Service interface {
	History(ctx context.Context) (*sitelatency.History, error)
	Run(ctx context.Context, sites []sitelatency.Site) (*sitelatency.Run, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

const maxBody = 32 << 10

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/site-latency", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.run}))
	mux.HandleFunc("/api/openbox/site-latency/history", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.history}))
}

type handlers struct{ s Service }

func (h *handlers) history(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	res, err := h.s.History(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *handlers) run(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var in struct {
		Sites []sitelatency.Site `json:"sites"`
	}
	if !httpx.DecodeJSON(w, r, &in, maxBody) {
		return
	}
	res, err := h.s.Run(r.Context(), in.Sites)
	if err != nil {
		var ie *sitelatency.InputError
		switch {
		case errors.As(err, &ie):
			httpx.Error(w, http.StatusBadRequest, ie.Msg)
		case r.Context().Err() != nil:
			httpx.Error(w, http.StatusServiceUnavailable, "测速已取消")
		default:
			httpx.Error(w, http.StatusInternalServerError, "站点延迟测试失败: "+err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
