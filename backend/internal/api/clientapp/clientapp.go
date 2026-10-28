// Package clientapp 实现 /api/openbox/client-app/* 里已做的部分(目前只有 GET info)。
// 响应形状取自前端 api/clientApp.ts 的 OpenboxClientAppInfo,没有参照上游。
package clientapp

import (
	"context"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/clientapp"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Service 是本分组要用的功能(*clientapp.Service 满足)。
type Service interface {
	Info(ctx context.Context) (clientapp.Info, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/client-app/info", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.info,
	}))
}

// info 对应前端 fetchClientAppInfo。路由器 ID 第一次读时生成并保存,所以这是个「读时可能写一次」的 GET,
// 只写自己的数据文件,不碰系统。
func (d Deps) info(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Service == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端 App 服务不可用")
		return
	}
	info, err := d.Service.Info(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "读取客户端 App 信息失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, info)
}
