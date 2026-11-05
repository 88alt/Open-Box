// Package contentfilter 实现 /api/openbox/content-filter*:内容过滤规则集的列表、新建 / 更新、启停、删除、预览、恢复默认、取文本。
// 响应形状取自前端 api/clientApp.ts,没有参照上游:新建 / 更新 / 启停 / 删除 / 恢复默认都回整份最新列表 { sets, sha256, size }。
package contentfilter

import (
	"context"
	"errors"
	"net/http"

	cf "github.com/timeflysoon/Open-Box/backend/internal/contentfilter"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

// Service 是本分组要用的功能(*contentfilter.Service 满足)。
type Service interface {
	List(ctx context.Context) (cf.List, error)
	Source(ctx context.Context, id string) (string, error)
	Preview(ctx context.Context, in cf.PreviewInput) (string, error)
	Save(ctx context.Context, in cf.SaveInput) (cf.List, error)
	SetEnabled(ctx context.Context, id string, enabled bool) (cf.List, error)
	Delete(ctx context.Context, id string) (cf.List, error)
	RestoreDefaults(ctx context.Context) (cf.List, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

// 请求体上限:规则文本最大 512 KiB,加上 JSON 转义和其它字段留足余量。
const maxBody = 2 << 20

type handlers struct{ s Service }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/content-filter", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.list, http.MethodPost: h.save,
	}))
	mux.HandleFunc("/api/openbox/content-filter/preview", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: h.preview,
	}))
	mux.HandleFunc("/api/openbox/content-filter/restore-defaults", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: h.restore,
	}))
	mux.HandleFunc("/api/openbox/content-filter/{id}", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPatch: h.patch, http.MethodDelete: h.delete,
	}))
	mux.HandleFunc("/api/openbox/content-filter/{id}/source", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.source,
	}))
}

func fail(w http.ResponseWriter, err error, fallback string) {
	var ie *cf.InputError
	var ri *rulelist.InputError
	var fe *rulelist.FetchError
	switch {
	case errors.As(err, &ie):
		httpx.Error(w, http.StatusBadRequest, ie.Msg)
	case errors.As(err, &ri):
		httpx.Error(w, http.StatusBadRequest, ri.Msg)
	case errors.As(err, &fe):
		httpx.Error(w, http.StatusBadGateway, fe.Msg)
	case errors.Is(err, cf.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, cf.ErrUnsupported):
		httpx.Error(w, http.StatusNotImplemented, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, fallback+": "+err.Error())
	}
}

func (h *handlers) reply(w http.ResponseWriter, l cf.List, err error, fallback string) {
	if err != nil {
		fail(w, err, fallback)
		return
	}
	httpx.JSON(w, http.StatusOK, l)
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	l, err := h.s.List(r.Context())
	h.reply(w, l, err, "读取内容过滤失败")
}

type saveBody struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Dialect   string         `json:"dialect"`
	SourceURL string         `json:"sourceUrl"`
	Text      *string        `json:"text"`
	Arguments map[string]any `json:"arguments"`
}

func (h *handlers) save(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var b saveBody
	if !httpx.DecodeJSON(w, r, &b, maxBody) {
		return
	}
	l, err := h.s.Save(r.Context(), cf.SaveInput{
		ID: b.ID, Name: b.Name, Dialect: b.Dialect, SourceURL: b.SourceURL, Text: b.Text, Arguments: b.Arguments,
	})
	h.reply(w, l, err, "保存内容过滤失败")
}

func (h *handlers) preview(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var b saveBody
	if !httpx.DecodeJSON(w, r, &b, maxBody) {
		return
	}
	text, err := h.s.Preview(r.Context(), cf.PreviewInput{Dialect: b.Dialect, SourceURL: b.SourceURL, Arguments: b.Arguments})
	if err != nil {
		fail(w, err, "解析规则链接失败")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"text": text})
}

func (h *handlers) patch(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if !httpx.DecodeJSON(w, r, &b, 4<<10) {
		return
	}
	if b.Enabled == nil {
		httpx.Error(w, http.StatusBadRequest, "缺少 enabled")
		return
	}
	l, err := h.s.SetEnabled(r.Context(), r.PathValue("id"), *b.Enabled)
	h.reply(w, l, err, "修改内容过滤失败")
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	l, err := h.s.Delete(r.Context(), r.PathValue("id"))
	h.reply(w, l, err, "删除内容过滤失败")
}

func (h *handlers) restore(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	l, err := h.s.RestoreDefaults(r.Context())
	h.reply(w, l, err, "恢复默认失败")
}

func (h *handlers) source(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	text, err := h.s.Source(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err, "读取规则文本失败")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"text": text})
}
