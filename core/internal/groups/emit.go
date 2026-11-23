package groups

import (
	"encoding/json"
	"fmt"

	"github.com/timeflysoon/Open-Box/core/internal/jsx"
)

// 对应上游 engine/user-groups.mjs(v0.1.318)的 emitUserGroups:把节点组翻译成 sing-box 出站,并挡住内核不拦的三类问题
// (成员为空 / 引用不存在的出站 / 环)。

// Outbound 是写进 sing-box 配置的出站。键顺序和旧实现一致。
type Outbound struct {
	Type                      string        `json:"type"`
	Tag                       string        `json:"tag"`
	Outbounds                 []string      `json:"outbounds,omitempty"`
	URL                       string        `json:"url,omitempty"`
	Interval                  string        `json:"interval,omitempty"`
	Tolerance                 *float64      `json:"tolerance,omitempty"`
	IdleTimeout               string        `json:"idle_timeout,omitempty"`
	Default                   string        `json:"default,omitempty"`
	InterruptExistConnections bool          `json:"interrupt_exist_connections,omitempty"`
	ExpectedStatus            string        `json:"expected_status,omitempty"`    // 可接受状态码,放最后(JS 里是 { ...outbound, expected_status })
	Bandwidth                 bool          `json:"bandwidth,omitempty"`          // 带宽择优:只在 urltest / loadbalance 上写,内核据此把实测带宽算进选择
	NodeScoreWeights          *ScoreWeights `json:"node_score_weights,omitempty"` // 节点评分权重:只在带宽择优开着的 urltest / loadbalance 上写,来自档案的 nodeScoreWeights
}

// MarshalJSON:负载均衡的 outbounds 即使是空的也要写出来(与上游一致,不能被 omitempty 吞掉);其它类型照旧。
func (o Outbound) MarshalJSON() ([]byte, error) {
	if o.Type == "loadbalance" {
		if o.Outbounds == nil {
			o.Outbounds = []string{}
		}
		return json.Marshal(struct {
			Type             string        `json:"type"`
			Tag              string        `json:"tag"`
			Outbounds        []string      `json:"outbounds"`
			URL              string        `json:"url,omitempty"`
			Interval         string        `json:"interval,omitempty"`
			ExpectedStatus   string        `json:"expected_status,omitempty"`
			Bandwidth        bool          `json:"bandwidth,omitempty"`
			NodeScoreWeights *ScoreWeights `json:"node_score_weights,omitempty"`
		}{o.Type, o.Tag, o.Outbounds, o.URL, o.Interval, o.ExpectedStatus, o.Bandwidth, o.NodeScoreWeights})
	}
	type plain Outbound
	return json.Marshal(plain(o))
}

type Dropped struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type LaneState struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Icon    string   `json:"icon"`
	Index   int      `json:"index"`
	Members []string `json:"members"`
	Valid   []string `json:"valid"`
	Mode    string   `json:"mode"`
	Ref     *string  `json:"ref"`
	SubTag  *string  `json:"subTag"`
}

type FailoverRuntimeSettings struct {
	Interval         string  `json:"interval"`
	IntervalMs       float64 `json:"intervalMs"`
	Tolerance        float64 `json:"tolerance"`
	TestURL          string  `json:"testUrl"`
	ExpectedStatus   string  `json:"expectedStatus,omitempty"`
	TimeoutMs        int64   `json:"timeoutMs"`
	FailureThreshold int64   `json:"failureThreshold"`
	RestorePrimary   bool    `json:"restorePrimary"`
	RecoveryHoldMs   int64   `json:"recoveryHoldMs"`
}

type FailoverState struct {
	ID        string                  `json:"id"`
	Tag       string                  `json:"tag"`
	Lanes     []LaneState             `json:"lanes"`
	RejectTag string                  `json:"rejectTag"`
	Settings  FailoverRuntimeSettings `json:"settings"`
}

type EmitResult struct {
	Outbounds    []Outbound      `json:"outbounds"`
	Dropped      []Dropped       `json:"dropped"`
	Placeholders []string        `json:"placeholders"`
	Builtin      Builtin         `json:"builtin"`
	InternalTags []string        `json:"internalTags"`
	PublicTags   []string        `json:"publicTags"`
	Failover     []FailoverState `json:"failover"`
}

// idleTimeoutFor:sing-box 要求 interval ≤ idle_timeout,否则启动时 FATAL,而 `sing-box check` 查不出。
func idleTimeoutFor(idleTimeout, interval string) string {
	idle := idleTimeout
	if idle == "" {
		idle = DefaultIdleTimeout
	}
	idleMs, intervalMs := parseDuration(idle), parseDuration(interval)
	if intervalMs > 0 && idleMs > 0 && intervalMs > idleMs {
		return interval
	}
	return idle
}

func orStr(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// keywordsHit:and = 所有(非空)关键词都要命中(官方 v0.1.354);否则任一命中即可。
// and 模式下关键词全为空时视为命中(与原有行为一致);排除侧另见调用处:没有有效关键词不应排除任何节点。
func keywordsHit(text string, kws []string, mode string) bool {
	if mode == "and" {
		for _, kw := range kws {
			if jsx.Trim(normalizeForMatch(kw)) == "" {
				continue
			}
			if !keywordMatches(text, kw) {
				return false
			}
		}
		return true
	}
	for _, kw := range kws {
		if keywordMatches(text, kw) {
			return true
		}
	}
	return false
}

// hasKeyword:至少有一个非空关键词;排除侧全是空词时不该排除任何节点。
func hasKeyword(kws []string) bool {
	for _, kw := range kws {
		if jsx.Trim(normalizeForMatch(kw)) != "" {
			return true
		}
	}
	return false
}

// resolveMembers:动态组按关键词从 dynamicTags 里现挑(不带关键词 = 全部;链式代理的节点不在里面,
// 住宅线路不该悄悄混进自动择优池);静态组用显式成员,可以是节点、别的组、启用的内置直连 / 拒绝,
// 剔除重复 / 自引用 / 指向不存在的东西的条目。
func resolveMembers(g *Group, dynamicTags []string, nodeTagSet, memberNames map[string]bool, matchText map[string]string) []string {
	if g.Mode == "dynamic" {
		return dynamicMatch(g, dynamicTags, matchText)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, m := range g.Members {
		if seen[m] || m == g.Name {
			continue
		}
		if !nodeTagSet[m] && !memberNames[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// dynamicMatch:动态组按关键词(和排除关键词)从候选里挑。
func dynamicMatch(g *Group, cands []string, matchText map[string]string) []string {
	out := []string{}
	for _, tag := range cands {
		text, ok := matchText[tag]
		if !ok {
			text = normalizeForMatch(tag)
		}
		if len(g.Keywords) > 0 && !keywordsHit(text, g.Keywords, g.KeywordMode) {
			continue
		}
		// 排除关键词:or = 命中任一就移出,and = 所有(非空)关键词都命中才移出
		if hasKeyword(g.ExcludeKeywords) && keywordsHit(text, g.ExcludeKeywords, g.ExcludeKeywordMode) {
			continue
		}
		out = append(out, tag)
	}
	return out
}

// dropCycles:按依赖顺序逐个接纳组,只允许引用「已经被接纳的组」或真实节点;剩下的就是环,整组丢弃。
func dropCycles(groups []*Group) []*Group {
	allNames := map[string]bool{}
	for _, g := range groups {
		allNames[g.Name] = true
	}
	accepted := []*Group{}
	acceptedNames := map[string]bool{}
	pending := append([]*Group{}, groups...)
	progressed := true
	for progressed && len(pending) > 0 {
		progressed = false
		for i := 0; i < len(pending); i++ {
			g := pending[i]
			blocked := false
			if g.Mode != "dynamic" && g.Type != "failover" && g.Type != "loadbalance" { // 负载均衡只收真实节点,不依赖别的组
				for _, m := range g.Members {
					if m != g.Name && allNames[m] && !acceptedNames[m] {
						blocked = true
						break
					}
				}
			}
			if blocked {
				continue
			}
			accepted = append(accepted, g)
			acceptedNames[g.Name] = true
			pending = append(pending[:i], pending[i+1:]...)
			i--
			progressed = true
		}
	}
	return accepted
}

type nodeInfo struct {
	tag, regionName string
	chain           bool   // 链式代理节点
	subscriptionID  string // 节点所属订阅(链式代理的节点不看这项,范围里用 ChainProxyScope)
	detour          string // 链式代理节点的上游(线路 / 节点 / 组),环路检测用
}

// ChainProxyScope 是动态组「订阅范围」里代表链式代理的那一项(对齐上游 v0.1.476 的 chain-proxy)。
const ChainProxyScope = "chain-proxy"

func parseNodes(v any) ([]nodeInfo, error) {
	if !jsx.Truthy(v) {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("nodes 不是数组")
	}
	out := make([]nodeInfo, len(arr))
	for i, n := range arr {
		out[i].tag, _ = jsx.Get(n, "tag").(string)
		out[i].regionName, _ = jsx.Get(n, "regionName").(string)
		out[i].chain = jsx.Truthy(jsx.Get(n, "chain"))
		out[i].subscriptionID, _ = jsx.Get(n, "subscriptionId").(string)
		out[i].detour, _ = jsx.Get(n, "detour").(string)
	}
	return out, nil
}

type orderedSet struct {
	list []string
	set  map[string]bool
}

func newOrderedSet() *orderedSet { return &orderedSet{set: map[string]bool{}} }
func (s *orderedSet) add(v string) {
	if !s.set[v] {
		s.set[v] = true
		s.list = append(s.list, v)
	}
}

// EmitUserGroups 生成出站。options 里用 testUrl 和 expectedStatus(档案里的全局设置)。
func EmitUserGroups(groupsRaw any, nodesRaw any, options any) (*EmitResult, error) {
	testURL := DefaultTestURL
	if s, ok := jsx.Get(options, "testUrl").(string); ok && s != "" {
		testURL = s
	}
	// 可接受状态码:组自己的优先,没写用全局设置;都没写就不往配置里写这个字段
	expectedDefault, _ := NormalizeExpectedStatus(jsx.Get(options, "expectedStatus"))
	expectedOf := func(g *Group) string { return orStr(g.ExpectedStatus, expectedDefault) }
	// 客户端配置不做带宽测试(档案里 disableBandwidth = true):组上开着也不往出站里写
	bandwidthOf := func(g *Group) bool { return g.Bandwidth && !jsx.Truthy(jsx.Get(options, "disableBandwidth")) }
	// 节点评分权重(档案 nodeScoreWeights):只跟着带宽择优写,不合法就不写(内核用固定的 40 / 30 / 30)
	scoreWeights := ScoreWeightsOf(jsx.Get(options, "nodeScoreWeights"))
	weightsOf := func(g *Group) *ScoreWeights {
		if bandwidthOf(g) {
			return scoreWeights
		}
		return nil
	}
	normalized := NormalizeGroups(groupsRaw)
	nodes, err := parseNodes(nodesRaw)
	if err != nil {
		return nil, err
	}
	nodeTags := make([]string, len(nodes))
	dynamicTags := []string{}
	matchText := map[string]string{}
	nodeTagSet := map[string]bool{}
	scopeOf := map[string]string{} // 节点 → 订阅范围里的 id(链式代理统一是 ChainProxyScope)
	chainTag := map[string]bool{}
	for i, n := range nodes {
		nodeTags[i] = n.tag
		nodeTagSet[n.tag] = true
		if !n.chain {
			dynamicTags = append(dynamicTags, n.tag)
			scopeOf[n.tag] = n.subscriptionID
		} else {
			scopeOf[n.tag] = ChainProxyScope
			chainTag[n.tag] = true
		}
		matchText[n.tag] = normalizeForMatch(n.regionName + " " + n.tag)
	}

	builtin := builtinTags(normalized)
	active := []*Group{}
	for _, g := range normalized {
		if g.Enabled || g.Kind == "direct" {
			active = append(active, g)
		}
	}
	userActive := []*Group{}
	for _, g := range active {
		if g.Kind == "" {
			userActive = append(userActive, g)
		}
	}
	withoutCycles := dropCycles(userActive)
	kept := map[*Group]bool{}
	for _, g := range withoutCycles {
		kept[g] = true
	}
	dropped := []Dropped{}
	for _, g := range userActive {
		if !kept[g] {
			dropped = append(dropped, Dropped{g.Name, "cycle"})
		}
	}
	// 静态组的成员还能是内置的直连 / 拒绝(停用的不认:拒绝停用时配置里根本没有它,直连停用的意思是「选不到它」)
	memberNames := map[string]bool{}
	for _, g := range withoutCycles {
		memberNames[g.Name] = true
	}
	if builtin.DirectEnabled {
		memberNames[builtin.Direct] = true
	}
	if builtin.BlockEnabled {
		memberNames[builtin.Block] = true
	}

	// 动态组的成员:没有订阅范围 = 所有非链式节点按关键词挑(和以前一样);有范围 = 只在范围内订阅的节点里挑,
	// 范围含 ChainProxyScope 时链式代理的节点也是候选。链式节点的上游线路绕回这个组(会成环)就不收。
	// 边:链式节点 → 它的上游,静态组 / 故障转移 → 成员,动态组 → 已挑出的成员;按依赖顺序挨个挑,后面的组能看到前面的边。
	edges := map[string][]string{}
	for _, n := range nodes {
		if n.chain && n.detour != "" {
			edges[n.tag] = []string{n.detour}
		}
	}
	for _, g := range withoutCycles {
		switch {
		case g.Mode == "dynamic":
		case g.Type == "failover":
			for _, lane := range g.Lanes {
				edges[g.Name] = append(edges[g.Name], lane.Members...)
			}
		default:
			edges[g.Name] = g.Members
		}
	}
	reaches := func(from, target string) bool {
		seen := map[string]bool{}
		var walk func(cur string) bool
		walk = func(cur string) bool {
			for _, next := range edges[cur] {
				if next == target {
					return true
				}
				if seen[next] {
					continue
				}
				seen[next] = true
				if walk(next) {
					return true
				}
			}
			return false
		}
		return walk(from)
	}
	dynMembers := map[*Group][]string{}
	for _, g := range withoutCycles {
		if g.Mode != "dynamic" {
			continue
		}
		cands := dynamicTags
		if len(g.SubscriptionIDs) > 0 {
			inScope := map[string]bool{}
			for _, id := range g.SubscriptionIDs {
				inScope[id] = true
			}
			cands = []string{}
			for _, t := range nodeTags {
				if !inScope[scopeOf[t]] || (chainTag[t] && reaches(t, g.Name)) {
					continue
				}
				cands = append(cands, t)
			}
		}
		dynMembers[g] = dynamicMatch(g, cands, matchText)
		edges[g.Name] = dynMembers[g]
	}

	outbounds := []Outbound{}
	placeholders := []string{}
	internal := newOrderedSet()
	failover := []FailoverState{}
	usedTags := map[string]bool{}
	for _, t := range nodeTags {
		usedTags[t] = true
	}
	for _, g := range normalized {
		usedTags[g.Name] = true
	}
	rejectTag := FailoverRejectTag
	if builtin.BlockEnabled {
		rejectTag = builtin.Block
	}

	emitFailover := func(g *Group) {
		lanes := []LaneState{}
		refs := []string{}
		for index, lane := range g.Lanes {
			valid := []string{}
			for _, m := range lane.Members {
				if nodeTagSet[m] {
					valid = append(valid, m)
				}
			}
			st := LaneState{ID: lane.ID, Name: lane.Name, Icon: lane.Icon, Index: index, Members: lane.Members, Valid: valid}
			switch len(valid) {
			case 0:
				st.Mode = "empty"
			case 1:
				ref := valid[0]
				st.Mode, st.Ref = "single", &ref
				refs = append(refs, ref)
			default:
				subTag := FailoverPrefix + g.ID + ":" + lane.ID
				for usedTags[subTag] {
					subTag += "~"
				}
				usedTags[subTag] = true
				internal.add(subTag)
				// 手动选择的页签:内部子组是 selector,默认第一个节点,之后按用户在代理页点的
				if lane.Manual {
					outbounds = append(outbounds, Outbound{Type: "selector", Tag: subTag, Outbounds: valid, Default: valid[0]})
					st.Mode, st.Ref, st.SubTag = "selector", &subTag, &subTag
					refs = append(refs, subTag)
					lanes = append(lanes, st)
					continue
				}
				interval := orStr(g.Interval, "300s")
				tol := g.Tolerance
				outbounds = append(outbounds, Outbound{
					Type: "urltest", Tag: subTag, Outbounds: valid,
					URL: orStr(g.TestURL, testURL), Interval: interval, Tolerance: &tol,
					IdleTimeout: idleTimeoutFor(DefaultIdleTimeout, interval), ExpectedStatus: expectedOf(g),
					Bandwidth: bandwidthOf(g), NodeScoreWeights: weightsOf(g),
				})
				st.Mode, st.Ref, st.SubTag = "urltest", &subTag, &subTag
				refs = append(refs, subTag)
			}
			lanes = append(lanes, st)
		}
		seen := map[string]bool{}
		memberTags := []string{}
		for _, r := range refs {
			if !seen[r] {
				seen[r] = true
				memberTags = append(memberTags, r)
			}
		}
		if rejectTag == FailoverRejectTag {
			has := false
			for _, o := range outbounds {
				if o.Tag == FailoverRejectTag {
					has = true
				}
			}
			if !has {
				outbounds = append(outbounds, Outbound{Type: "block", Tag: FailoverRejectTag})
				internal.add(FailoverRejectTag)
			}
		}
		def := rejectTag
		if len(memberTags) > 0 {
			def = memberTags[0]
		}
		outbounds = append(outbounds, Outbound{
			Type: "selector", Tag: g.Name, Outbounds: append(append([]string{}, memberTags...), rejectTag),
			Default: def, InterruptExistConnections: true,
		})
		interval := orStr(g.Interval, "300s")
		fo := g.Failover
		failover = append(failover, FailoverState{
			ID: g.ID, Tag: g.Name, Lanes: lanes, RejectTag: rejectTag,
			Settings: FailoverRuntimeSettings{
				Interval: interval, IntervalMs: parseDuration(interval), Tolerance: g.Tolerance, TestURL: orStr(g.TestURL, testURL), ExpectedStatus: expectedOf(g),
				TimeoutMs: fo.TimeoutMs, FailureThreshold: fo.FailureThreshold, RestorePrimary: fo.RestorePrimary, RecoveryHoldMs: fo.RecoveryHoldMs,
			},
		})
	}

	for _, g := range active {
		switch {
		case g.Kind == "direct":
			outbounds = append(outbounds, Outbound{Type: "direct", Tag: g.Name})
			continue
		case g.Kind == "block":
			outbounds = append(outbounds, Outbound{Type: "block", Tag: g.Name})
			continue
		case !kept[g]:
			continue
		case g.Type == "failover":
			emitFailover(g)
			continue
		}
		var members []string
		if g.Mode == "dynamic" {
			members = dynMembers[g]
		} else {
			members = resolveMembers(g, dynamicTags, nodeTagSet, memberNames, matchText)
		}
		if g.Type == "loadbalance" {
			// 负载均衡只接真实节点:引用的组、不存在的名字一律不写进出站
			nodesOnly := make([]string, 0, len(members))
			for _, m := range members {
				if nodeTagSet[m] {
					nodesOnly = append(nodesOnly, m)
				}
			}
			members = nodesOnly
			if len(members) > LoadBalanceMaxNodes {
				return nil, fmt.Errorf("load balance supports at most %d nodes", LoadBalanceMaxNodes)
			}
		}
		if len(members) == 0 && g.Type != "loadbalance" { // 负载均衡与上游一致:空成员原样写出,不挂占位
			// 空组不能原样写进配置(内核会 FATAL),也不该整个丢掉:挂一个直连占位
			members = []string{builtin.Direct}
			placeholders = append(placeholders, g.Name)
		}
		if g.Type == "urltest" {
			interval := orStr(g.Interval, DefaultInterval)
			tol := g.Tolerance
			outbounds = append(outbounds, Outbound{
				Type: "urltest", Tag: g.Name, Outbounds: members, URL: orStr(g.TestURL, testURL),
				Interval: interval, Tolerance: &tol, IdleTimeout: idleTimeoutFor(g.IdleTimeout, interval), ExpectedStatus: expectedOf(g),
				Bandwidth: bandwidthOf(g), NodeScoreWeights: weightsOf(g),
			})
		} else if g.Type == "loadbalance" {
			outbounds = append(outbounds, Outbound{
				Type: "loadbalance", Tag: g.Name, Outbounds: members, URL: orStr(g.TestURL, testURL),
				Interval: orStr(g.Interval, DefaultLoadBalanceInterval), ExpectedStatus: expectedOf(g),
				Bandwidth: bandwidthOf(g), NodeScoreWeights: weightsOf(g),
			})
		} else {
			outbounds = append(outbounds, Outbound{Type: "selector", Tag: g.Name, Outbounds: members})
		}
	}

	publicTags := []string{}
	for _, o := range outbounds {
		if !internal.set[o.Tag] {
			publicTags = append(publicTags, o.Tag)
		}
	}
	if internal.list == nil {
		internal.list = []string{}
	}
	return &EmitResult{
		Outbounds: outbounds, Dropped: dropped, Placeholders: placeholders, Builtin: builtin,
		InternalTags: internal.list, PublicTags: publicTags, Failover: failover,
	}, nil
}
