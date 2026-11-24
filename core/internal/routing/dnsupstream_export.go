package routing

import "github.com/timeflysoon/Open-Box/core/internal/jsx"

// 把 dnsupstream.go 里的取法导给后端:双内核(backend STATUS 3e-2)装配 Mihomo 的 DNS 段时,
// 要拿「直连 / 代理两侧各用哪台上游 DNS,以及各自的额外上游」。这套逻辑 core 里已经有了
// (directUpstream / proxyUpstream / directExtraUpstreams / proxyExtraUpstreams),后端不能 import core,
// 抄一份又会和 core 各自漂移,所以这里加一个薄方法(mihomo/mihomoDnsUpstreams)。
//
// 形状(与 backend/STATUS.md 里那条「需要 core 配合」一致):
//
//	{profile, systemDns} → {direct, proxy, directExtras, proxyExtras}
//	   server 对象 {protocol, server, port, path?, wan?, bootstrap?}
//	     protocol  udp / tcp / tls / https
//	     server    地址字符串,IPv6 不带方括号(写 URL 时由后端加)
//	     path      只有 https 有(地址里带的路径,默认已补成 /dns-query 或 /)
//	     wan       true = 取自路由器自己的上游 DNS(代理侧不给它加 #RULES)
//	     bootstrap 需要解析主机名时的那台解析器,形状 {protocol, server, port}
//
// 这里只做形状转换,取法一律复用 dnsupstream.go / dnsupstream_doh.go,避免与 sing-box 配置那一侧出现两套口径。

// DNSUpstreamServer 是一台上游 DNS 的形状。
type DNSUpstreamServer struct {
	Protocol  string           `json:"protocol"`
	Server    string           `json:"server"`
	Port      int              `json:"port"`
	Path      string           `json:"path,omitempty"`
	Wan       bool             `json:"wan,omitempty"`
	Bootstrap *DNSUpstreamBoot `json:"bootstrap,omitempty"`
}

// DNSUpstreamBoot 是 bootstrap 解析器的形状(对应 upstream 的 BootProto / BootServer / BootPort)。
type DNSUpstreamBoot struct {
	Protocol string `json:"protocol"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
}

// DNSUpstreamSet 是 mihomo/mihomoDnsUpstreams 的返回。
type DNSUpstreamSet struct {
	Direct       DNSUpstreamServer   `json:"direct"`
	Proxy        DNSUpstreamServer   `json:"proxy"`
	DirectExtras []DNSUpstreamServer `json:"directExtras"`
	ProxyExtras  []DNSUpstreamServer `json:"proxyExtras"`
}

// DNSUpstreamsFor:profile + systemDns(与 routeGet 等方法的 systemDns 同源)→ 两侧上游。
func DNSUpstreamsFor(profile any, systemDNS []any) DNSUpstreamSet {
	return DNSUpstreamSet{
		Direct:       dnsUpstreamServerOf(directUpstream(profile, systemDNS), profile, systemDNS),
		Proxy:        dnsUpstreamServerOf(proxyUpstream(profile, systemDNS), profile, systemDNS),
		DirectExtras: dnsUpstreamServersOf(directExtraUpstreams(profile, systemDNS), profile, systemDNS),
		ProxyExtras:  dnsUpstreamServersOf(proxyExtraUpstreams(profile, systemDNS), profile, systemDNS),
	}
}

func dnsUpstreamServersOf(ups []upstream, profile any, systemDNS []any) []DNSUpstreamServer {
	// 空列表序列化成 [],不是 null。
	out := make([]DNSUpstreamServer, 0, len(ups))
	for _, up := range ups {
		out = append(out, dnsUpstreamServerOf(up, profile, systemDNS))
	}
	return out
}

func dnsUpstreamServerOf(up upstream, profile any, systemDNS []any) DNSUpstreamServer {
	s := DNSUpstreamServer{
		Protocol: up.Protocol,
		Server:   serverText(up.Server),
		Port:     up.Port,
		Wan:      up.Wan,
	}
	if up.Protocol == "https" {
		s.Path = up.Path
	}
	if up.Domain {
		// 和 dnsServerEntries / bootstrapEntry 同一套取法:档案里指定了 bootstrap 就用它,
		// 没指定就退回「上游 DNS」此刻的那台 udp 53。
		if up.BootServer != nil {
			s.Bootstrap = &DNSUpstreamBoot{Protocol: up.BootProto, Server: serverText(up.BootServer), Port: up.BootPort}
		} else {
			s.Bootstrap = &DNSUpstreamBoot{Protocol: "udp", Server: serverText(wanServer(profile, systemDNS)), Port: DefaultDNSPort}
		}
	}
	return s
}

// serverText:上游地址按字符串给出(档案里已经规整成字符串;系统上游 DNS 万一是数字之类,按 JS 的 String() 转)。
func serverText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return jsx.ToString(v)
}
