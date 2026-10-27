// Package policyentries 实现「域名穿透」:一个站点集会命中哪些域名 / IP。
//
// 数据来源(都是后端自己的设计,依据前端 OpenboxPolicyEntries 的类型):
//   - 站点集的手写条件(domain / domainSuffix / domainKeyword / ipCidr),来源记为 "custom";
//   - 站点集引用的规则集链接(ruleUrls),拉回文本名单展开,来源记为链接的「主机 + 路径」(不带查询串,避免暴露 token);
//   - 规则库分类(geosite-* / geoip-*,包括前置自定义分流里的 geosite / geoip / ruleset 行):
//     用 internal/geodata 读随包规则库(交给 sing-box 解码)展开,来源记为分类名;
//   - 前置自定义分流(固定的一条,名字取自档案里的 custom.name)的每一行;
//   - 兜底站点集命中「其他所有」,没有条目,只返回 fallback = true。
//
// 展开不了的(没接规则库、规则库里没有这个分类、没有 sing-box、解码失败、名字不合法)如实列在 missing 里,
// 不假装没有,也不让整个查询失败。
package policyentries

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/geodata"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

// 条目族,与前端 OpenboxPolicyEntryFamily 一致。
const (
	FamilyDomain = "domain"
	FamilyIP     = "ip"
	FamilyOther  = "other"

	sourceCustom = "custom"
	typePort     = "port"

	// maxEntries 是一次查询汇总的条目上限(路由器内存小)。
	maxEntries = 300000
)

// ErrCoreUnavailable 表示没有可用的 openbox-core(站点集的规范化在核心里)。
var ErrCoreUnavailable = errors.New("openbox-core 不可用,无法查看站点集的条目")

// InputError 是调用方参数有问题(接口返回 400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// NotFoundError 表示没有这个名字的站点集(接口返回 404)。
type NotFoundError struct{ Name string }

func (e *NotFoundError) Error() string { return "没有名为「" + e.Name + "」的站点集" }

// Policy 是规范化后的站点集里本功能用得到的字段。
type Policy struct {
	Name          string   `json:"name"`
	Rulesets      []string `json:"rulesets"`
	RuleUrls      []string `json:"ruleUrls"`
	Domain        []string `json:"domain"`
	DomainSuffix  []string `json:"domainSuffix"`
	DomainKeyword []string `json:"domainKeyword"`
	IPCidr        []string `json:"ipCidr"`
}

// CustomRule 是前置自定义分流的一行。
type CustomRule struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Routing 是核心 normalizeRouting 的返回里本功能用得到的部分。
type Routing struct {
	Custom struct {
		Name  string       `json:"name"`
		Rules []CustomRule `json:"rules"`
	} `json:"custom"`
	Policies []Policy `json:"policies"`
	Fallback struct {
		Name string `json:"name"`
	} `json:"fallback"`
}

// Profiles 读取档案(测试里用假实现)。
type Profiles interface {
	Get(ctx context.Context) (map[string]any, error)
}

// Core 是用到的核心能力。
type Core interface {
	NormalizeRouting(ctx context.Context, routing map[string]any) (Routing, error)
}

// Lists 拉取并解析规则集链接(*rulelist.Service 满足它)。
type Lists interface {
	Load(ctx context.Context, rawURL string) (*rulelist.Result, error)
}

// Geo 是随包规则库(*geodata.Library 满足它)。
type Geo interface {
	Entries(ctx context.Context, tag string) (*geodata.Result, error)
}

// CoreAdapter 把 *core.Client 适配成 Core。
type CoreAdapter struct{ C *core.Client }

// NormalizeRouting 实现 Core。不传 geoTags:传了核心会把规则库里没有的分类直接丢掉,
// 而这里要的是如实把它们列进 missing,所以让核心保留全部。
func (a CoreAdapter) NormalizeRouting(ctx context.Context, routing map[string]any) (Routing, error) {
	var out Routing
	if err := a.C.CallInto(ctx, "normalizeRouting", []any{routing}, &out); err != nil {
		return Routing{}, err
	}
	return out, nil
}

// Service 汇总站点集的条目。
type Service struct {
	profiles Profiles
	core     Core // 可为 nil
	lists    Lists
	geo      Geo // 可为 nil:规则库分类全部列进 missing
}

// New 创建服务。c 可以为 nil(此时查询返回 ErrCoreUnavailable)。
func New(p Profiles, c Core, l Lists) *Service { return &Service{profiles: p, core: c, lists: l} }

// WithGeo 接上随包规则库,之后 geosite-* / geoip-* 分类会被展开;不接则它们如实列进 missing。
func (s *Service) WithGeo(g Geo) *Service {
	s.geo = g
	return s
}

// Query 是查询参数。
type Query struct {
	Name   string
	Tab    string // all / domain / ip,空 = all
	Q      string // 不区分大小写的子串,匹配内容和来源
	Sort   string // type / content / source,空 = 保持原顺序
	Desc   bool
	Offset int
	Limit  int
}

// Entry 是一条命中的规则,字段与前端 OpenboxPolicyEntry 一致。
type Entry struct {
	Type    string `json:"type"`
	Family  string `json:"family"`
	Content string `json:"content"`
	Source  string `json:"source"`
}

// Counts 是各档的条数(不受搜索词和当前档影响)。
type Counts struct {
	All    int `json:"all"`
	Domain int `json:"domain"`
	IP     int `json:"ip"`
}

// Result 是查询结果,字段与前端 OpenboxPolicyEntries 一致。
type Result struct {
	Name     string   `json:"name"`
	Fallback bool     `json:"fallback"`
	Counts   Counts   `json:"counts"`
	Total    int      `json:"total"`   // 当前档的条数
	Matched  int      `json:"matched"` // 当前档里命中搜索词的条数
	Offset   int      `json:"offset"`
	Limit    int      `json:"limit"`
	HasMore  bool     `json:"hasMore"`
	Entries  []Entry  `json:"entries"`
	Missing  []string `json:"missing"`
}

// Query 返回名为 q.Name 的站点集命中的条目。
func (s *Service) Query(ctx context.Context, q Query) (*Result, error) {
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" {
		return nil, &InputError{"缺少 name 参数"}
	}
	switch q.Tab {
	case "", "all", FamilyDomain, FamilyIP:
	default:
		return nil, &InputError{"tab 只能是 all、domain 或 ip"}
	}
	switch q.Sort {
	case "", "type", "content", "source":
	default:
		return nil, &InputError{"sort 只能是 type、content 或 source"}
	}
	if s.core == nil {
		return nil, ErrCoreUnavailable
	}
	prof, err := s.profiles.Get(ctx)
	if err != nil {
		return nil, err
	}
	routing, _ := prof["routing"].(map[string]any)
	rt, err := s.core.NormalizeRouting(ctx, routing)
	if err != nil {
		return nil, err
	}

	res := &Result{Name: q.Name, Entries: []Entry{}, Missing: []string{}}
	var all []Entry
	switch {
	case q.Name == rt.Fallback.Name:
		res.Fallback = true
	case q.Name == rt.Custom.Name:
		all, res.Missing, err = s.fromCustom(ctx, rt.Custom.Rules)
	default:
		p := findPolicy(rt.Policies, q.Name)
		if p == nil {
			return nil, &NotFoundError{q.Name}
		}
		all, res.Missing, err = s.fromPolicy(ctx, *p)
	}
	if err != nil {
		return nil, err
	}
	finish(res, all, q)
	return res, nil
}

func findPolicy(list []Policy, name string) *Policy {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// collector 去重(同类型、同内容、同来源只留一条)并限制总量。
type collector struct {
	seen      map[Entry]struct{}
	out       []Entry
	truncated bool
}

func newCollector() *collector { return &collector{seen: map[Entry]struct{}{}} }

func (c *collector) add(typ, content, source string) {
	if content == "" {
		return
	}
	e := Entry{Type: typ, Family: familyOf(typ), Content: content, Source: source}
	if _, dup := c.seen[e]; dup {
		return
	}
	if len(c.out) >= maxEntries {
		c.truncated = true
		return
	}
	c.seen[e] = struct{}{}
	c.out = append(c.out, e)
}

func familyOf(typ string) string {
	switch typ {
	case rulelist.TypeDomain, rulelist.TypeDomainSuffix, rulelist.TypeDomainKeyword, rulelist.TypeDomainRegex:
		return FamilyDomain
	case rulelist.TypeIPCIDR:
		return FamilyIP
	}
	return FamilyOther
}

// missingList 按出现顺序收集去重的「缺失」名。
type missingList struct {
	seen map[string]struct{}
	out  []string
}

func (m *missingList) add(s string) {
	if m.seen == nil {
		m.seen = map[string]struct{}{}
	}
	if _, dup := m.seen[s]; dup {
		return
	}
	m.seen[s] = struct{}{}
	m.out = append(m.out, s)
}

const truncatedNote = "条目太多,只统计了前 30 万条"

// expandGeo 把规则库分类 tag 的条目加进 c,来源记为分类名。没接规则库、名字不合法、分类不在库里、
// 没有 sing-box、解码失败都记入 miss(不让整个查询失败);请求被取消则把取消错误交回去。
func (s *Service) expandGeo(ctx context.Context, tag string, c *collector, miss *missingList) error {
	if s.geo == nil || !geodata.ValidTag(tag) {
		miss.add(tag)
		return nil
	}
	res, err := s.geo.Entries(ctx, tag)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		miss.add(tag)
		return nil
	}
	for _, e := range res.Entries {
		c.add(e.Type, e.Value, tag)
	}
	if res.Truncated {
		miss.add(tag + "(" + truncatedNote + ")")
	}
	return nil
}

// expandList 拉取规则集链接并把条目加进 c;拉取失败记入 miss(不让整个查询失败)。
func (s *Service) expandList(ctx context.Context, raw string, c *collector, miss *missingList) error {
	label := urlLabel(raw)
	res, err := s.lists.Load(ctx, raw)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		miss.add(label)
		return nil
	}
	for _, e := range res.Entries {
		c.add(e.Type, e.Value, label)
	}
	return nil
}

func (s *Service) fromPolicy(ctx context.Context, p Policy) ([]Entry, []string, error) {
	c, miss := newCollector(), &missingList{}
	for _, v := range p.Domain {
		c.add(rulelist.TypeDomain, v, sourceCustom)
	}
	for _, v := range p.DomainSuffix {
		c.add(rulelist.TypeDomainSuffix, v, sourceCustom)
	}
	for _, v := range p.DomainKeyword {
		c.add(rulelist.TypeDomainKeyword, v, sourceCustom)
	}
	for _, v := range p.IPCidr {
		c.add(rulelist.TypeIPCIDR, v, sourceCustom)
	}
	// 规则集链接:核心把每个链接折算成 list-<hash> 追加进 rulesets,这里按链接展开,
	// 对应的 list-* 名不再当作「缺失」。
	isListTag := map[string]struct{}{}
	for _, u := range p.RuleUrls {
		isListTag[ListTagForURL(u)] = struct{}{}
		if err := s.expandList(ctx, u, c, miss); err != nil {
			return nil, nil, err
		}
	}
	for _, tag := range p.Rulesets {
		if _, ok := isListTag[tag]; ok {
			continue
		}
		// geosite-* / geoip-* 从随包规则库展开;展开不了的(或别的名字)记入 missing
		if err := s.expandGeo(ctx, tag, c, miss); err != nil {
			return nil, nil, err
		}
	}
	if c.truncated {
		miss.add(truncatedNote)
	}
	return c.out, miss.out, nil
}

func (s *Service) fromCustom(ctx context.Context, rules []CustomRule) ([]Entry, []string, error) {
	c, miss := newCollector(), &missingList{}
	for _, r := range rules {
		switch r.Type {
		case "domain":
			c.add(rulelist.TypeDomain, r.Value, sourceCustom)
		case "domainSuffix":
			c.add(rulelist.TypeDomainSuffix, r.Value, sourceCustom)
		case "domainKeyword":
			c.add(rulelist.TypeDomainKeyword, r.Value, sourceCustom)
		case "ipCidr":
			c.add(rulelist.TypeIPCIDR, r.Value, sourceCustom)
		case "port":
			c.add(typePort, r.Value, sourceCustom)
		case "geosite":
			if err := s.expandGeo(ctx, "geosite-"+r.Value, c, miss); err != nil {
				return nil, nil, err
			}
		case "geoip":
			if err := s.expandGeo(ctx, "geoip-"+r.Value, c, miss); err != nil {
				return nil, nil, err
			}
		case "ruleset":
			if err := s.expandGeo(ctx, r.Value, c, miss); err != nil {
				return nil, nil, err
			}
		case "ruleUrl":
			if err := s.expandList(ctx, r.Value, c, miss); err != nil {
				return nil, nil, err
			}
		}
	}
	if c.truncated {
		miss.add(truncatedNote)
	}
	return c.out, miss.out, nil
}

// finish 算各档条数,按档、搜索词过滤,排序,分页。
func finish(res *Result, all []Entry, q Query) {
	for _, e := range all {
		res.Counts.All++
		switch e.Family {
		case FamilyDomain:
			res.Counts.Domain++
		case FamilyIP:
			res.Counts.IP++
		}
	}
	tab := q.Tab
	if tab == "" {
		tab = "all"
	}
	tabbed := all
	if tab != "all" {
		tabbed = make([]Entry, 0, len(all))
		for _, e := range all {
			if e.Family == tab {
				tabbed = append(tabbed, e)
			}
		}
	}
	res.Total = len(tabbed)

	hits := tabbed
	if needle := strings.ToLower(strings.TrimSpace(q.Q)); needle != "" {
		hits = make([]Entry, 0, len(tabbed))
		for _, e := range tabbed {
			if strings.Contains(strings.ToLower(e.Content), needle) || strings.Contains(strings.ToLower(e.Source), needle) {
				hits = append(hits, e)
			}
		}
	}
	res.Matched = len(hits)

	if q.Sort != "" {
		hits = append([]Entry(nil), hits...)
		key := func(e Entry) string {
			switch q.Sort {
			case "type":
				return e.Type
			case "source":
				return e.Source
			}
			return e.Content
		}
		sort.SliceStable(hits, func(i, j int) bool {
			a, b := strings.ToLower(key(hits[i])), strings.ToLower(key(hits[j]))
			if a == b {
				return false
			}
			if q.Desc {
				return a > b
			}
			return a < b
		})
	}

	off, lim := q.Offset, q.Limit
	if off < 0 {
		off = 0
	}
	if off > len(hits) {
		off = len(hits)
	}
	if lim < 1 {
		lim = 1
	}
	end := off + lim
	if end > len(hits) {
		end = len(hits)
	}
	res.Offset, res.Limit = off, lim
	res.Entries = append(res.Entries, hits[off:end]...)
	res.HasMore = end < len(hits)
}

// urlLabel 把规则集链接缩成「主机 + 路径」,去掉账号、查询串和片段(里面常带 token)。
func urlLabel(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "规则集链接"
	}
	return u.Host + strings.TrimRight(u.Path, "/")
}

// ListTagForURL 把规则集链接折算成规则集名:去掉首尾空白后按 UTF-16 码元做 FNV-1a(32 位),
// 取 8 位十六进制,前缀 list-。与 core 的 ListTagForUrl 必须一致(核心把它追加进站点集的 rulesets)。
func ListTagForURL(raw string) string {
	s := strings.TrimFunc(raw, isJSSpace)
	if s == "" {
		return ""
	}
	h := uint32(0x811c9dc5)
	for _, u := range utf16.Encode([]rune(s)) {
		h ^= uint32(u)
		h *= 0x01000193
	}
	return fmt.Sprintf("list-%08x", h)
}

// isJSSpace 是 JavaScript String.prototype.trim 认的空白(比 unicode.IsSpace 多 U+FEFF,少 U+0085)。
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
