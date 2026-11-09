package routing

import (
	"reflect"
	"testing"
)

// DoT(v0.1.422 起):直连 / 代理 / 备用上游协议可选 tls;地址是域名时生成 domain_resolver + udp bootstrap。
// 写法按 sing-box 1.12+ DNS server 文档(type:tls,默认端口 853)自写,不对照旧金标准。
func TestBuildDNSDoT(t *testing.T) {
	opts := DNSOptions{SystemDNS: []any{"192.168.1.1"}}

	t.Run("不分流:直连 DoT 域名带 bootstrap", func(t *testing.T) {
		p := obj{"dns": obj{"mode": "dnsmasq", "direct": "dns.alidns.com", "directProtocol": "tls"}}
		res, err := BuildDNS(p, opts)
		if err != nil {
			t.Fatal(err)
		}
		want := []any{
			map[string]any{"type": "tls", "tag": "dns-direct", "server": "dns.alidns.com", "domain_resolver": "dns-direct-bootstrap"},
			map[string]any{"type": "udp", "tag": "dns-direct-bootstrap", "server": "192.168.1.1"},
		}
		if got := res.DNS["servers"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("servers = %#v", got)
		}
	})

	t.Run("不分流:直连 DoT IP 不带 bootstrap,非默认端口写 server_port", func(t *testing.T) {
		p := obj{"dns": obj{"mode": "dnsmasq", "direct": "223.5.5.5", "directProtocol": "tls", "directPort": 8853.0}}
		res, err := BuildDNS(p, opts)
		if err != nil {
			t.Fatal(err)
		}
		want := []any{map[string]any{"type": "tls", "tag": "dns-direct", "server": "223.5.5.5", "server_port": 8853}}
		if got := res.DNS["servers"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("servers = %#v", got)
		}
	})

	t.Run("分流:代理侧 DoT 域名带 detour,bootstrap 不带;tag 不重复", func(t *testing.T) {
		p := obj{"dns": obj{
			"mode": "dnsmasq", "split": true,
			"direct": "dns.alidns.com", "directProtocol": "tls",
			"proxy": "dns.google", "proxyProtocol": "tls",
			"proxyExtras": []any{obj{"server": "one.one.one.one", "protocol": "tls"}},
		}}
		res, err := BuildDNS(p, opts)
		if err != nil {
			t.Fatal(err)
		}
		servers, _ := res.DNS["servers"].([]any)
		tags := map[string]map[string]any{}
		for _, s := range servers {
			m := s.(map[string]any)
			tag := m["tag"].(string)
			if tags[tag] != nil {
				t.Fatalf("tag %q 重复", tag)
			}
			tags[tag] = m
		}
		for _, tag := range []string{"dns-direct", "dns-direct-bootstrap", ProxyResolverTag, ProxyResolverTag + "-bootstrap"} {
			if tags[tag] == nil {
				t.Fatalf("缺少 %q:%v", tag, tags)
			}
		}
		if tags[ProxyResolverTag]["type"] != "tls" || tags[ProxyResolverTag]["domain_resolver"] != ProxyResolverTag+"-bootstrap" {
			t.Fatalf("代理主条目 %+v", tags[ProxyResolverTag])
		}
		if _, has := tags[ProxyResolverTag+"-bootstrap"]["detour"]; has {
			t.Fatalf("bootstrap 不应带 detour:%+v", tags[ProxyResolverTag+"-bootstrap"])
		}
		// 引用的 domain_resolver 必须都在 servers 里
		for tag, m := range tags {
			if r, ok := m["domain_resolver"].(string); ok && tags[r] == nil {
				t.Fatalf("%q 的 domain_resolver %q 不存在", tag, r)
			}
		}
	})
}
