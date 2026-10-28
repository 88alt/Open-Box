// Package subshares 实现 /api/openbox/subscription-shares*:订阅分享的列表、新建、修改、换 token、删除。
// 响应形状取自前端 api/openbox.ts,没有参照上游:列表 { shares },新建 / 修改 / 换 token { share },删除 { ok: true }。
package subshares

import (
	"context"
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/subshare"
)

// Service 是本分组要用的功能(*subshare.Service 满足)。
type Service interface {
	List(ctx context.Context) ([]subshare.Share, error)
	Create(ctx context.Context, in subshare.Input) (subshare.Share, error)
	Update(ctx context.Context, id string, in subshare.Input) (subshare.Share, error)
	Regenerate(ctx context.Context, id string) (subshare.Share, error)
	Delete(ctx context.Context, id string) error
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

const maxBody = 64 << 10

type handlers struct{ s Service }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/subscription-shares", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.list, http.MethodPost: h.create,
	}))
	mux.HandleFunc("/api/openbox/subscription-shares/{id}", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPatch: h.patch, http.MethodDelete: h.delete,
	}))
	mux.HandleFunc("/api/openbox/subscription-shares/{id}/regenerate", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: h.regenerate,
	}))
}

// body 是新建 / 修改的请求体;指针字段区分「没传」和「传了」。
type body struct {
	Name            *string   `json:"name"`
	Host            *string   `json:"host"`
	Protocol        *string   `json:"protocol"`
	SubscriptionIDs *[]string `json:"subscriptionIds"`
	Token           *string   `json:"token"`
	Enabled         *bool     `json:"enabled"`
	Regenerate      bool      `json:"regenerate"`
}

func (b body) input() subshare.Input {
	return subshare.Input{
		Name: b.Name, Host: b.Host, Protocol: b.Protocol, SubscriptionIDs: b.SubscriptionIDs,
		Token: b.Token, Enabled: b.Enabled, Regenerate: b.Regenerate,
	}
}

func (h *handlers) fail(w http.ResponseWriter, err error, fallback string) {
	var ie *subshare.InputError
	switch {
	case errors.As(err, &ie):
		httpx.Error(w, http.StatusBadRequest, ie.Msg)
	case errors.Is(err, subshare.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, fallback+": "+err.Error())
	}
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	shares, err := h.s.List(r.Context())
	if err != nil {
		h.fail(w, err, "读取订阅分享失败")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"shares": shares})
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var b body
	if !httpx.DecodeJSON(w, r, &b, maxBody) {
		return
	}
	sh, err := h.s.Create(r.Context(), b.input())
	if err != nil {
		h.fail(w, err, "新建订阅分享失败")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"share": sh})
}

func (h *handlers) patch(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var b body
	if !httpx.DecodeJSON(w, r, &b, maxBody) {
		return
	}
	in := b.input()
	in.Token = nil // token 只能由后端换,不收客户端指定的新值
	sh, err := h.s.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		h.fail(w, err, "修改订阅分享失败")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"share": sh})
}

func (h *handlers) regenerate(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	sh, err := h.s.Regenerate(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err, "重新生成链接失败")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"share": sh})
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if err := h.s.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err, "删除订阅分享失败")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}
