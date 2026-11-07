package clientroutes

import (
	"strings"

	"github.com/timeflysoon/Open-Box/core/internal/ipx"
	"github.com/timeflysoon/Open-Box/core/internal/jsx"
)

// ExpandSourcesWithBindings 按 clientBindings / ipv6Bindings 把 IPv4 来源扩展出对应的 IPv6 /128。
//
// bindings 形状(自研约定,与前端/后端对齐):
//
//	{ "192.168.1.10": ["fd00::10", "fe80::1"], "10.0.0.5/32": "fd00::5" }
//
// 键可以是裸 IPv4 或带前缀的 CIDR;值是字符串或字符串数组。
// 只扩展 IPv4 来源;已有的 IPv6 来源原样保留。查表时依次试:
//  1. 规范化后的整段 (192.168.1.10/32)
//  2. 裸地址 (192.168.1.10)
//
// 找不到绑定的来源不变。去重且保持原顺序,新 IPv6 跟在对应 IPv4 后面。
func ExpandSourcesWithBindings(sources []string, bindings any) []string {
	if len(sources) == 0 || bindings == nil {
		return sources
	}
	m, ok := bindings.(map[string]any)
	if !ok || len(m) == 0 {
		return sources
	}
	// 预解析绑定表:key → 规范化 IPv6 /128 列表
	table := map[string][]string{}
	for k, v := range m {
		key := jsx.Trim(jsx.ToString(k))
		if key == "" {
			continue
		}
		var addrs []any
		switch t := v.(type) {
		case []any:
			addrs = t
		case []string:
			for _, s := range t {
				addrs = append(addrs, s)
			}
		default:
			if jsx.Truthy(v) {
				addrs = []any{v}
			}
		}
		var v6s []string
		seen := map[string]bool{}
		for _, a := range addrs {
			s := jsx.Trim(jsx.ToString(a))
			if s == "" || !ipx.IsIPv6(s) || strings.Contains(s, "%") {
				continue
			}
			// 裸 IPv6 → /128
			c := s
			if !strings.Contains(s, "/") {
				c = s + "/128"
			} else if n := NormalizeCidr(s); n != "" {
				c = n
			} else {
				continue
			}
			if !seen[c] {
				seen[c] = true
				v6s = append(v6s, c)
			}
		}
		if len(v6s) == 0 {
			continue
		}
		table[key] = v6s
		// 同时用裸地址做索引,方便 192.168.1.10/32 与 192.168.1.10 互通
		if parts := strings.SplitN(key, "/", 2); len(parts) == 2 && ipx.IsIPv4(parts[0]) {
			if _, exists := table[parts[0]]; !exists {
				table[parts[0]] = v6s
			}
		} else if ipx.IsIPv4(key) {
			slash32 := key + "/32"
			if _, exists := table[slash32]; !exists {
				table[slash32] = v6s
			}
		}
	}
	if len(table) == 0 {
		return sources
	}
	out := make([]string, 0, len(sources)*2)
	seen := map[string]bool{}
	for _, s := range sources {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
		extra := table[s]
		if extra == nil {
			// 再试裸地址
			if parts := strings.SplitN(s, "/", 2); len(parts) == 2 {
				extra = table[parts[0]]
			}
		}
		for _, e := range extra {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	return out
}
