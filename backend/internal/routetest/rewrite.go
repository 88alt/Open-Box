package routetest

import (
	"context"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
	"github.com/timeflysoon/Open-Box/backend/internal/dnsrewrite"
)

// matchRewrite 把命中的规则转成响应里的 rewrite(规则的匹配语义见 dnsrewrite.Match)。
func matchRewrite(rules []dnsrewrite.Rule, domain string) *dnsdecide.RewriteHit {
	r := dnsrewrite.Match(rules, domain)
	if r == nil {
		return nil
	}
	addrs := r.Addresses
	if addrs == nil {
		addrs = []string{}
	}
	return &dnsdecide.RewriteHit{Source: r.Source, Domain: r.Domain, Addresses: addrs}
}

// annotateRewrite 在预测命中重写服务器时补上是哪条规则。档案读不到、或档案里已经没有这条规则(配置之后又改了)时不补。
func (p *DNSPredictor) annotateRewrite(ctx context.Context, d *dnsdecide.Decision, domain string) {
	if p.Rewrites == nil || d.Server == nil || d.Server.Tag != dnsrewrite.Tag {
		return
	}
	rules, err := p.Rewrites(ctx)
	if err != nil {
		return
	}
	d.Rewrite = matchRewrite(rules, domain)
}
