package mihomo

import (
	"encoding/json"

	"github.com/timeflysoon/Open-Box/core/internal/jsx"
	"github.com/timeflysoon/Open-Box/core/internal/protocol"
	"github.com/timeflysoon/Open-Box/core/internal/routing"
)

// Register 注册 Mihomo 相关方法。
func Register(r *protocol.Registry) {
	r.Register("mihomoProxies", hProxies)
	r.Register("mihomoGroups", hGroups)
	r.Register("mihomoTargetRules", hTargetRules)
	r.Register("mihomoCompatibility", hCompat)
	r.Register("mihomoDnsUpstreams", hDnsUpstreams)
}

// mihomoProxies(nodes) → proxies
func hProxies(_ *protocol.Context, args []json.RawMessage) (any, error) {
	var nodes any
	if _, err := protocol.Arg(args, 0, &nodes); err != nil {
		return nil, err
	}
	return Proxies(nodes)
}

// mihomoGroups({profile, nodes, groups, chainNodes, selections}) → {proxies, groups, first, targets, builtins, failover}
func hGroups(_ *protocol.Context, args []json.RawMessage) (any, error) {
	var a any
	if _, err := protocol.Arg(args, 0, &a); err != nil {
		return nil, err
	}
	return Groups(a)
}

// mihomoTargetRules({routing, providers, ruleLists, targets, directDomains, directCIDRs, terminalRoutes, sections})
// → {rules, firstTerminal, endTerminal, directTerminal, firstPolicy}
func hTargetRules(_ *protocol.Context, args []json.RawMessage) (any, error) {
	var a any
	if _, err := protocol.Arg(args, 0, &a); err != nil {
		return nil, err
	}
	return routing.MihomoTargetRules(a)
}

// mihomoCompatibility({profile, nodes, groups, chainNodes, selections}) → problems(字符串数组,没有问题是 [])
func hCompat(_ *protocol.Context, args []json.RawMessage) (any, error) {
	var a any
	if _, err := protocol.Arg(args, 0, &a); err != nil {
		return nil, err
	}
	return Compatibility(a), nil
}

// mihomoDnsUpstreams({profile, systemDns}) → {direct, proxy, directExtras, proxyExtras}
// 双内核 3e-2 装配 Mihomo 的 DNS 段要用:两侧各用哪台上游、各自的额外上游。取法直接复用 routing 的实现
// (与 sing-box 配置那一侧同一套口径),这里只做方法暴露,见 routing/dnsupstream_export.go。
func hDnsUpstreams(_ *protocol.Context, args []json.RawMessage) (any, error) {
	var a any
	if _, err := protocol.Arg(args, 0, &a); err != nil {
		return nil, err
	}
	var systemDNS []any
	if raw := jsx.Get(a, "systemDns"); raw != nil {
		if arr, ok := raw.([]any); ok {
			systemDNS = arr
		}
	}
	return routing.DNSUpstreamsFor(jsx.Get(a, "profile"), systemDNS), nil
}
