package mihomo

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 自写向量:输入是面板存的节点(type / tag / server / server_port / fields),期望值按 Mihomo 文档人工写出。
var vectors = []struct{ name, node, want string }{
	{"ss", `{"type":"shadowsocks","tag":"a","server":"1.2.3.4","server_port":8388,"fields":{"method":"aes-256-gcm","password":"pw"}}`,
		`{"name":"a","type":"ss","server":"1.2.3.4","port":8388,"cipher":"aes-256-gcm","password":"pw","udp":true}`},
	{"ss-obfs", `{"type":"shadowsocks","tag":"a","server":"h","server_port":1,"fields":{"method":"aes-128-gcm","password":"p","plugin":"obfs-local","plugin_opts":"obfs=tls;obfs-host=bing.com"}}`,
		`{"name":"a","type":"ss","server":"h","port":1,"cipher":"aes-128-gcm","password":"p","udp":true,"plugin":"obfs","plugin-opts":{"mode":"tls","host":"bing.com"}}`},
	{"ss-v2ray", `{"type":"shadowsocks","tag":"a","server":"h","server_port":1,"fields":{"method":"aes-128-gcm","password":"p","plugin":"v2ray-plugin","plugin_opts":"mode=websocket;tls;host=x.com;path=/ws;mux=0"}}`,
		`{"name":"a","type":"ss","server":"h","port":1,"cipher":"aes-128-gcm","password":"p","udp":true,"plugin":"v2ray-plugin","plugin-opts":{"mode":"websocket","host":"x.com","path":"/ws","tls":true,"mux":false}}`},
	{"vmess-ws-tls", `{"type":"vmess","tag":"v","server":"s.com","server_port":443,"fields":{"uuid":"u","security":"auto","tls":{"enabled":true,"server_name":"s.com","alpn":["h2","http/1.1"]},"transport":{"type":"ws","path":"/p?ed=2048","headers":{"Host":"s.com"}}}}`,
		`{"name":"v","type":"vmess","server":"s.com","port":443,"uuid":"u","alterId":0,"cipher":"auto","udp":true,"tls":true,"servername":"s.com","alpn":["http/1.1"],"skip-cert-verify":true,"network":"ws","ws-opts":{"path":"/p","headers":{"Host":"s.com"},"max-early-data":2048,"early-data-header-name":"Sec-WebSocket-Protocol"}}`},
	{"vless-reality", `{"type":"vless","tag":"r","server":"1.1.1.1","server_port":443,"fields":{"uuid":"u","flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"apple.com","reality":{"enabled":true,"public_key":"6zECpstYZ2XQH60yRSPsC8Z7nv1qLZWJwTWt_t95Isw","short_id":"ab"},"utls":{"enabled":true,"fingerprint":"firefox"}}}}`,
		`{"name":"r","type":"vless","server":"1.1.1.1","port":443,"uuid":"u","flow":"xtls-rprx-vision","udp":true,"tls":true,"servername":"apple.com","client-fingerprint":"firefox","reality-opts":{"public-key":"6zECpstYZ2XQH60yRSPsC8Z7nv1qLZWJwTWt_t95Isw","short-id":"ab"}}`},
	{"vless-grpc", `{"type":"vless","tag":"g","server":"s","server_port":443,"fields":{"uuid":"u","tls":{"enabled":true,"server_name":"s"},"transport":{"type":"grpc","service_name":"/svc"}}}`,
		`{"name":"g","type":"vless","server":"s","port":443,"uuid":"u","udp":true,"tls":true,"servername":"s","skip-cert-verify":true,"network":"grpc","grpc-opts":{"grpc-service-name":"svc"}}`},
	{"vless-httpupgrade", `{"type":"vless","tag":"h","server":"s","server_port":80,"fields":{"uuid":"u","transport":{"type":"httpupgrade","host":"h.com","path":"/up"}}}`,
		`{"name":"h","type":"vless","server":"s","port":80,"uuid":"u","udp":true,"network":"ws","ws-opts":{"path":"/up","headers":{"Host":"h.com"},"v2ray-http-upgrade":true}}`},
	{"vmess-http", `{"type":"vmess","tag":"h","server":"s","server_port":80,"fields":{"uuid":"u","transport":{"type":"http","host":"a.com,b.com","path":"/x","method":"GET"}}}`,
		`{"name":"h","type":"vmess","server":"s","port":80,"uuid":"u","alterId":0,"cipher":"auto","udp":true,"network":"http","http-opts":{"method":"GET","path":["/x"],"headers":{"Host":["a.com","b.com"]}}}`},
	{"trojan", `{"type":"trojan","tag":"t","server":"s","server_port":443,"fields":{"password":"pw","tls":{"enabled":true,"server_name":"s.com"}}}`,
		`{"name":"t","type":"trojan","server":"s","port":443,"password":"pw","udp":true,"tls":true,"sni":"s.com","skip-cert-verify":true}`},
	{"anytls", `{"type":"anytls","tag":"x","server":"s","server_port":443,"fields":{"password":"pw","tls":{"enabled":true,"server_name":"s.com"}}}`,
		`{"name":"x","type":"anytls","server":"s","port":443,"password":"pw","udp":true,"tls":true,"sni":"s.com","skip-cert-verify":true}`},
	{"hy2-hop", `{"type":"hysteria2","tag":"h","server":"s","server_port":443,"fields":{"password":"pw","tls":{"enabled":true,"server_name":"s.com"},"obfs":{"type":"salamander","password":"op"},"server_ports":"20000-30000","hop_interval":"30"}}`,
		`{"name":"h","type":"hysteria2","server":"s","port":443,"password":"pw","tls":true,"sni":"s.com","skip-cert-verify":true,"obfs":"salamander","obfs-password":"op","ports":"20000-30000","hop-interval":30}`},
	{"hy2-multi-ports", `{"type":"hysteria2","tag":"h","server":"s","server_port":443,"fields":{"password":"pw","server_ports":"20000-30000,40000-50000","hop_interval":"1m"}}`,
		`{"name":"h","type":"hysteria2","server":"s","port":443,"password":"pw","ports":"20000-30000,40000-50000","hop-interval":60}`},
	{"tuic", `{"type":"tuic","tag":"t","server":"s","server_port":443,"fields":{"uuid":"u","password":"pw","congestion_control":"bbr","tls":{"enabled":true,"server_name":"s.com","alpn":["h3"]}}}`,
		`{"name":"t","type":"tuic","server":"s","port":443,"uuid":"u","password":"pw","udp-relay-mode":"native","congestion-controller":"bbr","tls":true,"sni":"s.com","alpn":["h3"],"skip-cert-verify":true}`},
	{"socks", `{"type":"socks","tag":"k","server":"s","server_port":1080,"fields":{"username":"u","password":"p"}}`,
		`{"name":"k","type":"socks5","server":"s","port":1080,"udp":true,"username":"u","password":"p"}`},
	{"http-tls", `{"type":"http","tag":"h","server":"s","server_port":443,"fields":{"username":"u","password":"p","tls":{"enabled":true,"server_name":"s.com"}}}`,
		`{"name":"h","type":"http","server":"s","port":443,"username":"u","password":"p","tls":true,"sni":"s.com","skip-cert-verify":true}`},
	{"chain-detour", `{"type":"trojan","tag":"t","server":"s","server_port":443,"detour":"up","fields":{"password":"pw"}}`,
		`{"name":"t","type":"trojan","server":"s","port":443,"password":"pw","udp":true,"dialer-proxy":"up"}`},
	{"wireguard", `{"type":"wireguard","tag":"w","server":"2.2.2.2","server_port":51820,"fields":{"private_key":"O5qhQv76RKq4OrHAkJ9pKf1TZWuJXsL8DkfUEupiulQ=","peer_public_key":"ABfep3cPfs/3qzwgUGVGEp6Wveui9US7jlQU63l4YSI=","pre_shared_key":"Iz7fpYyEhHcS6RFXJTOQfcRnccKvAizqB2a0ItRPMeU=","local_address":["10.0.0.2","fd00::2/128"]}}`,
		`{"name":"w","type":"wireguard","udp":true,"server":"2.2.2.2","port":51820,"private-key":"O5qhQv76RKq4OrHAkJ9pKf1TZWuJXsL8DkfUEupiulQ=","public-key":"ABfep3cPfs/3qzwgUGVGEp6Wveui9US7jlQU63l4YSI=","pre-shared-key":"Iz7fpYyEhHcS6RFXJTOQfcRnccKvAizqB2a0ItRPMeU=","allowed-ips":["0.0.0.0/0","::/0"],"ip":"10.0.0.2","ipv6":"fd00::2"}`},
}

func norm(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func run(t *testing.T, node string) ([]any, error) {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte("["+node+"]"), &v); err != nil {
		t.Fatal(err)
	}
	return Proxies(v)
}

func TestVectors(t *testing.T) {
	for _, c := range vectors {
		got, err := run(t, c.node)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		b, _ := json.Marshal(got[0])
		if string(b) != norm(t, c.want) {
			t.Errorf("%s:\n got  %s\n want %s", c.name, b, norm(t, c.want))
		}
	}
}

func TestErrors(t *testing.T) {
	cases := []struct{ name, node, sub string }{
		{"quic", `{"type":"vmess","tag":"q","server":"s","server_port":1,"fields":{"uuid":"u","transport":{"type":"quic"}}}`, "transport quic"},
		{"socks4", `{"type":"socks","tag":"k","server":"s","server_port":1,"fields":{"version":"4"}}`, "socks version 4"},
		{"plugin", `{"type":"shadowsocks","tag":"p","server":"s","server_port":1,"fields":{"method":"m","password":"p","plugin":"shadow-tls"}}`, "plugin shadow-tls"},
		{"unknown", `{"type":"nope","tag":"n","server":"s","server_port":1}`, "n: "},
	}
	for _, c := range cases {
		_, err := run(t, c.node)
		if err == nil || !strings.Contains(err.Error(), c.sub) {
			t.Errorf("%s: got %v, want error containing %q", c.name, err, c.sub)
		}
	}
	if _, err := Proxies(map[string]any{}); err == nil {
		t.Error("non-array should fail")
	}
	if got, err := Proxies([]any{}); err != nil || len(got) != 0 {
		t.Errorf("empty: %v %v", got, err)
	}
}

func TestHopSeconds(t *testing.T) {
	for in, want := range map[string]float64{"30s": 30, "2m": 120, "1h": 3600, "500ms": 1, "100ms": 1, "1500ms": 2} {
		if got, ok := hopSeconds(in); !ok || got != want {
			t.Errorf("%s: %v %v", in, got, ok)
		}
	}
	if _, ok := hopSeconds("x"); ok {
		t.Error("bad duration accepted")
	}
}

// 设了 OPENBOX_MIHOMO_BIN 才跑:把全部向量的产出交给真实 Mihomo 做 -t 校验。
func TestRealMihomoCheck(t *testing.T) {
	bin := os.Getenv("OPENBOX_MIHOMO_BIN")
	if bin == "" {
		t.Skip("OPENBOX_MIHOMO_BIN not set")
	}
	var all []any
	for i, c := range vectors {
		got, err := run(t, c.node)
		if err != nil {
			t.Fatal(err)
		}
		p := got[0].(map[string]any)
		p["name"] = p["name"].(string) + "-" + string(rune('a'+i))
		delete(p, "dialer-proxy")
		all = append(all, p)
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	b, _ := json.Marshal(map[string]any{"proxies": all, "rules": []any{"MATCH,REJECT"}})
	if err := os.WriteFile(cfg, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-d", dir, "-f", cfg, "-t").CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t: %v\n%s", err, out)
	}
}
