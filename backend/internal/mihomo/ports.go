// Package mihomo 是 Mihomo 内核一侧的配置装配(STATUS 双内核第 3e 步,上游 v0.1.462 Node 里的 di 那一套)。
// 这一块(3e-1)只放输入与小工具:端口串解析、嗅探端口、DNS 服务器写成 URL、节点 DNS、绕过端口、
// ipv6 模式、tun 取值、订阅主机、输入指纹。DNS 段、规则集、入口旁路、整份装配见 STATUS 3e-2 ~ 3e-5。
package mihomo

import (
	"sort"
	"strconv"
	"strings"
)

// Ports 是解析后的端口串(与 sing-box 的 port / port_range 同形):单个端口,和 "起:止" 的范围。
type Ports struct {
	Port  []int
	Range []string
}

// ParsePorts 解析 "8443, 9443-9450" 这样的端口串(逗号 / 空白分隔,范围用 -)。
// 空串、任何一项写坏、端口不在 1~65535、起大于止,整体返回 ok=false。起等于止的范围算单个端口。
func ParsePorts(s string) (Ports, bool) {
	items := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v'
	})
	if len(items) == 0 {
		return Ports{}, false
	}
	var p Ports
	for _, it := range items {
		lo, hi, ok := parsePortItem(it)
		if !ok {
			return Ports{}, false
		}
		if lo == hi {
			p.Port = append(p.Port, lo)
		} else {
			p.Range = append(p.Range, strconv.Itoa(lo)+":"+strconv.Itoa(hi))
		}
	}
	return p, true
}

func parsePortItem(it string) (lo, hi int, ok bool) {
	a, b, hasRange := strings.Cut(it, "-")
	lo, ok = digits(a)
	if !ok {
		return 0, 0, false
	}
	hi = lo
	if hasRange {
		if hi, ok = digits(b); !ok {
			return 0, 0, false
		}
	}
	if lo < 1 || lo > 65535 || hi < 1 || hi > 65535 || lo > hi {
		return 0, 0, false
	}
	return lo, hi, true
}

// digits 只认 1~5 位十进制数字(不收正负号、小数、空串)。
func digits(s string) (int, bool) {
	if len(s) == 0 || len(s) > 5 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// NormalizePorts 把端口串规整成 "a, b, x-y" 的样子(单个端口在前、范围在后,保持输入顺序);写坏了回空串。
func NormalizePorts(s string) string {
	p, ok := ParsePorts(s)
	if !ok {
		return ""
	}
	out := make([]string, 0, len(p.Port)+len(p.Range))
	for _, n := range p.Port {
		out = append(out, strconv.Itoa(n))
	}
	for _, r := range p.Range {
		out = append(out, strings.Replace(r, ":", "-", 1))
	}
	return strings.Join(out, ", ")
}

// SniffPorts 是档案 mihomoTlsSniffPorts 落到 Mihomo sniffer 的 TLS 端口列表:去重、去掉 443(Mihomo 的 TLS 嗅探
// 默认就有)、单个端口按数值升序排在前,范围按「起:止」的字典序排在后并写成 "起-止"。值是 int 或 string,
// 直接可以放进 YAML 的 ports 列表。没配或写坏返回空。
func SniffPorts(profile map[string]any) []any {
	raw, _ := profile["mihomoTlsSniffPorts"].(string)
	p, ok := ParsePorts(raw)
	if !ok {
		return nil
	}
	seen := map[int]bool{}
	var ports []int
	for _, n := range p.Port {
		if n != 443 && !seen[n] {
			seen[n] = true
			ports = append(ports, n)
		}
	}
	sort.Ints(ports)
	seenR := map[string]bool{}
	var ranges []string
	for _, r := range p.Range {
		if !seenR[r] {
			seenR[r] = true
			ranges = append(ranges, r)
		}
	}
	sort.Strings(ranges)
	var out []any
	for _, n := range ports {
		out = append(out, n)
	}
	for _, r := range ranges {
		out = append(out, strings.Replace(r, ":", "-", 1))
	}
	return out
}

// BypassPorts 是档案里的「绕过端口」设置:黑名单模式取 bypassPorts,白名单模式取 bypassPortsWhitelist。
type BypassPorts struct {
	Ports string
	Mode  string // "blacklist" | "whitelist"
}

// BypassPortsOf 读档案的绕过端口设置(bypassPortsMode 不是 "whitelist" 一律按黑名单)。
func BypassPortsOf(profile map[string]any) BypassPorts {
	if profile == nil {
		profile = map[string]any{}
	}
	if m, _ := profile["bypassPortsMode"].(string); m == "whitelist" {
		s, _ := profile["bypassPortsWhitelist"].(string)
		return BypassPorts{Ports: s, Mode: "whitelist"}
	}
	s, _ := profile["bypassPorts"].(string)
	return BypassPorts{Ports: s, Mode: "blacklist"}
}
