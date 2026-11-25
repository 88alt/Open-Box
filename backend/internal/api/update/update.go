// Package update 挂「组件更新」的五个接口:GET /api/openbox/update/status、/check、/notes,POST /update/run、/cancel。
// 契约见 docs/ENDPOINTS.md 与前端 api/openbox.ts 的 OpenboxUpdateStatus 等类型。
// run 现在走「探测 → 下载并解压到暂存 → 校验 → 冒烟」,提交(替换文件并重启)是 M6-6;
// 已经有一次升级在跑时回 409。
package update

import (
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/update"
)

// Deps 是本分组的依赖。
type Deps struct {
	Service *update.Service
}

// Register 挂路由。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service}
	mux.HandleFunc("/api/openbox/update/status", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.status}))
	mux.HandleFunc("/api/openbox/update/check", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.check}))
	mux.HandleFunc("/api/openbox/update/notes", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.notes}))
	mux.HandleFunc("/api/openbox/update/run", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.run}))
	mux.HandleFunc("/api/openbox/update/cancel", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.cancel}))
}

type handlers struct{ s *update.Service }

func (h *handlers) status(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	httpx.JSON(w, http.StatusOK, h.s.Status(r.Context()))
}

// check:联网读发布列表。读不到回 502 和原因(前端 check 失败时把它显示出来)。
func (h *handlers) check(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	res, err := h.s.Check(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "检查更新失败:"+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// notes:永远回 200;取不到时 note 为 null、error 写原因。
func (h *handlers) notes(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	httpx.JSON(w, http.StatusOK, h.s.Notes(r.Context(), r.URL.Query().Get("latest")))
}

// run:开始一次升级。已经在升级时回 409(M6 第七节:同一时刻只一个升级)。
// 接口立即返回,进度靠轮询 status。
func (h *handlers) run(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Channel string `json:"channel"`
	}
	if !httpx.DecodeJSON(w, r, &req, 4<<10) {
		return
	}
	if err := h.s.Run(req.Channel); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, update.ErrBusy) {
			status = http.StatusConflict
		}
		httpx.Error(w, status, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "output": ""})
}

func (h *handlers) cancel(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"result": h.s.Cancel()})
}
