// Package routetest 实现 POST /api/openbox/route-test。
// 响应形状取自前端 api/openbox.ts 的 OpenboxRouteTest,没有参照上游;哪些字段还没做见 internal/routetest 的包注释。
package routetest

import (
	"context"
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/routetest"
)

// Service 是本分组要用的功能(*routetest.Service 满足)。
type Service interface {
	Run(ctx context.Context, in routetest.Input) (*routetest.Result, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

const maxBody = 4 << 10

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/route-test", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.run}))
}

type handlers struct{ s Service }

func (h *handlers) run(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var in routetest.Input
	if !httpx.DecodeJSON(w, r, &in, maxBody) {
		return
	}
	res, err := h.s.Run(r.Context(), in)
	if err != nil {
		var ie *routetest.InputError
		switch {
		case errors.As(err, &ie):
			httpx.Error(w, http.StatusBadRequest, ie.Msg)
		case r.Context().Err() != nil:
			httpx.Error(w, http.StatusServiceUnavailable, "测试已取消")
		default:
			httpx.Error(w, http.StatusInternalServerError, "真实路由测试失败: "+err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
