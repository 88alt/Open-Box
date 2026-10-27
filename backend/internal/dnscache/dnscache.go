// Package dnscache 把「清 DNS 缓存」的三层合在一起:内核(sing-box)、dnsmasq、systemd-resolved。
// 每一层独立尝试、互不影响;只是让缓存失效,不改配置、不重启,所以不需要回滚。
package dnscache

import (
	"context"
	"log/slog"
	"time"
)

// Result 对应前端 OpenboxDnsFlushResult:每一层是否清成功。
type Result struct {
	Kernel   bool `json:"kernel"`
	Dnsmasq  bool `json:"dnsmasq"`
	Resolved bool `json:"resolved"`
}

// Any 是有没有任何一层清成功。
func (r Result) Any() bool { return r.Kernel || r.Dnsmasq || r.Resolved }

// Layers 是三层的实现;为空的层按「没清掉」处理。
type Layers struct {
	Kernel   func(ctx context.Context) error // 失败的原因只记日志
	Dnsmasq  func(ctx context.Context) bool
	Resolved func(ctx context.Context) bool
	Log      *slog.Logger
}

const perLayer = 8 * time.Second

// Flush 依次清三层。一层卡住最多占 8 秒,不拖累后面的层。
func (l Layers) Flush(ctx context.Context) Result {
	var r Result
	if l.Kernel != nil {
		c, cancel := context.WithTimeout(ctx, perLayer)
		err := l.Kernel(c)
		cancel()
		r.Kernel = err == nil
		if err != nil && l.Log != nil {
			l.Log.Debug("清内核 DNS 缓存没成功", "error", err)
		}
	}
	if l.Dnsmasq != nil {
		c, cancel := context.WithTimeout(ctx, perLayer)
		r.Dnsmasq = l.Dnsmasq(c)
		cancel()
	}
	if l.Resolved != nil {
		c, cancel := context.WithTimeout(ctx, perLayer)
		r.Resolved = l.Resolved(c)
		cancel()
	}
	return r
}
