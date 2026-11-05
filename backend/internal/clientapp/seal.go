package clientapp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
)

// 配对码封包:对齐上游 v0.1.402 面板服务端(catch-up-upstream 的 panel/server/index.mjs 可读部分)。
// 维护者 2026-11-05 授权参考上游闭源产物 / engine-snapshot 相关部分。
//
// 导出 / 同步密文外形(与上游 open-box-client-sealed 一致):
//
//	密钥 = PBKDF2-HMAC-SHA256(配对码, salt, 200000, 32 字节)
//	salt = 每个封包随机的 16 字节(写进 kdf.salt,base64url)
//	data = base64url( iv(12) || ciphertext || tag(16) )  // AES-256-GCM,无 AAD
//
// 配对码:4 个字符,字母表 ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789。
// nonce:12 字节随机 → 24 位十六进制(进二维码,不参与派生)。

const (
	SealVersion = 1
	// SealKDF 是封包里 kdf.alg 的取值。
	SealKDF = "pbkdf2-sha256"
	// DefaultSealIter 对齐上游 zf=2e5。
	DefaultSealIter = 200000

	sealKeyLen = 32
	saltLen    = 16
	ivLen      = 12
	tagLen     = 16
)

// ErrSealOpen 是解不开封包(配对码不对、封包被改、格式不认识)。不区分原因。
var ErrSealOpen = errors.New("封包解不开")

var b64 = base64.RawURLEncoding

// Envelope 是上游导出文件里的加密部分:kdf + data。
// 导出文件顶层还会带 format / version / router;这里只描述可解开的密文。
type Envelope struct {
	KDF  KDFParams `json:"kdf"`
	Data string    `json:"data"`
}

// KDFParams 是派生参数。
type KDFParams struct {
	Alg        string `json:"alg"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
}

// keyCache 缓存「同一把配对码 + 同一份 salt + 同一迭代次数」的密钥,避免公开接口反复算 20 万次。
type keyCache struct {
	mu   sync.Mutex
	pair string
	salt string
	iter int
	key  []byte
}

func (c *keyCache) get(pair string, salt []byte, iter int) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != nil && c.pair == pair && c.salt == string(salt) && c.iter == iter {
		return c.key, nil
	}
	k, err := pbkdf2.Key(sha256.New, pair, salt, iter, sealKeyLen)
	if err != nil {
		return nil, fmt.Errorf("派生密钥失败: %w", err)
	}
	c.pair, c.salt, c.iter, c.key = pair, string(salt), iter, k
	return k, nil
}

func gcmOf(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// seal 用配对码封 plain。rnd 为 nil 用 crypto/rand。
func seal(pair string, iter int, plain []byte, rnd io.Reader) (Envelope, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	if iter <= 0 {
		iter = DefaultSealIter
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rnd, salt); err != nil {
		return Envelope{}, fmt.Errorf("生成随机数失败: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, pair, salt, iter, sealKeyLen)
	if err != nil {
		return Envelope{}, fmt.Errorf("派生密钥失败: %w", err)
	}
	g, err := gcmOf(key)
	if err != nil {
		return Envelope{}, err
	}
	iv := make([]byte, ivLen)
	if _, err := io.ReadFull(rnd, iv); err != nil {
		return Envelope{}, fmt.Errorf("生成随机数失败: %w", err)
	}
	ct := g.Seal(nil, iv, plain, nil)
	// ct 已含 tag;data = iv || ct
	data := make([]byte, 0, ivLen+len(ct))
	data = append(data, iv...)
	data = append(data, ct...)
	return Envelope{
		KDF:  KDFParams{Alg: SealKDF, Iterations: iter, Salt: b64.EncodeToString(salt)},
		Data: b64.EncodeToString(data),
	}, nil
}

// OpenEnvelope 用配对码解开封包。
// 迭代次数低于 minIter(或高到离谱)的封包不认,防止被降级成秒破的参数。
func OpenEnvelope(e Envelope, pairCode string, minIter, maxIter int) ([]byte, error) {
	if e.KDF.Alg != SealKDF || e.KDF.Iterations < minIter || e.KDF.Iterations > maxIter {
		return nil, ErrSealOpen
	}
	salt, err1 := b64.DecodeString(e.KDF.Salt)
	raw, err2 := b64.DecodeString(e.Data)
	if err1 != nil || err2 != nil || len(salt) != saltLen || len(raw) < ivLen+tagLen {
		return nil, ErrSealOpen
	}
	key, err := pbkdf2.Key(sha256.New, pairCode, salt, e.KDF.Iterations, sealKeyLen)
	if err != nil {
		return nil, ErrSealOpen
	}
	g, err := gcmOf(key)
	if err != nil {
		return nil, ErrSealOpen
	}
	iv, ct := raw[:ivLen], raw[ivLen:]
	plain, err := g.Open(nil, iv, ct, nil)
	if err != nil {
		return nil, ErrSealOpen
	}
	return plain, nil
}
