// Package clientapp 实现 /api/openbox/client-app/* 和 /api/openbox/client-config* 里已做的部分:
// GET client-app/info、POST client-app/egress-country,以及本地分流的同步状态(GET client-config、PUT client-config/enabled、POST client-config/rotate)。
// 响应形状取自前端 api/clientApp.ts 的类型,没有参照上游。
package clientapp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

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

// FileService 是导出文件要用的功能(*clientapp.Service 满足)。
type FileService interface {
	ExportFile(ctx context.Context, lan, publicBase string) (clientapp.Bundle, error)
}

// SyncService 是 App 公开同步接口要用的功能(*clientapp.Service 满足)。
type SyncService interface {
	Sync(ctx context.Context, nonce, ip string) (clientapp.SyncResult, error)
}

// EgressService 是按出口 IP 判国家要用的功能(*clientapp.Service 满足)。
type EgressService interface {
	DetectEgressCountry(ctx context.Context) (clientapp.EgressCountry, error)
}

// Deps 是本分组的依赖。Config 为 nil 时 client-config* 回 503,File 为 nil 时 client-config/file 回 503,Sync 为 nil 时 /client/v1/config 回 503,Egress 为 nil 时 egress-country 回 503。
type Deps struct {
	Service Service
	Config  ConfigService
	File    FileService
	Sync    SyncService
	Egress  EgressService
}

// maxEnabledBody 是 PUT client-config/enabled 的请求体上限:只有一个布尔值。
const maxEnabledBody = 1 << 10

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/client-app/info", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.info,
	}))
	mux.HandleFunc("/api/openbox/client-app/egress-country", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.egressCountry,
	}))
	mux.HandleFunc("/api/openbox/client-config", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.configGet,
	}))
	mux.HandleFunc("/api/openbox/client-config/enabled", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPut: d.configEnabled,
	}))
	mux.HandleFunc("/api/openbox/client-config/file", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.configFile,
	}))
	// App 用的公开同步接口:路径在 /api/ 之外,不过登录鉴权(鉴权只拦 /api/),凭 nonce 访问。
	mux.HandleFunc("/client/v1/config", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.syncConfig,
	}))
	mux.HandleFunc("/client/v1/", httpx.NotFound) // 别的路径回 JSON 404,不落到前端的 index.html
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

// maxFileBody 是 POST client-config/file 的请求体上限:只有 lan 和 publicBase 两个短字符串。
const maxFileBody = 8 << 10

// configFile 对应前端 fetchClientConfigFile:请求体 {lan, publicBase}(都可以空),返回封好的导出文件。
// 参数不合格 400;文件等于凭据,响应 no-store,不写日志。
func (d Deps) configFile(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.File == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	var body struct {
		Lan        string `json:"lan"`
		PublicBase string `json:"publicBase"`
	}
	if !httpx.DecodeJSON(w, r, &body, maxFileBody) {
		return
	}
	b, err := d.File.ExportFile(r.Context(), body.Lan, body.PublicBase)
	if err != nil {
		var ie *clientapp.InputError
		if errors.As(err, &ie) {
			httpx.Error(w, http.StatusBadRequest, ie.Msg)
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "导出客户端配置失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, b)
}

// syncConfig 是 App 的公开同步接口:GET /client/v1/config?p=<nonce>(nonce 就是导入码里的 p)。
// 同步开关关着 403 {error:"disabled"}(前端注释写明的约定);来源 IP 被限速 429 加 Retry-After;nonce 对不上 404;
// 成功 200 返回封包(没有配对码解不开)。取来源 IP 用连接对端地址,不信任 X-Forwarded-For(谁都能伪造)。
// 响应一律 no-store;日志只记方法、路径和状态,query 里的 nonce 不进日志。
func (d Deps) syncConfig(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Sync == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	res, err := d.Sync.Sync(r.Context(), r.URL.Query().Get("p"), peerIP(r))
	if err != nil {
		var le *clientapp.LimitedError
		switch {
		case errors.Is(err, clientapp.ErrDisabled):
			httpx.Error(w, http.StatusForbidden, "disabled")
		case errors.As(err, &le):
			secs := int((le.After + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
			httpx.Error(w, http.StatusTooManyRequests, le.Error())
		case errors.Is(err, clientapp.ErrSyncNotFound):
			httpx.Error(w, http.StatusNotFound, "not found")
		default:
			httpx.Error(w, http.StatusInternalServerError, "同步失败")
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// egressCountry 对应前端 detectEgressCountry:按出口公网 IP 判一次国家(只查不改设置),返回 {ip, country, at}。
// 出口 IP 没变就沿用上次的国家;内核没在运行 / 没接上判断 503;三家 IP 接口都问不到 502;别的错误 500。
func (d Deps) egressCountry(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Egress == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "出口国家判断不可用")
		return
	}
	e, err := d.Egress.DetectEgressCountry(r.Context())
	if err != nil {
		var de *clientapp.DetectError
		switch {
		case errors.Is(err, clientapp.ErrNotRunning), errors.Is(err, clientapp.ErrUnavailable):
			httpx.Error(w, http.StatusServiceUnavailable, err.Error())
		case errors.As(err, &de):
			httpx.Error(w, http.StatusBadGateway, de.Msg)
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			httpx.Error(w, http.StatusServiceUnavailable, "判断出口国家被取消或超时")
		default:
			httpx.Error(w, http.StatusInternalServerError, "判断出口国家失败: "+err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}
