// Package nodequality 攒「节点质量」(概览页「节点质量」卡,GET /api/openbox/node-quality)要用的数据并算分。
//
// 依据:前端 api/openbox.ts 的 OpenboxNodeQuality 类型、NodeQualityCard.vue 的用法、zh.ts 里的说明文字
// (qualityModelHelp 等:权重、门槛、覆盖率口径),以及 sing-box 公开文档;没有参照上游服务端。
// 说明文字没写明的换算(延时 / 抖动 / 带宽怎么折成 0~100 分、状态怎么判、等级分几档)是自己定的,
// 常量集中在 score.go,等拿到上游的真实响应样本后再对表。
//
// 数据来源(都只增不改既有行为,通过「观察钩子」接进来):
//   - 延迟:latencyhistory 每合入一条新样本调一次 ObserveLatency(delay<=0 记失败);
//   - 带宽:bandwidth 每完成一次测速调一次 ObserveBandwidth;
//   - 使用时长与业务流量:Run 里每 10 秒读一次内核 /connections(排除面板自己的探测:来源是回环地址的连接),
//     按节点、按订阅各记一份(订阅那份并发只计一次)。
//
// 存储:按小时桶保留最近 24 小时(窗口外的桶、样本落盘前丢掉),文件 node-quality;
// 只在有变化时每分钟写一次盘(路由器闪存),退出时再写一次。
package nodequality

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const (
	fileName      = "node-quality"
	windowHours   = 24
	maxNames      = 5000
	maxNameLen    = 200
	maxPoints     = 48
	tickInterval  = 10 * time.Second
	flushInterval = time.Minute
	resolveEvery  = time.Minute
)

type bucket struct {
	H      int64 `json:"h"` // 整点,Unix 秒
	Checks int   `json:"c,omitempty"`
	OK     int   `json:"o,omitempty"`
	LatSum int64 `json:"ls,omitempty"` // 成功检测的耗时之和(毫秒)
	JitSum int64 `json:"js,omitempty"` // 连续两次成功检测耗时之差的绝对值之和
	JitN   int   `json:"jn,omitempty"`
	Up     int64 `json:"u,omitempty"`
	Down   int64 `json:"d,omitempty"`
	Active int64 `json:"a,omitempty"` // 使用时长(毫秒,采样估算)
}

type point struct {
	At     int64   `json:"at"` // 毫秒
	V      float64 `json:"v"`
	Failed bool    `json:"f,omitempty"`
}

type entry struct {
	Buckets   []bucket `json:"b,omitempty"`
	Lat       []point  `json:"lat,omitempty"`
	BW        []point  `json:"bw,omitempty"`
	First     int64    `json:"first,omitempty"` // 窗口内最早一次检测(毫秒)
	Last      int64    `json:"last,omitempty"`  // 最近一次检测(毫秒)
	LastOK    bool     `json:"lastOk,omitempty"`
	PrevDelay int      `json:"prev,omitempty"` // 上一次检测成功时的耗时;失败后清 0,抖动只算连续成功
}

type file struct {
	Nodes     map[string]*entry `json:"nodes"`
	Subs      map[string]*entry `json:"subs"`
	UpdatedAt int64             `json:"updatedAt"`
}

// NodeInfo 是目录里的一个节点。
type NodeInfo struct{ Tag, Region, Protocol string }

// Subscription 是目录里的一个启用的订阅。
type Subscription struct {
	ID, Name string
	Mode     string // login | subscription | node
	Nodes    []NodeInfo
}

// Service 攒数据并回答查询。零值不可用:至少要给 Store 和 Catalog。
type Service struct {
	Store *store.Store
	// Catalog 返回启用的订阅及其节点(顺序即展示顺序)。
	Catalog func(ctx context.Context) ([]Subscription, error)
	// Conns 读内核当前连接;nil 时不采集使用时长 / 业务流量。
	Conns func(ctx context.Context) (singbox.KernelSnapshot, error)
	// BandwidthWindow 是带宽样本算「当前」的最长年龄(两个统一周期);nil 用 12 小时。
	BandwidthWindow func(ctx context.Context) time.Duration
	Now             func() time.Time
	// Interval 是读连接的间隔;<=0 用 10 秒。
	Interval time.Duration

	mu     sync.Mutex
	loaded bool
	data   file
	dirty  bool

	have    bool
	prevAt  time.Time
	seen    map[string][2]int64
	owner   map[string]string // 节点 tag → 订阅 id
	ownerAt time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.data = file{Nodes: map[string]*entry{}, Subs: map[string]*entry{}}
	if s.Store == nil {
		return
	}
	var f file
	if ok, err := s.Store.Load(fileName, &f); err == nil && ok {
		if f.Nodes != nil {
			s.data.Nodes = f.Nodes
		}
		if f.Subs != nil {
			s.data.Subs = f.Subs
		}
		s.data.UpdatedAt = f.UpdatedAt
	}
}

func (e *entry) bucket(t time.Time) *bucket {
	u := t.Unix()
	h := u - ((u%3600)+3600)%3600
	for i := range e.Buckets {
		if e.Buckets[i].H == h {
			return &e.Buckets[i]
		}
	}
	e.Buckets = append(e.Buckets, bucket{H: h})
	sort.Slice(e.Buckets, func(i, j int) bool { return e.Buckets[i].H < e.Buckets[j].H })
	for i := range e.Buckets {
		if e.Buckets[i].H == h {
			return &e.Buckets[i]
		}
	}
	return &e.Buckets[len(e.Buckets)-1]
}

func insertPoint(list []point, p point) []point {
	list = append(list, p)
	sort.SliceStable(list, func(i, j int) bool { return list[i].At < list[j].At })
	if len(list) > maxPoints {
		list = list[len(list)-maxPoints:]
	}
	return list
}

func hasPoint(list []point, at int64) bool {
	for _, p := range list {
		if p.At == at {
			return true
		}
	}
	return false
}

// trim 丢掉窗口外的桶和样本,返回是否还有内容。
func (e *entry) trim(now time.Time) bool {
	cutS := now.Unix() - windowHours*3600
	b := e.Buckets[:0]
	for _, x := range e.Buckets {
		if x.H+3600 > cutS {
			b = append(b, x)
		}
	}
	e.Buckets = b
	cutMs := cutS * 1000
	keep := func(in []point) []point {
		out := in[:0]
		for _, p := range in {
			if p.At >= cutMs {
				out = append(out, p)
			}
		}
		return out
	}
	e.Lat, e.BW = keep(e.Lat), keep(e.BW)
	if e.Last < cutMs {
		e.First, e.Last, e.LastOK, e.PrevDelay = 0, 0, false, 0
	}
	return len(e.Buckets) > 0 || len(e.Lat) > 0 || len(e.BW) > 0
}

func (s *Service) nodeLocked(name string) *entry {
	e := s.data.Nodes[name]
	if e == nil {
		if len(s.data.Nodes) >= maxNames {
			return nil
		}
		e = &entry{}
		s.data.Nodes[name] = e
	}
	return e
}

func validName(name string) bool {
	return name != "" && len([]rune(name)) <= maxNameLen
}

func (s *Service) inWindow(at, now time.Time) bool {
	return !at.Before(now.Add(-windowHours*time.Hour)) && !at.After(now.Add(5*time.Minute))
}

// ObserveLatency 记一次节点延迟检测;delay<=0 表示失败。同一时刻的重复样本忽略(幂等),
// 乱序的样本只进计数、不参与抖动和「最近一次」。节点组的样本不要传进来。
func (s *Service) ObserveLatency(name string, at time.Time, delay int) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	now := s.now()
	if !s.inWindow(at, now) {
		return
	}
	e := s.nodeLocked(name)
	if e == nil {
		return
	}
	atMs := at.UnixMilli()
	if hasPoint(e.Lat, atMs) {
		return
	}
	ok := delay > 0
	b := e.bucket(at)
	b.Checks++
	inOrder := atMs > e.Last
	if ok {
		b.OK++
		b.LatSum += int64(delay)
		if inOrder && e.PrevDelay > 0 {
			d := delay - e.PrevDelay
			if d < 0 {
				d = -d
			}
			b.JitSum += int64(d)
			b.JitN++
		}
	}
	if inOrder {
		e.Last, e.LastOK = atMs, ok
		if ok {
			e.PrevDelay = delay
		} else {
			e.PrevDelay = 0
		}
	}
	if e.First == 0 || atMs < e.First {
		e.First = atMs
	}
	v := 0.0
	if ok {
		v = float64(delay)
	}
	e.Lat = insertPoint(e.Lat, point{At: atMs, V: v, Failed: !ok})
	s.dirty = true
}

// ObserveBandwidth 记一次下载测速(Mbps)。失败的只进图表,不参与评分。
func (s *Service) ObserveBandwidth(name string, at time.Time, mbps float64, failed bool) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if !s.inWindow(at, s.now()) {
		return
	}
	e := s.nodeLocked(name)
	if e == nil {
		return
	}
	atMs := at.UnixMilli()
	if hasPoint(e.BW, atMs) {
		return
	}
	if failed || mbps < 0 {
		mbps = 0
	}
	e.BW = insertPoint(e.BW, point{At: atMs, V: mbps, Failed: failed})
	s.dirty = true
}

func isLoopback(src string) bool {
	return src == "127.0.0.1" || src == "::1" || strings.HasPrefix(src, "127.")
}

func leaf(chains []string) string {
	if len(chains) == 0 {
		return ""
	}
	return chains[0]
}

func (s *Service) resolveOwner(ctx context.Context, now time.Time) {
	if s.Catalog == nil || (s.owner != nil && now.Sub(s.ownerAt) < resolveEvery) {
		return
	}
	subs, err := s.Catalog(ctx)
	if err != nil {
		return
	}
	m := map[string]string{}
	for _, sub := range subs {
		for _, n := range sub.Nodes {
			if _, dup := m[n.Tag]; !dup {
				m[n.Tag] = sub.ID
			}
		}
	}
	s.owner, s.ownerAt = m, now
}

// Tick 读一次内核连接,把这一轮的流量增量和使用时长记进去。第一次只记基线。
// 使用时长按「这一轮有字节增量」估算,每个节点、每个订阅同一轮只加一次(并发使用只计一次)。
func (s *Service) Tick(ctx context.Context) error {
	if s.Conns == nil {
		return nil
	}
	snap, err := s.Conns(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	now := s.now()
	if err != nil {
		s.have = false
		return err
	}
	seen := make(map[string][2]int64, len(snap.Conns))
	if !s.have {
		for _, k := range snap.Conns {
			seen[k.ID] = [2]int64{k.Upload, k.Download}
		}
		s.have, s.prevAt, s.seen = true, now, seen
		return nil
	}
	iv := s.Interval
	if iv <= 0 {
		iv = tickInterval
	}
	elapsed := now.Sub(s.prevAt)
	if elapsed <= 0 || elapsed > 3*iv {
		elapsed = iv
	}
	s.resolveOwner(ctx, now)
	direct := map[string]bool{}
	for _, t := range snap.DirectTags {
		direct[t] = true
	}
	type delta struct{ up, down int64 }
	nodes := map[string]*delta{}
	for _, k := range snap.Conns {
		seen[k.ID] = [2]int64{k.Upload, k.Download}
		if isLoopback(k.Source) {
			continue // 面板自己的探测(测速、站点延迟、路由测试)不算使用
		}
		tag := leaf(k.Chains)
		if tag == "" || direct[tag] {
			continue
		}
		if s.owner != nil {
			if _, ok := s.owner[tag]; !ok {
				continue
			}
		}
		prev, old := s.seen[k.ID]
		up, down := k.Upload-prev[0], k.Download-prev[1]
		if !old || up < 0 || down < 0 {
			up, down = k.Upload, k.Download
		}
		if up == 0 && down == 0 {
			continue
		}
		d := nodes[tag]
		if d == nil {
			d = &delta{}
			nodes[tag] = d
		}
		d.up += up
		d.down += down
	}
	s.seen, s.prevAt = seen, now
	if len(nodes) == 0 {
		return nil
	}
	ms := elapsed.Milliseconds()
	subActive := map[string]*delta{}
	for tag, d := range nodes {
		if e := s.nodeLocked(tag); e != nil {
			b := e.bucket(now)
			b.Up += d.up
			b.Down += d.down
			b.Active += ms
		}
		if id := s.owner[tag]; id != "" {
			sd := subActive[id]
			if sd == nil {
				sd = &delta{}
				subActive[id] = sd
			}
			sd.up += d.up
			sd.down += d.down
		}
	}
	for id, d := range subActive {
		e := s.data.Subs[id]
		if e == nil {
			e = &entry{}
			s.data.Subs[id] = e
		}
		b := e.bucket(now)
		b.Up += d.up
		b.Down += d.down
		b.Active += ms
	}
	s.dirty = true
	return nil
}

// Flush 把有变化的数据落盘(同时丢掉窗口外的内容、已不在目录里的订阅)。
func (s *Service) Flush(ctx context.Context) error {
	var cat map[string]bool
	if s.Catalog != nil {
		if subs, err := s.Catalog(ctx); err == nil {
			cat = map[string]bool{}
			for _, sub := range subs {
				cat[sub.ID] = true
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if !s.dirty || s.Store == nil {
		return nil
	}
	now := s.now()
	for k, e := range s.data.Nodes {
		if !e.trim(now) {
			delete(s.data.Nodes, k)
		}
	}
	for id, e := range s.data.Subs {
		if !e.trim(now) || (cat != nil && !cat[id]) {
			delete(s.data.Subs, id)
		}
	}
	s.data.UpdatedAt = now.UnixMilli()
	if err := s.Store.Save(fileName, s.data); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// Run 每隔一小段时间读一次内核连接、每分钟把有变化的数据落盘,直到 ctx 结束(结束前再落盘一次)。
func (s *Service) Run(ctx context.Context) {
	iv := s.Interval
	if iv <= 0 {
		iv = tickInterval
	}
	tick := time.NewTicker(iv)
	defer tick.Stop()
	flush := time.NewTicker(flushInterval)
	defer flush.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.Flush(fctx); err != nil {
				slog.Warn("节点质量:退出前写盘失败", "error", err)
			}
			cancel()
			return
		case <-tick.C:
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			_ = s.Tick(cctx) // 内核没起来读不到时不报错、不记
			cancel()
		case <-flush.C:
			fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := s.Flush(fctx); err != nil {
				slog.Warn("节点质量:写盘失败", "error", err)
			}
			cancel()
		}
	}
}
