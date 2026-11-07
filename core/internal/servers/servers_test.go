package servers

import (
	"reflect"
	"testing"
)

// 自写测试向量:期望值按 sing-box 入站配置的字段定义(shadowsocks / vless / tuic /
// hysteria2 / mixed)与本包注释写明的语义人工核对,不依赖 engine 基线或官方 openbox-core 输出。

type M = map[string]any

var opts = M{"certPath": "/c.pem", "keyPath": "/k.pem"}

func tlsWith(extra M) M {
	t := M{"enabled": true, "server_name": "open-box.local", "certificate_path": "/c.pem", "key_path": "/k.pem"}
	for k, v := range extra {
		t[k] = v
	}
	return t
}

func build(t *testing.T, servers any, o any, want []any) {
	t.Helper()
	got := BuildServerInbounds(servers, o)
	if got == nil {
		t.Fatalf("返回值必须是非 nil 切片(序列化成 [] 而不是 null)")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func TestEmptyAndInvalidInput(t *testing.T) {
	for _, in := range []any{nil, "x", float64(1), M{}, []any{}} {
		build(t, in, opts, []any{})
	}
}

func TestShadowsocks(t *testing.T) {
	build(t, []any{M{"id": "a1", "protocol": "shadowsocks", "port": float64(8388), "method": "2022-blake3-aes-128-gcm", "password": "pw"}}, opts,
		[]any{M{"tag": "share-a1", "listen": "::", "listen_port": float64(8388), "type": "shadowsocks", "method": "2022-blake3-aes-128-gcm", "password": "pw"}})
}

func TestVless(t *testing.T) {
	base := func(tls any) []any {
		return []any{M{"id": "v", "protocol": "vless", "port": float64(443), "uuid": "u-1", "tls": tls}}
	}
	plain := M{"tag": "share-v", "listen": "::", "listen_port": float64(443), "type": "vless", "users": []any{M{"uuid": "u-1"}}}
	build(t, base(nil), opts, []any{plain})
	build(t, base(false), opts, []any{plain})
	build(t, base("true"), opts, []any{plain}) // 只有布尔 true 才开 TLS
	build(t, base(float64(1)), opts, []any{plain})
	withTLS := M{"tag": "share-v", "listen": "::", "listen_port": float64(443), "type": "vless", "users": []any{M{"uuid": "u-1"}}, "tls": tlsWith(nil)}
	build(t, base(true), opts, []any{withTLS})
}

func TestTuic(t *testing.T) {
	build(t, []any{M{"id": "t", "protocol": "tuic", "port": float64(4443), "uuid": "u", "password": "p"}}, opts,
		[]any{M{"tag": "share-t", "listen": "::", "listen_port": float64(4443), "type": "tuic",
			"users": []any{M{"uuid": "u", "password": "p"}}, "congestion_control": "bbr",
			"tls": tlsWith(M{"alpn": []any{"h3"}})}})
}

func TestHysteria2(t *testing.T) {
	mk := func(obfs any) []any {
		return []any{M{"id": "h", "protocol": "hysteria2", "port": float64(5443), "password": "p", "obfs": obfs}}
	}
	plain := M{"tag": "share-h", "listen": "::", "listen_port": float64(5443), "type": "hysteria2",
		"users": []any{M{"password": "p"}}, "tls": tlsWith(M{"alpn": []any{"h3"}})}
	for _, o := range []any{nil, "", false, float64(0)} {
		build(t, mk(o), opts, []any{plain})
	}
	withObfs := M{"tag": "share-h", "listen": "::", "listen_port": float64(5443), "type": "hysteria2",
		"users": []any{M{"password": "p"}}, "tls": tlsWith(M{"alpn": []any{"h3"}}),
		"obfs": M{"type": "salamander", "password": "secret"}}
	build(t, mk("secret"), opts, []any{withObfs})
}

func TestMixed(t *testing.T) {
	head := func(extra M) M {
		m := M{"tag": "share-m", "listen": "::", "listen_port": float64(1080), "type": "mixed"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	mk := func(extra M) []any {
		s := M{"id": "m", "protocol": "mixed", "port": float64(1080)}
		for k, v := range extra {
			s[k] = v
		}
		return []any{s}
	}
	build(t, mk(nil), opts, []any{head(nil)})                                // 不认证:没有 users
	build(t, mk(M{"username": "", "password": "x"}), opts, []any{head(nil)}) // 空用户名也是不认证
	build(t, mk(M{"username": "u", "password": "p"}), opts, []any{head(M{"users": []any{M{"username": "u", "password": "p"}}})})
	build(t, mk(M{"username": "u"}), opts, []any{head(M{"users": []any{M{"username": "u", "password": ""}}})})
	build(t, mk(M{"username": "u", "password": nil}), opts, []any{head(M{"users": []any{M{"username": "u", "password": ""}}})})
	build(t, mk(M{"username": "u", "password": ""}), opts, []any{head(M{"users": []any{M{"username": "u", "password": ""}}})})
}

func TestFilteringAndOrder(t *testing.T) {
	ss := func(id string, extra M) M {
		m := M{"id": id, "protocol": "shadowsocks", "port": float64(1), "method": "m", "password": "p"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	servers := []any{
		ss("a", nil),
		nil,                                // 空项
		false,                              // 假值
		ss("off", M{"enabled": false}),     // 显式关闭
		M{"id": "u", "protocol": "socks4"}, // 不认识的协议
		M{"id": "n"},                       // 没有协议
		ss("b", M{"enabled": nil}),         // 只有布尔 false 才算关闭
		ss("c", M{"enabled": "false"}),
		ss("d", M{"enabled": float64(0)}),
		ss("e", M{"enabled": true}),
	}
	got := BuildServerInbounds(servers, opts)
	var tags []string
	for _, x := range got {
		tags = append(tags, x.(map[string]any)["tag"].(string))
	}
	if want := []string{"share-a", "share-b", "share-c", "share-d", "share-e"}; !reflect.DeepEqual(tags, want) {
		t.Errorf("tags = %v, want %v", tags, want)
	}
}

func TestCertOptionsAndMissingPort(t *testing.T) {
	servers := []any{M{"id": "v", "protocol": "vless", "uuid": "u", "tls": true}}
	noCert := M{"tag": "share-v", "listen": "::", "type": "vless", "users": []any{M{"uuid": "u"}},
		"tls": M{"enabled": true, "server_name": "open-box.local"}}
	build(t, servers, nil, []any{noCert}) // 没给证书:tls 里不带路径键;没给端口:不带 listen_port
	build(t, servers, M{"certPath": "/only.pem"}, []any{M{"tag": "share-v", "listen": "::", "type": "vless", "users": []any{M{"uuid": "u"}},
		"tls": M{"enabled": true, "server_name": "open-box.local", "certificate_path": "/only.pem"}}})
}

func TestTLSServerNameConst(t *testing.T) {
	if TLSServerName != "open-box.local" {
		t.Errorf("TLSServerName = %q", TLSServerName)
	}
}
