package bandwidth

import (
	"context"
	"time"
)

type histFileData struct {
	UpdatedAt    int64                  `json:"updatedAt"`
	Measurements map[string]Measurement `json:"measurements"`
}

// LoadHistory 从数据目录读回上次的测量(进程启动时调用一次)。
func (s *Service) LoadHistory() {
	if s.Store == nil {
		return
	}
	var f histFileData
	ok, err := s.Store.Load(histFile, &f)
	if err != nil || !ok || f.Measurements == nil {
		return
	}
	s.mu.Lock()
	s.measures = f.Measurements
	s.updated = f.UpdatedAt
	s.mu.Unlock()
}

func (s *Service) saveHistoryLocked() {
	if s.Store == nil {
		return
	}
	f := histFileData{UpdatedAt: s.updated, Measurements: s.measures}
	_ = s.Store.Save(histFile, f)
}

// KeepWindow 历史样本最长保留多久。优先档案 traffic.keepMonths(按约 30 天/月),最少 24 小时。
func (s *Service) KeepWindow(ctx context.Context) time.Duration {
	d := 24 * time.Hour
	if s.Profile == nil {
		return d
	}
	prof, err := s.Profile(ctx)
	if err != nil || prof == nil {
		return d
	}
	tr, _ := prof["traffic"].(map[string]any)
	if tr == nil {
		return d
	}
	months, _ := asInt(tr["keepMonths"])
	if months < 1 {
		months = 1
	}
	if months > 24 {
		months = 24
	}
	return time.Duration(months) * 30 * 24 * time.Hour
}

func (s *Service) pruneHistoryLocked(ctx context.Context) {
	win := s.KeepWindow(ctx)
	cut := s.now().Add(-win).UnixMilli()
	for name, m := range s.measures {
		h := m.History[:0]
		for _, sample := range m.History {
			if sample.At >= cut {
				h = append(h, sample)
			}
		}
		m.History = h
		s.measures[name] = m
	}
}

// EnqueueDue 把「启用定期检查」且距上次完成已超过 intervalHours 的节点排进队。
func (s *Service) EnqueueDue(ctx context.Context) (int, error) {
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return 0, err
	}
	nodes, err := s.listNodes(ctx)
	if err != nil {
		return 0, err
	}
	interval := time.Duration(settings.IntervalHours) * time.Hour
	if interval < time.Hour {
		interval = time.Hour
	}
	now := s.now()
	ts := now.UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[string]queueItem{}
	}
	if s.measures == nil {
		s.measures = map[string]Measurement{}
	}
	n := 0
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}
		if _, ok := s.pending[node.Name]; ok {
			continue
		}
		m := s.measures[node.Name]
		if m.CompletedAt > 0 && now.Sub(time.UnixMilli(m.CompletedAt)) < interval {
			continue
		}
		s.pending[node.Name] = queueItem{SubID: node.SubscriptionID, RequestedAt: ts, Manual: false}
		n++
	}
	if n > 0 {
		s.updated = ts
	}
	return n, nil
}

// Loop 后台循环:按周期入队 + 消化队列。ctx 取消时退出。
func (s *Service) Loop(ctx context.Context) {
	s.LoadHistory()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		_, _ = s.EnqueueDue(ctx)
		s.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
