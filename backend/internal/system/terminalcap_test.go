package system

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func tpfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestPickLANDevice(t *testing.T) {
	cases := []struct {
		name string
		ifs  []IfaceAddrs
		want *TerminalLAN
	}{
		{"没有网卡", nil, nil},
		{"只有回环和公网", []IfaceAddrs{
			{Name: "lo", Up: true, Loopback: true, Addrs: []netip.Prefix{tpfx("127.0.0.1/8")}},
			{Name: "eth1", Up: true, Addrs: []netip.Prefix{tpfx("203.0.113.5/24")}},
		}, nil},
		{"br-lan 排在别的私网网卡前面", []IfaceAddrs{
			{Name: "eth0", Up: true, Addrs: []netip.Prefix{tpfx("10.0.0.2/24")}},
			{Name: "br-lan", Up: true, Addrs: []netip.Prefix{tpfx("192.168.5.1/24")}},
		}, &TerminalLAN{Device: "br-lan", Address: "192.168.5.1", Mask: tintp(24)}},
		{"没有 LAN 名字就取第一块私网网卡", []IfaceAddrs{
			{Name: "eth0", Up: true, Addrs: []netip.Prefix{tpfx("10.0.0.2/16")}},
			{Name: "eth2", Up: true, Addrs: []netip.Prefix{tpfx("192.168.1.2/24")}},
		}, &TerminalLAN{Device: "eth0", Address: "10.0.0.2", Mask: tintp(16)}},
		{"跳过没起来的、隧道、容器网桥、veth、只有 IPv6 的", []IfaceAddrs{
			{Name: "br-lan", Up: false, Addrs: []netip.Prefix{tpfx("192.168.5.1/24")}},
			{Name: "tun0", Up: true, Addrs: []netip.Prefix{tpfx("10.1.0.1/24")}},
			{Name: "docker0", Up: true, Addrs: []netip.Prefix{tpfx("172.17.0.1/16")}},
			{Name: "veth123", Up: true, Addrs: []netip.Prefix{tpfx("10.9.0.1/24")}},
			{Name: "lan", Up: true, Addrs: []netip.Prefix{tpfx("fd00::1/64")}},
		}, nil},
		{"网卡第一个地址是公网、第二个是私网", []IfaceAddrs{
			{Name: "lan1", Up: true, Addrs: []netip.Prefix{tpfx("203.0.113.9/24"), tpfx("192.168.9.1/24")}},
		}, &TerminalLAN{Device: "lan1", Address: "192.168.9.1", Mask: tintp(24)}},
		{"IPv4 映射写法的 128 位前缀还原成 IPv4 位数", []IfaceAddrs{
			{Name: "br-lan", Up: true, Addrs: []netip.Prefix{netip.PrefixFrom(netip.MustParseAddr("::ffff:192.168.7.1"), 120)}},
		}, &TerminalLAN{Device: "br-lan", Address: "192.168.7.1", Mask: tintp(24)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PickLANDevice(c.ifs)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}

func tintp(n int) *int { return &n }

// okProbe 返回一个所有前提都满足的假探测器。
func okProbe() TerminalProbe {
	files := map[string]string{
		"/proc/modules":              "veth 20480 0 - Live 0x0\n",
		"/proc/sys/kernel/osrelease": "6.6.1\n",
	}
	return TerminalProbe{
		Euid: func() int { return 0 },
		Ifaces: func() []IfaceAddrs {
			return []IfaceAddrs{{Name: "br-lan", Up: true, Addrs: []netip.Prefix{tpfx("192.168.5.1/24")}}}
		},
		Run: func(ctx context.Context, name string, args ...string) (RunResult, error) {
			if name == "ip" && strings.Join(args, " ") == "netns list" {
				return RunResult{Code: 0}, nil
			}
			return RunResult{}, errors.New("unexpected command " + name)
		},
		LookPath: func(n string) (string, error) {
			switch n {
			case "ip", "udhcpc":
				return "/sbin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Exists: func(p string) bool {
			return p == "/proc/self/ns/net" || p == "/proc/net/nf_conntrack"
		},
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, errors.New("no such file")
		},
		Glob: func(string) ([]string, error) { return nil, nil },
	}
}

func TestCapabilityAllPresent(t *testing.T) {
	got := okProbe().Capability(context.Background())
	if !got.OK || len(got.Missing) != 0 || got.Missing == nil {
		t.Fatalf("应当全部满足且 missing 是空数组: %+v", got)
	}
	if got.LAN == nil || got.LAN.Device != "br-lan" || got.LAN.Address != "192.168.5.1" || got.LAN.Mask == nil || *got.LAN.Mask != 24 {
		t.Fatalf("lan: %+v", got.LAN)
	}
}

func TestCapabilityMissingEach(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*TerminalProbe)
		want   []string
	}{
		{"不是 root", func(p *TerminalProbe) { p.Euid = func() int { return 1000 } }, []string{"root"}},
		{"没有 ip 命令:netns 和 veth 都缺", func(p *TerminalProbe) {
			p.LookPath = func(n string) (string, error) {
				if n == "udhcpc" {
					return "/sbin/udhcpc", nil
				}
				return "", errors.New("x")
			}
		}, []string{"netns", "veth"}},
		{"ip 不支持 netns(退出码非 0)", func(p *TerminalProbe) {
			p.Run = func(context.Context, string, ...string) (RunResult, error) { return RunResult{Code: 1}, nil }
		}, []string{"netns"}},
		{"ip 根本启动不了", func(p *TerminalProbe) {
			p.Run = func(context.Context, string, ...string) (RunResult, error) { return RunResult{}, errors.New("boom") }
		}, []string{"netns"}},
		{"内核没有命名空间支持文件", func(p *TerminalProbe) {
			p.Exists = func(path string) bool { return path == "/proc/net/nf_conntrack" }
		}, []string{"netns"}},
		{"没有 veth 驱动", func(p *TerminalProbe) {
			p.ReadFile = func(string) ([]byte, error) { return nil, errors.New("x") }
		}, []string{"veth"}},
		{"没有私有 IPv4 的 LAN 口", func(p *TerminalProbe) { p.Ifaces = func() []IfaceAddrs { return nil } }, []string{"lan"}},
		{"没有 conntrack", func(p *TerminalProbe) {
			p.Exists = func(path string) bool { return path == "/proc/self/ns/net" }
		}, []string{"conntrack"}},
		{"没有 DHCP 客户端", func(p *TerminalProbe) {
			p.LookPath = func(n string) (string, error) {
				if n == "ip" {
					return "/sbin/ip", nil
				}
				return "", errors.New("x")
			}
		}, []string{"dhcp"}},
		{"全缺时按前端类型里的顺序", func(p *TerminalProbe) { *p = TerminalProbe{} }, []string{"root", "netns", "veth", "lan", "conntrack", "dhcp"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := okProbe()
			c.mutate(&p)
			got := p.Capability(context.Background())
			if got.OK || !reflect.DeepEqual(got.Missing, c.want) {
				t.Fatalf("missing=%v ok=%v,想要 %v", got.Missing, got.OK, c.want)
			}
		})
	}
}

func TestCapabilityDHCPClientAlternatives(t *testing.T) {
	for _, name := range []string{"udhcpc", "dhclient", "dhcpcd"} {
		p := okProbe()
		p.LookPath = func(n string) (string, error) {
			if n == "ip" || n == name {
				return "/x/" + n, nil
			}
			return "", errors.New("x")
		}
		if got := p.Capability(context.Background()); !got.OK {
			t.Fatalf("%s 应当算有 DHCP 客户端: %+v", name, got.Missing)
		}
	}
}

func TestCapabilityConntrackByCommand(t *testing.T) {
	p := okProbe()
	p.Exists = func(path string) bool { return path == "/proc/self/ns/net" }
	p.LookPath = func(n string) (string, error) {
		switch n {
		case "ip", "udhcpc", "conntrack":
			return "/x/" + n, nil
		}
		return "", errors.New("x")
	}
	if got := p.Capability(context.Background()); !got.OK {
		t.Fatalf("有 conntrack 命令应当算有: %+v", got.Missing)
	}
}

func TestVethAvailableSources(t *testing.T) {
	mk := func(files map[string]string, globs map[string][]string, exists ...string) TerminalProbe {
		set := map[string]bool{}
		for _, e := range exists {
			set[e] = true
		}
		return TerminalProbe{
			Exists: func(p string) bool { return set[p] },
			ReadFile: func(p string) ([]byte, error) {
				if s, ok := files[p]; ok {
					return []byte(s), nil
				}
				return nil, errors.New("x")
			},
			Glob: func(pat string) ([]string, error) { return globs[pat], nil },
		}
	}
	rel := map[string]string{"/proc/sys/kernel/osrelease": "5.15.0\n"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range rel {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name string
		p    TerminalProbe
		want bool
	}{
		{"/sys/module/veth", mk(nil, nil, "/sys/module/veth"), true},
		{"已加载模块", mk(map[string]string{"/proc/modules": "bridge 1 0 - Live\nveth 2 0 - Live\n"}, nil), true},
		{"模块名只是前缀相似不算", mk(map[string]string{"/proc/modules": "vethx 2 0 - Live\n"}, nil), false},
		{"编进内核", mk(with(map[string]string{"/lib/modules/5.15.0/modules.builtin": "kernel/drivers/net/dummy.ko\nkernel/drivers/net/veth.ko\n"}), nil), true},
		{"标准路径的模块文件", mk(rel, map[string][]string{"/lib/modules/5.15.0/kernel/drivers/net/veth.ko*": {"/lib/modules/5.15.0/kernel/drivers/net/veth.ko.zst"}}), true},
		{"OpenWrt 平铺目录的模块文件", mk(rel, map[string][]string{"/lib/modules/5.15.0/veth.ko*": {"/lib/modules/5.15.0/veth.ko"}}), true},
		{"哪里都没有", mk(rel, nil), false},
		{"内核版本里带路径分隔符不去拼路径", mk(map[string]string{"/proc/sys/kernel/osrelease": "../../x"}, map[string][]string{"/lib/modules/../../x/veth.ko*": {"y"}}), false},
	}
	for _, c := range cases {
		if got := c.p.vethAvailable(); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestRealTerminalProbeRuns(t *testing.T) {
	// 只确认真实实现能跑完、不 panic、字段齐全(沙箱的结果不固定,不断言缺哪些)
	got := RealTerminalProbe().Capability(context.Background())
	if got.Missing == nil {
		t.Fatal("missing 不能是 null")
	}
	if got.OK != (len(got.Missing) == 0) {
		t.Fatalf("ok 与 missing 不一致: %+v", got)
	}
}
