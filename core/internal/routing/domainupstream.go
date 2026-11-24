package routing

import (
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/core/internal/jsx"
)

// 域名指定上游(面板 DNS 页的「域名分流上游」卡,档案 dns.domainUpstreams)。
//
// 形状(取自上游 v0.1.482 面板):{enabled: bool, rules: [{id, enabled, matchType, domains, protocol, server, port, bootstrap?, note?}]}。
// 上游服务端怎么把这份设置落进配置看不到,这里的做法是**推断**:按规则顺序写进 dns.rules,命中域名 / 域名后缀的查询
// 交给规则自己的那台上游(每台被引用到的上游建一个 server,tag 为 dns-du-<序号>),排在 DNS 重写与本地主机名之后、
// 其余分流规则之前——也就是「这一个域名用这台 DNS」压过站点集 / 自定义分流 / 终端分流,但不抢重写和 dnsmasq 的本地名字。
//
// 域名规整与面板同一口径:去首尾空白、转小写、去掉末尾的点;domainSuffix 另外去掉开头的 "*." 或 ".";带 "*" 的、
// 规整后为空的不要。matchType 只认 "domain" 与 "domainSuffix",别的按 domain 处理。
// 上游地址复用 dnsupstream.go 的 upstreamOf / attachBootstrap(协议、端口、DoT / DoH 的 bootstrap 取法全一致);
// 写坏的地址按直连侧的退路处理(退回「上游 DNS」),不让它把整份配置弄坏。
const (
	// MaxDomainUpstreamRules / MaxDomainUpstreamDomains 与面板一致(面板拦得住,这里再兜一层)。
	MaxDomainUpstreamRules   = 200
	MaxDomainUpstreamDomains = 64
)

func domainUpstreamsEnabled(dnsConf any) bool {
	du := jsx.Get(dnsConf, "domainUpstreams")
	if !jsx.Truthy(du) {
		return false
	}
	return jsx.Truthy(jsx.Get(du, "enabled"))
}

// normalizeDomainUpstreamHost:一条域名 → 规整后的写法;不合格返回空串。
func normalizeDomainUpstreamHost(raw any, suffix bool) string {
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	s = strings.ToLower(jsx.Trim(s))
	s = strings.TrimRight(s, ".")
	if suffix {
		if strings.HasPrefix(s, "*.") {
			s = s[2:]
		} else if strings.HasPrefix(s, ".") {
			s = s[1:]
		}
		s = strings.TrimRight(s, ".")
	}
	if s == "" || strings.Contains(s, "*") || len(s) > 253 {
		return ""
	}
	return s
}

// domainUpstreamRules:按档案顺序给出 dns.rules 里那几条,并用 appendEntries 把每台被用到的上游建出来。
func domainUpstreamRules(profile, dnsConf any, systemDNS []any, appendEntries func(up upstream, tag string)) []any {
	if !domainUpstreamsEnabled(dnsConf) {
		return []any{}
	}
	list, ok := jsx.Get(jsx.Get(dnsConf, "domainUpstreams"), "rules").([]any)
	if !ok {
		return []any{}
	}
	if len(list) > MaxDomainUpstreamRules {
		list = list[:MaxDomainUpstreamRules]
	}
	rules := []any{}
	tags := map[string]string{} // 上游指纹 → tag,同一台只建一次
	seq := 0
	for _, item := range list {
		if jsx.Get(item, "enabled") == false {
			continue
		}
		matchType := "domain"
		if mt, isStr := jsx.Get(item, "matchType").(string); isStr && mt == "domainSuffix" {
			matchType = "domainSuffix"
		}
		suffix := matchType == "domainSuffix"
		domains := []string{}
		seen := map[string]bool{}
		raw, _ := jsx.Get(item, "domains").([]any)
		for _, d := range raw {
			h := normalizeDomainUpstreamHost(d, suffix)
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			domains = append(domains, h)
			if len(domains) >= MaxDomainUpstreamDomains {
				break
			}
		}
		if len(domains) == 0 {
			continue
		}
		server, ok := jsx.Get(item, "server").(string)
		if !ok || jsx.Trim(server) == "" {
			continue
		}
		proto, _ := jsx.Get(item, "protocol").(string)
		up := upstreamOf(profile, server, proto, jsx.Get(item, "port"), "udp", WanUpstream, systemDNS)
		up = attachBootstrap(up, jsx.Get(item, "bootstrap"))
		key := up.Protocol + "|" + serverKey(up.Server) + "|" + strconv.Itoa(up.Port) + "|" + up.Path + "|" + serverKey(up.BootServer) + "|" + up.BootProto + "|" + strconv.Itoa(up.BootPort)
		tag, has := tags[key]
		if !has {
			seq++
			tag = "dns-du-" + strconv.Itoa(seq)
			tags[key] = tag
			appendEntries(up, tag)
		}
		cond := map[string]any{"server": tag}
		if suffix {
			cond["domain_suffix"] = domains
		} else {
			cond["domain"] = domains
		}
		rules = append(rules, cond)
	}
	return rules
}
