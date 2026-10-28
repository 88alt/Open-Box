// Package clientapp 实现 /api/openbox/client-app/* 和 /api/openbox/client-config* 里已做的部分:
// GET client-app/info,以及本地分流的同步状态(GET client-config、PUT client-config/enabled、POST client-config/rotate)。
// 响应形状取自前端 api/clientApp.ts 的类型,没有参照上游。
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

// ConfigService 是本地分流同步状态要用的功能(*clientapp.Service 满足)。
type ConfigService interface {
	ClientConfig(ctx context.Context) (clientapp.Issue, error)
	SetClientConfigEnabled(ctx context.Context, enabled bool) (bool, error)
	RotateClientConfig(ctx context.Context) (clientapp.Issue, error)
}

// Deps 是本分组的依赖。Config 为 nil 时 client-config* 回 503。
type Deps struct {
	Service Service
	Config  ConfigService
}

// maxEnabledBody 是 PUT client-config/enabled 的请求体上限:只有一个布尔值。
const maxEnabledBody = 1 << 10

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/client-app/info", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.info,
	}))
	mux.HandleFunc("/api/openbox/client-config", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.configGet,
	}))
	mux.HandleFunc("/api/openbox/client-config/enabled", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPut: d.configEnabled,
	}))
	mux.HandleFunc("/api/openbox/client-config/rotate", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.configRotate,
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

// 配对码是凭据:响应一律 no-store,日志里不出现请求 / 响应内容(本服务的访问日志只记方法、路径和状态)。

// configGet 对应前端 fetchClientConfig。第一次读时生成并保存配对码,同步开关默认关。
func (d Deps) configGet(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Config == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	is, err := d.Config.ClientConfig(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "读取客户端配置失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, is)
}

// configEnabled 对应前端 setClientConfigEnabled。请求体必须是 {"enabled": true|false}。
func (d Deps) configEnabled(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Config == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !httpx.DecodeJSON(w, r, &body, maxEnabledBody) {
		return
	}
	if body.Enabled == nil {
		httpx.Error(w, http.StatusBadRequest, "enabled 必须是 true 或 false")
		return
	}
	on, err := d.Config.SetClientConfigEnabled(r.Context(), *body.Enabled)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "保存同步开关失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"enabled": on})
}

// configRotate 对应前端 rotateClientConfig:更换配对码,之前导入的设备和导出的文件都会失效。
func (d Deps) configRotate(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Config == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	is, err := d.Config.RotateClientConfig(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "更换配对码失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, is)
}
