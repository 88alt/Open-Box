// Package sysres 实现 /api/openbox/system/resources 背后的采样:CPU(总 + 每核)、内存(总 / 可用 / 已用 / Open-Box 常驻)、
// 活跃终端数。响应形状取自前端 api/openbox.ts 的 SystemResources 和概览页「硬件负载」、侧边栏卡片的用法
// (阈值着色、每核展开、内存三段进度条、概览的「活跃终端」),没有参照上游。
//
//   - CPU 读 /proc/stat,用两次采样的差算使用率;第一次调用没有上一次,隔 200ms 再采一次。距上一次采样不到 500ms 的调用
//     直接用上一次的结果(多个页面各自轮询时不会把采样间隔压得太短,使用率抖得没意义)。
//   - 内存读 /proc/meminfo:已用 = 总 − 可用(MemAvailable,老内核没有就用 MemFree + Buffers + Cached)。
//     Open-Box 常驻内存 = 后端自己 + 内核进程(comm 为 sing-box)的 VmRSS 之和,读不到进程就不给这个字段。
//   - 活跃终端:内核当前连接里出现的来源 IP,去掉本机地址、回环、未指定地址;已知 MAC 的把 IPv4 / IPv6 合成一个,
//     没有邻居表项的 IP 各算一个。读不到连接(内核没跑)就不给这个字段。
package sysres

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Core 是一个 CPU 核的负载。ID 形如 cpu0(前端转成大写显示)。
type Core struct {
	ID    string  `json:"id"`
	Usage float64 `json:"usage"`
}

// CPU 总负载和每核负载,单位 %,一位小数。
type CPU struct {
	Usage float64 `json:"usage"`
	Cores []Core  `json:"cores"`
}

// Memory 单位字节。Openbox 读不到时为 nil。
type Memory struct {
	Total     int64  `json:"total"`
	Available int64  `json:"available"`
	Used      int64  `json:"used"`
	Openbox   *int64 `json:"openbox,omitempty"`
}

// Snapshot 是某一刻的系统资源。At 是毫秒时间戳。
type Snapshot struct {
	At              int64   `json:"at"`
	CPU             CPU     `json:"cpu"`
	Memory          *Memory `json:"memory,omitempty"`
	ActiveTerminals *int    `json:"activeTerminals,omitempty"`
}

// Service 采样。零值可用(读真实的 /proc,没有终端统计)。
type Service struct {
	// ProcDir 默认 /proc。
	ProcDir string
	// Now 默认 time.Now。
	Now func() time.Time
	// Sleep 第一次采样时的间隔,默认等 200ms(可被 ctx 取消);测试里换成空函数。
	Sleep func(ctx context.Context, d time.Duration)
	// Sources 返回内核当前连接的来源 IP;nil = 不统计活跃终端。
	Sources func(ctx context.Context) ([]string, error)
	// Neighbors 返回邻居表里 IP → MAC(小写);nil 或出错 = 都当作未知 MAC。
	Neighbors func(ctx context.Context) map[string]string
	// LocalAddrs 返回本机所有接口地址(要从终端里排除);nil = 不排除。
	LocalAddrs func() []netip.Addr
	// KernelComm 内核进程的 comm,默认 sing-box;用来算 Open-Box 常驻内存。
	KernelComm string

	mu      sync.Mutex
	prev    cpuSample
	prevAt  time.Time
	hasPrev bool
	cpu     CPU
}

const (
	firstGap = 200 * time.Millisecond
	minGap   = 500 * time.Millisecond
)

func (s *Service) procDir() string {
	if s.ProcDir != "" {
		return s.ProcDir
	}
	return "/proc"
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Snapshot 采一次。读不到 /proc/stat(不是 Linux)返回 error;内存读不到、终端读不到只是少对应的字段。
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	cpu, err := s.sampleCPU(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{At: s.now().UnixMilli(), CPU: cpu}
	out.Memory = s.memory()
	if s.Sources != nil {
		if srcs, err := s.Sources(ctx); err == nil {
			n := s.countTerminals(ctx, srcs)
			out.ActiveTerminals = &n
		}
	}
	return out, nil
}

// ---- CPU ----

type cpuTimes struct{ idle, total uint64 }

type cpuSample struct {
	all   cpuTimes
	cores []namedTimes
}

type namedTimes struct {
	id string
	t  cpuTimes
}

func (s *Service) readCPU() (cpuSample, error) {
	b, err := os.ReadFile(s.procDir() + "/stat")
	if err != nil {
		return cpuSample{}, err
	}
	return parseStat(b)
}

func parseStat(b []byte) (cpuSample, error) {
	var out cpuSample
	got := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var vals []uint64
		for _, x := range f[1:] {
			v, err := strconv.ParseUint(x, 10, 64)
			if err != nil {
				break
			}
			vals = append(vals, v)
		}
		if len(vals) < 4 {
			continue
		}
		// user nice system idle iowait irq softirq steal(guest 已经算在 user 里,不重复加)
		var total uint64
		for i, v := range vals {
			if i >= 8 {
				break
			}
			total += v
		}
		idle := vals[3]
		if len(vals) > 4 {
			idle += vals[4]
		}
		t := cpuTimes{idle: idle, total: total}
		if f[0] == "cpu" {
			out.all = t
			got = true
		} else {
			out.cores = append(out.cores, namedTimes{id: f[0], t: t})
		}
	}
	if !got {
		return cpuSample{}, errors.New("/proc/stat 里没有 cpu 行")
	}
	return out, nil
}

func usage(prev, cur cpuTimes) float64 {
	if cur.total <= prev.total {
		return 0
	}
	dt := float64(cur.total - prev.total)
	di := float64(0)
	if cur.idle > prev.idle {
		di = float64(cur.idle - prev.idle)
	}
	u := (1 - di/dt) * 100
	return math.Round(math.Min(100, math.Max(0, u))*10) / 10
}

func (s *Service) sampleCPU(ctx context.Context) (CPU, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.hasPrev && now.Sub(s.prevAt) < minGap {
		return s.cpu, nil
	}
	if !s.hasPrev {
		first, err := s.readCPU()
		if err != nil {
			return CPU{}, err
		}
		s.prev, s.prevAt, s.hasPrev = first, now, true
		if s.Sleep != nil {
			s.Sleep(ctx, firstGap)
		} else {
			select {
			case <-time.After(firstGap):
			case <-ctx.Done():
				return CPU{}, ctx.Err()
			}
		}
		now = s.now()
	}
	cur, err := s.readCPU()
	if err != nil {
		return CPU{}, err
	}
	res := CPU{Usage: usage(s.prev.all, cur.all), Cores: []Core{}}
	prevCores := make(map[string]cpuTimes, len(s.prev.cores))
	for _, c := range s.prev.cores {
		prevCores[c.id] = c.t
	}
	for _, c := range cur.cores {
		res.Cores = append(res.Cores, Core{ID: c.id, Usage: usage(prevCores[c.id], c.t)})
	}
	s.prev, s.prevAt, s.cpu = cur, now, res
	return res, nil
}

// ---- 内存 ----

func (s *Service) memory() *Memory {
	b, err := os.ReadFile(s.procDir() + "/meminfo")
	if err != nil {
		return nil
	}
	kv := map[string]int64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		kv[name] = v * 1024 // meminfo 单位是 kB
	}
	total := kv["MemTotal"]
	if total <= 0 {
		return nil
	}
	avail, ok := kv["MemAvailable"]
	if !ok {
		avail = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	avail = min(max(avail, 0), total)
	m := &Memory{Total: total, Available: avail, Used: total - avail}
	if rss, ok := s.openboxRSS(); ok {
		m.Openbox = &rss
	}
	return m
}

func (s *Service) rssOf(pid string) (int64, bool) {
	b, err := os.ReadFile(s.procDir() + "/" + pid + "/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				if v, err := strconv.ParseInt(f[0], 10, 64); err == nil {
					return v * 1024, true
				}
			}
		}
	}
	return 0, false
}

// openboxRSS 后端自己 + 内核进程的常驻内存之和;一个都读不到返回 false。
func (s *Service) openboxRSS() (int64, bool) {
	var sum int64
	found := false
	if v, ok := s.rssOf("self"); ok {
		sum += v
		found = true
	}
	comm := s.KernelComm
	if comm == "" {
		comm = "sing-box"
	}
	for _, pid := range system.FindProcs(s.procDir(), comm) {
		if v, ok := s.rssOf(strconv.Itoa(pid)); ok {
			sum += v
			found = true
		}
	}
	return sum, found
}

// ---- 活跃终端 ----

func (s *Service) countTerminals(ctx context.Context, sources []string) int {
	local := map[netip.Addr]bool{}
	if s.LocalAddrs != nil {
		for _, a := range s.LocalAddrs() {
			local[a.Unmap()] = true
		}
	}
	var neigh map[string]string
	if s.Neighbors != nil {
		neigh = s.Neighbors(ctx)
	}
	seen := map[string]bool{}
	for _, raw := range sources {
		a, ok := parseAddr(raw)
		if !ok || a.IsLoopback() || a.IsUnspecified() || local[a] {
			continue
		}
		key := a.String()
		if mac := neigh[key]; mac != "" {
			key = "mac:" + mac
		}
		seen[key] = true
	}
	return len(seen)
}

func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '%'); i >= 0 { // IPv6 zone
		s = s[:i]
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// ParseARP 解析 /proc/net/arp:IP → MAC(小写),跳过没解析成功的(flags 0x0 或全 0 的 MAC)。
func ParseARP(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Scan() // 表头
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		mac := strings.ToLower(f[3])
		if f[2] == "0x0" || mac == "00:00:00:00:00:00" {
			continue
		}
		if a, ok := parseAddr(f[0]); ok {
			out[a.String()] = mac
		}
	}
	return out
}

// ParseIPNeigh 解析 `ip -6 neigh show` 的输出:形如 `fe80::1 dev br-lan lladdr aa:bb:cc:dd:ee:ff REACHABLE`,
// 没有 lladdr 的(FAILED / INCOMPLETE)跳过。
func ParseIPNeigh(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 1 {
			continue
		}
		mac := ""
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "lladdr" {
				mac = strings.ToLower(f[i+1])
				break
			}
		}
		if mac == "" {
			continue
		}
		if a, ok := parseAddr(f[0]); ok {
			out[a.String()] = mac
		}
	}
	return out
}

// Neighbors 返回读真实邻居表的函数:IPv4 读 /proc/net/arp,IPv6 用 run 跑 `ip -6 neigh show`(run 为 nil 就只有 IPv4)。
func Neighbors(procDir string, run system.Runner) func(ctx context.Context) map[string]string {
	if procDir == "" {
		procDir = "/proc"
	}
	return func(ctx context.Context) map[string]string {
		out := map[string]string{}
		if b, err := os.ReadFile(procDir + "/net/arp"); err == nil {
			for k, v := range ParseARP(b) {
				out[k] = v
			}
		}
		if run != nil {
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if res, err := run(cctx, "ip", "-6", "neigh", "show"); err == nil && res.Code == 0 {
				for k, v := range ParseIPNeigh([]byte(res.Stdout)) {
					out[k] = v
				}
			}
		}
		return out
	}
}

// LocalAddrs 返回本机所有接口上的 IP 地址(排除终端统计用)。读不到返回空。
func LocalAddrs() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr().Unmap())
		}
	}
	return out
}
