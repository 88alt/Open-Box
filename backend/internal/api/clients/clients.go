// Package clients 实现 GET /api/openbox/clients:终端分流选来源用的「已知终端」。
package clients

import (
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Deps 是本分组的依赖。
type Deps struct {
	// List 返回此刻的已知终端(*system.ReadDHCPLeases 的包装),测试里换假的。为空时回空列表。
	List func() []system.Client
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/clients", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.list,
	}))
}

// list 对应前端 fetchKnownClients:{ clients: [{ ip, name, mac? }] }。
// 前端注释说来源是「DHCP 租约里的设备 + 今天流量里出现过的来源 IP」;流量统计(traffic*)还没做,
// 目前只有租约这一路。读不到租约不是错误,回空数组(前端本来就把失败当作没有)。
func (d Deps) list(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var cs []system.Client
	if d.List != nil {
		cs = d.List()
	}
	if cs == nil {
		cs = []system.Client{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"clients": cs})
}
