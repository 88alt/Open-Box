package profile

import "testing"

func TestCheckEntryDNS(t *testing.T) {
	cases := []struct {
		name string
		dns  any
		ok   bool
	}{
		{"没有", nil, true},
		{"空 url", map[string]any{"url": ""}, true},
		{"DoH 域名带引导", map[string]any{"url": "https://dns.example.net/dns-query", "bootstrap": "1.1.1.1"}, true},
		{"DoH 域名没引导", map[string]any{"url": "https://dns.example.net/dns-query"}, false},
		{"DoH IP", map[string]any{"url": "https://223.5.5.5/dns-query"}, true},
		{"DoT 域名带引导", map[string]any{"url": "tls://dns.example.net", "bootstrap": "8.8.8.8"}, true},
		{"udp IP", map[string]any{"url": "udp://10.0.0.1"}, true},
		{"udp 域名", map[string]any{"url": "udp://dns.example.net", "bootstrap": "8.8.8.8"}, false},
		{"tcp 端口越界", map[string]any{"url": "tcp://10.0.0.1:70000"}, false},
		{"协议不认", map[string]any{"url": "quic://10.0.0.1"}, false},
		{"不是对象", "x", false},
		{"bootstrap 不是 IP", map[string]any{"url": "https://dns.example.net/dns-query", "bootstrap": "dns.google"}, false},
	}
	for _, key := range []string{"chainProxies", "clientRoutes"} {
		for _, c := range cases {
			entry := map[string]any{"id": "a", "name": "n"}
			if c.dns != nil {
				entry["dns"] = c.dns
			}
			err := Check(map[string]any{key: []any{entry}})
			if (err == nil) != c.ok {
				t.Errorf("%s/%s: err=%v, 期望 ok=%v", key, c.name, err, c.ok)
			}
		}
	}
}
