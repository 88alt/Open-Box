// Package shareservers 校验并规整档案里的 servers(「共享网络」:本机开给局域网 / 别的设备连的入站)。
//
// 自己的设计,规则取自前端 ServerEditDialog.vue 保存前的校验(名称、端口、各协议必填字段、mixed 的用户名密码成对、
// 端口不重复)、OpenboxServer 的类型,以及 sing-box 官方文档对各入站字段的要求(UUID 格式、Shadowsocks 2022 密钥长度),
// 没有参照上游。目的是让不合格的服务器在保存时就被拒绝,而不是等到部署时被 sing-box check 拦下。
package shareservers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	// MaxServers 是共享服务器的条数上限(前端没有限制,这里是防止异常输入的保险)。
	MaxServers   = 64
	maxIDLen     = 64
	maxNameLen   = 80
	maxAddrLen   = 253
	maxOutbound  = 128
	maxSecretLen = 256
)

var protocols = map[string]bool{"shadowsocks": true, "vless": true, "tuic": true, "hysteria2": true, "mixed": true}

// ssKeyBytes 是 Shadowsocks 各加密方式要求的密钥字节数:0 = 普通 AEAD(密码任意非空),>0 = 2022 系列(密码必须是该长度密钥的 base64)。
var ssMethods = map[string]int{
	"aes-128-gcm":                   0,
	"aes-192-gcm":                   0,
	"aes-256-gcm":                   0,
	"chacha20-ietf-poly1305":        0,
	"xchacha20-ietf-poly1305":       0,
	"2022-blake3-aes-128-gcm":       16,
	"2022-blake3-aes-256-gcm":       32,
	"2022-blake3-chacha20-poly1305": 32,
}

// Normalize 校验 servers 并返回规整后的副本(入参不改):字符串去首尾空白;空的 address / outbound / obfs 去掉键;
// 其余未知字段原样保留。出错返回给人看的中文原因,不部分应用。
func Normalize(v any) ([]any, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("servers 必须是数组")
	}
	if len(list) > MaxServers {
		return nil, fmt.Errorf("共享服务器最多 %d 台", MaxServers)
	}
	out := make([]any, 0, len(list))
	ids := map[string]int{}
	ports := map[int]int{}
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("第 %d 台服务器必须是对象", i+1)
		}
		n, err := normalizeOne(m)
		if err != nil {
			return nil, fmt.Errorf("第 %d 台服务器%s: %w", i+1, label(m), err)
		}
		id := n["id"].(string)
		if j, dup := ids[id]; dup {
			return nil, fmt.Errorf("第 %d 台和第 %d 台服务器的 id 重复(%s)", j+1, i+1, id)
		}
		ids[id] = i
		port := n["port"].(int)
		if j, dup := ports[port]; dup {
			return nil, fmt.Errorf("第 %d 台和第 %d 台服务器用了同一个端口 %d", j+1, i+1, port)
		}
		ports[port] = i
		out = append(out, n)
	}
	return out, nil
}

func label(m map[string]any) string {
	if s, ok := m["name"].(string); ok {
		if s = strings.TrimSpace(s); s != "" {
			return "「" + truncate(s, 20) + "」"
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func normalizeOne(in map[string]any) (map[string]any, error) {
	m := make(map[string]any, len(in))
	for k, v := range in {
		m[k] = v
	}

	id, err := str(m, "id")
	if err != nil {
		return nil, err
	}
	if id == "" || len(id) > maxIDLen || !idChars(id) {
		return nil, fmt.Errorf("id 只能由字母、数字、下划线和短横线组成,长度 1–%d", maxIDLen)
	}
	m["id"] = id

	if v, has := m["enabled"]; has && v != nil {
		if _, ok := v.(bool); !ok {
			return nil, fmt.Errorf("enabled 必须是 true 或 false")
		}
	}

	name, err := str(m, "name")
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("名称不能为空")
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		return nil, fmt.Errorf("名称太长(上限 %d 字)", maxNameLen)
	}
	m["name"] = name

	proto, err := str(m, "protocol")
	if err != nil {
		return nil, err
	}
	if !protocols[proto] {
		return nil, fmt.Errorf("协议只能是 shadowsocks / vless / tuic / hysteria2 / mixed")
	}
	m["protocol"] = proto

	port, err := intOf(m["port"])
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("端口必须是 1–65535 的整数")
	}
	m["port"] = port

	if err := optStr(m, "address", maxAddrLen); err != nil {
		return nil, err
	}
	if a, _ := m["address"].(string); a != "" && !hostOK(a) {
		return nil, fmt.Errorf("连接地址只能是域名或 IP(不带端口、路径和空格)")
	}
	if err := optStr(m, "outbound", maxOutbound); err != nil {
		return nil, err
	}

	switch proto {
	case "shadowsocks":
		method, err := str(m, "method")
		if err != nil {
			return nil, err
		}
		keyLen, known := ssMethods[method]
		if !known {
			return nil, fmt.Errorf("加密方式不支持:%q", method)
		}
		m["method"] = method
		pw, err := secret(m, "password")
		if err != nil {
			return nil, err
		}
		if keyLen > 0 {
			raw, err := base64.StdEncoding.DecodeString(pw)
			if err != nil || len(raw) != keyLen {
				return nil, fmt.Errorf("%s 的密码必须是 %d 字节密钥的 base64(例如 openssl rand -base64 %d 的输出)", method, keyLen, keyLen)
			}
		}
	case "vless":
		if _, err := uuid(m); err != nil {
			return nil, err
		}
		if v, has := m["tls"]; has && v != nil {
			if _, ok := v.(bool); !ok {
				return nil, fmt.Errorf("tls 必须是 true 或 false")
			}
		}
	case "tuic":
		if _, err := uuid(m); err != nil {
			return nil, err
		}
		if _, err := secret(m, "password"); err != nil {
			return nil, err
		}
	case "hysteria2":
		if _, err := secret(m, "password"); err != nil {
			return nil, err
		}
		if err := optStr(m, "obfs", maxSecretLen); err != nil {
			return nil, err
		}
	case "mixed":
		if err := optStr(m, "username", maxSecretLen); err != nil {
			return nil, err
		}
		if err := optStr(m, "password", maxSecretLen); err != nil {
			return nil, err
		}
		u, _ := m["username"].(string)
		p, _ := m["password"].(string)
		if (u != "") != (p != "") {
			return nil, fmt.Errorf("SOCKS5 / HTTP 的用户名和密码要么都填,要么都不填")
		}
	}
	return m, nil
}

// str 取字符串字段并去首尾空白;缺省视为空串(由调用方判空);类型不对报错。
func str(m map[string]any, key string) (string, error) {
	v, has := m[key]
	if !has || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s 必须是字符串", key)
	}
	return strings.TrimSpace(s), nil
}

// optStr 可选字符串:规整成去空白的值,空的就去掉这个键。
func optStr(m map[string]any, key string, max int) error {
	s, err := str(m, key)
	if err != nil {
		return err
	}
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%s 太长(上限 %d 字符)", key, max)
	}
	if s == "" {
		delete(m, key)
		return nil
	}
	m[key] = s
	return nil
}

// secret 必填的凭据字符串(密码),去首尾空白后不能为空。
func secret(m map[string]any, key string) (string, error) {
	s, err := str(m, key)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("%s 不能为空", key)
	}
	if utf8.RuneCountInString(s) > maxSecretLen {
		return "", fmt.Errorf("%s 太长(上限 %d 字符)", key, maxSecretLen)
	}
	m[key] = s
	return s, nil
}

func uuid(m map[string]any) (string, error) {
	s, err := str(m, "uuid")
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("uuid 不能为空")
	}
	if !uuidOK(s) {
		return "", fmt.Errorf("uuid 格式不对(应为 8-4-4-4-12 的十六进制,或 32 位十六进制)")
	}
	m["uuid"] = s
	return s, nil
}

func uuidOK(s string) bool {
	hex := func(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
	switch len(s) {
	case 32:
		for i := 0; i < 32; i++ {
			if !hex(s[i]) {
				return false
			}
		}
		return true
	case 36:
		for i := 0; i < 36; i++ {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if s[i] != '-' {
					return false
				}
			} else if !hex(s[i]) {
				return false
			}
		}
		return true
	}
	return false
}

func idChars(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// hostOK:地址只用来拼分享链接,必须是单独的主机名 / IP,不能带端口、路径、账号、空白、控制字符。
// 允许 IPv6(含方括号)。
func hostOK(s string) bool {
	for _, r := range s {
		if r <= ' ' || r == 0x7f || r == '/' || r == '?' || r == '#' || r == '@' || r == '\\' {
			return false
		}
	}
	if strings.HasPrefix(s, "[") {
		return strings.HasSuffix(s, "]") && strings.Count(s, "]") == 1 && strings.Contains(s, ":")
	}
	if strings.Count(s, ":") == 1 { // host:port
		return false
	}
	return true
}

func intOf(v any) (int, error) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || math.IsInf(n, 0) || math.IsNaN(n) || math.Abs(n) > 1<<31 {
			return 0, fmt.Errorf("not integer")
		}
		return int(n), nil
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		return int(i), err
	}
	return 0, fmt.Errorf("not integer")
}
