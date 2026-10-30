package latencyhistory

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

func TestCollectPicksUpNewKernelResultsAndSurvivesErrors(t *testing.T) {
	s, _ := newSvc(t)
	var mu sync.Mutex
	var calls int
	var cur []singbox.KernelProxy
	var kerr error
	s.Kernel = func(context.Context) ([]singbox.KernelProxy, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return cur, kerr
	}
	set := func(p []singbox.KernelProxy, err error) { mu.Lock(); cur, kerr = p, err; mu.Unlock() }
	nCalls := func() int { mu.Lock(); defer mu.Unlock(); return calls }
	got := func(name string) int {
		h, _, _ := s.Get(context.Background())
		return len(h[name])
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			if ok() {
				return
			}
		}
		t.Fatalf("等不到: %s", what)
	}

	set(nil, errors.New("内核没起来")) // 一开始内核读不到:不记、不退出
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Collect(ctx, 5*time.Millisecond); close(done) }()
	waitFor("读过几次内核", func() bool { return nCalls() >= 3 })
	if h, up, _ := s.Get(ctx); len(h) != 0 || up != 0 {
		t.Fatalf("内核读不到时不应写历史: %v %d", h, up)
	}

	// 内核起来了,有一轮结果:节点自己的样本没有 node,组的样本带当时选中的节点
	set([]singbox.KernelProxy{
		{Name: "HK-01", Type: "Shadowsocks", History: []singbox.KernelPoint{{Time: ts(1), Delay: 120}}},
		{Name: "自动选择", Type: "URLTest", Now: "HK-01", History: []singbox.KernelPoint{{Time: ts(1), Delay: 120}}},
	}, nil)
	waitFor("合进第一轮结果", func() bool { return got("HK-01") == 1 && got("自动选择") == 1 })
	h, _, _ := s.Get(ctx)
	if h["HK-01"][0].Node != "" || h["自动选择"][0].Node != "HK-01" {
		t.Errorf("样本形状不对: %+v", h)
	}

	// 同一份结果被反复读到:不重复记、不动 updatedAt(不写盘)
	_, up1, _ := s.Get(ctx)
	before := nCalls()
	waitFor("又读了几次", func() bool { return nCalls() >= before+3 })
	if _, up2, _ := s.Get(ctx); up2 != up1 || got("HK-01") != 1 {
		t.Errorf("没有新结果不应写盘: %d -> %d, n=%d", up1, up2, got("HK-01"))
	}

	// 下一轮结果到了
	set([]singbox.KernelProxy{{Name: "HK-01", Type: "Shadowsocks", History: []singbox.KernelPoint{{Time: ts(200), Delay: 95}}}}, nil)
	waitFor("合进下一轮结果", func() bool { return got("HK-01") == 2 })

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 Collect 应退出")
	}
}

func TestCollectWithoutKernelReturnsImmediately(t *testing.T) {
	s, _ := newSvc(t) // Kernel 为 nil:站点延迟那份历史就是这样
	var finished atomic.Bool
	go func() { s.Collect(context.Background(), time.Millisecond); finished.Store(true) }()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end) && !finished.Load(); time.Sleep(time.Millisecond) {
	}
	if !finished.Load() {
		t.Fatal("没有内核来源时应立刻返回")
	}
}
