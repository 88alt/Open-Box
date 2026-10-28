package latencyhistory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func newSvc(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.UnixMilli(1_700_000_000_000)
	return &Service{Store: st, Now: func() time.Time { clock = clock.Add(time.Second); return clock }}, dir
}

func ts(sec int) string { return time.Unix(1_700_000_000+int64(sec), 0).UTC().Format(time.RFC3339) }

func TestAddMergesSortsDedupsAndCaps(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	if h, up, err := s.Get(ctx); err != nil || len(h) != 0 || up != 0 {
		t.Fatalf("空状态: %v %v %v", h, up, err)
	}
	var in []Input
	for i := 14; i >= 0; i-- { // 乱序上报 15 条
		in = append(in, Input{Name: "HK", Time: ts(i), Delay: 0})
	}
	in = append(in, Input{Name: "HK", Time: ts(3), Delay: 0}) // 重复时刻
	h, err := s.Add(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(h["HK"]) != MaxSamples || h["HK"][0].Time != ts(5) || h["HK"][9].Time != ts(14) {
		t.Fatalf("应只留最新 10 条且从旧到新: %v", h["HK"])
	}
	_, up1, _ := s.Get(ctx)
	if up1 == 0 {
		t.Fatal("有变化应记 updatedAt")
	}
	// 完全重复的上报:没变化,updatedAt 不动
	if _, err := s.Add(ctx, []Input{{Name: "HK", Time: ts(14), Delay: 0}}); err != nil {
		t.Fatal(err)
	}
	if _, up2, _ := s.Get(ctx); up2 != up1 {
		t.Errorf("没变化不该动 updatedAt: %d → %d", up1, up2)
	}
	// 比最旧的还旧的样本进不来
	h, _ = s.Add(ctx, []Input{{Name: "HK", Time: ts(-100), Delay: 0}})
	if h["HK"][0].Time != ts(5) {
		t.Errorf("过旧的样本不该挤掉新的: %v", h["HK"][0])
	}
	if _, up3, _ := s.Get(ctx); up3 != up1 {
		t.Errorf("被截掉的样本等于没变化,不该动 updatedAt: %d → %d", up1, up3)
	}
}

func TestAddValidationRejectsWholeBatch(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	bad := []Input{{Name: "ok", Time: ts(1), Delay: 5}}
	for name, x := range map[string]Input{
		"空名字":  {Name: " ", Time: ts(1)},
		"时间坏了": {Name: "a", Time: "昨天"},
		"没有时间": {Name: "a"},
		"负延迟":  {Name: "a", Time: ts(1), Delay: -1},
		"延迟太大": {Name: "a", Time: ts(1), Delay: maxDelayMs + 1},
		"名字太长": {Name: string(make([]rune, maxNameLen+1)), Time: ts(1)},
	} {
		var ie *InputError
		if _, err := s.Add(ctx, append(append([]Input{}, bad...), x)); !errors.As(err, &ie) {
			t.Errorf("%s 应是 InputError: %v", name, err)
		}
	}
	if h, _, _ := s.Get(ctx); len(h) != 0 {
		t.Errorf("有一条不合法整批都不记: %v", h)
	}
	big := make([]Input, MaxPerRequest+1)
	var ie *InputError
	if _, err := s.Add(ctx, big); !errors.As(err, &ie) {
		t.Errorf("超过单次上限应拒绝: %v", err)
	}
}

func TestSyncFromKernel(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	s.Kernel = func(context.Context) ([]singbox.KernelProxy, error) {
		return []singbox.KernelProxy{
			{Name: "HK", Type: "Trojan", History: []singbox.KernelPoint{{Time: ts(1), Delay: 120}}},
			{Name: "自动选择", Type: "URLTest", Now: "HK", History: []singbox.KernelPoint{{Time: ts(2), Delay: 130}}},
			{Name: "坏的", Type: "Trojan", History: []singbox.KernelPoint{{Time: "x", Delay: 1}, {Time: ts(3), Delay: 0}}},
			{Name: "直连", Type: "Direct"},
		}, nil
	}
	h, err := s.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h["HK"][0].Delay != 120 || h["HK"][0].Node != "" || h["自动选择"][0].Node != "HK" {
		t.Fatalf("同步结果: %v", h)
	}
	_, up1, _ := s.Get(ctx)
	if _, err := s.Sync(ctx); err != nil { // 内核没新数据
		t.Fatal(err)
	}
	if _, up2, _ := s.Get(ctx); up2 != up1 {
		t.Error("没新数据不该动 updatedAt")
	}
}

func TestSyncWithoutKernelReturnsExisting(t *testing.T) {
	s, dir := newSvc(t)
	ctx := context.Background()
	if _, err := s.Add(ctx, []Input{{Name: "HK", Time: ts(1), Delay: 0}}); err != nil {
		t.Fatal(err)
	}
	s.Kernel = func(context.Context) ([]singbox.KernelProxy, error) { return nil, errors.New("连不上") }
	h, err := s.Sync(ctx)
	if err != nil || len(h["HK"]) != 1 {
		t.Fatalf("内核读不到应回现有的、不报错: %v %v", h, err)
	}
	s.Kernel = nil
	if h, err := s.Sync(ctx); err != nil || len(h["HK"]) != 1 {
		t.Fatalf("没接内核同上: %v %v", h, err)
	}
	// 没变化时不写盘:文件修改时间不动
	p := filepath.Join(dir, "latency-history.json")
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	_, _ = s.Sync(ctx)
	after, _ := os.Stat(p)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("没变化不该写盘")
	}
}

func TestNameCapAndConcurrency(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = s.Add(ctx, []Input{{Name: fmt.Sprintf("n%d", i%4), Time: ts(i), Delay: 0}})
		}(i)
	}
	wg.Wait()
	h, _, _ := s.Get(ctx)
	total := 0
	for _, l := range h {
		total += len(l)
	}
	if len(h) != 4 || total != 20 {
		t.Fatalf("并发上报不该丢(4 个名字共 20 条): %d 个名字 %d 条", len(h), total)
	}
	f := &file{History: History{}}
	for i := 0; i < MaxNames; i++ {
		f.History[fmt.Sprint(i)] = nil
	}
	if merge(f, "new", Sample{Time: ts(1)}) {
		t.Error("名字数到上限后不再收新名字")
	}
	if !merge(f, "5", Sample{Time: ts(1)}) {
		t.Error("已有的名字照常收")
	}
}

func TestRecordAndSeparateFile(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nodes := &Service{Store: st}
	sites := &Service{Store: st, File: "site-latency-history"}
	ctx := context.Background()
	at := "2026-10-28T08:00:00Z"
	h, err := sites.Record(ctx, []Item{{Name: "baidu", Sample: Sample{Time: at, Delay: 30, Node: "直连"}}})
	if err != nil || len(h["baidu"]) != 1 || h["baidu"][0].Node != "直连" {
		t.Fatalf("%v %v", h, err)
	}
	// 同一时刻再记:不变,仍返回现有的历史
	h, err = sites.Record(ctx, []Item{{Name: "baidu", Sample: Sample{Time: at, Delay: 99}}})
	if err != nil || len(h["baidu"]) != 1 || h["baidu"][0].Delay != 30 {
		t.Fatalf("%v %v", h, err)
	}
	// 两份历史互不混
	if nh, _, _ := nodes.Get(ctx); len(nh) != 0 {
		t.Fatalf("节点历史不该看到站点的记录:%v", nh)
	}
	if sh, _, _ := sites.Get(ctx); len(sh["baidu"]) != 1 {
		t.Fatal(sh)
	}
}
