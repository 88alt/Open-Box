package bandwidth

import (
	"context"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

// Sample 对应 OpenboxBandwidthSample。
type Sample struct {
	At         int64   `json:"at"`
	Mbps       float64 `json:"mbps"`
	Failed     bool    `json:"failed,omitempty"`
	LowerBound bool    `json:"lowerBound,omitempty"`
}

// Measurement 对应 OpenboxBandwidthMeasurement。
type Measurement struct {
	Sample
	Enabled     bool     `json:"enabled,omitempty"`
	Pending     bool     `json:"pending,omitempty"`
	RequestedAt int64    `json:"requestedAt,omitempty"`
	CompletedAt int64    `json:"completedAt,omitempty"`
	Error       string   `json:"error,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	History     []Sample `json:"history,omitempty"`
}

// Status 对应 OpenboxBandwidthStatus。
type Status struct {
	Settings              Settings               `json:"settings"`
	Nodes                 int                    `json:"nodes"`
	DailyBytes            int64                  `json:"dailyBytes"`
	Running               bool                   `json:"running"`
	LastError             string                 `json:"lastError"`
	UpdatedAt             int64                  `json:"updatedAt"`
	Measurements          map[string]Measurement `json:"measurements,omitempty"`
	PendingBySubscription map[string]int         `json:"pendingBySubscription,omitempty"`
}

// QueueResult 对应 OpenboxBandwidthQueueResult。
type QueueResult struct {
	Queued                int            `json:"queued"`
	Joined                int            `json:"joined,omitempty"`
	RequestedAt           int64          `json:"requestedAt,omitempty"`
	PendingBySubscription map[string]int `json:"pendingBySubscription"`
}

// ProfileReader 读档案。
type ProfileReader func(ctx context.Context) (map[string]any, error)

// NodeLister 列出可测节点:name、所属订阅 id、是否启用定期检查。
type NodeLister func(ctx context.Context) ([]NodeRef, error)

// NodeRef 是一个可排队的节点。
type NodeRef struct {
	Name           string
	SubscriptionID string
	Enabled        bool // 订阅开了定期带宽检查
}

// Service 带宽状态与排队(测速执行另接;本包先完成状态机与 API 形状)。
type Service struct {
	Store   *store.Store
	Profile ProfileReader
	Nodes   NodeLister
	Now     func() time.Time
	Measure Measurer

	mu       sync.Mutex
	running  bool
	lastErr  string
	updated  int64
	pending  map[string]queueItem // node name → item
	measures map[string]Measurement
}

type queueItem struct {
	SubID       string
	RequestedAt int64
	Manual      bool
}

const histFile = "bandwidth-history"

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Status 汇总当前测速参数与排队情况。
func (s *Service) Status(ctx context.Context) (Status, error) {
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return Status{}, err
	}
	nodes, err := s.listNodes(ctx)
	if err != nil {
		return Status{}, err
	}
	enabledCount := 0
	for _, n := range nodes {
		if n.Enabled {
			enabledCount++
		}
	}
	// 按周期估算:每天测次数 * 单次上限
	perDay := 24.0 / float64(settings.IntervalHours)
	daily := int64(perDay * float64(settings.MaxDownloadMB) * float64(enabledCount) * 1024 * 1024)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.measures == nil {
		s.measures = map[string]Measurement{}
	}
	if s.pending == nil {
		s.pending = map[string]queueItem{}
	}
	// 合并 pending 到 measurements
	ms := map[string]Measurement{}
	for k, v := range s.measures {
		ms[k] = v
	}
	pendingBy := map[string]int{}
	for name, q := range s.pending {
		m := ms[name]
		m.Pending = true
		m.RequestedAt = q.RequestedAt
		ms[name] = m
		pendingBy[q.SubID]++
	}
	for _, n := range nodes {
		m := ms[n.Name]
		m.Enabled = n.Enabled
		ms[n.Name] = m
	}
	return Status{
		Settings: settings, Nodes: enabledCount, DailyBytes: daily,
		Running: s.running, LastError: s.lastErr, UpdatedAt: s.updated,
		Measurements: ms, PendingBySubscription: pendingBy,
	}, nil
}

func (s *Service) loadSettings(ctx context.Context) (Settings, error) {
	if s.Profile == nil {
		return DefaultSettings(), nil
	}
	prof, err := s.Profile(ctx)
	if err != nil {
		return Settings{}, err
	}
	return NormalizeSettings(prof["bandwidth"])
}

func (s *Service) listNodes(ctx context.Context) ([]NodeRef, error) {
	if s.Nodes == nil {
		return nil, nil
	}
	return s.Nodes(ctx)
}

// QueueSubscription 把某订阅下节点排进队。
func (s *Service) QueueSubscription(ctx context.Context, subID string) (QueueResult, error) {
	nodes, err := s.listNodes(ctx)
	if err != nil {
		return QueueResult{}, err
	}
	ts := s.now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[string]queueItem{}
	}
	queued, joined := 0, 0
	for _, n := range nodes {
		if n.SubscriptionID != subID {
			continue
		}
		if _, ok := s.pending[n.Name]; ok {
			joined++
			continue
		}
		s.pending[n.Name] = queueItem{SubID: subID, RequestedAt: ts, Manual: true}
		queued++
	}
	s.updated = ts
	return QueueResult{
		Queued: queued, Joined: joined, RequestedAt: ts,
		PendingBySubscription: s.pendingByLocked(),
	}, nil
}

// QueueNode 把单个节点排进队。
func (s *Service) QueueNode(ctx context.Context, name string) (QueueResult, error) {
	nodes, err := s.listNodes(ctx)
	if err != nil {
		return QueueResult{}, err
	}
	var ref *NodeRef
	for i := range nodes {
		if nodes[i].Name == name {
			ref = &nodes[i]
			break
		}
	}
	if ref == nil {
		// 允许排队未知名:仍记一条,避免前端空转;sub 用空串
		ref = &NodeRef{Name: name}
	}
	ts := s.now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[string]queueItem{}
	}
	joined := 0
	queued := 0
	if _, ok := s.pending[name]; ok {
		joined = 1
	} else {
		s.pending[name] = queueItem{SubID: ref.SubscriptionID, RequestedAt: ts, Manual: true}
		queued = 1
	}
	s.updated = ts
	return QueueResult{
		Queued: queued, Joined: joined, RequestedAt: ts,
		PendingBySubscription: s.pendingByLocked(),
	}, nil
}

func (s *Service) pendingByLocked() map[string]int {
	out := map[string]int{}
	for _, q := range s.pending {
		out[q.SubID]++
	}
	return out
}

// Samples 返回一个节点的测速历史(从旧到新,含失败的),给「节点质量」按需读;没测过回 nil。
func (s *Service) Samples(name string) []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.measures[name]
	if !ok || len(m.History) == 0 {
		return nil
	}
	return append([]Sample(nil), m.History...)
}
