// Package policies 实现 /api/openbox/policies/entries(「域名穿透」:一个站点集命中哪些域名 / IP)。
package policies

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
)

const (
	defaultLimit = 50
	maxLimit     = 500
)

// Deps 是本分组的依赖。
type Deps struct{ Entries *policyentries.Service }

type handlers struct{ e *policyentries.Service }

// Register 把本分组的接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{e: d.Entries}
	mux.HandleFunc("/api/openbox/policies/entries", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.entries}))
}

func intParam(r *http.Request, k string, def, min, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(k))
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func (h *handlers) entries(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	qs := r.URL.Query()
	dir := qs.Get("dir")
	if dir != "" && dir != "asc" && dir != "desc" {
		httpx.Error(w, http.StatusBadRequest, "dir 只能是 asc 或 desc")
		return
	}
	res, err := h.e.Query(r.Context(), policyentries.Query{
		Name:   qs.Get("name"),
		Tab:    qs.Get("tab"),
		Q:      qs.Get("q"),
		Sort:   qs.Get("sort"),
		Desc:   dir == "desc",
		Offset: intParam(r, "offset", 0, 0, 1<<30),
		Limit:  intParam(r, "limit", defaultLimit, 1, maxLimit),
	})
	if err != nil {
		var in *policyentries.InputError
		var nf *policyentries.NotFoundError
		var ce *core.CoreError
		switch {
		case errors.As(err, &in):
			httpx.Error(w, http.StatusBadRequest, in.Msg)
		case errors.As(err, &nf):
			httpx.Error(w, http.StatusNotFound, nf.Error())
		case errors.Is(err, policyentries.ErrCoreUnavailable):
			httpx.Error(w, http.StatusServiceUnavailable, err.Error())
		case errors.As(err, &ce):
			httpx.Error(w, http.StatusBadRequest, "站点集无法处理:"+ce.Message)
		case errors.Is(err, r.Context().Err()) && r.Context().Err() != nil:
			httpx.Error(w, http.StatusGatewayTimeout, "请求已取消或超时")
		default:
			httpx.Error(w, http.StatusInternalServerError, "读取站点集条目失败: "+err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
