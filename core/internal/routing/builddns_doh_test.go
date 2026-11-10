package routing

import "testing"

// dohTestServersByTag:BuildDNS 输出的 servers 按 tag 取;tag 重复直接失败。
func dohTestServersByTag(t *testing.T, dns map[string]any) map[string]map[string]any {
	t.Helper()
	list, _ := dns["servers"].([]any)
	m := map[string]map[string]any{}
	for _, s := range list {
		e, ok := s.(map[string]any)
		if !ok {
			t.Fatalf("server 不是对象:%#v", s)
		}
		tag, _ := e["tag"].(string)
		if _, dup := m[tag]; dup {
			t.Fatalf("server tag 重复:%s", tag)
		}
		m[tag] = e
	}
	return m
}

func TestBuildDNSDoH(t *testing.T) {
	opts := DNSOptions{SystemDNS: []any{"8.8.8.8"}}
	build := func(t *testing.T, dns map[string]any) map[string]map[string]any {
		t.Helper()
		res, err := BuildDNS(map[string]any{"dns": dns}, opts)
		if err != nil {
			t.Fatal(err)
		}
		return dohTestServersByTag(t, res.DNS)
	}

	t.Run("域名 DoH:path、domain_resolver、指定的 bootstrap", func(t *testing.T) {
		s := build(t, map[string]any{
			"mode": "dnsmasq", "direct": "https://dns.example/custom", "directProtocol": "https",
			"directBootstrap": map[string]any{"server": "9.9.9.9", "protocol": "tcp", "port": 5353.0},
		})
		m := s["dns-direct"]
		if m["type"] != "https" || m["server"] != "dns.example" || m["path"] != "/custom" || m["domain_resolver"] != "dns-direct-bootstrap" {
			t.Fatalf("主条目 %+v", m)
		}
		if _, has := m["server_port"]; has {
			t.Fatalf("默认 443 不应写 server_port:%+v", m)
		}
		b := s["dns-direct-bootstrap"]
		if b["type"] != "tcp" || b["server"] != "9.9.9.9" || b["server_port"] != 5353 {
			t.Fatalf("bootstrap %+v", b)
		}
	})

	t.Run("域名 DoH 没指定 bootstrap:退回上游 DNS(udp)", func(t *testing.T) {
		s := build(t, map[string]any{"mode": "dnsmasq", "direct": "https://dns.example/dns-query", "directProtocol": "https"})
		if _, has := s["dns-direct"]["path"]; has {
			t.Fatalf("默认 /dns-query 不应写 path:%+v", s["dns-direct"])
		}
		b := s["dns-direct-bootstrap"]
		if b["type"] != "udp" || b["server"] != "8.8.8.8" {
			t.Fatalf("bootstrap %+v", b)
		}
	})

	t.Run("IP 主机 DoH:带端口、不带 bootstrap", func(t *testing.T) {
		s := build(t, map[string]any{"mode": "dnsmasq", "direct": "https://1.1.1.1:8443/dns-query", "directProtocol": "https"})
		m := s["dns-direct"]
		if m["server"] != "1.1.1.1" || m["server_port"] != 8443 {
			t.Fatalf("主条目 %+v", m)
		}
		if _, has := m["domain_resolver"]; has {
			t.Fatalf("IP 主机不应有 domain_resolver:%+v", m)
		}
		if _, has := s["dns-direct-bootstrap"]; has {
			t.Fatal("IP 主机不应有 bootstrap 条目")
		}
	})

	t.Run("域名 DoT 同样吃 directBootstrap", func(t *testing.T) {
		s := build(t, map[string]any{
			"mode": "dnsmasq", "direct": "dns.google", "directProtocol": "tls",
			"directBootstrap": map[string]any{"server": "9.9.9.9"},
		})
		m, b := s["dns-direct"], s["dns-direct-bootstrap"]
		if m["type"] != "tls" || m["domain_resolver"] != "dns-direct-bootstrap" || b["type"] != "udp" || b["server"] != "9.9.9.9" {
			t.Fatalf("DoT %+v / bootstrap %+v", m, b)
		}
	})
}
