package bandwidth

import (
	"context"
	"io"
	"net/http"
	"time"
)

// MeasureResult 一次测速结果。
type MeasureResult struct {
	Mbps       float64
	Failed     bool
	LowerBound bool
	Error      string
	Reason     string
}

// Measurer 对某个节点名做带宽探测(真实环境应走该节点出站;测试可注入假实现)。
type Measurer func(ctx context.Context, node string, maxBytes int64) MeasureResult

// DefaultHTTPMeasurer 直连下载探测(不经节点)。生产应换成经内核出站的实现。
func DefaultHTTPMeasurer(url string) Measurer {
	if url == "" {
		url = "https://speed.cloudflare.com/__down?bytes=25000000"
	}
	client := &http.Client{Timeout: 60 * time.Second}
	return func(ctx context.Context, node string, maxBytes int64) MeasureResult {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return MeasureResult{Failed: true, Error: err.Error(), Reason: "invalid"}
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return MeasureResult{Failed: true, Error: err.Error(), Reason: "timeout"}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return MeasureResult{Failed: true, Error: resp.Status, Reason: "http"}
		}
		n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxBytes+1))
		elapsed := time.Since(start).Seconds()
		if elapsed <= 0 {
			elapsed = 0.001
		}
		lower := n > maxBytes
		if lower {
			n = maxBytes
		}
		if err != nil && err != io.EOF {
			return MeasureResult{Failed: true, Error: err.Error(), Reason: "network"}
		}
		mbps := (float64(n) * 8) / elapsed / 1e6
		return MeasureResult{Mbps: mbps, LowerBound: lower}
	}
}

// RunOnce 处理队列里一个节点;没有排队则 noop。
func (s *Service) RunOnce(ctx context.Context) {
	s.mu.Lock()
	if s.running || len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}
	var name string
	var item queueItem
	for n, q := range s.pending {
		name, item = n, q
		break
	}
	delete(s.pending, name)
	s.running = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.updated = s.now().UnixMilli()
		s.mu.Unlock()
	}()

	settings, err := s.loadSettings(ctx)
	maxBytes := int64(settings.MaxDownloadMB) * 1024 * 1024
	if maxBytes <= 0 {
		maxBytes = 50 * 1024 * 1024
	}
	measurer := s.Measure
	if measurer == nil {
		// 没有注入测速器时标记失败,避免默默直连打外网
		s.record(name, item, MeasureResult{Failed: true, Error: "measurer not configured", Reason: "unavailable"})
		return
	}
	if err != nil {
		s.record(name, item, MeasureResult{Failed: true, Error: err.Error(), Reason: "settings"})
		return
	}
	res := measurer(ctx, name, maxBytes)
	s.record(name, item, res)
}

func (s *Service) record(name string, item queueItem, res MeasureResult) {
	ts := s.now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.measures == nil {
		s.measures = map[string]Measurement{}
	}
	m := s.measures[name]
	sample := Sample{At: ts, Mbps: res.Mbps, Failed: res.Failed, LowerBound: res.LowerBound}
	m.Sample = sample
	m.Pending = false
	m.RequestedAt = item.RequestedAt
	m.CompletedAt = ts
	m.Error = res.Error
	m.Reason = res.Reason
	m.History = append(m.History, sample)
	// 保留最近 48 条;长期保留策略后续跟分析数据时长
	if len(m.History) > 48 {
		m.History = m.History[len(m.History)-48:]
	}
	s.measures[name] = m
	if res.Failed {
		s.lastErr = res.Error
	} else {
		s.lastErr = ""
	}
	s.updated = ts
	s.pruneHistoryLocked(context.Background())
	s.saveHistoryLocked()
}
