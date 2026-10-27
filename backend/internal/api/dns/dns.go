// Package dns 实现 /api/openbox/dns/* 里与「上游 DNS」有关的接口。
// 本包目前只有只读的 GET /api/openbox/dns/wan;region-detect、upstream-test、flush-cache 见 STATUS.md M2 的后续条目。
package dns

import (
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Deps 是本分组的依赖。
type Deps struct {
	// WAN 返回系统此刻的上游 DNS(*system.ReadWANDNS 的包装),测试里换假的。为空或返回 nil 都按「没有」处理。
	WAN func() []string
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/dns/wan", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.wan,
	}))
}

// wan 对应前端 fetchWanDns:{ servers: string[] }。读不到时回空数组而不是报错——
// 前端拿它填「上游 DNS」那一项的说明,没有就不显示。
func (d Deps) wan(w http.ResponseWriter, _ *http.Request) {
	httpx.NoStore(w)
	servers := []string{}
	if d.WAN != nil {
		if s := d.WAN(); s != nil {
			servers = s
		}
	}
	httpx.JSON(w, http.StatusOK, map[string][]string{"servers": servers})
}
