package traffic

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// 三条时间线的边界:正好落在界上的那天不该被处理,差一天才处理。
// (TestMaintain 覆盖了主流程;这里钉的是 off-by-one。)
func TestMaintainBoundaries(t *testing.T) {
	now := at("2026-10-28", 12, 0)
	s, ck, dir := newStore(t, at("2026-01-01", 12, 0))
	rec := func(day string, hosts int) {
		items := make([]Item, 0, hosts)
		for i := 0; i < hosts; i++ {
			items = append(items, item("c", "n", fmt.Sprintf("%s-h%03d.com", day, i), 1, 0, 1, false))
		}
		if err := s.Record(at(day, 9, 0), Delta{Up: int64(hosts), Conns: int64(hosts), Items: items}); err != nil {
			t.Fatal(err)
		}
	}

	// 小时明细:正好 7 天前(2026-10-21)还在,8 天前(2026-10-20)删
	rec("2026-10-21", 3)
	rec("2026-10-20", 3)
	// 合并:正好 30 天前(2026-09-28)不合并,31 天前(2026-09-27)合并
	rec("2026-09-28", 300)
	rec("2026-09-27", 300)

	ck.t = now
	if err := s.Maintain(now, 12); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "2026-10-21.hours.json")); err != nil {
		t.Errorf("正好 7 天前的小时明细该保留:%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10-20.hours.json")); !os.IsNotExist(err) {
		t.Errorf("8 天前的小时明细该删:%v", err)
	}

	// 正好 30 天前:访问目标数不变(没合并);31 天前:压到 keepHosts+1
	r28, err := s.DayView("2026-09-28", 5000, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if r28.HostsCount <= keepHosts+1 {
		t.Errorf("正好 30 天前不该合并,访问目标应仍多于 %d,实际 %d", keepHosts+1, r28.HostsCount)
	}
	r27, err := s.DayView("2026-09-27", 5000, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if r27.HostsCount != keepHosts+1 {
		t.Errorf("31 天前该合并成 %d 个,实际 %d", keepHosts+1, r27.HostsCount)
	}
}

// keepMonths 的上下限:<=0 按 12 个月,>36 按 36 个月(不让用户把保留期设成永久或超长)。
func TestMaintainKeepMonthsClamp(t *testing.T) {
	now := at("2026-10-28", 12, 0)
	for _, c := range []struct {
		in       int
		delOlder string // 早于这一天的会被删
	}{
		{0, "2025-10-28"},  // 按 12 个月
		{-5, "2025-10-28"}, // 负数按 12 个月
		{36, "2023-10-28"}, // 上限 36 个月
		{99, "2023-10-28"}, // 超过上限按 36 个月
	} {
		s, ck, dir := newStore(t, at("2026-01-01", 12, 0))
		if err := s.Record(at("2026-10-01", 9, 0), Delta{Up: 1}); err != nil {
			t.Fatal(err)
		}
		// 造一天比 cutDel 还早的数据:用 2024-01-01(对 12 个月与 36 个月都该留)
		if err := s.Record(at("2024-06-01", 9, 0), Delta{Up: 1}); err != nil {
			t.Fatal(err)
		}
		ck.t = now
		if err := s.Maintain(now, c.in); err != nil {
			t.Fatalf("keepMonths=%d:%v", c.in, err)
		}
		// 2025-10-28 之后的数据一定在;2024-06-01 在 36 个月档该留下、12 个月档该删
		if _, err := os.Stat(filepath.Join(dir, "2026-10-01.json")); err != nil {
			t.Errorf("keepMonths=%d:近期的数据被误删:%v", c.in, err)
		}
		_, err := os.Stat(filepath.Join(dir, "2024-06-01.json"))
		wantKept := c.delOlder == "2023-10-28"
		if wantKept && err != nil {
			t.Errorf("keepMonths=%d:36 个月档应保留 2024-06-01:%v", c.in, err)
		}
		if !wantKept && !os.IsNotExist(err) {
			t.Errorf("keepMonths=%d:12 个月档应删掉 2024-06-01:%v", c.in, err)
		}
	}
}
