// Package bandwidth 挂 GET /api/openbox/bandwidth:带宽择优的测速参数、要测的节点数、每日下载量上限和调度的运行情况。
// 契约见 docs/ENDPOINTS.md 与前端 api/openbox.ts 的 OpenboxBandwidthStatus。
package bandwidth

import (
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/bandwidth"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Deps 是本分组的依赖。
type Deps struct{ Service *bandwidth.Service }

// Register 挂路由。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/bandwidth", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			httpx.NoStore(w)
			httpx.JSON(w, http.StatusOK, d.Service.Describe(r.Context()))
		},
	}))
}
