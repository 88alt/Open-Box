package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeChecker struct {
	tag  string
	err  error
	n    int
	seen int
}

func (f *fakeChecker) LatestTagOnly(context.Context) (string, string, error) {
	f.n++
	f.seen++
	if f.err != nil {
		return "", "", f.err
	}
	return f.tag, "api", nil
}

// 命中缓存时不重复问;过期后才再问一次。
func TestLatestReleaseTagCachedCaches(t *testing.T) {
	f := &fakeChecker{tag: "v0.1.487"}
	got := latestReleaseTagCached(f, time.Hour)
	for i := 0; i < 5; i++ {
		if v := got(context.Background()); v != "v0.1.487" {
			t.Fatalf("第 %d 次: %q", i, v)
		}
	}
	if f.n != 1 {
		t.Fatalf("缓存内不该重复请求,实际请求 %d 次", f.n)
	}
}

// 失败时保留上一次成功值(显示旧版本号比空着好)。
func TestLatestReleaseTagCachedKeepsLastGood(t *testing.T) {
	f := &fakeChecker{tag: "v0.1.487"}
	got := latestReleaseTagCached(f, time.Nanosecond) // 立刻过期,每次都问
	if v := got(context.Background()); v != "v0.1.487" {
		t.Fatalf("首次: %q", v)
	}
	f.err = errors.New("限流")
	time.Sleep(2 * time.Millisecond)
	if v := got(context.Background()); v != "v0.1.487" {
		t.Fatalf("失败时应保留上次成功值: %q", v)
	}
}

// 从来没成功过就返回空串,由调用方退回构建注入的版本。
func TestLatestReleaseTagCachedNoSuccessIsEmpty(t *testing.T) {
	f := &fakeChecker{err: errors.New("离网")}
	got := latestReleaseTagCached(f, time.Nanosecond)
	if v := got(context.Background()); v != "" {
		t.Fatalf("没有成功值应返回空串: %q", v)
	}
	if f.n != 1 {
		t.Fatalf("应问过一次: %d", f.n)
	}
	// 失败结果缓存 1 分钟:紧接着再调不该再问
	_ = got(context.Background())
	if f.n != 1 {
		t.Fatalf("失败后短时间内不该重复请求: %d", f.n)
	}
}

// 拿到空 tag 也当失败(不把空串缓存成成功值)。
func TestLatestReleaseTagCachedEmptyTag(t *testing.T) {
	f := &fakeChecker{tag: ""}
	got := latestReleaseTagCached(f, time.Hour)
	if v := got(context.Background()); v != "" {
		t.Fatalf("空 tag 应返回空串: %q", v)
	}
	_ = got(context.Background())
	if f.n != 1 {
		t.Fatalf("空结果应按失败缓存,不重复请求: %d", f.n)
	}
}
