package routing

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/timeflysoon/Open-Box/core/internal/jsx"
)

// mihomoTargetRules:站点集、前置自定义分流、终端分流、订阅 / 节点直连 → Mihomo 的 rules 字符串。
//
// 调用形状取自上游 v0.1.462 的 Node:入参 {routing, providers, ruleLists, targets, directDomains, directCIDRs,
// terminalRoutes, sections},返回 {rules, firstTerminal, endTerminal, directTerminal, firstPolicy}。
//
// 【推断,待真机核对】规则表各段怎么排没有明文,下面的排法是从 Node 对四个下标的用法反推的:
//
//	[firstTerminal, endTerminal)   「严格」终端分流(出口是节点 / 组,Node 给的 strict=true)——排最前,压过前置自定义
//	[endTerminal, directTerminal)  前置自定义分流(Node 把这一段的域名规则收进终端 DNS 的豁免名单)
//	[directTerminal, firstPolicy)  内网 / NTP / 订阅节点直连 + 其余终端分流(直连、拒绝、不进内核);分流 DNS 规划跳过这一段
//	[firstPolicy, 末尾)            站点集(按用户排的顺序),最后一条 MATCH 是兜底
//
// 与 sing-box 的顺序(前置自定义最前、终端分流在站点集之前)不同。要改排法只改 MihomoTargetRules 末尾的拼接顺序和下标。
// 不在这里做的:IPv6 / QUIC 拒绝(Node 逐条插)、入口 IN-NAME 规则、fake-ip / 流量检测的前置规则。

var escapeRe = regexp.MustCompile("[,()\\x00-\\x1f\\x7f-\\x9f]")

const escapedPrefix = "~openbox~"

// encodeTarget:出口名里有逗号 / 括号 / 控制字符、首尾空白,或本身以 ~openbox~ 开头时,
// 用 ~openbox~ + base64url 写(内核解析规则时会还原),否则原样。
func encodeTarget(name string) string {
	if strings.HasPrefix(name, escapedPrefix) || strings.TrimSpace(name) != name || escapeRe.MatchString(name) {
		return escapedPrefix + base64.RawURLEncoding.EncodeToString([]byte(name))
	}
	return name
}

type terminalRoute struct {
	Match    string   `json:"match"`
	Outbound string   `json:"outbound"`
	Bypass   bool     `json:"bypass"`
	Strict   bool     `json:"strict"`
	Sources  []string `json:"sources"`
	Macs     []string `json:"macs"`
}

// MihomoRulesResult 是 mihomoTargetRules 的返回。
type MihomoRulesResult struct {
	Rules          []string `json:"rules"`
	FirstTerminal  int      `json:"firstTerminal"`
	EndTerminal    int      `json:"endTerminal"`
	DirectTerminal int      `json:"directTerminal"`
	FirstPolicy    int      `json:"firstPolicy"`
}

// MihomoTargetRules 的入参见上。
func MihomoTargetRules(args any) (*MihomoRulesResult, error) {
	conf := normalizeRouting(jsx.Get(args, "routing"))
	lists := parseRuleLists(jsx.Get(args, "ruleLists"))
	kinds, _ := jsx.Get(args, "providers").(map[string]any)
	targets, _ := jsx.Get(args, "targets").(map[string]any)
	targetOf := func(name string) (string, bool) {
		v, ok := targets[name].(string)
		return v, ok
	}
	strs := func(key string) []string {
		out := []string{}
		if l, ok := jsx.Get(args, key).([]any); ok {
			for _, x := range l {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	}
	var terminals []terminalRoute
	if raw := jsx.Get(args, "terminalRoutes"); !jsx.Nullish(raw) {
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &terminals); err != nil {
			return nil, fmt.Errorf("terminalRoutes: %w", err)
		}
	}

	// 一条 sing-box 风格的匹配条件 → 若干条 Mihomo 规则(同一出口,OR 关系)。
	// IP 类一律带 no-resolve:只管已经是 IP 的连接,不为了匹配 IP 规则去解析域名(对应 sing-box 里 ip 规则只看没有域名的连接)。
	convert := func(m map[string]any, target string) ([]string, error) {
		var out []string
		t := encodeTarget(target)
		if tags, ok := m["rule_set"].([]string); ok {
			for _, tag := range tags {
				kind, ok := kinds[tag].(string)
				if !ok {
					return nil, fmt.Errorf("mihomo 不支持规则集 %s", tag)
				}
				if kind == "ipcidr" {
					out = append(out, "RULE-SET,"+tag+","+t+",no-resolve")
				} else {
					out = append(out, "RULE-SET,"+tag+","+t)
				}
			}
		}
		for _, p := range [][2]string{{"domain", "DOMAIN"}, {"domain_suffix", "DOMAIN-SUFFIX"}, {"domain_keyword", "DOMAIN-KEYWORD"}} {
			if l, ok := m[p[0]].([]string); ok {
				for _, v := range l {
					out = append(out, p[1]+","+v+","+t)
				}
			}
		}
		if l, ok := m["ip_cidr"].([]string); ok {
			for _, v := range l {
				kind := "IP-CIDR"
				if strings.Contains(v, ":") {
					kind = "IP-CIDR6"
				}
				out = append(out, kind+","+v+","+t+",no-resolve")
			}
		}
		if l, ok := m["port"].([]int); ok {
			for _, v := range l {
				out = append(out, fmt.Sprintf("DST-PORT,%d,%s", v, t))
			}
		}
		if l, ok := m["port_range"].([]string); ok {
			for _, v := range l {
				out = append(out, "DST-PORT,"+strings.ReplaceAll(v, ":", "-")+","+t)
			}
		}
		return out, nil
	}

	var strict, custom, direct, policies []string

	// 前置自定义分流
	if customPolicyActive(conf.Custom) {
		for _, rule := range conf.Custom.Rules {
			name := rule.Outbound
			target, ok := targetOf(name)
			if !ok {
				continue // 出口不认识:和 sing-box 一样整行跳过
			}
			for _, part := range customRuleParts(rule, lists, name) {
				rs, err := convert(part.rule, target)
				if err != nil {
					return nil, err
				}
				custom = append(custom, rs...)
			}
		}
	}

	// 内网 / NTP / 订阅节点直连
	directTarget, ok := targetOf("direct")
	if !ok {
		directTarget = "DIRECT"
	}
	dt := encodeTarget(directTarget)
	direct = append(direct, "GEOIP,LAN,"+dt+",no-resolve", "AND,((NETWORK,UDP),(DST-PORT,123)),"+dt)
	for _, d := range strs("directDomains") {
		direct = append(direct, "DOMAIN,"+d+","+dt)
	}
	for _, c := range strs("directCIDRs") {
		kind := "IP-CIDR"
		if strings.Contains(c, ":") {
			kind = "IP-CIDR6"
		}
		direct = append(direct, kind+","+c+","+dt+",no-resolve")
	}

	// 终端分流:严格的排最前,其余跟在直连后面
	for _, tr := range terminals {
		target, ok := targetOf(tr.Outbound)
		if tr.Outbound == "" || !ok {
			continue
		}
		t := encodeTarget(target)
		var rs []string
		if tr.Match == "mac" {
			for _, m := range tr.Macs {
				rs = append(rs, "OPENBOX-SRC-MAC,"+m+","+t)
			}
		} else {
			for _, s := range tr.Sources {
				rs = append(rs, "SRC-IP-CIDR,"+s+","+t)
			}
		}
		if tr.Strict {
			strict = append(strict, rs...)
		} else {
			direct = append(direct, rs...)
		}
	}

	// 站点集:每个拆成「看域名」「看 IP」两份,按用户排的顺序
	for _, policy := range conf.ActivePolicies {
		target, ok := targetOf(policy.Name)
		if !ok {
			continue
		}
		domain, ip := policyRouteMatches(policy, lists)
		for _, m := range []map[string]any{domain, ip} {
			if m == nil {
				continue
			}
			rs, err := convert(m, target)
			if err != nil {
				return nil, err
			}
			policies = append(policies, rs...)
		}
	}
	fb, ok := targetOf(conf.Fallback.Name)
	if !ok {
		return nil, fmt.Errorf("fallback %q is not a known outbound", conf.Fallback.Name)
	}
	policies = append(policies, "MATCH,"+encodeTarget(fb))

	res := &MihomoRulesResult{FirstTerminal: 0}
	res.EndTerminal = len(strict)
	res.DirectTerminal = res.EndTerminal + len(custom)
	res.FirstPolicy = res.DirectTerminal + len(direct)
	res.Rules = append(append(append(append([]string{}, strict...), custom...), direct...), policies...)
	return res, nil
}
