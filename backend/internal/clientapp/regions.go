package clientapp

// 地区分流(档案 shareRegions)的形状,取自前端 api/clientApp.ts 的 OpenboxShareRegionGroup 等类型。
// 共享网络的手机客户端按所在地区选一组:命中规则的按规则走(direct 本地直连 / proxy 经路由器),其余按 catchAll 走。

// Rule 是一条地区分流规则。type 取 geosite / geoip / domain / domainSuffix / domainKeyword / ipcidr / ruleUrl。
type Rule struct {
	Type   string `json:"type"`
	Value  string `json:"value"`
	Action string `json:"action"`
}

// DNSUpstream 是一条备用 DNS 上游。server 是 IP,或记号 "wan"(手机此刻所在网络的系统 DNS,固定 UDP 53)。
type DNSUpstream struct {
	Server   string `json:"server"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
}

// GroupDNS 是一组的 DNS:直连、代理两侧各一条主上游加若干备用。
type GroupDNS struct {
	Direct         string        `json:"direct"`
	DirectProtocol string        `json:"directProtocol"`
	DirectPort     int           `json:"directPort"`
	DirectExtras   []DNSUpstream `json:"directExtras"`
	Proxy          string        `json:"proxy"`
	ProxyProtocol  string        `json:"proxyProtocol"`
	ProxyPort      int           `json:"proxyPort"`
	ProxyExtras    []DNSUpstream `json:"proxyExtras"`
}

// Group 是一个地区组。
type Group struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Regions     []string `json:"regions"`
	RegionMatch string   `json:"regionMatch,omitempty"` // "" / "inside" = 在这些地区时用;"outside" = 在这些地区之外时用
	Default     bool     `json:"default,omitempty"`
	Rules       []Rule   `json:"rules"`
	CatchAll    string   `json:"catchAll"`
	DNS         GroupDNS `json:"dns"`
}

const wanUpstream = "wan"

// regionDNS 是一组的 DNS 默认值,与前端 constant/shareRegionDns.ts 的 regionDnsDefaults 一致:
// 含中国大陆——直连系统 DNS、代理 TCP 1.1.1.1;不含——两侧都是系统 DNS。
func regionDNS(inCN bool) GroupDNS {
	d := GroupDNS{
		Direct: wanUpstream, DirectProtocol: "udp", DirectPort: 53, DirectExtras: []DNSUpstream{},
		Proxy: wanUpstream, ProxyProtocol: "udp", ProxyPort: 53, ProxyExtras: []DNSUpstream{},
	}
	if inCN {
		d.Proxy, d.ProxyProtocol = "1.1.1.1", "tcp"
	}
	return d
}

// DefaultShareRegions 是「恢复默认」的三组:中国大陆 / 港澳 / 其他地区。
//
// 自己的设计,没有参照上游(前端只说了有这三组,没说内容)。思路是「人在哪就按哪的网络环境来」:
//   - 中国大陆:国内的站点(geosite cn、geoip cn)本地直连,其余经路由器出去;DNS 用含大陆的默认值。
//   - 港澳、其他地区:没有大陆那样的网络限制,默认本地直连;只把国内的站点(geosite cn、geoip cn)送回路由器,
//     这样在外面也能用到只认国内出口的服务;DNS 两侧都用手机当前网络的系统 DNS。
//     「其他地区」是默认组(定位不到、或不在任何一组里时用),写成「中国大陆 / 港澳之外」。
//
// 每次返回新的切片,调用方可以随意改。
func DefaultShareRegions() []Group {
	cnRules := func(action string) []Rule {
		return []Rule{
			{Type: "geosite", Value: "cn", Action: action},
			{Type: "geoip", Value: "cn", Action: action},
		}
	}
	return []Group{
		{
			ID: "cn", Name: "中国大陆", Description: "国内网站本地直连,其余回路由器",
			Regions: []string{"CN"}, Rules: cnRules("direct"), CatchAll: "proxy", DNS: regionDNS(true),
		},
		{
			ID: "hkmo", Name: "港澳", Description: "本地直连,国内网站回路由器",
			Regions: []string{"HK", "MO"}, Rules: cnRules("proxy"), CatchAll: "direct", DNS: regionDNS(false),
		},
		{
			ID: "other", Name: "其他地区", Description: "本地直连,国内网站回路由器",
			Regions: []string{"CN", "HK", "MO"}, RegionMatch: "outside", Default: true,
			Rules: cnRules("proxy"), CatchAll: "direct", DNS: regionDNS(false),
		},
	}
}
