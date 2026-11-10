package routing

import (
	"math"
	"regexp"
	"strings"

	"github.com/timeflysoon/Open-Box/core/internal/jsx"
	"github.com/timeflysoon/Open-Box/core/internal/urlx"
)

// 对应上游 engine/dns-upstream.mjs(基线 v0.1.318 快照,v0.1.422 起另加 DoT)。档案里的两个 DNS 上游
// (dns.direct / dns.proxy)和各自的协议、端口、备用上游:
//   - 协议:udp / tcp / tls(DoT)。udp / tcp 默认端口 53;tls 默认 853(sing-box type:tls 的默认端口)
//   - 地址:IPv4 / IPv6 或「上游 DNS」记号 wan;tls 另收域名(生成时带 domain_resolver + udp bootstrap,见 dnsServerEntries)
//   - 端口等于该协议的默认值时,生成配置不写 server_port
//
// DoT 部分按 sing-box 1.12+ DNS server 文档(type:tls)自写,行为目标来自维护者对 v0.1.422 的功能说明。
// 这里只移植 configDetailed 用得到的部分。下面这些只有面板的 Node 进程用,不属于 core,没有移植:
// regionDnsDefaults / regionDnsError(地区页签与档案校验)、directResolverServers(面板自己做直连解析)、
// isProxyResolverTag / applyProxyUpstreamRoutes(部署后处理)、dnsPolicyClasses(部署流程)。

const (
	DefaultDNSPort        = 53
	DefaultDoTPort        = 853 // sing-box type:tls 的默认端口
	DefaultDoHPort        = 443 // sing-box type:https 的默认端口
	DefaultDirectUpstream = "223.5.5.5"
	DefaultProxyUpstream  = "1.1.1.1"
	// MaxDNSExtras:每一侧最多几个备用上游(和主上游一起并发竞速)。
	MaxDNSExtras = 3
	// WanUpstream:「上游 DNS」记号——路由器系统此刻的上游 DNS(部署时读 resolv.conf.auto)。固定直连、固定 UDP 53。
	WanUpstream = "wan"
)

// JS 的 \d 只认 ASCII 数字,Go 的 \d 同样;字符类一律写死 ASCII,避免 (?i) 带进 Unicode 折叠(ſ、K 等)。
var (
	dnsIPv4Re   = regexp.MustCompile(`^(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)$`)
	dnsIPv6Re   = regexp.MustCompile(`^[0-9a-fA-F:]+$`)
	dnsSchemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*://`)
)

func isWanUpstream(v any) bool { s, ok := v.(string); return ok && s == WanUpstream }

// dnsRegionOf:路由器在哪(档案 dns.region)。没有这个键的(老设备、全新安装)按中国大陆算。
func dnsRegionOf(profile any) string {
	if jsx.Get(jsx.Get(profile, "dns"), "region") == "intl" {
		return "intl"
	}
	return "cn"
}

// wanFallbackUpstream:读不到系统的上游 DNS 时「上游 DNS」退回哪台。
func wanFallbackUpstream(region string) string {
	if region == "intl" {
		return DefaultProxyUpstream
	}
	return DefaultDirectUpstream
}

func isValidDNSUpstream(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	host := jsx.Trim(s)
	if host == "" {
		return false
	}
	if dnsIPv4Re.MatchString(host) {
		return host != "0.0.0.0"
	}
	if !strings.Contains(host, ":") {
		return false
	}
	return dnsIPv6Re.MatchString(host) && host != "::" && len(strings.Split(host, "::")) <= 2 && len(strings.Split(host, ":")) <= 8
}

// normalizeDNSUpstream:"https://1.1.1.1/dns-query" → "1.1.1.1";"[2001:db8::1]" → "2001:db8::1";裸地址原样(去空白)。
// 折不出合法 IP(包括域名)就返回空串,调用方自己决定兜底值。
func normalizeDNSUpstream(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	host := jsx.Trim(s)
	if dnsSchemeRe.MatchString(host) {
		h, ok := urlx.Hostname(host) // new URL(host).hostname;抛错 → ''
		if !ok {
			return ""
		}
		host = h
	}
	host = strings.TrimPrefix(host, "[") // host.replace(/^\[|\]$/g, '')
	host = strings.TrimSuffix(host, "]")
	if isValidDNSUpstream(host) {
		return host // 注意:不再 trim——和 JS 一样,方括号里的空白会被原样带出去
	}
	return ""
}

func isDNSProtocol(v any) bool {
	s, ok := v.(string)
	return ok && (s == "udp" || s == "tcp" || s == "tls" || s == "https")
}

func isValidDNSPort(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || f < 1 || f > 65535 {
		return 0, false
	}
	return int(f), true
}

// dnsDefaultPort:协议默认端口(udp / tcp → 53,tls → 853,https → 443)。
func dnsDefaultPort(protocol string) int {
	switch protocol {
	case "tls":
		return DefaultDoTPort
	case "https":
		return DefaultDoHPort
	}
	return DefaultDNSPort
}

func dnsPortOr(v any, protocol string) int {
	if p, ok := isValidDNSPort(v); ok {
		return p
	}
	return dnsDefaultPort(protocol)
}

func dnsProtocolOr(v any, fallback string) string {
	if isDNSProtocol(v) {
		return v.(string)
	}
	return fallback
}

// isDNSDomainHost:DoT 允许的域名主机(非 IP、含点、各标签只含 ASCII 字母数字和连字符)。
func isDNSDomainHost(host string) bool {
	host = jsx.Trim(host)
	if host == "" || isValidDNSUpstream(host) || isWanUpstream(host) {
		return false
	}
	if len(host) > 253 || !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// normalizeDNSDomain:从原始值取出可作 DoT server 的域名(统一小写);带协议头时取 hostname。不是域名返回 ""。
func normalizeDNSDomain(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	host := jsx.Trim(s)
	if dnsSchemeRe.MatchString(host) {
		h, ok := urlx.Hostname(host)
		if !ok {
			return ""
		}
		host = h
	}
	if isDNSDomainHost(host) {
		return strings.ToLower(host)
	}
	return ""
}

// upstream 是档案里的一条上游整理后的样子。Server 是 any:「上游 DNS」取的是部署时读到的系统上游,JS 里不做类型检查,
// 测试里喂过非字符串,保持原样带出去。Domain 为 true 表示 Server 是域名(仅 tls),生成配置要带 domain_resolver + bootstrap。
type upstream struct {
	Protocol   string
	Server     any
	Port       int
	Wan        bool
	Domain     bool
	Path       string
	BootServer any
	BootProto  string
	BootPort   int
}

// serverKey 对应 JS Set 的 SameValueZero:同一台上游去重用。
func serverKey(v any) string {
	switch x := v.(type) {
	case string:
		return "s:" + x
	case float64:
		return "n:" + jsx.NumberToString(x)
	}
	return "o:" + jsx.ToString(v)
}

// wanServer:「上游 DNS」此刻是哪台——部署时读到的系统上游 DNS 的第一台,读不到按地区退回。
func wanServer(profile any, systemDNS []any) any {
	for _, v := range systemDNS {
		if jsx.Truthy(v) {
			return v
		}
	}
	return wanFallbackUpstream(dnsRegionOf(profile))
}

// upstreamOf:档案里的一条上游 → upstream。上游 DNS 固定 UDP 53(协议跟着上游,档案里存的协议不看),wan 标出来
// (代理侧不给它写 detour);地址折不出合法 IP 的(tls 再试域名;手改库)按 invalid 退:直连侧退成上游 DNS,代理侧退成 1.1.1.1。
func upstreamOf(profile, raw, protocol, port any, fallbackProtocol, invalid string, systemDNS []any) upstream {
	proto := dnsProtocolOr(protocol, fallbackProtocol)
	if proto == "https" && !isWanUpstream(raw) {
		return dohUpstreamOf(profile, raw, port, fallbackProtocol, invalid, systemDNS)
	}
	server := ""
	domain := false
	if !isWanUpstream(raw) {
		server = normalizeDNSUpstream(raw)
		if server == "" && proto == "tls" {
			if d := normalizeDNSDomain(raw); d != "" {
				server, domain = d, true
			}
		}
	}
	if server == "" && (isWanUpstream(raw) || invalid == WanUpstream) {
		return upstream{Protocol: "udp", Server: wanServer(profile, systemDNS), Port: DefaultDNSPort, Wan: true}
	}
	if server == "" {
		server = invalid
	}
	return upstream{Protocol: proto, Server: server, Port: dnsPortOr(port, proto), Domain: domain}
}

// directUpstream:直连侧实际用哪台。默认是上游 DNS。
func directUpstream(profile any, systemDNS []any) upstream {
	dns := jsx.Get(profile, "dns")
	up := upstreamOf(profile, jsx.Get(dns, "direct"), jsx.Get(dns, "directProtocol"), jsx.Get(dns, "directPort"), "udp", WanUpstream, systemDNS)
	return attachBootstrap(up, jsx.Get(dns, "directBootstrap"))
}

// proxyUpstream:代理侧上游,协议默认 tcp(UDP 经代理常被截断 / 丢包),地址不合法退成 1.1.1.1。
func proxyUpstream(profile any, systemDNS []any) upstream {
	dns := jsx.Get(profile, "dns")
	up := upstreamOf(profile, jsx.Get(dns, "proxy"), jsx.Get(dns, "proxyProtocol"), jsx.Get(dns, "proxyPort"), "tcp", DefaultProxyUpstream, systemDNS)
	return attachBootstrap(up, jsx.Get(dns, "proxyBootstrap"))
}

// extraUpstreams:备用上游(档案 dns.directExtras / dns.proxyExtras),和主上游并发竞速。
// 先截前 MaxDNSExtras 个再筛,和主上游(或别的备用)落到同一台的去掉。
func extraUpstreams(profile, list any, primary upstream, fallbackProtocol string, systemDNS []any) []upstream {
	out := []upstream{}
	arr, ok := list.([]any)
	if !ok {
		return out
	}
	if len(arr) > MaxDNSExtras {
		arr = arr[:MaxDNSExtras]
	}
	seen := map[string]bool{serverKey(primary.Server): true}
	for _, item := range arr {
		if !jsx.Truthy(item) {
			continue
		}
		server := jsx.Get(item, "server")
		itemProto := dnsProtocolOr(jsx.Get(item, "protocol"), fallbackProtocol)
		_, _, _, dohOK := parseDoHURL(server)
		if !(isWanUpstream(server) || normalizeDNSUpstream(server) != "" || (itemProto == "tls" && normalizeDNSDomain(server) != "") || (itemProto == "https" && dohOK)) {
			continue
		}
		up := upstreamOf(profile, server, jsx.Get(item, "protocol"), jsx.Get(item, "port"), fallbackProtocol, WanUpstream, systemDNS)
		up = attachBootstrap(up, jsx.Get(item, "bootstrap"))
		k := serverKey(up.Server)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, up)
	}
	return out
}

func directExtraUpstreams(profile any, systemDNS []any) []upstream {
	return extraUpstreams(profile, jsx.Get(jsx.Get(profile, "dns"), "directExtras"), directUpstream(profile, systemDNS), "udp", systemDNS)
}

func proxyExtraUpstreams(profile any, systemDNS []any) []upstream {
	return extraUpstreams(profile, jsx.Get(jsx.Get(profile, "dns"), "proxyExtras"), proxyUpstream(profile, systemDNS), "tcp", systemDNS)
}

// dnsServerEntry:生成配置里的一条 DNS server(sing-box 1.12+ 的 server 对象)。
// 端口等于该协议默认值(udp / tcp 53,tls 853)就不写 server_port;域名 DoT 的 bootstrap 由 dnsServerEntries 补。
func dnsServerEntry(up upstream, tag, detour string) map[string]any {
	m := map[string]any{"type": up.Protocol, "tag": tag, "server": up.Server}
	if up.Port != dnsDefaultPort(up.Protocol) {
		m["server_port"] = up.Port
	}
	if up.Protocol == "https" && up.Path != "" && up.Path != "/dns-query" {
		m["path"] = up.Path
	}
	if detour != "" {
		m["detour"] = detour
	}
	return m
}

// dnsServerEntries:主 server;若是域名 DoT,再附一台 udp bootstrap(「上游 DNS」此刻的那台)。
// bootstrap 的 tag 为 tag+"-bootstrap",主条目写 domain_resolver 指向它;bootstrap 不走 detour。
func dnsServerEntries(up upstream, tag, detour string, profile any, systemDNS []any) []map[string]any {
	main := dnsServerEntry(up, tag, detour)
	if !up.Domain {
		return []map[string]any{main}
	}
	bootTag := tag + "-bootstrap"
	main["domain_resolver"] = bootTag
	return []map[string]any{main, bootstrapEntry(up, bootTag, profile, systemDNS)}
}
