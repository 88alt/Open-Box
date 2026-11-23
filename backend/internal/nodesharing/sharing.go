// Package nodesharing 实现 GET /api/openbox/node-info/sharing/{ip} 背后的查询:
// 向 ping0.cc 问一个出口 IP 的「共享人数区间 / 风控值 / IP 类型」。
//
// 对齐上游 v0.1.462(最后一版明文后端)的行为:从路由器本机直接访问 https://ping0.cc/ip/<ip>(不经节点),
// 从页面 HTML 里抠三样东西;不是公网地址、页面不是这个 IP 的、取不到都按「什么也没查到」处理,
// 只有页面要求人工验证(Cloudflare Turnstile)时带 error:"challenge",前端据此显示验证链接。
package nodesharing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// 有数据的结果存一天,什么都没查到(含被要求验证)只存 5 分钟,免得面板每次打开都去敲 ping0.cc。
	hitTTL  = 24 * time.Hour
	missTTL = 5 * time.Minute
	maxKeep = 512
	// 页面大小上限和单次请求时限(与上游一致)。
	maxBody  = 512 * 1024
	fetchTTL = 6 * time.Second
)

// ErrInvalidIP 是请求里的 IP 不是合法地址(接口回 400)。
var ErrInvalidIP = errors.New("无效的 IP 地址")

// Risk 是 ping0.cc 的风控值:百分比和它落在哪一档(档位认不出时为 nil)。
type Risk struct {
	Tier    *string `json:"tier"`
	Percent float64 `json:"percent"`
}

// Report 对应前端 OpenboxNodeSharing。没查到的项是 null / 空数组,不是缺字段。
type Report struct {
	Range   *string  `json:"range"`
	Risk    *Risk    `json:"risk"`
	IPTypes []string `json:"ipTypes"`
	Error   string   `json:"error,omitempty"`
}

func (r *Report) hasData() bool { return r.Range != nil || r.Risk != nil || len(r.IPTypes) > 0 }

// Service 查询并缓存。零值不可用,至少要设 IsPublic;其余字段为空用默认。
type Service struct {
	// IsPublic 判断地址能不能拿去问(内网、回环等不问,直接当没查到)。
	IsPublic func(netip.Addr) bool
	// Client 为空用不跟随重定向、6 秒超时的默认客户端。
	Client *http.Client
	// BaseURL 为空是 https://ping0.cc/ip/(测试时指向假服务)。
	BaseURL string
	Now     func() time.Time

	mu       sync.Mutex
	cache    map[netip.Addr]cached
	inflight map[netip.Addr]*call
}

type cached struct {
	rep *Report
	at  time.Time
	ttl time.Duration
}

type call struct {
	done chan struct{}
	rep  *Report
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Query 返回这个 IP 的报告。refresh 跳过缓存重新查(同一个 IP 正在查时仍合并成一次)。
func (s *Service) Query(ctx context.Context, ip string, refresh bool) (*Report, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || addr.Zone() != "" {
		return nil, ErrInvalidIP
	}
	addr = addr.Unmap()

	s.mu.Lock()
	if !refresh {
		if c, ok := s.cache[addr]; ok {
			if s.now().Sub(c.at) < c.ttl {
				s.mu.Unlock()
				return c.rep, nil
			}
			delete(s.cache, addr)
		}
	}
	cl, running := s.inflight[addr]
	if !running {
		cl = &call{done: make(chan struct{})}
		if s.inflight == nil {
			s.inflight = map[netip.Addr]*call{}
		}
		s.inflight[addr] = cl
		go s.run(addr, cl)
	}
	s.mu.Unlock()

	select {
	case <-cl.done:
		return cl.rep, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// run 在后台查一次(不受某个调用者取消的影响,别的等着的调用者还要用),存缓存后放行等待者。
func (s *Service) run(addr netip.Addr, cl *call) {
	rep := s.fetch(addr)
	if rep == nil {
		rep = &Report{IPTypes: []string{}}
	}
	ttl := missTTL
	if rep.hasData() {
		ttl = hitTTL
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[netip.Addr]cached{}
	}
	s.cache[addr] = cached{rep: rep, at: s.now(), ttl: ttl}
	for len(s.cache) > maxKeep {
		var oldest netip.Addr
		var oldAt time.Time
		first := true
		for k, v := range s.cache {
			if first || v.at.Before(oldAt) {
				oldest, oldAt, first = k, v.at, false
			}
		}
		delete(s.cache, oldest)
	}
	delete(s.inflight, addr)
	cl.rep = rep
	s.mu.Unlock()
	close(cl.done)
}

func (s *Service) fetch(addr netip.Addr) *Report {
	if s.IsPublic == nil || !s.IsPublic(addr) {
		return nil
	}
	base := s.BaseURL
	if base == "" {
		base = "https://ping0.cc/ip/"
	}
	// 与 JS 的 encodeURIComponent 一致:IPv6 的冒号写成 %3A。
	target := base + strings.ReplaceAll(url.PathEscape(addr.String()), ":", "%3A")
	client := s.Client
	if client == nil {
		client = &http.Client{
			Timeout:       fetchTTL,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("不跟随重定向") },
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchTTL)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return nil
	}
	html := string(body)
	if rep := parse(html, addr); rep != nil {
		return rep
	}
	if reTurnstile.MatchString(html) {
		return &Report{IPTypes: []string{}, Error: "challenge"}
	}
	return nil
}

var (
	reTitle     = regexp.MustCompile(`(?i)<title>\s*([\da-f:.]+)\s*-`)
	reDivTag    = regexp.MustCompile(`(?i)<div\b[^>]*>`)
	reUseBarCls = regexp.MustCompile(`(?i)\bclass=["'][^"']*\busecountbar\b[^"']*["']`)
	reUseCount  = regexp.MustCompile(`(?i)\busecount=["']([^"']+)["']`)
	reRange     = regexp.MustCompile(`^\s*(\d+\s*-\s*\d+|\d+\s*\+)\s*`)
	reRisk      = regexp.MustCompile(`(?i)<div\b([^>]*\bclass=["'][^"']*\briskcurrent\b[^"']*["'][^>]*)>([\s\S]*?)</div>`)
	reRiskValue = regexp.MustCompile(`(?i)<span\b[^>]*\bclass=["']value["'][^>]*>\s*(\d{1,3}(?:\.\d+)?)\s*%\s*</span>`)
	reRiskTier  = regexp.MustCompile(`(?i)\btitle=["']\s*(\d+-\d+)(?:\s|["'])`)
	reIPTypeBox = regexp.MustCompile(`(?i)<div\b[^>]*\bclass=["'][^"']*\bline-iptype\b[^"']*["'][^>]*>[\s\S]*?</div>`)
	reContent   = regexp.MustCompile(`(?i)^\s*<div\b[^>]*\bclass=["']content["'][^>]*>([\s\S]*?)</div>`)
	reLabel     = regexp.MustCompile(`(?i)<span\b[^>]*\bclass=["'][^"']*\blabel\b[^"']*["'][^>]*>([^<]*)</span>`)
	reNbsp      = regexp.MustCompile(`(?i)&nbsp;`)
	reTurnstile = regexp.MustCompile(`(?i)<div\b[^>]*\bclass=["'][^"']*\bcf-turnstile\b`)
)

var validRanges = map[string]bool{"1-10": true, "10-100": true, "100-1000": true, "1000-10000": true, "10000+": true}

// 风控值落在哪一档:页面把档位写在风控块的 title 里。
var riskTiers = map[string]string{
	"0-15": "veryClean", "15-25": "clean", "25-40": "neutral",
	"40-50": "slight", "50-70": "elevated", "70-100": "extreme",
}

// parse 从 ping0.cc 的页面取三项。页面标题里的 IP 不是请求的 IP(被改写、错页、验证页)就返回 nil。
func parse(html string, want netip.Addr) *Report {
	m := reTitle.FindStringSubmatch(html)
	if m == nil {
		return nil
	}
	got, err := netip.ParseAddr(m[1])
	if err != nil || got.Unmap() != want {
		return nil
	}
	rep := &Report{IPTypes: []string{}}

	for _, tag := range reDivTag.FindAllString(html, -1) {
		if !reUseBarCls.MatchString(tag) {
			continue
		}
		if um := reUseCount.FindStringSubmatch(tag); um != nil {
			rep.Range = parseRange(um[1])
			break
		}
	}

	if rm := reRisk.FindStringSubmatch(html); rm != nil {
		if vm := reRiskValue.FindStringSubmatch(rm[2]); vm != nil {
			if pct, err := strconv.ParseFloat(vm[1], 64); err == nil && pct <= 100 {
				risk := &Risk{Percent: pct}
				if tm := reRiskTier.FindStringSubmatch(rm[1]); tm != nil {
					if tier, ok := riskTiers[tm[1]]; ok {
						risk.Tier = &tier
					}
				}
				rep.Risk = risk
			}
		}
	}

	if loc := reIPTypeBox.FindStringIndex(html); loc != nil {
		if cm := reContent.FindStringSubmatch(html[loc[1]:]); cm != nil {
			for _, lm := range reLabel.FindAllStringSubmatch(cm[1], -1) {
				text := strings.Join(strings.Fields(reNbsp.ReplaceAllString(lm[1], " ")), " ")
				if text != "" {
					rep.IPTypes = append(rep.IPTypes, text)
				}
			}
		}
	}
	return rep
}

// parseRange:「1-10」「10000+」,后面只能跟 "(" / "（" 或结束;空白去掉;不在五档里的当没有。
func parseRange(s string) *string {
	loc := reRange.FindStringSubmatchIndex(s)
	if loc == nil {
		return nil
	}
	rest := s[loc[1]:]
	if rest != "" && !strings.HasPrefix(rest, "(") && !strings.HasPrefix(rest, "（") {
		return nil
	}
	v := strings.Join(strings.Fields(s[loc[2]:loc[3]]), "")
	if !validRanges[v] {
		return nil
	}
	return &v
}
