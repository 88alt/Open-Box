// Package auth 实现面板的访问密码:首次设置、登录、修改密码、会话校验。
//
//   - 密码用 PBKDF2-HMAC-SHA256 存哈希(标准库实现,无第三方依赖)。哈希字符串里带算法名和迭代次数,
//     以后换算法或加大迭代只需要在校验时兼容旧格式。
//   - 会话是无状态的签名 Cookie:v1.<过期时间>.<随机数>.<HMAC>,密钥保存在数据文件里。
//     修改密码会换密钥,让其它设备上的会话全部失效。登出只清除本机 Cookie。
//   - 登录失败按客户端限速(见 limiter)。
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const (
	storeName        = "auth"
	hashScheme       = "pbkdf2-sha256"
	defaultIter      = 600_000 // OWASP 对 PBKDF2-HMAC-SHA256 的建议值
	maxIter          = 10_000_000
	keyLen           = 32
	MinPasswordRunes = 4   // 与前端 MIN_PASSWORD_LENGTH 一致
	MaxPasswordBytes = 256 // 限制哈希的计算量
	SessionTTL       = 30 * 24 * time.Hour
)

var (
	ErrNotSetup        = errors.New("auth: 尚未设置访问密码")
	ErrAlreadySet      = errors.New("auth: 访问密码已设置")
	ErrTooShort        = errors.New("auth: 密码太短")
	ErrTooLong         = errors.New("auth: 密码太长")
	ErrInvalidPassword = errors.New("auth: 密码错误")
)

// LockedError 表示该客户端登录失败次数过多,需等待 RetryAfter。
type LockedError struct{ RetryAfter time.Duration }

func (e *LockedError) Error() string {
	return fmt.Sprintf("auth: 登录尝试过多,请 %d 秒后再试", int(e.RetryAfter.Seconds())+1)
}

// record 是落盘的内容。
type record struct {
	PasswordHash  string `json:"passwordHash"`
	SessionSecret string `json:"sessionSecret"` // base64
	UpdatedAt     int64  `json:"updatedAt"`
}

type state struct {
	hash   string
	secret []byte
}

// Manager 管理密码与会话。并发安全。
type Manager struct {
	store      *store.Store
	iterations int
	now        func() time.Time
	lim        *limiter

	opMu sync.Mutex // 串行化 设置 / 登录校验 / 改密:同一时刻只算一个哈希,也让限速无法被并发绕过
	mu   sync.RWMutex
	st   *state
}

// Option 调整 Manager(主要给测试用)。
type Option func(*Manager)

// WithIterations 设置新密码哈希的迭代次数。
func WithIterations(n int) Option { return func(m *Manager) { m.iterations = n } }

// WithClock 替换时钟。
func WithClock(now func() time.Time) Option { return func(m *Manager) { m.now = now } }

// NewManager 读取已保存的密码状态。数据文件损坏时返回错误,不会当成「未设置」(否则任何人都能重新设置密码)。
func NewManager(st *store.Store, opts ...Option) (*Manager, error) {
	m := &Manager{store: st, iterations: defaultIter, now: time.Now}
	for _, o := range opts {
		o(m)
	}
	m.lim = newLimiter(func() time.Time { return m.now() })

	var rec record
	ok, err := st.Load(storeName, &rec)
	if err != nil {
		return nil, err
	}
	if ok {
		secret, err := base64.StdEncoding.DecodeString(rec.SessionSecret)
		if err != nil || len(secret) < 16 || rec.PasswordHash == "" {
			return nil, errors.New("auth: 数据文件内容不完整,拒绝启动(请检查或删除 auth.json 后重新设置密码)")
		}
		m.st = &state{hash: rec.PasswordHash, secret: secret}
	}
	return m, nil
}

// PasswordSet 报告是否已经设置过访问密码。
func (m *Manager) PasswordSet() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.st != nil
}

func (m *Manager) state() *state {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.st
}

// Setup 首次设置密码并返回会话令牌。已设置过返回 ErrAlreadySet。
func (m *Manager) Setup(password string) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if m.state() != nil {
		return "", ErrAlreadySet
	}
	st, err := m.persist(password)
	if err != nil {
		return "", err
	}
	return m.newToken(st.secret)
}

// Login 校验密码并返回会话令牌。clientKey 用于限速(一般是客户端 IP)。
func (m *Manager) Login(password, clientKey string) (string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	st := m.state()
	if st == nil {
		return "", ErrNotSetup
	}
	if err := m.checkPassword(st, password, clientKey); err != nil {
		return "", err
	}
	return m.newToken(st.secret)
}

// ChangePassword 校验当前密码后换成新密码,同时更换会话密钥(其它会话全部失效),返回新令牌。
func (m *Manager) ChangePassword(current, next, clientKey string) (string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	st := m.state()
	if st == nil {
		return "", ErrNotSetup
	}
	if err := validatePassword(next); err != nil {
		return "", err
	}
	if err := m.checkPassword(st, current, clientKey); err != nil {
		return "", err
	}
	nst, err := m.persist(next)
	if err != nil {
		return "", err
	}
	return m.newToken(nst.secret)
}

// Authenticated 校验会话令牌(签名正确且未过期)。
func (m *Manager) Authenticated(token string) bool {
	st := m.state()
	if st == nil || token == "" {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || !hmac.Equal(sig, sign(st.secret, strings.Join(parts[:3], "."))) {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	return err == nil && m.now().Unix() < exp
}

// checkPassword 在 opMu 保护下调用:先看是否被锁定,再校验,失败计数。
func (m *Manager) checkPassword(st *state, password, clientKey string) error {
	if wait := m.lim.blocked(clientKey); wait > 0 {
		return &LockedError{RetryAfter: wait}
	}
	if len(password) > MaxPasswordBytes || !verifyPassword(st.hash, password) {
		m.lim.fail(clientKey)
		return ErrInvalidPassword
	}
	m.lim.reset(clientKey)
	return nil
}

// persist 生成新哈希和新会话密钥,先落盘再换内存状态。
func (m *Manager) persist(password string) (*state, error) {
	hash, err := hashPassword(password, m.iterations)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	rec := record{PasswordHash: hash, SessionSecret: base64.StdEncoding.EncodeToString(secret), UpdatedAt: m.now().Unix()}
	if err := m.store.Save(storeName, rec); err != nil {
		return nil, err
	}
	st := &state{hash: hash, secret: secret}
	m.mu.Lock()
	m.st = st
	m.mu.Unlock()
	return st, nil
}

func (m *Manager) newToken(secret []byte) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := "v1." + strconv.FormatInt(m.now().Add(SessionTTL).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	return payload + "." + base64.RawURLEncoding.EncodeToString(sign(secret, payload)), nil
}

func sign(secret []byte, payload string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func validatePassword(p string) error {
	if utf8.RuneCountInString(p) < MinPasswordRunes {
		return ErrTooShort
	}
	if len(p) > MaxPasswordBytes {
		return ErrTooLong
	}
	return nil
}

func hashPassword(password string, iter int) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iter, keyLen)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", hashScheme, iter, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 || iter > maxIter {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}
