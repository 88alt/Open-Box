package clientapp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// App 在线配对与按 token 拉配置:对齐上游 v0.1.402
// GET/POST /client/v1/:routerId/pair、GET /client/v1/:routerId/config/:token。
// 面板导出仍用配对码封包;App 配对成功后用设备随机 key 的 {v,iv,data} 拉配置。

const (
	tokenHexLen = 48 // 24 字节
	maxDevices  = 50
	pairFailMax = 10
	pairFailWin = 10 * time.Minute
)

// 设备凭据(库里只存 token 哈希,token 明文只在配对响应的 sealed 里出现一次)。
type device struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Via          string `json:"via,omitempty"`
	TokenHash    string `json:"tokenHash"`
	Key          string `json:"key"` // base64url,32 字节
	CreatedAt    int64  `json:"createdAt"`
	LastSyncAt   int64  `json:"lastSyncAt,omitempty"`
	LastSyncFrom string `json:"lastSyncFrom,omitempty"`
}

// TokenBundle 是上游 Pp 的外形:AES-256-GCM,iv/data 为标准 base64。
type TokenBundle struct {
	V    int    `json:"v"`
	IV   string `json:"iv"`
	Data string `json:"data"`
}

var (
	ErrPairUnpaired  = errors.New("unpaired")
	ErrPairDisabled  = errors.New("disabled")
	ErrPairExpired   = errors.New("expired")
	ErrPairBadCode   = errors.New("bad_code")
	ErrPairLocked    = errors.New("locked")
	ErrTokenNotFound = errors.New("not found")
)

// PairLockedError 带重试秒数。
type PairLockedError struct{ After time.Duration }

func (e *PairLockedError) Error() string { return "locked" }

// PairBadCodeError 带剩余次数。
type PairBadCodeError struct{ Remaining int }

func (e *PairBadCodeError) Error() string { return "bad_code" }

type pairLimiter struct {
	mu    sync.Mutex
	fails []int64 // unix ms
}

func (p *pairLimiter) locked(now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	cut := now.Add(-pairFailWin).UnixMilli()
	kept := p.fails[:0]
	for _, t := range p.fails {
		if t >= cut {
			kept = append(kept, t)
		}
	}
	p.fails = kept
	if len(p.fails) < pairFailMax {
		return 0
	}
	return time.UnixMilli(p.fails[0]).Add(pairFailWin).Sub(now)
}

func (p *pairLimiter) fail(now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fails = append(p.fails, now.UnixMilli())
	cut := now.Add(-pairFailWin).UnixMilli()
	kept := p.fails[:0]
	for _, t := range p.fails {
		if t >= cut {
			kept = append(kept, t)
		}
	}
	p.fails = kept
	rem := pairFailMax - len(p.fails)
	if rem < 0 {
		rem = 0
	}
	return rem
}

func (p *pairLimiter) reset() {
	p.mu.Lock()
	p.fails = nil
	p.mu.Unlock()
}

// PairGet:GET pair → {nonce}。routerId 不对 404 unpaired;开关关 403 disabled。
func (s *Service) PairGet(ctx context.Context, routerID string) (nonce string, err error) {
	is, err := s.ClientConfig(ctx)
	if err != nil {
		return "", err
	}
	if routerID != is.RouterID {
		return "", ErrPairUnpaired
	}
	if !is.Enabled {
		return "", ErrPairDisabled
	}
	return is.Nonce, nil
}

// PairPost:POST pair {nonce,proof} → {sealed}(内含 token+key)。
func (s *Service) PairPost(ctx context.Context, routerID string, nonce, proof string) (sealed string, err error) {
	now := s.now()
	if s.pairLimit == nil {
		s.pairLimit = &pairLimiter{}
	}
	if d := s.pairLimit.locked(now); d > 0 {
		return "", &PairLockedError{After: d}
	}
	is, err := s.ClientConfig(ctx)
	if err != nil {
		return "", err
	}
	if routerID != is.RouterID {
		return "", ErrPairUnpaired
	}
	if !is.Enabled {
		return "", ErrPairDisabled
	}
	if nonce != is.Nonce || !validNonce(nonce) {
		return "", ErrPairExpired
	}
	key, err := pairDeriveKey(is.PairCode, is.RouterID, is.Nonce)
	if err != nil {
		return "", err
	}
	want := pairProof(key, is.Nonce)
	got := []byte(proof)
	if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
		rem := s.pairLimit.fail(now)
		return "", &PairBadCodeError{Remaining: rem}
	}
	s.pairLimit.reset()

	tok, err := randomToken(s.Rand)
	if err != nil {
		return "", err
	}
	devKey, err := randomDeviceKey(s.Rand)
	if err != nil {
		return "", err
	}
	id, err := randomDeviceID(s.Rand)
	if err != nil {
		return "", err
	}
	dev := device{
		ID: id, Name: "app", Via: "pair",
		TokenHash: sha256Hex(tok), Key: devKey,
		CreatedAt: now.UnixMilli(),
	}
	var st state
	err = s.Store.Update(stateFile, &st, func() error {
		list := append([]*device{}, st.Devices...)
		list = append(list, &dev)
		if len(list) > maxDevices {
			list = list[len(list)-maxDevices:]
		}
		st.Devices = list
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("保存设备失败: %w", err)
	}
	plain, _ := json.Marshal(map[string]string{"token": tok, "key": devKey})
	env, err := sealBytes(key, plain, s.Rand)
	if err != nil {
		return "", err
	}
	return env, nil
}

// authDevice 认设备:路由器 ID 不对、token 格式不对、库里没有这个 token,一律 ErrPairUnpaired(404 unpaired,App 据此提示重新配对);
// 认出设备之后同步开关关着才回 ErrPairDisabled(403 disabled)——只有拿着有效 token 的才看得到这个状态,
// 对齐上游 v0.1.402 的设备校验(先认设备、再看开关)。config/:token 和它下面的子资源共用这一处。
func (s *Service) authDevice(ctx context.Context, routerID, token string) (Issue, *device, error) {
	is, err := s.ClientConfig(ctx)
	if err != nil {
		return Issue{}, nil, err
	}
	if routerID != is.RouterID || !validToken(token) {
		return Issue{}, nil, ErrPairUnpaired
	}
	hash := sha256Hex(token)
	var st state
	if _, err := s.Store.Load(stateFile, &st); err != nil {
		return Issue{}, nil, err
	}
	var dev *device
	for _, d := range st.Devices {
		if d != nil && subtle.ConstantTimeCompare([]byte(d.TokenHash), []byte(hash)) == 1 {
			c := *d
			dev = &c
		}
	}
	if dev == nil {
		return Issue{}, nil, ErrPairUnpaired
	}
	if !is.Enabled {
		return Issue{}, nil, ErrPairDisabled
	}
	return is, dev, nil
}

// ConfigByToken:GET config/:token → TokenBundle。
func (s *Service) ConfigByToken(ctx context.Context, routerID, token, ip string) (TokenBundle, error) {
	is, dev, err := s.authDevice(ctx, routerID, token)
	if err != nil {
		return TokenBundle{}, err
	}
	hash := sha256Hex(token)
	var st state
	plain, now, err := s.plainConfig(ctx, is, "")
	if err != nil {
		return TokenBundle{}, err
	}
	bundle, err := sealToken(dev.Key, plain, s.Rand)
	if err != nil {
		return TokenBundle{}, err
	}
	_ = s.Store.Update(stateFile, &st, func() error {
		for _, d := range st.Devices {
			if d != nil && d.TokenHash == hash {
				d.LastSyncAt = now
				d.LastSyncFrom = ip
			}
		}
		if st.Config != nil {
			st.Config.LastSyncAt = now
		}
		return nil
	})
	return bundle, nil
}

func pairDeriveKey(pairCode, routerID, nonce string) ([]byte, error) {
	salt := []byte(fmt.Sprintf("openbox-pair:%s:%s", routerID, nonce))
	return pbkdf2.Key(sha256.New, pairCode, salt, DefaultSealIter, sealKeyLen)
}

func pairProof(key []byte, nonce string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("openbox-pair-v1:" + nonce))
	// 上游: digest("base64url") 再 Buffer.from(string) 按 utf8 比较
	s := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	return []byte(s)
}

func sealBytes(key, plain []byte, rnd io.Reader) (string, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rnd, iv); err != nil {
		return "", err
	}
	ct := g.Seal(nil, iv, plain, nil)
	out := append(append([]byte{}, iv...), ct...)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func sealToken(keyB64 string, plain []byte, rnd io.Reader) (TokenBundle, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	key, err := base64.RawURLEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 32 {
		return TokenBundle{}, errors.New("设备密钥损坏")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return TokenBundle{}, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return TokenBundle{}, err
	}
	iv := make([]byte, 12)
	if _, err := io.ReadFull(rnd, iv); err != nil {
		return TokenBundle{}, err
	}
	ct := g.Seal(nil, iv, plain, nil)
	return TokenBundle{
		V:    1,
		IV:   base64.StdEncoding.EncodeToString(iv),
		Data: base64.StdEncoding.EncodeToString(ct),
	}, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func validToken(s string) bool {
	if len(s) != tokenHexLen {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func randomToken(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 24)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomDeviceKey(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 32)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomDeviceID(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 6)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return "d" + hex.EncodeToString(b), nil
}

// clearDevices 换配对码时清掉已配对设备(上游 rotate 时 Ip(t,[])).
func clearDevices(st *state) {
	st.Devices = nil
}
