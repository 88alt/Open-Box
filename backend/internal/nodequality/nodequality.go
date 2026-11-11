// Package nodequality 攒「节点质量」(概览页「节点质量」卡,GET /api/openbox/node-quality)要用的数据并算分。
//
// 口径对齐上游 v0.1.423 的服务端(panel/server/index.mjs 里的 node-quality):
// 按小时存检测统计、中位数延时、连续探测的波动、下载最近 10 次的平均、分段线性评分、订阅汇总与分档。
// 评分和汇总在 score.go,按小时存的原始统计在 hours.go,查询与响应形状在 query.go。
//
// 与上游不同的地方(都是这边缺少对应的来源):
//   - 上游按「节点配置的身份」记账,改名不丢历史、同一个节点不重复计;这里按节点 tag 记;
//   - 上游只记「探测地址与档案里设的测试地址一致」的样本;延迟历史没有记地址,这里全部接收;
//   - 上游每个节点有自己的「新鲜时限」;这里统一 10 分钟(Service.FreshFor 可改);
//   - 上传没有测速来源,恒为 null。
//
// 数据来源:
//   - 延迟:latencyhistory 每合入一条新样本调一次 ObserveLatency(delay<=0 记失败);
//   - 带宽:查询时从带宽服务的历史读(Service.Bandwidth),不另存;
//   - 使用时长与业务流量:Run 里每 10 秒读一次内核 /connections(排除面板自己的探测:来源是回环地址的连接)。
//
// 存储:文件 node-quality,只在有变化时每分钟写一次盘(路由器闪存),退出时再写一次。
package nodequality

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

const (
	fileName      = "node-quality"
	fileVersion   = 2
	maxNames      = 5000
	maxNameLen    = 200
	tickInterval  = 10 * time.Second
	flushInterval = time.Minute
	resolveEvery  = time.Minute
)

type file struct {
	V         int                  `json:"v"`
	Nodes     map[string]nodeHours `json:"nodes"`
	UpdatedAt int64                `json:"updatedAt"`
}

// NodeInfo 是目录里的一个节点。
type NodeInfo struct{ Tag, Region, Protocol string }

// Subscription 是目录里的一个订阅。
type Subscription struct {
	ID, Name       string
	Mode           string // login | subscription | node
	Enabled        bool
	BandwidthCheck bool // 开了定期带宽检查
	Nodes          []NodeInfo
}

// BWSample 是一次下载测速。
type BWSample struct {
	At         int64 // 毫秒
	Mbps       float64
	Failed     bool
	LowerBound bool
}

// HistSample 是延迟历史里一条节点自己的样本。
type HistSample struct {
	At    int64 // 毫秒
	Delay int   // 0 = 不通
}

// Service 攒数据并回答查询。零值不可用:至少要给 Store 和 Catalog。
type Service struct {
	Store *store.Store
	// Catalog 返回订阅及其节点(顺序即展示顺序)。
	Catalog func(ctx context.Context) ([]Subscription, error)
	// Conns 读内核当前连接;nil 时不采集使用时长 / 业务流量。
	Conns func(ctx context.Context) (singbox.KernelSnapshot, error)
	// Period 是带宽测速周期;nil 或 <=0 用 12 小时。下载样本超过两个周期不算「当前」。
	Period func(ctx context.Context) time.Duration
	// Bandwidth 取一个节点的下载测速历史(从旧到新);nil 时没有下载数据。不能在回调里再进本服务。
	Bandwidth func(tag string) []BWSample
	// History 取一个节点的延迟历史,图表里和自己攒的探测合并;nil 时只用自己攒的。不能在回调里再进本服务。
	History func(ctx context.Context, tag string) []HistSample
	// FreshFor 是检测结果的新鲜时限;<=0 用 10 分钟。
	FreshFor time.Duration
	Now      func() time.Time
	// Interval 是读连接的间隔;<=0 用 10 秒。
	Interval time.Duration

	mu     sync.Mutex
	loaded bool
	data   file
	dirty  bool

	have    bool
	prevAt  time.Time
	seen    map[string][2]int64
	known   map[string]bool // 目录里的节点 tag;nil = 目录读不到,不过滤
	knownAt time.Time
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
	s.data = file{V: fileVersion, Nodes: map[string]nodeHours{}}
	if s.Store == nil {
		return
	}
	var f file
	if ok, err := s.Store.Load(fileName, &f); err == nil && ok && f.V == fileVersion {
		if f.Nodes != nil {
			s.data.Nodes = f.Nodes
		}
		s.data.UpdatedAt = f.UpdatedAt
	}
}

func (s *Service) nodeLocked(name string) nodeHours {
	n := s.data.Nodes[name]
	if n == nil {
		if len(s.data.Nodes) >= maxNames {
			return nil
		}
		n = nodeHours{}
		s.data.Nodes[name] = n
	}
	return n
}

func validName(name string) bool {
	return name != "" && len([]rune(name)) <= maxNameLen
}

// ObserveLatency 记一次节点延迟检测;delay==0 表示失败,delay<0 不记。
// 时刻不在 24 小时窗口里、或不比同一整点里最近一次更晚(重复、乱序)的样本忽略。节点组的样本不要传进来。
func (s *Service) ObserveLatency(name string, at time.Time, delay int) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	n := s.nodeLocked(name)
	if n == nil {
		return
	}
	if n.recordProbe(at.UnixMilli(), delay, s.now().UnixMilli()) {
		s.dirty = true
	} else if len(n) == 0 {
		delete(s.data.Nodes, name)
	}
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

func (s *Service) resolveKnown(ctx context.Context, now time.Time) {
	if s.Catalog == nil || (s.known != nil && now.Sub(s.knownAt) < resolveEvery) {
		return
	}
	subs, err := s.Catalog(ctx)
	if err != nil {
		return
	}
	m := map[string]bool{}
	for _, sub := range subs {
		for _, n := range sub.Nodes {
			m[n.Tag] = true
		}
	}
	s.known, s.knownAt = m, now
}

// Tick 读一次内核连接,把这一轮的流量增量记进去。第一次只记基线。
// 使用时长按「这一轮有字节增量」把最近一段时间(最多 10 秒)铺成 2 秒一格的活跃槽位。
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
	s.resolveKnown(ctx, now)
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
		if s.known != nil && !s.known[tag] {
			continue
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
	for tag, d := range nodes {
		if n := s.nodeLocked(tag); n != nil {
			n.recordTraffic(now.UnixMilli(), d.up, d.down, elapsed.Milliseconds())
			s.dirty = true
		}
	}
	return nil
}

// Flush 把有变化的数据落盘(同时丢掉窗口外的整点)。
func (s *Service) Flush(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if !s.dirty || s.Store == nil {
		return nil
	}
	nowMs := s.now().UnixMilli()
	for k, n := range s.data.Nodes {
		if !n.trim(nowMs) {
			delete(s.data.Nodes, k)
		}
	}
	s.data.UpdatedAt = nowMs
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
