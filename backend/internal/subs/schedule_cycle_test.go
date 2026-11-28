package subs

import (
	"context"
	"testing"
	"time"
)

// 每隔 N 小时的完整周期:按假时钟一小时一小时往前推,每一轮到点都要触发、
// 没到点的一轮都不该触发。这验的是「长周期跑起来真的按小时重复」,
// 不需要真挂几小时(逻辑由单测验证,长周期行为未实测)。
func TestRunOnceHourlyCycleAcrossManyRounds(t *testing.T) {
	plan := map[string]any{"enabled": true, "mode": "hours", "hours": float64(1)}
	now := at(2026, 10, 29, 8, 0)
	// 上次更新是很久以前 → 首轮一定到期,之后再把 UpdatedAt 顶着走
	f := &fakeSvc{recs: []Record{recOf("a", at(2026, 10, 1, 8, 0), plan)}}
	s := newSched(f, &now)

	if n := s.RunOnce(context.Background()); n != 1 {
		t.Fatalf("首轮应更新,实际 %d", n)
	}
	f.mu.Lock()
	f.recs[0].UpdatedAt = now.UnixMilli()
	f.mu.Unlock()
	// 更新完把 updatedAt 往前走,让下一轮基于新的时间算
	for i := 0; i < 6; i++ {
		// 推进 30 分钟:还没到 1 小时,不该触发
		now = now.Add(30 * time.Minute)
		if n := s.RunOnce(context.Background()); n != 0 {
			t.Fatalf("第 %d 次半程不该更新,实际 %d", i+1, n)
		}
		// 再推进 30 分钟:满 1 小时,该触发
		now = now.Add(30 * time.Minute)
		if n := s.RunOnce(context.Background()); n != 1 {
			t.Fatalf("第 %d 次满 1 小时应更新,实际 %d", i+1, n)
		}
		f.mu.Lock()
		f.recs[0].UpdatedAt = now.UnixMilli()
		f.mu.Unlock()
	}
	f.mu.Lock()
	got := len(f.calls)
	f.mu.Unlock()
	if got != 7 { // 首轮 + 6 轮
		t.Fatalf("共应更新 7 次,实际 %d", got)
	}
}

// 每隔 3 小时:2 小时不触发、3 小时触发,跨多轮都保持这个节奏。
func TestRunOnceMultiHourCycle(t *testing.T) {
	plan := map[string]any{"enabled": true, "mode": "hours", "hours": float64(3)}
	now := at(2026, 10, 29, 6, 0)
	f := &fakeSvc{recs: []Record{recOf("a", now, plan)}}
	s := newSched(f, &now)

	s.RunOnce(context.Background())
	f.mu.Lock()
	f.recs[0].UpdatedAt = now.UnixMilli()
	f.mu.Unlock()

	for round := 0; round < 3; round++ {
		now = now.Add(2 * time.Hour)
		if n := s.RunOnce(context.Background()); n != 0 {
			t.Fatalf("第 %d 轮 2 小时时不该更新,实际 %d", round+1, n)
		}
		now = now.Add(1 * time.Hour)
		if n := s.RunOnce(context.Background()); n != 1 {
			t.Fatalf("第 %d 轮 3 小时时应更新,实际 %d", round+1, n)
		}
		f.mu.Lock()
		f.recs[0].UpdatedAt = now.UnixMilli()
		f.mu.Unlock()
	}
}

// 退避期间即使订阅本身到期,也不该重拉:三档(10/30/60 分钟)逐档验,且第四档起固定在 60 分钟。
func TestRunOnceBackoffBlocksEvenWhenDue(t *testing.T) {
	plan := map[string]any{"enabled": true, "mode": "hours", "hours": float64(1)}
	now := at(2026, 10, 29, 10, 0)
	f := &fakeSvc{recs: []Record{recOf("a", now, plan)}, failIDs: map[string]bool{"a": true}}
	s := newSched(f, &now)

	// 每次都把 updatedAt 顶着走,让「订阅到期」这个条件始终成立,
	// 于是能否重拉只取决于退避窗口。
	try := func(advance time.Duration) int {
		now = now.Add(advance)
		f.mu.Lock()
		f.recs[0].UpdatedAt = findOld(f.recs[0])
		f.mu.Unlock()
		return s.RunOnce(context.Background())
	}
	_ = try(0) // 第一次失败,退避 10 分钟

	// 第 1 档:10 分钟内不重试
	if n := try(9 * time.Minute); n != 0 {
		t.Fatalf("第 1 档 9 分钟不该重试,实际 %d", n)
	}
	if n := try(2 * time.Minute); n != 1 {
		t.Fatalf("第 1 档 11 分钟应重试,实际 %d", n)
	}
	// 第 2 档:30 分钟
	if n := try(29 * time.Minute); n != 0 {
		t.Fatalf("第 2 档 29 分钟不该重试,实际 %d", n)
	}
	if n := try(2 * time.Minute); n != 1 {
		t.Fatalf("第 2 档 31 分钟应重试,实际 %d", n)
	}
	// 第 3 档:60 分钟
	if n := try(59 * time.Minute); n != 0 {
		t.Fatalf("第 3 档 59 分钟不该重试,实际 %d", n)
	}
	if n := try(2 * time.Minute); n != 1 {
		t.Fatalf("第 3 档 61 分钟应重试,实际 %d", n)
	}
	// 第 4 档起仍固定 60 分钟(不会退到更长)
	if n := try(59 * time.Minute); n != 0 {
		t.Fatalf("第 4 档 59 分钟不该重试,实际 %d", n)
	}
	if n := try(2 * time.Minute); n != 1 {
		t.Fatalf("第 4 档 61 分钟应重试,实际 %d", n)
	}
}

// findOld 给一条「很久以前更新过」的 updatedAt,保证 NextDue 一定判为到期。
func findOld(r Record) int64 {
	return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
}
