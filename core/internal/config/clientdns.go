package config

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/core/internal/clientroutes"
	"github.com/timeflysoon/Open-Box/core/internal/jsx"
)

// 终端分流 / 链式代理的自定义 DNS(上游 v0.1.473 新增:面板「自定义 DNS」,存成 { url, bootstrap? })。
// url 是 https:// tls:// tcp:// udp:// 地址;域名形式的服务器要有 bootstrap(IP)来解析它。
// 上游核心闭源,这里按面板文案实现:
//   - 终端分流规则配了 dns:这些终端的 DNS 查询走这台服务器;
//   - 链式代理配了 dns:终端分流直选这条链路(出口 = 链路名)、且那条规则自己没配 dns 时用它(终端 DNS 优先)。

const ClientDNSTagPrefix = "dns-client-"

// ClientDNSServers 把一份 { url, bootstrap? } 变成内核的 DNS 服务器(地址是域名时再加一台 udp 引导服务器)。
// 地址不合法(协议不认、没主机、端口越界、域名没引导 DNS)返回 ok=false,调用方当没配。
func ClientDNSServers(raw map[string]any, tag string) (servers []map[string]any, ok bool) {
	rawURL, _ := raw["url"].(string)
	rawURL = jsx.Trim(rawURL)
	bootstrap, _ := raw["bootstrap"].(string)
	bootstrap = jsx.Trim(bootstrap)
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, false
	}
	typ := u.Scheme
	defaultPort := 0
	switch typ {
	case "https":
		defaultPort = 443
	case "tls":
		defaultPort = 853
	case "tcp", "udp":
		defaultPort = 53
	default:
		return nil, false
	}
	host := strings.TrimSuffix(strings.TrimPrefix(u.Hostname(), "["), "]")
	if host == "" {
		return nil, false
	}
	port := defaultPort
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, false
		}
		port = n
	}
	entry := map[string]any{"type": typ, "tag": tag, "server": host}
	if port != defaultPort {
		entry["server_port"] = port
	}
	if typ == "https" {
		path := u.EscapedPath()
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
		if path != "" && path != "/dns-query" {
			entry["path"] = path
		}
	}
	if !isIP(host) {
		if !isIP(bootstrap) {
			return nil, false
		}
		entry["domain_resolver"] = tag + "-bootstrap"
		return []map[string]any{entry, {"type": "udp", "tag": tag + "-bootstrap", "server": bootstrap}}, true
	}
	return []map[string]any{entry}, true
}

// ValidateClientDNS 是保存前的校验:返回错误信息,合法(或没配)返回 ""。
func ValidateClientDNS(raw any) string {
	if raw == nil || jsx.IsUndefined(raw) {
		return ""
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "dns must be an object"
	}
	for _, key := range []string{"url", "bootstrap"} {
		if v, has := m[key]; has {
			if _, isStr := v.(string); !isStr {
				return "dns." + key + " must be a string"
			}
		}
	}
	if u, _ := m["url"].(string); jsx.Trim(u) == "" {
		return ""
	}
	if utf16Len(m["url"].(string)) > 2048 {
		return "dns.url is too long"
	}
	if _, ok := ClientDNSServers(m, "check"); !ok {
		return "dns must be an https:// tls:// tcp:// or udp:// address (a domain needs an IP bootstrap)"
	}
	return ""
}

// PlanClientDNS:给每条配了(或能从链式代理继承到)自定义 DNS 的终端规则分配专用解析器。
// 返回填好 DNSTag 的规则(保持顺序)和要加进 dns.servers 的服务器;地址不合法的规则按没配处理。
func PlanClientDNS(routes []clientroutes.Route, profile any) ([]clientroutes.Route, []map[string]any) {
	chainDNS := map[string]map[string]any{}
	if list, ok := jsx.Get(profile, "chainProxies").([]any); ok {
		for _, c := range list {
			if jsx.Get(c, "enabled") == false {
				continue
			}
			name, _ := jsx.Get(c, "name").(string)
			name = jsx.Trim(name)
			if d := cleanChainDNS(jsx.Get(c, "dns")); name != "" && d != nil {
				chainDNS[name] = d
			}
		}
	}
	out := make([]clientroutes.Route, len(routes))
	servers := []map[string]any{}
	for i, cr := range routes {
		out[i] = cr
		if cr.Bypass {
			continue
		}
		dns := cr.DNS
		if dns == nil {
			dns = chainDNS[cr.Outbound]
		}
		if dns == nil {
			continue
		}
		tag := ClientDNSTagPrefix + strconv.Itoa(i)
		list, ok := ClientDNSServers(dns, tag)
		if !ok {
			continue
		}
		out[i].DNS = dns
		out[i].DNSTag = tag
		servers = append(servers, list...)
	}
	return out, servers
}

func cleanChainDNS(raw any) map[string]any {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	if u, _ := m["url"].(string); jsx.Trim(u) == "" {
		return nil
	}
	return m
}
