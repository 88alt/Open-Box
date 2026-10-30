// Package dnsrewrite 是 DNS 重写服务:core 生成的配置把命中重写规则的域名交给 dns-rewrite 服务器(127.0.0.1:7854),
// 这个包就是那台服务器——把一个域名的答案改成别的域名(动态解析)或固定的 IPv4 / IPv6。
// 自己的设计,依据前端 OpenboxDnsRewriteRule 的注释(「源域名(精确或 *.泛域名)→ 目标域名(动态解析)或固定地址」)
// 和 core 生成的配置形状,没有参照上游;只用标准库,DNS 报文自己拼。
package dnsrewrite

import (
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
)

// Tag 是配置里重写服务器的 tag,DefaultAddr 是它监听的地址(都和 core 生成的一致)。
const (
	Tag         = "dns-rewrite"
	DefaultAddr = "127.0.0.1:7854"
)

// Rule 是档案 dns.rewrite.rules 里的一条(已启用的才会生成进配置)。Domain 和 Addresses 二选一。
type Rule struct {
	Source    string
	Domain    string
	Addresses []string
}

// RulesFromProfile 从档案里取已启用的重写规则。整理方式和 core 生成配置时一致:源域名转小写、去末尾的点,
// 没有源、或域名和地址都没有的丢掉;总开关 enabled 为 false 时一条都不要;域名型和地址型都填了按域名型算。
func RulesFromProfile(profile map[string]any) []Rule {
	dns, _ := profile["dns"].(map[string]any)
	rw, _ := dns["rewrite"].(map[string]any)
	if on, ok := rw["enabled"].(bool); ok && !on {
		return nil
	}
	raw, _ := rw["rules"].([]any)
	var out []Rule
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
		out = append(out, Rule{Source: src, Domain: dom, Addresses: addrs})
	}
	return out
}

func normHost(v any) string {
	s, _ := v.(string)
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), ".")
}

// Match 找 domain 命中的重写规则:精确的源优先,其次泛域名(*.x 只匹配 x 的子域,和配置里生成的 .x 后缀规则一致),
// 泛域名取最长的(最具体的)。内核只负责把查询交给重写服务器,服务器里谁先谁后不是配置能看出来的,这是本实现的决定。
func Match(rules []Rule, domain string) *Rule {
	domain = strings.TrimRight(strings.ToLower(domain), ".")
	var best *Rule
	for i := range rules {
		r := &rules[i]
		if r.Source == domain {
			return r
		}
		if strings.HasPrefix(r.Source, "*.") && dnsdecide.SuffixMatch(r.Source[1:], domain) &&
			(best == nil || len(r.Source) > len(best.Source)) {
			best = r
		}
	}
	return best
}
