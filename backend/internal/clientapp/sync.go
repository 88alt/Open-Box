package clientapp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"sync"
	"time"
)

// App 用的公开同步接口(GET /client/v1/config)在服务层的部分。自己的设计,依据 DESIGN 第 8 节「配对码封包」的授权口径
// (同步开关关着回 403 disabled、按 IP 限速且放在派生和解密之前、成功同步后更新 lastSyncAt)和前端 api/clientApp.ts 的注释,
// 没有参照上游。接口不需要登录,所以每一步都先做便宜的检查:
//
//	开关关着 → ErrDisabled(只读状态文件,不进任何密码运算);
//	来源 IP 被锁 → *LimitedError;
//	nonce 对不上 → ErrSyncNotFound(记一次失败,同样的答话,不泄露「有没有开」之外的任何东西);
//	都过了才封包返回(密钥已缓存时只有一次 AES,没缓存时派生一次并缓存),并更新 lastSyncAt。
//
// 返回的只是封包:没有配对码解不开(拿到封包可离线爆破,所以 nonce 对上才给)。

var (
	// ErrDisabled:同步开关关着。
	ErrDisabled = errors.New("同步开关关着")
	// ErrSyncNotFound:nonce 对不上(没有配对过 / 已换码)。
	ErrSyncNotFound = errors.New("没有这个配对")
)

// LimitedError:来源 IP 被限速,After 是还要等多久。
type LimitedError struct{ After time.Duration }

func (e *LimitedError) Error() string { return "尝试太频繁,请稍后再试" }

// SyncResult 是公开同步接口的响应体:认得出格式、版本和这台路由器,配置在 Seal 里。
type SyncResult struct {
	Format     string       `json:"format"`
	Version    int          `json:"version"`
	Router     BundleRouter `json:"router"`
	KDF        KDFParams    `json:"kdf"`
	Data       string       `json:"data"`
	ExportedAt int64        `json:"exportedAt,omitempty"`
}

// Seal 方便测试解开密文。
func (r SyncResult) Seal() Envelope { return Envelope{KDF: r.KDF, Data: r.Data} }

// Sync 处理一次 App 同步。ip 是连接对端 IP(调用方不要用 X-Forwarded-For)。
func (s *Service) Sync(ctx context.Context, nonce, ip string) (SyncResult, error) {
	var st state
	if _, err := s.Store.Load(stateFile, &st); err != nil {
		return SyncResult{}, fmt.Errorf("读取客户端配置状态失败: %w", err)
	}
	c := st.Config
	if c == nil || !c.Enabled || !validPairCode(c.PairCode) || !validNonce(c.Nonce) {
		return SyncResult{}, ErrDisabled
	}
	if d := s.syncLimit.blocked(ip, s.now()); d > 0 {
		return SyncResult{}, &LimitedError{After: d}
	}
	if subtle.ConstantTimeCompare([]byte(nonce), []byte(c.Nonce)) != 1 {
		s.syncLimit.fail(ip, s.now())
		return SyncResult{}, ErrSyncNotFound
	}
	is, err := s.ClientConfig(ctx) // 此时状态已齐全,这一步只读
	if err != nil {
		return SyncResult{}, err
	}
	env, now, err := s.sealConfig(ctx, is, "")
	if err != nil {
		return SyncResult{}, err
	}
	// 只在配对码没被换掉的前提下记下同步时刻(换码时 lastSyncAt 清零,不能被迟到的同步写回去)
	err = s.Store.Update(stateFile, &st, func() error {
		if st.Config == nil || st.Config.Nonce != is.Nonce {
			return errNoChange
		}
		st.Config.LastSyncAt = now
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		return SyncResult{}, fmt.Errorf("记录同步时刻失败: %w", err)
	}
	return SyncResult{
		Format: BundleFormat, Version: BundleVersion,
		Router: BundleRouter{ID: is.RouterID, Name: is.RouterName},
		KDF:    env.KDF, Data: env.Data, ExportedAt: now,
	}, nil
}

// Warm 在启动时把当前这一代配对码的密钥派生好放进缓存,第一次同步就不用现算。
// 没有配对码(从没打开过页面)、读不出状态时什么都不做。
func (s *Service) Warm(context.Context) {
	// 上游 salt 每个封包随机,启动时无法预派生;第一次同步时再进缓存。
}

// syncLimiter 按来源 IP 限制连续失败的同步:连续失败 10 次后锁定,从 1 分钟起逐次翻倍、封顶 15 分钟;
// 15 分钟内没有新的失败则清零。nonce 只有 64 位、也不是凭据,所以放得比登录宽一些。
type syncLimiter struct {
	mu      sync.Mutex
	entries map[string]*syncAttempt
}

type syncAttempt struct {
	fails       int
	last        time.Time
	lockedUntil time.Time
}

const (
	syncMaxTracked = 1024
	syncFreeFails  = 10
	syncBaseLock   = time.Minute
	syncMaxLock    = 15 * time.Minute
	syncForget     = 15 * time.Minute
)

func (l *syncLimiter) blocked(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a, ok := l.entries[ip]; ok {
		if d := a.lockedUntil.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func (l *syncLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string]*syncAttempt{}
	}
	a, ok := l.entries[ip]
	if !ok {
		if len(l.entries) >= syncMaxTracked {
			for k, v := range l.entries {
				if now.After(v.lockedUntil) && now.Sub(v.last) > syncForget {
					delete(l.entries, k)
				}
			}
			if len(l.entries) >= syncMaxTracked { // 仍然满了:宁可放松一次,也不让表无限增长
				l.entries = map[string]*syncAttempt{}
			}
		}
		a = &syncAttempt{}
		l.entries[ip] = a
	}
	if now.Sub(a.last) > syncForget {
		a.fails = 0
	}
	a.fails++
	a.last = now
	if a.fails >= syncFreeFails {
		shift := min(a.fails-syncFreeFails, 5)
		a.lockedUntil = now.Add(min(syncBaseLock<<shift, syncMaxLock))
	}
}
