package mihomo

import (
	"net/netip"
	"strconv"
	"strings"
)

// 入口旁路的 nft 规则(双内核 3e-4c;对应上游 v0.1.462 Node 的 mu,落盘到 etc/mihomo-entry-bypass.nft)。
//
// 生成的是一份完整的 nft 片段(不含 table 声明,table 由服务脚本建):若干 set(direct_ip4 / direct_ip6 /
// fallback_keep4 / direct_answered4 / direct_answered6)与四个链:
//   - entry_bypass_in(prerouting):给「不该进内核」的终端、直连 IP 段、绕过端口打上 0x02000000 记号,
//     已打记号(0x2023)的包直接返回;
//   - entry_bypass_out(output):路由器自己发出的绕过端口流量同样打记号;
//   - tcp_mss_out(output):按 tcpMss 钳制 MSS;
//   - terminal_dns(nat prerouting)+ terminal_dns_forward / egress:把终端的 DNS 改道到 TUN DNS 或本机端口,
//     并从别的出口出去的 53 端口一律丢弃(防泄露)。
//
// 口径与上游逐字一致,包括一处上游自己的边角:只开白名单终端 DNS 端口、又没有 TUN DNS 地址时,
// 普通终端的改道目标会写成 `redirect to :0`(上游 `L` 这一支没看 dnsPort);照抄,免得两边行为不同。

const (
	nftMark       = "0x02000000"
	nftTCPMssMark = "0x2024"
	nftTunMark    = "0x2023"

	// nftMarkRule 是上游 gt:同时给 socket 与连接跟踪打记号。
	nftMarkRule = "meta mark set meta mark or " + nftMark + " ct mark set ct mark or " + nftMark
	// nftLocalDst 是上游 nA:目标地址是本机的流量(终端问路由器自己的 DNS)。
	nftLocalDst = "fib daddr type local"
	// tunDevice 是 TUN 设备名(上游 Nt)。
	tunDevice = "openbox-tun"
	// nftDNSProto 是 53 端口的匹配串。
	nftDNSProto = "meta l4proto { tcp, udp } th dport 53"

	nftDirectAnswered4 = "direct_answered4"
	nftDirectAnswered6 = "direct_answered6"
)

// TerminalSet 是一组终端(上游 {ips, macs, localIps, localMacs, ipv6Macs})。
// EntryLists / AdmitLists / BypassLists 产出它,BuildEntryNft 消费它。
type TerminalSet struct {
	IPs       []string `json:"ips"`
	Macs      []string `json:"macs"`
	LocalIPs  []string `json:"localIps,omitempty"`
	LocalMacs []string `json:"localMacs,omitempty"`
	IPv6Macs  []string `json:"ipv6Macs,omitempty"`
}

// TunDNS 是 TUN 的 DNS 地址(上游 Qa,默认 198.18.0.2 / fdfe:dcba:9876::2)。
type TunDNS struct {
	V4 string `json:"v4"`
	V6 string `json:"v6"`
}

// FakeIPCidrs 是假 IP 池(上游 fakeIpCidrs:{v4,v6})。
type FakeIPCidrs struct {
	V4 []string `json:"v4"`
	V6 []string `json:"v6"`
}

// EntryNftInput 是 BuildEntryNft 的入参(字段名同上游 mu 的解构参数)。
type EntryNftInput struct {
	Ports            string // 绕过端口;PortsMode 是 "whitelist" 时是白名单
	PortsMode        string // blacklist / whitelist
	ClientRoutes     any    // 原始档案行(ClientRoutes 只从这里取「不进内核 / 只决定谁进内核」的名单)
	AutoRedirect     bool   // 不是纯 TUN
	PureTun          bool
	LANIfaces        []string // 局域网口(不合规的名字会被丢掉)
	DirectAnswered   bool     // direct_answered 两个 set 是否存在(要不要写依赖它们的规则)
	DirectIPCidrs    any      // 直连 IP 段:数组,或 {v4,v6};只在 AutoRedirect 下用
	FallbackKeepIPv4 []string // 兜底直连要保留的 IPv4 网段(上游 Y.fallbackKeepIpv4)
	TCPMss           int      // 0 = 不钳制
	TerminalDNS      TerminalSet
	DNSPort          int  // TUN DNS 的端口(0 = 没有)
	DNSV6            bool // IPv6 也交给节点(mu 里的 f)
	FakeIPCidrs      FakeIPCidrs
	AdmitDNSPort     int // 白名单终端的单独 DNS 端口(0 = 不开)
	ProxyTerminals   TerminalSet
	DNSUpstreamIPs   []string // 路由器自己的上游 DNS(只取 IP)
	TunDNSAddresses  TunDNS
	ProtectTunDNS    bool
}

// BuildEntryNft 对应上游 mu,返回 nft 片段(行之间 "\n",没有结尾换行)。
func BuildEntryNft(in EntryNftInput) string {
	var b, s, e, n, q []string // 上游 b / S / E / N / Q
	auto, pure := in.AutoRedirect, in.PureTun

	// 路由器自己的 DNS 查询从本机发出,要打上记号免得被当成终端流量。
	if auto {
		v4, v6 := splitFamily(upstreamIPList(in.DNSUpstreamIPs))
		if len(v4) > 0 {
			s = append(s, "meta skuid 0 ip daddr "+braceList(v4)+" "+nftDNSProto+" "+nftMarkRule)
		}
		if len(v6) > 0 {
			s = append(s, "meta skuid 0 ip6 daddr "+braceList(v6)+" "+nftDNSProto+" "+nftMarkRule)
		}
	}

	// 已由直连应答过的主机(上游 directAnswered):非 53 的一律打记号。
	if in.DirectAnswered {
		for _, item := range [][2]string{{"ip", nftDirectAnswered4}, {"ip6", nftDirectAnswered6}} {
			b = append(b,
				item[0]+" daddr @"+item[1]+" meta l4proto != { tcp, udp } "+nftMarkRule,
				item[0]+" daddr @"+item[1]+" meta l4proto { tcp, udp } th dport != 53 "+nftMarkRule)
		}
	}

	ports := NormalizePorts(in.Ports)
	lan := validLANIfaces(in.LANIfaces)
	// 兜底直连的网段要在局域网口上排除,缺一样就不写。
	fallback := auto && len(lan) > 0 && len(in.FallbackKeepIPv4) > 0
	var directRaw []string
	if auto {
		directRaw = flattenCIDRs(in.DirectIPCidrs)
	}
	d4, d6 := splitFamily(directRaw)

	var sets, directRules []string
	if len(d4) > 0 {
		sets = append(sets, "\tset direct_ip4 {", "\t\ttype ipv4_addr; flags interval;",
			"\t\telements = "+braceList(d4), "\t}")
	}
	if len(d6) > 0 {
		sets = append(sets, "\tset direct_ip6 {", "\t\ttype ipv6_addr; flags interval;",
			"\t\telements = "+braceList(d6), "\t}")
	}
	for _, item := range [][2]any{{"ip", d4}, {"ip6", d6}} {
		fam, list := item[0].(string), item[1].([]string)
		if len(list) == 0 {
			continue
		}
		name := "direct_ip4"
		if fam == "ip6" {
			name = "direct_ip6"
		}
		directRules = append(directRules,
			fam+" daddr @"+name+" meta l4proto != { tcp, udp } "+nftMarkRule,
			fam+" daddr @"+name+" meta l4proto { tcp, udp } th dport != 53 "+nftMarkRule)
	}

	// 白名单终端:先返回,后面的记号规则不再作用于它们。
	var macReturn []string
	if auto {
		v4, v6 := splitFamily(in.ProxyTerminals.IPs)
		iface := ""
		if len(lan) > 0 {
			iface = "iifname " + braceList(quoteAll(lan)) + " "
		}
		if len(v4) > 0 {
			macReturn = append(macReturn, iface+"ip saddr "+braceList(v4)+" return")
		}
		if len(v6) > 0 {
			macReturn = append(macReturn, iface+"ip6 saddr "+braceList(v6)+" return")
		}
		for _, mac := range normalizeMACs(in.ProxyTerminals.Macs) {
			macReturn = append(macReturn, iface+"ether saddr "+mac+" return")
		}
		for _, mac := range normalizeMACs(in.ProxyTerminals.IPv6Macs) {
			macReturn = append(macReturn, iface+"meta nfproto ipv6 ether saddr "+mac+" return")
		}
	}

	if fallback {
		head := "iifname " + braceList(quoteAll(lan)) +
			" ct state new fib daddr type unicast ip daddr != @fallback_keep4"
		b = append(b,
			head+" meta l4proto != { tcp, udp } "+nftMarkRule,
			head+" meta l4proto { tcp, udp } th dport != 53 "+nftMarkRule)
	}

	// 假 IP 池:终端流量不打记号(它的目的地是内核自己造的地址)。
	notV4, notV6 := "", ""
	if len(in.FakeIPCidrs.V4) > 0 {
		notV4 = " ip daddr != " + braceList(in.FakeIPCidrs.V4)
	}
	if len(in.FakeIPCidrs.V6) > 0 {
		notV6 = " ip6 daddr != " + braceList(in.FakeIPCidrs.V6)
	}
	nfprotos := []string{""}
	if notV4 != "" || notV6 != "" {
		nfprotos = []string{"meta nfproto ipv4" + notV4 + " ", "meta nfproto ipv6" + notV6 + " "}
	}

	switch {
	case ports != "" && in.PortsMode == "whitelist":
		// 白名单:局域网口上除了这些端口都打记号。
		if len(lan) > 0 {
			head := "iifname " + braceList(quoteAll(lan))
			for _, nf := range nfprotos {
				b = append(b, head+" "+nf+"meta l4proto { tcp, udp } th dport != "+braceList([]string{ports})+
					" th sport != "+braceList([]string{ports})+" "+nftMarkRule)
			}
		}
	case ports != "":
		// 黑名单:这些端口(去程与回程)打记号,本机发出的同样打。
		for _, dir := range []string{"dport", "sport"} {
			for _, nf := range nfprotos {
				rule := nf + "meta l4proto { tcp, udp } th " + dir + " " + braceList([]string{ports}) + " " + nftMarkRule
				b = append(b, rule)
				s = append(s, rule)
			}
		}
	}

	// 纯 TUN:没有自动重定向,靠连接跟踪的应答方向打记号。
	if pure && !auto {
		b = append(b, "ct direction reply meta mark set meta mark or "+nftMark)
		s = append(s, "ct direction reply meta mark set meta mark or "+nftMark)
	}

	// 不进内核的终端(上游 bypass)。
	if auto {
		bypass := BypassLists(in.ClientRoutes)
		v4, v6 := splitFamily(bypass.IPs)
		if len(v4) > 0 {
			b = append(b, "ip saddr "+braceList(v4)+notV4+" "+nftMarkRule)
		}
		if len(v6) > 0 {
			b = append(b, "ip6 saddr "+braceList(v6)+notV6+" "+nftMarkRule)
		}
		for _, mac := range normalizeMACs(bypass.Macs) {
			if notV4 != "" || notV6 != "" {
				b = append(b,
					"ether saddr "+mac+" meta nfproto ipv4"+notV4+" "+nftMarkRule,
					"ether saddr "+mac+" meta nfproto ipv6"+notV6+" "+nftMarkRule)
			} else {
				b = append(b, "ether saddr "+mac+" "+nftMarkRule)
			}
		}
	}

	// 终端 DNS 改道。
	if (auto || pure) && (in.TunDNSAddresses.V4 != "" || in.TunDNSAddresses.V6 != "" ||
		in.DNSPort > 0 || in.AdmitDNSPort > 0) {
		hasTunDNS := in.TunDNSAddresses.V4 != "" || in.TunDNSAddresses.V6 != ""
		redirectTo := func(port int) string { return nftDNSProto + " redirect to :" + strconv.Itoa(port) }
		// dnat 对应上游 M:有 TUN DNS 地址就 dnat 过去,没配就退回 redirect。
		dnat := func(fam string) string {
			addr, suffix := in.TunDNSAddresses.V4, ""
			if fam == "v6" {
				addr, suffix = in.TunDNSAddresses.V6, "6"
			}
			if addr == "" {
				return redirectTo(in.DNSPort)
			}
			return nftDNSProto + " dnat ip" + suffix + " to " + addr
		}
		// addIP 对应上游 K。
		addIP := func(fam string, list []string, prefix string) {
			if len(list) == 0 {
				return
			}
			head := "ip"
			if fam == "v6" {
				head = "ip6"
			}
			head += " saddr " + braceList(list) + " " + prefix
			if (fam == "v6" && in.TunDNSAddresses.V6 != "") || (fam == "v4" && in.TunDNSAddresses.V4 != "") {
				e = append(e, head+nftDNSProto+" return")
			}
			n = append(n, head+dnat(fam))
		}
		// addMAC 对应上游 B:port>0 时优先改到那个端口,否则按 TUN DNS 地址分协议族改。
		addMAC := func(macs []string, prefix string, forceV6 bool, port int) {
			for _, mac := range macs {
				z := "ether saddr " + mac + " "
				if port > 0 {
					c := redirectTo(port)
					if forceV6 {
						n = append(n, "meta nfproto ipv6 "+z+c)
					} else {
						v4 := ""
						if !in.DNSV6 {
							v4 = "meta nfproto ipv4 "
						}
						n = append(n, z+v4+prefix+c)
					}
					continue
				}
				if hasTunDNS {
					fams := []string{"v4"}
					if forceV6 {
						fams = []string{"v6"}
					} else if in.DNSV6 {
						fams = append(fams, "v6")
					}
					for _, fam := range fams {
						if (fam == "v6" && in.TunDNSAddresses.V6 == "") || (fam == "v4" && in.TunDNSAddresses.V4 == "") {
							continue
						}
						nf := "ipv4"
						if fam == "v6" {
							nf = "ipv6"
						}
						head := z + "meta nfproto " + nf + " " + prefix
						e = append(e, head+nftDNSProto+" return")
						n = append(n, head+dnat(fam))
					}
					continue
				}
				if forceV6 {
					n = append(n, "meta nfproto ipv6 "+z+prefix+redirectTo(0))
				} else {
					v4 := ""
					if !in.DNSV6 {
						v4 = "meta nfproto ipv4 "
					}
					n = append(n, z+v4+prefix+redirectTo(0))
				}
			}
		}
		// addTerminals 对应上游 q:一批 IP 与 MAC。
		addTerminals := func(ips, macs []string, prefix string, port int) {
			v4, v6 := splitFamily(ips)
			addIP("v4", v4, prefix)
			if in.DNSV6 {
				addIP("v6", v6, prefix)
			}
			addMAC(normalizeMACs(macs), prefix, false, port)
		}
		// 只决定谁进内核的终端单独一个 DNS 端口(上游 V):这些终端不该被改道到内核的 DNS。
		admitPort := 0
		if in.AdmitDNSPort > 0 {
			admitPort = in.AdmitDNSPort
		}
		addTerminals(in.TerminalDNS.IPs, in.TerminalDNS.Macs, "", 0)
		addTerminals(in.TerminalDNS.LocalIPs, in.TerminalDNS.LocalMacs, nftLocalDst+" ", admitPort)
		if in.DNSV6 {
			addMAC(normalizeMACs(in.TerminalDNS.IPv6Macs), "", true, 0)
		}
	}

	// 只决定谁进内核的终端:局域网口上「不在名单里」的打记号(名单里的进内核)。
	if auto {
		admit := AdmitLists(in.ClientRoutes)
		if len(admit.IPs) > 0 && len(lan) > 0 {
			v4, v6 := splitFamily(admit.IPs)
			var notMacs strings.Builder
			for _, mac := range normalizeMACs(admit.Macs) {
				notMacs.WriteString(" ether saddr != " + mac)
			}
			iface := "iifname " + braceList(quoteAll(lan))
			m4 := iface + " meta nfproto ipv4"
			if len(v4) > 0 {
				m4 += " ip saddr != " + braceList(v4)
			}
			m4 += notMacs.String()
			m6 := iface + " meta nfproto ipv6"
			if len(v6) > 0 {
				m6 += " ip6 saddr != " + braceList(v6)
			}
			m6 += notMacs.String()
			b = append(b, m4+notV4+" "+nftMarkRule, m6+notV6+" "+nftMarkRule)
			if in.AdmitDNSPort > 0 {
				rule := nftLocalDst + " " + nftDNSProto + " redirect to :" + strconv.Itoa(in.AdmitDNSPort)
				n = append(n, m4+" "+rule)
				if in.DNSV6 {
					n = append(n, m6+" "+rule)
				}
			}
		}
	}

	// 防泄露:从别的出口出去的 53 端口一律丢弃。
	if auto || pure {
		if in.ProtectTunDNS {
			for _, item := range [][3]any{{"ip", "198.18.0.2", 4}, {"ip", "172.19.0.2", 4}, {"ip6", "fdfe:dcba:9876::2", 6}} {
				q = append(q, dropDNSRule(item[0].(string), item[1].(string)))
			}
		}
		if isAddrFamily(in.TunDNSAddresses.V4, 4) {
			q = append(q, dropDNSRule("ip", in.TunDNSAddresses.V4))
		}
		if in.DNSV6 && isAddrFamily(in.TunDNSAddresses.V6, 6) {
			q = append(q, dropDNSRule("ip6", in.TunDNSAddresses.V6))
		}
		if in.ProtectTunDNS {
			if len(in.FakeIPCidrs.V4) > 0 {
				q = append(q, "oifname != \""+tunDevice+"\" ip daddr 198.18.0.0/15 counter drop")
			}
			if len(in.FakeIPCidrs.V6) > 0 {
				q = append(q, "oifname != \""+tunDevice+"\" ip6 daddr 2001:2::/48 counter drop")
			}
		}
	}

	ctMark := 0
	if auto && (len(directRules) > 0 || in.DirectAnswered) {
		ctMark = 1
	}
	tcpMss := 0
	if in.TCPMss > 0 {
		tcpMss = 1
	}

	var lines []string
	if in.DirectAnswered {
		lines = append(lines,
			"\tset "+nftDirectAnswered4+" {", "\t\ttype ipv4_addr; flags timeout;", "\t}",
			"\tset "+nftDirectAnswered6+" {", "\t\ttype ipv6_addr; flags timeout;", "\t}")
	}
	lines = append(lines, sets...)
	if fallback {
		lines = append(lines,
			"\tset fallback_keep4 {", "\t\ttype ipv4_addr; flags interval;",
			"\t\telements = "+braceList(in.FallbackKeepIPv4), "\t}")
	}
	if len(e) > 0 || len(macReturn) > 0 || len(directRules) > 0 || len(b) > 0 {
		inRules := append([]string{}, e...)
		inRules = append(inRules, macReturn...)
		if ctMark == 1 {
			inRules = append(inRules, "ct mark "+nftTunMark+" return")
		}
		inRules = append(inRules, directRules...)
		inRules = append(inRules, b...)
		lines = append(lines, nftChain("entry_bypass_in", "filter hook prerouting", "mangle - 10", inRules)...)
	}
	if len(s) > 0 {
		lines = append(lines, nftChain("entry_bypass_out", "route hook output", "mangle - 10", s)...)
	}
	if tcpMss == 1 {
		lines = append(lines, nftChain("tcp_mss_out", "filter hook output", "mangle - 10",
			[]string{"meta mark " + nftTCPMssMark + " tcp flags syn tcp option maxseg size set " + strconv.Itoa(in.TCPMss)})...)
	}
	if len(n) > 0 {
		lines = append(lines, nftChain("terminal_dns", "nat hook prerouting", "dstnat - 5", n)...)
	}
	if len(q) > 0 {
		q = dedupeStrings(q)
		lines = append(lines, nftChain("terminal_dns_forward", "filter hook forward", "mangle", q)...)
		lines = append(lines, nftChain("terminal_dns_egress", "filter hook postrouting", "mangle", q)...)
	}
	return strings.Join(lines, "\n")
}

// nftChain 对应上游 j。
func nftChain(name, typ, prio string, rules []string) []string {
	out := []string{"\tchain " + name + " {", "\t\ttype " + typ + " priority " + prio + "; policy accept;"}
	for _, r := range rules {
		out = append(out, "\t\t"+r)
	}
	return append(out, "\t}")
}

func dropDNSRule(fam, addr string) string {
	return "oifname != \"" + tunDevice + "\" " + fam + " daddr " + addr + " " + nftDNSProto + " counter drop"
}

func isAddrFamily(s string, fam int) bool {
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return false
	}
	if fam == 4 {
		return a.Is4()
	}
	return a.Is6()
}

// braceList 对应上游 wt:nft 的集合写法 `{ a, b }`。
func braceList(items []string) string { return "{ " + strings.Join(items, ", ") + " }" }

func quoteAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "\"" + s + "\""
	}
	return out
}

// dedupeStrings 是上游的 [...new Set(x)](保持第一次出现的顺序)。
func dedupeStrings(items []string) []string {
	if len(items) == 0 {
		return []string{}
	}
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, s := range items {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// splitFamily 对应上游 Xs:按地址族把 CIDR 分成 IPv4 / IPv6(认不出的丢掉)。
func splitFamily(list []string) (v4, v6 []string) {
	for _, s := range list {
		host := s
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		a, err := netip.ParseAddr(host)
		if err != nil || a.Zone() != "" {
			continue
		}
		switch {
		case a.Is4():
			v4 = append(v4, s)
		case a.Is6():
			v6 = append(v6, s)
		}
	}
	return v4, v6
}

// normalizeMACs 是上游 (macs||[]).map(rr).filter(Boolean)。
func normalizeMACs(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if m := normalizeMAC(s); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// upstreamIPList 是上游 [...new Set(h)].filter(isIP)。
func upstreamIPList(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range dedupeStrings(list) {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// validLANIfaces 是上游 [...new Set(s)].filter(jI.test)。
func validLANIfaces(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range dedupeStrings(list) {
		if lanIfaceName.MatchString(s) {
			out = append(out, s)
		}
	}
	return out
}

// flattenCIDRs 对应上游 `Array.isArray(a) ? a : [...a.v4||[], ...a.v6||[]]`。
func flattenCIDRs(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case map[string]any:
		var out []string
		out = append(out, stringsOf(x["v4"])...)
		out = append(out, stringsOf(x["v6"])...)
		return out
	case map[string][]string:
		return append(append([]string{}, x["v4"]...), x["v6"]...)
	}
	return nil
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
