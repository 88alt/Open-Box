package system

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
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
// 支持三种客户端,按这个顺序挑第一个找得到的:udhcpc(OpenWrt 自带的 busybox、Debian 的 udhcpc 包)、
// dhcpcd(用它的测试模式 -T:只取租约并把变量打到标准输出,不配地址、不留进程)、
// dhclient(ISC,自带回调脚本把租约打出来,脚本在确认拿到租约后让 dhclient 自己退出)。
// 三者最后都走同一条路:解析出租约,由本程序去命名空间里配地址和默认路由。

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

// dhclient 在事件发生时运行这个脚本,租约在 new_* 环境变量里。拿到租约(BOUND / REBOOT)后,
// 只在父进程确实是 dhclient 时才让它退出(dhclient 拿到租约后会留守续租,我们只要一次性取址);
// 看不出父进程是谁就不动手,由超时收尾。脚本不改任何网络配置。
const vtermDhclientScript = `#!/bin/sh
case "$reason" in
BOUND|REBOOT)
  echo "OB ip $new_ip_address"
  echo "OB subnet $new_subnet_mask"
  echo "OB router $new_routers"
  echo "OB dns $new_domain_name_servers"
  echo "OB serverid $new_dhcp_server_identifier"
  echo "OB lease $new_dhcp_lease_time"
  case "$(cat /proc/$PPID/comm 2>/dev/null)" in
  dhclient*) kill -TERM "$PPID" 2>/dev/null ;;
  esac
  ;;
esac
exit 0
`

const (
	dhcpUdhcpc   = "udhcpc"
	dhcpDhcpcd   = "dhcpcd"
	dhcpDhclient = "dhclient"
)

func (n *VTermNet) lookPath(file string) (string, error) {
	if n.LookPath != nil {
		return n.LookPath(file)
	}
	return exec.LookPath(file)
}

// dhcpClient 选客户端,返回程序路径和种类(dhcpUdhcpc / dhcpDhcpcd / dhcpDhclient)。
func (n *VTermNet) dhcpClient() (string, string, error) {
	for _, c := range []string{dhcpUdhcpc, dhcpDhcpcd, dhcpDhclient} {
		if p, err := n.lookPath(c); err == nil && p != "" {
			return p, c, nil
		}
	}
	return "", "", errors.New("找不到 DHCP 客户端(udhcpc / dhcpcd / dhclient),虚拟终端没法取址")
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
	client, kind, err := n.dhcpClient()
	if err != nil {
		return DHCPLease{}, err
	}

	// 先清干净这一头上上次留下的,再取。
	if err := n.flushPeer(ctx, ns, peer); err != nil {
		return DHCPLease{}, err
	}

	start := n.now()
	lease, err := n.runDHCPClient(ctx, ns, peer, client, kind)
	if err != nil {
		return DHCPLease{}, err
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

// PointGatewayAt 把 v 这个虚拟终端的默认网关改指到 gateway(旁路由场景:DHCP 给的网关是主路由,
// 终端的流量不会经过本机,测试就没有意义,所以改成本机 LAN 口的地址,模拟「终端手动把网关指到旁路由」)。
// 只动命名空间里这一头的默认路由(`ip -n <ns> route replace default via <gw> dev <peer>`),不碰本机和别的网络;
// 失败时原来的默认路由保持不变。v 必须是本进程 Ensure 出来、仍在用的,gateway 必须是 IPv4 地址。
func (n *VTermNet) PointGatewayAt(ctx context.Context, v VirtualTerminal, gateway string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.checkNames(); err != nil {
		return err
	}
	ns, _, peer := n.names()
	if n.cur == nil || n.cur.Namespace != ns || n.cur.PeerIf != peer || v.Namespace != ns || v.PeerIf != peer {
		return errors.New("这不是本程序当前在用的虚拟终端,没有改网关")
	}
	if a, err := netip.ParseAddr(gateway); err != nil || !a.Is4() {
		return fmt.Errorf("网关 %q 不是 IPv4 地址", gateway)
	}
	_, err := n.ip(ctx, "-n", ns, "route", "replace", "default", "via", gateway, "dev", peer)
	return err
}

// runDHCPClient 在命名空间里跑一次 kind 种类的客户端并解析出租约;临时文件(回调脚本、dhclient 的
// pid / 租约文件)用完就删,不论成败。
func (n *VTermNet) runDHCPClient(ctx context.Context, ns, peer, client, kind string) (DHCPLease, error) {
	base := filepath.Base(client)
	dir := n.ScriptDir
	if dir == "" {
		dir = os.TempDir()
	}
	writeScript := func(body string) (string, error) {
		script := filepath.Join(dir, vtermScriptName)
		if err := n.writeFile(script, []byte(body), 0o700); err != nil {
			return "", fmt.Errorf("写不了取址回调脚本 %s:%w", script, err)
		}
		return script, nil
	}
	runIn := func(args ...string) (RunResult, error) {
		r, err := n.runFor(ctx, vtermDHCPTimeout, "ip", append([]string{"netns", "exec", ns, client}, args...)...)
		if err != nil {
			return r, fmt.Errorf("运行 %s 失败:%w", base, err)
		}
		return r, nil
	}

	switch kind {
	case dhcpDhcpcd:
		// -T 测试模式:收到租约就把变量打到标准输出并退出,不配地址;-f /dev/null 不读本机的 dhcpcd.conf,
		// 免得别人的配置(静态地址、denyinterfaces 等)干扰这次测试。
		r, err := runIn("-T", "-4", "-t", "6", "-f", "/dev/null", peer)
		if err != nil {
			return DHCPLease{}, err
		}
		lease, perr := parseDhcpcdLease(r.Stdout)
		if perr != nil {
			if r.Code != 0 {
				return DHCPLease{}, fmt.Errorf("没有取到地址(%s 退出码 %d):%s", base, r.Code, lastLine(firstNonEmpty(r.Stderr, r.Stdout)))
			}
			return DHCPLease{}, fmt.Errorf("取址客户端的输出看不懂,没有配地址:%w", perr)
		}
		return lease, nil

	case dhcpDhclient:
		script, err := writeScript(vtermDhclientScript)
		if err != nil {
			return DHCPLease{}, err
		}
		pid := filepath.Join(dir, "obterm-dhclient.pid")
		lf := filepath.Join(dir, "obterm-dhclient.leases")
		defer func() {
			_ = n.remove(script)
			_ = n.remove(pid)
			_ = n.remove(lf)
		}()
		// -1 只试一次(失败退出码 2);-d 前台运行;租约文件和 pid 文件放在临时目录,不碰系统自己的。
		r, err := runIn("-4", "-1", "-d", "-sf", script, "-pf", pid, "-lf", lf, peer)
		if err != nil {
			return DHCPLease{}, err
		}
		// 脚本在拿到租约后让 dhclient 退出,退出码因此不可靠:以有没有解析出租约为准。
		lease, perr := parseDHCPLease(r.Stdout)
		if perr != nil {
			if r.Code != 0 {
				return DHCPLease{}, fmt.Errorf("没有取到地址(%s 退出码 %d):%s", base, r.Code, lastLine(firstNonEmpty(r.Stderr, r.Stdout)))
			}
			return DHCPLease{}, fmt.Errorf("取址客户端的输出看不懂,没有配地址:%w", perr)
		}
		return lease, nil
	}

	// udhcpc
	script, err := writeScript(vtermDHCPScript)
	if err != nil {
		return DHCPLease{}, err
	}
	defer func() { _ = n.remove(script) }()
	r, err := runIn("-i", peer, "-n", "-q", "-f", "-t", "3", "-T", "2", "-s", script)
	if err != nil {
		return DHCPLease{}, err
	}
	if r.Code != 0 {
		return DHCPLease{}, fmt.Errorf("没有取到地址(%s 退出码 %d):%s", base, r.Code, lastLine(r.Stderr))
	}
	lease, err := parseDHCPLease(r.Stdout)
	if err != nil {
		return DHCPLease{}, fmt.Errorf("取址客户端的输出看不懂,没有配地址:%w", err)
	}
	return lease, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
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
	return leaseFromKV(kv)
}

// parseDhcpcdLease 解析 dhcpcd -T 打到标准输出的 "键=值" 行(值可带单引号或双引号),
// 键换成上面统一的名字后走同一套判断。其它行(日志、reason=TEST 等)忽略。
func parseDhcpcdLease(out string) (DHCPLease, error) {
	// dhcpcd 10 的 -T 打的键带 new_ 前缀(实测:new_ip_address、new_subnet_cidr……);
	// 不带前缀的旧写法也认,但同名时以带前缀的为准(不带前缀的可能是别的上下文的值)。
	names := map[string]string{
		"ip_address":             "ip",
		"subnet_cidr":            "mask",
		"subnet_mask":            "subnet",
		"routers":                "router",
		"domain_name_servers":    "dns",
		"dhcp_server_identifier": "serverid",
		"dhcp_lease_time":        "lease",
	}
	kv := map[string]string{}
	seenNew := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		prefixed := strings.HasPrefix(k, "new_")
		name, known := names[strings.TrimPrefix(k, "new_")]
		if !known {
			continue
		}
		if _, have := kv[name]; have && !prefixed && seenNew[name] {
			continue
		}
		if prefixed {
			seenNew[name] = true
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		kv[name] = v
	}
	return leaseFromKV(kv)
}

// leaseFromKV 把统一命名的键值变成租约:ip / mask(或 subnet)/ router / dns / serverid / lease。
func leaseFromKV(kv map[string]string) (DHCPLease, error) {
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
