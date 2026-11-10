package system

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dhcpFake 在 fakeIP 之上再认「ip netns exec … udhcpc」和命名空间里的 addr / route 命令。
type dhcpFake struct {
	*fakeIP
	execOut    string
	execCode   int
	execStderr string
	failOn     func(line string) bool
	dcalls     []string
}

func (d *dhcpFake) run(ctx context.Context, name string, args ...string) (RunResult, error) {
	line := name + " " + strings.Join(args, " ")
	if name == "ip" && len(args) >= 2 {
		switch {
		case args[0] == "netns" && args[1] == "exec":
			d.dcalls = append(d.dcalls, line)
			return RunResult{Stdout: d.execOut, Stderr: d.execStderr, Code: d.execCode}, nil
		case args[0] == "-n" && len(args) >= 3 && (args[2] == "addr" || args[2] == "route"):
			d.dcalls = append(d.dcalls, line)
			if d.failOn != nil && d.failOn(line) {
				return RunResult{Code: 2, Stderr: "boom"}, nil
			}
			return RunResult{}, nil
		}
	}
	return d.fakeIP.run(ctx, name, args...)
}

const goodLease = "OB ip 192.168.1.50\nOB mask 24\nOB subnet 255.255.255.0\nOB router 192.168.1.1\nOB dns 192.168.1.1 1.1.1.1\nOB serverid 192.168.1.1\nOB lease 600\n"

type scriptRec struct {
	written map[string]os.FileMode
	removed []string
	failW   bool
}

func newDHCPNet(t *testing.T) (*VTermNet, *dhcpFake, *scriptRec) {
	t.Helper()
	n, f, _ := newVTerm(t)
	d := &dhcpFake{fakeIP: f, execOut: goodLease}
	rec := &scriptRec{written: map[string]os.FileMode{}}
	state := n.StateFile
	n.Run = d.run
	n.LookPath = func(file string) (string, error) {
		if file == "udhcpc" {
			return "/sbin/udhcpc", nil
		}
		return "", errors.New("not found")
	}
	n.ScriptDir = "/run/obtest"
	n.WriteFile = func(p string, b []byte, perm os.FileMode) error {
		if p == state { // 记账文件照常写
			return os.WriteFile(p, b, perm)
		}
		if rec.failW {
			return errors.New("read-only file system")
		}
		rec.written[p] = perm
		return nil
	}
	n.Remove = func(p string) error {
		if p == state {
			return os.Remove(p)
		}
		rec.removed = append(rec.removed, p)
		return nil
	}
	return n, d, rec
}

func TestVTermAcquireDHCP(t *testing.T) {
	ctx := context.Background()
	t.Run("取到地址:清旧的 → 跑客户端 → 配地址和默认路由,脚本用完就删", func(t *testing.T) {
		n, d, rec := newDHCPNet(t)
		v, err := n.Ensure(ctx, "br-lan")
		if err != nil {
			t.Fatal(err)
		}
		l, err := n.AcquireDHCP(ctx, v)
		if err != nil {
			t.Fatal(err)
		}
		if l.IP != "192.168.1.50" || l.Mask != 24 || l.Gateway != "192.168.1.1" || l.Server != "192.168.1.1" || l.LeaseSeconds != 600 ||
			len(l.DNS) != 2 || l.DNS[0] != "192.168.1.1" || l.DNS[1] != "1.1.1.1" {
			t.Fatalf("租约不对:%+v", l)
		}
		want := []string{
			"ip -n obterm addr flush dev obtv-t",
			"ip -n obterm route flush dev obtv-t",
			"ip netns exec obterm /sbin/udhcpc -i obtv-t -n -q -f -t 3 -T 2 -s /run/obtest/obterm-dhcp.sh",
			"ip -n obterm addr add 192.168.1.50/24 dev obtv-t",
			"ip -n obterm route add default via 192.168.1.1 dev obtv-t",
		}
		if strings.Join(d.dcalls, "\n") != strings.Join(want, "\n") {
			t.Fatalf("命令不对:\n%s", strings.Join(d.dcalls, "\n"))
		}
		if perm, ok := rec.written["/run/obtest/obterm-dhcp.sh"]; !ok || perm != 0o700 {
			t.Fatalf("脚本没写或权限不对:%v", rec.written)
		}
		if len(rec.removed) != 1 || rec.removed[0] != "/run/obtest/obterm-dhcp.sh" {
			t.Fatalf("脚本该删:%v", rec.removed)
		}
	})
	t.Run("服务器没给网关:不加默认路由", func(t *testing.T) {
		n, d, _ := newDHCPNet(t)
		d.execOut = "OB ip 10.0.0.9\nOB mask 8\nOB router \nOB dns \n"
		v, _ := n.Ensure(ctx, "br-lan")
		l, err := n.AcquireDHCP(ctx, v)
		if err != nil || l.Gateway != "" || len(l.DNS) != 0 {
			t.Fatalf("l=%+v err=%v", l, err)
		}
		for _, c := range d.dcalls {
			if strings.Contains(c, "route add") {
				t.Fatalf("不该加路由:%s", c)
			}
		}
	})
	t.Run("不是本进程在用的虚拟终端:什么都不做", func(t *testing.T) {
		n, d, rec := newDHCPNet(t)
		if _, err := n.AcquireDHCP(ctx, VirtualTerminal{Namespace: "obterm", PeerIf: "obtv-t"}); err == nil {
			t.Fatal("没 Ensure 过应报错")
		}
		v, _ := n.Ensure(ctx, "br-lan")
		v.Namespace = "somebody-else"
		if _, err := n.AcquireDHCP(ctx, v); err == nil {
			t.Fatal("名字对不上应报错")
		}
		if len(d.dcalls) != 0 || len(rec.written) != 0 {
			t.Fatalf("不该有动作:%v %v", d.dcalls, rec.written)
		}
	})
	t.Run("没有任何 DHCP 客户端", func(t *testing.T) {
		n, d, _ := newDHCPNet(t)
		v, _ := n.Ensure(ctx, "br-lan")
		n.LookPath = func(string) (string, error) { return "", errors.New("nf") }
		if _, err := n.AcquireDHCP(ctx, v); err == nil || !strings.Contains(err.Error(), "找不到 DHCP 客户端") {
			t.Fatalf("err=%v", err)
		}
		if len(d.dcalls) != 0 {
			t.Fatalf("不该有动作:%v", d.dcalls)
		}
	})
	t.Run("客户端挑选顺序:udhcpc 优先,其次 dhcpcd,最后 dhclient", func(t *testing.T) {
		n, _, _ := newDHCPNet(t)
		have := map[string]bool{"udhcpc": true, "dhcpcd": true, "dhclient": true}
		n.LookPath = func(f string) (string, error) {
			if have[f] {
				return "/sbin/" + f, nil
			}
			return "", errors.New("nf")
		}
		for _, want := range []string{"udhcpc", "dhcpcd", "dhclient"} {
			_, kind, err := n.dhcpClient()
			if err != nil || kind != want {
				t.Fatalf("want %s got %s err=%v", want, kind, err)
			}
			have[want] = false
		}
	})
	t.Run("dhcpcd:用 -T 测试模式取租约,不写回调脚本,配地址路由由本程序做", func(t *testing.T) {
		n, d, rec := newDHCPNet(t)
		n.LookPath = func(f string) (string, error) {
			if f == "dhcpcd" {
				return "/sbin/dhcpcd", nil
			}
			return "", errors.New("nf")
		}
		d.execOut = "reason='TEST'\ninterface='obtv-t'\nnew_ip_address='192.168.1.77'\nnew_subnet_cidr='24'\nnew_subnet_mask='255.255.255.0'\nnew_routers='192.168.1.1'\nnew_domain_name_servers='192.168.1.1 8.8.4.4'\nnew_dhcp_server_identifier='192.168.1.1'\nnew_dhcp_lease_time='7200'\n"
		v, _ := n.Ensure(ctx, "br-lan")
		l, err := n.AcquireDHCP(ctx, v)
		if err != nil {
			t.Fatal(err)
		}
		if l.IP != "192.168.1.77" || l.Mask != 24 || l.Gateway != "192.168.1.1" || l.Server != "192.168.1.1" || l.LeaseSeconds != 7200 ||
			len(l.DNS) != 2 || l.DNS[1] != "8.8.4.4" {
			t.Fatalf("租约不对:%+v", l)
		}
		want := []string{
			"ip -n obterm addr flush dev obtv-t",
			"ip -n obterm route flush dev obtv-t",
			"ip netns exec obterm /sbin/dhcpcd -T -4 -t 6 -f /dev/null obtv-t",
			"ip -n obterm addr add 192.168.1.77/24 dev obtv-t",
			"ip -n obterm route add default via 192.168.1.1 dev obtv-t",
		}
		if strings.Join(d.dcalls, "\n") != strings.Join(want, "\n") {
			t.Fatalf("命令不对:\n%s", strings.Join(d.dcalls, "\n"))
		}
		if len(rec.written) != 0 || len(rec.removed) != 0 {
			t.Fatalf("dhcpcd 不该写 / 删文件:%v %v", rec.written, rec.removed)
		}
	})
	t.Run("dhcpcd:失败如实报最后一行,不配任何东西", func(t *testing.T) {
		n, d, _ := newDHCPNet(t)
		n.LookPath = func(f string) (string, error) {
			if f == "dhcpcd" {
				return "/sbin/dhcpcd", nil
			}
			return "", errors.New("nf")
		}
		d.execOut, d.execCode, d.execStderr = "", 1, "obtv-t: soliciting a DHCP lease\ntimed out\n"
		v, _ := n.Ensure(ctx, "br-lan")
		_, err := n.AcquireDHCP(ctx, v)
		if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "退出码 1") {
			t.Fatalf("err=%v", err)
		}
		for _, c := range d.dcalls {
			if strings.Contains(c, "addr add") || strings.Contains(c, "route add") {
				t.Fatalf("不该配:%s", c)
			}
		}
	})
	t.Run("dhclient:写回调脚本(0700),用 -1 -d,拿到租约即使退出码非 0(被脚本结束)也算成功;脚本和 pid / 租约文件都删", func(t *testing.T) {
		n, d, rec := newDHCPNet(t)
		n.LookPath = func(f string) (string, error) {
			if f == "dhclient" {
				return "/sbin/dhclient", nil
			}
			return "", errors.New("nf")
		}
		d.execOut = "OB ip 10.9.8.7\nOB subnet 255.255.255.0\nOB router 10.9.8.1\nOB dns 10.9.8.1\nOB serverid 10.9.8.1\nOB lease 3600\n"
		d.execCode = -1
		v, _ := n.Ensure(ctx, "br-lan")
		l, err := n.AcquireDHCP(ctx, v)
		if err != nil {
			t.Fatal(err)
		}
		if l.IP != "10.9.8.7" || l.Mask != 24 || l.Gateway != "10.9.8.1" || l.LeaseSeconds != 3600 {
			t.Fatalf("租约不对:%+v", l)
		}
		wantRun := "ip netns exec obterm /sbin/dhclient -4 -1 -d -sf /run/obtest/obterm-dhcp.sh -pf /run/obtest/obterm-dhclient.pid -lf /run/obtest/obterm-dhclient.leases obtv-t"
		found := false
		for _, c := range d.dcalls {
			if c == wantRun {
				found = true
			}
		}
		if !found {
			t.Fatalf("命令不对:\n%s", strings.Join(d.dcalls, "\n"))
		}
		if perm, ok := rec.written["/run/obtest/obterm-dhcp.sh"]; !ok || perm != 0o700 {
			t.Fatalf("脚本没写或权限不对:%v", rec.written)
		}
		if len(rec.removed) != 3 {
			t.Fatalf("脚本、pid、租约文件都该删:%v", rec.removed)
		}
	})
	t.Run("dhclient:没拿到租约且退出码非 0:如实报错,不配东西,临时文件照删", func(t *testing.T) {
		n, d, rec := newDHCPNet(t)
		n.LookPath = func(f string) (string, error) {
			if f == "dhclient" {
				return "/sbin/dhclient", nil
			}
			return "", errors.New("nf")
		}
		d.execOut, d.execCode, d.execStderr = "", 2, "No DHCPOFFERS received.\nNo working leases in persistent database - sleeping.\n"
		v, _ := n.Ensure(ctx, "br-lan")
		_, err := n.AcquireDHCP(ctx, v)
		if err == nil || !strings.Contains(err.Error(), "退出码 2") || !strings.Contains(err.Error(), "sleeping") {
			t.Fatalf("err=%v", err)
		}
		for _, c := range d.dcalls {
			if strings.Contains(c, "addr add") || strings.Contains(c, "route add") {
				t.Fatalf("不该配:%s", c)
			}
		}
		if len(rec.removed) != 3 {
			t.Fatalf("临时文件该删:%v", rec.removed)
		}
	})
	t.Run("dhclient 回调脚本:只在父进程是 dhclient 时才结束它", func(t *testing.T) {
		if !strings.Contains(vtermDhclientScript, `dhclient*) kill -TERM "$PPID"`) || !strings.Contains(vtermDhclientScript, "/proc/$PPID/comm") {
			t.Fatal("脚本要先核对父进程名")
		}
		if strings.Contains(vtermDhclientScript, "ip addr") || strings.Contains(vtermDhclientScript, "ip route") || strings.Contains(vtermDhclientScript, "resolv") {
			t.Fatal("脚本不该改任何网络配置")
		}
	})
	t.Run("取不到地址:如实报客户端的最后一行,不配任何东西,脚本也删", func(t *testing.T) {
		n, d, rec := newDHCPNet(t)
		d.execOut, d.execCode, d.execStderr = "", 1, "udhcpc: started\nudhcpc: no lease, failing\n"
		v, _ := n.Ensure(ctx, "br-lan")
		_, err := n.AcquireDHCP(ctx, v)
		if err == nil || !strings.Contains(err.Error(), "no lease, failing") || !strings.Contains(err.Error(), "退出码 1") {
			t.Fatalf("err=%v", err)
		}
		for _, c := range d.dcalls {
			if strings.Contains(c, "addr add") || strings.Contains(c, "route add") {
				t.Fatalf("不该配:%s", c)
			}
		}
		if len(rec.removed) != 1 {
			t.Fatalf("脚本该删:%v", rec.removed)
		}
	})
	t.Run("客户端起不来 / 超时", func(t *testing.T) {
		n, d, _ := newDHCPNet(t)
		v, _ := n.Ensure(ctx, "br-lan")
		n.Run = func(ctx context.Context, name string, args ...string) (RunResult, error) {
			if len(args) > 1 && args[0] == "netns" && args[1] == "exec" {
				return RunResult{}, context.DeadlineExceeded
			}
			return d.run(ctx, name, args...)
		}
		if _, err := n.AcquireDHCP(ctx, v); err == nil || !strings.Contains(err.Error(), "失败") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("输出看不懂:不配地址", func(t *testing.T) {
		n, d, _ := newDHCPNet(t)
		d.execOut = "OB ip not-an-ip\nOB mask 24\n"
		v, _ := n.Ensure(ctx, "br-lan")
		if _, err := n.AcquireDHCP(ctx, v); err == nil || !strings.Contains(err.Error(), "看不懂") {
			t.Fatalf("err=%v", err)
		}
		for _, c := range d.dcalls {
			if strings.Contains(c, "addr add") {
				t.Fatalf("不该配:%s", c)
			}
		}
	})
	t.Run("配地址或路由失败:清掉这一头再报错", func(t *testing.T) {
		for _, bad := range []string{"addr add", "route add"} {
			n, d, _ := newDHCPNet(t)
			d.failOn = func(l string) bool { return strings.Contains(l, bad) }
			v, _ := n.Ensure(ctx, "br-lan")
			if _, err := n.AcquireDHCP(ctx, v); err == nil || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("%s: err=%v", bad, err)
			}
			if len(d.dcalls) < 2 || !strings.HasSuffix(d.dcalls[len(d.dcalls)-1], "route flush dev obtv-t") ||
				!strings.HasSuffix(d.dcalls[len(d.dcalls)-2], "addr flush dev obtv-t") {
				t.Fatalf("%s: 失败后该清掉:%v", bad, d.dcalls)
			}
		}
	})
	t.Run("清旧配置失败 / 写不了脚本:都不去跑客户端", func(t *testing.T) {
		n, d, rec := newDHCPNet(t)
		v, _ := n.Ensure(ctx, "br-lan")
		d.failOn = func(l string) bool { return strings.Contains(l, "addr flush") }
		if _, err := n.AcquireDHCP(ctx, v); err == nil {
			t.Fatal("清不掉应报错")
		}
		d.failOn = nil
		rec.failW = true
		if _, err := n.AcquireDHCP(ctx, v); err == nil || !strings.Contains(err.Error(), "写不了") {
			t.Fatalf("err=%v", err)
		}
		for _, c := range d.dcalls {
			if strings.Contains(c, "netns exec") {
				t.Fatalf("不该跑客户端:%s", c)
			}
		}
	})
	t.Run("拆除之后不能再取址", func(t *testing.T) {
		n, _, _ := newDHCPNet(t)
		v, _ := n.Ensure(ctx, "br-lan")
		if err := n.Teardown(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := n.AcquireDHCP(ctx, v); err == nil {
			t.Fatal("拆除后应报错")
		}
	})
}

func TestParseDhcpcdLease(t *testing.T) {
	cases := []struct {
		name, in string
		ok       bool
		check    func(DHCPLease) bool
	}{
		{"引号和无引号都认", "new_ip_address=10.1.2.3\nnew_subnet_cidr=24\nnew_routers='10.1.2.1 10.1.2.2'\nnew_domain_name_servers=\"1.1.1.1 9.9.9.9\"\n", true,
			func(l DHCPLease) bool {
				return l.IP == "10.1.2.3" && l.Mask == 24 && l.Gateway == "10.1.2.1" && len(l.DNS) == 2
			}},
		{"没有 subnet_cidr 回落到 subnet_mask", "new_ip_address=10.1.2.3\nnew_subnet_mask=255.255.0.0\n", true, func(l DHCPLease) bool { return l.Mask == 16 }},
		{"别的键和日志行忽略", "reason=TEST\nold_ip_address=1.2.3.4\nnew_ip_address=10.1.2.3\nnew_subnet_cidr=8\nfoo\n", true, func(l DHCPLease) bool { return l.IP == "10.1.2.3" }},
		{"同名时带 new_ 的优先", "ip_address=9.9.9.9\nnew_ip_address=10.1.2.3\nip_address=8.8.8.8\nsubnet_cidr=8\n", true, func(l DHCPLease) bool { return l.IP == "10.1.2.3" }},
		{"没有 new_ 前缀的旧写法也认", "ip_address=10.1.2.3\nsubnet_cidr=24\n", true, func(l DHCPLease) bool { return l.IP == "10.1.2.3" && l.Mask == 24 }},
		{"没有地址", "new_subnet_cidr=24\n", false, nil},
		{"没有掩码", "new_ip_address=10.1.2.3\n", false, nil},
		{"空输出", "", false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, err := parseDhcpcdLease(c.in)
			if (err == nil) != c.ok {
				t.Fatalf("err=%v", err)
			}
			if c.ok && !c.check(l) {
				t.Fatalf("%+v", l)
			}
		})
	}
}

func TestParseDHCPLease(t *testing.T) {
	cases := []struct {
		name, in string
		ok       bool
		check    func(DHCPLease) bool
	}{
		{"只有 subnet 没有 mask", "OB ip 10.1.2.3\nOB subnet 255.255.255.0\n", true, func(l DHCPLease) bool { return l.Mask == 24 }},
		{"mask 越界回落到 subnet", "OB ip 10.1.2.3\nOB mask 33\nOB subnet 255.255.0.0\n", true, func(l DHCPLease) bool { return l.Mask == 16 }},
		{"掩码都没有就不算拿到", "OB ip 10.1.2.3\n", false, nil},
		{"0.0.0.0 不算", "OB ip 0.0.0.0\nOB mask 24\n", false, nil},
		{"IPv6 不算", "OB ip fe80::1\nOB mask 24\n", false, nil},
		{"没有地址", "OB mask 24\n", false, nil},
		{"完全没有输出", "", false, nil},
		{"客户端自己的日志行被忽略", "udhcpc: lease obtained\nOB ip 10.1.2.3\nOB mask 24\n", true, func(l DHCPLease) bool { return l.IP == "10.1.2.3" }},
		{"网关取第一个合法的", "OB ip 10.1.2.3\nOB mask 24\nOB router x 0.0.0.0 10.1.2.254 10.1.2.253\n", true, func(l DHCPLease) bool { return l.Gateway == "10.1.2.254" }},
		{"DNS 丢掉不合法和 IPv6", "OB ip 10.1.2.3\nOB mask 24\nOB dns 8.8.8.8 zzz ::1 9.9.9.9\n", true, func(l DHCPLease) bool { return len(l.DNS) == 2 && l.DNS[1] == "9.9.9.9" }},
		{"租约时长读不出按 0", "OB ip 10.1.2.3\nOB mask 24\nOB lease abc\n", true, func(l DHCPLease) bool { return l.LeaseSeconds == 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, err := parseDHCPLease(c.in)
			if (err == nil) != c.ok {
				t.Fatalf("err=%v", err)
			}
			if c.ok && !c.check(l) {
				t.Fatalf("%+v", l)
			}
		})
	}
}

// 真实 DHCP:沙箱里用 busybox 的 udhcpd 当服务器、udhcpc 当客户端。要 root、netns、busybox(带 udhcpc / udhcpd),缺一样就跳过。
func TestVTermRealDHCP(t *testing.T) {
	for _, kind := range []string{"udhcpc", "dhcpcd", "dhclient"} {
		t.Run(kind, func(t *testing.T) { realDHCPCase(t, kind) })
	}
}

func realDHCPCase(t *testing.T, kind string) {
	realIPReady(t)
	bb, err := exec.LookPath("busybox")
	if err != nil {
		t.Skip("没有 busybox")
	}
	for _, app := range []string{"udhcpc", "udhcpd"} {
		if out, err := exec.Command(bb, app, "--help").CombinedOutput(); err != nil && !strings.Contains(strings.ToLower(string(out)), "usage") {
			t.Skipf("busybox 没有 %s", app)
		}
	}
	var real string
	if kind != "udhcpc" {
		if real, err = exec.LookPath(kind); err != nil {
			t.Skipf("没有 %s", kind)
		}
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "udhcpc")
	if err := os.Symlink(bb, link); err != nil {
		t.Skip(err)
	}

	br := "obtbr-" + kind[:2]
	if out, err := exec.Command("ip", "link", "add", br, "type", "bridge").CombinedOutput(); err != nil {
		t.Skipf("建网桥失败:%v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", br).Run() })
	for _, a := range [][]string{{"addr", "add", "10.254.77.1/24", "dev", br}, {"link", "set", br, "up"}} {
		if out, err := exec.Command("ip", a...).CombinedOutput(); err != nil {
			t.Skipf("配网桥失败:%v %s", err, out)
		}
	}
	conf := filepath.Join(dir, "udhcpd.conf")
	leases := filepath.Join(dir, "leases")
	_ = os.WriteFile(leases, nil, 0o600)
	_ = os.WriteFile(conf, []byte("start 10.254.77.50\nend 10.254.77.60\ninterface "+br+"\noption subnet 255.255.255.0\noption router 10.254.77.1\noption dns 10.254.77.1 9.9.9.9\noption lease 600\nlease_file "+leases+"\npidfile "+filepath.Join(dir, "d.pid")+"\n"), 0o600)
	srv := exec.Command(bb, "udhcpd", "-f", conf)
	if err := srv.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = srv.Process.Kill(); _ = srv.Wait() })
	time.Sleep(500 * time.Millisecond)

	state := filepath.Join(dir, "terminal-net.json")
	n := &VTermNet{Run: ExecRunner, StateFile: state, Namespace: "obtd-ns", HostIf: "obtd-h", PeerIf: "obtd-p",
		LookPath: func(f string) (string, error) {
			if f != kind {
				return "", errors.New("nf")
			}
			if kind == "udhcpc" {
				return link, nil
			}
			return real, nil
		},
		ScriptDir: dir, Kill: func(int, syscall.Signal) error { return nil }}
	t.Cleanup(func() { _, _ = n.Recover(context.Background()) })
	ctx := context.Background()
	v, err := n.Ensure(ctx, br)
	if err != nil {
		t.Fatal(err)
	}
	l, err := n.AcquireDHCP(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(l.IP, "10.254.77.") || l.Mask != 24 || l.Gateway != "10.254.77.1" || len(l.DNS) != 2 || l.DNS[1] != "9.9.9.9" || l.LeaseSeconds != 600 {
		t.Fatalf("租约不对:%+v", l)
	}
	out, _ := exec.Command("ip", "-n", "obtd-ns", "-4", "-o", "addr", "show", "dev", "obtd-p").CombinedOutput()
	if !strings.Contains(string(out), l.IP+"/24") {
		t.Fatalf("命名空间里没有这个地址:%s", out)
	}
	out, _ = exec.Command("ip", "-n", "obtd-ns", "route", "show", "default").CombinedOutput()
	if !strings.Contains(string(out), "via 10.254.77.1") {
		t.Fatalf("默认路由不对:%s", out)
	}
	if exists(filepath.Join(dir, vtermScriptName)) {
		t.Fatal("回调脚本该删")
	}
	// 再取一次(模拟沿用的终端):先清旧的,不会因为地址已存在而失败。
	if _, err := n.AcquireDHCP(ctx, v); err != nil {
		t.Fatalf("重复取址:%v", err)
	}
	if err := n.Teardown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestVTermPointGatewayAt(t *testing.T) {
	ctx := context.Background()
	t.Run("只改命名空间里这一头的默认路由", func(t *testing.T) {
		n, d, _ := newDHCPNet(t)
		v, err := n.Ensure(ctx, "br-lan")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := n.AcquireDHCP(ctx, v); err != nil {
			t.Fatal(err)
		}
		d.dcalls = nil
		if err := n.PointGatewayAt(ctx, v, "192.168.1.9"); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(d.dcalls, "\n"); got != "ip -n obterm route replace default via 192.168.1.9 dev obtv-t" {
			t.Fatalf("命令不对:%s", got)
		}
	})
	t.Run("命令失败带原因", func(t *testing.T) {
		n, d, _ := newDHCPNet(t)
		v, _ := n.Ensure(ctx, "br-lan")
		d.failOn = func(line string) bool { return strings.Contains(line, "route replace") }
		if err := n.PointGatewayAt(ctx, v, "192.168.1.9"); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("不是本进程在用的终端 / 网关不是 IPv4:什么都不做", func(t *testing.T) {
		n, d, _ := newDHCPNet(t)
		if err := n.PointGatewayAt(ctx, VirtualTerminal{Namespace: "obterm", PeerIf: "obtv-t"}, "192.168.1.9"); err == nil {
			t.Fatal("没 Ensure 过应报错")
		}
		v, _ := n.Ensure(ctx, "br-lan")
		for _, gw := range []string{"", "not-ip", "fd00::1", "192.168.1.9; reboot"} {
			if err := n.PointGatewayAt(ctx, v, gw); err == nil {
				t.Errorf("%q 不该放行", gw)
			}
		}
		for _, c := range d.dcalls {
			if strings.Contains(c, "route replace") {
				t.Fatalf("不该跑命令:%s", c)
			}
		}
	})
}
