package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 直连 IP 计划(双内核 3e-4b-2;对应上游 v0.1.462 Node 的 ai,「直连不进内核已关闭」那份)。
//
// 入口旁路(SC / mu,3e-4c~d)要知道「哪些 IP 段此刻是直连的」,好在入口(nft)就放行、不进内核。
// 这里把规整后的分流(core normalizeRouting 的返回)读成一份网段名单:
//   - 站点集 / 前置自定义分流里写死的 ip_cidr,出站落到直连的才算;
//   - 排在前面、走代理的规则里的网段先记下来(l),后面的直连网段要把它们扣掉,否则会抢走前面规则的地址;
//   - 前面有可能含 IP 段的规则(geoip / 规则集链接 / 规则集)走了代理,就只放行它前面的直连网段,其后的不放行(u);
//   - 有按端口分流的站点集、有走代理的端口规则、有「单独指定走代理的域名」,入口只按 IP 判不了,整份放弃。
// 开了 FakeIP 时再把假 IP 池(198.19.0.0/16、2001:2::/49)从结果里扣掉。

// CustomRule 是前置自定义分流的一行(core normalizeRouting 的 custom.rules[])。
type CustomRule struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	Outbound string `json:"outbound"`
}

// CustomPolicy 是前置自定义分流(上游 Kr 判断它生不生效:有对象、enabled 不是 false、rules 非空)。
type CustomPolicy struct {
	Enabled *bool        `json:"enabled"`
	Rules   []CustomRule `json:"rules"`
}

func (c *CustomPolicy) active() bool {
	return c != nil && (c.Enabled == nil || *c.Enabled) && len(c.Rules) > 0
}

// RoutingPolicy 是启用的站点集(activePolicies[])。直连 IP 计划用不到 Domain / DomainSuffix / DomainKeyword,
// 但 route-test 判断「DNS 规则是不是旧的」要按这些条件认归属,所以一起收进来。
type RoutingPolicy struct {
	Name          string   `json:"name"`
	Default       string   `json:"default"`
	Ports         []string `json:"ports,omitempty"`
	IPCidr        []string `json:"ipCidr"`
	Rulesets      []string `json:"rulesets"`
	Domain        []string `json:"domain"`
	DomainSuffix  []string `json:"domainSuffix"`
	DomainKeyword []string `json:"domainKeyword"`
}

// RoutingFallback 是兜底站点的名字与默认出口。
type RoutingFallback struct {
	Name    string `json:"name"`
	Default string `json:"default"`
}

// TargetOverride 是「单独指定某个目标走哪个站点集」解析后的一条(上游 v0.1.423 起;name / default 是它指向的站点集)。
// 现在的 core normalizeRouting 不返回这个字段,这里照上游留着:core 以后带上就自动生效,没带就是空。
type TargetOverride struct {
	Name    string `json:"name"`
	Default string `json:"default"`
	CIDR    string `json:"cidr"`
}

// RoutingModel 是 core normalizeRouting 的返回里直连 IP 计划用得到的部分(上游 ke 的返回)。
type RoutingModel struct {
	Custom          *CustomPolicy    `json:"custom"`
	ActivePolicies  []RoutingPolicy  `json:"activePolicies"`
	Fallback        RoutingFallback  `json:"fallback"`
	TargetOverrides []TargetOverride `json:"targetOverrides,omitempty"`
}

// DirectIPOptions 是 ai 的第二个参数。零值等于上游的各项默认。
type DirectIPOptions struct {
	// Members:出站候选(上游 members,默认 ["direct"])。nil = 默认;空切片就是「一个都没有」。
	Members []string
	// Builtin:内置出站的名字,零值 = direct / block。
	Builtin BuiltinNames
	// Selections:用户选中的出站(上游 selections),键是站点集名,值是出站名,值也可以是另一个键(最多跟 16 层)。
	Selections map[string]any
	// FakeIP:开了 FakeIP 就扣掉假 IP 池。
	FakeIP bool
	// Disabled:上游 enabled=false(「直连不进内核」没开)。
	Disabled bool
}

// DirectIPPlan 是 ai 的返回。Key 是 CIDRs 排序后的 JSON(给缓存和指纹用),没有计划时是空串。
type DirectIPPlan struct {
	CIDRs  []string `json:"cidrs"`
	Key    string   `json:"key"`
	Reason string   `json:"reason"`
}

func noDirectIPPlan(reason string) DirectIPPlan {
	return DirectIPPlan{CIDRs: []string{}, Key: "", Reason: reason}
}

// outboundName 对应上游 sa:direct / block 换成内置出站的名字,别的原样。
func outboundName(outbound string, b BuiltinNames) string {
	switch outbound {
	case "direct":
		return b.Direct
	case "block":
		return b.Block
	}
	return outbound
}

// followSelection 对应上游 Yf:从 name 出发顺着选中项一路跟下去(最多 16 层、遇到走过的键就停)。
// 值不是字符串时(档案里不该有)当作落不到任何出站,返回 false。
func followSelection(sel map[string]any, name string) (string, bool) {
	n := name
	seen := map[string]bool{}
	for i := 0; i < 16; i++ {
		v, ok := sel[n]
		if !ok || seen[n] {
			break
		}
		seen[n] = true
		s, isStr := v.(string)
		if !isStr {
			return "", false
		}
		n = s
	}
	return n, true
}

// pickMember 对应上游 o$:站点集的默认出站落到成员里的哪一个。
func pickMember(def string, members []string, b BuiltinNames) (string, bool) {
	n := def
	switch def {
	case "direct":
		n = b.Direct
	case "block":
		n = b.Block
	}
	if containsString(members, n) {
		return n, true
	}
	if def == "proxy" {
		for _, m := range members {
			if m != b.Direct && m != b.Block && m != "" { // 上游 `if(o)`:空串当没找到
				return m, true
			}
		}
	}
	if len(members) == 0 {
		return "", false
	}
	return members[0], true
}

// chosenIsDirect 对应上游 Os:这个站点集此刻的出站是不是直连(选中项优先,没选按默认出站在成员里取)。
func chosenIsDirect(name, def string, members []string, b BuiltinNames, sel map[string]any) bool {
	var (
		got string
		ok  bool
	)
	if _, picked := sel[name]; picked {
		got, ok = followSelection(sel, name)
	} else {
		got, ok = pickMember(def, members, b)
	}
	return ok && got == b.Direct
}

// BuildDirectIPPlan 对应上游 ai(纯函数,入参是 core normalizeRouting 已经规整过的分流)。
func BuildDirectIPPlan(r RoutingModel, opt DirectIPOptions) DirectIPPlan {
	if opt.Disabled {
		return noDirectIPPlan("直连不进内核已关闭")
	}
	members := opt.Members
	if members == nil {
		members = []string{"direct"}
	}
	b := opt.Builtin
	if b == (BuiltinNames{}) {
		b = BuiltinNames{Direct: "direct", Block: "block"}
	}
	sel := opt.Selections
	for _, p := range r.ActivePolicies {
		if len(p.Ports) > 0 {
			return noDirectIPPlan(fmt.Sprintf("站点集「%s」按端口分流,入口按 IP 判不了", p.Name))
		}
	}

	var (
		l []string // 前面规则占着的网段(走代理的)
		c []string // 能放行的直连网段
		u string   // 前面有可能含 IP 段的规则走了代理:之后的直连网段不放行
	)
	direct := func(cidrs []string) {
		kept, _ := SubtractRanges(ParseRanges(cidrs...), ParseRanges(l...))
		c = append(c, RangesToCIDRs(kept)...)
	}

	if r.Custom.active() {
		for _, h := range r.Custom.Rules {
			w := outboundName(h.Outbound, b) == b.Direct
			if h.Type == "port" {
				if !w {
					return noDirectIPPlan(fmt.Sprintf("前置自定义分流「%s」按端口走代理,入口不能只按 IP 放行", h.Value))
				}
				continue
			}
			if h.Type == "ipCidr" {
				if w {
					direct([]string{h.Value})
				} else {
					l = append(l, h.Value)
				}
				continue
			}
			if !w && (h.Type == "geoip" || h.Type == "ruleUrl" || h.Type == "ruleset") {
				u = fmt.Sprintf("前置自定义分流「%s」可能包含 IP 段", h.Value)
			}
		}
	}

	for _, o := range r.TargetOverrides {
		if chosenIsDirect(o.Name, o.Default, members, b, sel) {
			continue
		}
		if o.CIDR == "" {
			return noDirectIPPlan("有单独指定走代理的域名,入口无法按真实 IP 区分")
		}
		l = append(l, o.CIDR)
	}

	for _, p := range r.ActivePolicies {
		if chosenIsDirect(p.Name, p.Default, members, b, sel) {
			if u == "" {
				direct(p.IPCidr)
			}
			continue
		}
		l = append(l, p.IPCidr...)
		if u == "" && hasIPRuleset(p.Rulesets) {
			u = fmt.Sprintf("站点集「%s」排在前面且可能包含 IP 段", p.Name)
		}
	}

	var holes []Range
	if opt.FakeIP {
		holes = ParseRanges(fakeIPv4Pool, fakeIPv6Pool)
	}
	kept, _ := SubtractRanges(MergeRanges(ParseRanges(c...)), holes)
	m := RangesToCIDRs(kept)
	if m == nil {
		m = []string{}
	}
	var reason string
	switch {
	case len(m) > 0 && u != "":
		reason = fmt.Sprintf("%s,其后的直连 IP 段不旁路;直连 IP 段 %d 计入入口", u, len(m))
	case len(m) > 0:
		reason = fmt.Sprintf("直连 IP 段 %d 计入入口", len(m))
	case u != "":
		reason = u
	default:
		reason = "没有可旁路的直连 IP 段"
	}
	sorted := make([]string, len(m)) // 不能是 nil:没有网段时 Key 要是 "[]",不是 "null"
	copy(sorted, m)
	sort.Strings(sorted)
	key, _ := json.Marshal(sorted)
	return DirectIPPlan{CIDRs: m, Key: string(key), Reason: reason}
}

// hasIPRuleset:规则集名可能带 IP 段(geoip-* 或用户规则集链接 list-*)。
func hasIPRuleset(sets []string) bool {
	for _, s := range sets {
		if strings.HasPrefix(s, "geoip-") || strings.HasPrefix(s, "list-") {
			return true
		}
	}
	return false
}

// DirectIPPlan 先让 core 把档案里的 routing 规整一遍(normalizeRouting),再算直连 IP 计划。
func (c *Compiler) DirectIPPlan(ctx context.Context, routing any, opt DirectIPOptions) (DirectIPPlan, error) {
	raw, err := c.call(ctx, "normalizeRouting", routing)
	if err != nil {
		return DirectIPPlan{}, err
	}
	var model RoutingModel
	if err := json.Unmarshal(raw, &model); err != nil {
		return DirectIPPlan{}, fmt.Errorf("core: 解析 normalizeRouting 的返回值失败: %w", err)
	}
	return BuildDirectIPPlan(model, opt), nil
}
