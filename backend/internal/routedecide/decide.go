// Package routedecide 按一份已生成的 sing-box 配置里的路由规则(route.rules),预测「访问这个域名或 IP」
// 会命中哪条规则、去哪个出站,给 /api/openbox/penetration 用。
// 自己的设计,依据前端 api/openbox.ts 的 OpenboxPenetrationResult / OpenboxRuleAssumption 类型和注释,
// 以及 sing-box 官方文档对路由规则和规则集的说明(同一条规则里不同条件是「且」,同一个条件的多个值是「或」,
// 规则按顺序取第一条命中的,没有命中走 route.final),没有参照上游。
//
// 两种目标:
//   - 域名:连接带域名,按域名条件判断;对 IP 条件(ip_cidr、ip_is_private、规则集里的 ip_cidr)判断不了——
//     这次没有解析出 IP——记成「前提」(needs 含 destIp),预测按「不满足它」往下走;
//   - IP:连接没有域名,域名条件恒不命中;ip_cidr、ip_is_private、规则集里的 ip_cidr 都能判断。
//
// 域名目标可以带一个 DestIP:终端自己解析出域名后按这个真实地址连进内核,内核再嗅探出域名,所以连接同时带域名和目的 IP
// (core 生成的「看 IP 的规则」都带「连接没有域名」的前提,有域名的连接不会被它们命中;私网直连、节点服务器直连、
// 内核占位段这类不带这个前提的规则则按目的 IP 判)。带了 DestIP,IP 条件(ip_cidr、ip_is_private、ip_version、
// 规则集里的 IP 条目)按它判并在命中时记下(Decision.ViaIP);没带(没解析到、只拿到 fake-ip 占位地址)仍记成前提。
//
// 判断不了的条件不猜:来源(source_ip_cidr / source_mac_address)、端口和网络类型(port / port_range / network)、
// 地址族(域名目标的 ip_version)这些前端预留了「前提」的位置,记进 Assumed(needs 取 sourceIp / sourceMac / port / destIp / ipVersion);
// 规则集读不出来、或里面有这次判断不了的规则,没法确定这条规则命不命中,整个预测停下,MatchError 写原因,
// 不给「没命中」的假结论(前端要求把「没能检查」和「确实没命中」分开显示)。
//
// 动作:sniff / hijack-dns / bypass / route-options / resolve 不是「去哪个出站」的决定,跳过(resolve 记 PreResolve);
// reject 和带 outbound 的规则是决定。入站、协议、clash_mode 这类描述连接来历的条件,生成的用户规则里没有,
// 带着它们的规则一律按不命中处理。
//
// 本包只做纯逻辑,不读文件、不联网:规则集是否包含由调用方给的 Sets 回答。
package routedecide

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
)

// Target 是要查的目标:Domain 和 IP 二选一。
type Target struct {
	Domain string
	IP     netip.Addr
	// DestIP 只给域名目标用:终端按解析结果连的那个真实地址;没解析到、或拿到的是占位地址(fake-ip)时留空。
	DestIP netip.Addr
}

// IsIP 目标是不是 IP 字面量。
func (t Target) IsIP() bool { return t.IP.IsValid() }

// Sets 回答规则集是否包含这个域名 / IP。判断不了返回 dnsdecide.ErrUnknownSet(规则集本身读得出来、
// 只是里面有这次判断不了的规则时,还要带上 dnsdecide.ErrSetPartial)。
type Sets interface {
	Match(ctx context.Context, tag, domain string) (bool, error)
	MatchIP(ctx context.Context, tag string, ip netip.Addr) (bool, error)
}

// View 是从配置里取出来的、判断要用的部分。
type View struct {
	Rules []any
	Final string
	Types map[string]string // 出站 tag → 类型
}

// FromConfig 从完整的 sing-box 配置(已解码成 map)里取出路由部分。没有 route 段返回 error。
func FromConfig(cfg map[string]any) (*View, error) {
	route, ok := cfg["route"].(map[string]any)
	if !ok {
		return nil, errors.New("当前配置里没有 route 段")
	}
	v := &View{Types: map[string]string{}}
	v.Rules, _ = route["rules"].([]any)
	v.Final, _ = route["final"].(string)
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

// 前端 OpenboxRuleAssumption.needs 的取值。
const (
	NeedSourceIP  = "sourceIp"
	NeedSourceMAC = "sourceMac"
	NeedPort      = "port"
	NeedDestIP    = "destIp"
	NeedIPVersion = "ipVersion"
)

var needOrder = []string{NeedSourceIP, NeedSourceMAC, NeedPort, NeedDestIP, NeedIPVersion}

// Assumption 对应前端 OpenboxRuleAssumption:一条「其余条件满足、只差判断不了的条件」的规则。
// Leaf / SameOutcome 要知道内核此刻的线路才算得出,由调用方填。
type Assumption struct {
	Index       int            `json:"index"`
	Rule        map[string]any `json:"rule"`
	Needs       []string       `json:"needs"`
	Outbound    string         `json:"outbound,omitempty"`
	Action      string         `json:"action,omitempty"`
	Leaf        string         `json:"leaf,omitempty"`
	SameOutcome *bool          `json:"sameOutcome,omitempty"`
}

// Decision 是预测结果。
type Decision struct {
	// Index 是命中的规则在 route.rules 里的下标;nil = 没有规则命中、走了 route.final(此时 Rule 为空)。
	Index *int
	Rule  map[string]any
	// Outbound 是决定的出站;Action 为 "reject" 时没有出站。
	Outbound string
	Action   string
	Assumed  []Assumption
	// PreResolve:规则表里有 resolve 动作(以域名进内核的连接先解析成 IP 再判 IP 规则)。
	PreResolve bool
	// ViaIP 是命中这条规则靠的目的地址:域名目标带了 DestIP、并且是 IP 条件命中了才有,否则无效。
	ViaIP netip.Addr
	// MatchError 非空:有规则的规则集没能检查,预测停在那里,不给结论(Index / Outbound 都没有意义)。
	MatchError string
}

type tri int

const (
	no tri = iota
	yes
	unknown // 缺一个条件(记进 needs)
	hard    // 规则集没能检查(MatchError)
)

type evalCtx struct {
	ctx   context.Context
	sets  Sets
	t     Target
	needs map[string]bool
	err   string // 第一个没能检查的原因
	via   bool   // 有 IP 条件靠 DestIP 命中过
}

// dest 返回 IP 条件该用的目的地址:IP 目标是它自己,域名目标是 DestIP(可能无效)。
func (e *evalCtx) dest() netip.Addr {
	if e.t.IsIP() {
		return e.t.IP.Unmap()
	}
	return e.t.DestIP.Unmap()
}

// viaDest 在判断是靠域名目标的 DestIP 做出的时候记一笔。
func (e *evalCtx) viaDest(r tri) tri {
	if r == yes && !e.t.IsIP() {
		e.via = true
	}
	return r
}

func (e *evalCtx) need(n string) { e.needs[n] = true }

func (e *evalCtx) sortedNeeds() []string {
	out := []string{}
	for _, n := range needOrder {
		if e.needs[n] {
			out = append(out, n)
		}
	}
	return out
}

// 规则里不属于匹配条件的键(动作、选项、结构)。
var skipKeys = map[string]bool{
	"type": true, "mode": true, "rules": true, "invert": true,
	"action": true, "outbound": true, "override_address": true, "override_port": true,
	"sniffer": true, "timeout": true, "strategy": true, "method": true, "no_drop": true,
	"network_strategy": true, "fallback_delay": true, "udp_disable_domain_unmapping": true,
	"udp_connect": true, "udp_timeout": true, "tls_fragment": true, "tls_record_fragment": true,
}

// Decide 预测 t 的走向。
func Decide(ctx context.Context, v *View, sets Sets, t Target) *Decision {
	if !t.IsIP() {
		t.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(t.Domain), "."))
	}
	d := &Decision{}
	for i, r := range v.Rules {
		rule, _ := r.(map[string]any)
		if rule == nil {
			continue
		}
		action, _ := rule["action"].(string)
		switch action {
		case "resolve":
			d.PreResolve = true
			continue
		case "sniff", "hijack-dns", "bypass", "route-options":
			continue
		case "", "route", "reject":
		default:
			continue // 不认识的动作:不是「去哪个出站」的决定,不猜
		}
		outbound, _ := rule["outbound"].(string)
		if action != "reject" && outbound == "" {
			continue
		}
		ec := &evalCtx{ctx: ctx, sets: sets, t: t, needs: map[string]bool{}}
		switch ec.evalRule(rule) {
		case hard:
			d.MatchError = ec.err
			return d
		case unknown:
			a := Assumption{Index: i, Rule: rule, Needs: ec.sortedNeeds(), Outbound: outbound}
			a.Action = action
			if a.Action == "" {
				a.Action = "route"
			}
			d.Assumed = append(d.Assumed, a)
		case yes:
			idx := i
			d.Index, d.Rule = &idx, rule
			if ec.via {
				d.ViaIP = t.DestIP.Unmap()
			}
			if action == "reject" {
				d.Action = "reject"
			} else {
				d.Outbound, d.Action = outbound, "route"
			}
			return d
		}
	}
	d.Outbound = v.Final
	return d
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
	var anyUnknown, anyHard bool
	for _, s := range subs {
		m, _ := s.(map[string]any)
		if m == nil {
			continue
		}
		switch e.evalRule(m) {
		case no:
			if and {
				return no
			}
		case yes:
			if !and {
				return yes
			}
		case unknown:
			anyUnknown = true
		case hard:
			anyHard = true
		}
	}
	switch {
	case anyHard:
		return hard
	case anyUnknown:
		return unknown
	case and:
		return yes
	}
	return no
}

func (e *evalCtx) evalDefault(rule map[string]any) tri {
	var anyNo, anyUnknown, anyHard bool
	add := func(r tri) {
		switch r {
		case no:
			anyNo = true
		case unknown:
			anyUnknown = true
		case hard:
			anyHard = true
		}
	}
	keys := make([]string, 0, len(rule))
	for k := range rule {
		keys = append(keys, k)
	}
	sort.Strings(keys) // map 遍历顺序不定,按名字排,记下的前提和原因才稳定
	addrDone := false
	for _, k := range keys {
		val := rule[k]
		if skipKeys[k] || empty(val) {
			continue
		}
		switch k {
		case "domain", "domain_suffix", "domain_keyword", "domain_regex":
			if !addrDone { // 这四个是同一组,任意一个命中即可
				addrDone = true
				add(boolTri(!e.t.IsIP() && dnsdecide.MatchAddr(rule, e.t.Domain)))
			}
		case "ip_cidr":
			add(e.evalCIDR(val))
		case "ip_is_private":
			want, _ := val.(bool)
			if a := e.dest(); a.IsValid() {
				add(e.viaDest(boolTri(nonPublic(a) == want)))
			} else {
				e.need(NeedDestIP)
				add(unknown)
			}
		case "ip_version":
			if a := e.dest(); a.IsValid() {
				add(e.viaDest(boolTri(matchIPVersion(val, a))))
			} else {
				e.need(NeedIPVersion)
				add(unknown)
			}
		case "rule_set":
			add(e.evalRuleSets(val))
		case "source_ip_cidr", "source_ip_is_private":
			e.need(NeedSourceIP)
			add(unknown)
		case "source_mac_address":
			e.need(NeedSourceMAC)
			add(unknown)
		case "port", "port_range", "source_port", "source_port_range", "network":
			e.need(NeedPort)
			add(unknown)
		default:
			add(no) // 入站、协议、clash_mode 等描述连接来历的条件:见包注释
		}
	}
	switch {
	case anyNo: // 有一个不满足就不用管判断不了的了
		return no
	case anyHard:
		return hard
	case anyUnknown:
		return unknown
	}
	return yes
}

func (e *evalCtx) evalCIDR(val any) tri {
	ip := e.dest()
	if !ip.IsValid() {
		e.need(NeedDestIP)
		return unknown
	}
	for _, s := range strs(val) {
		if pf, err := parseCIDROrAddr(s); err == nil && pf.Contains(ip) {
			return e.viaDest(yes)
		}
	}
	return no
}

func (e *evalCtx) evalRuleSets(val any) tri {
	out := no
	for _, tag := range strs(val) {
		var m bool
		var err error
		if e.t.IsIP() {
			m, err = e.sets.MatchIP(e.ctx, tag, e.t.IP)
		} else {
			m, err = e.sets.Match(e.ctx, tag, e.t.Domain)
			if !m && e.t.DestIP.IsValid() && (err == nil || errors.Is(err, dnsdecide.ErrSetPartial)) {
				// 域名没中(规则集里只能按 IP 判的那部分也读得出来):再按终端会连的那个地址比一遍,判法和 IP 目标一样
				// (读不出来 / 判断不了就是没能检查)
				if m, err = e.sets.MatchIP(e.ctx, tag, e.t.DestIP.Unmap()); m {
					e.viaDest(yes)
				}
			}
		}
		switch {
		case m:
			return yes
		case err == nil:
		case !e.t.IsIP() && !e.t.DestIP.IsValid() && errors.Is(err, dnsdecide.ErrSetPartial):
			// 规则集读得出来,但里面有只能按 IP 判的规则:缺的是目标 IP
			e.need(NeedDestIP)
			if out == no {
				out = unknown
			}
		default:
			if e.err == "" {
				e.err = errText(tag, err)
			}
			out = hard
		}
	}
	return out
}

func errText(tag string, err error) string {
	msg := err.Error()
	if strings.Contains(msg, tag) {
		return msg
	}
	return "规则集 " + tag + ":" + msg
}

func boolTri(b bool) tri {
	if b {
		return yes
	}
	return no
}

// nonPublic 对应 ip_is_private:私有、回环、链路本地、组播、未指定地址都算。
func nonPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

func matchIPVersion(val any, ip netip.Addr) bool {
	want := 4
	if !ip.Unmap().Is4() {
		want = 6
	}
	var items []any
	switch x := val.(type) {
	case []any:
		items = x
	default:
		items = []any{x}
	}
	for _, it := range items {
		switch n := it.(type) {
		case float64:
			if int(n) == want {
				return true
			}
		case int:
			if n == want {
				return true
			}
		case string:
			if n == fmt.Sprint(want) {
				return true
			}
		}
	}
	return false
}

func parseCIDROrAddr(s string) (netip.Prefix, error) {
	if pf, err := netip.ParsePrefix(s); err == nil {
		return pf.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
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

func strs(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
