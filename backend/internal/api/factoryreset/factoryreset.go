// Package factoryreset 实现 POST /api/openbox/factory-reset(恢复出厂设置)。做法和失败时的状态见 internal/factoryreset 的包说明。
// 响应形状取自前端 api/openbox.ts 的 OpenboxFactoryResetResult,没有参照上游。
package factoryreset

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/factoryreset"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Service 是本分组要用的功能(*factoryreset.Service 满足)。
type Service interface {
	Run(ctx context.Context) (factoryreset.Result, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

// budget 是整个恢复过程的总时限(撤销 DNS 接管、停内核各自还有更细的超时)。
const budget = 5 * time.Minute

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/factory-reset", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.run}))
}

type handlers struct{ s Service }

// run 状态码:200 做完;409 部署或另一次恢复正在进行(什么都没动);
// 500 内核没停下来(什么都没删)或清到一半有项失败(信息里写明已清和失败的项、密码没清)。
func (h *handlers) run(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	// 一旦开始就要做完:请求断开不能把数据清到一半就停
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), budget)
	defer cancel()
	res, err := h.s.Run(ctx)
	if err != nil {
		if errors.Is(err, factoryreset.ErrBusy) {
			httpx.Error(w, http.StatusConflict, err.Error())
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
