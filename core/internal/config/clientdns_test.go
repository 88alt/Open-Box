package config

import (
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/clientroutes"
)

func TestClientDNSServers(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want []map[string]any
	}{
		{"DoH IP 默认路径", map[string]any{"url": "https://223.5.5.5/dns-query"},
			[]map[string]any{{"type": "https", "tag": "t", "server": "223.5.5.5"}}},
		{"DoH 域名 + 自定义端口 / 路径", map[string]any{"url": "https://dns.example.net:8443/q", "bootstrap": "1.1.1.1"},
			[]map[string]any{
				{"type": "https", "tag": "t", "server": "dns.example.net", "server_port": 8443, "path": "/q", "domain_resolver": "t-bootstrap"},
				{"type": "udp", "tag": "t-bootstrap", "server": "1.1.1.1"}}},
		{"DoT", map[string]any{"url": "tls://10.0.0.2"}, []map[string]any{{"type": "tls", "tag": "t", "server": "10.0.0.2"}}},
		{"tcp 端口", map[string]any{"url": "tcp://10.0.0.2:5353"}, []map[string]any{{"type": "tcp", "tag": "t", "server": "10.0.0.2", "server_port": 5353}}},
		{"udp IPv6", map[string]any{"url": "udp://[2001:4860:4860::8888]"}, []map[string]any{{"type": "udp", "tag": "t", "server": "2001:4860:4860::8888"}}},
		{"域名没引导", map[string]any{"url": "tls://dns.example.net"}, nil},
		{"协议不认", map[string]any{"url": "quic://10.0.0.2"}, nil},
		{"端口越界", map[string]any{"url": "udp://10.0.0.2:0"}, nil},
	}
	for _, c := range cases {
		got, ok := ClientDNSServers(c.raw, "t")
		if (c.want != nil) != ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v ok=%v, want %v", c.name, got, ok, c.want)
		}
	}
}

func TestPlanClientDNS(t *testing.T) {
	routes := []clientroutes.Route{
		{ID: "a", Match: "ip", Outbound: "chain-1", Sources: []string{"10.0.0.5/32"}},
		{ID: "b", Match: "ip", Outbound: "chain-1", Sources: []string{"10.0.0.6/32"}, DNS: map[string]any{"url": "udp://10.0.0.1"}},
		{ID: "c", Match: "ip", Outbound: "node-x", Sources: []string{"10.0.0.7/32"}},
		{ID: "d", Match: "ip", Outbound: "direct", Bypass: true, Sources: []string{"10.0.0.8/32"}},
	}
	profile := map[string]any{"chainProxies": []any{
		map[string]any{"name": "chain-1", "enabled": true, "dns": map[string]any{"url": "tls://9.9.9.9"}},
	}}
	out, servers := PlanClientDNS(routes, profile)
	if out[0].DNSTag != "dns-client-0" || out[0].DNS["url"] != "tls://9.9.9.9" {
		t.Errorf("直选链式代理的终端应继承链路的 DNS: %+v", out[0])
	}
	if out[1].DNSTag != "dns-client-1" || out[1].DNS["url"] != "udp://10.0.0.1" {
		t.Errorf("规则自己的 DNS 优先: %+v", out[1])
	}
	if out[2].DNSTag != "" || out[3].DNSTag != "" {
		t.Errorf("没配的 / 不进内核的不该有专用解析器: %+v %+v", out[2], out[3])
	}
	if len(servers) != 2 {
		t.Errorf("servers=%v", servers)
	}
	// 链路停用就不继承
	profile["chainProxies"].([]any)[0].(map[string]any)["enabled"] = false
	if out, _ = PlanClientDNS(routes, profile); out[0].DNSTag != "" {
		t.Errorf("停用的链路不该继承: %+v", out[0])
	}
}

func TestValidateClientDNS(t *testing.T) {
	if ValidateClientDNS(nil) != "" || ValidateClientDNS(map[string]any{"url": ""}) != "" {
		t.Error("没配应通过")
	}
	if ValidateClientDNS(map[string]any{"url": "https://dns.example.net/dns-query"}) == "" {
		t.Error("域名没引导应不通过")
	}
	if ValidateClientDNS(map[string]any{"url": "udp://10.0.0.1"}) != "" {
		t.Error("udp IP 应通过")
	}
}
