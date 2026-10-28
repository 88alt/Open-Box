package system

// 「已知终端」:局域网里 DHCP 租出去的设备(IP、主机名、MAC),给「终端分流」选来源用。
// 只读;不碰 dnsmasq 或任何设置。
//
// 租约文件是 dnsmasq 写的,每行一条:
//
//	<到期时间(秒级时间戳,0 表示永不过期)> <MAC> <IP> <主机名,没有是 *> <客户端 ID,没有是 *>
//
// IPv6 的租约行(第二列是 IAID 而不是 MAC、第三列是 IPv6 地址)和 `duid` 行不是终端的 IPv4 来源,一律跳过。
// 终端分流按 IPv4 来源匹配,所以这里只收 IPv4。
//
// 文件位置依据 OpenWrt 与 dnsmasq 的公开文档,没有在真机上见过,需真机确认(见 STATUS.md)。

import (
	"bufio"
	"io"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DHCPLeaseFiles 是候选的租约文件,存在的都读,同一个 IP 以到期更晚的那条为准:
//   - /tmp/dhcp.leases:OpenWrt 的 dnsmasq 默认位置;
//   - /var/lib/misc/dnsmasq.leases:Debian 的 dnsmasq 包默认位置;
//   - /var/lib/dnsmasq/dnsmasq.leases:部分发行版 / 容器里的位置。
var DHCPLeaseFiles = []string{
	"/tmp/dhcp.leases",
	"/var/lib/misc/dnsmasq.leases",
	"/var/lib/dnsmasq/dnsmasq.leases",
}

// maxLeaseFile 是单个租约文件读取的上限,超出的部分忽略(租约文件通常只有几 KB)。
const maxLeaseFile = 1 << 20

// Client 是一台已知终端。
type Client struct {
	IP   string `json:"ip"`
	Name string `json:"name"`
	MAC  string `json:"mac,omitempty"`
}

// ParseDHCPLeases 解析一份租约文件的内容,now 用来排除已过期的租约。
// 返回的 expiry 与 Client 一一对应(0 表示永不过期),供多份文件合并时比较;不去重、不排序。
func ParseDHCPLeases(r io.Reader, now time.Time) ([]Client, []int64) {
	var (
		out []Client
		exp []int64
	)
	sc := bufio.NewScanner(io.LimitReader(r, maxLeaseFile))
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[0] == "duid" {
			continue
		}
		expiry, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil || expiry < 0 {
			continue
		}
		if expiry != 0 && expiry <= now.Unix() {
			continue
		}
		hw, err := net.ParseMAC(f[1])
		if err != nil || len(hw) != 6 {
			continue
		}
		ip, err := netip.ParseAddr(f[2])
		if err != nil || !ip.Is4() {
			continue
		}
		name := f[3]
		if name == "*" {
			name = ""
		}
		out = append(out, Client{IP: ip.String(), Name: name, MAC: strings.ToLower(hw.String())})
		exp = append(exp, expiry)
	}
	return out, exp
}

// ReadDHCPLeases 依次读 files 里存在的文件,合并成一份:同一个 IP 留到期更晚的那条(0 = 永不过期最晚),
// 按 IPv4 地址从小到大排序。读不到、没有租约都返回空切片(不是 nil,方便直接序列化成 [])。
func ReadDHCPLeases(files []string, now time.Time) []Client {
	type item struct {
		c   Client
		exp int64
	}
	byIP := map[string]item{}
	later := func(a, b int64) bool { // a 是否比 b 到期更晚
		if a == 0 {
			return b != 0
		}
		return b != 0 && a > b
	}
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		cs, es := ParseDHCPLeases(f, now)
		f.Close()
		for i, c := range cs {
			if old, ok := byIP[c.IP]; !ok || later(es[i], old.exp) {
				byIP[c.IP] = item{c, es[i]}
			}
		}
	}
	out := make([]Client, 0, len(byIP))
	for _, it := range byIP {
		out = append(out, it.c)
	}
	sort.Slice(out, func(i, j int) bool {
		return netip.MustParseAddr(out[i].IP).Less(netip.MustParseAddr(out[j].IP))
	})
	return out
}
