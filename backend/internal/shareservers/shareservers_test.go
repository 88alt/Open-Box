package shareservers

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func parse(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

const (
	u1  = "0b8e7c0a-3a1d-4f0e-9a52-6c1f2d3e4a5b"
	k32 = "q1w2e3r4t5y6u7i8o9p0a1s2d3f4g5h6j7k8l9z0x1c=" // 44 字符 base64 = 32 字节
)

// 一台合法的服务器:五种协议各一台,字段都是前端 ServerEditDialog 会发出来的样子。
func TestNormalizeValidFiveProtocols(t *testing.T) {
	in := parse(t, `[
	 {"id":"s1","enabled":true,"name":" 客厅 SS ","protocol":"shadowsocks","port":8388,"address":" 192.168.1.1 ","method":"aes-256-gcm","password":" pw "},
	 {"id":"s2","enabled":false,"name":"V","protocol":"vless","port":8443,"address":"","uuid":"`+u1+`","tls":true},
	 {"id":"s3","enabled":true,"name":"T","protocol":"tuic","port":8444,"uuid":"`+strings.ToUpper(u1)+`","password":"p","outbound":" 节点组 A "},
	 {"id":"s4","enabled":true,"name":"H","protocol":"hysteria2","port":8445,"password":"p","obfs":"  "},
	 {"id":"s5","enabled":true,"name":"M","protocol":"mixed","port":7080,"username":"","password":""}
	]`)
	got, err := Normalize(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatal(len(got))
	}
	s1 := got[0].(map[string]any)
	if s1["name"] != "客厅 SS" || s1["address"] != "192.168.1.1" || s1["password"] != "pw" || s1["port"] != 8388 {
		t.Errorf("s1 应去空白: %v", s1)
	}
	if _, has := got[1].(map[string]any)["address"]; has {
		t.Errorf("空 address 应去掉键: %v", got[1])
	}
	if got[2].(map[string]any)["outbound"] != "节点组 A" {
		t.Errorf("outbound 应去空白: %v", got[2])
	}
	if _, has := got[3].(map[string]any)["obfs"]; has {
		t.Errorf("空 obfs 应去掉键: %v", got[3])
	}
	// mixed 不认证:用户名和密码都空,键保留为空串或去掉都行,但不能报错;认证时两者都在
	m := got[4].(map[string]any)
	if u, _ := m["username"].(string); u != "" {
		t.Errorf("%v", m)
	}
}

// 入参不被改动。
func TestNormalizeDoesNotMutateInput(t *testing.T) {
	in := parse(t, `[{"id":"s1","name":" a ","protocol":"hysteria2","port":8445,"password":" p ","obfs":" "}]`)
	before := parse(t, `[{"id":"s1","name":" a ","protocol":"hysteria2","port":8445,"password":" p ","obfs":" "}]`)
	if _, err := Normalize(in); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatalf("入参被改了: %v", in)
	}
}

// 未知字段原样保留(前端以后加字段,旧后端不该悄悄丢掉)。
func TestNormalizeKeepsUnknownFields(t *testing.T) {
	got, err := Normalize(parse(t, `[{"id":"s1","name":"a","protocol":"mixed","port":7080,"future":{"x":1}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got[0].(map[string]any)["future"], map[string]any{"x": float64(1)}) {
		t.Fatalf("%v", got[0])
	}
}

func TestNormalizeEmptyAndNotArray(t *testing.T) {
	if got, err := Normalize([]any{}); err != nil || len(got) != 0 || got == nil {
		t.Fatalf("空数组合法,且结果是非 nil 的空数组: %v %v", got, err)
	}
	for _, v := range []any{map[string]any{}, "x", 5.0, true} {
		if _, err := Normalize(v); err == nil || !strings.Contains(err.Error(), "必须是数组") {
			t.Errorf("%v: %v", v, err)
		}
	}
}

func TestNormalizeRejects(t *testing.T) {
	ok := `{"id":"s1","name":"a","protocol":"shadowsocks","port":8388,"method":"aes-256-gcm","password":"p"}`
	cases := []struct{ name, json, want string }{
		{"不是对象", `["x"]`, "必须是对象"},
		{"没有 id", `[{"name":"a","protocol":"mixed","port":7080}]`, "id 只能由"},
		{"id 含空格", `[{"id":"a b","name":"a","protocol":"mixed","port":7080}]`, "id 只能由"},
		{"id 含斜杠", `[{"id":"a/b","name":"a","protocol":"mixed","port":7080}]`, "id 只能由"},
		{"id 太长", `[{"id":"` + strings.Repeat("a", 65) + `","name":"a","protocol":"mixed","port":7080}]`, "id 只能由"},
		{"id 不是字符串", `[{"id":5,"name":"a","protocol":"mixed","port":7080}]`, "id 必须是字符串"},
		{"enabled 不是布尔", `[{"id":"a","enabled":"yes","name":"a","protocol":"mixed","port":7080}]`, "enabled 必须是"},
		{"没有名称", `[{"id":"a","protocol":"mixed","port":7080}]`, "名称不能为空"},
		{"名称全是空白", `[{"id":"a","name":"   ","protocol":"mixed","port":7080}]`, "名称不能为空"},
		{"名称太长", `[{"id":"a","name":"` + strings.Repeat("长", 81) + `","protocol":"mixed","port":7080}]`, "名称太长"},
		{"协议不认识", `[{"id":"a","name":"a","protocol":"trojan","port":7080}]`, "协议只能是"},
		{"没有协议", `[{"id":"a","name":"a","port":7080}]`, "协议只能是"},
		{"端口 0", `[{"id":"a","name":"a","protocol":"mixed","port":0}]`, "端口必须是"},
		{"端口 65536", `[{"id":"a","name":"a","protocol":"mixed","port":65536}]`, "端口必须是"},
		{"端口小数", `[{"id":"a","name":"a","protocol":"mixed","port":80.5}]`, "端口必须是"},
		{"端口是字符串", `[{"id":"a","name":"a","protocol":"mixed","port":"80"}]`, "端口必须是"},
		{"没有端口", `[{"id":"a","name":"a","protocol":"mixed"}]`, "端口必须是"},
		{"地址带端口", `[{"id":"a","name":"a","protocol":"mixed","port":7080,"address":"x.com:80"}]`, "连接地址"},
		{"地址带路径", `[{"id":"a","name":"a","protocol":"mixed","port":7080,"address":"x.com/a"}]`, "连接地址"},
		{"地址带空格", `[{"id":"a","name":"a","protocol":"mixed","port":7080,"address":"x .com"}]`, "连接地址"},
		{"地址带账号", `[{"id":"a","name":"a","protocol":"mixed","port":7080,"address":"u@x.com"}]`, "连接地址"},
		{"地址括号不配对", `[{"id":"a","name":"a","protocol":"mixed","port":7080,"address":"[::1"}]`, "连接地址"},
		{"地址不是字符串", `[{"id":"a","name":"a","protocol":"mixed","port":7080,"address":5}]`, "address 必须是字符串"},
		{"出口太长", `[{"id":"a","name":"a","protocol":"mixed","port":7080,"outbound":"` + strings.Repeat("x", 129) + `"}]`, "outbound 太长"},
		{"id 重复", `[` + ok + `,{"id":"s1","name":"b","protocol":"mixed","port":7080}]`, "id 重复"},
		{"端口重复", `[` + ok + `,{"id":"s2","name":"b","protocol":"mixed","port":8388}]`, "同一个端口 8388"},
		{"SS 加密方式缺失", `[{"id":"a","name":"a","protocol":"shadowsocks","port":1,"password":"p"}]`, "加密方式不支持"},
		{"SS 加密方式不认识", `[{"id":"a","name":"a","protocol":"shadowsocks","port":1,"method":"rc4","password":"p"}]`, "加密方式不支持"},
		{"SS 没密码", `[{"id":"a","name":"a","protocol":"shadowsocks","port":1,"method":"aes-256-gcm"}]`, "password 不能为空"},
		{"SS 密码全空白", `[{"id":"a","name":"a","protocol":"shadowsocks","port":1,"method":"aes-256-gcm","password":"  "}]`, "password 不能为空"},
		{"SS 2022 密钥不是 base64", `[{"id":"a","name":"a","protocol":"shadowsocks","port":1,"method":"2022-blake3-aes-256-gcm","password":"不是密钥"}]`, "32 字节密钥的 base64"},
		{"SS 2022 密钥长度不对", `[{"id":"a","name":"a","protocol":"shadowsocks","port":1,"method":"2022-blake3-aes-256-gcm","password":"AAAAAAAAAAAAAAAAAAAAAA=="}]`, "32 字节密钥的 base64"},
		{"SS 2022-128 要 16 字节", `[{"id":"a","name":"a","protocol":"shadowsocks","port":1,"method":"2022-blake3-aes-128-gcm","password":"` + k32 + `"}]`, "16 字节密钥的 base64"},
		{"vless 没 uuid", `[{"id":"a","name":"a","protocol":"vless","port":1}]`, "uuid 不能为空"},
		{"vless uuid 格式不对", `[{"id":"a","name":"a","protocol":"vless","port":1,"uuid":"not-a-uuid"}]`, "uuid 格式不对"},
		{"vless uuid 连字符位置不对", `[{"id":"a","name":"a","protocol":"vless","port":1,"uuid":"0b8e7c0a3-a1d-4f0e-9a52-6c1f2d3e4a5b"}]`, "uuid 格式不对"},
		{"vless tls 不是布尔", `[{"id":"a","name":"a","protocol":"vless","port":1,"uuid":"` + u1 + `","tls":"yes"}]`, "tls 必须是"},
		{"tuic 没密码", `[{"id":"a","name":"a","protocol":"tuic","port":1,"uuid":"` + u1 + `"}]`, "password 不能为空"},
		{"tuic 没 uuid", `[{"id":"a","name":"a","protocol":"tuic","port":1,"password":"p"}]`, "uuid 不能为空"},
		{"hy2 没密码", `[{"id":"a","name":"a","protocol":"hysteria2","port":1}]`, "password 不能为空"},
		{"hy2 obfs 不是字符串", `[{"id":"a","name":"a","protocol":"hysteria2","port":1,"password":"p","obfs":1}]`, "obfs 必须是字符串"},
		{"mixed 只有用户名", `[{"id":"a","name":"a","protocol":"mixed","port":1,"username":"u"}]`, "要么都填"},
		{"mixed 只有密码", `[{"id":"a","name":"a","protocol":"mixed","port":1,"password":"p"}]`, "要么都填"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Normalize(parse(t, c.json))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("想要含 %q 的错误,实际: %v", c.want, err)
			}
			// 错误里带是第几台,方便定位
			if !strings.Contains(err.Error(), "第") && !strings.Contains(err.Error(), "必须是数组") {
				t.Errorf("错误应指出是第几台: %v", err)
			}
		})
	}
}

func TestNormalizeAcceptsEdgeValues(t *testing.T) {
	for _, js := range []string{
		`[{"id":"a","name":"a","protocol":"mixed","port":1}]`,
		`[{"id":"a","name":"a","protocol":"mixed","port":65535}]`,
		`[{"id":"a","name":"a","protocol":"mixed","port":7080,"username":"u","password":"p"}]`,
		`[{"id":"a","name":"a","protocol":"mixed","port":7080,"address":"[2001:db8::1]"}]`,
		`[{"id":"a","name":"a","protocol":"mixed","port":7080,"address":"2001:db8::1"}]`,
		`[{"id":"a","name":"a","protocol":"mixed","port":7080,"address":"router.lan"}]`,
		`[{"id":"A_b-9","name":"a","protocol":"mixed","port":7080}]`,
		`[{"id":"a","name":"` + strings.Repeat("长", 80) + `","protocol":"mixed","port":7080}]`,
		`[{"id":"a","name":"a","protocol":"vless","port":7080,"uuid":"0b8e7c0a3a1d4f0e9a526c1f2d3e4a5b"}]`,
		`[{"id":"a","name":"a","protocol":"shadowsocks","port":1,"method":"2022-blake3-aes-256-gcm","password":"` + k32 + `"}]`,
		`[{"id":"a","name":"a","protocol":"shadowsocks","port":1,"method":"2022-blake3-aes-128-gcm","password":"AAAAAAAAAAAAAAAAAAAAAA=="}]`,
		`[{"id":"a","name":"a","protocol":"shadowsocks","port":1,"method":"chacha20-ietf-poly1305","password":"任意密码"}]`,
	} {
		if _, err := Normalize(parse(t, js)); err != nil {
			t.Errorf("%s: %v", js, err)
		}
	}
	// 条数上限
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i <= MaxServers; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"id":"s` + itoa(i) + `","name":"a","protocol":"mixed","port":` + itoa(10000+i) + `}`)
	}
	sb.WriteString("]")
	if _, err := Normalize(parse(t, sb.String())); err == nil || !strings.Contains(err.Error(), "最多") {
		t.Fatalf("超过上限应报错: %v", err)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
