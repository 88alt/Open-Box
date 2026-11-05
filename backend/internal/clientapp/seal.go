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

// 配对码封包(client-config/file,以后也给 App 用的公开同步接口和 share-codes 用)。
// 自己定义的格式,带版本号 v:1,依据 DESIGN 第 8 节「配对码封包」的决定和 NIST SP 800-132 / RFC 8018
// (PBKDF2)、NIST SP 800-38D(GCM)的公开规范,没有参照上游;与上游 App 不互通。
//
// 封包是 JSON 对象 {v, kdf, iter, salt, iv, ct},三个二进制字段都是不带填充的 base64url:
//
//	密钥 = PBKDF2-HMAC-SHA256(配对码, salt, iter, 32 字节)
//	salt = SHA-256("open-box/client-config/v1" 0x00 routerId 0x00 nonce) 的前 16 字节
//	iv   = 每个封包随机的 12 字节
//	ct   = AES-256-GCM 密文加 16 字节标签;附加认证数据 AAD = "open-box/client-config/v1" 0x00 routerId 0x00 nonce
//
// salt 由路由器 ID 和 nonce 决定、不是每个封包随机的:这样同一个配对码周期内密钥只需派生一次
// (配对码和 nonce 一起换,换码即换密钥),请求来了查内存缓存,不再现算 60 万次(否则公开接口会变成吃 CPU 的入口)。
// iv 仍是每个封包随机,同一把密钥封的封包数远小于 2^32,随机 96 位 iv 的碰撞概率可忽略。
// AAD 把封包绑在这台路由器和这一代配对上:拿别家的封包、换码前的封包塞进来,GCM 校验都会失败。
const (
	SealVersion = 1
	// SealKDF 是封包里 kdf 字段的取值。
	SealKDF = "pbkdf2-sha256"
	// DefaultSealIter 与 DESIGN D7 的密码哈希同一个迭代次数。
	DefaultSealIter = 600000

	sealDomain = "open-box/client-config/v1"
	sealKeyLen = 32
	saltLen    = 16
	ivLen      = 12
)

// ErrSealOpen 是解不开封包(配对码不对、封包被改、不是这一代的、格式不认识)。不区分原因,不给攻击者多余信息。
var ErrSealOpen = errors.New("封包解不开")

// Envelope 是封好的内容。
type Envelope struct {
	V    int    `json:"v"`
	KDF  string `json:"kdf"`
	Iter int    `json:"iter"`
	Salt string `json:"salt"`
	IV   string `json:"iv"`
	CT   string `json:"ct"`
}

var b64 = base64.RawURLEncoding

func sealSalt(routerID, nonce string) []byte {
	h := sha256.Sum256([]byte(sealDomain + "\x00" + routerID + "\x00" + nonce))
	return h[:saltLen]
}

func sealAAD(routerID, nonce string) []byte {
	return []byte(sealDomain + "\x00" + routerID + "\x00" + nonce)
}

// keyCache 只缓存「当前这一代」的一把密钥:配对码或 nonce 一换,条目对不上,重新派生并顶掉旧的。
// 密钥只在内存里,不落盘。
type keyCache struct {
	mu   sync.Mutex
	pair string
	salt string
	iter int
	key  []byte
}

// get 取 (配对码, salt, iter) 对应的密钥,没有缓存就派生一次。派生期间持锁:同一时刻只算一次,
// 并发的请求等着用现成的,不会同时起多份 PBKDF2。
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

// seal 用 key 封 plain。rnd 为 nil 用 crypto/rand。
func seal(key []byte, routerID, nonce string, iter int, plain []byte, rnd io.Reader) (Envelope, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	g, err := gcmOf(key)
	if err != nil {
		return Envelope{}, err
	}
	iv := make([]byte, ivLen)
	if _, err := io.ReadFull(rnd, iv); err != nil {
		return Envelope{}, fmt.Errorf("生成随机数失败: %w", err)
	}
	return Envelope{
		V: SealVersion, KDF: SealKDF, Iter: iter,
		Salt: b64.EncodeToString(sealSalt(routerID, nonce)),
		IV:   b64.EncodeToString(iv),
		CT:   b64.EncodeToString(g.Seal(nil, iv, plain, sealAAD(routerID, nonce))),
	}, nil
}

// OpenEnvelope 用配对码解开封包。App 一侧的做法也是这样;本后端自己用在测试里,
// 以后的公开同步接口收到 App 上传的东西时也用它。迭代次数低于 minIter(或高到离谱)的封包不认,
// 防止被降级成秒破的参数,也防止被喂一个大迭代数拖死 CPU。
func OpenEnvelope(e Envelope, pairCode, routerID, nonce string, minIter, maxIter int) ([]byte, error) {
	if e.V != SealVersion || e.KDF != SealKDF || e.Iter < minIter || e.Iter > maxIter {
		return nil, ErrSealOpen
	}
	salt, err1 := b64.DecodeString(e.Salt)
	iv, err2 := b64.DecodeString(e.IV)
	ct, err3 := b64.DecodeString(e.CT)
	if err1 != nil || err2 != nil || err3 != nil || len(iv) != ivLen || len(salt) != saltLen {
		return nil, ErrSealOpen
	}
	key, err := pbkdf2.Key(sha256.New, pairCode, salt, e.Iter, sealKeyLen)
	if err != nil {
		return nil, ErrSealOpen
	}
	g, err := gcmOf(key)
	if err != nil {
		return nil, ErrSealOpen
	}
	plain, err := g.Open(nil, iv, ct, sealAAD(routerID, nonce))
	if err != nil {
		return nil, ErrSealOpen
	}
	return plain, nil
}
