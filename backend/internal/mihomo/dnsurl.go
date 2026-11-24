package mihomo

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// DNSServer 是一台上游 DNS(上游 DNS 设置里的一项)。
type DNSServer struct {
	Protocol  string // udp / tcp / tls / https ...
	Server    string
	Port      int
	Path      string
	WAN       bool       // 取自路由器的上游 DNS(WAN 口拿到的),不走代理
	Bootstrap *DNSServer // 解析 Server 这个域名用的 DNS
}

func isIPv6(host string) bool {
	a, err := netip.ParseAddr(host)
	return err == nil && a.Is6()
}

// ServerURL 把一台 DNS 写成 Mihomo 的 nameserver URL:协议://主机:端口路径,IPv6 地址加方括号。
func ServerURL(s DNSServer) string {
	host := s.Server
	if isIPv6(host) {
		host = "[" + host + "]"
	}
	return s.Protocol + "://" + host + ":" + strconv.Itoa(s.Port) + s.Path
}

// URLOptions 是 ServerURLs 的选项。
type URLOptions struct {
	Route    bool // 这组 DNS 要走路由(代理):非 WAN 的加 #RULES
	IPv4Only bool // 加 disable-ipv6=true
}

// ServerURLs 把一组 DNS 写成 nameserver 列表。每项后面按需带 #a&b 的参数,顺序固定:
// RULES(Route 且不是 WAN)、disable-ipv6=true、openbox-bootstrap=<编码后的 bootstrap URL>、
// openbox-node-post=true(https)、openbox-race=true(这组不止一台)。openbox-* 要上游打过补丁的 Mihomo 才认。
func ServerURLs(list []DNSServer, o URLOptions) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		var params []string
		if o.Route && !s.WAN {
			params = append(params, "RULES")
		}
		if o.IPv4Only {
			params = append(params, "disable-ipv6=true")
		}
		if s.Bootstrap != nil {
			params = append(params, "openbox-bootstrap="+EncodeURIComponent(ServerURL(*s.Bootstrap)))
		}
		if s.Protocol == "https" {
			params = append(params, "openbox-node-post=true")
		}
		if len(list) > 1 {
			params = append(params, "openbox-race=true")
		}
		u := ServerURL(s)
		if len(params) > 0 {
			u += "#" + strings.Join(params, "&")
		}
		out = append(out, u)
	}
	return out
}

// EncodeURIComponent 与 JavaScript 的 encodeURIComponent 一致:字母数字和 - _ . ! ~ * ' ( ) 不转义,
// 其余按 UTF-8 字节写成 %XX(大写)。
func EncodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// NodeDNS 是某个订阅的「节点 DNS」:解析这个订阅节点的域名时用的 DNS。
type NodeDNS struct {
	ID           string
	Nameserver   string // DoH 地址原样
	Bootstrap    string // 解析 Nameserver 里那个域名的 udp://IP:53;Nameserver 的主机本来就是 IP 时为空
	HasBootstrap bool
}

// NodeDNSOf 取各个启用订阅的节点 DNS,按订阅顺序。订阅里 enabled 为 false、没配 nodeDns、或地址不合格的跳过;
// 同一个 id 出现两次时后面的覆盖前面的、位置不变。valid 校验一份 nodeDns({url, bootstrap}),传 nil 时只要求 url 非空
// (后端存档案时已经校验过,装配时一般传 subs.ValidateNodeDNS)。
func NodeDNSOf(subs []map[string]any, valid func(map[string]any) error) []NodeDNS {
	var out []NodeDNS
	index := map[string]int{}
	for _, sub := range subs {
		if sub == nil {
			continue
		}
		if en, ok := sub["enabled"].(bool); ok && !en {
			continue
		}
		nd, _ := sub["nodeDns"].(map[string]any)
		rawURL, _ := nd["url"].(string)
		rawURL = strings.TrimSpace(rawURL)
		if nd == nil || rawURL == "" {
			continue
		}
		if valid != nil && valid(nd) != nil {
			continue
		}
		bs, _ := nd["bootstrap"].(string)
		bs = strings.TrimSpace(bs)
		id, _ := sub["id"].(string)
		item := NodeDNS{ID: id, Nameserver: rawURL}
		host := ""
		if u, err := url.Parse(rawURL); err == nil {
			host = u.Hostname()
		}
		if bs != "" && net.ParseIP(host) == nil {
			item.Bootstrap = ServerURL(DNSServer{Protocol: "udp", Server: bs, Port: 53})
			item.HasBootstrap = true
		}
		if i, dup := index[id]; dup {
			out[i] = item
			continue
		}
		index[id] = len(out)
		out = append(out, item)
	}
	return out
}
