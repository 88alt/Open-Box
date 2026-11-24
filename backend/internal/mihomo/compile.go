package mihomo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

// 调 core 的 mihomoGroups / mihomoTargetRules(双内核 3e-3;对应上游 v0.1.462 Node 的 hC / yC / gC / AC)。
//
// core 只管把「节点组、站点集、分流」算成 Mihomo 的 proxy-groups 和 rules 字符串;
// 这里负责三件事:缓存(同样的输入不重复起 core 进程)、把用户在面板里选中的节点套到 select 组上(gC)、
// 把「某个组最终落到哪里」归成 direct / reject / proxy(AC,DNS 与入口旁路按它决定要不要走代理)。

// Call 调 core 的一个方法,返回 value 的原始 JSON(接线处把 *core.Client 的 Call 包一下就行)。
// args 按顺序对应 core 方法的参数:多数方法只有一个,bypassPlan / entryPlan 是 (routing, opts) 两个。
type Call func(ctx context.Context, method string, args ...any) (json.RawMessage, error)

// 缓存容量:与上游一致(组 4 份、规则 16 份)。
const (
	groupsCacheSize = 4
	rulesCacheSize  = 16
)

// Compiler 带缓存地调 core。并发安全。
type Compiler struct {
	call   Call
	mu     sync.Mutex
	groups *lru[*GroupsModel]
	rules  *lru[*RulesModel]
}

// NewCompiler 创建 Compiler。
func NewCompiler(call Call) *Compiler {
	return &Compiler{call: call, groups: newLRU[*GroupsModel](groupsCacheSize), rules: newLRU[*RulesModel](rulesCacheSize)}
}

// lru 是上游 pg 那种「先进先出、写入时刷新位置」的小缓存:命中不改顺序,写入(含覆盖)放到最新,满了丢最旧的。
type lru[V any] struct {
	max  int
	keys []string
	m    map[string]V
}

func newLRU[V any](max int) *lru[V] { return &lru[V]{max: max, m: map[string]V{}} }

func (l *lru[V]) get(k string) (V, bool) { v, ok := l.m[k]; return v, ok }

func (l *lru[V]) set(k string, v V) {
	if _, ok := l.m[k]; ok {
		for i, x := range l.keys {
			if x == k {
				l.keys = append(l.keys[:i], l.keys[i+1:]...)
				break
			}
		}
	} else if len(l.keys) >= l.max {
		delete(l.m, l.keys[0])
		l.keys = l.keys[1:]
	}
	l.keys = append(l.keys, k)
	l.m[k] = v
}

func digest(parts ...[]byte) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonical 把值写成稳定的 JSON(map 键按字典序;不转义 < > &),只用来算缓存键。
func canonical(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte(fmt.Sprintf("%v", v))
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

func decodeNumber(raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 数字保持原样,别把 8443 之类变成浮点再写回去
	return dec.Decode(out)
}

// GroupsInput 是 mihomoGroups 的入参(字段含义见 core/internal/mihomo/groups.go)。
// Selections 是面板里选中的节点({组名: 节点 tag 或 direct/block}),不参与缓存键、也不传给 core,由 ApplySelections 套上。
type GroupsInput struct {
	Profile    any
	Nodes      any
	Groups     any
	ChainNodes any
	Selections map[string]any
}

// GroupsModel 是 mihomoGroups 的返回(已套上选中项)。
type GroupsModel struct {
	Proxies  []any             `json:"proxies"`
	Groups   []map[string]any  `json:"groups"`
	First    map[string]string `json:"first"`
	Targets  map[string]string `json:"targets"`
	Builtins map[string]string `json:"builtins"`
	Failover json.RawMessage   `json:"failover"`
}

// Groups 对应上游 hC。baseKey 非空时直接当缓存键(调用方保证它已覆盖 profile / nodes / groups / chainNodes),
// 否则按这四项的内容算。同样的键只起一次 core;每次返回的都是新套了 Selections 的副本,可以放心改。
func (c *Compiler) Groups(ctx context.Context, in GroupsInput, baseKey string) (*GroupsModel, error) {
	body := map[string]any{
		"profile": in.Profile, "nodes": in.Nodes, "groups": in.Groups, "chainNodes": in.ChainNodes,
		"selections": map[string]any{}, // core 要的是不带选中项的结果
	}
	key := baseKey
	if key == "" {
		key = digest(canonical(map[string]any{"profile": in.Profile, "nodes": in.Nodes, "groups": in.Groups, "chainNodes": in.ChainNodes}))
	}
	c.mu.Lock()
	base, ok := c.groups.get(key)
	c.mu.Unlock()
	if !ok {
		raw, err := c.call(ctx, "mihomoGroups", body)
		if err != nil {
			return nil, err
		}
		base = &GroupsModel{}
		if err := decodeNumber(raw, base); err != nil {
			return nil, fmt.Errorf("core: 解析 mihomoGroups 的返回值失败: %w", err)
		}
		c.mu.Lock()
		c.groups.set(key, base)
		c.mu.Unlock()
	}
	return base.ApplySelections(in.Selections), nil
}

// ApplySelections 对应上游 gC:把选中项放到 select 组成员的最前面,并更新 First(各组此刻的第一选择)。
// 选中项可以是 Targets 里的别名(direct / block 换成内置出站的名字),也可以是节点名;
// 不在组成员里、已经在最前面、组只有一个成员,都原样不动。不改调用者的对象,返回新的。
func (m *GroupsModel) ApplySelections(selections map[string]any) *GroupsModel {
	out := &GroupsModel{Proxies: m.Proxies, Targets: m.Targets, Builtins: m.Builtins, Failover: m.Failover}
	out.First = make(map[string]string, len(m.First))
	for k, v := range m.First {
		out.First[k] = v
	}
	out.Groups = make([]map[string]any, len(m.Groups))
	for i, g := range m.Groups {
		out.Groups[i] = g
		name, _ := g["name"].(string)
		if typ, _ := g["type"].(string); typ != "select" {
			continue
		}
		members, ok := g["proxies"].([]any)
		if !ok || len(members) <= 1 {
			continue
		}
		pick, _ := selections[name].(string)
		if pick == "" {
			continue
		}
		if t, ok := m.Targets[pick]; ok {
			pick = t
		}
		if first, _ := members[0].(string); first == pick {
			continue
		}
		found := false
		for _, x := range members {
			if s, _ := x.(string); s == pick {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		reordered := make([]any, 0, len(members))
		reordered = append(reordered, pick)
		for _, x := range members {
			if s, _ := x.(string); s != pick {
				reordered = append(reordered, x)
			}
		}
		ng := make(map[string]any, len(g))
		for k, v := range g {
			ng[k] = v
		}
		ng["proxies"] = reordered
		out.Groups[i] = ng
		out.First[name] = pick
	}
	return out
}

// ClassOf 对应上游 AC:沿着「select 组此刻的第一选择」一路走下去,看最后落在哪里。
// 落到 direct 内置出站 → "direct",落到 block 内置出站 → "reject",遇到非 select 的组(自动选择等)、
// 绕成环、或者落在节点上 → "proxy"。
func (m *GroupsModel) ClassOf() func(name string) string {
	types := make(map[string]string, len(m.Groups))
	for _, g := range m.Groups {
		n, _ := g["name"].(string)
		t, _ := g["type"].(string)
		types[n] = t
	}
	return func(name string) string {
		seen := map[string]bool{}
		for {
			next, isGroup := m.First[name]
			if !isGroup || seen[name] {
				break
			}
			seen[name] = true
			if types[name] != "select" {
				return "proxy"
			}
			name = next
		}
		if t, ok := m.Targets["direct"]; ok && name == t {
			return "direct"
		}
		if t, ok := m.Targets["block"]; ok && name == t {
			return "reject"
		}
		return "proxy"
	}
}

// RulesInput 是 mihomoTargetRules 的入参。Sections 一直传 true(要段下标)。
type RulesInput struct {
	Routing        any
	Providers      any // {规则集名: "domain" | "ipcidr"}
	RuleLists      any
	Targets        any
	DirectDomains  any
	DirectCIDRs    any
	TerminalRoutes any
}

// RulesModel 是 mihomoTargetRules 的返回:规则文本和各段的起点下标(分段见 core/internal/routing/mihomo_rules.go)。
type RulesModel struct {
	Rules          []string `json:"rules"`
	FirstTerminal  int      `json:"firstTerminal"`
	EndTerminal    int      `json:"endTerminal"`
	DirectTerminal int      `json:"directTerminal"`
	FirstPolicy    int      `json:"firstPolicy"`
}

// CompatInput 是 mihomoCompatibility 的入参。
type CompatInput struct {
	Profile    any
	Nodes      any
	Groups     any
	ChainNodes any
	Selections any
}

// Compatibility 对应上游 `i("mihomoCompatibility", …)`:返回 Mihomo 表达不了的问题(空 = 没有)。
func (c *Compiler) Compatibility(ctx context.Context, in CompatInput) ([]string, error) {
	raw, err := c.call(ctx, "mihomoCompatibility", map[string]any{
		"profile": in.Profile, "nodes": in.Nodes, "groups": in.Groups,
		"chainNodes": in.ChainNodes, "selections": in.Selections,
	})
	if err != nil {
		return nil, err
	}
	var problems []string
	if err := json.Unmarshal(raw, &problems); err != nil {
		return nil, fmt.Errorf("core: 解析 mihomoCompatibility 的返回值失败: %w", err)
	}
	return problems, nil
}

// BuiltinNames 是内置出站的名字与启用开关(上游 Dn / core builtinTags 的返回)。
// 零值 = 默认的 direct / block,且两个都算启用(DirectEnabled / BlockEnabled 为 nil 时按启用算)。
type BuiltinNames struct {
	Direct        string `json:"direct"`
	Block         string `json:"block"`
	DirectEnabled *bool  `json:"directEnabled,omitempty"`
	BlockEnabled  *bool  `json:"blockEnabled,omitempty"`
}

func (b BuiltinNames) directOn() bool { return b.DirectEnabled == nil || *b.DirectEnabled }
func (b BuiltinNames) blockOn() bool  { return b.BlockEnabled == nil || *b.BlockEnabled }

// withDefaults 把空名字补成 direct / block。
func (b BuiltinNames) withDefaults() BuiltinNames {
	if b.Direct == "" {
		b.Direct = "direct"
	}
	if b.Block == "" {
		b.Block = "block"
	}
	return b
}

// BuiltinTags 对应上游 Re:调 core 的 builtinTags,拿内置直连 / 拒绝此刻的名字与启用开关。
func (c *Compiler) BuiltinTags(ctx context.Context, groups any) (BuiltinNames, error) {
	raw, err := c.call(ctx, "builtinTags", groups)
	if err != nil {
		return BuiltinNames{}, err
	}
	var b BuiltinNames
	if err := json.Unmarshal(raw, &b); err != nil {
		return BuiltinNames{}, fmt.Errorf("core: 解析 builtinTags 的返回值失败: %w", err)
	}
	return b.withDefaults(), nil
}

// EmitResult 是 core emitGroups 里装配时用得到的部分。
type EmitResult struct {
	PublicTags []string `json:"publicTags"`
	Outbounds  []any    `json:"outbounds"`
}

// PublicTags 对应上游 `Wt(s,o,{}).publicTags`:用户分组的公开成员(能当出站名字用的那些)。
func (c *Compiler) PublicTags(ctx context.Context, groups, nodes any) (EmitResult, error) {
	raw, err := c.call(ctx, "emitGroups", groups, nodes, map[string]any{})
	if err != nil {
		return EmitResult{}, err
	}
	var out EmitResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return EmitResult{}, fmt.Errorf("core: 解析 emitGroups 的返回值失败: %w", err)
	}
	return out, nil
}

// NormalizeRouting 调 core 的 normalizeRouting(上游 ke),返回规整后的分流。
func (c *Compiler) NormalizeRouting(ctx context.Context, routing any) (RoutingModel, error) {
	return c.routingModel(ctx, routing)
}

// TargetRules 对应上游 yC。baseKey 非空时,缓存键只看 baseKey 加 providers / ruleLists / directCIDRs / terminalRoutes
// (调用方保证 baseKey 已覆盖 routing、targets、directDomains);为空就按整个入参算。返回的 Rules 是新切片,可以放心改。
func (c *Compiler) TargetRules(ctx context.Context, in RulesInput, baseKey string) (*RulesModel, error) {
	body := map[string]any{
		"routing": in.Routing, "providers": in.Providers, "ruleLists": in.RuleLists, "targets": in.Targets,
		"directDomains": in.DirectDomains, "directCIDRs": in.DirectCIDRs, "terminalRoutes": in.TerminalRoutes,
		"sections": true,
	}
	var key string
	if baseKey != "" {
		key = digest([]byte(baseKey), canonical([]any{in.Providers, in.RuleLists, in.DirectCIDRs, in.TerminalRoutes}))
	} else {
		key = digest(canonical(body))
	}
	c.mu.Lock()
	base, ok := c.rules.get(key)
	c.mu.Unlock()
	if !ok {
		raw, err := c.call(ctx, "mihomoTargetRules", body)
		if err != nil {
			return nil, err
		}
		base = &RulesModel{}
		if err := json.Unmarshal(raw, base); err != nil {
			return nil, fmt.Errorf("core: 解析 mihomoTargetRules 的返回值失败: %w", err)
		}
		c.mu.Lock()
		c.rules.set(key, base)
		c.mu.Unlock()
	}
	out := *base
	out.Rules = append([]string(nil), base.Rules...)
	return &out, nil
}
