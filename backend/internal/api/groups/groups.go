// Package groups 实现 /api/openbox/groups(GET 读取 / PUT 整份覆盖)。
package groups

import (
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
)

const maxBody = 4 << 20 // 4 MiB

// Deps 是本分组的依赖。
type Deps struct{ Groups *nodegroups.Service }

type handlers struct{ g *nodegroups.Service }

// Register 把本分组的接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{g: d.Groups}
	mux.HandleFunc("/api/openbox/groups", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.get,
		http.MethodPut: h.put,
	}))
}

func (h *handlers) fail(w http.ResponseWriter, err error, what string) {
	var in *nodegroups.InputError
	var ce *core.CoreError
	switch {
	case errors.As(err, &in):
		httpx.Error(w, http.StatusBadRequest, in.Msg)
	case errors.Is(err, nodegroups.ErrCoreUnavailable):
		httpx.Error(w, http.StatusServiceUnavailable, err.Error())
	case errors.As(err, &ce):
		httpx.Error(w, http.StatusBadRequest, "节点组无法处理:"+ce.Message)
	default:
		httpx.Error(w, http.StatusInternalServerError, what+err.Error())
	}
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	p, err := h.g.Get(r.Context())
	if err != nil {
		h.fail(w, err, "读取节点组失败: ")
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (h *handlers) put(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var body struct {
		Groups []any `json:"groups"`
	}
	if !httpx.DecodeJSON(w, r, &body, maxBody) {
		return
	}
	if body.Groups == nil {
		httpx.Error(w, http.StatusBadRequest, "缺少 groups 数组")
		return
	}
	res, err := h.g.Save(r.Context(), body.Groups)
	if err != nil {
		h.fail(w, err, "保存节点组失败: ")
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
