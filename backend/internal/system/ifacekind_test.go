package system

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestParseUbusNetworkDump(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"空输出", "", map[string]string{}},
		{"不是 JSON", "ubus: not found", map[string]string{}},
		{"没有 interface", `{"x":1}`, map[string]string{}},
		{
			"lan 与 wan 的普通形状",
			`{"interface":[{"interface":"lan","device":"br-lan","l3_device":"br-lan"},
			              {"interface":"wan","device":"eth0.2","l3_device":"pppoe-wan"}]}`,
			map[string]string{"br-lan": "lan", "pppoe-wan": "wan"},
		},
		{
			"没有 l3_device 时用 device",
			`{"interface":[{"interface":"lan","device":"br-lan"}]}`,
			map[string]string{"br-lan": "lan"},
		},
		{
			"拨号后 IP 在 l3_device 上,device 不记",
			`{"interface":[{"interface":"wan","device":"eth0.2","l3_device":"pppoe-wan"}]}`,
			map[string]string{"pppoe-wan": "wan"},
		},
		{
			"同一设备上 wan 胜过 wan6",
			`{"interface":[{"interface":"wan6","device":"eth1"},{"interface":"wan","device":"eth1"}]}`,
			map[string]string{"eth1": "wan"},
		},
		{
			"同一设备上先来的不是 6 结尾就不被 6 覆盖",
			`{"interface":[{"interface":"lan","device":"br-lan"},{"interface":"lan6","device":"br-lan"}]}`,
			map[string]string{"br-lan": "lan"},
		},
		{
			"没有名字的条目跳过",
			`{"interface":[{"device":"br-lan"},{"interface":"lan"},{"interface":"lan","device":"br-lan"}]}`,
			map[string]string{"br-lan": "lan"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseUbusNetworkDump([]byte(tt.raw)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v;想要 %v", got, tt.want)
			}
		})
	}
}

func TestLogicalIfaceKind(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"lan", "lan"},
		{"LAN", "lan"},
		{"lan1", "lan"},
		{"lan_guest", "lan"},
		{"wan", "wan"},
		{"wwan", "wan"},
		{"WAN6", "wan"},
		{"guest", ""},
		{"iot", ""},
		{"", ""},
		{"wlan", ""}, // 无线网卡名不是逻辑网络名,按别的接口处理
	}
	for _, tt := range tests {
		if got := LogicalIfaceKind(tt.in); got != tt.want {
			t.Errorf("LogicalIfaceKind(%q) = %q;想要 %q", tt.in, got, tt.want)
		}
	}
}

func TestUbusIfaceKinds(t *testing.T) {
	dump := `{"interface":[{"interface":"lan","device":"br-lan"},{"interface":"wan","device":"eth0.2","l3_device":"pppoe-wan"},{"interface":"guest","device":"br-guest"}]}`
	fake := func(out string, code int, err error) Runner {
		return func(ctx context.Context, name string, args ...string) (RunResult, error) {
			if name != "ubus" || len(args) != 3 || args[0] != "call" || args[1] != "network.interface" || args[2] != "dump" {
				t.Errorf("跑了意外的命令: %s %v", name, args)
			}
			return RunResult{Stdout: out, Code: code}, err
		}
	}
	if got := UbusIfaceKinds(fake(dump, 0, nil)); !reflect.DeepEqual(got, map[string]string{"br-lan": "lan", "pppoe-wan": "wan"}) {
		t.Errorf("got %v", got)
	}
	if got := UbusIfaceKinds(fake("", 0, errors.New("exec: ubus: not found"))); len(got) != 0 {
		t.Errorf("没有 ubus 时应为空,got %v", got)
	}
	if got := UbusIfaceKinds(fake("", 1, nil)); len(got) != 0 {
		t.Errorf("ubus 退出码非 0 时应为空,got %v", got)
	}
	if got := UbusIfaceKinds(nil); len(got) != 0 {
		t.Errorf("没有 runner 时应为空,got %v", got)
	}
}

// ubus 说某个接口是 wan 时,即使它配了运营商内网地址(10/8)也不算局域网口——这是入口 nft 的 iifname。
func TestLANDevicesDropsWAN(t *testing.T) {
	ifs := []IfaceAddrs{
		{Name: "br-lan", Up: true, Addrs: pfx(t, "192.168.1.1/24")},
		{Name: "pppoe-wan", Up: true, Addrs: pfx(t, "10.64.0.2/32")},
	}
	got := LANDevices(ifs, map[string]string{"pppoe-wan": "wan"})
	if !reflect.DeepEqual(got, []string{"br-lan"}) {
		t.Errorf("got %v;想要 [br-lan]", got)
	}
}

// ubus 说某个接口是 lan 时按 kind 收(地址是不是私有不影响),但没有地址的接口不收(上游清单来自 ip addr)。
func TestLANDevicesUsesUbusLAN(t *testing.T) {
	ifs := []IfaceAddrs{
		{Name: "br-lan2", Up: true, Addrs: pfx(t, "203.0.113.5/24")},
		{Name: "br-lan3", Up: true},
	}
	got := LANDevices(ifs, map[string]string{"br-lan2": "lan", "br-lan3": "lan"})
	if !reflect.DeepEqual(got, []string{"br-lan2"}) {
		t.Errorf("got %v;想要 [br-lan2]", got)
	}
}

// ubus 不认识的接口(guest / iot 之类)仍按私有地址判断:宁可多收,不要因为改了判定而漏掉局域网。
func TestLANDevicesUnclassifiedFallsBackToAddress(t *testing.T) {
	ifs := []IfaceAddrs{
		{Name: "br-guest", Up: true, Addrs: pfx(t, "192.168.2.1/24")},
		{Name: "eth0", Up: true, Addrs: pfx(t, "203.0.113.7/24")},
	}
	got := LANDevices(ifs, map[string]string{"pppoe-wan": "wan"})
	if !reflect.DeepEqual(got, []string{"br-guest"}) {
		t.Errorf("got %v;想要 [br-guest]", got)
	}
}

// LAN 口排在前面:kinds 说 br-home 是 lan,它就排到普通接口 eth3 前面。
func TestLANDevicesOrdersByKind(t *testing.T) {
	ifs := []IfaceAddrs{
		{Name: "eth3", Up: true, Addrs: pfx(t, "192.168.30.1/24")},
		{Name: "br-home", Up: true, Addrs: pfx(t, "10.10.0.1/24")},
	}
	got := LANDevices(ifs, map[string]string{"br-home": "lan"})
	if !reflect.DeepEqual(got, []string{"br-home", "eth3"}) {
		t.Errorf("got %v;想要 [br-home eth3]", got)
	}
}

// CollectLAN 跟着同一份判定走:wan 口的地址不进 lanAddresses / lanSubnets。
func TestCollectLANDropsWAN(t *testing.T) {
	ifs := []IfaceAddrs{
		{Name: "br-lan", Up: true, Addrs: pfx(t, "192.168.1.1/24")},
		{Name: "pppoe-wan", Up: true, Addrs: pfx(t, "10.64.0.2/32")},
	}
	addrs, subnets := CollectLAN(ifs, map[string]string{"pppoe-wan": "wan"})
	if !reflect.DeepEqual(addrs, []string{"192.168.1.1"}) {
		t.Errorf("addrs = %v", addrs)
	}
	if !reflect.DeepEqual(subnets, []string{"192.168.1.0/24"}) {
		t.Errorf("subnets = %v", subnets)
	}
}

func TestSkipLANName(t *testing.T) {
	skip := []string{"tun0", "utun3", "singtun", "wg0", "docker0", "veth12ab", "virbr0",
		"lxcbr0", "lxdbr0", "incusbr0", "podman0", "cni0", "tailscale0", "ztabc", "tap0", "br-0123456789ab"}
	keep := []string{"br-lan", "lan", "lan1", "eth0", "eth0.2", "pppoe-wan", "br-guest", "br-0123456789ABCD", "br-lanx"}
	for _, n := range skip {
		if !skipLANName(n) {
			t.Errorf("%q 应跳过", n)
		}
	}
	for _, n := range keep {
		if skipLANName(n) {
			t.Errorf("%q 不该跳过", n)
		}
	}
}
