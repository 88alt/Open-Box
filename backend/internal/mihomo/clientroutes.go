package mihomo

import (
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// 终端分流(档案 clientRoutes)的规整(双内核 3e-4b-3;对应上游 v0.1.462 Node 的 nr)。
//
// 入口旁路(bypassPlan / entryPlan)只关心「哪些终端、从哪条出站走、要不要绕开内核」,
// 所以这里按上游的口径把档案里的原始行规整成一份干净的名单:
//   - 停用的(enabled 为 false)、只决定谁进内核的(admit 为 true)丢掉;
//   - 按 IP 认终端(match=ip)收 sources,按 MAC 认终端(match=mac)收 macs;两者都为空的行丢掉;
//   - 不进内核(bypass)的行:出口用内置直连的名字(档案里的 outbound 空着也一样),并且不带自定义 DNS;
//   - 别的行出口为空就丢掉;自定义 DNS 只在能认出来时带上。
//
// 与上游的一处差别:上游还会把「IPv4 的 /32 来源按 ipv6Bindings 映射成 MAC」再补一条 ipv6Only 的行,
// 但 DE 调用时不传 ipv6Bindings(默认 {})、而且那条映射值按 MAC 校验恒为空,上游实际也不产出;这里不移植。

// RouteDNS 是终端分流里一条「自定义 DNS」(档案 clientRoutes[].dns)。
type RouteDNS struct {
	URL       string `json:"url"`
	Bootstrap string `json:"bootstrap,omitempty"`
}

// ClientRoute 是规整后的一条终端分流(字段名同上游 nr 的返回)。
type ClientRoute struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Match    string    `json:"match"` // ip / mac
	Outbound string    `json:"outbound"`
	Bypass   bool      `json:"bypass,omitempty"`
	DNS      *RouteDNS `json:"dns,omitempty"`
	Sources  []string  `json:"sources"` // match=ip 时的 CIDR 名单(空数组,不是 null)
	Macs     []string  `json:"macs,omitempty"`
	// IPv6Only 是上游从 ipv6Bindings 映射出来的 MAC 行(只对 IPv6 生效)。这里不产出,
	// 留字段是为了入口旁路的名单(sl)与上游同形。
	IPv6Only bool `json:"ipv6Only,omitempty"`
	// Strict 是装配时补上的(上游 di 里的 R):不是「不进内核」,且出口不是内置直连 / 拒绝。
	// 只有装配阶段才有意义,档案里没有这个键。
	Strict bool `json:"strict,omitempty"`
}

var (
	macPattern = regexp.MustCompile(`^([0-9a-f]{2}:){5}[0-9a-f]{2}$`)
	allDigits  = regexp.MustCompile(`^[0-9]+$`)
)

// NormalizeClientRoutes 对应上游 nr(只带 directTag;不带 ipv6Bindings / chains)。
// 传进来的不是数组时返回空切片(上游同样回 [])。
func NormalizeClientRoutes(routes any, directTag string) []ClientRoute {
	out := []ClientRoute{}
	var list []any
	switch v := routes.(type) {
	case []any:
		list = v
	case []map[string]any:
		list = make([]any, len(v))
		for i, m := range v {
			list[i] = m
		}
	default:
		return out
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if b, isBool := m["enabled"].(bool); isBool && !b {
			continue
		}
		if b, isBool := m["admit"].(bool); isBool && b {
			continue
		}
		match := matchKind(m)
		bypass := isTrue(m["bypass"])
		var outbound string
		if bypass {
			outbound = strings.TrimSpace(directTag)
			if outbound == "" {
				outbound = strings.TrimSpace(stringOf(m["outbound"]))
			}
			if outbound == "" {
				outbound = "direct"
			}
		} else {
			outbound = strings.TrimSpace(stringOf(m["outbound"]))
		}
		if outbound == "" {
			continue
		}
		r := ClientRoute{
			ID: stringOf(m["id"]), Name: stringOf(m["name"]),
			Match: match, Outbound: outbound, Bypass: bypass, Sources: []string{},
		}
		if !bypass {
			if d, ok := routeDNS(m["dns"]); ok {
				r.DNS = d
			}
		}
		if match == "mac" {
			macs := macsOf(m)
			if len(macs) == 0 {
				continue
			}
			r.Macs = macs
			out = append(out, r)
			continue
		}
		sources := sourcesOf(m)
		if len(sources) == 0 {
			continue
		}
		r.Sources = sources
		out = append(out, r)
	}
	return out
}

// matchKind 对应上游 ks:档案写明了就按写的;没写时,不进内核 / 只决定谁进内核的行带 MAC 名单就算 MAC。
func matchKind(m map[string]any) string {
	if s, _ := m["match"].(string); s == "ip" || s == "mac" {
		return s
	}
	if (isTrue(m["bypass"]) || isTrue(m["admit"])) && len(rawArray(m["macs"])) > 0 {
		return "mac"
	}
	return "ip"
}

// sourcesOf 对应上游 h0:CIDR 规整后去重(保持第一次出现的顺序),认不出的丢掉。
func sourcesOf(m map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range rawArray(m["sources"]) {
		c := normalizeCIDR(stringOf(v))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// macsOf 对应上游 y0:MAC 规整后去重(保持第一次出现的顺序),认不出的丢掉。
func macsOf(m map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range rawArray(m["macs"]) {
		c := normalizeMAC(stringOf(v))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// normalizeCIDR 对应上游 xo:不带掩码的 IPv4 补 /32、IPv6 补 /128;掩码要是不带符号的十进制且在范围内;
// 地址部分原样保留(不重新排版 IPv6 的写法,与上游一致)。
func normalizeCIDR(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "/")
	if len(parts) > 2 {
		return ""
	}
	a, err := netip.ParseAddr(parts[0])
	if err != nil || a.Zone() != "" {
		return ""
	}
	bits := 32
	if a.Is6() {
		bits = 128
	}
	if len(parts) == 1 {
		return parts[0] + "/" + strconv.Itoa(bits)
	}
	if !allDigits.MatchString(parts[1]) {
		return ""
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n > bits {
		return ""
	}
	return parts[0] + "/" + strconv.Itoa(n)
}

// normalizeMAC 对应上游 rr:去空白、小写、连字符换冒号,必须是 6 组两位十六进制。
func normalizeMAC(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "-", ":")
	if !macPattern.MatchString(s) {
		return ""
	}
	return s
}

// routeDNS 对应上游 Kt:取一条自定义 DNS 的 url 与 bootstrap;url 没有、太长、或形状不对就当没有。
// 校验按上游 Br 的要点(DoH / DoT 用域名时必须给 bootstrap;udp / tcp 只认 IP 地址)。
func routeDNS(v any) (*RouteDNS, bool) {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return nil, false
	}
	raw := strings.TrimSpace(stringOf(m["url"]))
	if raw == "" || len(raw) > 2048 {
		return nil, false
	}
	boot := strings.TrimSpace(stringOf(m["bootstrap"]))
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	ip, err := netip.ParseAddr(u.Hostname())
	isIP := err == nil && ip.Zone() == ""
	switch strings.ToLower(u.Scheme) {
	case "udp", "tcp":
		if !isIP || boot != "" {
			return nil, false
		}
	case "tls", "https":
		if !isIP && boot == "" {
			return nil, false
		}
		if boot != "" {
			if _, err := netip.ParseAddr(boot); err != nil {
				return nil, false
			}
		}
	default:
		return nil, false
	}
	d := &RouteDNS{URL: raw}
	if boot != "" {
		d.Bootstrap = boot
	}
	return d, true
}

func rawArray(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

// isTrue 是 JS 的严格 true(档案里写了 true,不是「真值」)。
func isTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// stringOf 是 JS 的 String(x||""):空值给空串,数字 / 布尔按 JS 的写法。
func stringOf(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}
