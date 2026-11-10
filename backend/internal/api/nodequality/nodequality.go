// Package nodequality 是 GET /api/openbox/node-quality(概览页「节点质量」卡)。
// 参数和响应形状取自前端 api/openbox.ts 的 fetchNodeQuality / OpenboxNodeQuality,没有参照上游。
package nodequality

import (
	"context"
	"net/http"
	"strconv"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	nq "github.com/timeflysoon/Open-Box/backend/internal/nodequality"
)

// Service 是 *nodequality.Service 的接口。
type Service interface {
	Query(ctx context.Context, q nq.Query) (nq.Result, error)
}

// Deps 是依赖。
type Deps struct{ Service Service }

// Register 把路由挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/node-quality", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.get}))
}

type handlers struct{ s Service }

func boolParam(v string) bool { return v == "1" || v == "true" }

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	p := r.URL.Query()
	q := nq.Query{
		AllSubscriptions: boolParam(p.Get("allSubscriptions")),
		Search:           p.Get("search"),
		Details:          p.Get("details"),
		AllNodes:         boolParam(p.Get("allNodes")),
		Node:             p.Get("node"),
	}
	if v := p.Get("nodePageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			httpx.Error(w, http.StatusBadRequest, "nodePageSize 要是 1 到 500 的整数")
			return
		}
		q.NodePageSize = n
	}
	res, err := h.s.Query(r.Context(), q)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
