// Package servers 实现 /api/openbox/servers/*(「共享网络」)。
// 目前只有 port-check;服务器本身存在档案的 servers 里,走 /api/openbox/profile 读写。
package servers

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/portcheck"
)

// PortChecker 判断端口能不能用(*portcheck.Checker 实现),测试里换假的。
type PortChecker interface {
	Check(ctx context.Context, port int, id string) (portcheck.Result, error)
}

// Deps 是本分组的依赖。Ports 为空时 port-check 返回 503。
type Deps struct {
	Ports PortChecker
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/servers/port-check", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: d.portCheck}))
}

// portCheck 对应前端 checkServerPort:GET ?port=&id=,回 { ok, reason?, name? }。
// 端口不是 1–65535 的整数不是请求错误,而是检测结果(ok:false, reason:"invalid"),前端据此提示;
// 读不到端口占用情况才回错误(前端显示「端口检测失败」)。
func (d Deps) portCheck(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Ports == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "端口检测未启用")
		return
	}
	q := r.URL.Query()
	port, err := strconv.Atoi(strings.TrimSpace(q.Get("port")))
	if err != nil {
		port = 0
	}
	res, err := d.Ports.Check(r.Context(), port, q.Get("id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "读取端口占用情况失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
