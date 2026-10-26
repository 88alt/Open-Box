package system

import (
	"bufio"
	"net"
	"net/netip"
	"os"
	"strings"
)

// ReadResolvers 读 resolv.conf 里的 nameserver,去掉本机回环地址(那是 dnsmasq 或本程序自己,不能当上游)。
// 读不到文件返回空。
func ReadResolvers(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		a, err := netip.ParseAddr(fields[1])
		if err != nil || a.IsLoopback() || a.IsUnspecified() || a.Zone() != "" {
			continue
		}
		s := a.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// LocalSubnets 返回本机局域网网段(私有 IPv4、ULA IPv6),跳过回环和 tun 类接口。
func LocalSubnets() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 || isTunName(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			p, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			p = p.Unmap()
			if !p.IsPrivate() {
				continue
			}
			ones, _ := ipn.Mask.Size()
			if p.Is4() && ones > 32 {
				continue
			}
			s := netip.PrefixFrom(p, ones).Masked().String()
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

func isTunName(n string) bool {
	n = strings.ToLower(n)
	return strings.HasPrefix(n, "tun") || strings.HasPrefix(n, "utun") || strings.HasPrefix(n, "sing") || strings.HasPrefix(n, "wg")
}
