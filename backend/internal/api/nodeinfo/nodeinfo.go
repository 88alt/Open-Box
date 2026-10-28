// Package nodeinfo 实现 GET /api/openbox/node-info/{tag}(节点信息)。
// entry-country 和 exit 要查第三方的 IP 归属,是后续的条目,做到时在这里继续挂。
package nodeinfo

import (
	"context"
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	ni "github.com/timeflysoon/Open-Box/backend/internal/nodeinfo"
)

// Service 是本分组要用的功能(*nodeinfo.Service 满足)。
type Service interface {
	Get(ctx context.Context, tag string) (*ni.Info, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/node-info/{tag}", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.get}))
}

type handlers struct{ s Service }

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	info, err := h.s.Get(r.Context(), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, ni.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, err.Error())
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, info)
}
