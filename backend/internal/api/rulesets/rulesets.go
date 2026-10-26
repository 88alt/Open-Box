// Package rulesets 实现 /api/openbox/rulesets/*。
//
// 已实现:preview、import(规则集链接指向的文本名单)。
// 未实现(501):entries(按规则库里的分类名查,要读随包的 geosite / geoip 并交给 sing-box 解码)、
// refresh(立即重拉重编,要先有配置生成与部署),等对应条目做完再接。
package rulesets

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

const (
	defaultLimit = 50
	maxLimit     = 500
	// maxImport 是 import 一次返回的条目上限(整份导入进站点集,太大没意义)。
	maxImport = 50000
)

// Deps 是本分组的依赖。
type Deps struct{ Lists *rulelist.Service }

type handlers struct{ l *rulelist.Service }

// Register 把本分组的接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{l: d.Lists}
	mux.HandleFunc("/api/openbox/rulesets/preview", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.preview}))
	mux.HandleFunc("/api/openbox/rulesets/import", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.importAll}))
	mux.HandleFunc("/api/openbox/rulesets/entries", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: notReady("按分类名查看规则集条目暂未实现(需要随包规则库与 sing-box)")}))
	mux.HandleFunc("/api/openbox/rulesets/refresh", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: notReady("规则集立即更新暂未实现(需要配置生成与部署)")}))
}

func notReady(msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		httpx.NoStore(w)
		httpx.Error(w, http.StatusNotImplemented, msg)
	}
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
