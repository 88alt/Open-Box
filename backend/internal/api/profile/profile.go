// Package profile 实现 /api/openbox/profile、/api/openbox/profile/defaults 和 /api/openbox/defaults/*。
package profile

import (
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
)

const maxPatchBody = 1 << 20 // 1 MiB

// Deps 是本分组的依赖。
type Deps struct{ Profile *profile.Service }

type handlers struct{ p *profile.Service }

// Register 把本分组的接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{p: d.Profile}
	mux.HandleFunc("/api/openbox/profile", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.get,
		http.MethodPut: h.put,
	}))
	mux.HandleFunc("/api/openbox/profile/defaults", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.regionDefaults,
	}))
	mux.HandleFunc("/api/openbox/defaults/routing", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.defaultRouting,
	}))
	mux.HandleFunc("/api/openbox/defaults/groups", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.defaultGroups,
	}))
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	p, err := h.p.Get(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"profile": p})
}

// put 把请求体当作补丁深度合并进档案(前端每次只发有改动的部分)。
func (h *handlers) put(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var patch map[string]any
	if !httpx.DecodeJSON(w, r, &patch, maxPatchBody) {
		return
	}
	if patch == nil {
		httpx.Error(w, http.StatusBadRequest, "请求体必须是 JSON 对象")
		return
	}
	p, err := h.p.Patch(r.Context(), patch)
	if err != nil {
		var ve *profile.ValidationError
		if errors.As(err, &ve) {
			httpx.Error(w, http.StatusBadRequest, ve.Msg)
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "保存档案失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"profile": p})
}

// regionDefaults 同时服务两个前端调用(fetchProfileDefaults 读 defaults,
// fetchDnsRewriteDefaults 读 dnsRewriteDefaults),所以两个字段都给。
func (h *handlers) regionDefaults(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	region := r.URL.Query().Get("region")
	if region == "" {
		region = profile.RegionCN
	}
	if !profile.ValidRegion(region) {
		httpx.Error(w, http.StatusBadRequest, "region 只能是 cn 或 intl")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"defaults":           profile.RegionDefaults(region),
		"dnsRewriteDefaults": profile.DefaultDNSRewrite(),
	})
}

func (h *handlers) defaultRouting(w http.ResponseWriter, _ *http.Request) {
	httpx.NoStore(w)
	httpx.JSON(w, http.StatusOK, map[string]any{"routing": profile.DefaultRouting()})
}

func (h *handlers) defaultGroups(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	groups, err := h.p.DefaultGroups(r.Context())
	if err != nil {
		if errors.Is(err, profile.ErrCoreUnavailable) {
			httpx.Error(w, http.StatusServiceUnavailable, "openbox-core 不可用,暂时无法生成默认节点组")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "生成默认节点组失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"groups": groups})
}
