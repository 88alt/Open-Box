package system

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 前端 OpenboxTerminalMissing 的取值:虚拟终端测试缺哪一样前提。
const (
	MissingRoot      = "root"
	MissingNetns     = "netns"
	MissingVeth      = "veth"
	MissingLAN       = "lan"
	MissingConntrack = "conntrack"
	MissingDHCP      = "dhcp"
)

// TerminalLAN 对应前端 OpenboxTerminalCapability.lan:虚拟终端要接到的 LAN 口。
type TerminalLAN struct {
	Device  string `json:"device"`
	Address string `json:"address"`
	Mask    *int   `json:"mask"` // IPv4 前缀长度;读不出来是 null
}

// TerminalCapability 对应前端 OpenboxTerminalCapability。Missing 永远是数组(不是 null);LAN 找不到是 null。
type TerminalCapability struct {
	OK      bool         `json:"ok"`
	Missing []string     `json:"missing"`
	LAN     *TerminalLAN `json:"lan"`
}

// PickLANDevice 从网卡列表里挑虚拟终端要接的 LAN 口(只读):跳过回环、没起来的、隧道和容器网桥,
// 只收有私有 IPv4 地址的;名字像 LAN 口(br-lan / lan*)的排在前面,其余保持网卡原有顺序。找不到返回 nil。
// 自己的设计,依据前端 OpenboxTerminalCapability.lan 的字段和 CollectLAN 的同一套筛选。
func PickLANDevice(ifs []IfaceAddrs) *TerminalLAN {
	var best *TerminalLAN
	bestIsLAN := false
	for _, ifc := range ifs {
		if ifc.Loopback || !ifc.Up || skipLANName(ifc.Name) {
			continue
		}
		for _, p := range ifc.Addrs {
			a := p.Addr().Unmap()
			if !a.IsValid() || !a.Is4() || !a.IsPrivate() {
				continue
			}
			bits := p.Bits()
			if p.Addr().Is4In6() {
				bits -= 96
			}
			var mask *int
			if bits >= 0 && bits <= 32 {
				m := bits
				mask = &m
			}
			isLAN := isLANName(ifc.Name)
			if best == nil || (isLAN && !bestIsLAN) {
				best = &TerminalLAN{Device: ifc.Name, Address: a.String(), Mask: mask}
				bestIsLAN = isLAN
			}
			break // 一块网卡只取第一个私有 IPv4
		}
	}
	return best
}

// TerminalProbe 检查这台设备能不能建「模拟 LAN 终端」的虚拟终端(独立网络命名空间 + veth + DHCP 取址 + conntrack 取证)。
// 只读:不建命名空间、不改任何系统状态。所有对系统的访问都是注入的,测试里换假的;零值的字段用真实实现补上(见 Real)。
type TerminalProbe struct {
	Euid     func() int
	Ifaces   func() []IfaceAddrs
	Run      Runner
	LookPath func(string) (string, error)
	Exists   func(path string) bool
	ReadFile func(path string) ([]byte, error)
	Glob     func(pattern string) ([]string, error)
}

// RealTerminalProbe 返回读真实系统的探测器。
func RealTerminalProbe() TerminalProbe {
	return TerminalProbe{
		Euid:     os.Geteuid,
		Ifaces:   realIfaces,
		Run:      ExecRunner,
		LookPath: exec.LookPath,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		ReadFile: os.ReadFile,
		Glob:     filepath.Glob,
	}
}

// dhcpClients 是能给虚拟终端取址的 DHCP 客户端程序名:OpenWrt 自带的 busybox udhcpc,以及 Debian 常见的两种。
var dhcpClients = []string{"udhcpc", "dhclient", "dhcpcd"}

// Capability 逐项检查前提,缺的按前端类型里的顺序(root、netns、veth、lan、conntrack、dhcp)列在 Missing 里。
// 判断不了就当缺(不猜):例如读不到网卡列表按「没有 LAN 口」处理。
func (p TerminalProbe) Capability(ctx context.Context) TerminalCapability {
	out := TerminalCapability{Missing: []string{}}
	has := func(f func(string) (string, error), name string) bool {
		if f == nil {
			return false
		}
		_, err := f(name)
		return err == nil
	}
	exists := func(path string) bool { return p.Exists != nil && p.Exists(path) }

	if p.Euid == nil || p.Euid() != 0 {
		out.Missing = append(out.Missing, MissingRoot)
	}

	hasIP := has(p.LookPath, "ip")
	if !(hasIP && exists("/proc/self/ns/net") && p.ipNetnsWorks(ctx)) {
		out.Missing = append(out.Missing, MissingNetns)
	}
	if !(hasIP && p.vethAvailable()) {
		out.Missing = append(out.Missing, MissingVeth)
	}

	if p.Ifaces != nil {
		out.LAN = PickLANDevice(p.Ifaces())
	}
	if out.LAN == nil {
		out.Missing = append(out.Missing, MissingLAN)
	}

	if !(exists("/proc/net/nf_conntrack") || has(p.LookPath, "conntrack")) {
		out.Missing = append(out.Missing, MissingConntrack)
	}

	dhcp := false
	for _, n := range dhcpClients {
		if has(p.LookPath, n) {
			dhcp = true
			break
		}
	}
	if !dhcp {
		out.Missing = append(out.Missing, MissingDHCP)
	}

	out.OK = len(out.Missing) == 0
	return out
}

// ipNetnsWorks:`ip netns list` 能跑(退出码 0)才说明这个 ip 支持网络命名空间。
// OpenWrt 默认的 busybox ip 不带 netns 子命令,要装 ip-full;读命令,不改任何东西。
func (p TerminalProbe) ipNetnsWorks(ctx context.Context) bool {
	if p.Run == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, err := p.Run(ctx, "ip", "netns", "list")
	return err == nil && r.Code == 0
}

// vethAvailable:内核有 veth 驱动——已加载(/sys/module/veth、/proc/modules)、编进内核(modules.builtin)、
// 或者模块文件在 /lib/modules 里(标准路径 kernel/drivers/net/ 和 OpenWrt 的平铺目录)。
func (p TerminalProbe) vethAvailable() bool {
	if p.Exists != nil && p.Exists("/sys/module/veth") {
		return true
	}
	if p.ReadFile == nil {
		return false
	}
	if b, err := p.ReadFile("/proc/modules"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "veth ") {
				return true
			}
		}
	}
	rel := ""
	if b, err := p.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		rel = strings.TrimSpace(string(b))
	}
	if rel == "" || strings.ContainsAny(rel, "/\\") {
		return false
	}
	base := "/lib/modules/" + rel
	if b, err := p.ReadFile(base + "/modules.builtin"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasSuffix(strings.TrimSpace(line), "/veth.ko") {
				return true
			}
		}
	}
	if p.Glob != nil {
		for _, pat := range []string{base + "/kernel/drivers/net/veth.ko*", base + "/veth.ko*"} {
			if m, err := p.Glob(pat); err == nil && len(m) > 0 {
				return true
			}
		}
	}
	return false
}
