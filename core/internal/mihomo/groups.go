package mihomo

import (
	"fmt"
	"math"
	"time"

	"github.com/timeflysoon/Open-Box/core/internal/config"
	"github.com/timeflysoon/Open-Box/core/internal/groups"
	"github.com/timeflysoon/Open-Box/core/internal/jsx"
	"github.com/timeflysoon/Open-Box/core/internal/routing"
)

// 第二块:mihomoGroups(节点组 + 站点集 selector → Mihomo proxy-groups)。
//
// 组的语义沿用 groups.EmitUserGroups(成员解析、环 / 空组处理、failover 泳道都和 sing-box 配置一致),
// 再把得到的 sing-box 出站翻成 Mihomo 的组。目标是上游打过补丁的 Mihomo 内核(mihomo-v1.19.34-openbox.N):
// url-test / load-balance 带 `openbox` 选项就是 Open-Box 自己的自动组(带宽择优、拨号失败冷却、负载均衡对冲),
// 自动组的检测间隔要写在 `openbox-health-check-interval`(与 `interval` 互斥)。

// Mihomo 内置名;档案里的组 / 节点和它们重名时,内核靠 openbox-builtin-names 把内置的那个改个别名。
var reserved = []string{"DIRECT", "REJECT", "REJECT-DROP", "COMPATIBLE", "PASS", "PASS-RULE", "GLOBAL"}

// GroupsResult 对应 core 方法 mihomoGroups 的返回。
type GroupsResult struct {
	Proxies  []any                  `json:"proxies"`
	Groups   []any                  `json:"groups"`
	First    map[string]string      `json:"first"`
	Targets  map[string]string      `json:"targets"`
	Builtins map[string]string      `json:"builtins"`
	Failover []groups.FailoverState `json:"failover"`
}

// Groups 的入参:{profile, nodes, groups, chainNodes, selections}。
func Groups(args any) (*GroupsResult, error) {
	profile := jsx.Get(args, "profile")
	nodes, _ := jsx.Get(args, "nodes").([]any)
	userGroups := jsx.Get(args, "groups")
	if !jsx.Truthy(userGroups) {
		userGroups = []any{}
	}
	selections, _ := jsx.Get(args, "selections").(map[string]any)

	chain, err := config.ResolveChainNodes(jsx.Get(args, "chainNodes"), nodes, userGroups)
	if err != nil {
		return nil, err
	}
	allNodes := append(append([]any{}, nodes...), chain...)
	proxies, err := Proxies(allNodes)
	if err != nil {
		return nil, err
	}
	res, err := groups.EmitUserGroups(userGroups, allNodes, map[string]any{
		"testUrl":          jsx.Get(profile, "testUrl"),
		"expectedStatus":   jsx.Get(profile, "testExpectedStatus"),
		"disableBandwidth": jsx.Get(profile, "disableBandwidth"),
	})
	if err != nil {
		return nil, err
	}
	b := routing.NewBuiltin(res.Builtin.Direct, res.Builtin.Block, res.Builtin.DirectEnabled, res.Builtin.BlockEnabled)

	// 重名:档案里的组 / 节点占了内置名,内置的改别名。
	taken := map[string]bool{}
	for _, p := range proxies {
		taken[jsx.ToString(p.(map[string]any)["name"])] = true
	}
	for _, o := range res.Outbounds {
		taken[o.Tag] = true
	}
	r := routing.NormalizeRouting(jsx.Get(profile, "routing"))
	for _, p := range r.ActivePolicies {
		taken[p.Name] = true
	}
	taken[r.Fallback.Name] = true
	out := &GroupsResult{Proxies: proxies, Groups: []any{}, First: map[string]string{}, Targets: map[string]string{}, Builtins: map[string]string{}, Failover: res.Failover}
	if out.Failover == nil {
		out.Failover = []groups.FailoverState{}
	}
	for _, n := range reserved {
		if taken[n] {
			out.Builtins[n] = "__openbox_builtin_" + n + "__"
		}
	}
	name := func(n string) string {
		if a, ok := out.Builtins[n]; ok {
			return a
		}
		return n
	}
	// targets:档案里的出口名 → Mihomo 里的名字。上游 Node 用它判断一个出口名认不认识(终端分流、入口规则的出口),
	// 并把认识的名字换成 Mihomo 侧的写法。键:节点、公开组、站点集、兜底(都原样),内置直连 / 拒绝的名字,
	// 以及 "direct" / "block" 这两个关键字。
	for _, p := range proxies {
		n := jsx.ToString(p.(map[string]any)["name"])
		out.Targets[n] = n
	}
	for _, t := range res.PublicTags {
		out.Targets[t] = t
	}
	for _, p := range r.ActivePolicies {
		out.Targets[p.Name] = p.Name
	}
	out.Targets[r.Fallback.Name] = r.Fallback.Name
	for _, k := range []string{b.Direct(), "direct"} {
		if k != "" {
			out.Targets[k] = name("DIRECT")
		}
	}
	for _, k := range []string{b.Block(), "block"} {
		if k != "" {
			out.Targets[k] = name("REJECT")
		}
	}
	// 档案侧的名字 → Mihomo 侧的名字:内置直连 / 拒绝换成 DIRECT / REJECT。
	mapName := func(tag string) string {
		if b.Direct() != "" && tag == b.Direct() {
			return out.Targets["direct"]
		}
		if b.Block() != "" && tag == b.Block() {
			return out.Targets["block"]
		}
		return tag
	}
	mapList := func(in []string) []string {
		o := make([]string, 0, len(in))
		for _, t := range in {
			o = append(o, mapName(t))
		}
		if len(o) == 0 {
			o = append(o, out.Targets["block"])
		}
		return o
	}
	weights := scoreWeights(jsx.Get(profile, "nodeScoreWeights"))

	pickFirst := func(g string, members []string, preferred string) string {
		if s, ok := selections[g].(string); ok {
			if m := mapName(s); contains(members, m) {
				return m
			}
		}
		if p := mapName(preferred); preferred != "" && contains(members, p) {
			return p
		}
		return members[0]
	}
	placeholder := map[string]bool{}
	for _, t := range res.Placeholders {
		placeholder[t] = true
	}
	for _, o := range res.Outbounds {
		if o.Type == "direct" || o.Type == "block" || (placeholder[o.Tag] && o.Type == "selector" && (o.Tag == b.Direct() || o.Tag == b.Block())) {
			continue
		}
		members := mapList(o.Outbounds)
		g := map[string]any{"name": o.Tag}
		switch o.Type {
		case "selector":
			g["type"] = "select"
			first := pickFirst(o.Tag, members, o.Default)
			g["proxies"] = append([]string{first}, without(members, first)...)
			out.First[o.Tag] = first
			if o.InterruptExistConnections {
				g["interrupt-exist-connections"] = true
			}
		case "urltest", "loadbalance":
			if o.Type == "urltest" {
				g["type"] = "url-test"
				if o.Tolerance != nil {
					g["tolerance"] = *o.Tolerance
				}
			} else {
				g["type"] = "load-balance"
			}
			g["proxies"] = members
			g["url"] = o.URL
			if o.ExpectedStatus != "" {
				g["expected-status"] = o.ExpectedStatus
			}
			g["openbox-health-check-interval"] = checkInterval(o.Interval)
			ob := map[string]any{"bandwidth": o.Bandwidth}
			if weights != nil {
				ob["node_score_weights"] = weights
			}
			g["openbox"] = ob
			if o.InterruptExistConnections {
				g["interrupt-exist-connections"] = true
			}
			out.First[o.Tag] = members[0]
		default:
			return nil, fmt.Errorf("%s: no Mihomo group for outbound type %s", o.Tag, o.Type)
		}
		out.Groups = append(out.Groups, g)
	}

	// 站点集 / 兜底:select 组,成员是全部公开组 + 内置直连 / 拒绝。
	policyMembers := mapList(routing.PolicyOutboundOptions(res.PublicTags, b))
	addPolicy := func(n, preferred string) {
		first := pickFirst(n, policyMembers, routing.EffectiveOutbound(preferred, routing.PolicyOutboundOptions(res.PublicTags, b), b))
		out.Groups = append(out.Groups, map[string]any{"name": n, "type": "select", "proxies": append([]string{first}, without(policyMembers, first)...)})
		out.First[n] = first
	}
	for _, p := range r.ActivePolicies {
		addPolicy(p.Name, p.Default)
	}
	addPolicy(r.Fallback.Name, r.Fallback.Default)
	return out, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func without(l []string, s string) []string {
	o := make([]string, 0, len(l))
	for _, x := range l {
		if x != s {
			o = append(o, x)
		}
	}
	return o
}

// checkInterval:sing-box 的检测间隔("3m" / "300s")→ Mihomo 的 Go 时长;写不对就按 3 分钟。
func checkInterval(s string) string {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return s
	}
	return "3m"
}

// scoreWeights:档案的 nodeScoreWeights({latency, download, jitter},三项相加 100)→ 内核的 node_score_weights;不合法就不写。
func scoreWeights(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	var sum float64
	o := map[string]any{}
	for _, k := range []string{"latency", "download", "jitter"} {
		f, ok := m[k].(float64)
		if !ok || f < 0 {
			return nil
		}
		o[k] = f
		sum += f
	}
	if math.Abs(sum-100) > 1e-9 {
		return nil
	}
	return o
}
