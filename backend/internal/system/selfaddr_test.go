package system

import (
	"net/netip"
	"testing"
)

func ifc(name string, up bool, addrs ...string) IfaceAddrs {
	ia := IfaceAddrs{Name: name, Up: up}
	for _, a := range addrs {
		ia.Addrs = append(ia.Addrs, netip.MustParsePrefix(a))
	}
	return ia
}

// selfKindIn 的判定顺序:本机网卡 → 逻辑网络名 → 私有地址兜底 → 隧道算 other。
func TestSelfKindIn(t *testing.T) {
	ifaces := []IfaceAddrs{
		ifc("br-lan", true, "192.168.2.5/24"),
		ifc("pppoe-wan", true, "203.0.113.7/32"),
		ifc("openbox-tun", true, "172.19.0.1/30"),
		ifc("lo", true, "127.0.0.1/8"),
		ifc("eth9", false, "10.9.9.9/24"),
	}
	kinds := map[string]string{"br-lan": "lan", "pppoe-wan": "wan"}

	cases := []struct {
		name   string
		addr   string
		wantK  string
		wantI  string
		wantOK bool
	}{
		{"LAN 口地址按 ubus 判成 lan", "192.168.2.5", "lan", "br-lan", true},
		{"WAN 口地址判成 wan", "203.0.113.7", "wan", "pppoe-wan", true},
		{"隧道口一律 other", "172.19.0.1", "other", "openbox-tun", true},
		{"回环不算本机终端来源", "127.0.0.1", "", "", false},
		{"没起来的网卡不认", "10.9.9.9", "", "", false},
		{"不是本机地址", "192.168.2.99", "", "", false},
		{"非法地址", "not-an-ip", "", "", false},
	}
	for _, c := range cases {
		k, i, ok := selfKindIn(ifaces, kinds, c.addr)
		if k != c.wantK || i != c.wantI || ok != c.wantOK {
			t.Errorf("%s:%s → (%q,%q,%v),想要 (%q,%q,%v)", c.name, c.addr, k, i, ok, c.wantK, c.wantI, c.wantOK)
		}
	}
}

// 没有 ubus 时按地址是否私有兜底:私有算 lan,公网算 wan。
func TestSelfKindInWithoutUbus(t *testing.T) {
	ifaces := []IfaceAddrs{
		ifc("eth0", true, "10.0.0.5/24"),
		ifc("eth1", true, "198.51.100.9/24"),
	}
	if k, _, ok := selfKindIn(ifaces, nil, "10.0.0.5"); !ok || k != "lan" {
		t.Fatalf("私有地址应为 lan:%q %v", k, ok)
	}
	if k, _, ok := selfKindIn(ifaces, nil, "198.51.100.9"); !ok || k != "wan" {
		t.Fatalf("公网地址应为 wan:%q %v", k, ok)
	}
}
