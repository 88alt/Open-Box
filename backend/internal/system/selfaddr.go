package system

import (
	"net/netip"
	"strings"
)

// SelfKind 判断一个来源地址是不是路由器自己的,是的话它属于哪类网卡。
//
// 用途:流量统计的「终端」那一维会把路由器自身地址也记进去(内核连接表里,
// 路由器自己发起的连接来源就是它的 LAN / WAN 地址)。前端要把它显示成
// 「本机 · WAN pppoe-wan」这种标注、并且不允许改名,所以这里给出 kind 与网卡名。
//
// 判定顺序(与上游一致):
//  1. 拿本机网卡清单,找到持有这个地址的那块网卡;找不到就不是本机地址;
//  2. 有 ubus 时按逻辑网络名判定:lan* → "lan",wan* / wwan* → "wan";
//  3. 没有 ubus(Debian / 容器)或逻辑名不归类时:私有地址算 "lan",其余算 "wan";
//  4. 隧道类(tun / sing-box 的 openbox-tun 等)一律 "other"——它们既不是 LAN 也不是 WAN。
//
// 第二个返回值是网卡名,读不到时为空。
func SelfKind(addr string) (kind, iface string, ok bool) {
	return selfKindIn(realIfaces(), UbusIfaceKinds(ubusRunner), addr)
}

// selfKindIn 是 SelfKind 的纯函数核心:网卡清单与「设备 → 逻辑网络」都从外面给,
// 这样判定规则本身可以被单测直接钉住。
func selfKindIn(ifaces []IfaceAddrs, kinds map[string]string, addr string) (kind, iface string, ok bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil {
		return "", "", false
	}
	ip = ip.Unmap()
	for _, ia := range ifaces {
		if ia.Loopback || !ia.Up || ip.IsLoopback() {
			continue
		}
		for _, p := range ia.Addrs {
			if p.Addr().Unmap() == ip {
				return kindOfIface(ia.Name, ip, kinds), ia.Name, true
			}
		}
	}
	return "", "", false
}

// kindOfIface 给一块本机网卡定性。ubus 读不到时退回按地址是否私有来分。
func kindOfIface(name string, ip netip.Addr, kinds map[string]string) string {
	if isTunIfaceName(name) {
		return "other"
	}
	if kind := kinds[name]; kind == "lan" || kind == "wan" {
		return kind
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return "lan"
	}
	return "wan"
}

// IsSelfAddr 是 SelfKind 的布尔简写:这个来源地址是不是路由器自己的。
func IsSelfAddr(addr string) bool {
	_, _, ok := SelfKind(addr)
	return ok
}

// isTunIfaceName 比 isTunName 宽一点:除了 tun/sing/wg 开头,
// 还认「名字里带 tun」的(自研内核的 openbox-tun、iproute2 建的 obterm 之类),
// 这些口上的地址既不是家里局域网也不是外网出口。
func isTunIfaceName(n string) bool {
	if isTunName(n) {
		return true
	}
	l := strings.ToLower(n)
	return strings.Contains(l, "tun") || strings.HasPrefix(l, "obterm")
}
