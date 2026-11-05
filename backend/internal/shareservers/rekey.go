package shareservers

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// Rekey 给一台共享服务器换一个新的随机凭据,原地改 m(m 必须是已经过 Normalize 的一条),返回换了哪个字段(没有可换的返回空串)。
// 「哪个字段算密码」按协议定,依据本包的校验和前端 OpenboxServer 的类型:
//
//	shadowsocks  password(2022 系列按所需字节数生成 base64,普通 AEAD 生成 24 个字符的随机串)
//	vless        uuid(随机 v4)
//	tuic         password(uuid 只是用户标识,不换)
//	hysteria2    password(obfs 是另一个共享口令,不动)
//	mixed        用户名和密码都设了才换 password;没设认证的入站不能凭空加上认证,否则会断掉现有的使用者,返回空串
//
// rnd 为 nil 时用 crypto/rand。换完的值一定能再过 Normalize。
func Rekey(m map[string]any, rnd io.Reader) (string, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	proto, _ := m["protocol"].(string)
	switch proto {
	case "shadowsocks":
		method, _ := m["method"].(string)
		keyLen, known := ssMethods[method]
		if !known {
			return "", fmt.Errorf("加密方式不支持:%q", method)
		}
		var pw string
		var err error
		if keyLen > 0 {
			pw, err = randB64(rnd, keyLen, base64.StdEncoding)
		} else {
			pw, err = randB64(rnd, 18, base64.RawURLEncoding)
		}
		if err != nil {
			return "", err
		}
		m["password"] = pw
		return "password", nil
	case "vless":
		id, err := randUUID(rnd)
		if err != nil {
			return "", err
		}
		m["uuid"] = id
		return "uuid", nil
	case "tuic", "hysteria2":
		pw, err := randB64(rnd, 18, base64.RawURLEncoding)
		if err != nil {
			return "", err
		}
		m["password"] = pw
		return "password", nil
	case "mixed":
		u, _ := m["username"].(string)
		p, _ := m["password"].(string)
		if u == "" || p == "" {
			return "", nil
		}
		pw, err := randB64(rnd, 18, base64.RawURLEncoding)
		if err != nil {
			return "", err
		}
		m["password"] = pw
		return "password", nil
	}
	return "", fmt.Errorf("协议只能是 shadowsocks / vless / tuic / hysteria2 / mixed")
}

func randB64(r io.Reader, n int, enc *base64.Encoding) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	return enc.EncodeToString(b), nil
}

func randUUID(r io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // 版本 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 变体
	const hexd = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, c := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexd[c>>4], hexd[c&0x0f])
	}
	return string(out), nil
}
