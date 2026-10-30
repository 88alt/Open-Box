// Package failover 挂故障转移组的三个接口:GET /api/openbox/failover/status(已部署的计划 + 内核此刻的选择),
// POST /failover/refresh 和 POST /failover/recheck(都要靠后台检测管理器,管理器还没做,如实回 501)。
// 契约见 docs/ENDPOINTS.md 与前端 api/openbox.ts 的 OpenboxFailoverStatus 等类型。
package failover

import (
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/failover"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Deps 是本分组的依赖。
type Deps struct {
	// Plan 读已部署的故障转移计划。
	Plan func() (failover.Plan, error)
	// Kernel 读内核当前的代理;nil 或读不到时状态里各组是 unknown。
	Kernel failover.KernelFunc
}

// Register 挂路由。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{d: d}
	mux.HandleFunc("/api/openbox/failover/status", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.status}))
	mux.HandleFunc("/api/openbox/failover/refresh", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.refresh}))
	mux.HandleFunc("/api/openbox/failover/recheck", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.recheck}))
}

type handlers struct{ d Deps }

const notReady = "故障转移的自动检测和切换还没实现,暂时只能看状态"

func (h *handlers) plan() (failover.Plan, error) {
	if h.d.Plan == nil {
		return failover.Plan{}, nil
	}
	return h.d.Plan()
}

func (h *handlers) status(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	p, err := h.plan()
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "读取故障转移计划失败:"+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, failover.BuildStatus(r.Context(), p, h.d.Kernel))
}

func (h *handlers) refresh(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, http.StatusNotImplemented, notReady)
}

// recheck:先按前端文档对不存在的组回 404(「组不存在回 404」),组在的话如实回 501。
func (h *handlers) recheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !httpx.DecodeJSON(w, r, &req, 4<<10) {
		return
	}
	p, err := h.plan()
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "读取故障转移计划失败:"+err.Error())
		return
	}
	for _, g := range p {
		if g.ID == req.ID {
			httpx.Error(w, http.StatusNotImplemented, notReady)
			return
		}
	}
	httpx.Error(w, http.StatusNotFound, "没有这个故障转移组")
}
