// Package nodegroups 是节点组(用户自定义策略组)的服务层。
//
//	groups.json  {"groups":[...]}  已经过 core normalizeGroups 补全的节点组(含内置直连 / 拒绝)
//
// 规范化、成员解析、环检测都由 openbox-core 做(normalizeGroups / emitGroups),这里只负责:
// 整份覆盖保存、重名校验、改名时迁移其它组对它的引用、报告悬空成员和被丢弃的组、
// 以及给前端成员选择器的节点清单。不碰 sing-box,也不重启内核。
package nodegroups

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

const (
	fileGroups = "groups"

	// MaxGroups、MaxNameRunes 是外部输入的上限。
	MaxGroups    = 200
	MaxNameRunes = 100
)

// Types 是前端可选的组类型。
var Types = []string{"urltest", "selector", "failover"}

// ErrCoreUnavailable:openbox-core 不可用(没装或没配置)。
var ErrCoreUnavailable = errors.New("openbox-core 不可用,无法处理节点组")

// InputError 是用户输入有问题(400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &InputError{Msg: fmt.Sprintf(format, a...)} }

// Dropped 是生成配置时会被丢弃的组(目前只有 cycle:成员互相引用成环)。
type Dropped struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Core 是用到的核心方法。
type Core interface {
	// NormalizeGroups 对应 normalizeGroups:补全字段并带上内置直连 / 拒绝。
	NormalizeGroups(ctx context.Context, groups []any) ([]any, error)
	// EmitGroups 对应 emitGroups:返回会被丢弃的组。nodes 元素为 {tag, regionName, chain}。
	EmitGroups(ctx context.Context, groups []any, nodes []any) ([]Dropped, error)
}

// ChainNode 是启用的链式代理(它在节点清单里以 chain 节点出现)。
type ChainNode struct{ Name, Upstream string }

// Chains 读取启用的链式代理。
type Chains func(ctx context.Context) ([]ChainNode, error)

// Catalog 是订阅目录(subs.Service 实现)。
type Catalog interface {
	Catalog(ctx context.Context) ([]subs.Record, []subs.CatalogNode, error)
}

// Service 是节点组服务。
type Service struct {
	st     *store.Store
	core   Core
	cat    Catalog
	chains Chains
}

// New 创建服务。core 为 nil 时读写都返回 ErrCoreUnavailable;chains 可为 nil。
func New(st *store.Store, c Core, cat Catalog, chains Chains) *Service {
	return &Service{st: st, core: c, cat: cat, chains: chains}
}

// ChainsFromProfile 从档案的 chainProxies 里取启用且有名字的链式代理。
func ChainsFromProfile(p *profile.Service) Chains {
	return func(ctx context.Context) ([]ChainNode, error) {
		prof, err := p.Get(ctx)
		if err != nil {
			return nil, err
		}
		list, _ := prof["chainProxies"].([]any)
		out := []ChainNode{}
		for _, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			if en, has := m["enabled"].(bool); has && !en {
				continue
			}
			if strings.TrimSpace(name) == "" {
				continue
			}
			up, _ := m["upstream"].(string)
			out = append(out, ChainNode{Name: name, Upstream: up})
		}
		return out, nil
	}
}

type groupsFile struct {
	Groups []any `json:"groups"`
}

// AvailableNode 对应前端 OpenboxGroupsPayload.availableNodes 的元素。
type AvailableNode struct {
	Name           string `json:"name"`
	Subscription   string `json:"subscription"`
	SubscriptionID string `json:"subscriptionId,omitempty"`
	Chain          bool   `json:"chain,omitempty"`
	Upstream       string `json:"upstream,omitempty"`
	Region         string `json:"region,omitempty"`
}

// SubRef 是订阅范围下拉的一项。
type SubRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Payload 对应前端 OpenboxGroupsPayload。
type Payload struct {
	Groups          []any           `json:"groups"`
	Types           []string        `json:"types"`
	AvailableNodes  []AvailableNode `json:"availableNodes"`
	Subscriptions   []SubRef        `json:"subscriptions"`
	AvailableGroups []string        `json:"availableGroups"`
}

// stored 返回当前生效的节点组:存过就用存的,没存过用默认(不落盘)。
func (s *Service) stored(ctx context.Context) ([]any, error) {
	var f groupsFile
	found, err := s.st.Load(fileGroups, &f)
	if err != nil {
		return nil, err
	}
	if found && f.Groups != nil {
		return f.Groups, nil
	}
	if s.core == nil {
		return nil, ErrCoreUnavailable
	}
	return s.core.NormalizeGroups(ctx, profile.DefaultGroupsInput())
}

// Groups 返回当前节点组(给配置生成用)。
func (s *Service) Groups(ctx context.Context) ([]any, error) { return s.stored(ctx) }

func (s *Service) nodes(ctx context.Context) ([]AvailableNode, []SubRef, error) {
	nodes := []AvailableNode{}
	subsList := []SubRef{}
	if s.cat != nil {
		recs, cn, err := s.cat.Catalog(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range recs {
			subsList = append(subsList, SubRef{ID: r.ID, Name: r.Name})
		}
		for _, n := range cn {
			nodes = append(nodes, AvailableNode{
				Name:           n.Node.Tag,
				Subscription:   n.SubscriptionName,
				SubscriptionID: n.SubscriptionID,
				Region:         n.Node.RegionName,
			})
		}
	}
	if s.chains != nil {
		cs, err := s.chains(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, c := range cs {
			nodes = append(nodes, AvailableNode{Name: c.Name, Chain: true, Upstream: c.Upstream})
		}
	}
	return nodes, subsList, nil
}

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

// enabled:缺省视为启用。
func enabled(m map[string]any) bool {
	v, ok := m["enabled"].(bool)
	return !ok || v
}

// Get 返回 GET /api/openbox/groups 的内容。
func (s *Service) Get(ctx context.Context) (*Payload, error) {
	groups, err := s.stored(ctx)
	if err != nil {
		return nil, err
	}
	nodes, subsList, err := s.nodes(ctx)
	if err != nil {
		return nil, err
	}
	avail := []string{}
	for _, g := range groups {
		if m, ok := g.(map[string]any); ok && enabled(m) {
			if n := str(m, "name"); n != "" {
				avail = append(avail, n)
			}
		}
	}
	return &Payload{Groups: groups, Types: Types, AvailableNodes: nodes, Subscriptions: subsList, AvailableGroups: avail}, nil
}

// Renamed 是一次保存里被改名的组。
type Renamed struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Dangling 是某个组里既不是节点也不是组的成员(生成配置时会被忽略)。
type Dangling struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// SaveResult 对应 PUT /api/openbox/groups 的响应。
type SaveResult struct {
	OK       bool       `json:"ok"`
	Groups   []any      `json:"groups"`
	Dropped  []Dropped  `json:"dropped"`
	Dangling []Dangling `json:"dangling,omitempty"`
	Renamed  []Renamed  `json:"renamed,omitempty"`
}

// rewriteMembers 把组 m 里引用 from 的成员改成 to(去重,保持顺序),返回是否有改动。
func rewriteMembers(m map[string]any, from, to string) bool {
	changed := false
	fix := func(list []any) ([]any, bool) {
		seen := map[string]bool{}
		out := make([]any, 0, len(list))
		hit := false
		for _, v := range list {
			s, _ := v.(string)
			if s == from {
				s, hit = to, true
			}
			if s != "" && seen[s] {
				continue
			}
			if s != "" {
				seen[s] = true
			}
			out = append(out, s)
		}
		return out, hit
	}
	if ms, ok := m["members"].([]any); ok {
		if out, hit := fix(ms); hit {
			m["members"], changed = out, true
		}
	}
	if lanes, ok := m["lanes"].([]any); ok {
		for _, l := range lanes {
			lm, ok := l.(map[string]any)
			if !ok {
				continue
			}
			if ms, ok := lm["members"].([]any); ok {
				if out, hit := fix(ms); hit {
					lm["members"], changed = out, true
				}
			}
		}
	}
	return changed
}

// allMembers 返回组里引用的全部成员名(static 的 members + failover 各页签的 members)。
func allMembers(m map[string]any) []string {
	var out []string
	add := func(list []any) {
		for _, v := range list {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	if ms, ok := m["members"].([]any); ok {
		add(ms)
	}
	if lanes, ok := m["lanes"].([]any); ok {
		for _, l := range lanes {
			if lm, ok := l.(map[string]any); ok {
				if ms, ok := lm["members"].([]any); ok {
					add(ms)
				}
			}
		}
	}
	return out
}

// Save 整份覆盖节点组。流程:规范化 → 校验名字 → 迁移改名引用 → 再规范化 → 报告悬空 / 丢弃 → 落盘。
func (s *Service) Save(ctx context.Context, in []any) (*SaveResult, error) {
	if s.core == nil {
		return nil, ErrCoreUnavailable
	}
	if len(in) > MaxGroups {
		return nil, bad("节点组最多 %d 个", MaxGroups)
	}
	for i, g := range in {
		if _, ok := g.(map[string]any); !ok {
			return nil, bad("第 %d 个节点组不是对象", i+1)
		}
	}
	norm, err := s.core.NormalizeGroups(ctx, in)
	if err != nil {
		return nil, err
	}
	nodes, _, err := s.nodes(ctx)
	if err != nil {
		return nil, err
	}
	tagSet := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		tagSet[n.Name] = true
	}

	// 名字校验:非空、不超长、组与组不重名、不与节点重名(sing-box 出站 tag 全局唯一)。
	byName := map[string]bool{}
	byID := map[string]map[string]any{}
	for _, g := range norm {
		m, ok := g.(map[string]any)
		if !ok {
			return nil, bad("节点组格式不正确")
		}
		name := str(m, "name")
		if strings.TrimSpace(name) == "" {
			return nil, bad("节点组名称不能为空")
		}
		if utf8.RuneCountInString(name) > MaxNameRunes {
			return nil, bad("节点组名称不能超过 %d 个字符:%s", MaxNameRunes, name)
		}
		if byName[name] {
			return nil, bad("节点组名称重复:%s", name)
		}
		if tagSet[name] {
			return nil, bad("节点组名称与节点重名:%s", name)
		}
		byName[name] = true
		if id := str(m, "id"); id != "" {
			byID[id] = m
		}
	}

	// 改名:按 id 对旧档,名字变了的把其它组里的引用迁移过去。
	old, err := s.stored(ctx)
	if err != nil && !errors.Is(err, ErrCoreUnavailable) {
		return nil, err
	}
	var renamed []Renamed
	for _, og := range old {
		om, ok := og.(map[string]any)
		if !ok {
			continue
		}
		nm, found := byID[str(om, "id")]
		if !found || str(om, "id") == "" {
			continue
		}
		from, to := str(om, "name"), str(nm, "name")
		if from == "" || from == to {
			continue
		}
		renamed = append(renamed, Renamed{From: from, To: to})
		for _, g := range norm {
			if m := g.(map[string]any); str(m, "id") != str(nm, "id") {
				rewriteMembers(m, from, to)
			}
		}
	}
	if len(renamed) > 0 {
		// 引用变了,让核心按最新的成员再规范化一遍(去重、清理)。
		if norm, err = s.core.NormalizeGroups(ctx, norm); err != nil {
			return nil, err
		}
	}

	// 悬空成员:既不是节点也不是(任何)组。
	names := map[string]bool{}
	for _, g := range norm {
		names[str(g.(map[string]any), "name")] = true
	}
	var dangling []Dangling
	for _, g := range norm {
		m := g.(map[string]any)
		if str(m, "mode") == "dynamic" && str(m, "type") != "failover" {
			continue // 动态组的成员是现算的
		}
		var miss []string
		seen := map[string]bool{}
		for _, name := range allMembers(m) {
			if !tagSet[name] && !names[name] && !seen[name] {
				seen[name] = true
				miss = append(miss, name)
			}
		}
		if len(miss) > 0 {
			dangling = append(dangling, Dangling{Name: str(m, "name"), Members: miss})
		}
	}

	// 环:按保存后的内容让核心算一遍(只看会不会被丢弃,不落配置)。
	emitNodes := make([]any, 0, len(nodes))
	for _, n := range nodes {
		emitNodes = append(emitNodes, map[string]any{"tag": n.Name, "regionName": n.Region, "chain": n.Chain})
	}
	dropped, err := s.core.EmitGroups(ctx, norm, emitNodes)
	if err != nil {
		return nil, err
	}
	if dropped == nil {
		dropped = []Dropped{}
	}

	if err := s.st.Save(fileGroups, groupsFile{Groups: norm}); err != nil {
		return nil, err
	}
	return &SaveResult{OK: true, Groups: norm, Dropped: dropped, Dangling: dangling, Renamed: renamed}, nil
}
