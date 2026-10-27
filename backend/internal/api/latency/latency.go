// Package latency 实现 /api/openbox/nodes/latency 和 /api/openbox/chain-proxies/latency(节点测速)。
//
// 测速由 internal/probe 完成:起一个只含被测出站的临时 sing-box,经本机 http 入站发请求并计时。
// 链式代理测速可带 ipUrls:经同一条线路再请求 IP 信息接口,把正文原样交回(前端按答话那一家的格式解析)。
// 没做的:延迟历史(请求里的 record、响应里的 history,见 STATUS 的 M3 条目)。
package latency

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

const (
	maxBody        = 16 << 20
	defaultTestURL = "https://www.gstatic.com/generate_204"
	chainProbeTag  = "ob-chain-probe"
)

// Profiles 读取完整档案(取 testUrl)。
type Profiles interface {
	Get(ctx context.Context) (map[string]any, error)
}

// Deps 是本分组的依赖。
type Deps struct {
	Gen      *singbox.Generator
	Subs     *subs.Service
	Profiles Profiles
	Runner   *probe.Runner
}

type handlers struct{ Deps }

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{d}
	mux.HandleFunc("/api/openbox/nodes/latency", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.nodes}))
	mux.HandleFunc("/api/openbox/chain-proxies/latency", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.chain}))
}

type storedJob struct {
	Tag    string `json:"tag"`
	URL    string `json:"url"`
	Record *bool  `json:"record"`
}

type nodesReq struct {
	// 预览(订阅还没保存):有 url / urls / content / loginId 之一
	URL           string         `json:"url"`
	URLs          []string       `json:"urls"`
	Content       string         `json:"content"`
	UserAgent     string         `json:"userAgent"`
	LoginID       string         `json:"loginId"`
	Name          string         `json:"name"`
	RenameOptions map[string]any `json:"renameOptions"`
	NodeDNS       map[string]any `json:"nodeDns"`
	Tags          []string       `json:"tags"`
	// 已保存节点
	Jobs     []storedJob `json:"jobs"`
	Progress bool        `json:"progress"`

	TimeoutMs int `json:"timeoutMs"`
}

func (r *nodesReq) isPreview() bool {
	return r.URL != "" || len(r.URLs) > 0 || r.Content != "" || r.LoginID != ""
}

func timeoutOf(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func (h *handlers) testURL(ctx context.Context) string {
	if p, err := h.Profiles.Get(ctx); err == nil {
		if u, _ := p["testUrl"].(string); strings.TrimSpace(u) != "" {
			return strings.TrimSpace(u)
		}
	}
	return defaultTestURL
}

// fail 把服务层错误转成响应。
func fail(w http.ResponseWriter, err error, what string) {
	var ce *core.CoreError
	var ie *subs.InputError
	var fe *sub.FetchError
	switch {
	case errors.Is(err, singbox.ErrCoreUnavailable):
		httpx.Error(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, probe.ErrNoSingBox):
		httpx.Error(w, http.StatusServiceUnavailable, "找不到 sing-box 可执行文件,无法测速")
	case errors.Is(err, subs.ErrNoLogin):
		httpx.Error(w, http.StatusNotImplemented, err.Error())
	case errors.As(err, &ce):
		httpx.Error(w, http.StatusBadRequest, ce.Message)
	case errors.As(err, &ie):
		httpx.Error(w, http.StatusBadRequest, ie.Msg)
	case errors.Is(err, sub.ErrEmpty), errors.Is(err, sub.ErrNoNodes), errors.Is(err, sub.ErrUnrecognized), errors.Is(err, sub.ErrClashInvalid):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &fe):
		httpx.Error(w, http.StatusBadGateway, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, what+": "+err.Error())
	}
}

func (h *handlers) nodes(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var req nodesReq
	if !httpx.DecodeJSON(w, r, &req, maxBody) {
		return
	}
	ctx := r.Context()
	var (
		built *singbox.Built
		jobs  []probe.Job
		err   error
	)
	if req.isPreview() {
		if len(req.Tags) == 0 {
			httpx.Error(w, http.StatusBadRequest, "tags 不能为空")
			return
		}
		if len(req.Tags) > probe.MaxJobs {
			httpx.Error(w, http.StatusBadRequest, "一次测的节点太多")
			return
		}
		src := subs.Source{URL: req.URL, URLs: req.URLs, Content: req.Content, UserAgent: req.UserAgent, LoginID: req.LoginID}
		nodes, err := h.Subs.PreviewNodes(ctx, src, req.Name, req.RenameOptions)
		if err != nil {
			fail(w, err, "解析订阅失败")
			return
		}
		ov, err := singbox.PreviewOverride(nodes, req.NodeDNS, req.Name)
		if err != nil {
			fail(w, err, "准备测速失败")
			return
		}
		if built, err = h.Gen.BuildWith(ctx, ov); err != nil {
			fail(w, err, "生成配置失败")
			return
		}
		u := h.testURL(ctx)
		for _, t := range req.Tags {
			jobs = append(jobs, probe.Job{Tag: t, URL: u})
		}
	} else {
		if len(req.Jobs) == 0 {
			httpx.Error(w, http.StatusBadRequest, "jobs 不能为空")
			return
		}
		if len(req.Jobs) > probe.MaxJobs {
			httpx.Error(w, http.StatusBadRequest, "一次测的节点太多")
			return
		}
		def := h.testURL(ctx)
		for _, j := range req.Jobs {
			u := strings.TrimSpace(j.URL)
			if u == "" {
				u = def
			}
			jobs = append(jobs, probe.Job{Tag: j.Tag, URL: u})
		}
		if built, err = h.Gen.Build(ctx); err != nil {
			fail(w, err, "生成配置失败")
			return
		}
	}

	tags := make([]string, len(jobs))
	for i, j := range jobs {
		tags[i] = j.Tag
	}
	m := probe.Subset(built.Config, tags)
	opt := probe.Options{Timeout: timeoutOf(req.TimeoutMs)}

	if req.Progress && !req.isPreview() {
		h.stream(w, r, m, jobs, opt)
		return
	}
	res, err := h.Runner.Run(ctx, m, jobs, opt)
	if err != nil {
		fail(w, err, "测速失败")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"results": res})
}

// stream 回 NDJSON:每测完一个任务一行 {"done":[下标]},最后一行是 {"results":[...]}(出错则 {"error":"..."})。
func (h *handlers) stream(w http.ResponseWriter, r *http.Request, m *probe.Material, jobs []probe.Job, opt probe.Options) {
	var mu sync.Mutex
	wrote := false
	fl, _ := w.(http.Flusher)
	line := func(v any) {
		b, _ := json.Marshal(v)
		if !wrote {
			w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			wrote = true
		}
		_, _ = w.Write(append(b, '\n'))
		if fl != nil {
			fl.Flush()
		}
	}
	opt.OnDone = func(i int) {
		mu.Lock()
		defer mu.Unlock()
		line(map[string]any{"done": []int{i}})
	}
	res, err := h.Runner.Run(r.Context(), m, jobs, opt)
	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		if !wrote { // 还没开始写:按普通错误回,状态码有意义
			fail(w, err, "测速失败")
			return
		}
		msg := err.Error()
		if errors.Is(err, probe.ErrNoSingBox) {
			msg = "找不到 sing-box 可执行文件,无法测速"
		}
		line(map[string]any{"error": msg})
		return
	}
	line(map[string]any{"results": res})
}

type chainReq struct {
	Link      string   `json:"link"`
	Upstream  string   `json:"upstream"`
	TestURL   string   `json:"testUrl"`
	TimeoutMs int      `json:"timeoutMs"`
	IPURLs    []string `json:"ipUrls"`
}

// IP 信息接口的白名单:前端设置里能选的三家。后端只放行这几个主机,免得「出口 IP」功能变成能让路由器
// 经任意线路去请求任意地址的入口。正文最多交回 16 KiB,和前端类型 OpenboxChainLatencyResult.ip 的约定一致。
var ipHosts = map[string]bool{"api.ip.sb": true, "ipwho.is": true, "api.ipapi.is": true}

const (
	maxIPURLs = 5
	ipBodyMax = 16 << 10
)

// normalizeIPURLs 校验并去重 ipUrls:必须是 https、主机在白名单里、没有账号、端口只能是 443。
// 错误信息里要带「ipUrls」:前端靠它认出「后端的白名单和这边对不上」,丢掉 IP 信息后重测一次。
func normalizeIPURLs(list []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range list {
		raw = strings.TrimSpace(raw)
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || !ipHosts[strings.ToLower(u.Hostname())] || (u.Port() != "" && u.Port() != "443") {
			return nil, errors.New("ipUrls 里有不支持的地址:只认 https 的 api.ip.sb、ipwho.is、api.ipapi.is")
		}
		u.Fragment = ""
		u.Host = strings.ToLower(u.Hostname()) // 主机名不分大小写、默认端口 443 省掉,「同一个地址」才能去重
		key := u.String()
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	if len(out) > maxIPURLs {
		return nil, errors.New("ipUrls 最多 5 个")
	}
	return out, nil
}

func (h *handlers) chain(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var req chainReq
	if !httpx.DecodeJSON(w, r, &req, 1<<20) {
		return
	}
	req.Link, req.Upstream = strings.TrimSpace(req.Link), strings.TrimSpace(req.Upstream)
	if req.Link == "" {
		httpx.Error(w, http.StatusBadRequest, "请填写链式代理的链接")
		return
	}
	if req.Upstream == "" {
		httpx.Error(w, http.StatusBadRequest, "请选择上游节点或节点组")
		return
	}
	ipURLs, err := normalizeIPURLs(req.IPURLs)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	ov, err := singbox.ChainOverride(chainProbeTag, req.Link, req.Upstream)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	built, err := h.Gen.BuildWith(ctx, ov)
	if err != nil {
		fail(w, err, "生成配置失败")
		return
	}
	u := strings.TrimSpace(req.TestURL)
	if u == "" {
		u = h.testURL(ctx)
	}
	m := probe.Subset(built.Config, []string{chainProbeTag})
	if !m.Has(chainProbeTag) {
		// 核心把它丢了(上游不存在、成环、协议生成不出出站):没有更具体的原因可报
		httpx.Error(w, http.StatusBadRequest, "这条链式代理生成不出出站,请检查上游是否存在、是否成环")
		return
	}
	// 测速和 IP 地区同一次起内核、并行跑;IP 这一项按 ipUrls 的顺序试到第一个成功
	jobs := []probe.Job{{Tag: chainProbeTag, URL: u}}
	if len(ipURLs) > 0 {
		jobs = append(jobs, probe.Job{Tag: chainProbeTag, URL: ipURLs[0], Capture: ipBodyMax, Fallback: ipURLs[1:]})
	}
	res, err := h.Runner.Run(ctx, m, jobs, probe.Options{Timeout: timeoutOf(req.TimeoutMs)})
	if err != nil {
		fail(w, err, "测速失败")
		return
	}
	out := map[string]any{"via": ResolveVia(built.Config, req.Upstream), "ok": res[0].OK}
	if len(res) > 1 {
		if res[1].OK {
			out["ip"] = map[string]any{"ok": true, "url": res[1].URL, "body": res[1].Body}
		} else {
			out["ip"] = map[string]any{"ok": false, "error": res[1].Error}
		}
	}
	if res[0].OK {
		out["ms"] = res[0].Ms
	} else {
		out["error"] = res[0].Error
		if res[0].Reason != "" {
			out["reason"] = res[0].Reason
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ResolveVia 静态地找出上游「此刻选中的节点」:selector 取 default(没有取第一个成员),urltest 取第一个成员,
// 逐层往下,直到不是组。没有运行中的内核,这只是配置里的默认选择;内核启用后(service 条目)应改读控制接口。
func ResolveVia(cfg map[string]any, tag string) string {
	byTag := map[string]map[string]any{}
	list, _ := cfg["outbounds"].([]any)
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			if t, _ := m["tag"].(string); t != "" {
				byTag[t] = m
			}
		}
	}
	cur := tag
	for i := 0; i < 16; i++ {
		m, ok := byTag[cur]
		if !ok {
			return cur
		}
		typ, _ := m["type"].(string)
		if typ != "selector" && typ != "urltest" {
			return cur
		}
		next, _ := m["default"].(string)
		members, _ := m["outbounds"].([]any)
		if next == "" && len(members) > 0 {
			next, _ = members[0].(string)
		}
		if next == "" {
			return cur
		}
		cur = next
	}
	return cur
}
