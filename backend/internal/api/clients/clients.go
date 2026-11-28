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
	// Sources 返回最近几天流量里出现过的来源地址,用来补上「没有 DHCP 租约但在上网」的设备
	// (静态 IP、旁路由转发来的)。为空时只列租约,与以前一致。
	Sources func() []string
	// Self 判断某个地址是不是路由器自己的;为空时不过滤。
	// 路由器自身也会出现在流量里,但它不是一个可选的分流来源。
	Self func(addr string) (kind, iface string, ok bool)
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/clients", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.list,
	}))
}

// list 对应前端 fetchKnownClients:{ clients: [{ ip, name, mac? }] }。
// 两路来源:DHCP 租约(带主机名 / MAC)+ 最近几天流量里出现过的来源地址(只有 IP)。
// 读不到租约不是错误,回空数组(前端本来就把失败当作没有)。
func (d Deps) list(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var cs []system.Client
	if d.List != nil {
		cs = d.List()
	}
	// 租约里没有、但流量里确实出现过的来源,补成没有名字、没有 MAC 的条目:
	// 用户至少能在「终端分流」里选到它,而不是因为设备用静态 IP 就选不着。
	if d.Sources != nil {
		have := make(map[string]bool, len(cs))
		for _, c := range cs {
			have[c.IP] = true
		}
		for _, ip := range d.Sources() {
			if ip == "" || have[ip] {
				continue
			}
			if d.Self != nil {
				if _, _, self := d.Self(ip); self {
					continue
				}
			}
			have[ip] = true
			cs = append(cs, system.Client{IP: ip})
		}
	}
	if cs == nil {
		cs = []system.Client{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"clients": cs})
}
