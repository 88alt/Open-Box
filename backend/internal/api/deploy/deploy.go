// Package deploy 实现 POST /api/openbox/deploy(生成配置 → 校验 → 原子替换 → 重启 → 健康检查 → 失败回滚)
// 和 GET /api/openbox/deploy/state(最近一次部署的结果)。
// 响应形状取自前端 api/openbox.ts 的 OpenboxDeployResult / OpenboxDeployState,没有参照上游。
package deploy

import (
	"context"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/deploystate"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// Service 是本分组要用的功能(*deploystate.Service 满足)。
type Service interface {
	Run(ctx context.Context) singbox.DeployResult
	State() (deploystate.State, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/deploy", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.run}))
	mux.HandleFunc("/api/openbox/deploy/state", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.state}))
}

type handlers struct{ s Service }

// status 把阶段对到 HTTP 状态码:成功 200,其余都是非 2xx(前端 deployNow 靠响应体里的
// {ok, stage, message, badTags} 解释原因,状态码只是给别的调用方看的)。
func status(r singbox.DeployResult) int {
	switch {
	case r.OK:
		return http.StatusOK
	case r.Busy, r.Stage == singbox.StageConflict:
		return http.StatusConflict
	case r.Stage == singbox.StageValidate:
		return http.StatusUnprocessableEntity
	case r.Stage == singbox.StageStart, r.Stage == singbox.StageVerify:
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}

func (h *handlers) run(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	res := h.s.Run(r.Context())
	if res.BadTags == nil {
		res.BadTags = []string{}
	}
	httpx.JSON(w, status(res), res)
}

func (h *handlers) state(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	st, err := h.s.State()
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "读不到部署状态: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"state": st})
}
