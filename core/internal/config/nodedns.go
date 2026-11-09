package config

import (
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/timeflysoon/Open-Box/core/internal/ipx"
	"github.com/timeflysoon/Open-Box/core/internal/jsx"
	"github.com/timeflysoon/Open-Box/core/internal/urlx"
)

// 对应基线 engine/node-dns.mjs:节点服务器域名的专用解析器(GitHub #136)。
// 有的机场要求节点域名只能用它自己给的 DoH 解析;按订阅设 nodeDns = { url, bootstrap }。
// 私有路径相当于口令:诊断包里要抹掉(api/diagnostics.mjs 按 NodeDNSTagPrefix 认)。

const NodeDNSTagPrefix = "dns-sub-"

type dohURL struct {
	host string
	port int
	path string
}

func parseDohURL(raw string) (dohURL, bool) {
	u, ok := urlx.Parse(jsx.Trim(raw))
	if !ok || u.Scheme != "https" || u.Hostname == "" || u.Username != "" || u.Password != "" || u.Hash != "" {
		return dohURL{}, false
	}
	host := u.Hostname
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	port := 443
	if u.Port != "" {
		port, _ = strconv.Atoi(u.Port)
	}
	path := u.Pathname
	if path == "" {
		path = "/"
	}
	return dohURL{host, port, path + u.Search}, true
}

func isIP(s string) bool { return ipx.IsIP(s) != 0 }

// utf16Len 是 JS 的 .length(按 UTF-16 码元数)。
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func strField(m map[string]any, key string) (string, bool) {
	s, ok := m[key].(string)
	return s, ok
}

// ValidateNodeDNS 是保存前的校验:返回错误信息,合法返回 ""。nodeDns 为空表示不用专用解析器。
// protocol 缺省或 https → DoH(url 或 server);tls → DoT(server 为主,兼容把 host 写在 url 里)。
func ValidateNodeDNS(raw any) string {
	if raw == nil || jsx.IsUndefined(raw) {
		return ""
	}
	m, isObj := raw.(map[string]any)
	if !isObj {
		return "nodeDns must be an object"
	}
	for _, key := range []string{"url", "bootstrap", "server", "protocol"} {
		if _, has := m[key]; has {
			if _, ok := m[key].(string); !ok {
				return "nodeDns." + key + " must be a string"
			}
		}
	}
	proto, _ := strField(m, "protocol")
	proto = strings.ToLower(jsx.Trim(proto))
	if proto == "" {
		proto = "https"
	}
	if proto != "https" && proto != "tls" {
		return "nodeDns.protocol must be https or tls"
	}
	url, _ := strField(m, "url")
	url = jsx.Trim(url)
	server, _ := strField(m, "server")
	server = jsx.Trim(server)
	bootstrap, _ := strField(m, "bootstrap")
	bootstrap = jsx.Trim(bootstrap)

	if proto == "tls" {
		host := server
		if host == "" {
			host = url
		}
		if host == "" {
			if bootstrap != "" {
				return "nodeDns.server is required when bootstrap is set"
			}
			return ""
		}
		if utf16Len(host) > 2048 {
			return "nodeDns.server is too long"
		}
		// 允许裸 host 或 tls://host[:port]
		if strings.Contains(host, "://") {
			u, ok := urlx.Parse(host)
			if !ok || u.Hostname == "" {
				return "nodeDns.server must be a DoT host"
			}
			host = strings.TrimPrefix(strings.TrimSuffix(u.Hostname, "]"), "[")
			if u.Port != "" {
				if n, err := strconv.Atoi(u.Port); err != nil || n < 1 || n > 65535 {
					return "nodeDns.server must be a DoT host"
				}
			}
		}
		// 裸 host 不能带端口、路径等(端口走 port 字段或 tls://host:port);IPv6 字面量含 ":" 但 isIP 放行
		if host == "" || (!isIP(host) && strings.ContainsAny(host, " /\\:?#@")) {
			return "nodeDns.server must be a DoT host"
		}
		if bootstrap != "" && !isIP(bootstrap) {
			return "nodeDns.bootstrap must be an IP address"
		}
		if bootstrap == "" && !isIP(host) {
			return "nodeDns.bootstrap is required when the DoT address is a domain"
		}
		return ""
	}

	// https / DoH
	if url == "" {
		url = server
	}
	if url == "" {
		if bootstrap != "" {
			return "nodeDns.url is required when bootstrap is set"
		}
		return ""
	}
	if utf16Len(url) > 2048 {
		return "nodeDns.url is too long"
	}
	parsed, ok := parseDohURL(url)
	if !ok {
		return "nodeDns.url must be an https:// DoH address"
	}
	if bootstrap != "" && !isIP(bootstrap) {
		return "nodeDns.bootstrap must be an IP address"
	}
	if bootstrap == "" && !isIP(parsed.host) {
		return "nodeDns.bootstrap is required when the DoH address is a domain"
	}
	return ""
}

// NodeDNSConf 是存下来的样子:没配就是 nil。
type NodeDNSConf struct {
	Protocol  string // https | tls
	URL       string // DoH 完整 URL(https)
	Server    string // DoT 主机或 DoH 解析出的 host
	Port      int    // 0 表示默认
	Path      string // DoH path
	Bootstrap string
}

func NormalizeNodeDNS(raw any) *NodeDNSConf {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	if ValidateNodeDNS(raw) != "" {
		return nil
	}
	proto, _ := strField(m, "protocol")
	proto = strings.ToLower(jsx.Trim(proto))
	if proto == "" {
		proto = "https"
	}
	url, _ := strField(m, "url")
	url = jsx.Trim(url)
	server, _ := strField(m, "server")
	server = jsx.Trim(server)
	bootstrap, _ := strField(m, "bootstrap")
	bootstrap = jsx.Trim(bootstrap)

	if proto == "tls" {
		host := server
		if host == "" {
			host = url
		}
		if host == "" {
			return nil
		}
		port := 853
		if strings.Contains(host, "://") {
			u, ok := urlx.Parse(host)
			if !ok {
				return nil
			}
			host = strings.TrimPrefix(strings.TrimSuffix(u.Hostname, "]"), "[")
			if u.Port != "" {
				if n, err := strconv.Atoi(u.Port); err == nil {
					port = n
				}
			}
		}
		if v, ok := m["port"].(float64); ok {
			if int(v) >= 1 && int(v) <= 65535 && v == float64(int(v)) {
				port = int(v)
			}
		}
		return &NodeDNSConf{Protocol: "tls", Server: host, Port: port, Bootstrap: bootstrap}
	}

	if url == "" {
		url = server
	}
	if url == "" {
		return nil
	}
	parsed, ok := parseDohURL(url)
	if !ok {
		return nil
	}
	return &NodeDNSConf{Protocol: "https", URL: url, Server: parsed.host, Port: parsed.port, Path: parsed.path, Bootstrap: bootstrap}
}

// safeID:非 [A-Za-z0-9_-] 的字符(按 UTF-16 码元,和 JS 的不带 u 标志的正则一致)换成 _,最长 40。
func safeID(id any) string {
	s := ""
	if jsx.Truthy(id) {
		s = jsx.ToString(id)
	}
	var b strings.Builder
	n := 0
	for _, c := range utf16.Encode([]rune(s)) {
		if n == 40 {
			break
		}
		if c < 0x80 && (c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			b.WriteByte(byte(c))
		} else {
			b.WriteByte('_')
		}
		n++
	}
	return b.String()
}

// NodeDNSServers:一份 nodeDns → 内核的 DNS 服务器(DoH 或 DoT 一台,地址是域名时再加一台 udp bootstrap)。tag 按订阅 id 取,稳定。
func NodeDNSServers(nodeDns any, id any) (tag string, servers []map[string]any, ok bool) {
	conf := NormalizeNodeDNS(nodeDns)
	if conf == nil {
		return "", nil, false
	}
	tag = NodeDNSTagPrefix + safeID(id)
	typ, defaultPort := "https", 443
	if conf.Protocol == "tls" {
		typ, defaultPort = "tls", 853
	}
	entry := map[string]any{"type": typ, "tag": tag, "server": conf.Server}
	if conf.Port != defaultPort {
		entry["server_port"] = conf.Port
	}
	if typ == "https" && conf.Path != "" && conf.Path != "/dns-query" {
		entry["path"] = conf.Path
	}
	if !isIP(conf.Server) {
		entry["domain_resolver"] = tag + "-bootstrap"
		return tag, []map[string]any{entry, {"type": "udp", "tag": tag + "-bootstrap", "server": conf.Bootstrap}}, true
	}
	return tag, []map[string]any{entry}, true
}

// NodeDNSPlan 是 planNodeDns 的结果:Servers 是全部要加的 DNS 服务器,BySubscription 是 订阅 id → DoH 的 tag
// (JS 的 Map:按插入顺序,同 id 再写入只更新值、不换位置)。
type NodeDNSPlan struct {
	Servers        []map[string]any
	bySubscription []subTag
}

type subTag struct {
	id  any
	tag string
}

// sameValueZero 只覆盖订阅 id 会出现的标量(字符串 / 数字 / 布尔 / null / undefined)。
func sameValueZero(a, b any) bool {
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	}
	return jsx.IsUndefined(a) && jsx.IsUndefined(b)
}

// Lookup 相当于 bySubscription.get(id);没有返回空串。
func (p NodeDNSPlan) Lookup(id any) string {
	for _, e := range p.bySubscription {
		if sameValueZero(e.id, id) {
			return e.tag
		}
	}
	return ""
}

// Entries 按插入顺序给出 [id, tag](undefined 的 id 序列化成 null,和 JSON.stringify 一致)。
func (p NodeDNSPlan) Entries() [][2]any {
	out := make([][2]any, 0, len(p.bySubscription))
	for _, e := range p.bySubscription {
		id := e.id
		if jsx.IsUndefined(id) {
			id = nil
		}
		out = append(out, [2]any{id, e.tag})
	}
	return out
}

// PlanNodeDNS:哪些订阅配了专用解析器。subscriptions 不是数组按没有处理;停用的订阅跳过。
func PlanNodeDNS(subscriptions any) NodeDNSPlan {
	plan := NodeDNSPlan{Servers: []map[string]any{}}
	list, _ := subscriptions.([]any)
	for _, sub := range list {
		if !jsx.Truthy(sub) || jsx.Get(sub, "enabled") == false {
			continue
		}
		id := jsx.Get(sub, "id")
		tag, servers, ok := NodeDNSServers(jsx.Get(sub, "nodeDns"), id)
		if !ok {
			continue
		}
		plan.Servers = append(plan.Servers, servers...)
		replaced := false
		for i := range plan.bySubscription {
			if sameValueZero(plan.bySubscription[i].id, id) {
				plan.bySubscription[i].tag = tag
				replaced = true
				break
			}
		}
		if !replaced {
			plan.bySubscription = append(plan.bySubscription, subTag{id, tag})
		}
	}
	return plan
}

// WithNodeResolver:出站的服务器是域名才需要解析器;是 IP 的不带,免得内核对着一个用不上的解析器报警告。
// 不改原对象,需要加时返回一份拷贝。
func WithNodeResolver(outbound map[string]any, tag string) map[string]any {
	server, _ := outbound["server"].(string)
	if tag == "" || outbound == nil || server == "" || isIP(server) {
		return outbound
	}
	out := make(map[string]any, len(outbound)+1)
	for k, v := range outbound {
		out[k] = v
	}
	out["domain_resolver"] = tag
	return out
}
