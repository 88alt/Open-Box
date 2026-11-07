package sysres

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const statA = "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 50 0 50 400 0 0 0 0 0 0\ncpu1 50 0 50 400 0 0 0 0 0 0\nintr 1\n"

// 总量 +950(空闲 +750)→ 21.1%;cpu0 +450(空闲 +250)→ 44.4%;cpu1 +500(空闲 +500)→ 0%
const statB = "cpu  200 0 200 1550 0 0 0 0 0 0\ncpu0 150 0 150 650 0 0 0 0 0 0\ncpu1 50 0 50 900 0 0 0 0 0 0\n"

const meminfo = "MemTotal:        1000 kB\nMemFree:          100 kB\nMemAvailable:     400 kB\nBuffers: 10 kB\nCached: 20 kB\n"

func fixture(t *testing.T) (*Service, string, *time.Time) {
	dir := t.TempDir()
	write(t, dir, "stat", statA)
	write(t, dir, "meminfo", meminfo)
	write(t, dir, "self/status", "Name:\topenbox-backend\nVmRSS:\t   2000 kB\n")
	write(t, dir, "77/comm", "sing-box\n")
	write(t, dir, "77/status", "Name:\tsing-box\nVmRSS:\t   3000 kB\n")
	write(t, dir, "88/comm", "dnsmasq\n")
	write(t, dir, "88/status", "VmRSS:\t 9999 kB\n")
	now := time.UnixMilli(1_700_000_000_000)
	s := &Service{ProcDir: dir, Now: func() time.Time { return now }}
	// 第一次采样的「等 200ms」在测试里变成:写入第二份 /proc/stat 并把时间往前拨
	s.Sleep = func(context.Context, time.Duration) {
		write(t, dir, "stat", statB)
		now = now.Add(time.Second)
	}
	return s, dir, &now
}

func TestSnapshotCPUMemory(t *testing.T) {
	s, _, now := fixture(t)
	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.CPU.Usage != 21.1 {
		t.Fatalf("总使用率: %v", snap.CPU.Usage)
	}
	if len(snap.CPU.Cores) != 2 || snap.CPU.Cores[0].ID != "cpu0" || snap.CPU.Cores[0].Usage != 44.4 || snap.CPU.Cores[1].Usage != 0 {
		t.Fatalf("每核: %+v", snap.CPU.Cores)
	}
	m := snap.Memory
	if m == nil || m.Total != 1000*1024 || m.Available != 400*1024 || m.Used != 600*1024 {
		t.Fatalf("内存: %+v", m)
	}
	if m.Openbox == nil || *m.Openbox != 5000*1024 { // 后端自己 2000 + sing-box 3000,不含 dnsmasq
		t.Fatalf("Open-Box 常驻内存: %v", m.Openbox)
	}
	if snap.At != now.UnixMilli() || snap.ActiveTerminals != nil {
		t.Fatalf("%+v", snap)
	}
}

// 距上一次采样不到 500ms:CPU 直接用上一次的结果,不重新采
func TestCPUThrottled(t *testing.T) {
	s, dir, now := fixture(t)
	first, _ := s.Snapshot(context.Background())
	write(t, dir, "stat", "cpu  1 0 1 1 0 0 0 0 0 0\n")
	*now = now.Add(100 * time.Millisecond)
	again, err := s.Snapshot(context.Background())
	if err != nil || again.CPU.Usage != first.CPU.Usage || len(again.CPU.Cores) != 2 {
		t.Fatalf("%+v %v", again.CPU, err)
	}
	// 过了 500ms 才重新采;这里计数回绕(总量变小)按 0 算,不出负数
	*now = now.Add(time.Second)
	later, _ := s.Snapshot(context.Background())
	if later.CPU.Usage != 0 {
		t.Fatalf("%+v", later.CPU)
	}
}

func TestMemAvailableFallbackAndNoOpenbox(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stat", statA)
	write(t, dir, "meminfo", "MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 10 kB\nCached: 20 kB\n")
	s := &Service{ProcDir: dir, Sleep: func(context.Context, time.Duration) {}}
	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := snap.Memory
	if m == nil || m.Available != 130*1024 || m.Used != 870*1024 || m.Openbox != nil {
		t.Fatalf("%+v", m)
	}
}

func TestNoProcStat(t *testing.T) {
	s := &Service{ProcDir: t.TempDir()}
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Fatal("读不到 /proc/stat 应该报错")
	}
}

func TestActiveTerminals(t *testing.T) {
	s, _, _ := fixture(t)
	s.Sources = func(context.Context) ([]string, error) {
		return []string{
			"192.168.1.10", "fd00::10", // 同一个 MAC → 1 台
			"192.168.1.11", "192.168.1.11", // 重复 → 1 台
			"192.168.1.12", // 没有邻居表项 → 1 台
			"192.168.1.1",  // 本机
			"127.0.0.1", "::1", "0.0.0.0", "", "bad",
			"::ffff:192.168.1.11", // 映射地址和上面的 IPv4 是同一台
			"fe80::2%br-lan",      // 带 zone,没有邻居表项 → 1 台
		}, nil
	}
	s.Neighbors = func(context.Context) map[string]string {
		return map[string]string{
			"192.168.1.10": "aa:aa", "fd00::10": "aa:aa", "192.168.1.11": "bb:bb",
		}
	}
	s.LocalAddrs = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("192.168.1.1")} }
	snap, err := s.Snapshot(context.Background())
	if err != nil || snap.ActiveTerminals == nil || *snap.ActiveTerminals != 4 {
		t.Fatalf("%+v %v", snap.ActiveTerminals, err)
	}
	// 读不到连接(内核没跑):不给这个字段,其余照常
	s.Sources = func(context.Context) ([]string, error) { return nil, context.DeadlineExceeded }
	snap, err = s.Snapshot(context.Background())
	if err != nil || snap.ActiveTerminals != nil || snap.Memory == nil {
		t.Fatalf("%+v %v", snap, err)
	}
}

func TestParseARPAndNeigh(t *testing.T) {
	arp := "IP address       HW type     Flags       HW address            Mask     Device\n" +
		"192.168.1.10     0x1         0x2         AA:BB:CC:DD:EE:01     *        br-lan\n" +
		"192.168.1.11     0x1         0x0         00:00:00:00:00:00     *        br-lan\n"
	got := ParseARP([]byte(arp))
	if len(got) != 1 || got["192.168.1.10"] != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("%v", got)
	}
	neigh := "fe80::1 dev br-lan lladdr AA:BB:CC:DD:EE:01 REACHABLE\n" +
		"fd00::5 dev br-lan  FAILED\n" +
		"fd00::6 dev br-lan lladdr aa:bb:cc:dd:ee:02 STALE\n"
	n := ParseIPNeigh([]byte(neigh))
	if len(n) != 2 || n["fe80::1"] != "aa:bb:cc:dd:ee:01" || n["fd00::6"] != "aa:bb:cc:dd:ee:02" {
		t.Fatalf("%v", n)
	}
	if !strings.Contains(statA, "cpu0") {
		t.Fatal("fixture")
	}
}
