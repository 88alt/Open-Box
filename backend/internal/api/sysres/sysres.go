// Package sysres 实现 /api/openbox/system/resources:概览页「硬件负载」、侧边栏卡片和「活跃终端」用的系统资源快照。
package sysres

import (
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/sysres"
)

// Deps.
type Deps struct {
	Service *sysres.Service
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/system/resources", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.get,
	}))
}

func (d Deps) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	snap, err := d.Service.Snapshot(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusServiceUnavailable, "读不到系统负载: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, snap)
}
