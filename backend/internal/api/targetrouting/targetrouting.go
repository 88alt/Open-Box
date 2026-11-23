// Package targetrouting 实现 /api/openbox/profile/target-routing(连接页「改分流」):
// GET 按目标查命中的目标分流(?target=&type=&original=,或 ?target=&first=1 只取第一个命中),POST 加规则,DELETE 删规则。
// 逻辑在 internal/targetrouting;本包只做参数解析、状态码和 applied。
package targetrouting

import (
	"context"
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/targetrouting"
)

const maxBody = 64 << 10

// PendingFunc 判断「配置改了、内核还没用上」。内核没在运行、或判断不了时返回 (false, nil)
// (与服务状态的 pendingRestart、订阅保存的 applied 同一个口径)。
type PendingFunc func(ctx context.Context) (pending bool, reasons []string)

// Deps 是本分组的依赖。
type Deps struct {
	Service *targetrouting.Service
	Pending PendingFunc // 为空 = applied 恒为「没应用」
}

type handlers struct {
	s       *targetrouting.Service
	pending PendingFunc
}

// Register 把本分组的接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Service, pending: d.Pending}
	mux.HandleFunc("/api/openbox/profile/target-routing", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet:    h.get,
		http.MethodPost:   h.post,
		http.MethodDelete: h.del,
	}))
}

// fail 把服务层的错误翻成状态码。注意 404 不能用:前端把 404 当成「后端不支持此操作」。
func fail(w http.ResponseWriter, err error, what string) {
	var in *targetrouting.InputError
	var ve *profile.ValidationError
	switch {
	case errors.As(err, &in):
		httpx.Error(w, http.StatusBadRequest, in.Msg)
	case errors.As(err, &ve):
		httpx.Error(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, targetrouting.ErrCoreUnavailable):
		httpx.Error(w, http.StatusServiceUnavailable, "openbox-core 不可用,暂时无法查看目标分流")
	default:
		httpx.Error(w, http.StatusInternalServerError, what+": "+err.Error())
	}
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	q := r.URL.Query()
	first := q.Get("first") == "1"
	res, err := h.s.Find(r.Context(), q.Get("target"), q.Get("type"), first)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		fail(w, err, "查询目标分流失败")
		return
	}
	if first {
		httpx.JSON(w, http.StatusOK, res)
		return
	}
	httpx.JSON(w, http.StatusOK, res.Lookup)
}

type addBody struct {
	Target    string   `json:"target"`
	Type      string   `json:"type"`
	PolicyIDs []string `json:"policyIds"`
}

type pendingRestart struct {
	Pending bool     `json:"pending"`
	Reasons []string `json:"reasons"`
}

// applied 是保存之后「内核用上没有」(前端 OpenboxTargetRoutingApplied):增删只改档案、从不自动重启内核,
// 所以内核在跑时 ok=false、pendingRestart.pending=true;档案没变时 skipped="unchanged"。
type applied struct {
	OK             bool           `json:"ok"`
	Skipped        string         `json:"skipped,omitempty"`
	PendingRestart pendingRestart `json:"pendingRestart"`
}

func (h *handlers) applied(ctx context.Context, c *targetrouting.Change) applied {
	var pending bool
	var reasons []string
	if h.pending != nil {
		pending, reasons = h.pending(ctx)
	}
	if reasons == nil {
		reasons = []string{}
	}
	a := applied{OK: !pending, PendingRestart: pendingRestart{Pending: pending, Reasons: reasons}}
	if !c.Changed {
		a.Skipped = "unchanged"
	}
	return a
}

func (h *handlers) reply(w http.ResponseWriter, r *http.Request, c *targetrouting.Change, err error, what string) {
	if err != nil {
		fail(w, err, what)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"applied": h.applied(r.Context(), c)})
}

func (h *handlers) post(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var b addBody
	if !httpx.DecodeJSON(w, r, &b, maxBody) {
		return
	}
	c, err := h.s.Add(r.Context(), b.Target, b.Type, b.PolicyIDs)
	h.reply(w, r, c, err, "保存目标分流失败")
}

func (h *handlers) del(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var b targetrouting.RemoveRequest
	if !httpx.DecodeJSON(w, r, &b, maxBody) {
		return
	}
	c, err := h.s.Remove(r.Context(), b)
	h.reply(w, r, c, err, "删除目标分流规则失败")
}
