package routing

import (
	"net/url"
	"strconv"
	"strings"
)

// DoH(type:https)与 bootstrap 的档案解析。sing-box 1.12+ DNS server 文档:
//   - https:server / server_port(默认 443)/ path(默认 /dns-query)
//   - 主机是域名时靠 domain_resolver 指向一台 bootstrap(udp / tcp)解析
// 域名 DoT 与域名 DoH 共用同一套 bootstrap:档案里 dns.directBootstrap / dns.proxyBootstrap(备用上游用各自的 bootstrap),
// 留空 = 「上游 DNS」(和原来域名 DoT 的行为一致)。

// parseDoHURL:https DoH 地址 → host(IPv6 不带方括号), port(地址里没写端口时为 0), path(含查询串)。
func parseDoHURL(raw any) (host string, port int, path string, ok bool) {
	s, isStr := raw.(string)
	if !isStr {
		return "", 0, "", false
	}
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", 0, "", false
	}
	if p := u.Port(); p != "" {
		n, perr := strconv.Atoi(p)
		if perr != nil || n < 1 || n > 65535 {
			return "", 0, "", false
		}
		port = n
	}
	path = u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return u.Hostname(), port, path, true
}

// dohUpstreamOf:协议是 https 且地址不是 wan 时的 upstream。地址里的端口优先,其次档案里的 port,最后 443。
// 地址不合法:invalid 是 wan 就退成上游 DNS,否则退成 invalid(fallbackProtocol,默认端口)。
func dohUpstreamOf(profile, raw, port any, fallbackProtocol, invalid string, systemDNS []any) upstream {
	host, urlPort, path, ok := parseDoHURL(raw)
	if !ok {
		if invalid == WanUpstream {
			return upstream{Protocol: "udp", Server: wanServer(profile, systemDNS), Port: DefaultDNSPort, Wan: true}
		}
		return upstream{Protocol: fallbackProtocol, Server: invalid, Port: dnsPortOr(nil, fallbackProtocol)}
	}
	p := urlPort
	if p == 0 {
		p = dnsPortOr(port, "https")
	}
	return upstream{Protocol: "https", Server: host, Port: p, Domain: !isValidDNSUpstream(host), Path: path}
}

// readBootstrap:档案里的 bootstrap 对象 → 地址 / 协议(udp 或 tcp)/ 端口。留空、写 wan、地址不是 IP 都返回 nil 地址(= 用上游 DNS)。
func readBootstrap(raw any) (server any, proto string, port int) {
	m, ok := raw.(map[string]any)
	if !ok || m == nil {
		return nil, "udp", DefaultDNSPort
	}
	proto = "udp"
	if pr, isStr := m["protocol"].(string); isStr && pr == "tcp" {
		proto = "tcp"
	}
	port = dnsPortOr(m["port"], proto)
	if isWanUpstream(m["server"]) {
		return nil, proto, port
	}
	if ip := normalizeDNSUpstream(m["server"]); ip != "" {
		return ip, proto, port
	}
	return nil, proto, port
}

// attachBootstrap:需要解析主机名的上游(域名 DoT / 域名 DoH)带上档案里指定的 bootstrap;没指定就不动(生成时退回上游 DNS)。
func attachBootstrap(up upstream, bootRaw any) upstream {
	if !up.Domain {
		return up
	}
	srv, proto, port := readBootstrap(bootRaw)
	if srv == nil {
		return up
	}
	up.BootServer, up.BootProto, up.BootPort = srv, proto, port
	return up
}

// bootstrapEntry:bootstrap 的 DNS server 条目(不走 detour)。没指定 bootstrap 就是「上游 DNS」此刻的那台 udp。
func bootstrapEntry(up upstream, tag string, profile any, systemDNS []any) map[string]any {
	if up.BootServer == nil {
		return map[string]any{"type": "udp", "tag": tag, "server": wanServer(profile, systemDNS)}
	}
	e := map[string]any{"type": up.BootProto, "tag": tag, "server": up.BootServer}
	if up.BootPort != 0 && up.BootPort != dnsDefaultPort(up.BootProto) {
		e["server_port"] = up.BootPort
	}
	return e
}
