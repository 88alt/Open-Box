package bandwidth

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/probe"
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

// pickNextLocked 挑下一个该测的节点:手动优先,同类里先到先测。调用方必须已持有 s.mu。
// 返回的 name 为空表示队列空了。
//
// 为什么手动优先:上游 v0.1.493 #1010 明确「支持手动测速优先插队」——
// 用户点了某台节点的重测,期望它马上出结果,而不是排在几十个后台巡检项后面。
func (s *Service) pickNextLocked() (string, queueItem) {
	var name string
	var item queueItem
	found := false
	for n, q := range s.pending {
		if !found {
			name, item, found = n, q, true
			continue
		}
		// 手动项排在所有后台项前面
		if q.Manual != item.Manual {
			if q.Manual {
				name, item = n, q
			}
			continue
		}
		// 同级按请求时间先到先测
		if q.RequestedAt < item.RequestedAt {
			name, item = n, q
		}
	}
	return name, item
}

// RunOnce 处理队列里一个节点;没有排队则 noop。
func (s *Service) RunOnce(ctx context.Context) {
	s.mu.Lock()
	if s.running || len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}
	// 上游 v0.1.493 #1010:手动测速优先插队。
	// 以前这里是直接 range 取第一个,Go 的 map 遍历顺序随机,手动点的那台可能排在几十个
	// 后台巡检项后面。现在先挑 Manual(同类里取最早请求的),没有手动项才取后台项。
	name, item := s.pickNextLocked()
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

	// 上游 v0.1.493 #1009:Cloudflare 那类测速站在 403 / 429 时会拒绝大盘下载,
	// 但小体积往往能过。这里按 1/4、再按 1/16 各降一档重试,能拿到数就按「下限」记 ——
	// 宁可给一个偏保守的真实值,也比直接判失败有用。
	if res.Failed && isRateLimitReason(res.Reason) {
		for _, div := range []int64{4, 16} {
			smaller := maxBytes / div
			if smaller < minDowngradeBytes {
				break
			}
			if ctx.Err() != nil {
				break
			}
			if retry := measurer(ctx, name, smaller); !retry.Failed {
				// 降档测出来的数只保证「至少这么快」,标成 LowerBound 以免被当成精确值
				retry.LowerBound = true
				res = retry
				break
			}
		}
	}
	s.record(name, item, res)
}

// isRateLimitReason 判断这次失败是不是「站点拒绝 / 限流」这一类(值得降档重试)。
func isRateLimitReason(reason string) bool {
	return reason == probe.ReasonForbidden || reason == probe.ReasonRateLimited
}

// minDowngradeBytes 降档重试的最小体积:再小测出来的数已经没参考意义。
const minDowngradeBytes = 2 << 20 // 2 MiB

func (s *Service) record(name string, item queueItem, res MeasureResult) {
	ts := s.now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	// 上游 v0.1.493 #1010:测到一半时这个节点又被重新排了(用户手动点重测,或后台又排了一次),
	// 这次的结果就已经是旧的 —— 丢弃,别把新请求的 pending 清掉、也别覆盖它的时间戳,
	// 否则界面上会出现「刚点重测,结果却立刻变成上一次的数」。
	if q, again := s.pending[name]; again && q.RequestedAt > item.RequestedAt {
		return
	}
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
