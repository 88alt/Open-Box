package sub

import (
	"errors"
	"reflect"
	"testing"
)

const clashSample = `mixed-port: 7890
mode: rule
dns:
  enable: true
  nameserver: [223.5.5.5]
proxies:
  - {name: "香港 01", type: ss, server: hk.example.com, port: 8388, cipher: aes-256-gcm, password: "pa ss", udp: true}
  - {name: ss-obfs, type: ss, server: 1.2.3.4, port: 443, cipher: chacha20-ietf-poly1305, password: 123456, plugin: obfs, plugin-opts: {mode: tls, host: bing.com}}
  - {name: ss-v2, type: ss, server: 1.2.3.5, port: 443, cipher: aes-128-gcm, password: p, plugin: v2ray-plugin, plugin-opts: {mode: websocket, tls: true, host: a.com, path: /ws}}
  - {name: ss-shadowtls, type: ss, server: 1.2.3.6, port: 443, cipher: aes-128-gcm, password: p, plugin: shadow-tls}
  - name: vmess-ws
    type: vmess
    server: v.example.com
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
    alterId: 0
    cipher: auto
    tls: true
    servername: sni.example.com
    network: ws
    ws-opts:
      path: /ray
      headers:
        Host: cdn.example.com
      max-early-data: 2048
    skip-cert-verify: true
  - name: vless-reality
    type: vless
    server: r.example.com
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
    flow: xtls-rprx-vision
    tls: true
    servername: www.microsoft.com
    client-fingerprint: chrome
    reality-opts:
      public-key: PUBKEY
      short-id: abcd
  - {name: vless-grpc, type: vless, server: g.example.com, port: 443, uuid: u1, tls: true, network: grpc, grpc-opts: {grpc-service-name: svc}}
  - {name: vless-xhttp, type: vless, server: x.example.com, port: 443, uuid: u2, tls: true, network: xhttp}
  - {name: trojan, type: trojan, server: t.example.com, port: 443, password: tp, sni: t.example.com, alpn: [h2, http/1.1], network: ws, ws-opts: {path: /t}}
  - {name: hy2, type: hysteria2, server: h.example.com, ports: 20000-30000, password: hp, sni: h.example.com, obfs: salamander, obfs-password: op, hop-interval: 30}
  - {name: tuic, type: tuic, server: tu.example.com, port: 443, uuid: tu1, password: tp, congestion-controller: bbr, alpn: [h3], sni: tu.example.com}
  - {name: tuic4, type: tuic, server: tu4.example.com, port: 443, token: abc}
  - {name: anytls, type: anytls, server: a.example.com, port: 443, password: ap, sni: a.example.com}
  - {name: sock, type: socks5, server: s.example.com, port: 1080, username: u, password: p}
  - {name: web, type: http, server: w.example.com, port: 8080, tls: true, sni: w.example.com}
  - {name: wg, type: wireguard, server: w.example.com, port: 51820}
  - {name: DIRECT, type: direct}
  - {name: bad, type: ss, server: "", port: 1, cipher: aes-128-gcm, password: p}
proxy-groups:
  - {name: PROXY, type: select, proxies: [香港 01, DIRECT]}
rules:
  - MATCH,PROXY
`

func clashByName(t *testing.T, res *Result, name string) Node {
	t.Helper()
	for _, n := range res.Nodes {
		if n.Tag == name {
			return n
		}
	}
	t.Fatalf("没有节点 %q:%+v", name, res.Nodes)
	return Node{}
}

func TestClashParse(t *testing.T) {
	res, err := Parse([]byte(clashSample))
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != FormatClash {
		t.Fatalf("format=%s", res.Format)
	}

	ss := clashByName(t, res, "香港 01")
	if ss.Type != "shadowsocks" || ss.Server != "hk.example.com" || ss.ServerPort != 8388 || ss.OriginalTag != "香港 01" ||
		ss.Fields["method"] != "aes-256-gcm" || ss.Fields["password"] != "pa ss" {
		t.Fatalf("%+v", ss)
	}
	ob := clashByName(t, res, "ss-obfs")
	if ob.Fields["password"] != "123456" || ob.Fields["plugin"] != "obfs-local" || ob.Fields["plugin_opts"] != "obfs=tls;obfs-host=bing.com" {
		t.Fatalf("%+v", ob.Fields)
	}
	v2 := clashByName(t, res, "ss-v2")
	if v2.Fields["plugin"] != "v2ray-plugin" || v2.Fields["plugin_opts"] != "mode=websocket;tls;host=a.com;path=/ws" {
		t.Fatalf("%+v", v2.Fields)
	}

	vm := clashByName(t, res, "vmess-ws")
	tr := sub(vm.Fields, "transport")
	tls := sub(vm.Fields, "tls")
	if vm.Fields["uuid"] != "11111111-2222-3333-4444-555555555555" || vm.Fields["alter_id"] != 0 || vm.Fields["security"] != "auto" ||
		tr["type"] != "ws" || tr["path"] != "/ray" || sub(tr, "headers")["Host"] != "cdn.example.com" || tr["max_early_data"] != 2048 ||
		tr["early_data_header_name"] != "Sec-WebSocket-Protocol" || tls["enabled"] != true || tls["server_name"] != "sni.example.com" {
		t.Fatalf("%+v", vm.Fields)
	}

	vl := clashByName(t, res, "vless-reality")
	tls = sub(vl.Fields, "tls")
	re := sub(tls, "reality")
	if vl.Fields["flow"] != "xtls-rprx-vision" || tls["server_name"] != "www.microsoft.com" || re["public_key"] != "PUBKEY" || re["short_id"] != "abcd" ||
		sub(tls, "utls")["fingerprint"] != "chrome" {
		t.Fatalf("%+v", vl.Fields)
	}
	gr := clashByName(t, res, "vless-grpc")
	if sub(gr.Fields, "transport")["service_name"] != "svc" {
		t.Fatalf("%+v", gr.Fields)
	}

	tj := clashByName(t, res, "trojan")
	tls = sub(tj.Fields, "tls") // trojan 不写 tls 也必走 TLS
	if tj.Fields["password"] != "tp" || tls["enabled"] != true || !reflect.DeepEqual(tls["alpn"], []any{"h2", "http/1.1"}) || sub(tj.Fields, "transport")["path"] != "/t" {
		t.Fatalf("%+v", tj.Fields)
	}

	hy := clashByName(t, res, "hy2")
	if hy.ServerPort != 20000 || hy.Fields["server_ports"] != "20000-30000" || hy.Fields["hop_interval"] != "30" ||
		sub(hy.Fields, "obfs")["type"] != "salamander" || sub(hy.Fields, "obfs")["password"] != "op" || sub(hy.Fields, "tls")["server_name"] != "h.example.com" {
		t.Fatalf("%+v", hy)
	}

	tu := clashByName(t, res, "tuic")
	if tu.Fields["uuid"] != "tu1" || tu.Fields["congestion_control"] != "bbr" || !reflect.DeepEqual(sub(tu.Fields, "tls")["alpn"], []any{"h3"}) {
		t.Fatalf("%+v", tu.Fields)
	}
	at := clashByName(t, res, "anytls")
	if at.Fields["password"] != "ap" || sub(at.Fields, "tls")["server_name"] != "a.example.com" {
		t.Fatalf("%+v", at.Fields)
	}
	sk := clashByName(t, res, "sock")
	if sk.Type != "socks" || sk.Fields["username"] != "u" || sk.Fields["password"] != "p" {
		t.Fatalf("%+v", sk)
	}
	ht := clashByName(t, res, "web")
	if ht.Type != "http" || sub(ht.Fields, "tls")["server_name"] != "w.example.com" {
		t.Fatalf("%+v", ht)
	}

	// 没能导入的要带原因;DIRECT 这类不算节点也不算失败
	skipped := map[string]Skipped{}
	for _, s := range res.Skipped {
		skipped[s.Name] = s
	}
	if len(res.Nodes) != 12 || len(res.Skipped) != 5 {
		t.Fatalf("nodes=%d skipped=%+v", len(res.Nodes), res.Skipped)
	}
	for name, want := range map[string]string{
		"ss-shadowtls": ReasonUnsupportedPlugin, "vless-xhttp": ReasonUnsupportedTransport,
		"tuic4": ReasonUnsupportedType, "wg": ReasonUnsupportedType, "bad": ReasonInvalid,
	} {
		if skipped[name].Reason != want {
			t.Errorf("%s: %+v", name, skipped[name])
		}
	}
}

func TestClashOtherTransports(t *testing.T) {
	res, err := Parse([]byte(`proxies:
  - {name: h2, type: vmess, server: a.com, port: 443, uuid: u, tls: true, network: h2, h2-opts: {host: [a.com, b.com], path: /h}}
  - {name: http, type: vmess, server: b.com, port: 80, uuid: u, network: http, http-opts: {method: GET, path: [/p], headers: {Host: [c.com]}}}
  - {name: hu, type: vless, server: c.com, port: 443, uuid: u, tls: true, network: ws, ws-opts: {path: /u, v2ray-http-upgrade: true, headers: {Host: d.com}}}
  - {name: notls, type: vmess, server: d.com, port: 80, uuid: u, tls: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	h2 := sub(clashByName(t, res, "h2").Fields, "transport")
	if h2["type"] != "http" || !reflect.DeepEqual(h2["host"], []any{"a.com", "b.com"}) || h2["path"] != "/h" {
		t.Fatalf("%+v", h2)
	}
	ht := sub(clashByName(t, res, "http").Fields, "transport")
	if ht["type"] != "http" || !reflect.DeepEqual(ht["host"], []any{"c.com"}) || ht["path"] != "/p" || ht["method"] != "GET" {
		t.Fatalf("%+v", ht)
	}
	hu := sub(clashByName(t, res, "hu").Fields, "transport")
	if hu["type"] != "httpupgrade" || hu["host"] != "d.com" || hu["path"] != "/u" {
		t.Fatalf("%+v", hu)
	}
	if _, has := clashByName(t, res, "notls").Fields["tls"]; has {
		t.Fatal("tls: false 不该写 tls")
	}
}

// 订阅服务器常把整份 Clash 配置再 base64 一次。
func TestClashInBase64(t *testing.T) {
	res, err := Parse([]byte(b64(clashSample)))
	if err != nil || res.Format != FormatClash || len(res.Nodes) != 12 {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestClashNoNodesAndInvalid(t *testing.T) {
	// 只有 proxy-providers、没有 proxies 内容
	if _, err := Parse([]byte("proxies:\nproxy-providers:\n  a: {type: http, url: 'https://x'}\n")); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("%v", err)
	}
	// proxies 段里的 YAML 有语法错误
	if _, err := Parse([]byte("proxies:\n  - {name: a\n")); !errors.Is(err, ErrClashInvalid) {
		t.Fatalf("%v", err)
	}
	// 配置其它部分用了不支持的写法(多行文本)不影响 proxies
	res, err := Parse([]byte("proxies:\n  - {name: a, type: ss, server: s.com, port: 1, cipher: aes-128-gcm, password: p}\nnote: |\n  多行\n  文本\n"))
	if err != nil || len(res.Nodes) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
}

// 解析结果要能被 core 的 configDetailed 接受:字段形状与分享链接解析出来的一致。
func TestClashMatchesLinkShape(t *testing.T) {
	link := mustOne(t, "trojan://tp@t.example.com:443?sni=t.example.com&type=ws&path=%2Ft#T")
	res, err := Parse([]byte("proxies:\n  - {name: T, type: trojan, server: t.example.com, port: 443, password: tp, sni: t.example.com, network: ws, ws-opts: {path: /t}}\n"))
	if err != nil || len(res.Nodes) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if !reflect.DeepEqual(res.Nodes[0], link) {
		t.Fatalf("\nclash %#v\nlink  %#v", res.Nodes[0], link)
	}
}
