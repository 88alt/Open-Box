// Package penetration 实现 POST /api/openbox/penetration 的逻辑:给一个域名或 IP,
// 按已部署配置里的路由规则(见 routedecide)预测会命中哪条规则、去哪个出站,再顺着内核当前选中的节点一路走到叶子。
// 响应形状取自前端 api/openbox.ts 的 OpenboxPenetrationResult,自己的设计,没有参照上游。
//
// 这一版做了:matched(规则下标和内容、出站 / 动作)、owner(内置规则的类别、站点集)、assumed(带 leaf / sameOutcome)、
// preResolve、dns(复用真实路由测试的 DNS 判定)、chain / finalOutbound / chainError、matchError、routingStale。
// entries / entriesTotal(命中的具体条目)、resolved(域名经内核 DNS 解析的结果)、viaIp(规则是按解析出的地址命中的)也做了。
// 域名目标:终端自己解析后按真实地址连进内核、内核再嗅探出域名,所以连接同时带域名和目的 IP;推算按「第一个真实 IPv4(没有就第一个 IPv6)」
// 当目的 IP,去比不带「连接没有域名」前提的 IP 规则(私网直连、节点服务器直连、内核占位段等)。解析到的是占位地址(fake-ip)、
// 解析失败时没有目的 IP,IP 条件仍记成前提。
// 还没做:preMatch、firstLayer(这些要内核侧的部署元数据),前置自定义分流的 ownerName(要知道它的名字);见 STATUS.md。
package penetration

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
	"github.com/timeflysoon/Open-Box/backend/internal/routedecide"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// InputError 是用户输入的问题(接口回 400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

var targetPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// DNSPredictor 预测域名的 DNS 走向(*routetest.DNSPredictor 满足)。
type DNSPredictor interface {
	Predict(ctx context.Context, domain string) (*dnsdecide.Decision, error)
}

// Resolver 向内核的 DNS 入站解析一个域名(*routetest.Service 满足):返回答案里有没有占位地址(fake-ip)、
// 所有 A / AAAA 地址;没有任何地址时 errText 是原因。
type Resolver interface {
	ResolveName(ctx context.Context, name string) (fakeIP bool, addrs []string, errText string)
}

// Service 的依赖都是函数 / 接口,测试里换假的。
type Service struct {
	// Config 读已部署的配置;规则集的解码缓存跟着 Sets 走。
	Config func() (map[string]any, error)
	// Sets 按配置里的规则集定义回答「规则集是否包含」。
	Sets func(cfg map[string]any) routedecide.Sets
	DNS  DNSPredictor // 可为空
	// Resolver 解析域名目标,给 resolved 字段和目的 IP 用;nil = 不解析(没有 resolved,IP 条件记成前提)。
	Resolver Resolver
	// Proxies 读内核的代理列表;nil 或返回 error 时线路写 chainError。
	Proxies func(ctx context.Context) ([]singbox.KernelProxy, error)
	// Stale 报告「内核还在跑旧的分流配置」;nil = 否。
	Stale func(ctx context.Context) bool
}

type Matched struct {
	Index    int            `json:"index"`
	Rule     map[string]any `json:"rule"`
	Outbound string         `json:"outbound,omitempty"`
	Action   string         `json:"action,omitempty"`
	// ViaIP:这条规则是按目的 IP 命中的(域名解析出来的地址落进了规则里的 IP 条件),就是那个地址。
	ViaIP string `json:"viaIp,omitempty"`
	// Entries 是具体命中的条目(最多 maxEntries 条),EntriesTotal 是命中的总数;Source 是规则集名,规则自己写的条件是 "custom"。
	Entries      []Entry `json:"entries,omitempty"`
	EntriesTotal int     `json:"entriesTotal,omitempty"`
}

// Entry 对应前端 matched.entries 的一项。
type Entry struct {
	Type   string `json:"type"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

const maxEntries = 20

// EntrySets 是可选能力:规则集能说出具体命中了哪些条目(*dnsdecide.Bound 满足);Sets 不满足时就不给 entries。
type EntrySets interface {
	Entries(ctx context.Context, tag, domain string, ip netip.Addr) ([]dnsdecide.Entry, error)
}

type Owner struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Resolved 对应前端 resolved:推算时向内核 DNS 解析这个域名的结果。
type Resolved struct {
	FakeIP    bool     `json:"fakeIp,omitempty"` // 内核返回的是占位地址(连接进内核后按域名找回目标)
	Addresses []string `json:"addresses"`
	Error     string   `json:"error,omitempty"`
}

type Result struct {
	Matched       *Matched                 `json:"matched"`
	Owner         *Owner                   `json:"owner,omitempty"`
	Assumed       []routedecide.Assumption `json:"assumed,omitempty"`
	PreResolve    bool                     `json:"preResolve,omitempty"`
	DNS           any                      `json:"dns,omitempty"`
	Resolved      *Resolved                `json:"resolved,omitempty"`
	Chain         []string                 `json:"chain"`
	FinalOutbound *string                  `json:"finalOutbound"`
	ChainError    string                   `json:"chainError,omitempty"`
	MatchError    string                   `json:"matchError,omitempty"`
	RoutingStale  bool                     `json:"routingStale,omitempty"`
}

// ParseTarget 校验并规范化目标:IP 字面量,或域名(小写、去掉末尾的点)。
func ParseTarget(raw string) (routedecide.Target, error) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return routedecide.Target{}, &InputError{"请填写要查询的域名或 IP"}
	}
	if len(t) > 253 || strings.HasPrefix(t, "-") || !targetPattern.MatchString(t) {
		return routedecide.Target{}, &InputError{"目标只能是域名或 IP(字母、数字、点、冒号、短横线)"}
	}
	if a, err := netip.ParseAddr(t); err == nil {
		return routedecide.Target{IP: a.Unmap()}, nil
	}
	if strings.Contains(t, ":") {
		return routedecide.Target{}, &InputError{"不是合法的 IP 地址"}
	}
	t = strings.TrimSuffix(strings.ToLower(t), ".")
	for _, label := range strings.Split(t, ".") {
		if label == "" || len(label) > 63 {
			return routedecide.Target{}, &InputError{"不是合法的域名"}
		}
	}
	return routedecide.Target{Domain: t}, nil
}

// Query 做一次推算。读不到配置返回 error(接口回 500 / 提示还没部署);其余问题写进响应字段。
func (s *Service) Query(ctx context.Context, raw string) (*Result, error) {
	t, err := ParseTarget(raw)
	if err != nil {
		return nil, err
	}
	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	v, err := routedecide.FromConfig(cfg)
	if err != nil {
		return nil, err
	}
	sets := s.Sets(cfg)
	var resolved *Resolved
	if !t.IsIP() && s.Resolver != nil {
		resolved = s.resolve(ctx, t.Domain)
		if !resolved.FakeIP {
			t.DestIP = pickDest(resolved.Addresses)
		}
	}
	d := routedecide.Decide(ctx, v, sets, t)

	res := &Result{Chain: []string{}, Assumed: d.Assumed, PreResolve: d.PreResolve, Resolved: resolved}
	if s.Stale != nil {
		res.RoutingStale = s.Stale(ctx)
	}
	switch {
	case t.IsIP():
		res.DNS = map[string]any{"skipped": true}
	case s.DNS == nil:
		res.DNS = map[string]any{"error": "DNS 判定没有接入"}
	default:
		if dd, err := s.DNS.Predict(ctx, t.Domain); err != nil {
			res.DNS = map[string]any{"error": fmt.Sprintf("DNS 判定失败: %v", err)}
		} else {
			res.DNS = dd
		}
	}
	if d.MatchError != "" {
		// 没能检查:不给「没命中」的结论,也不往下走线路
		res.MatchError = d.MatchError
		res.Assumed = nil
		return res, nil
	}

	if d.Index != nil {
		res.Matched = &Matched{Index: *d.Index, Rule: d.Rule, Outbound: d.Outbound, Action: d.Action}
		et := t
		if d.ViaIP.IsValid() {
			res.Matched.ViaIP = d.ViaIP.String()
			et.IP = d.ViaIP // 靠地址命中的:条目里把 IP 条目也列出来
		}
		if es, ok := sets.(EntrySets); ok {
			all := collectEntries(ctx, es, d.Rule, et)
			res.Matched.EntriesTotal = len(all)
			if len(all) > maxEntries {
				all = all[:maxEntries]
			}
			res.Matched.Entries = all
		}
	}
	res.Owner = ownerOf(d, v)

	// 线路:从命中的出站(没命中是 final)出发,顺着内核当前选中的节点走到叶子;reject 没有线路
	if d.Action == "reject" || d.Outbound == "" {
		return res, nil
	}
	now, known, perr := s.proxyState(ctx)
	if perr != nil {
		res.Chain = []string{d.Outbound}
		res.ChainError = "读不到内核的代理列表,线路只到这一层: " + perr.Error()
		return res, nil
	}
	res.Chain = drill(d.Outbound, now, known)
	leaf := res.Chain[len(res.Chain)-1]
	res.FinalOutbound = &leaf
	for i := range res.Assumed {
		a := &res.Assumed[i]
		if a.Outbound == "" {
			continue
		}
		l := drill(a.Outbound, now, known)
		a.Leaf = l[len(l)-1]
		same := a.Leaf == leaf
		a.SameOutcome = &same
	}
	return res, nil
}

// resolve 经内核 DNS 解析域名。解析失败不算接口错误,写在 error 里;地址总是个数组(没有时为空)。
func (s *Service) resolve(ctx context.Context, name string) *Resolved {
	fake, addrs, errText := s.Resolver.ResolveName(ctx, name)
	if addrs == nil {
		addrs = []string{}
	}
	return &Resolved{FakeIP: fake, Addresses: addrs, Error: errText}
}

// pickDest 选终端会连的那个地址:第一个 IPv4,没有就第一个 IPv6;解析不出来的条目跳过;一个都没有返回无效地址。
func pickDest(addrs []string) netip.Addr {
	var v6 netip.Addr
	for _, s := range addrs {
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		a = a.Unmap()
		if a.Is4() {
			return a
		}
		if !v6.IsValid() {
			v6 = a
		}
	}
	return v6
}

func (s *Service) proxyState(ctx context.Context) (now map[string]string, known map[string]bool, err error) {
	if s.Proxies == nil {
		return nil, nil, fmt.Errorf("没有接入内核")
	}
	ps, err := s.Proxies(ctx)
	if err != nil {
		return nil, nil, err
	}
	now, known = make(map[string]string, len(ps)), make(map[string]bool, len(ps))
	for _, p := range ps {
		now[p.Name], known[p.Name] = p.Now, true
	}
	return now, known, nil
}

const maxChain = 8

// drill 从 name 出发沿着「当前选中」往下走,最多 maxChain 层(防环)。选中的是内核代理列表里没有的叶子(内置直连等)也记进去。
func drill(name string, now map[string]string, known map[string]bool) []string {
	chain := []string{name}
	for i := 0; i < maxChain && known[name]; i++ {
		next := now[name]
		if next == "" {
			break
		}
		chain = append(chain, next)
		name = next
	}
	return chain
}

// ownerOf 判断命中的规则属于谁:内置规则按它的形状认(私网、NTP、节点站点、终端、内核占位段),
// 出站是站点集分组的算站点集;认不出的(前置自定义分流等)不给,不猜。没有规则命中走 final = 内置的「其他」。
func ownerOf(d *routedecide.Decision, v *routedecide.View) *Owner {
	if d.Index == nil {
		return &Owner{Kind: "builtin", Name: "other"}
	}
	r := d.Rule
	switch {
	case r["ip_is_private"] == true:
		return &Owner{Kind: "builtin", Name: "private"}
	case hasKey(r, "port") && r["network"] == "udp" && d.Outbound != "" && !hasKey(r, "rule_set") && !hasKey(r, "domain_suffix"):
		return &Owner{Kind: "builtin", Name: "ntp"}
	case usesNodeSet(r):
		return &Owner{Kind: "builtin", Name: "nodes"}
	case hasKey(r, "source_ip_cidr"):
		return &Owner{Kind: "builtin", Name: "clients"}
	case d.Action == "reject" && hasKey(r, "ip_cidr"):
		return &Owner{Kind: "builtin", Name: "kernel"}
	}
	switch v.Types[d.Outbound] {
	case "selector", "urltest":
		if hasKey(r, "rule_set") || hasKey(r, "domain_suffix") || hasKey(r, "domain") {
			return &Owner{Kind: "policy", Name: d.Outbound}
		}
	}
	return nil
}

func hasKey(r map[string]any, k string) bool {
	if _, ok := r[k]; ok {
		return true
	}
	subs, _ := r["rules"].([]any)
	for _, x := range subs {
		if m, ok := x.(map[string]any); ok && hasKey(m, k) {
			return true
		}
	}
	return false
}

func usesNodeSet(r map[string]any) bool {
	var tags []string
	switch x := r["rule_set"].(type) {
	case string:
		tags = []string{x}
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				tags = append(tags, s)
			}
		}
	}
	for _, t := range tags {
		if strings.HasPrefix(t, "obnode-") {
			return true
		}
	}
	return false
}

// collectEntries 收集命中的规则里具体命中了哪些条目:规则自己写的条件(来源 custom)在前,再按规则集在规则里出现的顺序展开。
// 逻辑规则只看里面没有取反的子规则;规则集读不出来就跳过(命中与否已经由推算判过了,这里只是补充说明)。同一条目只记一次。
func collectEntries(ctx context.Context, es EntrySets, rule map[string]any, t routedecide.Target) []Entry {
	var out []Entry
	seen := map[Entry]bool{}
	add := func(e Entry) {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	var tags []string
	var walk func(r map[string]any)
	walk = func(r map[string]any) {
		if inv, _ := r["invert"].(bool); inv {
			return
		}
		for _, e := range dnsdecide.RuleEntries(r, t.Domain, t.IP) {
			add(Entry{Type: e.Type, Value: e.Value, Source: "custom"})
		}
		switch x := r["rule_set"].(type) {
		case string:
			tags = append(tags, x)
		case []any:
			for _, v := range x {
				if s, ok := v.(string); ok {
					tags = append(tags, s)
				}
			}
		}
		subs, _ := r["rules"].([]any)
		for _, sub := range subs {
			if m, ok := sub.(map[string]any); ok {
				walk(m)
			}
		}
	}
	walk(rule)
	for _, tag := range tags {
		es2, err := es.Entries(ctx, tag, t.Domain, t.IP)
		if err != nil {
			continue
		}
		for _, e := range es2 {
			add(Entry{Type: e.Type, Value: e.Value, Source: tag})
		}
	}
	return out
}
