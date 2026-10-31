// Package backup 实现 GET /api/openbox/backup(导出)。POST /backup/import(导入)还没做,如实回 501。
// 响应形状和文件格式见 internal/backup 的包说明。
package backup

import (
	"context"
	"net/http"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/backup"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
)

// Exporter 是导出功能(*backup.Service 满足)。
type Exporter interface {
	Export(ctx context.Context, opts backup.Options) (*backup.Backup, error)
}

// Deps 是本分组的依赖。
type Deps struct{ Exporter Exporter }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{e: d.Exporter}
	mux.HandleFunc("/api/openbox/backup", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.export}))
	mux.HandleFunc("/api/openbox/backup/import", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.importNotReady}))
}

type handlers struct{ e Exporter }

// export:?subscriptions=&chainProxies=&clientRoutes=&servers=,取值 1 / 0(也认 true / false)。
// 没传的:链式代理按带上算(前端注释:老调用方不传按带上算),订阅、终端分流、共享网络按不带算。
func (h *handlers) export(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	q := r.URL.Query()
	opts := backup.Options{
		Subscriptions: flag(q.Get("subscriptions"), false),
		ChainProxies:  flag(q.Get("chainProxies"), true),
		ClientRoutes:  flag(q.Get("clientRoutes"), false),
		Servers:       flag(q.Get("servers"), false),
	}
	b, err := h.e.Export(r.Context(), opts)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, b)
}

func (h *handlers) importNotReady(w http.ResponseWriter, _ *http.Request) {
	httpx.Error(w, http.StatusNotImplemented, "导入备份还没实现,暂时只能导出")
}

func flag(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true":
		return true
	case "0", "false":
		return false
	}
	return def
}
