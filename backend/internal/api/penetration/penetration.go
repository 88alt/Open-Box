// Package penetration 实现 POST /api/openbox/penetration。
// 请求 {"target": "域名或 IP"},响应形状取自前端 api/openbox.ts 的 OpenboxPenetrationResult,没有参照上游;
// 哪些字段还没做见 internal/penetration 的包注释。
package penetration

import (
	"context"
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/penetration"
)

// Service 是本分组要用的功能(*penetration.Service 满足)。
type Service interface {
	Query(ctx context.Context, target string) (*penetration.Result, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

const maxBody = 1 << 10

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/penetration", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.query}))
}

type handlers struct{ s Service }

func (h *handlers) query(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var in struct {
		Target string `json:"target"`
	}
	if !httpx.DecodeJSON(w, r, &in, maxBody) {
		return
	}
	res, err := h.s.Query(r.Context(), in.Target)
	if err != nil {
		var ie *penetration.InputError
		switch {
		case errors.As(err, &ie):
			httpx.Error(w, http.StatusBadRequest, ie.Msg)
		case r.Context().Err() != nil:
			httpx.Error(w, http.StatusServiceUnavailable, "查询已取消")
		default:
			httpx.Error(w, http.StatusInternalServerError, "分流推算失败: "+err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
