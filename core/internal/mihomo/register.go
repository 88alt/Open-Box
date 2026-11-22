package mihomo

import (
	"encoding/json"

	"github.com/timeflysoon/Open-Box/core/internal/protocol"
)

// Register 注册 Mihomo 相关方法。
func Register(r *protocol.Registry) {
	r.Register("mihomoProxies", hProxies)
	r.Register("mihomoGroups", hGroups)
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
