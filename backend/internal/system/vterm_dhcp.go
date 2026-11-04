package system

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 虚拟终端的 DHCP 取址(terminal-test 的 POST 的一步):在虚拟终端的命名空间里跑一次 DHCP 客户端,
// 像一台刚接上 LAN 的普通设备那样向路由器(或旁路由场景里的真网关)要一个地址,拿到后把地址和默认路由
// 配在命名空间里的那一头上。自己的设计,依据前端 OpenboxTerminalTest.source 的字段(ip / dns / gateway / dhcpMs)
// 和 busybox udhcpc 公开的用法与回调脚本约定,没有参照上游。
//
// 取址客户端只做一次性的(拿到租约就退出,不留守护进程),所以没有要登记的常驻进程;
// 万一卡住,超时会杀掉它,拆除虚拟终端时也会清命名空间里残留的进程(killNamespaceProcs)。
// 地址和路由都配在命名空间里,命名空间一删就全没了,不需要另外的记账,也不碰主命名空间的任何网络配置。
//
// 现在只支持 udhcpc(OpenWrt 自带的 busybox、Debian 的 udhcpc 包)。能力检测里 dhclient / dhcpcd 也算有 DHCP 客户端,
// 但只装了它们的设备会在这里如实报「暂不支持」,不假装成功。

// DHCPLease 是取到的租约(对应前端 source 里的 ip / dns / gateway / dhcpMs)。
type DHCPLease struct {
	IP           string
	Mask         int      // 前缀长度
	Gateway      string   // 为空 = 服务器没给网关
	DNS          []string // 只含合法的 IPv4
	Server       string
	LeaseSeconds int
	Ms           int64 // 从开始取址到拿到租约用了多久
}

const (
	vtermDHCPTimeout = 20 * time.Second
	vtermScriptName  = "obterm-dhcp.sh"
)

// udhcpc 在事件发生时运行这个脚本、把租约放在环境变量里;脚本只把值逐行打到标准输出,
// 由我们解析并自己去配地址(脚本不改任何网络配置)。每行 "OB <键> <值>"。
const vtermDHCPScript = `#!/bin/sh
case "$1" in
bound|renew)
  echo "OB ip $ip"
  echo "OB mask $mask"
  echo "OB subnet $subnet"
  echo "OB router $router"
  echo "OB dns $dns"
  echo "OB serverid $serverid"
  echo "OB lease $lease"
  ;;
esac
exit 0
`

func (n *VTermNet) lookPath(file string) (string, error) {
	if n.LookPath != nil {
		return n.LookPath(file)
	}
	return exec.LookPath(file)
}

// dhcpClient 选客户端:只认 udhcpc。
func (n *VTermNet) dhcpClient() (string, error) {
	if p, err := n.lookPath("udhcpc"); err == nil && p != "" {
		return p, nil
	}
	var other []string
	for _, c := range []string{"dhclient", "dhcpcd"} {
		if p, err := n.lookPath(c); err == nil && p != "" {
			other = append(other, c)
		}
	}
	if len(other) > 0 {
		return "", fmt.Errorf("这台设备只有 %s,虚拟终端取址暂时只支持 udhcpc", strings.Join(other, "、"))
	}
	return "", errors.New("找不到 DHCP 客户端(udhcpc),虚拟终端没法取址")
}

// AcquireDHCP 让 v 这个虚拟终端(必须是本进程 Ensure 出来、仍在用的)取一个地址并配好。
// 沿用的终端(Reused)会先清掉上次的地址和路由,再重新取,保证每次测的都是一次完整的取址。
// 任何一步失败都把命名空间里这一头的地址和路由清掉再返回错误,不留半截配置。
// 取址期间持有锁:拆除会等它做完(最久 vtermDHCPTimeout 加几条 ip 命令)。
func (n *VTermNet) AcquireDHCP(ctx context.Context, v VirtualTerminal) (DHCPLease, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.checkNames(); err != nil {
		return DHCPLease{}, err
	}
	ns, _, peer := n.names()
	if n.cur == nil || n.cur.Namespace != ns || n.cur.PeerIf != peer || v.Namespace != ns || v.PeerIf != peer {
		return DHCPLease{}, errors.New("这不是本程序当前在用的虚拟终端,没有取址")
	}
	client, err := n.dhcpClient()
	if err != nil {
		return DHCPLease{}, err
	}

	// 先清干净这一头上上次留下的,再取。
	if err := n.flushPeer(ctx, ns, peer); err != nil {
		return DHCPLease{}, err
	}

	dir := n.ScriptDir
	if dir == "" {
		dir = os.TempDir()
	}
	script := filepath.Join(dir, vtermScriptName)
	if err := n.writeFile(script, []byte(vtermDHCPScript), 0o700); err != nil {
		return DHCPLease{}, fmt.Errorf("写不了取址回调脚本 %s:%w", script, err)
	}
	defer func() { _ = n.remove(script) }()

	start := n.now()
	r, err := n.runFor(ctx, vtermDHCPTimeout, "ip", "netns", "exec", ns, client,
		"-i", peer, "-n", "-q", "-f", "-t", "3", "-T", "2", "-s", script)
	if err != nil {
		return DHCPLease{}, fmt.Errorf("运行 %s 失败:%w", filepath.Base(client), err)
	}
	if r.Code != 0 {
		return DHCPLease{}, fmt.Errorf("没有取到地址(%s 退出码 %d):%s", filepath.Base(client), r.Code, lastLine(r.Stderr))
	}
	lease, err := parseDHCPLease(r.Stdout)
	if err != nil {
		return DHCPLease{}, fmt.Errorf("取址客户端的输出看不懂,没有配地址:%w", err)
	}
	lease.Ms = n.now().Sub(start).Milliseconds()

	addr := lease.IP + "/" + strconv.Itoa(lease.Mask)
	if _, err := n.ip(ctx, "-n", ns, "addr", "add", addr, "dev", peer); err != nil {
		_ = n.flushPeer(context.WithoutCancel(ctx), ns, peer)
		return DHCPLease{}, err
	}
	if lease.Gateway != "" {
		if _, err := n.ip(ctx, "-n", ns, "route", "add", "default", "via", lease.Gateway, "dev", peer); err != nil {
			_ = n.flushPeer(context.WithoutCancel(ctx), ns, peer)
			return DHCPLease{}, err
		}
	}
	return lease, nil
}

// flushPeer 清掉命名空间里这一头的地址和路由。
func (n *VTermNet) flushPeer(ctx context.Context, ns, peer string) error {
	if _, err := n.ip(ctx, "-n", ns, "addr", "flush", "dev", peer); err != nil {
		return err
	}
	_, err := n.ip(ctx, "-n", ns, "route", "flush", "dev", peer)
	return err
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// parseDHCPLease 解析回调脚本打出的 "OB <键> <值>" 行。不确定就报错:没有合法的 IPv4 地址、
// 没有掩码(mask 或 subnet 都读不出)都不算拿到租约;网关和 DNS 里不合法的项丢掉,网关不合法按「没给网关」。
func parseDHCPLease(out string) (DHCPLease, error) {
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "OB" {
			continue
		}
		kv[f[1]] = strings.Join(f[2:], " ")
	}
	var l DHCPLease
	ip := net.ParseIP(strings.TrimSpace(kv["ip"]))
	if ip == nil || ip.To4() == nil || ip.To4().Equal(net.IPv4zero) {
		return l, fmt.Errorf("没有合法的 IPv4 地址(%q)", kv["ip"])
	}
	l.IP = ip.To4().String()
	if m, err := strconv.Atoi(strings.TrimSpace(kv["mask"])); err == nil && m >= 1 && m <= 32 {
		l.Mask = m
	} else if sm := net.ParseIP(strings.TrimSpace(kv["subnet"])); sm != nil && sm.To4() != nil {
		ones, bits := net.IPMask(sm.To4()).Size()
		if bits == 32 && ones >= 1 {
			l.Mask = ones
		}
	}
	if l.Mask == 0 {
		return l, errors.New("读不出子网掩码")
	}
	for _, g := range strings.Fields(kv["router"]) {
		if gw := net.ParseIP(g); gw != nil && gw.To4() != nil && !gw.To4().Equal(net.IPv4zero) {
			l.Gateway = gw.To4().String()
			break
		}
	}
	for _, d := range strings.Fields(kv["dns"]) {
		if a := net.ParseIP(d); a != nil && a.To4() != nil && !a.To4().Equal(net.IPv4zero) {
			l.DNS = append(l.DNS, a.To4().String())
		}
	}
	if sv := net.ParseIP(strings.TrimSpace(kv["serverid"])); sv != nil && sv.To4() != nil {
		l.Server = sv.To4().String()
	}
	l.LeaseSeconds, _ = strconv.Atoi(strings.TrimSpace(kv["lease"]))
	return l, nil
}
