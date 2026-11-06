package clientapp

import (
	_ "embed"
	"encoding/json"
)

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

//go:embed share-regions-default.json
var shareRegionsDefaultJSON []byte

type shareRegionsFile struct {
	Version int     `json:"version"`
	Groups  []Group `json:"groups"`
}

// regionDNS 是一组的 DNS 默认值,与前端 constant/shareRegionDns.ts 的 regionDnsDefaults 一致:
// 含中国大陆——直连系统 DNS、代理 TCP 1.1.1.1;不含——两侧都是系统 DNS。
// 随包 JSON 已自带 dns;本函数仅供测试或补缺。
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

// DefaultShareRegions 是「恢复默认」的三组,内容对齐上游 v0.1.412 随包
// panel/server/defaults/share-regions-default.json(维护者授权只读参考;实现自写)。
// 每次返回新的切片,调用方可以随意改。
func DefaultShareRegions() []Group {
	var f shareRegionsFile
	if err := json.Unmarshal(shareRegionsDefaultJSON, &f); err != nil {
		panic("内嵌 share-regions-default.json 损坏: " + err.Error())
	}
	out := make([]Group, len(f.Groups))
	copy(out, f.Groups)
	for i := range out {
		if out[i].Rules == nil {
			out[i].Rules = []Rule{}
		}
		if out[i].DNS.DirectExtras == nil {
			out[i].DNS.DirectExtras = []DNSUpstream{}
		}
		if out[i].DNS.ProxyExtras == nil {
			out[i].DNS.ProxyExtras = []DNSUpstream{}
		}
		// 深拷贝 rules/regions,避免调用方改到内嵌数据
		if len(out[i].Rules) > 0 {
			r := make([]Rule, len(out[i].Rules))
			copy(r, out[i].Rules)
			out[i].Rules = r
		}
		if len(out[i].Regions) > 0 {
			reg := make([]string, len(out[i].Regions))
			copy(reg, out[i].Regions)
			out[i].Regions = reg
		}
	}
	return out
}
