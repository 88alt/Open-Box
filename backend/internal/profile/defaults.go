// Package profile 管理「档案」(面板的全部设置):默认值、深度合并、校验、落盘。
//
// 档案在内存和磁盘上都是 map[string]any:前端会往里写一些类型声明里没有的键
// (例如 shareRegions、serverInfo),这些键必须原样保留,所以不用固定结构体。
package profile

// WAN 是「路由器系统的上游 DNS」的记号(前端 DnsUpstreamCard 的 WAN 常量)。
const WAN = "wan"

// 地区取值。
const (
	RegionCN   = "cn"
	RegionIntl = "intl"
)

// ValidRegion 报告 r 是不是支持的地区。
func ValidRegion(r string) bool { return r == RegionCN || r == RegionIntl }

// DefaultDNS 返回某个地区的默认 DNS 设置。
// 上游的默认值以前端 DnsUpstreamCard 的 regionDefaultsPatch 为准:
// 中国大陆——直连用系统上游 DNS,代理走 TCP 1.1.1.1;中国大陆之外——两侧都用系统上游 DNS。
func DefaultDNS(region string) map[string]any {
	proxy, proxyProto := "1.1.1.1", "tcp"
	if region == RegionIntl {
		proxy, proxyProto = WAN, "udp"
	}
	return map[string]any{
		"mode":           "dnsmasq",
		"split":          true,
		"region":         region,
		"direct":         WAN,
		"directProtocol": "udp",
		"directPort":     float64(53),
		"directExtras":   []any{},
		"proxy":          proxy,
		"proxyProtocol":  proxyProto,
		"proxyPort":      float64(53),
		"proxyExtras":    []any{},
		"fakeIpForProxy": false,
		"rewrite":        map[string]any{"enabled": false, "initialized": float64(0), "rules": []any{}},
		"filter": map[string]any{
			"enabled": false, "lists": []any{}, "allowDomains": []any{},
			"autoUpdate": map[string]any{"enabled": false, "days": float64(1), "hour": float64(4)},
		},
	}
}

// DefaultRouting 返回默认分流:一个「国内直连」站点集 + 兜底走第一个节点组。
// 这是自研后端自己定的起点(没有参照上游),规则集名按随包规则库的命名(geosite-cn / geoip-cn);
// 规则库里没有的名字会被 core 的 normalizeRouting 过滤掉,不会导致配置出错。
func DefaultRouting() map[string]any {
	return map[string]any{
		"fallbackName":    "其他",
		"fallbackIcon":    "globe:earth-meridians",
		"fallbackDefault": "proxy", // 迁移留下的占位:第一个节点组
		"displayOrder":    []any{},
		"outboundOptions": map[string]any{"direct": true, "reject": true, "groups": true},
		"adBlock":         false,
		"policies": []any{
			map[string]any{
				"id": "cn", "name": "国内直连", "icon": "misc:dart", "default": "direct",
				"enabled": true, "rulesets": []any{"geosite-cn", "geoip-cn"},
			},
		},
	}
}

// DefaultDNSRewrite 是「DNS 重写」的默认项。目前为空:没有可靠依据的规则不预置
// (见 docs/DESIGN.md「待维护者确认」)。
func DefaultDNSRewrite() []any { return []any{} }

// DefaultGroupsInput 是默认节点组的原始输入(交给 core 的 normalizeGroups 补全并带上内置直连 / 拒绝)。
func DefaultGroupsInput() []any {
	return []any{
		map[string]any{"id": "auto", "name": "自动选择", "type": "urltest", "mode": "dynamic"},
	}
}

// Default 返回一份完整的默认档案。
func Default() map[string]any {
	return map[string]any{
		"region":               RegionCN,
		"ipv6":                 false,
		"ipv6Proxy":            "node",
		"directForNodes":       true,
		"directBypass":         true,
		"rejectQuic":           true,
		"bypassPorts":          "",
		"bypassPortsWhitelist": "",
		"bypassPortsMode":      "blacklist",
		"tun":                  map[string]any{"autoRedirect": true, "stack": "mixed", "mtu": float64(0), "tcpMss": float64(0)},
		"dns":                  DefaultDNS(RegionCN),
		"routing":              DefaultRouting(),
		"testUrl":              "https://www.gstatic.com/generate_204",
		"directTestUrl":        "https://connectivitycheck.platform.hicloud.com/generate_204",
		"testExpectedStatus":   "",
		"traffic":              map[string]any{"keepMonths": float64(12)},
		"updates":              map[string]any{"openbox": map[string]any{"auto": false, "hour": float64(4), "days": float64(1), "channel": "auto"}},
		"servers":              []any{},
		"clientRoutes":         []any{},
		"chainProxies":         []any{},
	}
}

// RegionDefaults 是 GET /api/openbox/profile/defaults?region=x 的 defaults 字段。
func RegionDefaults(region string) map[string]any {
	return map[string]any{"region": region, "dns": DefaultDNS(region), "routing": DefaultRouting()}
}

// Clone 深拷贝 JSON 形态的值(map[string]any / []any / 标量)。
func Clone(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = Clone(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = Clone(e)
		}
		return out
	default:
		return v
	}
}
