// Package nodeinfo 实现 GET /api/openbox/node-info/{tag}(节点信息)和 GET .../{tag}/entry-country(入口 IP 的国家)。
// exit(出口归属和风控值)要查第三方,是后续的条目,做到时在这里继续挂。
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
	EntryCountry(ctx context.Context, tag string) (*ni.EntryResult, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/node-info/{tag}", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.get}))
	mux.HandleFunc("/api/openbox/node-info/{tag}/entry-country", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.entryCountry}))
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

// entryCountry:节点没有 → 404;没有可查的入口地址或第三方都查不到 → 502(前端遇到错误只是不显示国旗)。
func (h *handlers) entryCountry(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	res, err := h.s.EntryCountry(r.Context(), r.PathValue("tag"))
	if err != nil {
		var noAddr *ni.ErrNoAddress
		switch {
		case errors.Is(err, ni.ErrNotFound):
			httpx.Error(w, http.StatusNotFound, err.Error())
		case errors.As(err, &noAddr):
			httpx.Error(w, http.StatusBadGateway, "查不了入口国家:"+err.Error())
		default:
			httpx.Error(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
