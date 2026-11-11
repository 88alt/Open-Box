// Package nodequality 是 GET /api/openbox/node-quality(概览页「节点质量」卡)。
// 参数和响应形状对齐上游 v0.1.423 的服务端(见 internal/nodequality)。
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

// boolParam 与上游一致:true / 1 为真,其余(包括没传)为假。
func boolParam(v string) bool { return v == "1" || v == "true" }

// intParam 取整数参数:没传或不是整数当 0,交给服务按默认值和范围夹取(上游同样不报错)。
func intParam(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	p := r.URL.Query()
	q := nq.Query{
		Page:             intParam(p.Get("page")),
		PageSize:         intParam(p.Get("pageSize")),
		AllSubscriptions: boolParam(p.Get("allSubscriptions")),
		Search:           p.Get("search"),
		Details:          p.Get("details"),
		NodePage:         intParam(p.Get("nodePage")),
		NodePageSize:     intParam(p.Get("nodePageSize")),
		AllNodes:         boolParam(p.Get("allNodes")),
		Node:             p.Get("node"),
	}
	res, err := h.s.Query(r.Context(), q)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
