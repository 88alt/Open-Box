// Package diagnostics 实现 GET /api/openbox/diagnostics(诊断包,只读)。
package diagnostics

import (
	"context"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Collector 是本分组要用的功能(*diagnostics.Service 满足)。
type Collector interface {
	Collect(ctx context.Context) map[string]any
}

// Deps 是本分组的依赖。
type Deps struct{ Service Collector }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/diagnostics", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			httpx.NoStore(w)
			httpx.JSON(w, http.StatusOK, d.Service.Collect(r.Context()))
		},
	}))
}
