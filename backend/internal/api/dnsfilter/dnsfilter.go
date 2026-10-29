// Package dnsfilter 实现 /api/openbox/dns-filter 里的设置读写(GET 状态、PUT 保存设置)、
// GET /api/openbox/dns-filter/preview(名单预览)和 POST /api/openbox/dns-filter/apply(下载名单并应用)。
// 以及 GET /api/openbox/dns-filter/records、/summary(dnsmasq 查询日志的记录与统计,数据来自 internal/dnsqlog)。
package dnsfilter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsfilter"
	"github.com/timeflysoon/Open-Box/backend/internal/dnsqlog"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

const maxBody = 256 << 10

// Profiles 是本分组需要的档案读写(*profile.Service 实现它),测试里换假的。
type Profiles interface {
	Get(ctx context.Context) (map[string]any, error)
	Patch(ctx context.Context, patch map[string]any) (map[string]any, error)
}

// Applier 是名单的状态与应用(*dnsfilter.Service 实现它),测试里换假的。
type Applier interface {
	Status(ctx context.Context) (dnsfilter.Status, error)
	Apply(ctx context.Context, update bool, listID string) (dnsfilter.Status, error)
}

// QueryLog 是查询记录与统计的来源(*dnsqlog.Log 满足),测试里换假的。
type QueryLog interface {
	Records(q dnsqlog.Query) dnsqlog.Page
	Summarize(now time.Time) dnsqlog.Summary
	Connected() bool
}

// Deps 是本分组的依赖。
type Deps struct {
	Profiles Profiles
	// Filter 提供带名单统计的状态和「应用」;为空时 GET 只回设置、apply 返回 503。
	Filter Applier
	// Preview 下载并解析一份名单、分页返回(*dnsfilter.Previewer.Preview);为空时 preview 返回 503。
	Preview func(ctx context.Context, q dnsfilter.PreviewQuery) (*dnsfilter.Preview, error)
	// Log 提供查询记录与统计;为空时两个接口照常回 200,只是没有数据、connected 为假。
	Log QueryLog
	// Now 给统计用,默认 time.Now。
	Now func() time.Time
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{p: d.Profiles, preview: d.Preview, filter: d.Filter, log: d.Log, now: d.Now}
	mux.HandleFunc("/api/openbox/dns-filter", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.get,
		http.MethodPut: h.put,
	}))
	mux.HandleFunc("/api/openbox/dns-filter/apply", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: h.apply,
	}))
	mux.HandleFunc("/api/openbox/dns-filter/preview", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.previewList,
	}))
	mux.HandleFunc("/api/openbox/dns-filter/records", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.records,
	}))
	mux.HandleFunc("/api/openbox/dns-filter/summary", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.summary,
	}))
}

type handlers struct {
	p       Profiles
	preview func(ctx context.Context, q dnsfilter.PreviewQuery) (*dnsfilter.Preview, error)
	filter  Applier
	log     QueryLog
	now     func() time.Time
}

// apply 对应前端 applyDnsFilter:POST {update?: bool, listId?: string}。
// update 为真重新下载启用的名单(带 listId 只重下那一份);之后编成规则集并部署。
// 已有一次应用在进行、或内核没在运行(名单已更新但没应用)409;名单一份都不可用、请求有问题 400;部署失败 500(消息里写着回滚情况)。
func (h *handlers) apply(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if h.filter == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "名单应用不可用")
		return
	}
	var req struct {
		Update bool   `json:"update"`
		ListID string `json:"listId"`
	}
	if raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10)); err != nil {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "请求体太大或读取失败")
		return
	} else if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "请求内容不是有效的 JSON")
			return
		}
	}
	st, err := h.filter.Apply(r.Context(), req.Update, strings.TrimSpace(req.ListID))
	if err != nil {
		var in *dnsfilter.InputError
		var ae *dnsfilter.ApplyError
		switch {
		case errors.Is(err, dnsfilter.ErrBusy), errors.Is(err, dnsfilter.ErrNotRunning):
			httpx.Error(w, http.StatusConflict, err.Error())
		case errors.Is(err, dnsfilter.ErrNoUsableList), errors.As(err, &in):
			httpx.Error(w, http.StatusBadRequest, err.Error())
		case errors.As(err, &ae):
			httpx.Error(w, http.StatusInternalServerError, ae.Msg)
		default:
			httpx.Error(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

// previewList 对应前端 fetchDnsFilterPreview:GET ?url=&search=&action=&page=&pageSize=。
// 只预览、不保存、不应用;地址和名单内容有问题 400,拉取失败 502。
func (h *handlers) previewList(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if h.preview == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "名单预览不可用")
		return
	}
	q := r.URL.Query()
	u := strings.TrimSpace(q.Get("url"))
	if u == "" {
		httpx.Error(w, http.StatusBadRequest, "缺少 url 参数")
		return
	}
	res, err := h.preview(r.Context(), dnsfilter.PreviewQuery{
		URL: u, Search: q.Get("search"), Action: q.Get("action"),
		Page: intParam(q.Get("page")), PageSize: intParam(q.Get("pageSize")),
	})
	if err != nil {
		var in *rulelist.InputError
		var fe *rulelist.FetchError
		switch {
		case errors.As(err, &in):
			httpx.Error(w, http.StatusBadRequest, in.Msg)
		case errors.As(err, &fe):
			httpx.Error(w, http.StatusBadGateway, fe.Msg)
		case errors.Is(err, dnsfilter.ErrNoRules), errors.Is(err, dnsfilter.ErrBinary), errors.Is(err, dnsfilter.ErrTooMany):
			httpx.Error(w, http.StatusBadRequest, err.Error())
		default:
			httpx.Error(w, http.StatusBadRequest, "名单解析失败: "+err.Error())
		}
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// intParam 解析正整数查询参数;不是数字按 0(由预览用默认值 / 收回到范围内)。
func intParam(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if h.filter != nil {
		st, err := h.filter.Status(r.Context())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.JSON(w, http.StatusOK, st)
		return
	}
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

// enabled 读「过滤是否启用」:有 Filter 时取它的状态,否则读档案。
func (h *handlers) enabled(ctx context.Context) (bool, error) {
	if h.filter != nil {
		st, err := h.filter.Status(ctx)
		return st.Settings.Enabled, err
	}
	p, err := h.p.Get(ctx)
	if err != nil {
		return false, err
	}
	return dnsfilter.FromProfile(p).Enabled, nil
}

// records 对应前端 fetchDnsFilterRecords:GET ?search=&result=&page=&pageSize=。
// 返回 dnsmasq 查询日志里最近的查询(新的在前),result 为 "" 是全部、"dnsmasq" 是 dnsmasq 自己答的(缓存和本地记录)、
// 其余按 blocked / allowed / error / unknown / cached / local 精确匹配;每页最多 100 条。没有日志来源时回空页,不报错。
func (h *handlers) records(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	q := r.URL.Query()
	size := intParam(q.Get("pageSize"))
	if h.log == nil {
		if size <= 0 {
			size = 20
		}
		httpx.JSON(w, http.StatusOK, dnsqlog.Page{Rows: []dnsqlog.Record{}, Page: 1, PageSize: size})
		return
	}
	httpx.JSON(w, http.StatusOK, h.log.Records(dnsqlog.Query{
		Search: q.Get("search"), Result: q.Get("result"), Page: intParam(q.Get("page")), PageSize: size,
	}))
}

// summary 对应前端 fetchDnsFilterSummary:最近 24 小时的查询数、拦截数、平均耗时、每小时曲线和热门域名。
// enabled 是过滤开关;connected 是查询日志是否接上了(没接上时数字全是 0)。
func (h *handlers) summary(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	enabled, err := h.enabled(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now
	if h.now != nil {
		now = h.now
	}
	var s dnsqlog.Summary
	connected := false
	if h.log != nil {
		s, connected = h.log.Summarize(now()), h.log.Connected()
	} else {
		s = dnsqlog.New().Summarize(now())
	}
	httpx.JSON(w, http.StatusOK, struct {
		Enabled   bool `json:"enabled"`
		Connected bool `json:"connected"`
		dnsqlog.Summary
	}{enabled, connected, s})
}
