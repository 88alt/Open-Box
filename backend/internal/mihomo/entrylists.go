package mihomo

import (
	"regexp"
)

// 入口旁路的终端名单(双内核 3e-4c;对应上游 v0.1.462 Node 的 ol / b0 / sl / w0)。
//
// 入口处要「把哪些终端的流量标上记号、把哪些终端的 DNS 改道」,取决于三件事:
//   - 这条终端分流行在入口处算不算数(ol):由 DNS 模式、自动重定向 / 纯 TUN、是否分 DNS、
//     这条行是「不进内核」还是「走内置直连 / 拒绝 / 自己的出口」共同决定;
//   - 白名单终端(b0):要不要给「只决定谁进内核」的终端在局域网口上单独开一个 DNS 端口;
//   - 名单(sl / w0):按「不进内核 / 只决定谁进内核」把终端拆成 IP 与 MAC 两组,
//     其中「不进内核」的终端走 local*(它们的 DNS 不用改道)。
//
// 上游的 ipv6Only 行(IPv4 的 /32 来源按 ipv6Bindings 映射出来的 MAC 行)在这条链上不会产出
// (见 clientroutes.go 的说明),这里仍然保留这条分支,以免以后补上映射时又要改一遍。

// RouteOpts 是入口旁路判定要的几个选项(上游调用点里的 P)。
type RouteOpts struct {
	DNSMode      string `json:"dnsMode"`      // 档案 dns.mode:hijack / dnsmasq / off
	AutoRedirect bool   `json:"autoRedirect"` // 不是纯 TUN(档案 tun.autoRedirect 不是 false 且 dns.mode 不是 off)
	PureTun      bool   `json:"pureTun"`      // !AutoRedirect
	// DirectBypass 对应档案 directBypass;nil 当作没配(= 真,与上游 `!== false` 同义)。
	DirectBypass *bool  `json:"directBypass"`
	SplitDNS     bool   `json:"splitDns"`  // 档案 dns.split
	DirectTag    string `json:"directTag"` // 内置直连这一组的名字
	BlockTag     string `json:"blockTag"`  // 内置拒绝这一组的名字
}

// directBypassOn 对应上游 `o!==!1`。
func (o RouteOpts) directBypassOn() bool {
	return o.DirectBypass == nil || *o.DirectBypass
}

// EntryRouteActive 对应上游 ol:这条终端分流行在入口处要不要标记号 / 改 DNS。
//
// r 传的是规整过的行(NormalizeClientRoutes 的结果)。
func EntryRouteActive(r ClientRoute, o RouteOpts) bool {
	if !o.AutoRedirect && !o.PureTun {
		return false
	}
	// 出口既不是内置直连也不是内置拒绝(走真正的节点):只有 MAC 认的终端在劫持模式下才算。
	if r.Outbound != "" && r.Outbound != o.DirectTag && r.Outbound != o.BlockTag {
		return o.DNSMode != "hijack" || r.Match == "mac"
	}
	if o.DNSMode != "dnsmasq" && o.DNSMode != "hijack" {
		return false
	}
	// 自己带 DNS 的行,除了出口是拒绝的,一律要改道。
	if r.DNS != nil && r.Outbound != o.BlockTag {
		return true
	}
	if !o.SplitDNS {
		return false
	}
	if r.Bypass {
		return o.AutoRedirect
	}
	if o.DNSMode == "hijack" {
		return r.Match == "mac" && r.Outbound != ""
	}
	if !o.AutoRedirect {
		return false
	}
	return o.directBypassOn() && o.DirectTag != "" && r.Outbound == o.DirectTag
}

// EntryLists 对应上游 sl:按 ol 判定把终端拆成四组名单(去重、保持第一次出现的顺序)。
func EntryLists(routes []ClientRoute, o RouteOpts) TerminalSet {
	out := TerminalSet{IPs: []string{}, Macs: []string{}, LocalIPs: []string{}, LocalMacs: []string{}}
	for _, r := range routes {
		if !EntryRouteActive(r, o) {
			continue
		}
		switch {
		case r.Match == "mac" && r.IPv6Only:
			out.IPv6Macs = append(out.IPv6Macs, r.Macs...)
		case r.Match == "mac":
			if r.Bypass {
				out.LocalMacs = append(out.LocalMacs, r.Macs...)
			} else {
				out.Macs = append(out.Macs, r.Macs...)
			}
		default:
			if r.Bypass {
				out.LocalIPs = append(out.LocalIPs, r.Sources...)
			} else {
				out.IPs = append(out.IPs, r.Sources...)
			}
		}
	}
	out.IPs = dedupeStrings(out.IPs)
	out.Macs = dedupeStrings(out.Macs)
	out.LocalIPs = dedupeStrings(out.LocalIPs)
	out.LocalMacs = dedupeStrings(out.LocalMacs)
	out.IPv6Macs = dedupeStrings(out.IPv6Macs)
	return out
}

// AdmitLists 对应上游 vo:档案里「只决定谁进内核」的终端。
func AdmitLists(routes any) TerminalSet { return listsWithFlag(routes, "admit") }

// BypassLists 对应上游 Qi:档案里「不进内核」的终端。
func BypassLists(routes any) TerminalSet { return listsWithFlag(routes, "bypass") }

// listsWithFlag 对应上游 w0:按某个布尔标记收 IP 与 MAC(原始档案行,不是规整后的行)。
func listsWithFlag(routes any, flag string) TerminalSet {
	var ips, macs []string
	for _, item := range rawArray(routes) {
		m, ok := item.(map[string]any)
		if !ok || m == nil {
			continue
		}
		if b, isBool := m["enabled"].(bool); isBool && !b {
			continue
		}
		if !isTrue(m[flag]) {
			continue
		}
		if matchKind(m) == "mac" {
			macs = append(macs, macsOf(m)...)
		} else {
			ips = append(ips, sourcesOf(m)...)
		}
	}
	return TerminalSet{IPs: dedupeStrings(ips), Macs: dedupeStrings(macs)}
}

// AdmitDNSEnabled 对应上游 b0:要不要给白名单终端在局域网口上开一个单独的 DNS 端口
// (自动重定向 + 分 DNS + dnsmasq / 劫持,且确实有白名单终端)。
func AdmitDNSEnabled(routes any, o RouteOpts) bool {
	if !o.AutoRedirect || !o.SplitDNS || (o.DNSMode != "dnsmasq" && o.DNSMode != "hijack") {
		return false
	}
	l := AdmitLists(routes)
	return len(l.IPs) > 0 || len(l.Macs) > 0
}

var lanIfaceName = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,15}$`)
