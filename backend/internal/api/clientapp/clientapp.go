// Package clientapp 实现 /api/openbox/client-app/* 和 /api/openbox/client-config* 里已做的部分:
// GET client-app/info、POST client-app/egress-country,以及本地分流的同步状态(GET client-config、PUT client-config/enabled、POST client-config/rotate)。
// 响应形状取自前端 api/clientApp.ts 的类型,没有参照上游。
package clientapp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
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

// PairService 是上游口径的 App 在线配对与按 token 拉配置(*clientapp.Service 满足)。
type PairService interface {
	PairGet(ctx context.Context, routerID string) (nonce string, err error)
	PairPost(ctx context.Context, routerID, nonce, proof string) (sealed string, err error)
	ConfigByToken(ctx context.Context, routerID, token, ip string) (clientapp.TokenBundle, error)
	// RuleSetPath / Background 是 config/:token 下面的子资源:规则集文件路径、面板背景图。
	RuleSetPath(ctx context.Context, routerID, token, tag string) (string, error)
	Background(ctx context.Context, routerID, token string) (*clientapp.BackgroundImage, error)
}

// ShareService 是共享网络服务器的 App 码要用的功能(*clientapp.Service 满足)。
type ShareService interface {
	ShareCodes(ctx context.Context, links map[string]string) (map[string]clientapp.ShareSeal, error)
	Share(ctx context.Context, sid, gen, ip string) (clientapp.ShareResult, error)
	RotateShare(ctx context.Context, sid string) (clientapp.ShareRotated, error)
}

// EgressService 是按出口 IP 判国家要用的功能(*clientapp.Service 满足)。
type EgressService interface {
	DetectEgressCountry(ctx context.Context) (clientapp.EgressCountry, error)
}

// Deps 是本分组的依赖。Config 为 nil 时 client-config* 回 503,File 为 nil 时 client-config/file 回 503,Sync 为 nil 时 /client/v1/config 回 503,Share 为 nil 时 share-codes(含 {id}/rotate)和 /client/v1/share 回 503,Egress 为 nil 时 egress-country 回 503。
type Deps struct {
	Service Service
	Config  ConfigService
	File    FileService
	Sync    SyncService
	Pair    PairService
	Share   ShareService
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
	mux.HandleFunc("/client/v1/share", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.shareGet,
	}))
	// 对齐上游 v0.1.402 App 路径
	mux.HandleFunc("/client/v1/{routerId}/pair", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet:  d.pairGet,
		http.MethodPost: d.pairPost,
	}))
	mux.HandleFunc("/client/v1/{routerId}/config/{token}", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.configByToken,
	}))
	mux.HandleFunc("/client/v1/{routerId}/config/{token}/rule-set/{tag}", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.ruleSetGet,
	}))
	mux.HandleFunc("/client/v1/{routerId}/config/{token}/background", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.backgroundGet,
	}))
	mux.HandleFunc("/client/v1/", httpx.NotFound) // 别的路径回 JSON 404,不落到前端的 index.html
	mux.HandleFunc("/api/openbox/client-config/rotate", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.configRotate,
	}))
	mux.HandleFunc("/api/openbox/client-app/share-codes", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.shareCodes,
	}))
	// share-codes/{id}/rotate:换这台服务器的配对码,密码一起换并立即应用(路径里的 id 已被 ServeMux 解码)。别的子路径回 JSON 404,不落到前端的 index.html
	mux.HandleFunc("/api/openbox/client-app/share-codes/{id}/rotate", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.shareRotate,
	}))
	mux.HandleFunc("/api/openbox/client-app/share-codes/", httpx.NotFound)
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
		var tl *clientapp.ContentFilterTooLargeError
		switch {
		case errors.As(err, &ie):
			httpx.Error(w, http.StatusBadRequest, ie.Msg)
		case errors.As(err, &tl): // 启用的内容过滤规则太多:如实说,不截断
			httpx.Error(w, http.StatusConflict, tl.Error())
		default:
			httpx.Error(w, http.StatusInternalServerError, "导出客户端配置失败: "+err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, b)
}

// syncConfig 是 App 的公开同步接口:GET /client/v1/config?p=<nonce>(nonce 就是导入码里的 p)。
// 同步开关关着 403 {error:"disabled"}(前端注释写明的约定);来源 IP 被限速 429 加 Retry-After;nonce 对不上 404;
// nonce 对上了但启用的内容过滤规则超过同步上限 409 {error:"content filter too large"}(不截断,也不更新 lastSyncAt);
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
		var tl *clientapp.ContentFilterTooLargeError
		switch {
		case errors.Is(err, clientapp.ErrDisabled):
			httpx.Error(w, http.StatusForbidden, "disabled")
		case errors.As(err, &le):
			secs := int((le.After + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
			httpx.Error(w, http.StatusTooManyRequests, le.Error())
		case errors.Is(err, clientapp.ErrSyncNotFound):
			httpx.Error(w, http.StatusNotFound, "not found")
		case errors.As(err, &tl): // 已通过 nonce 才会走到这里;启用的内容过滤规则超过同步上限,不截断
			httpx.Error(w, http.StatusConflict, "content filter too large")
		default:
			httpx.Error(w, http.StatusInternalServerError, "同步失败")
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// maxShareCodesBody 是 POST share-codes 的请求体上限:最多 64 台服务器、每条链接最多 4096 个字符(JSON 转义和多字节字符留足余量)。
const maxShareCodesBody = 1 << 20

// shareCodes 对应前端 fetchShareCodeSeals:请求体 {links: {服务器 id: 节点分享链接}},返回 {服务器 id: {pairCode, gen, sealed}}。
// 档案里没有的服务器、链接为空或超过 4096 字符的,回包里没有。第一次为某台服务器出码时生成它的配对码,配对码属于凭据:
// 响应 no-store,不写日志。
func (d Deps) shareCodes(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Share == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	var body struct {
		Links map[string]string `json:"links"`
	}
	if !httpx.DecodeJSON(w, r, &body, maxShareCodesBody) {
		return
	}
	if body.Links == nil {
		httpx.Error(w, http.StatusBadRequest, "links 必须是对象")
		return
	}
	out, err := d.Share.ShareCodes(r.Context(), body.Links)
	if err != nil {
		var ie *clientapp.InputError
		if errors.As(err, &ie) {
			httpx.Error(w, http.StatusBadRequest, ie.Msg)
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "生成共享服务器的配对码失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// shareRotate 对应前端 rotateShareCode:换这台服务器的配对码(代数加一,旧代数立刻失效),服务器密码一起换,并走部署让新密码生效。
// 返回 {pairCode, gen, server, applied: saved|restarted|failed, message?}:部署失败时配对码和密码仍已换,applied 是 failed、message 写原因(200,不是错误)。
// 服务器不存在 404;没接档案修改能力 503;档案里别的共享服务器有不合格的项(整份写回被校验拦下)400 带原因。
// 配对码和新密码是凭据:响应 no-store,不写日志(访问日志只记方法、路径和状态)。
func (d Deps) shareRotate(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Share == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	out, err := d.Share.RotateShare(r.Context(), r.PathValue("id"))
	if err != nil {
		var ie *clientapp.InputError
		switch {
		case errors.Is(err, clientapp.ErrServerNotFound):
			httpx.Error(w, http.StatusNotFound, "没有这台共享服务器")
		case errors.Is(err, clientapp.ErrUnavailable):
			httpx.Error(w, http.StatusServiceUnavailable, "更换配对码不可用")
		case errors.As(err, &ie):
			httpx.Error(w, http.StatusBadRequest, ie.Msg)
		default:
			httpx.Error(w, http.StatusInternalServerError, "更换配对码失败: "+err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// shareGet 是 App 取回共享服务器节点配置的公开接口:GET /client/v1/share?sid=<服务器 id>&g=<代数>(码里的 sid 和 g)。
// 口径与 /client/v1/config 相同:同步开关关着 403 {error:"disabled"},来源 IP 被限速 429 加 Retry-After,
// 服务器不存在 / 代数对不上 404,成功 200 返回封包(没有这台服务器的配对码解不开)。响应 no-store,日志不记 query。
func (d Deps) shareGet(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Share == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	q := r.URL.Query()
	res, err := d.Share.Share(r.Context(), q.Get("sid"), q.Get("g"), peerIP(r))
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
			httpx.Error(w, http.StatusInternalServerError, "取回失败")
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

func (d Deps) pairGet(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Pair == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	nonce, err := d.Pair.PairGet(r.Context(), r.PathValue("routerId"))
	if err != nil {
		writePairErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"nonce": nonce})
}

func (d Deps) pairPost(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Pair == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	var body struct {
		Nonce string `json:"nonce"`
		Proof string `json:"proof"`
	}
	if !httpx.DecodeJSON(w, r, &body, 2<<10) {
		return
	}
	sealed, err := d.Pair.PairPost(r.Context(), r.PathValue("routerId"), body.Nonce, body.Proof)
	if err != nil {
		writePairErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"sealed": sealed})
}

func (d Deps) configByToken(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Pair == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	out, err := d.Pair.ConfigByToken(r.Context(), r.PathValue("routerId"), r.PathValue("token"), peerIP(r))
	if err != nil {
		writePairErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ruleSetGet 给 App 一份规则集文件(.srs 二进制)。tag 可带 .srs 后缀;名字不合格、文件不存在都是 404 not found。
func (d Deps) ruleSetGet(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Pair == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	path, err := d.Pair.RuleSetPath(r.Context(), r.PathValue("routerId"), r.PathValue("token"), r.PathValue("tag"))
	if err != nil {
		writePairErr(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// backgroundGet 给 App 面板背景图,响应头 X-Openbox-Background-Version 是内容摘要;没有设置背景图是 404 not found。
func (d Deps) backgroundGet(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Pair == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "客户端配置服务不可用")
		return
	}
	img, err := d.Pair.Background(r.Context(), r.PathValue("routerId"), r.PathValue("token"))
	if err != nil {
		writePairErr(w, err)
		return
	}
	w.Header().Set("X-Openbox-Background-Version", img.Version)
	w.Header().Set("Content-Type", img.Type)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img.Bytes)
}

func writePairErr(w http.ResponseWriter, err error) {
	var locked *clientapp.PairLockedError
	var bad *clientapp.PairBadCodeError
	switch {
	case errors.Is(err, clientapp.ErrPairUnpaired):
		httpx.Error(w, http.StatusNotFound, "unpaired")
	case errors.Is(err, clientapp.ErrPairDisabled):
		httpx.Error(w, http.StatusForbidden, "disabled")
	case errors.Is(err, clientapp.ErrPairExpired):
		httpx.Error(w, http.StatusGone, "expired")
	case errors.As(err, &locked):
		secs := int((locked.After + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		httpx.Error(w, http.StatusTooManyRequests, "locked")
	case errors.As(err, &bad):
		httpx.JSON(w, http.StatusForbidden, map[string]any{"error": "bad_code", "remaining": bad.Remaining})
	case errors.Is(err, clientapp.ErrTokenNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	default:
		httpx.Error(w, http.StatusInternalServerError, "同步失败")
	}
}
