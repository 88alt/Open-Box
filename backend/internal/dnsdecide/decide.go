// Package dnsdecide 按一份已生成的 sing-box 配置里的 DNS 规则,预测「这个域名的 A 查询会命中哪条规则、
// 问哪台服务器」,给 route-test 响应里的 dns 用。
// 自己的设计,依据前端 api/openbox.ts 的 OpenboxRouteTest.dns / OpenboxDnsAssumption 类型和注释,
// 以及 sing-box 官方文档对 DNS 规则、DNS 服务器和规则集的说明(同一条规则里不同条件是「且」,同一个条件的多个值是「或」,
// 规则按顺序取第一条命中的;没有命中走 dns.final),没有参照上游。
//
// 能判断的条件:domain / domain_suffix / domain_keyword / domain_regex、rule_set、query_type、ip_version,
// 以及 logical(and / or)和 invert。判断不了的条件(终端来源、入站、clash_mode、解析后的 IP 等)不猜:
// 如果一条规则其余条件都满足、只差这些条件,就记进 Assumed(needs 写明缺什么),预测时按「没命中」往下走,
// 前端据此告诉用户这一步有前提。规则集读不出来也同样记成前提。
//
// 本包只做纯逻辑,不读文件、不联网:规则集是否包含某个域名由调用方给的 Sets 回答。
package dnsdecide

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ErrUnknownSet 表示这个规则集此刻判断不了(文件没有、解码失败、含有只看域名判断不了的规则等)。
var ErrUnknownSet = errors.New("规则集判断不了")

// Sets 回答「这个规则集包含不包含这个域名」。判断不了返回 ErrUnknownSet(或包着它的错误)。
type Sets interface {
	Match(ctx context.Context, tag, domain string) (bool, error)
}

// Server 是预测命中的 DNS 服务器,字段对应前端 server。
type Server struct {
	Tag    string `json:"tag"`
	Type   string `json:"type,omitempty"`
	Server string `json:"server,omitempty"`
	Detour string `json:"detour,omitempty"`
}

// Assumption 对应前端 OpenboxDnsAssumption:一条「其余条件满足、只差判断不了的条件」的规则。
type Assumption struct {
	RuleIndex    int      `json:"ruleIndex"`
	Needs        []string `json:"needs"`
	SourceIPCIDR []string `json:"sourceIpCidr"`
	SourceMAC    []string `json:"sourceMac,omitempty"`
	Server       string   `json:"server,omitempty"`
	Action       string   `json:"action,omitempty"`
	SameOutcome  *bool    `json:"sameOutcome,omitempty"`
}

// Decision 是预测结果。RuleIndex 为 nil 表示没有规则命中、走了 dns.final。
type Decision struct {
	RuleIndex  *int         `json:"ruleIndex"`
	Rejected   bool         `json:"rejected,omitempty"`
	Server     *Server      `json:"server,omitempty"`
	ViaProxy   *bool        `json:"viaProxy,omitempty"`
	FakeIPRule *int         `json:"fakeIpRule,omitempty"`
	Assumed    []Assumption `json:"assumed,omitempty"`
	// RuntimeChain / RuntimeLeaf:服务器走代理(detour)时,内核此刻实际的线路——detour 的站点集 → 节点组 → 节点,
	// 末尾是 RuntimeLeaf。预测本身不填,调用方读了内核的代理列表后补上。
	RuntimeLeaf  string   `json:"runtimeLeaf,omitempty"`
	RuntimeChain []string `json:"runtimeChain,omitempty"`
}

// View 是从配置里取出来的、判断要用的部分。
type View struct {
	Rules   []any
	Servers map[string]Server
	First   string // 第一台服务器的 tag(没写 final 时用它)
	Final   string
	Types   map[string]string // 出站 tag → 类型,用来判断 detour 是不是直连
}

// FromConfig 从完整的 sing-box 配置(已解码成 map)里取出 DNS 部分。没有 dns 段返回 error。
func FromConfig(cfg map[string]any) (*View, error) {
	dns, ok := cfg["dns"].(map[string]any)
	if !ok {
		return nil, errors.New("当前配置里没有 dns 段")
	}
	v := &View{Servers: map[string]Server{}, Types: map[string]string{}}
	v.Rules, _ = dns["rules"].([]any)
	v.Final, _ = dns["final"].(string)
	servers, _ := dns["servers"].([]any)
	for _, e := range servers {
		m, _ := e.(map[string]any)
		tag, _ := m["tag"].(string)
		if tag == "" {
			continue
		}
		s := Server{Tag: tag}
		s.Type, _ = m["type"].(string)
		s.Server, _ = m["server"].(string)
		s.Detour, _ = m["detour"].(string)
		v.Servers[tag] = s
		if v.First == "" {
			v.First = tag
		}
	}
	if len(v.Servers) == 0 {
		return nil, errors.New("当前配置里没有 DNS 服务器")
	}
	outs, _ := cfg["outbounds"].([]any)
	for _, e := range outs {
		m, _ := e.(map[string]any)
		tag, _ := m["tag"].(string)
		typ, _ := m["type"].(string)
		if tag != "" {
			v.Types[tag] = typ
		}
	}
	return v, nil
}

type tri int

const (
	no tri = iota
	yes
	unknown
)

// 规则里和动作有关、不属于匹配条件的键。
var actionKeys = map[string]bool{
	"action": true, "server": true, "strategy": true, "disable_cache": true, "rewrite_ttl": true,
	"client_subnet": true, "rcode": true, "answer": true, "ns": true, "extra": true, "method": true,
	"no_drop": true, "timeout": true, "invert": true, "type": true, "mode": true, "rules": true,
	"disable_optimistic_cache": true,
}

var sourceKeys = map[string]bool{
	"source_ip_cidr": true, "source_ip_is_private": true, "source_mac_address": true,
	"source_port": true, "source_port_range": true,
}

type evalCtx struct {
	ctx     context.Context
	sets    Sets
	domain  string
	qtype   string
	needs   []string // 这条规则判断不了的条件
	srcCIDR []string
	srcMAC  []string
}

func (e *evalCtx) need(n string) {
	for _, x := range e.needs {
		if x == n {
			return
		}
	}
	e.needs = append(e.needs, n)
}

// Decide 预测 domain 的 qtype(如 "A")查询的走向。domain 会转小写并去掉末尾的点。
func Decide(ctx context.Context, v *View, sets Sets, domain, qtype string) *Decision {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if qtype == "" {
		qtype = "A"
	}
	d := &Decision{}
	var assumed []Assumption
	var fakeIdx *int
	finish := func(idx *int, srv string, rejected bool) *Decision {
		d.RuleIndex, d.Rejected, d.FakeIPRule = idx, rejected, fakeIdx
		if srv != "" {
			s := v.Servers[srv]
			if s.Tag == "" {
				s = Server{Tag: srv}
			}
			d.Server = &s
			via := s.Detour != "" && v.Types[s.Detour] != "direct"
			d.ViaProxy = &via
		}
		for i := range assumed { // 这条前提和最终结果是不是一回事,前端用它判断有没有影响
			same := assumed[i].Server != "" && assumed[i].Server == srv && !rejected
			if assumed[i].Action == "reject" || assumed[i].Action == "predefined" {
				same = rejected && assumed[i].Action == "reject"
			}
			assumed[i].SameOutcome = &same
		}
		d.Assumed = assumed
		return d
	}
	for i, r := range v.Rules {
		rule, _ := r.(map[string]any)
		if rule == nil {
			continue
		}
		ec := &evalCtx{ctx: ctx, sets: sets, domain: domain, qtype: qtype}
		st := ec.evalRule(rule)
		if st == unknown {
			a := Assumption{RuleIndex: i, Needs: ec.needs, SourceIPCIDR: nz(ec.srcCIDR), SourceMAC: ec.srcMAC}
			a.Action, _ = rule["action"].(string)
			a.Server, _ = rule["server"].(string)
			if a.Action == "" && a.Server != "" {
				a.Action = "route"
			}
			assumed = append(assumed, a)
			continue
		}
		if st != yes {
			continue
		}
		action, _ := rule["action"].(string)
		srv, _ := rule["server"].(string)
		switch {
		case action == "reject":
			idx := i
			return finish(&idx, "", true)
		case action == "predefined":
			idx := i
			rc, _ := rule["rcode"].(string)
			return finish(&idx, "", rc != "" && rc != "NOERROR" && rule["answer"] == nil)
		case action == "" || action == "route":
			if srv == "" {
				continue
			}
			if v.Servers[srv].Type == "fakeip" { // 内核自己的 fake-ip 规则先命中:A / AAAA 拿占位地址,再往下找其它查询类型用的真解析器
				if fakeIdx == nil {
					idx := i
					fakeIdx = &idx
				}
				continue
			}
			idx := i
			return finish(&idx, srv, false)
		case action == "route-options":
			continue // 只改选项,不决定去哪
		default: // 不认识的动作:不猜,记成前提
			assumed = append(assumed, Assumption{RuleIndex: i, Needs: []string{"action:" + action}, SourceIPCIDR: []string{}, Action: action})
		}
	}
	final := v.Final
	if final == "" {
		final = v.First
	}
	return finish(nil, final, false)
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (e *evalCtx) evalRule(rule map[string]any) tri {
	var res tri
	if t, _ := rule["type"].(string); t == "logical" {
		res = e.evalLogical(rule)
	} else {
		res = e.evalDefault(rule)
	}
	if inv, _ := rule["invert"].(bool); inv {
		switch res {
		case yes:
			return no
		case no:
			return yes
		}
	}
	return res
}

func (e *evalCtx) evalLogical(rule map[string]any) tri {
	subs, _ := rule["rules"].([]any)
	and := rule["mode"] != "or"
	out := no
	if and {
		out = yes
	}
	for _, s := range subs {
		m, _ := s.(map[string]any)
		if m == nil {
			continue
		}
		r := e.evalRule(m)
		if and {
			if r == no {
				return no
			}
			if r == unknown {
				out = unknown
			}
		} else {
			if r == yes {
				return yes
			}
			if r == unknown {
				out = unknown
			}
		}
	}
	return out
}

func (e *evalCtx) evalDefault(rule map[string]any) tri {
	res := yes
	and := func(r tri) {
		if r == no {
			res = no
		} else if r == unknown && res == yes {
			res = unknown
		}
	}
	// 先算能判断的条件,有一个不满足就不用管判断不了的了
	var unk []string
	for k, val := range rule {
		if actionKeys[k] || empty(val) {
			continue
		}
		switch k {
		case "query_type":
			and(boolTri(matchQType(val, e.qtype)))
		case "ip_version":
			and(boolTri(matchIPVersion(val, e.qtype)))
		case "domain", "domain_suffix", "domain_keyword", "domain_regex":
			// 同一条规则里这四个是同一组,任意一个命中即可,下面统一算
		case "rule_set":
			and(e.evalRuleSets(val))
		default:
			unk = append(unk, k)
		}
	}
	if hasAny(rule, "domain", "domain_suffix", "domain_keyword", "domain_regex") {
		and(boolTri(matchAddr(rule, e.domain)))
	}
	if res == no {
		return no
	}
	sort.Strings(unk) // map 遍历顺序不定,按名字排,结果才稳定
	for _, k := range unk {
		e.need(k)
		if sourceKeys[k] {
			switch k {
			case "source_ip_cidr":
				e.srcCIDR = append(e.srcCIDR, strs(rule[k])...)
			case "source_mac_address":
				e.srcMAC = append(e.srcMAC, strs(rule[k])...)
			}
		}
	}
	if len(unk) > 0 && res == yes {
		return unknown
	}
	return res
}

func (e *evalCtx) evalRuleSets(val any) tri {
	out := no
	for _, tag := range strs(val) {
		m, err := e.sets.Match(e.ctx, tag, e.domain)
		switch {
		case err != nil:
			e.need("rule_set:" + tag)
			out = maxTri(out, unknown)
		case m:
			return yes
		}
	}
	return out
}

func maxTri(a, b tri) tri {
	if a == yes || b == yes {
		return yes
	}
	if a == unknown || b == unknown {
		return unknown
	}
	return no
}

func boolTri(b bool) tri {
	if b {
		return yes
	}
	return no
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	}
	return false
}

func hasAny(rule map[string]any, keys ...string) bool {
	for _, k := range keys {
		if !empty(rule[k]) {
			return true
		}
	}
	return false
}

func strs(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			switch s := e.(type) {
			case string:
				out = append(out, s)
			case float64:
				out = append(out, strconv.FormatFloat(s, 'f', -1, 64))
			}
		}
		return out
	}
	return nil
}

var qtypeNumbers = map[string]string{"A": "1", "NS": "2", "CNAME": "5", "SOA": "6", "PTR": "12", "MX": "15", "TXT": "16", "AAAA": "28", "SRV": "33", "SVCB": "64", "HTTPS": "65"}

func matchQType(val any, qtype string) bool {
	num := qtypeNumbers[qtype]
	for _, s := range strs(val) {
		if strings.EqualFold(s, qtype) || (num != "" && s == num) {
			return true
		}
	}
	return false
}

func matchIPVersion(val any, qtype string) bool {
	want := "4"
	if qtype == "AAAA" {
		want = "6"
	}
	for _, s := range strs(val) {
		if s == want {
			return true
		}
	}
	return false
}

// matchAddr 是 domain / domain_suffix / domain_keyword / domain_regex 这一组:任意一个命中即可。
// 后缀:域名本身或它的子域;以 . 开头的后缀只匹配子域。
func matchAddr(rule map[string]any, d string) bool {
	for _, s := range strs(rule["domain"]) {
		if strings.EqualFold(s, d) {
			return true
		}
	}
	for _, s := range strs(rule["domain_suffix"]) {
		if SuffixMatch(s, d) {
			return true
		}
	}
	for _, s := range strs(rule["domain_keyword"]) {
		if s != "" && strings.Contains(d, strings.ToLower(s)) {
			return true
		}
	}
	for _, s := range strs(rule["domain_regex"]) {
		if re, err := regexp.Compile(s); err == nil && re.MatchString(d) {
			return true
		}
	}
	return false
}

// SuffixMatch 按 sing-box 的 domain_suffix 语义判断:suffix 为 example.com 时匹配 example.com 和它的子域,
// 为 .example.com 时只匹配子域。大小写不分。
func SuffixMatch(suffix, d string) bool {
	suffix = strings.ToLower(suffix)
	if suffix == "" {
		return false
	}
	if strings.HasPrefix(suffix, ".") {
		return strings.HasSuffix(d, suffix)
	}
	return d == suffix || strings.HasSuffix(d, "."+suffix)
}
