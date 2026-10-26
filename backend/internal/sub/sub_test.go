package sub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func mustOne(t *testing.T, line string) Node {
	t.Helper()
	res, err := Parse([]byte(line))
	if err != nil {
		t.Fatalf("Parse(%q): %v", line, err)
	}
	if len(res.Nodes) != 1 {
		t.Fatalf("Parse(%q): nodes=%d skipped=%+v", line, len(res.Nodes), res.Skipped)
	}
	return res.Nodes[0]
}

func sub(m map[string]any, k string) map[string]any {
	v, _ := m[k].(map[string]any)
	return v
}

func TestSS(t *testing.T) {
	// SIP002:userinfo 是 base64(method:password)
	n := mustOne(t, "ss://"+b64("aes-256-gcm:pa:ss")+"@1.2.3.4:8388#%E9%A6%99%E6%B8%AF-01")
	if n.Type != "shadowsocks" || n.Server != "1.2.3.4" || n.ServerPort != 8388 || n.Tag != "香港-01" || n.OriginalTag != "香港-01" {
		t.Fatalf("%+v", n)
	}
	if n.Fields["method"] != "aes-256-gcm" || n.Fields["password"] != "pa:ss" {
		t.Fatalf("%+v", n.Fields)
	}
	// 旧写法:整段 base64
	n = mustOne(t, "ss://"+b64("chacha20-ietf-poly1305:pw@example.com:443")+"#old")
	if n.Server != "example.com" || n.ServerPort != 443 || n.Fields["method"] != "chacha20-ietf-poly1305" {
		t.Fatalf("%+v", n)
	}
	// 明文 userinfo + 插件
	n = mustOne(t, "ss://aes-128-gcm:secret@[2001:db8::1]:1234/?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dbing.com#v6")
	if n.Server != "2001:db8::1" || n.Fields["plugin"] != "obfs-local" || n.Fields["plugin_opts"] != "obfs=http;obfs-host=bing.com" {
		t.Fatalf("%+v", n)
	}
	// 不认识的插件 → 跳过并给原因
	res, _ := Parse([]byte("ss://aes-128-gcm:x@1.1.1.1:1?plugin=kcptun#k"))
	if len(res.Nodes) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Reason != ReasonUnsupportedPlugin || res.Skipped[0].Detail != "kcptun" || res.Skipped[0].Name != "k" {
		t.Fatalf("%+v", res)
	}
}

func TestVMess(t *testing.T) {
	j := `{"v":"2","ps":"US-1","add":"v.example.com","port":"443","id":"11111111-1111-1111-1111-111111111111","aid":"0","scy":"auto","net":"ws","host":"cdn.example.com","path":"/ws?ed=2048","tls":"tls","sni":"sni.example.com","alpn":"h2,http/1.1","fp":"chrome"}`
	n := mustOne(t, "vmess://"+b64(j))
	if n.Type != "vmess" || n.ServerPort != 443 || n.Tag != "US-1" {
		t.Fatalf("%+v", n)
	}
	tr, tls := sub(n.Fields, "transport"), sub(n.Fields, "tls")
	if tr["type"] != "ws" || tr["path"] != "/ws?ed=2048" || sub(tr, "headers")["Host"] != "cdn.example.com" {
		t.Fatalf("%+v", tr)
	}
	if tls["server_name"] != "sni.example.com" || sub(tls, "utls")["fingerprint"] != "chrome" || len(tls["alpn"].([]any)) != 2 {
		t.Fatalf("%+v", tls)
	}
	// 端口是数字、h2 传输
	n = mustOne(t, "vmess://"+b64(`{"ps":"h","add":"1.1.1.1","port":8443,"id":"u","net":"h2","host":"a.com,b.com","path":"/p","tls":"tls"}`))
	if tr := sub(n.Fields, "transport"); tr["type"] != "http" || len(tr["host"].([]any)) != 2 {
		t.Fatalf("%+v", n.Fields)
	}
	// kcp 不支持
	res, _ := Parse([]byte("vmess://" + b64(`{"ps":"k","add":"1.1.1.1","port":1,"id":"u","net":"kcp"}`)))
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != ReasonUnsupportedTransport {
		t.Fatalf("%+v", res)
	}
	// 坏 base64
	res, _ = Parse([]byte("vmess://@@@@"))
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != ReasonInvalid {
		t.Fatalf("%+v", res)
	}
}

func TestVLESS(t *testing.T) {
	n := mustOne(t, "vless://uuid-1@host.example.com:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=PUBKEY&sid=ab12&type=tcp&flow=xtls-rprx-vision#R")
	tls := sub(n.Fields, "tls")
	re := sub(tls, "reality")
	if n.Fields["flow"] != "xtls-rprx-vision" || re["public_key"] != "PUBKEY" || re["short_id"] != "ab12" || tls["server_name"] != "www.microsoft.com" {
		t.Fatalf("%+v", n.Fields)
	}
	if _, has := n.Fields["transport"]; has {
		t.Fatalf("tcp 不该有 transport: %+v", n.Fields)
	}
	n = mustOne(t, "vless://u@h.com:443?security=tls&type=grpc&serviceName=svc#G")
	if sub(n.Fields, "transport")["service_name"] != "svc" || sub(n.Fields, "tls")["enabled"] != true {
		t.Fatalf("%+v", n.Fields)
	}
	// xhttp 不支持
	res, _ := Parse([]byte("vless://u@h.com:443?type=xhttp#X"))
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != ReasonUnsupportedTransport || res.Skipped[0].Name != "X" {
		t.Fatalf("%+v", res)
	}
}

func TestTrojanHy2TuicAnyTLS(t *testing.T) {
	n := mustOne(t, "trojan://p%40ss@t.example.com:443?sni=t.example.com&type=ws&host=h.com&path=%2Fp#T")
	if n.Fields["password"] != "p@ss" || sub(n.Fields, "tls")["enabled"] != true || sub(n.Fields, "transport")["type"] != "ws" {
		t.Fatalf("%+v", n.Fields)
	}
	n = mustOne(t, "trojan://pw@t.example.com:443?security=none#T")
	if _, has := n.Fields["tls"]; has {
		t.Fatalf("%+v", n.Fields)
	}
	n = mustOne(t, "hysteria2://pw@h.example.com:443?sni=a.com&obfs=salamander&obfs-password=op&mport=20000-30000&insecure=1#H")
	if n.Fields["server_ports"] != "20000-30000" || sub(n.Fields, "obfs")["type"] != "salamander" || sub(n.Fields, "obfs")["password"] != "op" {
		t.Fatalf("%+v", n.Fields)
	}
	n = mustOne(t, "hy2://pw@h.example.com:443,5000-6000#H2")
	if n.ServerPort != 443 || n.Fields["server_ports"] != "5000-6000" {
		t.Fatalf("%+v", n)
	}
	n = mustOne(t, "hy2://pw@h.example.com#NoPort")
	if n.ServerPort != 443 {
		t.Fatalf("%+v", n)
	}
	n = mustOne(t, "tuic://uuid-2:pw@tu.example.com:443?congestion_control=bbr&alpn=h3&sni=s.com#TU")
	if n.Fields["uuid"] != "uuid-2" || n.Fields["password"] != "pw" || n.Fields["congestion_control"] != "bbr" {
		t.Fatalf("%+v", n.Fields)
	}
	n = mustOne(t, "anytls://pw@a.example.com:8443?sni=s.com#AT")
	if n.Type != "anytls" || n.ServerPort != 8443 {
		t.Fatalf("%+v", n)
	}
}

func TestSocksHTTP(t *testing.T) {
	n := mustOne(t, "socks5://"+b64("user:pw")+"@1.1.1.1:1080#S")
	if n.Type != "socks" || n.Fields["username"] != "user" || n.Fields["password"] != "pw" {
		t.Fatalf("%+v", n)
	}
	n = mustOne(t, "https://u:p@proxy.example.com:8443#P")
	if n.Type != "http" || sub(n.Fields, "tls")["enabled"] != true || n.Fields["username"] != "u" {
		t.Fatalf("%+v", n)
	}
}

func TestBadPortsAndTagFallback(t *testing.T) {
	for _, l := range []string{"vless://u@h.com:0", "vless://u@h.com:70000", "vless://u@h.com", "vless://@h.com:1", "trojan://@h.com:1"} {
		res, err := Parse([]byte(l))
		if err != nil || len(res.Nodes) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Reason != ReasonInvalid {
			t.Errorf("%s → %+v err=%v", l, res, err)
		}
	}
	n := mustOne(t, "vless://u@h.com:443?security=none")
	if n.Tag != "h.com:443" {
		t.Fatalf("没有名字时应退回 server:port,得到 %q", n.Tag)
	}
}

func TestFormatsAndBase64(t *testing.T) {
	lines := "ss://" + b64("aes-128-gcm:a") + "@1.1.1.1:1#a\n\n# 注释\r\ntrojan://p@2.2.2.2:2#b\nwireguard://x@3.3.3.3:3#w\n"
	res, err := Parse([]byte(lines))
	if err != nil || res.Format != FormatLinks || len(res.Nodes) != 2 || len(res.Skipped) != 1 || res.Skipped[0].Reason != ReasonUnsupportedType || res.Skipped[0].Name != "w" {
		t.Fatalf("%+v err=%v", res, err)
	}
	for _, enc := range []func(string) string{
		func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) },
		func(s string) string { return base64.RawStdEncoding.EncodeToString([]byte(s)) },
		func(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) },
		func(s string) string { // 每 76 字符换行
			e := base64.StdEncoding.EncodeToString([]byte(s))
			var sb strings.Builder
			for i := 0; i < len(e); i += 76 {
				j := min(i+76, len(e))
				sb.WriteString(e[i:j] + "\r\n")
			}
			return sb.String()
		},
	} {
		res, err := Parse([]byte("\xef\xbb\xbf" + enc(lines)))
		if err != nil || res.Format != FormatBase64 || len(res.Nodes) != 2 {
			t.Fatalf("%+v err=%v", res, err)
		}
	}
}

func TestSingBox(t *testing.T) {
	cfg := `{"log":{},"outbounds":[
	  {"type":"direct","tag":"direct"},
	  {"type":"selector","tag":"sel","outbounds":["a"]},
	  {"type":"vless","tag":"a","server":"1.1.1.1","server_port":443,"uuid":"u","tls":{"enabled":true,"server_name":"x"},"transport":{"type":"ws","path":"/"}},
	  {"type":"ssh","tag":"s","server":"2.2.2.2","server_port":22},
	  {"type":"trojan","tag":"nop","server":"","server_port":0},
	  {"type":"vmess","tag":"k","server":"3.3.3.3","server_port":1,"uuid":"u","transport":{"type":"kcp"}}
	]}`
	res, err := Parse([]byte(cfg))
	if err != nil || res.Format != FormatSingBox || len(res.Nodes) != 1 || len(res.Skipped) != 3 {
		t.Fatalf("%+v err=%v", res, err)
	}
	n := res.Nodes[0]
	if n.Tag != "a" || n.Fields["uuid"] != "u" || sub(n.Fields, "tls")["server_name"] != "x" {
		t.Fatalf("%+v", n)
	}
	if _, has := n.Fields["tag"]; has {
		t.Fatal("fields 里不该有 tag")
	}
	// 直接给出站数组
	res, err = Parse([]byte(`[{"type":"socks","tag":"s","server":"1.1.1.1","server_port":1080}]`))
	if err != nil || len(res.Nodes) != 1 {
		t.Fatalf("%+v err=%v", res, err)
	}
	if _, err := Parse([]byte(`{"foo":1}`)); err != ErrUnrecognized {
		t.Fatalf("err=%v", err)
	}
}

func TestErrors(t *testing.T) {
	if _, err := Parse([]byte("  \n ")); err != ErrEmpty {
		t.Fatal(err)
	}
	if _, err := Parse([]byte("hello world")); err != ErrUnrecognized {
		t.Fatal(err)
	}
	if _, err := Parse([]byte("proxies:\n  - {name: a\n")); !errors.Is(err, ErrClashInvalid) {
		t.Fatal(err)
	}
}

// 解析结果必须是 core 认识的 JSON 形状。
func TestNodeJSONShape(t *testing.T) {
	n := mustOne(t, "trojan://p@t.example.com:443#T")
	b, _ := json.Marshal(n)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"tag", "type", "server", "server_port", "fields", "originalTag"} {
		if _, ok := m[k]; !ok {
			t.Errorf("缺少 %s: %s", k, b)
		}
	}
	if m["server_port"] != float64(443) {
		t.Fatalf("%s", b)
	}
}

func TestParseUserinfo(t *testing.T) {
	u := ParseUserinfo("upload=10; download=20; total=1e3; expire=1700000000")
	if u == nil || *u.Upload != 10 || *u.Download != 20 || *u.Total != 1000 || *u.Expire != 1700000000 {
		t.Fatalf("%+v", u)
	}
	u = ParseUserinfo("upload=0; download=5; total=9; expire=")
	if u == nil || u.Expire != nil || *u.Download != 5 {
		t.Fatalf("%+v", u)
	}
	u = ParseUserinfo("total=9; expire=0")
	if u == nil || u.Expire != nil {
		t.Fatalf("expire=0 应为 nil: %+v", u)
	}
	for _, s := range []string{"", "garbage", "upload=abc", "upload=-1"} {
		if ParseUserinfo(s) != nil {
			t.Errorf("%q 应为 nil", s)
		}
	}
}

func TestFetch(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=3")
			_, _ = w.Write([]byte("trojan://p@1.1.1.1:1#a"))
		case "/big":
			_, _ = w.Write(make([]byte, MaxBodyBytes+10))
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/slow":
			time.Sleep(2 * time.Second)
		default:
			http.Error(w, "no", http.StatusForbidden)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	f, err := Fetch(ctx, nil, srv.URL+"/ok?token=SECRET", "")
	if err != nil || gotUA != DefaultUserAgent || f.Usage == nil || *f.Usage.Total != 3 || !strings.HasPrefix(string(f.Body), "trojan://") {
		t.Fatalf("%+v err=%v ua=%q", f, err, gotUA)
	}
	if _, err = Fetch(ctx, nil, srv.URL+"/ok", "clash.meta"); err != nil || gotUA != "clash.meta" {
		t.Fatalf("ua=%q err=%v", gotUA, err)
	}
	if _, err = Fetch(ctx, nil, srv.URL+"/redirect", ""); err != nil {
		t.Fatal(err)
	}
	_, err = Fetch(ctx, nil, srv.URL+"/nope?token=SECRET", "")
	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err=%v", err)
	}
	if _, err = Fetch(ctx, nil, srv.URL+"/big", ""); err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Fatalf("err=%v", err)
	}
	for _, bad := range []string{"", "ftp://x/y", "file:///etc/passwd", "http://", "not a url"} {
		if _, err = Fetch(ctx, nil, bad, ""); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
	short := &http.Client{Timeout: 200 * time.Millisecond}
	_, err = Fetch(ctx, short, srv.URL+"/slow?token=SECRET", "")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err=%v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = Fetch(cctx, nil, srv.URL+"/ok", ""); err == nil {
		t.Fatal("已取消的 ctx 应报错")
	}
}
