package config

import (
	"reflect"
	"testing"
)

func TestValidateNodeDNSDoT(t *testing.T) {
	obj := func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"tls IP 无需 bootstrap", obj("protocol", "tls", "server", "1.1.1.1"), ""},
		{"tls 域名要 bootstrap", obj("protocol", "tls", "server", "dns.example"), "nodeDns.bootstrap is required when the DoT address is a domain"},
		{"tls 域名 + bootstrap", obj("protocol", "tls", "server", "dns.example", "bootstrap", "8.8.8.8"), ""},
		{"tls://host:port 写法", obj("protocol", "tls", "server", "tls://dns.example:8853", "bootstrap", "8.8.8.8"), ""},
		{"tls 兼容 host 写在 url 里", obj("protocol", "tls", "url", "dns.example", "bootstrap", "8.8.8.8"), ""},
		{"tls 空 server 视为未配置", obj("protocol", "tls"), ""},
		{"tls 空 server 却有 bootstrap", obj("protocol", "tls", "bootstrap", "8.8.8.8"), "nodeDns.server is required when bootstrap is set"},
		{"tls bootstrap 必须是 IP", obj("protocol", "tls", "server", "dns.example", "bootstrap", "dns.google"), "nodeDns.bootstrap must be an IP address"},
		{"tls 裸 host 带端口", obj("protocol", "tls", "server", "dns.example:853", "bootstrap", "8.8.8.8"), "nodeDns.server must be a DoT host"},
		{"tls 裸 host 带路径", obj("protocol", "tls", "server", "dns.example/x", "bootstrap", "8.8.8.8"), "nodeDns.server must be a DoT host"},
		{"tls URL 端口越界", obj("protocol", "tls", "server", "tls://dns.example:70000", "bootstrap", "8.8.8.8"), "nodeDns.server must be a DoT host"},
		{"tls IPv6 字面量", obj("protocol", "tls", "server", "2001:db8::1"), ""},
		{"协议非法", obj("protocol", "udp", "server", "1.1.1.1"), "nodeDns.protocol must be https or tls"},
		{"protocol 必须是字符串", obj("protocol", 1.0), "nodeDns.protocol must be a string"},
		{"协议大小写不敏感", obj("protocol", "TLS", "server", "1.1.1.1"), ""},
		{"https 仍可用 server 写 URL", obj("protocol", "https", "server", "https://1.1.1.1/dns-query"), ""},
	}
	for _, c := range cases {
		if got := ValidateNodeDNS(c.in); got != c.want {
			t.Errorf("%s:got %q,want %q", c.name, got, c.want)
		}
	}
}

func TestNodeDNSServersDoT(t *testing.T) {
	obj := func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	// IP:单条,默认端口不写 server_port
	tag, servers, ok := NodeDNSServers(obj("protocol", "tls", "server", "1.1.1.1"), "s1")
	if !ok || tag != "dns-sub-s1" {
		t.Fatalf("tag=%q ok=%v", tag, ok)
	}
	if want := []map[string]any{{"type": "tls", "tag": "dns-sub-s1", "server": "1.1.1.1"}}; !reflect.DeepEqual(servers, want) {
		t.Fatalf("IP:%+v", servers)
	}
	// 域名 + port 字段:带 server_port、domain_resolver 与 udp bootstrap
	_, servers, ok = NodeDNSServers(obj("protocol", "tls", "server", "dns.example", "port", 8853.0, "bootstrap", "8.8.8.8"), "s2")
	want := []map[string]any{
		{"type": "tls", "tag": "dns-sub-s2", "server": "dns.example", "server_port": 8853, "domain_resolver": "dns-sub-s2-bootstrap"},
		{"type": "udp", "tag": "dns-sub-s2-bootstrap", "server": "8.8.8.8"},
	}
	if !ok || !reflect.DeepEqual(servers, want) {
		t.Fatalf("域名:%+v", servers)
	}
	// tls://host:port 写法
	_, servers, _ = NodeDNSServers(obj("protocol", "tls", "server", "tls://dns.example:853", "bootstrap", "8.8.8.8"), "s3")
	if servers[0]["server"] != "dns.example" {
		t.Fatalf("URL 写法:%+v", servers[0])
	}
	if _, has := servers[0]["server_port"]; has {
		t.Fatalf("853 不应写 server_port:%+v", servers[0])
	}
	// 非法配置:不产出
	if _, _, ok := NodeDNSServers(obj("protocol", "tls", "server", "dns.example"), "s4"); ok {
		t.Fatal("缺 bootstrap 应不产出")
	}
	// DoH 行为不变:没写 protocol 仍是 https
	_, servers, ok = NodeDNSServers(obj("url", "https://1.1.1.1/dns-query"), "s5")
	if !ok || servers[0]["type"] != "https" {
		t.Fatalf("DoH:%+v", servers)
	}
}
