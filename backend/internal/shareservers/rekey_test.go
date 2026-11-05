package shareservers

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestRekeyEachProtocol(t *testing.T) {
	cases := []struct {
		name  string
		in    map[string]any
		field string // 期望被换的字段,空 = 不换
	}{
		{"ss 2022-128", map[string]any{"id": "a", "name": "A", "protocol": "shadowsocks", "port": 1, "method": "2022-blake3-aes-128-gcm", "password": base64.StdEncoding.EncodeToString(make([]byte, 16))}, "password"},
		{"ss 2022-256", map[string]any{"id": "a", "name": "A", "protocol": "shadowsocks", "port": 1, "method": "2022-blake3-aes-256-gcm", "password": base64.StdEncoding.EncodeToString(make([]byte, 32))}, "password"},
		{"ss aead", map[string]any{"id": "a", "name": "A", "protocol": "shadowsocks", "port": 1, "method": "aes-256-gcm", "password": "old"}, "password"},
		{"vless", map[string]any{"id": "a", "name": "A", "protocol": "vless", "port": 1, "uuid": "11111111-2222-3333-4444-555555555555"}, "uuid"},
		{"tuic", map[string]any{"id": "a", "name": "A", "protocol": "tuic", "port": 1, "uuid": "11111111-2222-3333-4444-555555555555", "password": "old"}, "password"},
		{"hysteria2", map[string]any{"id": "a", "name": "A", "protocol": "hysteria2", "port": 1, "password": "old", "obfs": "keep"}, "password"},
		{"mixed 有认证", map[string]any{"id": "a", "name": "A", "protocol": "mixed", "port": 1, "username": "u", "password": "old"}, "password"},
		{"mixed 无认证", map[string]any{"id": "a", "name": "A", "protocol": "mixed", "port": 1}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range c.in {
				m[k] = v
			}
			got, err := Rekey(m, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.field {
				t.Fatalf("换的字段 = %q,期望 %q", got, c.field)
			}
			if c.field == "" {
				if len(m) != len(c.in) {
					t.Fatalf("没有可换的凭据时不该改动: %+v", m)
				}
				return
			}
			if m[c.field] == c.in[c.field] {
				t.Fatalf("%s 没有变", c.field)
			}
			for k, v := range c.in { // 其它字段(含 obfs、uuid 等)原样
				if k != c.field && m[k] != v {
					t.Fatalf("%s 不该变: %v -> %v", k, v, m[k])
				}
			}
			if _, err := Normalize([]any{m}); err != nil {
				t.Fatalf("换完应能再过校验: %v", err)
			}
		})
	}
}

func TestRekeyRandomAndStable(t *testing.T) {
	mk := func() map[string]any {
		return map[string]any{"id": "a", "name": "A", "protocol": "vless", "port": 1, "uuid": "11111111-2222-3333-4444-555555555555"}
	}
	seed := bytes.Repeat([]byte{7}, 64)
	a, b := mk(), mk()
	if _, err := Rekey(a, bytes.NewReader(seed)); err != nil {
		t.Fatal(err)
	}
	if _, err := Rekey(b, bytes.NewReader(seed)); err != nil {
		t.Fatal(err)
	}
	if a["uuid"] != b["uuid"] {
		t.Fatal("同样的随机源应得到同样的值(便于测试)")
	}
	u := a["uuid"].(string)
	if len(u) != 36 || u[14] != '4' || !(u[19] == '8' || u[19] == '9' || u[19] == 'a' || u[19] == 'b') {
		t.Fatalf("应是 v4 UUID: %s", u)
	}
	c, d := mk(), mk()
	_, _ = Rekey(c, nil)
	_, _ = Rekey(d, nil)
	if c["uuid"] == d["uuid"] {
		t.Fatal("系统随机源两次不该相同")
	}
}

func TestRekeyErrors(t *testing.T) {
	if _, err := Rekey(map[string]any{"protocol": "nope"}, nil); err == nil {
		t.Error("未知协议要报错")
	}
	if _, err := Rekey(map[string]any{"protocol": "shadowsocks", "method": "rc4"}, nil); err == nil {
		t.Error("不支持的加密方式要报错")
	}
	if _, err := Rekey(map[string]any{"protocol": "vless"}, bytes.NewReader([]byte{1, 2})); err == nil {
		t.Error("随机源不够要报错,不能换成半截的值")
	}
}
