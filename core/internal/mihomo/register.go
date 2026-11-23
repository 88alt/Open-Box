package mihomo

import (
	"encoding/json"

	"github.com/timeflysoon/Open-Box/core/internal/protocol"
	"github.com/timeflysoon/Open-Box/core/internal/routing"
)

// Register 注册 Mihomo 相关方法。
func Register(r *protocol.Registry) {
	r.Register("mihomoProxies", hProxies)
	r.Register("mihomoGroups", hGroups)
	r.Register("mihomoTargetRules", hTargetRules)
	r.Register("mihomoCompatibility", hCompat)
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
