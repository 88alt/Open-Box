package routetest

import "strings"

// noDomainRule 判断这次命中的是不是「只管没有域名的连接」的 IP 规则(前端 exit.noDomain):
// 规则是按 IP 判的(写了 ip_cidr / ip_is_private,没有任何域名类条件),并且这条探测连接没有域名(host 为空)。
// 这样的规则,有域名的访问是不会靠它判的:域名没解析出 IP 之前 IP 规则匹配不上,域名都没命中就走兜底。
//
// 认法依据真实 sing-box 1.14.4 的 Clash API /connections 里 rule 的写法(沙箱里跑真实内核取的样例,rulePayload 为空):
//
//	ip_cidr=127.0.0.3/32 => route(viaip)              普通规则:空格分隔的 key=value,后面跟 => 动作
//	ip_cidr=[127.0.0.3/32 127.0.0.9/32] => route(x)   多个值用方括号,里面有空格
//	ip_cidr=127.0.0.5/32 || domain=z.test => route(x) 逻辑规则:各子规则用 || (任一)或 && (全部)连起来
//	rule_set=ipset => route(x)                        规则集:看不到里面是域名还是 IP,所以不认(不猜)
//
// 认不出的写法(带括号的嵌套逻辑规则、|| 和 && 混用)一律返回 false,宁可不提示也不乱提示。
func noDomainRule(rule, host string) bool {
	if host != "" {
		return false
	}
	desc, _, _ := strings.Cut(rule, " => ")
	desc = strings.TrimSpace(desc)
	if desc == "" || strings.ContainsAny(desc, "()") {
		return false
	}
	hasOr, hasAnd := strings.Contains(desc, " || "), strings.Contains(desc, " && ")
	switch {
	case hasOr && hasAnd:
		return false
	case hasOr:
		// 任一子规则命中就算命中:每个分支都得是纯 IP 规则,否则有域名的访问可以走域名那一支
		for _, part := range strings.Split(desc, " || ") {
			if !ipOnly(ruleKeys(part)) {
				return false
			}
		}
		return true
	case hasAnd:
		return ipOnly(ruleKeys(strings.ReplaceAll(desc, " && ", " ")))
	}
	return ipOnly(ruleKeys(desc))
}

// ipOnly:至少有一个 IP 条件,并且没有域名类条件(规则集内容看不到,当作可能含域名)。
func ipOnly(keys []string) bool {
	ip := false
	for _, k := range keys {
		switch k {
		case "ip_cidr", "ip_is_private":
			ip = true
		case "domain", "domain_suffix", "domain_keyword", "domain_regex", "rule_set", "geosite", "geoip":
			return false
		}
	}
	return ip
}

// ruleKeys 取一段规则描述里每个条件的名字。值可能是 [a b c](里面有空格)。
func ruleKeys(s string) []string {
	var keys []string
	r := []rune(s)
	for i := 0; i < len(r); {
		for i < len(r) && r[i] == ' ' {
			i++
		}
		start := i
		for i < len(r) && r[i] != '=' && r[i] != ' ' {
			i++
		}
		if i > start {
			keys = append(keys, string(r[start:i]))
		}
		if i < len(r) && r[i] == '=' {
			i++
			if i < len(r) && r[i] == '[' {
				for i < len(r) && r[i] != ']' {
					i++
				}
			}
			for i < len(r) && r[i] != ' ' {
				i++
			}
		}
	}
	return keys
}
