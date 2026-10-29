// Package controllerproxy 把面板的请求转发给 sing-box 的控制接口(Clash API):
//   - /api/controller/<路径>     REST(代理、规则、连接、配置、延迟测试……),响应原样回传,流式响应边到边转;
//   - /api/controller-ws/<路径>  WebSocket(connections / logs / memory / traffic),握手和帧原样转发。
//
// 前端的 axios 把 baseURL 设成 /api/controller、WebSocket 连 /api/controller-ws/<名字>(api/index.ts),
// 所以这两个前缀就是前端对后端的全部约定;连不上内核时回 502(前端据此不弹一堆报错)。
// 自己的设计,用标准库的反向代理,没有参照上游,无新依赖。
//
// 安全:
//   - 这两个前缀在登录保护之内(和其它 /api/ 接口一样),代理本身不另做登录;
//   - 内核的 secret 只由后端从正式配置里读出来、加在发给内核的请求上,不会出现在发给浏览器的任何内容里;
//   - 浏览器带来的 Cookie / Authorization / 自己的 token 参数一律不转给内核;
//   - WebSocket 只接受同源(Origin 的主机和 Host 一致,没有 Origin 的非浏览器客户端放行),挡跨站劫持。
package controllerproxy

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

const (
	// PrefixREST / PrefixWS 是前端用的两个前缀。
	PrefixREST = "/api/controller"
	PrefixWS   = "/api/controller-ws"
)

// Target 是内核控制接口的地址(host:port)和 secret。
type Target struct{ Addr, Secret string }

// Resolver 每次请求时取当前的控制接口(内核可能被重新部署成别的端口)。
type Resolver func() (Target, error)

// FileResolver 从正式配置文件里取,按文件的修改时间和大小缓存:没变就不重新解析(配置可能有几 MB,面板又轮询得很勤)。
func FileResolver(configFile string) Resolver {
	var mu sync.Mutex
	var cached Target
	var cachedErr error
	var mod time.Time
	var size int64 = -1
	return func() (Target, error) {
		mu.Lock()
		defer mu.Unlock()
		st, err := os.Stat(configFile)
		if err != nil {
			return Target{}, errors.New("读不到当前配置(内核还没部署过?)")
		}
		if st.ModTime().Equal(mod) && st.Size() == size {
			return cached, cachedErr
		}
		addr, secret, err := singbox.ControllerFromFile(configFile)
		mod, size = st.ModTime(), st.Size()
		cached, cachedErr = Target{Addr: addr, Secret: secret}, err
		return cached, cachedErr
	}
}

// Handler 是转发处理器,按前缀分别取 REST() 和 WS()。
type Handler struct {
	resolve   Resolver
	transport *http.Transport
}

// New 创建处理器。
func New(resolve Resolver) *Handler {
	return &Handler{resolve: resolve, transport: &http.Transport{
		Proxy:                 nil, // 控制接口在本机,不走任何代理环境变量
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		ResponseHeaderTimeout: 120 * time.Second, // 延迟测试等会等很久,和面板侧的超时对齐
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		DisableCompression:    true, // 不替浏览器改 Accept-Encoding,响应原样回传
	}}
}

// REST 处理 /api/controller/…。
func (h *Handler) REST() http.Handler { return h.handler(PrefixREST, false) }

// WS 处理 /api/controller-ws/…。
func (h *Handler) WS() http.Handler { return h.handler(PrefixWS, true) }

func badGateway(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (h *Handler) handler(prefix string, ws bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ws && !sameOrigin(r) {
			http.Error(w, "不允许跨站连接", http.StatusForbidden)
			return
		}
		escaped := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
		if escaped == "" {
			escaped = "/"
		}
		if !strings.HasPrefix(escaped, "/") { // 比如 /api/controllerx
			http.NotFound(w, r)
			return
		}
		path, err := url.PathUnescape(escaped)
		if err != nil || hasDotDot(path) {
			http.Error(w, "路径不合法", http.StatusBadRequest)
			return
		}
		t, err := h.resolve()
		if err != nil {
			badGateway(w, "内核控制接口不可用:"+err.Error())
			return
		}
		p := &httputil.ReverseProxy{
			Transport:     h.transport,
			FlushInterval: -1, // 流式响应(日志、流量)边到边转
			Rewrite: func(pr *httputil.ProxyRequest) {
				out := pr.Out
				out.URL.Scheme, out.URL.Host, out.Host = "http", t.Addr, t.Addr
				out.URL.Path, out.URL.RawPath = path, escaped
				// 浏览器自己带的凭据不转给内核;内核的 secret 由这里加
				out.Header.Del("Cookie")
				out.Header.Del("Authorization")
				out.Header.Del("Referer")
				q := pr.In.URL.Query()
				if q.Has("token") {
					q.Del("token")
					out.URL.RawQuery = q.Encode()
				}
				if t.Secret != "" {
					out.Header.Set("Authorization", "Bearer "+t.Secret)
					if ws { // WebSocket 握手时内核也认 token 参数
						q.Set("token", t.Secret)
						out.URL.RawQuery = q.Encode()
					}
				}
				if ws {
					out.Header.Set("Origin", "http://"+t.Addr)
				}
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				if errors.Is(err, r.Context().Err()) && r.Context().Err() != nil {
					return // 浏览器自己走了
				}
				badGateway(w, "内核控制接口连不上(内核没在运行?)")
			},
		}
		p.ServeHTTP(w, r)
	})
}

func hasDotDot(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// sameOrigin:浏览器发的 WebSocket 握手会带 Origin,只放行主机和 Host 一致的;没有 Origin 的(非浏览器客户端)放行。
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}
