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
	"regexp"
	"strconv"
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
var Types = []string{"urltest", "selector", "failover", "loadbalance"}

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
	// 档案里的带宽测速参数,GET 时回给面板(组编辑里显示周期);nil = 不回
	bandwidth func(ctx context.Context) any
}

// WithBandwidth 设置读取带宽测速参数的函数。
func (s *Service) WithBandwidth(f func(ctx context.Context) any) *Service {
	s.bandwidth = f
	return s
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
	// 带宽择优的统一测速参数({intervalHours, maxDownloadMB}),没接档案时不出现
	Bandwidth any `json:"bandwidth,omitempty"`
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
	p := &Payload{Groups: groups, Types: Types, AvailableNodes: nodes, Subscriptions: subsList, AvailableGroups: avail}
	if s.bandwidth != nil {
		p.Bandwidth = s.bandwidth(ctx)
	}
	return p, nil
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

// checkedGroups 是 Save 和 Restore 共用的前半段:数量与形状 → 核心规范化 → 名字校验。
// tags 在规范化之后才取(取不到就报错),是「已有节点名」(含启用的链式代理),组名不能与它们重名。
// 返回规范化后的组,以及按 id 索引的组(Save 对旧档改名时用)。
func (s *Service) checkedGroups(ctx context.Context, in []any, tags func() (map[string]bool, error)) ([]any, map[string]map[string]any, error) {
	if s.core == nil {
		return nil, nil, ErrCoreUnavailable
	}
	if len(in) > MaxGroups {
		return nil, nil, bad("节点组最多 %d 个", MaxGroups)
	}
	for i, g := range in {
		if _, ok := g.(map[string]any); !ok {
			return nil, nil, bad("第 %d 个节点组不是对象", i+1)
		}
	}
	if err := checkBandwidthFlags(in); err != nil {
		return nil, nil, err
	}
	norm, err := s.core.NormalizeGroups(ctx, in)
	if err != nil {
		return nil, nil, err
	}
	tagSet, err := tags()
	if err != nil {
		return nil, nil, err
	}
	// 负载均衡的专属校验看的是用户提交的原始内容(规范化会把越界的间隔悄悄改回默认值,用户就不知道了)。
	prev, _ := s.stored(ctx)
	if err := checkLoadBalance(in, tagSet, prev); err != nil {
		return nil, nil, err
	}

	// 名字校验:非空、不超长、组与组不重名、不与节点重名(sing-box 出站 tag 全局唯一)。
	byName := map[string]bool{}
	byID := map[string]map[string]any{}
	for _, g := range norm {
		m, ok := g.(map[string]any)
		if !ok {
			return nil, nil, bad("节点组格式不正确")
		}
		name := str(m, "name")
		if strings.TrimSpace(name) == "" {
			return nil, nil, bad("节点组名称不能为空")
		}
		if utf8.RuneCountInString(name) > MaxNameRunes {
			return nil, nil, bad("节点组名称不能超过 %d 个字符:%s", MaxNameRunes, name)
		}
		if byName[name] {
			return nil, nil, bad("节点组名称重复:%s", name)
		}
		if tagSet[name] {
			return nil, nil, bad("节点组名称与节点重名:%s", name)
		}
		byName[name] = true
		if id := str(m, "id"); id != "" {
			byID[id] = m
		}
	}
	return norm, byID, nil
}

// nodeTags 是当前节点清单里的全部名字。
func (s *Service) nodeTags(ctx context.Context) ([]AvailableNode, map[string]bool, error) {
	nodes, _, err := s.nodes(ctx)
	if err != nil {
		return nil, nil, err
	}
	tagSet := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		tagSet[n.Name] = true
	}
	return nodes, tagSet, nil
}

// Save 整份覆盖节点组。流程:规范化 → 校验名字 → 迁移改名引用 → 再规范化 → 报告悬空 / 丢弃 → 落盘。
func (s *Service) Save(ctx context.Context, in []any) (*SaveResult, error) {
	var nodes []AvailableNode
	var tagSet map[string]bool
	norm, byID, err := s.checkedGroups(ctx, in, func() (map[string]bool, error) {
		var err error
		nodes, tagSet, err = s.nodeTags(ctx)
		return tagSet, err
	})
	if err != nil {
		return nil, err
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

	return s.finish(ctx, norm, nodes, tagSet, renamed)
}

// finish 是 Save 和 Restore 共用的后半段:报告悬空成员、让核心算会被丢弃的组、落盘。
func (s *Service) finish(ctx context.Context, norm []any, nodes []AvailableNode, tagSet map[string]bool, renamed []Renamed) (*SaveResult, error) {
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
		if strings.Contains(err.Error(), "load balance supports at most") { // 动态组命中太多节点这类,是用户能改的输入问题
			return nil, bad("%s", err.Error())
		}
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

// CheckRestore 只校验、不写盘:in 能不能作为整份节点组导入。tags 是导入之后会存在的节点名
// (启用订阅的节点 + 启用的链式代理),组名不能与它们重名。返回规范化后的组。
func (s *Service) CheckRestore(ctx context.Context, in []any, tags map[string]bool) ([]any, error) {
	norm, _, err := s.checkedGroups(ctx, in, func() (map[string]bool, error) { return tags, nil })
	return norm, err
}

// Restore 把 in 整份写成节点组,给备份导入用。和 Save 的区别:不对旧档做「改名迁移」——
// 备份文件里的引用本来就是自洽的,拿旧档的 id 去改写它反而会改错;其余(规范化、重名校验、
// 悬空 / 被丢弃的报告)一样。节点名取当前存着的节点,所以要在订阅和档案恢复之后调用。
func (s *Service) Restore(ctx context.Context, in []any) (*SaveResult, error) {
	var nodes []AvailableNode
	var tagSet map[string]bool
	norm, _, err := s.checkedGroups(ctx, in, func() (map[string]bool, error) {
		var err error
		nodes, tagSet, err = s.nodeTags(ctx)
		return tagSet, err
	})
	if err != nil {
		return nil, err
	}
	return s.finish(ctx, norm, nodes, tagSet, nil)
}

// 负载均衡(loadbalance)的限制:检测间隔 5s ~ 24h;成员只能是真实节点(不能放组),最多 64 个。
const (
	LoadBalanceMaxNodes      = 64
	loadBalanceMinIntervalMs = 5000.0
	loadBalanceMaxIntervalMs = 24 * 3600 * 1000.0
)

var bareNumberRe = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
var durationRe = regexp.MustCompile(`^(\d+(?:\.\d+)?(?:ms|s|m|h))+$`)
var durationPartRe = regexp.MustCompile(`(\d+(?:\.\d+)?)(ms|s|m|h)`)

// intervalMs 把 "30s" / "5m" / "1h30m" / "5000ms" / "30"(纯数字当秒)转成毫秒;写法不对返回 false。
func intervalMs(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if bareNumberRe.MatchString(v) {
		n, _ := strconv.ParseFloat(v, 64)
		return n * 1000, true
	}
	if !durationRe.MatchString(v) {
		return 0, false
	}
	total := 0.0
	for _, m := range durationPartRe.FindAllStringSubmatch(v, -1) {
		n, _ := strconv.ParseFloat(m[1], 64)
		switch m[2] {
		case "ms":
			total += n
		case "s":
			total += n * 1000
		case "m":
			total += n * 60000
		case "h":
			total += n * 3600000
		}
	}
	return total, true
}

// checkLoadBalance 校验用户提交的负载均衡组。prev 是已保存的组:这个组原来就有的成员即使现在不是节点了也放行,
// 免得订阅换了一批节点之后,用户连改个间隔都存不了。
func checkLoadBalance(in []any, tagSet map[string]bool, prev []any) error {
	prevMembers := map[string]map[string]bool{}
	for _, p := range prev {
		pm, ok := p.(map[string]any)
		if !ok || str(pm, "type") != "loadbalance" || str(pm, "id") == "" {
			continue
		}
		set := map[string]bool{}
		for _, name := range allMembers(pm) {
			set[name] = true
		}
		prevMembers[str(pm, "id")] = set
	}
	for _, g := range in {
		m, ok := g.(map[string]any)
		if !ok || str(m, "type") != "loadbalance" {
			continue
		}
		name := str(m, "name")
		if iv, has := m["interval"]; has && iv != nil && iv != "" {
			ivs, isStr := iv.(string)
			ms, valid := intervalMs(ivs)
			if !isStr || !valid || ms < loadBalanceMinIntervalMs || ms > loadBalanceMaxIntervalMs {
				return bad("负载均衡「%s」的检测间隔范围为 5s 到 24h", name)
			}
		}
		if str(m, "mode") == "dynamic" {
			continue // 动态组的成员是现算的,个数在生成配置时检查
		}
		members, isList := m["members"].([]any)
		if !isList {
			return bad("负载均衡「%s」的 members 必须是节点数组", name)
		}
		uniq := map[string]bool{}
		for _, v := range members {
			if ms, isStr := v.(string); isStr {
				uniq[ms] = true
			}
		}
		if len(uniq) > LoadBalanceMaxNodes {
			return bad("负载均衡「%s」最多支持 %d 个节点", name, LoadBalanceMaxNodes)
		}
		old := prevMembers[str(m, "id")]
		for _, v := range members {
			ms, isStr := v.(string)
			if !isStr || strings.TrimSpace(ms) == "" {
				return bad("负载均衡「%s」包含无效成员", name)
			}
			if !tagSet[ms] && !old[ms] {
				return bad("负载均衡「%s」只能添加真实节点：%s", name, ms)
			}
		}
	}
	return nil
}

// checkBandwidthFlags:带宽择优开关只能是布尔。看的是用户提交的原始内容(规范化会把别的值悄悄当成关,用户就不知道了)。
func checkBandwidthFlags(in []any) error {
	for _, g := range in {
		m, ok := g.(map[string]any)
		if !ok {
			continue
		}
		if v, has := m["bandwidth"]; has {
			if _, isBool := v.(bool); !isBool {
				return bad("节点组「%s」的 bandwidth 必须是 true 或 false", str(m, "name"))
			}
		}
	}
	return nil
}
