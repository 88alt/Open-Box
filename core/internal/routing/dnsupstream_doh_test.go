package routing

import "testing"

func TestParseDoHURL(t *testing.T) {
	cases := []struct {
		in   any
		ok   bool
		host string
		port int
		path string
	}{
		{"https://dns.google/dns-query", true, "dns.google", 0, "/dns-query"},
		{"https://dns.google:8443/q?x=1", true, "dns.google", 8443, "/q?x=1"},
		{"  https://1.1.1.1  ", true, "1.1.1.1", 0, "/"},
		{"https://[2606:4700::1111]/dns-query", true, "2606:4700::1111", 0, "/dns-query"},
		{"http://dns.google/dns-query", false, "", 0, ""},
		{"https://:443/x", false, "", 0, ""},
		{"https://u:p@dns.google/", false, "", 0, ""},
		{"https://dns.google/#f", false, "", 0, ""},
		{"https://dns.google:70000/", false, "", 0, ""},
		{"https://dns.google:0/", false, "", 0, ""},
		{"dns.google", false, "", 0, ""},
		{nil, false, "", 0, ""},
		{1.0, false, "", 0, ""},
	}
	for _, c := range cases {
		host, port, path, ok := parseDoHURL(c.in)
		if ok != c.ok || host != c.host || port != c.port || path != c.path {
			t.Errorf("parseDoHURL(%#v) = %q,%d,%q,%v;想要 %q,%d,%q,%v", c.in, host, port, path, ok, c.host, c.port, c.path, c.ok)
		}
	}
}

func TestDoHUpstream(t *testing.T) {
	sys := []any{"8.8.8.8"}
	prof := map[string]any{"dns": map[string]any{
		"direct":          "https://dns.example/custom",
		"directProtocol":  "https",
		"directBootstrap": map[string]any{"server": "9.9.9.9", "protocol": "tcp", "port": 5353.0},
	}}
	up := directUpstream(prof, sys)
	if up.Protocol != "https" || up.Server != "dns.example" || up.Port != 443 || !up.Domain || up.Path != "/custom" {
		t.Fatalf("DoH 域名:%+v", up)
	}
	ents := dnsServerEntries(up, "dns-direct", "", prof, sys)
	if len(ents) != 2 {
		t.Fatalf("条目数 %d", len(ents))
	}
	if ents[0]["type"] != "https" || ents[0]["path"] != "/custom" || ents[0]["domain_resolver"] != "dns-direct-bootstrap" {
		t.Fatalf("主条目 %+v", ents[0])
	}
	if _, has := ents[0]["server_port"]; has {
		t.Fatalf("默认 443 不应写 server_port:%+v", ents[0])
	}
	if ents[1]["type"] != "tcp" || ents[1]["server"] != "9.9.9.9" || ents[1]["server_port"] != 5353 {
		t.Fatalf("bootstrap %+v", ents[1])
	}
	if _, has := ents[1]["detour"]; has {
		t.Fatalf("bootstrap 不应带 detour:%+v", ents[1])
	}

	// 默认 path 不写;bootstrap 留空 = 上游 DNS(udp)
	prof2 := map[string]any{"dns": map[string]any{"direct": "https://dns.example/dns-query", "directProtocol": "https"}}
	e2 := dnsServerEntries(directUpstream(prof2, sys), "dns-direct", "", prof2, sys)
	if _, has := e2[0]["path"]; has {
		t.Fatalf("默认 /dns-query 不应写 path:%+v", e2[0])
	}
	if len(e2) != 2 || e2[1]["type"] != "udp" || e2[1]["server"] != "8.8.8.8" {
		t.Fatalf("默认 bootstrap %+v", e2)
	}

	// 地址里的端口优先;没写端口时用档案里的 port
	prof3 := map[string]any{"dns": map[string]any{"direct": "https://1.1.1.1:8443/dns-query", "directProtocol": "https", "directPort": 9999.0}}
	if u := directUpstream(prof3, sys); u.Port != 8443 || u.Domain {
		t.Fatalf("地址端口优先 / IP 主机:%+v", u)
	}
	prof4 := map[string]any{"dns": map[string]any{"direct": "https://1.1.1.1/dns-query", "directProtocol": "https", "directPort": 8443.0}}
	u4 := directUpstream(prof4, sys)
	if u4.Port != 8443 {
		t.Fatalf("档案 port:%+v", u4)
	}
	if n := len(dnsServerEntries(u4, "t", "", prof4, sys)); n != 1 {
		t.Fatalf("IP 主机不应带 bootstrap,条目数 %d", n)
	}
	if e := dnsServerEntries(u4, "t", "", prof4, sys)[0]; e["server_port"] != 8443 {
		t.Fatalf("非默认端口要写 server_port:%+v", e)
	}

	// 非法 https 地址:直连退成上游 DNS,代理退成 1.1.1.1(tcp 53)
	bad := map[string]any{"dns": map[string]any{"direct": "dns.example", "directProtocol": "https", "proxy": "http://x", "proxyProtocol": "https"}}
	if u := directUpstream(bad, sys); !u.Wan {
		t.Fatalf("直连应退成 wan:%+v", u)
	}
	if u := proxyUpstream(bad, sys); u.Protocol != "tcp" || u.Server != "1.1.1.1" || u.Port != 53 {
		t.Fatalf("代理应退成 1.1.1.1:%+v", u)
	}

	// 域名 DoT 同样吃 directBootstrap;备用上游吃自己的 bootstrap
	prof5 := map[string]any{"dns": map[string]any{
		"direct": "dns.google", "directProtocol": "tls",
		"directBootstrap": map[string]any{"server": "9.9.9.9"},
		"directExtras": []any{
			map[string]any{"server": "https://dns.example/dns-query", "protocol": "https", "bootstrap": map[string]any{"server": "1.0.0.1"}},
		},
	}}
	d := directUpstream(prof5, sys)
	if d.BootServer != "9.9.9.9" || d.BootProto != "udp" {
		t.Fatalf("DoT bootstrap:%+v", d)
	}
	ex := directExtraUpstreams(prof5, sys)
	if len(ex) != 1 || ex[0].Protocol != "https" || ex[0].BootServer != "1.0.0.1" {
		t.Fatalf("备用 DoH:%+v", ex)
	}
}
