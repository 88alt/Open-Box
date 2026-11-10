package routetest

import "github.com/timeflysoon/Open-Box/backend/internal/singbox"

// ConnView 是内核连接表里一条连接,按前端要的样子整理后的视图(route-test 的 exit 和 terminal-test 的 kernel 共用)。
type ConnView struct {
	Chains        []string // 路由选中的在前、最终出口在后(Clash API 给的顺序是反的)
	Rule          string
	RulePayload   string
	DestinationIP string
	Host          string
	NoDomain      bool // 命中的是只管没有域名的连接的 IP 规则,见 noDomainRule
	ViaProxy      bool // 最终出口不是 direct 类型的出站
}

// ViewOf 整理一条连接。连接没有链(还没选出口)返回 false。direct 是配置里所有 direct 出站的名字。
func ViewOf(c *singbox.KernelConn, direct []string) (ConnView, bool) {
	if c == nil || len(c.Chains) == 0 {
		return ConnView{}, false
	}
	chains := make([]string, 0, len(c.Chains))
	for i := len(c.Chains) - 1; i >= 0; i-- {
		chains = append(chains, c.Chains[i])
	}
	return ConnView{
		Chains: chains, Rule: c.Rule, RulePayload: c.RulePayload, DestinationIP: c.DestIP, Host: c.Host,
		NoDomain: noDomainRule(c.Rule, c.Host),
		ViaProxy: !contains(direct, chains[len(chains)-1]),
	}, true
}
