package routetest

import (
	"context"
	"regexp"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// dns.stale:已部署的 DNS 规则已经过时。
//
// 站点集走直连还是走代理,在配置里是一对「包着热切换开关的代理支 + 紧跟其后的直连支」规则(见 core 的 flip.go)。
// 用户在面板上把某个站点集翻面时,档案先变、配置在后台重新生成;这段时间里 route-test 读到的仍是旧配置,
// 预测出来的方向就和站点集此刻的选中项对不上。这种情况前端要提示「DNS 规则是旧的,面板正在后台重新生成配置」,
// 而不是让用户以为线路坏了。
//
// 判法照上游(`panel/server/index.mjs` 的 `POST /route-test` 里 `g.dns.stale` 那段):
//  1. 命中的 DNS 规则先脱掉动作,只看它的 rule_set / domain_suffix 这一组条件;
//  2. 按这组条件去档案启用中的站点集里找主人(规则集名单或域名后缀完全一致),找不到就是兜底站点;
//  3. 主人此刻在哪一侧:读内核的代理列表,从它出发沿「当前选中」走到末尾,末尾是 direct 出站就是 direct,否则 proxy;
//  4. 命中的那台 DNS 服务器在哪一侧:`dns-proxy` / `dns-policy-N` / `dns-custom-N` / `dns-client-N`(含 `__altN`)是代理侧,其余是直连侧;
//  5. 两边不一致,就把第 3 步的方向写进 stale。
//
// 与上游的差异(推断点):上游按 profile 的 `activePolicies.rulesets / domainSuffix` 做 2,
// 我们同样调 core 的 normalizeRouting 拿这份名单;上游用 core 的 builtinTags 比「直连组」,我们改看末尾出站的类型
// (direct)。末尾出站不在内核代理列表里(读不到 / 不是内置直连)时不判断,不猜。

// Policy 是一个启用中的站点集:名字 + 判断 DNS 规则归属用的条件(取自 core normalizeRouting 的 activePolicies)。
type Policy struct {
	Name         string
	Rulesets     []string
	DomainSuffix []string
}

// 代理侧的 DNS 解析器 tag(与 core 生成的一致);带 __altN 的是它的备用上游。
var proxyDNSTag = regexp.MustCompile(`^dns-(proxy|policy-\d+|custom-\d+|client-\d+)(__alt\d+)?$`)

// flipsPrefix 是热切换开关用的规则集前缀:它们只表示「这一支现在开不开」,不表示站点集。
const flipsPrefix = "obflip-"

// annotateStale 给命中 DNS 服务器的预测补 stale(见文件头)。算不出来就不补。
func (p *DNSPredictor) annotateStale(ctx context.Context, cfg map[string]any, d *dnsdecide.Decision, proxies []singbox.KernelProxy) {
	if d == nil || d.Server == nil || p.Policies == nil {
		return
	}
	policies, fallback, err := p.Policies(ctx)
	if err != nil {
		return
	}
	owner := fallback
	if d.RuleIndex != nil {
		if rule := dnsRuleAt(cfg, *d.RuleIndex); rule != nil {
			if name, ok := policyOf(rule, policies); ok {
				owner = name
			}
		}
	}
	if owner == "" {
		return
	}
	now, ok := chainEnd(proxies, owner)
	if !ok {
		return
	}
	deployed := "direct"
	if proxyDNSTag.MatchString(d.Server.Tag) {
		deployed = "proxy"
	}
	if now != deployed {
		d.Stale = now
	}
}

// dnsRuleAt 取已部署配置里第 i 条 DNS 规则。
func dnsRuleAt(cfg map[string]any, i int) map[string]any {
	dns, _ := cfg["dns"].(map[string]any)
	rules, _ := dns["rules"].([]any)
	if i < 0 || i >= len(rules) {
		return nil
	}
	rule, _ := rules[i].(map[string]any)
	return rule
}

// policyOf 找这条 DNS 规则属于哪个站点集:先按规则集名单,再按域名后缀;都不中就说不属于任何站点集。
func policyOf(rule map[string]any, policies []Policy) (string, bool) {
	sets, suffixes := ruleCondition(rule)
	if len(sets) == 0 && len(suffixes) == 0 {
		return "", false
	}
	for _, p := range policies {
		if len(sets) > 0 && sameList(p.Rulesets, sets) {
			return p.Name, true
		}
	}
	for _, p := range policies {
		if len(suffixes) > 0 && sameList(p.DomainSuffix, suffixes) {
			return p.Name, true
		}
	}
	return "", false
}

// ruleCondition 取规则里表示站点集的那组条件。逻辑规则(挂热切换开关的那种)看它里面不带开关的那一支;
// 只看规则集 / 域名后缀,别的条件不参与归属判断。
func ruleCondition(rule map[string]any) (sets, suffixes []string) {
	if subs, ok := rule["rules"].([]any); ok && rule["type"] == "logical" {
		for _, s := range subs {
			m, _ := s.(map[string]any)
			if m == nil {
				continue
			}
			sets, suffixes = ownSets(m), ownSuffixes(m)
			if len(sets) > 0 || len(suffixes) > 0 {
				return sets, suffixes
			}
		}
		return nil, nil
	}
	return ownSets(rule), ownSuffixes(rule)
}

// ownSets 取规则自己的规则集名单,去掉热切换开关(它们只表示分支开关,不是站点集)。
func ownSets(rule map[string]any) []string {
	raw, _ := rule["rule_set"].([]any)
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if t, _ := x.(string); t != "" && !strings.HasPrefix(t, flipsPrefix) {
			out = append(out, t)
		}
	}
	return out
}

func ownSuffixes(rule map[string]any) []string {
	raw, _ := rule["domain_suffix"].([]any)
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if t, _ := x.(string); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// sameList 判断两份名单是不是一模一样(顺序也算,与上游的 join() 比较一致)。
func sameList(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// chainEnd 从 name 出发,沿内核「当前选中」走到末尾,返回末尾出站是 direct 还是 proxy。
// name 不在内核代理列表里、或末尾出站读不到类型时 ok=false(不猜)。
func chainEnd(proxies []singbox.KernelProxy, name string) (string, bool) {
	now := make(map[string]string, len(proxies))
	kind := make(map[string]string, len(proxies))
	for _, x := range proxies {
		now[x.Name], kind[x.Name] = x.Now, x.Type
	}
	if _, ok := now[name]; !ok {
		return "", false
	}
	cur := name
	for i := 0; i < maxChain; i++ {
		next, ok := now[cur]
		if !ok || next == "" {
			break
		}
		cur = next
	}
	switch strings.ToLower(kind[cur]) {
	case "direct":
		return "direct", true
	case "":
		return "", false
	default:
		return "proxy", true
	}
}
