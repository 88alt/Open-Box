// Package controller 把 /api/controller/ 和 /api/controller-ws/ 两个前缀挂到 mux 上(转发见 internal/controllerproxy)。
package controller

import (
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/controllerproxy"
)

// Register 挂载。两个前缀都带结尾的 /,所以也覆盖它们下面的所有路径。
func Register(mux *http.ServeMux, h *controllerproxy.Handler) {
	mux.Handle(controllerproxy.PrefixREST+"/", h.REST())
	mux.Handle(controllerproxy.PrefixREST, h.REST()) // 没有结尾斜杠的也转(内核的根路径 /)
	mux.Handle(controllerproxy.PrefixWS+"/", h.WS())
}
