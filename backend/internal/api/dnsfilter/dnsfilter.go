// Package dnsfilter 实现 /api/openbox/dns-filter 里的设置读写:GET(状态)和 PUT(保存设置)。
// 名单的预览 / 应用、查询记录、统计是 dns-filter* 的后续几条,做到时在这里继续挂。
package dnsfilter

import (
	"context"
	"io"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsfilter"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
)

const maxBody = 256 << 10

// Profiles 是本分组需要的档案读写(*profile.Service 实现它),测试里换假的。
type Profiles interface {
	Get(ctx context.Context) (map[string]any, error)
	Patch(ctx context.Context, patch map[string]any) (map[string]any, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Profiles Profiles }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{p: d.Profiles}
	mux.HandleFunc("/api/openbox/dns-filter", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.get,
		http.MethodPut: h.put,
	}))
}

type handlers struct{ p Profiles }

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	p, err := h.p.Get(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, dnsfilter.BuildStatus(dnsfilter.FromProfile(p)))
}

// put 校验后只改档案里的 dns.filter,别的档案内容不动;返回保存后的状态
// (前端不读返回值,给出来方便调试)。校验不过 400,档案不变。
func (h *handlers) put(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "请求体太大或读取失败")
		return
	}
	s, err := dnsfilter.ParseSettings(raw)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := h.p.Patch(r.Context(), map[string]any{"dns": map[string]any{"filter": s.ToMap()}})
	if err != nil {
		if ve, ok := err.(*profile.ValidationError); ok {
			httpx.Error(w, http.StatusBadRequest, ve.Msg)
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "保存设置失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, dnsfilter.BuildStatus(dnsfilter.FromProfile(p)))
}
