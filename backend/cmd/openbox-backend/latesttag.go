package main

// 概览顶栏「最新版本号」的取数(预设决策:取 main 上最新的 release tag)。
// 放在 main 里而不是 activation 包里,是因为它要用 update.Checker;activation 只接一个
// func(ctx) string,保持它对「版本号从哪来」无感。

import (
	"context"
	"sync"
	"time"
)

// releaseTagChecker 是取最新 tag 的最小接口(*update.Checker 满足),抽出来便于测试。
// 用 LatestTagOnly 而不是 Latest:后者要求发布里带本架构安装包,而本仓库目前的发布是上游命名
// (`open-box-linux-x64.tar.gz`),那样会永远取不到 tag(2026-11-25 真机发现)。
type releaseTagChecker interface {
	LatestTagOnly(ctx context.Context) (tag, via string, err error)
}

// latestReleaseTagCached 返回一个带缓存的取 tag 函数:
//   - 命中缓存直接返回,不打网络;
//   - 缓存过期(或从来没有成功过)时问一次,单次超时 5 秒——面板会每几秒轮询这个接口,
//     不能让一个慢请求把面板的渲染拖住;
//   - 失败时:有过成功值就继续用旧值(版本号显示旧一版比空着好),从来没成功过就返回空串,
//     由调用方退回构建时注入的版本;失败结果也缓存一小会儿,免得每次轮询都去打一遍 GitHub。
func latestReleaseTagCached(c releaseTagChecker, ttl time.Duration) func(ctx context.Context) string {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	const failTTL = 1 * time.Minute
	const callTimeout = 5 * time.Second

	var mu sync.Mutex
	var tag string
	var at time.Time
	var lastErr bool

	return func(ctx context.Context) string {
		mu.Lock()
		defer mu.Unlock()
		age := time.Since(at)
		limit := ttl
		if lastErr {
			limit = failTTL
		}
		if !at.IsZero() && age < limit {
			return tag // 有成功值返回成功值;最近失败且没有成功值时这里是空串
		}
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		got, _, err := c.LatestTagOnly(cctx)
		at = time.Now()
		if err != nil || got == "" {
			lastErr = true
			return tag // 保留上一次成功值(可能为空)
		}
		lastErr = false
		tag = got
		return tag
	}
}
