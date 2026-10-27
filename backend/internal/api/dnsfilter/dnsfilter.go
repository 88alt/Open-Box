// Package dnsfilter 实现 /api/openbox/dns-filter 里的设置读写(GET 状态、PUT 保存设置)
// 和 GET /api/openbox/dns-filter/preview(名单预览)。
// 名单的应用、查询记录、统计是 dns-filter* 的后续几条,做到时在这里继续挂。
package dnsfilter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsfilter"
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

// Deps 是本分组的依赖。
type Deps struct {
	Profiles Profiles
	// Preview 下载并解析一份名单、分页返回(*dnsfilter.Previewer.Preview);为空时 preview 返回 503。
	Preview func(ctx context.Context, q dnsfilter.PreviewQuery) (*dnsfilter.Preview, error)
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{p: d.Profiles, preview: d.Preview}
	mux.HandleFunc("/api/openbox/dns-filter", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.get,
		http.MethodPut: h.put,
	}))
	mux.HandleFunc("/api/openbox/dns-filter/preview", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: h.previewList,
	}))
}

type handlers struct {
	p       Profiles
	preview func(ctx context.Context, q dnsfilter.PreviewQuery) (*dnsfilter.Preview, error)
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
