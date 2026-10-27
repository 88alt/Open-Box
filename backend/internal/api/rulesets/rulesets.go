// Package rulesets 实现 /api/openbox/rulesets/*。
//
// 已实现:preview、import(规则集链接指向的文本名单)、entries(按随包规则库里的分类名查,
// 由 internal/geodata 交给 sing-box 解码)、refresh(对一个链接立即重拉重编 .srs,
// 由 internal/singbox 的 Refresher 做;只改规则集文件,不重启、不部署)。
package rulesets

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/geodata"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

const (
	defaultLimit = 50
	maxLimit     = 500
	// maxImport 是 import 一次返回的条目上限(整份导入进站点集,太大没意义)。
	maxImport = 50000
)

// GeoEntries 是随包规则库(测试里换成假的)。
type GeoEntries interface {
	Entries(ctx context.Context, tag string) (*geodata.Result, error)
}

// Refresher 对单个规则集链接立即重拉重编(测试里换成假的)。
type Refresher interface {
	Refresh(ctx context.Context, rawURL string) (*singbox.RefreshResult, error)
}

// maxRefreshBody 是 refresh 请求体上限(只有一个链接)。
const maxRefreshBody = 8 << 10

// Deps 是本分组的依赖。Geo 为空时 entries 返回 503;Refresh 为空时 refresh 返回 503。
type Deps struct {
	Lists   *rulelist.Service
	Geo     GeoEntries
	Refresh Refresher
}

type handlers struct {
	l *rulelist.Service
	g GeoEntries
	r Refresher
}

// Register 把本分组的接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{l: d.Lists, g: d.Geo, r: d.Refresh}
	mux.HandleFunc("/api/openbox/rulesets/preview", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.preview}))
	mux.HandleFunc("/api/openbox/rulesets/import", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.importAll}))
	mux.HandleFunc("/api/openbox/rulesets/entries", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.entries}))
	mux.HandleFunc("/api/openbox/rulesets/refresh", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.refresh}))
}

func (h *handlers) load(w http.ResponseWriter, r *http.Request) (string, *rulelist.Result, bool) {
	httpx.NoStore(w)
	u := strings.TrimSpace(r.URL.Query().Get("url"))
	if u == "" {
		httpx.Error(w, http.StatusBadRequest, "缺少 url 参数")
		return "", nil, false
	}
	res, err := h.l.Load(r.Context(), u)
	if err != nil {
		var in *rulelist.InputError
		var fe *rulelist.FetchError
		switch {
		case errors.As(err, &in):
			httpx.Error(w, http.StatusBadRequest, in.Msg)
		case errors.As(err, &fe):
			httpx.Error(w, http.StatusBadGateway, fe.Msg)
		case errors.Is(err, rulelist.ErrBinary), errors.Is(err, rulelist.ErrEmpty):
			httpx.Error(w, http.StatusBadRequest, err.Error())
		default:
			httpx.Error(w, http.StatusBadRequest, "规则集解析失败: "+err.Error())
		}
		return "", nil, false
	}
	return u, res, true
}

func intParam(r *http.Request, k string, def, min, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(k))
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// preview:分页 + 关键词过滤。total 是名单总条数,matched 是命中关键词的条数。
func (h *handlers) preview(w http.ResponseWriter, r *http.Request) {
	u, res, ok := h.load(w, r)
	if !ok {
		return
	}
	hits := rulelist.Filter(res.Entries, r.URL.Query().Get("q"))
	offset := intParam(r, "offset", 0, 0, len(hits))
	limit := intParam(r, "limit", defaultLimit, 1, maxLimit)
	end := offset + limit
	if end > len(hits) {
		end = len(hits)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"url": u, "total": len(res.Entries), "matched": len(hits),
		"offset": offset, "limit": limit, "entries": hits[offset:end],
	})
}

// importAll:一次返回全部条目(保存站点集后不再依赖原链接)。
func (h *handlers) importAll(w http.ResponseWriter, r *http.Request) {
	u, res, ok := h.load(w, r)
	if !ok {
		return
	}
	if len(res.Entries) > maxImport {
		httpx.Error(w, http.StatusBadRequest, "名单条目太多(超过 "+strconv.Itoa(maxImport)+" 条),不适合整份导入;请改用「规则集链接」引用")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"url": u, "total": len(res.Entries), "matched": len(res.Entries),
		"offset": 0, "limit": len(res.Entries), "entries": res.Entries,
	})
}

// entries:按随包规则库里的分类名(geosite-cn、geoip-cn 等)查条目,形状与 preview 一致,
// 多一个 tag。分页 + 关键词过滤:total 是分类总条数,matched 是命中关键词的条数。
func (h *handlers) entries(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if h.g == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "规则库未启用")
		return
	}
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	if tag == "" {
		httpx.Error(w, http.StatusBadRequest, "缺少 tag 参数")
		return
	}
	res, err := h.g.Entries(r.Context(), tag)
	if err != nil {
		var de *geodata.DecodeError
		switch {
		case errors.Is(err, geodata.ErrInvalidTag):
			httpx.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, geodata.ErrNotFound):
			httpx.Error(w, http.StatusNotFound, "规则库里没有分类 "+tag)
		case errors.Is(err, geodata.ErrNoLibrary):
			httpx.Error(w, http.StatusServiceUnavailable, "随包规则库目录不存在(安装不完整,或 -geo-dir 路径不对)")
		case errors.Is(err, geodata.ErrNoSingBox):
			httpx.Error(w, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			httpx.Error(w, http.StatusServiceUnavailable, "请求已取消或超时")
		case errors.As(err, &de):
			httpx.Error(w, http.StatusInternalServerError, de.Msg)
		default:
			httpx.Error(w, http.StatusInternalServerError, "读取规则库失败: "+err.Error())
		}
		return
	}
	hits := rulelist.Filter(res.Entries, r.URL.Query().Get("q"))
	offset := intParam(r, "offset", 0, 0, len(hits))
	limit := intParam(r, "limit", defaultLimit, 1, maxLimit)
	end := offset + limit
	if end > len(hits) {
		end = len(hits)
	}
	body := map[string]any{
		"tag": tag, "total": len(res.Entries), "matched": len(hits),
		"offset": offset, "limit": limit, "entries": hits[offset:end],
	}
	if res.Truncated {
		body["truncated"] = true // 分类条目超过上限,只列了前面的
	}
	httpx.JSON(w, http.StatusOK, body)
}

// refresh:对一个规则集链接立即重拉重编。只改规则集文件;needsRestart 为真时由前端提示用户重启(重新部署)。
func (h *handlers) refresh(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if h.r == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "规则集立即更新未启用")
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if !httpx.DecodeJSON(w, r, &body, maxRefreshBody) {
		return
	}
	res, err := h.r.Refresh(r.Context(), body.URL)
	if err != nil {
		var ri *singbox.RefreshInputError
		var rb *singbox.RefreshBuildError
		var in *rulelist.InputError
		var fe *rulelist.FetchError
		switch {
		case errors.As(err, &ri):
			httpx.Error(w, http.StatusBadRequest, ri.Msg)
		case errors.As(err, &in):
			httpx.Error(w, http.StatusBadRequest, in.Msg)
		case errors.As(err, &fe):
			httpx.Error(w, http.StatusBadGateway, fe.Msg)
		case errors.Is(err, rulelist.ErrBinary), errors.Is(err, rulelist.ErrEmpty):
			httpx.Error(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, singbox.ErrNoSingBox):
			httpx.Error(w, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			httpx.Error(w, http.StatusServiceUnavailable, "请求已取消或超时")
		case errors.As(err, &rb):
			httpx.Error(w, http.StatusInternalServerError, rb.Msg)
		default:
			httpx.Error(w, http.StatusInternalServerError, "规则集更新失败: "+err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
