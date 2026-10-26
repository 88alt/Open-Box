package auth

import (
	"sync"
	"time"
)

// limiter 按客户端限制连续失败的登录:连续失败 5 次后锁定,
// 锁定时间从 30 秒起逐次翻倍,封顶 15 分钟;15 分钟内没有新的失败则清零。
type limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]*attempt
}

type attempt struct {
	fails       int
	last        time.Time
	lockedUntil time.Time
}

const (
	maxTracked    = 1024
	freeFails     = 5
	baseLock      = 30 * time.Second
	maxLock       = 15 * time.Minute
	forgetAfter   = 15 * time.Minute
	maxLockShifts = 5
)

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, entries: map[string]*attempt{}}
}

// blocked 返回还需等待多久;0 表示可以尝试。
func (l *limiter) blocked(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a, ok := l.entries[key]; ok {
		if d := a.lockedUntil.Sub(l.now()); d > 0 {
			return d
		}
	}
	return 0
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= maxTracked {
			l.purge(now)
		}
		a = &attempt{}
		l.entries[key] = a
	}
	if now.Sub(a.last) > forgetAfter {
		a.fails = 0
	}
	a.fails++
	a.last = now
	if a.fails >= freeFails {
		shift := min(a.fails-freeFails, maxLockShifts)
		a.lockedUntil = now.Add(min(baseLock<<shift, maxLock))
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// purge 清掉已过期的记录;仍然满了(上千个不同来源同时在锁定中)就整体清空,
// 宁可放松一次限速,也不能让表无限增长。
func (l *limiter) purge(now time.Time) {
	for k, a := range l.entries {
		if now.After(a.lockedUntil) && now.Sub(a.last) > forgetAfter {
			delete(l.entries, k)
		}
	}
	if len(l.entries) >= maxTracked {
		l.entries = map[string]*attempt{}
	}
}
