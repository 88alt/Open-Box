// Package penetration 实现 POST /api/openbox/penetration 的逻辑:给一个域名或 IP,
// 按已部署配置里的路由规则(见 routedecide)预测会命中哪条规则、去哪个出站,再顺着内核当前选中的节点一路走到叶子。
// 响应形状取自前端 api/openbox.ts 的 OpenboxPenetrationResult,自己的设计,没有参照上游。
//
// 这一版做了:matched(规则下标和内容、出站 / 动作)、owner(内置规则的类别、站点集)、assumed(带 leaf / sameOutcome)、
// preResolve、dns(复用真实路由测试的 DNS 判定)、chain / finalOutbound / chainError、matchError、routingStale。
// 还没做:entries / entriesTotal(命中的具体条目)、viaIp 和 resolved(域名解析出的地址落进 IP 规则)、
// preMatch、firstLayer(这些要内核侧的部署元数据),前置自定义分流的 ownerName(要知道它的名字);见 STATUS.md。
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

// Service 的依赖都是函数 / 接口,测试里换假的。
type Service struct {
	// Config 读已部署的配置;规则集的解码缓存跟着 Sets 走。
	Config func() (map[string]any, error)
	// Sets 按配置里的规则集定义回答「规则集是否包含」。
	Sets func(cfg map[string]any) routedecide.Sets
	DNS  DNSPredictor // 可为空
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
}

type Owner struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type Result struct {
	Matched       *Matched                 `json:"matched"`
	Owner         *Owner                   `json:"owner,omitempty"`
	Assumed       []routedecide.Assumption `json:"assumed,omitempty"`
	PreResolve    bool                     `json:"preResolve,omitempty"`
	DNS           any                      `json:"dns,omitempty"`
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
	d := routedecide.Decide(ctx, v, s.Sets(cfg), t)

	res := &Result{Chain: []string{}, Assumed: d.Assumed, PreResolve: d.PreResolve}
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
