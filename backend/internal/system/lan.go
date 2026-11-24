package system

import (
	"net"
	"net/netip"
	"sort"
	"strings"
)

// IfaceAddrs 是一块网卡此刻的状态,CollectLAN 的输入(与 net.Interface 解耦,方便测试)。
type IfaceAddrs struct {
	Name     string
	Up       bool
	Loopback bool
	Addrs    []netip.Prefix
}

// CollectLAN 从网卡列表里挑出「家里局域网」的地址和网段,只读。
//
// 规则(自己的设计,依据前端 OpenboxClientAppInfo 里 lanAddresses / lanSubnets 的注释):
//   - 跳过回环、没起来的、tun / wg / 容器网桥(docker*、veth*、br-<容器 id>)这类接口;
//   - kinds 给出接口所属逻辑网络时(OpenWrt 的 ubus,见 ifacekind.go):wan 口整个跳过,
//     lan 口的地址排在前面;不给时按名字和地址判断;
//   - 只收私有地址:IPv4 的 10/8、172.16/12、192.168/16,IPv6 的 fc00::/7(ULA);
//     公网地址、链路本地(fe80::)、运营商级 NAT(100.64/10)都不算「家里」;
//   - addrs:IPv4 在前、IPv6 在后;同一族里 LAN 口的地址排前面,其余保持网卡原有顺序;去重;
//   - subnets:只含 IPv4 网段(规整成网络地址,如 192.168.5.0/24),去重,顺序与 addrs 里的 IPv4 一致。
//
// 没有符合的地址时两个返回值都是空切片(不是 nil),方便直接编成 JSON 的 []。
func CollectLAN(ifs []IfaceAddrs, kinds map[string]string) (addrs, subnets []string) {
	addrs, subnets = []string{}, []string{}
	type item struct {
		ip     string
		subnet string
		v4     bool
		lanIf  bool
		order  int
	}
	var items []item
	seen := map[string]bool{}
	n := 0
	for _, ifc := range ifs {
		if ifc.Loopback || !ifc.Up || skipLANName(ifc.Name) {
			continue
		}
		kind := kinds[ifc.Name]
		if kind == "wan" {
			continue
		}
		lanIf := kind == "lan" || isLANName(ifc.Name)
		for _, p := range ifc.Addrs {
			a := p.Addr().Unmap()
			if !a.IsValid() || !a.IsPrivate() {
				continue
			}
			ip := a.String()
			if seen[ip] {
				continue
			}
			seen[ip] = true
			it := item{ip: ip, v4: a.Is4(), lanIf: lanIf, order: n}
			n++
			if it.v4 {
				bits := p.Bits()
				if p.Addr().Is4In6() {
					bits -= 96
				}
				if bits < 0 || bits > 32 {
					continue
				}
				it.subnet = netip.PrefixFrom(a, bits).Masked().String()
			}
			items = append(items, it)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].v4 != items[j].v4 {
			return items[i].v4
		}
		if items[i].lanIf != items[j].lanIf {
			return items[i].lanIf
		}
		return items[i].order < items[j].order
	})
	subSeen := map[string]bool{}
	for _, it := range items {
		addrs = append(addrs, it.ip)
		if it.subnet != "" && !subSeen[it.subnet] {
			subSeen[it.subnet] = true
			subnets = append(subnets, it.subnet)
		}
	}
	return addrs, subnets
}

// LAN 读本机网卡后交给 CollectLAN。读不到网卡返回空切片,不报错(信息只用于展示和拼 App 码)。
func LAN() (addrs, subnets []string) {
	ifs := realIfaces()
	return CollectLAN(ifs, UbusIfaceKinds(ubusRunner))
}

// LANDevices 从网卡列表里挑出承载家里局域网的接口名(入口 nft 的 iifname 用,对齐上游 di 的 lanIfaces):
// 跳过回环、没起来的、tun / 容器网桥;kinds 说它是 wan 的一律排除,说它是 lan 的(有地址时)直接收,
// 其余按「有没有私有地址」判断。同一个接口只出现一次,LAN 口排前面,其余保持网卡原有顺序。
//
// 上游从「网络清单」里取 kind=lan 的接口:OpenWrt 上就是 netifd 里名字以 lan 开头的逻辑网络
// (kinds 由 UbusIfaceKinds 提供);没有 ubus 时按私有地址判断,与上游 systemd 分支一致。
func LANDevices(ifs []IfaceAddrs, kinds map[string]string) []string {
	type item struct {
		name  string
		lanIf bool
		order int
	}
	var items []item
	seen := map[string]bool{}
	for _, ifc := range ifs {
		if ifc.Loopback || !ifc.Up || skipLANName(ifc.Name) || seen[ifc.Name] {
			continue
		}
		kind := kinds[ifc.Name]
		if kind == "wan" {
			continue
		}
		private := false
		for _, p := range ifc.Addrs {
			if a := p.Addr().Unmap(); a.IsValid() && a.IsPrivate() {
				private = true
				break
			}
		}
		// 上游的清单是从 `ip -o addr` 里来的:没有地址的接口根本不会出现,所以 kind=lan 也要有地址。
		if kind != "lan" && !private {
			continue
		}
		if kind == "lan" && len(ifc.Addrs) == 0 {
			continue
		}
		seen[ifc.Name] = true
		items = append(items, item{name: ifc.Name, lanIf: kind == "lan" || isLANName(ifc.Name), order: len(items)})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].lanIf != items[j].lanIf {
			return items[i].lanIf
		}
		return items[i].order < items[j].order
	})
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.name)
	}
	return out
}

// LANDeviceNames 读本机网卡后交给 LANDevices。读不到网卡返回空切片,不报错。
func LANDeviceNames() []string {
	ifs := realIfaces()
	return LANDevices(ifs, UbusIfaceKinds(ubusRunner))
}

// realIfaces 读本机网卡此刻的状态(读不到返回 nil)。
func realIfaces() []IfaceAddrs {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	in := make([]IfaceAddrs, 0, len(ifs))
	for _, ifc := range ifs {
		ia := IfaceAddrs{Name: ifc.Name, Up: ifc.Flags&net.FlagUp != 0, Loopback: ifc.Flags&net.FlagLoopback != 0}
		as, _ := ifc.Addrs()
		for _, a := range as {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ones, bits := ipn.Mask.Size()
			if bits == 0 {
				continue
			}
			// 有的系统把 IPv4 地址连同 128 位掩码一起给出(IPv4 映射写法):先还原成 IPv4 和 32 位以内的前缀长度
			if ip.Is4In6() {
				ip = ip.Unmap()
				if bits == 128 {
					ones -= 96
				}
			}
			if ones < 0 {
				continue
			}
			ia.Addrs = append(ia.Addrs, netip.PrefixFrom(ip, ones))
		}
		in = append(in, ia)
	}
	return in
}

// skipLANName 是不可能代表「家里局域网」的接口:隧道类(与 isTunName 同一批)和容器 / 虚拟机网桥、
// 虚拟对(名单对齐上游 `vP` 的 xP,另加 br-<12 位容器 id> 这种 docker 自己建的网桥)。
func skipLANName(n string) bool {
	if isTunName(n) {
		return true
	}
	n = strings.ToLower(n)
	for _, p := range []string{"docker", "veth", "virbr", "lxcbr", "lxdbr", "incusbr", "podman", "cni", "tailscale", "zt", "tap"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return strings.HasPrefix(n, "br-") && len(n) == 15 && isHex12(n[3:])
}

// isHex12 是 12 位小写十六进制(docker 网桥 br-<容器网络 id> 的后缀)。
func isHex12(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// isLANName 是 OpenWrt 上 LAN 口的常见名字:br-lan、lan、lan1…。
func isLANName(n string) bool {
	n = strings.ToLower(n)
	return n == "br-lan" || strings.HasPrefix(n, "lan")
}
