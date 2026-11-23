// Package nodeinfo 实现 GET /api/openbox/node-info/{tag}(节点信息)、GET .../{tag}/entry-country(入口 IP 的国家)
// 和 POST .../{tag}/exit(出口 IP 归属和风控值),另有 GET .../sharing/{ip}(Ping0.cc 的共享人数 / 风控值 / IP 类型)。
package nodeinfo

import (
	"context"
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/nodeexit"
	ni "github.com/timeflysoon/Open-Box/backend/internal/nodeinfo"
	"github.com/timeflysoon/Open-Box/backend/internal/nodesharing"
	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// Service 是本分组要用的功能(*nodeinfo.Service 满足)。
type Service interface {
	Get(ctx context.Context, tag string) (*ni.Info, error)
	EntryCountry(ctx context.Context, tag string) (*ni.EntryResult, error)
}

// ExitService 是出口查询要用的功能(*nodeexit.Service 满足)。
type ExitService interface {
	Query(ctx context.Context, tag string, refresh bool) (*nodeexit.Result, error)
}

// SharingService 是共享人数查询要用的功能(*nodesharing.Service 满足)。
type SharingService interface {
	Query(ctx context.Context, ip string, refresh bool) (*nodesharing.Report, error)
}

// Deps 是本分组的依赖。Exit 为 nil 时不挂 exit 接口,Sharing 为 nil 时 sharing 回 404。
type Deps struct {
	Service Service
	Exit    ExitService
	Sharing SharingService
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service, x: d.Exit, sh: d.Sharing}
	mux.HandleFunc("/api/openbox/node-info/{tag}", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.get}))
	// 两段的路径只挂一条:Go 的路由器不允许 `sharing/{ip}` 和 `{tag}/exit` 并存(sharing/exit 两边都匹配、谁都不更具体),
	// 所以在 sub 里按名字分发。IP 不可能叫 exit / entry-country,所以 sharing 优先不会抢走节点的子路径。
	mux.HandleFunc("/api/openbox/node-info/{first}/{second}", h.sub)
}

func (h *handlers) sub(w http.ResponseWriter, r *http.Request) {
	first, second := r.PathValue("first"), r.PathValue("second")
	switch {
	case first == "sharing" && second != "exit" && second != "entry-country":
		httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.sharing})(w, r)
	case second == "entry-country":
		httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.entryCountry})(w, r)
	case second == "exit" && h.x != nil:
		httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.exit})(w, r)
	default:
		httpx.NotFound(w, r)
	}
}

type handlers struct {
	s  Service
	x  ExitService
	sh SharingService
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	info, err := h.s.Get(r.Context(), r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, ni.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, err.Error())
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, info)
}

// entryCountry:节点没有 → 404;没有可查的入口地址或第三方都查不到 → 502(前端遇到错误只是不显示国旗)。
func (h *handlers) entryCountry(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	res, err := h.s.EntryCountry(r.Context(), r.PathValue("first"))
	if err != nil {
		var noAddr *ni.ErrNoAddress
		switch {
		case errors.Is(err, ni.ErrNotFound):
			httpx.Error(w, http.StatusNotFound, err.Error())
		case errors.As(err, &noAddr):
			httpx.Error(w, http.StatusBadGateway, "查不了入口国家:"+err.Error())
		default:
			httpx.Error(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// exit:body 可以没有;{"refresh":true} 跳过缓存重查(15 秒内刚查过的仍按缓存答)。
// 节点没有可探测的出站 404;内核或 sing-box 不可用 503;经节点连不上 / 接口都没答 502。
// 只有出口 IP 拿到了才是 200,第三方两路各自没查到只记在 errors 里。
func (h *handlers) exit(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var req struct {
		Refresh bool `json:"refresh"`
	}
	if r.ContentLength != 0 && !httpx.DecodeJSON(w, r, &req, 4<<10) {
		return
	}
	res, err := h.x.Query(r.Context(), r.PathValue("first"), req.Refresh)
	if err != nil {
		var pe *nodeexit.ProbeError
		switch {
		case errors.Is(err, nodeexit.ErrNoOutbound), errors.Is(err, ni.ErrNotFound):
			httpx.Error(w, http.StatusNotFound, err.Error())
		case errors.Is(err, singbox.ErrCoreUnavailable):
			httpx.Error(w, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, probe.ErrNoSingBox):
			httpx.Error(w, http.StatusServiceUnavailable, "找不到 sing-box 可执行文件,无法经节点查询")
		case errors.As(err, &pe):
			httpx.Error(w, http.StatusBadGateway, "经这个节点没有查到出口地址:"+pe.Msg)
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			httpx.Error(w, http.StatusGatewayTimeout, "查询超时或已取消")
		default:
			httpx.Error(w, http.StatusInternalServerError, "查询出口失败:"+err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// sharing:GET /api/openbox/node-info/sharing/{ip}[?refresh=1]。IP 不合法 400;其余一律 200
// (ping0.cc 没答、页面不对、不是公网地址都是「什么也没查到」的空报告,要人工验证时带 error:"challenge")。
func (h *handlers) sharing(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if h.sh == nil {
		httpx.NotFound(w, r)
		return
	}
	rep, err := h.sh.Query(r.Context(), r.PathValue("second"), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		switch {
		case errors.Is(err, nodesharing.ErrInvalidIP):
			httpx.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			httpx.Error(w, http.StatusGatewayTimeout, "查询超时或已取消")
		default:
			httpx.Error(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, rep)
}
