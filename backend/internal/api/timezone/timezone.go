// Package timezone 实现 GET / PUT /api/openbox/system/timezone。
package timezone

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	tz "github.com/timeflysoon/Open-Box/backend/internal/timezone"
)

// Service 是本分组要用的功能(*timezone.Service 满足)。
type Service interface {
	Get(ctx context.Context) (tz.State, error)
	Set(ctx context.Context, zone string) (tz.State, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Service Service }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/system/timezone", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.get, http.MethodPut: h.put,
	}))
}

type handlers struct{ s Service }

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	st, err := h.s.Get(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

// put:请求体 {zone};别名先换成名单里的名字,不认识 400;这台机器不支持改 501;其它失败 500(消息里写着有没有恢复)。
func (h *handlers) put(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "请求体太大或读取失败")
		return
	}
	var req struct {
		Zone string `json:"zone"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "请求内容不是有效的 JSON")
		return
	}
	st, err := h.s.Set(r.Context(), req.Zone)
	switch {
	case err == nil:
		httpx.JSON(w, http.StatusOK, st)
	case errors.Is(err, tz.ErrUnknownZone):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, tz.ErrUnsupported):
		httpx.Error(w, http.StatusNotImplemented, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, err.Error())
	}
}
