// Package subscriptions 实现 /api/openbox/subscriptions*(订阅 CRUD、排序、刷新、预览;login 暂不支持)。
package subscriptions

import (
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

const maxBody = 16 << 20 // 粘贴的节点内容可能较大

// Deps 是本分组的依赖。
type Deps struct{ Subs *subs.Service }

type handlers struct{ s *subs.Service }

// Register 把本分组的接口挂到 mux 上。字面路径(preview / login / order)比 {id} 优先。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{s: d.Subs}
	mux.HandleFunc("/api/openbox/subscriptions", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.list, http.MethodPost: h.create,
	}))
	mux.HandleFunc("/api/openbox/subscriptions/preview", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.preview}))
	mux.HandleFunc("/api/openbox/subscriptions/login", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.login}))
	mux.HandleFunc("/api/openbox/subscriptions/order", httpx.Methods(map[string]http.HandlerFunc{http.MethodPut: h.order}))
	mux.HandleFunc("/api/openbox/subscriptions/{id}", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPatch: h.patch, http.MethodDelete: h.delete,
	}))
	mux.HandleFunc("/api/openbox/subscriptions/{id}/refresh", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.refresh}))
}

// decode 读请求体为 JSON 对象。字段逐个检查(parse),这样能区分「没传」和「传了 null」。
func decode(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	var raw map[string]any
	if !httpx.DecodeJSON(w, r, &raw, maxBody) {
		return nil, false
	}
	if raw == nil {
		httpx.Error(w, http.StatusBadRequest, "请求体必须是 JSON 对象")
		return nil, false
	}
	return raw, true
}

func str(raw map[string]any, k string) (*string, bool) {
	v, ok := raw[k]
	if !ok || v == nil {
		return nil, true
	}
	s, isStr := v.(string)
	if !isStr {
		return nil, false
	}
	return &s, true
}

func obj(raw map[string]any, k string) (m map[string]any, set bool, ok bool) {
	v, has := raw[k]
	if !has {
		return nil, false, true
	}
	if v == nil {
		return nil, true, true
	}
	m, isObj := v.(map[string]any)
	return m, true, isObj
}

// parse 把原始 JSON 转成 subs.Input 和预览用的来源。
func parse(w http.ResponseWriter, raw map[string]any) (subs.Input, subs.Source, bool) {
	fail := func(msg string) (subs.Input, subs.Source, bool) {
		httpx.Error(w, http.StatusBadRequest, msg)
		return subs.Input{}, subs.Source{}, false
	}
	var in subs.Input
	var src subs.Source
	for _, k := range []string{"name", "url", "content", "userAgent", "loginId"} {
		if _, ok := str(raw, k); !ok {
			return fail(k + " 必须是字符串")
		}
	}
	in.Name, _ = str(raw, "name")
	u, _ := str(raw, "url")
	c, _ := str(raw, "content")
	ua, _ := str(raw, "userAgent")
	lid, _ := str(raw, "loginId")
	if u != nil {
		src.URL = *u
	}
	if c != nil {
		src.Content = *c
	}
	if lid != nil {
		src.LoginID = *lid
	}
	if ua != nil {
		src.UserAgent = *ua
		in.UserAgent = ua
	}
	if v, ok := raw["urls"]; ok && v != nil {
		arr, isArr := v.([]any)
		if !isArr {
			return fail("urls 必须是字符串数组")
		}
		for _, e := range arr {
			s, isStr := e.(string)
			if !isStr {
				return fail("urls 必须是字符串数组")
			}
			src.URLs = append(src.URLs, s)
		}
	}
	if u != nil || c != nil || len(src.URLs) > 0 || src.LoginID != "" {
		cp := src
		in.Source = &cp
	}
	var ok bool
	if in.Rename, _, ok = obj(raw, "renameOptions"); !ok {
		return fail("renameOptions 必须是对象")
	}
	if in.AutoUpdate, in.AutoSet, ok = obj(raw, "autoUpdate"); !ok {
		return fail("autoUpdate 必须是对象或 null")
	}
	if in.NodeDNS, in.NodeDNSSet, ok = obj(raw, "nodeDns"); !ok {
		return fail("nodeDns 必须是对象或 null")
	}
	if v, has := raw["enabled"]; has && v != nil {
		b, isBool := v.(bool)
		if !isBool {
			return fail("enabled 必须是布尔值")
		}
		in.Enabled = &b
	}
	return in, src, true
}

func (h *handlers) fail(w http.ResponseWriter, err error, what string) {
	var ie *subs.InputError
	switch {
	case errors.As(err, &ie):
		httpx.Error(w, http.StatusBadRequest, ie.Msg)
	case errors.Is(err, subs.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, subs.ErrNoLogin):
		httpx.Error(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, sub.ErrEmpty), errors.Is(err, sub.ErrNoNodes), errors.Is(err, sub.ErrUnrecognized), errors.Is(err, sub.ErrClashInvalid):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	default:
		// 拉取失败(连不上、HTTP 非 2xx、超时)等:来自订阅服务器,用 502 区别于本机错误
		var fe *sub.FetchError
		if errors.As(err, &fe) {
			httpx.Error(w, http.StatusBadGateway, err.Error())
			return
		}
		httpx.Error(w, http.StatusInternalServerError, what+": "+err.Error())
	}
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	recs, err := h.s.List(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "读取订阅失败: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"subscriptions": recs})
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	raw, ok := decode(w, r)
	if !ok {
		return
	}
	in, _, ok := parse(w, raw)
	if !ok {
		return
	}
	res, err := h.s.Create(r.Context(), in)
	if err != nil {
		h.fail(w, err, "保存订阅失败")
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *handlers) preview(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	raw, ok := decode(w, r)
	if !ok {
		return
	}
	in, src, ok := parse(w, raw)
	if !ok {
		return
	}
	name := ""
	if in.Name != nil {
		name = *in.Name
	}
	res, err := h.s.Preview(r.Context(), src, name, in.Rename)
	if err != nil {
		h.fail(w, err, "预览订阅失败")
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *handlers) login(w http.ResponseWriter, _ *http.Request) {
	httpx.Error(w, http.StatusNotImplemented, subs.ErrNoLogin.Error())
}

func (h *handlers) patch(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	raw, ok := decode(w, r)
	if !ok {
		return
	}
	in, _, ok := parse(w, raw)
	if !ok {
		return
	}
	res, err := h.s.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		h.fail(w, err, "修改订阅失败")
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	changed, err := h.s.Delete(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err, "删除订阅失败")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

func (h *handlers) order(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var req struct {
		IDs []string `json:"ids"`
	}
	if !httpx.DecodeJSON(w, r, &req, 1<<20) {
		return
	}
	recs, changed, err := h.s.Reorder(r.Context(), req.IDs)
	if err != nil {
		h.fail(w, err, "排序失败")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "subscriptions": recs, "changed": changed})
}

func (h *handlers) refresh(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var req struct {
		Rename map[string]any `json:"renameOptions"`
	}
	if !httpx.DecodeJSON(w, r, &req, 1<<20) {
		return
	}
	res, err := h.s.Refresh(r.Context(), r.PathValue("id"), req.Rename)
	if err != nil {
		h.fail(w, err, "刷新订阅失败")
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
