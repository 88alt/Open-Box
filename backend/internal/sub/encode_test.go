package sub

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// 各协议:解析一条真实写法的链接 → 编码 → 再解析,节点字段必须一致(名字改成别的也要带进链接)。
func TestEncodeLinkRoundTrip(t *testing.T) {
	vmessJSON := `{"v":"2","ps":"vm","add":"vm.example.com","port":"443","id":"11111111-2222-3333-4444-555555555555","aid":"0","scy":"auto","net":"ws","host":"cdn.example.com","path":"/ray","tls":"tls","sni":"cdn.example.com","alpn":"h2,http/1.1","fp":"chrome"}`
	cases := []struct{ name, link string }{
		{"ss", "ss://" + b64("aes-256-gcm:pa:ss") + "@1.2.3.4:8388#ss"},
		{"ss-plugin", "ss://" + b64("chacha20-ietf-poly1305:pw") + "@ss.example.com:443/?plugin=" + "obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dbing.com#ssp"},
		{"vmess", "vmess://" + b64(vmessJSON)},
		{"vless-reality", "vless://11111111-2222-3333-4444-555555555555@vl.example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.microsoft.com&fp=chrome&pbk=PUBKEY&sid=ab12&type=tcp#vl"},
		{"vless-ws-tls", "vless://11111111-2222-3333-4444-555555555555@vl.example.com:443?security=tls&sni=vl.example.com&alpn=h2&type=ws&host=cdn.example.com&path=%2Fws%3Fed%3D2048#vlws"},
		{"vless-grpc", "vless://11111111-2222-3333-4444-555555555555@vl.example.com:443?security=tls&type=grpc&serviceName=svc#vlg"},
		{"trojan", "trojan://p%40ss@tr.example.com:443?sni=tr.example.com&alpn=h2,http/1.1#tr"},
		{"trojan-notls", "trojan://pw@tr.example.com:8080?security=none#trn"},
		{"anytls", "anytls://pw@at.example.com:443?sni=at.example.com&alpn=h2#at"},
		{"hy2", "hysteria2://pw@hy.example.com:443/?sni=hy.example.com&alpn=h3&obfs=salamander&obfs-password=op&mport=5000-6000&hop-interval=30s#hy"},
		{"tuic", "tuic://11111111-2222-3333-4444-555555555555:pw@tu.example.com:443?congestion_control=bbr&sni=tu.example.com&alpn=h3#tu"},
		{"socks", "socks5://user:pass@10.0.0.1:1080#sk"},
		{"socks-noauth", "socks5://10.0.0.1:1080#sk2"},
		{"http", "http://user:pass@10.0.0.2:8080#hp"},
		{"https", "https://user:pass@hs.example.com:443?sni=hs.example.com#hs"},
		{"ipv6", "trojan://pw@[2001:db8::1]:443?sni=a.example.com#v6"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := mustOne(t, c.link)
			n.Tag = "分享 名字 + 100%"
			link, err := EncodeLink(n)
			if err != nil {
				t.Fatalf("EncodeLink: %v", err)
			}
			back := mustOne(t, link)
			if back.Tag != n.Tag {
				t.Errorf("tag = %q, want %q (link %s)", back.Tag, n.Tag, link)
			}
			if back.Type != n.Type || back.Server != n.Server || back.ServerPort != n.ServerPort {
				t.Errorf("endpoint differs: %+v vs %+v (link %s)", back, n, link)
			}
			if !reflect.DeepEqual(back.Fields, n.Fields) {
				t.Errorf("fields differ (link %s)\n got  %#v\n want %#v", link, back.Fields, n.Fields)
			}
		})
	}
}

func TestEncodeLinkErrors(t *testing.T) {
	var nl *ErrNoShareLink
	for name, n := range map[string]Node{
		"unsupported-type": {Tag: "x", Type: "wireguard", Server: "1.1.1.1", ServerPort: 51820, Fields: map[string]any{}},
		"no-uuid":          {Tag: "x", Type: "vless", Server: "1.1.1.1", ServerPort: 443, Fields: map[string]any{}},
		"no-password":      {Tag: "x", Type: "trojan", Server: "1.1.1.1", ServerPort: 443, Fields: map[string]any{}},
		"no-server":        {Tag: "x", Type: "trojan", Server: "", ServerPort: 443, Fields: map[string]any{"password": "p"}},
		"bad-transport": {Tag: "x", Type: "vless", Server: "1.1.1.1", ServerPort: 443,
			Fields: map[string]any{"uuid": "u", "transport": map[string]any{"type": "quic"}}},
	} {
		if _, err := EncodeLink(n); !errors.As(err, &nl) {
			t.Errorf("%s: err = %v, want *ErrNoShareLink", name, err)
		}
	}
}

func TestEncodeLinkEmptyTagOmitsFragment(t *testing.T) {
	link, err := EncodeLink(Node{Type: "socks", Server: "1.1.1.1", ServerPort: 1080, Fields: map[string]any{}})
	if err != nil || strings.Contains(link, "#") {
		t.Fatalf("link=%q err=%v", link, err)
	}
}
