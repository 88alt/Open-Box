package routetest

import (
	"context"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
)

// RewriteTag 是配置里 DNS 重写服务器的 tag(core 生成,127.0.0.1:7854)。
const RewriteTag = "dns-rewrite"

// RewriteRule 是档案 dns.rewrite.rules 里的一条(已启用的才会生成进配置)。
type RewriteRule struct {
	Source    string
	Domain    string
	Addresses []string
}

// RewriteRulesFromProfile 从档案里取已启用的重写规则。整理方式和 core 生成配置时一致:源域名转小写、去末尾的点,
// 没有源、或域名和地址都没有的丢掉;总开关 enabled 为 false 时一条都不要;域名型和地址型都填了按域名型算。
func RewriteRulesFromProfile(profile map[string]any) []RewriteRule {
	dns, _ := profile["dns"].(map[string]any)
	rw, _ := dns["rewrite"].(map[string]any)
	if on, ok := rw["enabled"].(bool); ok && !on {
		return nil
	}
	raw, _ := rw["rules"].([]any)
	var out []RewriteRule
	for _, x := range raw {
		m, _ := x.(map[string]any)
		if on, ok := m["enabled"].(bool); ok && !on {
			continue
		}
		src := normHost(m["source"])
		dom := normHost(m["domain"])
		var addrs []string
		if dom == "" {
			if l, ok := m["addresses"].([]any); ok {
				for _, a := range l {
					if s, ok := a.(string); ok && strings.TrimSpace(s) != "" {
						addrs = append(addrs, strings.ToLower(strings.TrimSpace(s)))
					}
				}
			}
		}
		if src == "" || (dom == "" && len(addrs) == 0) {
			continue
		}
		out = append(out, RewriteRule{Source: src, Domain: dom, Addresses: addrs})
	}
	return out
}

func normHost(v any) string {
	s, _ := v.(string)
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), ".")
}

// matchRewrite 找 domain 命中的重写规则:精确的源优先,其次泛域名(*.x 只匹配 x 的子域,和配置里生成的 .x 后缀规则一致),
// 泛域名取最长的(最具体的)。内核只负责把查询交给重写服务器,服务器里谁先谁后不是配置能看出来的,这是本实现的决定。
func matchRewrite(rules []RewriteRule, domain string) *dnsdecide.RewriteHit {
	domain = strings.TrimRight(strings.ToLower(domain), ".")
	var best *RewriteRule
	for i := range rules {
		r := &rules[i]
		if r.Source == domain {
			best = r
			break
		}
		if strings.HasPrefix(r.Source, "*.") && dnsdecide.SuffixMatch(r.Source[1:], domain) &&
			(best == nil || len(r.Source) > len(best.Source)) {
			best = r
		}
	}
	if best == nil {
		return nil
	}
	addrs := best.Addresses
	if addrs == nil {
		addrs = []string{}
	}
	return &dnsdecide.RewriteHit{Source: best.Source, Domain: best.Domain, Addresses: addrs}
}

// annotateRewrite 在预测命中重写服务器时补上是哪条规则。档案读不到、或档案里已经没有这条规则(配置之后又改了)时不补。
func (p *DNSPredictor) annotateRewrite(ctx context.Context, d *dnsdecide.Decision, domain string) {
	if p.Rewrites == nil || d.Server == nil || d.Server.Tag != RewriteTag {
		return
	}
	rules, err := p.Rewrites(ctx)
	if err != nil {
		return
	}
	d.Rewrite = matchRewrite(rules, domain)
}
