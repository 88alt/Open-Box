package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 路由计划(双内核 3e-4b-3;对应上游 v0.1.462 Node 的 DE / ug / si)。
//
// 入口旁路要用到三样东西:
//   - bypassPlan:哪些站点集此刻走直连、可以在入口就放走(core 的方法,3e-4b-1 里已经会用它);
//   - 直连 IP 计划:哪些 IP 段是直连的(3e-4b-2 的 ai,开了 FakeIP 时才有);
//   - entryPlan:入口默认放行(whitelist)还是默认进内核(blacklist),以及「必须进内核」的集合 / 网段。
//
// 这里把上游 DE 的装配按同一顺序走一遍:先按组此刻的第一选择算出「站点集 → 出口」,
// 再规整终端分流;有终端被指定走非直连出口时,入口计划与旁路计划都不看终端分流(那些终端整个进内核)。
// ug 再把这三样算成一个指纹,部署进配置里,下次生成时比对。

// entryModeWhitelist 是 core entryPlan 的「入口默认放行」模式(另一个是 blacklist)。
const entryModeWhitelist = "whitelist"

// EntryPlan 是 core entryPlan 的返回(core/internal/routing/entry.go)。
type EntryPlan struct {
	Mode         string   `json:"mode"`
	Reason       string   `json:"reason"`
	FakeIP       bool     `json:"fakeIp"`
	NeedSets     []string `json:"needSets"`
	NeedCidrs    []string `json:"needCidrs"`
	DirectAnswer bool     `json:"directAnswer"`
}

// SourceScopedFallback 对应上游的 sourceScopedFallback:入口是白名单模式时有终端被指定走非直连出口,
// 那些终端仍要整个进内核,于是把「必须进内核」的集合 / 网段按来源收窄一份给入口用。
type SourceScopedFallback struct {
	NeedSets  []string `json:"needSets"`
	NeedCidrs []string `json:"needCidrs"`
}

// PlanOptions 是 core bypassPlan / entryPlan 的第二个参数(字段名同 core 的 planOpts)。
// Members 为 nil 时 core 按 ["direct"] 处理、空切片就是「一个成员都没有」,所以这里不加 omitempty。
type PlanOptions struct {
	Members      []string       `json:"members"`
	Builtin      BuiltinNames   `json:"builtin"`
	Selections   map[string]any `json:"selections"`
	ClientRoutes []ClientRoute  `json:"clientRoutes"`
	FakeIP       bool           `json:"fakeIp"`
	DNSMode      string         `json:"dnsMode"`
}

// BypassPlanOf 调 core 的 bypassPlan(routing, opts)(对应上游 gn)。
func (c *Compiler) BypassPlanOf(ctx context.Context, routing any, opts PlanOptions) (BypassPlan, error) {
	raw, err := c.call(ctx, "bypassPlan", routing, opts)
	if err != nil {
		return BypassPlan{}, err
	}
	var p BypassPlan
	if err := json.Unmarshal(raw, &p); err != nil {
		return BypassPlan{}, fmt.Errorf("core: 解析 bypassPlan 的返回值失败: %w", err)
	}
	return p, nil
}

// EntryPlanOf 调 core 的 entryPlan(routing, opts)(对应上游 hl,si 包的那一层)。
func (c *Compiler) EntryPlanOf(ctx context.Context, routing any, opts PlanOptions) (EntryPlan, error) {
	raw, err := c.call(ctx, "entryPlan", routing, opts)
	if err != nil {
		return EntryPlan{}, err
	}
	var p EntryPlan
	if err := json.Unmarshal(raw, &p); err != nil {
		return EntryPlan{}, fmt.Errorf("core: 解析 entryPlan 的返回值失败: %w", err)
	}
	return p, nil
}

// bypassPlanKeyItem 是 jn 里 pending 的一项(键序同上游的字面量:policy、sets、against)。
type bypassPlanKeyItem struct {
	Policy  string              `json:"policy"`
	Sets    []string            `json:"sets"`
	Against []bypassPlanKeyRule `json:"against"`
}

// bypassPlanKeyRule 是 jn 里 against 的一项(键序同上游:name、geoip、cidrs、lists)。
type bypassPlanKeyRule struct {
	Name  string   `json:"name"`
	Geoip []string `json:"geoip"`
	Cidrs []string `json:"cidrs"`
	Lists []string `json:"lists,omitempty"`
}

// bypassPlanKeyBody 是 jn 的结果(键序同上游:sets、pending、fakeIp)。
type bypassPlanKeyBody struct {
	Sets    []string            `json:"sets"`
	Pending []bypassPlanKeyItem `json:"pending"`
	FakeIP  bool                `json:"fakeIp"`
}

// BypassPlanKey 对应上游 jn:旁路计划的规范化指纹。各名单排过序(不受顺序影响),
// pending 按 policy 排序;enabled / reason 不进指纹(它们是结果,不是输入)。
func BypassPlanKey(p *BypassPlan) string {
	if p == nil {
		return ""
	}
	body := bypassPlanKeyBody{Sets: sortedStrings(p.Sets), Pending: []bypassPlanKeyItem{}, FakeIP: p.FakeIP}
	for _, item := range p.Pending {
		it := bypassPlanKeyItem{Policy: item.Policy, Sets: sortedStrings(item.Sets), Against: []bypassPlanKeyRule{}}
		for _, r := range item.Against {
			it.Against = append(it.Against, bypassPlanKeyRule{
				Name: r.Name, Geoip: sortedStrings(r.Geoip), Cidrs: sortedStrings(r.Cidrs), Lists: sortedStrings(r.Lists),
			})
		}
		body.Pending = append(body.Pending, it)
	}
	sort.SliceStable(body.Pending, func(i, j int) bool { return body.Pending[i].Policy < body.Pending[j].Policy })
	return encodeNoHTML(body)
}

func sortedStrings(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	sort.Strings(out)
	return out
}

func encodeNoHTML(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return strings.TrimRight(b.String(), "\n")
}

// RouteSelections 对应上游 DE 里把「组名 → 此刻的选择」摊平进 selections:
// select 组取它此刻的第一选择(first,没给就用 model.First),别的组取自己的名字;
// 值等于 targets 里的 direct / block 时换成内置出站的名字。用户自己选过的项被这份覆盖(与上游的 {...n, ...i} 一致)。
func RouteSelections(model *GroupsModel, first map[string]string, builtin BuiltinNames, user map[string]any) map[string]any {
	out := make(map[string]any, len(user))
	for k, v := range user {
		out[k] = v
	}
	if model == nil {
		return out
	}
	if first == nil {
		first = model.First
	}
	for _, g := range model.Groups {
		name, _ := g["name"].(string)
		if name == "" {
			continue
		}
		typ, _ := g["type"].(string)
		h := name
		if typ == "select" {
			if v, ok := first[name]; ok {
				h = v
			} else {
				h = model.First[name]
			}
		}
		if t, ok := model.Targets["direct"]; ok && h == t {
			h = builtin.Direct
		} else if t, ok := model.Targets["block"]; ok && h == t {
			h = builtin.Block
		}
		out[name] = h
	}
	return out
}

// FakeIPForProxy 对应上游 yt:开了 FakeIP 且用来给走代理的域名发占位地址。
func FakeIPForProxy(profile map[string]any) bool {
	dns, _ := profile["dns"].(map[string]any)
	if dns == nil {
		return false
	}
	if b, ok := dns["split"].(bool); ok && !b {
		return false
	}
	return isTrue(dns["fakeIpForProxy"])
}

// DNSModeOf 对应上游 DE 里的 `dns?.mode || "hijack"`。
func DNSModeOf(profile map[string]any) string {
	if dns, _ := profile["dns"].(map[string]any); dns != nil {
		if s, _ := dns["mode"].(string); s != "" {
			return s
		}
	}
	return "hijack"
}

// RoutePlanInput 是 RoutePlan 的入参:档案、内置出站名、组的选择范围、用户选中项,以及 mihomoGroups 的返回(3e-3)。
type RoutePlanInput struct {
	Profile    map[string]any
	Builtin    BuiltinNames
	Members    []string
	Selections map[string]any
	Model      *GroupsModel
}

// RoutePlan 是 DE 的返回 + ug 算出的指纹。
type RoutePlan struct {
	Plan     BypassPlan
	DirectIP DirectIPPlan
	Fallback *SourceScopedFallback // 只有 si 给了 sourceScopedFallback 时才不是 nil
	Scoped   bool                  // 有终端被指定走非直连出口
	Key      string                // ug
}

// RoutePlan 对应上游 DE:算出旁路计划、直连 IP 计划与来源收窄的兜底,并算好指纹(ug)。
func (c *Compiler) RoutePlan(ctx context.Context, in RoutePlanInput) (RoutePlan, error) {
	b := in.Builtin
	if b == (BuiltinNames{}) {
		b = BuiltinNames{Direct: "direct", Block: "block"}
	}
	members := in.Members
	if members == nil {
		members = []string{"direct"}
	}
	profile := in.Profile
	if profile == nil {
		profile = map[string]any{}
	}
	routing := profile["routing"]
	sel := RouteSelections(in.Model, nil, b, in.Selections)
	routes := NormalizeClientRoutes(profile["clientRoutes"], b.Direct)
	fakeIP := FakeIPForProxy(profile)
	opts := PlanOptions{
		Members: members, Builtin: b, Selections: sel, ClientRoutes: routes,
		FakeIP: fakeIP, DNSMode: DNSModeOf(profile),
	}
	// 有终端要进内核(没标 bypass)时,旁路计划与直连 IP 计划都不看终端分流:
	// 入口要按来源分辨这些终端,而旁路是「按目标 IP」放的,两件事凑不到一起,上游同样清空。
	scoped := false
	for _, r := range routes {
		if !r.Bypass {
			scoped = true
			break
		}
	}
	plain := opts
	if scoped {
		plain.ClientRoutes = []ClientRoute{}
	}
	plan, err := c.BypassPlanOf(ctx, routing, plain)
	if err != nil {
		return RoutePlan{}, err
	}
	rm, err := c.routingModel(ctx, routing)
	if err != nil {
		return RoutePlan{}, err
	}
	directIP := DirectIPPlan{CIDRs: []string{}}
	if fakeIP {
		directIP = BuildDirectIPPlan(rm, DirectIPOptions{Members: members, Builtin: b, Selections: sel, FakeIP: true})
	}
	var fallback *SourceScopedFallback
	if hasNonDirectRoute(routes, b.Direct) {
		variant, err := c.EntryPlanOf(ctx, routing, plain)
		if err != nil {
			return RoutePlan{}, err
		}
		if variant.Mode == entryModeWhitelist && !hasProxyUserRuleset(rm, members, b, sel) {
			fallback = &SourceScopedFallback{
				NeedSets:  nonNilStrings(variant.NeedSets),
				NeedCidrs: nonNilStrings(variant.NeedCidrs),
			}
		}
	}
	out := RoutePlan{Plan: plan, DirectIP: directIP, Fallback: fallback, Scoped: scoped}
	out.Key = RoutePlanKey(out)
	return out, nil
}

// routingModel 调一次 core 的 normalizeRouting(上游 ke)。旁路 / 入口计划在 core 里各自会再规整一遍,
// 这里这一份只给直连 IP 计划与「有没有走代理的用户规则集」用。
func (c *Compiler) routingModel(ctx context.Context, routing any) (RoutingModel, error) {
	raw, err := c.call(ctx, "normalizeRouting", routing)
	if err != nil {
		return RoutingModel{}, err
	}
	var m RoutingModel
	if err := json.Unmarshal(raw, &m); err != nil {
		return RoutingModel{}, fmt.Errorf("core: 解析 normalizeRouting 的返回值失败: %w", err)
	}
	return m, nil
}

// hasNonDirectRoute 是 si 的第一个条件:有终端被指定走非直连的出口(这些终端整个进内核,
// 入口默认放行对它们不成立,才需要按来源收窄一份「必须进内核」的名单)。
func hasNonDirectRoute(routes []ClientRoute, directTag string) bool {
	for _, r := range routes {
		if r.Outbound != "" && r.Outbound != directTag {
			return true
		}
	}
	return false
}

// hasProxyUserRuleset 是 si 的第三个条件:有站点集引用了用户规则集链接(list-*)、而它此刻不走直连。
func hasProxyUserRuleset(rm RoutingModel, members []string, b BuiltinNames, sel map[string]any) bool {
	for _, p := range rm.ActivePolicies {
		has := false
		for _, tag := range p.Rulesets {
			if strings.HasPrefix(tag, "list-") {
				has = true
				break
			}
		}
		if has && !chosenIsDirect(p.Name, p.Default, members, b, sel) {
			return true
		}
	}
	return false
}

// RoutePlanKey 对应上游 ug:终端分流的收窄状态 + 旁路计划指纹 + 直连 IP 段 + 兜底计划的集合 / 网段。
func RoutePlanKey(p RoutePlan) string {
	var fb any
	if p.Fallback != nil {
		fb = []any{nonNilStrings(p.Fallback.NeedSets), nonNilStrings(p.Fallback.NeedCidrs)}
	}
	return StableJSON([]any{p.Scoped, BypassPlanKey(&p.Plan), nonNilStrings(p.DirectIP.CIDRs), fb})
}
