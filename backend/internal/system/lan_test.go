package system

import (
	"net/netip"
	"reflect"
	"testing"
)

func pfx(t *testing.T, s ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, x := range s {
		out = append(out, netip.MustParsePrefix(x))
	}
	return out
}

func TestCollectLAN(t *testing.T) {
	tests := []struct {
		name        string
		ifs         []IfaceAddrs
		wantAddrs   []string
		wantSubnets []string
	}{
		{"没有网卡", nil, []string{}, []string{}},
		{
			"只有 LAN 口",
			[]IfaceAddrs{{Name: "br-lan", Up: true, Addrs: pfx(t, "192.168.5.1/24")}},
			[]string{"192.168.5.1"}, []string{"192.168.5.0/24"},
		},
		{
			"跳过回环、没起来的、隧道和容器网桥",
			[]IfaceAddrs{
				{Name: "lo", Up: true, Loopback: true, Addrs: pfx(t, "127.0.0.1/8")},
				{Name: "eth1", Up: false, Addrs: pfx(t, "192.168.9.1/24")},
				{Name: "tun0", Up: true, Addrs: pfx(t, "10.9.0.1/24")},
				{Name: "singtun", Up: true, Addrs: pfx(t, "172.19.0.1/30")},
				{Name: "wg0", Up: true, Addrs: pfx(t, "10.8.0.1/24")},
				{Name: "docker0", Up: true, Addrs: pfx(t, "172.17.0.1/16")},
				{Name: "veth1234", Up: true, Addrs: pfx(t, "10.7.0.1/24")},
				{Name: "br-lan", Up: true, Addrs: pfx(t, "192.168.1.1/24")},
			},
			[]string{"192.168.1.1"}, []string{"192.168.1.0/24"},
		},
		{
			"公网、链路本地、运营商级 NAT 不算家里",
			[]IfaceAddrs{
				{Name: "eth0", Up: true, Addrs: pfx(t, "203.0.113.7/24", "100.64.3.2/10", "fe80::1/64", "2001:db8::1/64")},
				{Name: "br-lan", Up: true, Addrs: pfx(t, "10.0.0.1/8")},
			},
			[]string{"10.0.0.1"}, []string{"10.0.0.0/8"},
		},
		{
			"IPv4 在前 IPv6 在后;LAN 口排前面;网段只含 IPv4",
			[]IfaceAddrs{
				{Name: "eth0", Up: true, Addrs: pfx(t, "fd00:1::1/64", "192.168.100.2/24")},
				{Name: "br-lan", Up: true, Addrs: pfx(t, "192.168.1.1/24", "fd00:2::1/64")},
			},
			[]string{"192.168.1.1", "192.168.100.2", "fd00:2::1", "fd00:1::1"},
			[]string{"192.168.1.0/24", "192.168.100.0/24"},
		},
		{
			"同一网段多个地址只出一个网段;重复地址去重",
			[]IfaceAddrs{
				{Name: "br-lan", Up: true, Addrs: pfx(t, "192.168.1.1/24", "192.168.1.2/24")},
				{Name: "lan1", Up: true, Addrs: pfx(t, "192.168.1.1/24")},
			},
			[]string{"192.168.1.1", "192.168.1.2"}, []string{"192.168.1.0/24"},
		},
		{
			"IPv4 映射写法还原成 IPv4",
			[]IfaceAddrs{{Name: "br-lan", Up: true, Addrs: pfx(t, "::ffff:192.168.7.1/120")}},
			[]string{"192.168.7.1"}, []string{"192.168.7.0/24"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, s := CollectLAN(tt.ifs)
			if !reflect.DeepEqual(a, tt.wantAddrs) || !reflect.DeepEqual(s, tt.wantSubnets) {
				t.Errorf("addrs=%v subnets=%v;想要 %v %v", a, s, tt.wantAddrs, tt.wantSubnets)
			}
		})
	}
}

func TestLANDoesNotPanic(t *testing.T) {
	a, s := LAN()
	if a == nil || s == nil {
		t.Fatalf("应返回空切片而不是 nil: %v %v", a, s)
	}
}
