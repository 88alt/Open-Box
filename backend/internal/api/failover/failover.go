// Package failover 挂故障转移组的三个接口:GET /api/openbox/failover/status(已部署的计划 + 内核此刻的选择 + 检测管理器的运行数据),
// POST /failover/refresh(所有组马上重排一轮)和 POST /failover/recheck(一个组的节点强制全测一遍,组不存在 404)。
// 没有接检测管理器(Deps.Manager 为空)时,状态只有计划和内核的选择,两个 POST 如实回 501。
// 契约见 docs/ENDPOINTS.md 与前端 api/openbox.ts 的 OpenboxFailoverStatus 等类型。
package failover

import (
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/failover"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Manager 是检测管理器(*failover.Manager 满足)。
type Manager interface {
	failover.Runtime
	Refresh()
	Recheck(id string) bool
}

// Deps 是本分组的依赖。
type Deps struct {
	// Manager 可为空:空 = 没有后台检测,见包注释。
	Manager Manager
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
	if h.d.Manager == nil {
		httpx.JSON(w, http.StatusOK, failover.BuildStatus(r.Context(), p, h.d.Kernel))
		return
	}
	httpx.JSON(w, http.StatusOK, failover.BuildStatusWith(r.Context(), p, h.d.Kernel, h.d.Manager))
}

func (h *handlers) refresh(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if h.d.Manager == nil {
		httpx.Error(w, http.StatusNotImplemented, notReady)
		return
	}
	h.d.Manager.Refresh()
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// recheck:对不存在的组回 404(「组不存在回 404」);组在就让管理器把它的节点强制全测一遍(没有管理器时如实回 501)。
func (h *handlers) recheck(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var req struct {
		ID string `json:"id"`
	}
	if !httpx.DecodeJSON(w, r, &req, 4<<10) {
		return
	}
	if h.d.Manager != nil {
		if !h.d.Manager.Recheck(req.ID) {
			httpx.Error(w, http.StatusNotFound, "没有这个故障转移组")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
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
