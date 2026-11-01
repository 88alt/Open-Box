// Package backup 实现 GET /api/openbox/backup(导出)和 POST /api/openbox/backup/import(导入)。
// 文件格式见 internal/backup 的包说明,导入的规则见 internal/backup/import.go。
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/backup"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

// maxImportBody 是导入请求体上限:背景图最大 12 MiB,加上订阅节点和档案,32 MiB 足够。
const maxImportBody = 32 << 20

// Exporter 是导出功能(*backup.Service 满足)。
type Exporter interface {
	Export(ctx context.Context, opts backup.Options) (*backup.Backup, error)
}

// Importer 是导入功能(*backup.Service 满足)。
type Importer interface {
	Import(ctx context.Context, f *backup.ImportFile, mode subs.RestoreMode) (*backup.ImportResult, error)
}

// Deps 是本分组的依赖。Importer 为空时导入回 501。
type Deps struct {
	Exporter Exporter
	Importer Importer
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{e: d.Exporter, i: d.Importer}
	mux.HandleFunc("/api/openbox/backup", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.export}))
	mux.HandleFunc("/api/openbox/backup/import", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.importBackup}))
}

type handlers struct {
	e Exporter
	i Importer
}

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

// importBackup:POST ?subscriptions=replace|append,请求体是备份文件(OpenboxBackup)。
// 状态码:400 文件有问题(什么都没写)、409 已有导入在进行、413 太大、503 核心不可用、
// 500 写入中途失败(信息里写明已经写到哪一步)。
func (h *handlers) importBackup(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if h.i == nil {
		httpx.Error(w, http.StatusNotImplemented, "导入备份没有接入")
		return
	}
	mode := subs.RestoreMode(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("subscriptions"))))
	if mode == "" {
		mode = subs.RestoreReplace
	}
	if mode != subs.RestoreReplace && mode != subs.RestoreAppend {
		httpx.Error(w, http.StatusBadRequest, "subscriptions 只能是 replace 或 append")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "备份文件太大了")
		} else {
			httpx.Error(w, http.StatusBadRequest, "读取备份文件失败")
		}
		return
	}
	var f backup.ImportFile
	if err := json.Unmarshal(body, &f); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			httpx.Error(w, http.StatusBadRequest, "备份文件格式不对:「"+te.Field+"」的类型不对")
		} else {
			httpx.Error(w, http.StatusBadRequest, "备份文件不是有效的 JSON")
		}
		return
	}
	res, err := h.i.Import(r.Context(), &f, mode)
	if err != nil {
		var ie *backup.InputError
		var pe *backup.PartialError
		switch {
		case errors.As(err, &ie):
			httpx.Error(w, http.StatusBadRequest, ie.Msg)
		case errors.Is(err, backup.ErrBusy):
			httpx.Error(w, http.StatusConflict, err.Error())
		case errors.As(err, &pe):
			httpx.Error(w, http.StatusInternalServerError, pe.Error())
		case errors.Is(err, nodegroups.ErrCoreUnavailable):
			httpx.Error(w, http.StatusServiceUnavailable, err.Error())
		default:
			httpx.Error(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
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
