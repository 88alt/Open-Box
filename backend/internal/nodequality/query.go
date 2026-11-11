package nodequality

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// Query 对应前端 OpenboxNodeQualityQuery,外加上游多的分页参数 page / nodePage。
type Query struct {
	Page             int    // 订阅页码,默认 1
	PageSize         int    // 每页订阅数,默认 20,1~100
	AllSubscriptions bool   // 一页给全部订阅
	Search           string // 订阅名含这段文字(不分大小写);只筛订阅,不筛节点
	Details          string // 要展开节点列表的订阅 id
	NodePage         int    // 展开时的节点页码,默认 1
	NodePageSize     int    // 展开时每页节点数,默认 20,1~100
	AllNodes         bool   // 展开时一页给全部节点
	Node             string // 要带图表数据的节点 tag(须在展开的这一页里,且 Details 要有)
}

// Sample 对应 OpenboxQualitySample。
type Sample struct {
	At         int64   `json:"at"`
	Value      float64 `json:"value"`
	Failed     bool    `json:"failed,omitempty"`
	LowerBound bool    `json:"lowerBound,omitempty"`
}

// Charts 对应节点的 charts:Bandwidth 只在 BandwidthEnabled 为真时出现。
type Charts struct {
	Latency          []Sample `json:"latency"`
	BandwidthEnabled bool     `json:"bandwidthEnabled"`
	Bandwidth        []Sample `json:"bandwidth,omitempty"`
}

// Node 对应 OpenboxQualityNode(字段与上游一致)。lastAt / firstAt 没有检测时是 0。
type Node struct {
	Availability  *float64 `json:"availability"`
	Latency       *float64 `json:"latency"`
	Jitter        *float64 `json:"jitter"`
	Download      *float64 `json:"download"`
	Upload        *float64 `json:"upload"`
	State         string   `json:"state"`
	Score         *int     `json:"score"`
	Provisional   bool     `json:"provisional"`
	Checks        int      `json:"checks"`
	FirstAt       int64    `json:"firstAt"`
	LastAt        int64    `json:"lastAt"`
	DownloadAt    *int64   `json:"downloadAt"`
	LowerBound    bool     `json:"lowerBound"`
	DownloadStale bool     `json:"downloadStale"`
	ActiveMs      int64    `json:"activeMs"`
	Up            int64    `json:"up"`
	Down          int64    `json:"down"`
	Tag           string   `json:"tag"`
	Protocol      string   `json:"protocol"`
	Region        string   `json:"region"`
	Inactive      bool     `json:"inactive"`
	Charts        *Charts  `json:"charts,omitempty"`
}

// SubscriptionQuality 对应 OpenboxQualitySubscription(字段与上游一致)。
type SubscriptionQuality struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Mode           string   `json:"mode"`
	Enabled        bool     `json:"enabled"`
	BandwidthCheck bool     `json:"bandwidthCheck"`
	Total          int      `json:"total"`
	Usable         int      `json:"usable"`
	Failed         int      `json:"failed"`
	Unknown        int      `json:"unknown"`
	Assessed       int      `json:"assessed"`
	Coverage       float64  `json:"coverage"`
	Score          *int     `json:"score"`
	Grade          string   `json:"grade"`
	Checks         int      `json:"checks"`
	Provisional    bool     `json:"provisional"`
	Availability   *float64 `json:"availability"`
	Latency        *float64 `json:"latency"`
	Jitter         *float64 `json:"jitter"`
	Download       *float64 `json:"download"`
	Upload         *float64 `json:"upload"`
	BandwidthCount int      `json:"bandwidthCount"`
	Up             int64    `json:"up"`
	Down           int64    `json:"down"`
	ActiveMs       int64    `json:"activeMs"`
	Nodes          []Node   `json:"nodes,omitempty"`
	NodePage       int      `json:"nodePage,omitempty"`
	NodePageSize   int      `json:"nodePageSize,omitempty"`
}

// Result 对应 OpenboxNodeQuality。
type Result struct {
	At            int64                 `json:"at"`
	WindowHours   int                   `json:"windowHours"`
	PeriodHours   float64               `json:"periodHours"`
	Total         int                   `json:"total"`
	Page          int                   `json:"page"`
	PageSize      int                   `json:"pageSize"`
	Subscriptions []SubscriptionQuality `json:"subscriptions"`
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// pageOf 按上游的口径算页码和切片范围:size 先夹到 1~100(all 时取全部),页码夹到 1~总页数。
func pageOf(total, page, size int, all bool) (p, sz, from, to int) {
	if all {
		sz = total
		if sz < 1 {
			sz = 1
		}
	} else {
		if size <= 0 {
			size = defaultPageSize
		}
		sz = clampInt(size, 1, maxPageSize)
	}
	pages := (total + sz - 1) / sz
	if pages < 1 {
		pages = 1
	}
	if page <= 0 {
		page = 1
	}
	p = clampInt(page, 1, pages)
	from = (p - 1) * sz
	to = p * sz
	if from > total {
		from = total
	}
	if to > total {
		to = total
	}
	return
}

// 按可用率从高到低排(没有可用率排最后),同分保持原来的顺序。
func availKey(a *float64) float64 {
	if a == nil || math.IsNaN(*a) || math.IsInf(*a, 0) {
		return -1
	}
	return *a
}

// downloadOf 取一个节点下载测速的汇总:最近最多 10 次成功(mbps>0)测速的平均,
// 且最近一次要晚于「现在往前两个周期」,否则视为没有。
func downloadOf(list []BWSample, nowMs, periodMs int64) *download {
	var ok []BWSample
	for _, p := range list {
		if !p.Failed && p.Mbps > 0 && p.At <= nowMs {
			ok = append(ok, p)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	sort.SliceStable(ok, func(i, j int) bool { return ok[i].At < ok[j].At })
	if len(ok) > fullSamples {
		ok = ok[len(ok)-fullSamples:]
	}
	d := &download{Count: len(ok)}
	var sum float64
	for _, p := range ok {
		sum += p.Mbps
		if p.At > d.At {
			d.At = p.At
		}
		if p.LowerBound {
			d.LowerBound = true
		}
	}
	d.Mbps = sum / float64(len(ok))
	if d.At <= nowMs-2*periodMs {
		return nil
	}
	return d
}

// latencyChart 把自己攒的探测和延迟历史合并成图表用的序列:同一时刻以自己攒的为准,最近 60 条。
func latencyChart(own []Sample, hist []HistSample, nowMs int64) []Sample {
	byAt := map[int64]Sample{}
	for _, h := range hist {
		if h.At > nowMs || nowMs-h.At >= windowHours*hourMs || h.Delay < 0 {
			continue
		}
		smp := Sample{At: h.At, Value: float64(h.Delay)}
		if h.Delay == 0 {
			smp.Failed = true
		}
		byAt[h.At] = smp
	}
	for _, o := range own {
		byAt[o.At] = o
	}
	out := make([]Sample, 0, len(byAt))
	for _, v := range byAt {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	if len(out) > maxProbes {
		out = out[len(out)-maxProbes:]
	}
	return out
}

// Query 回答一次查询。
func (s *Service) Query(ctx context.Context, q Query) (Result, error) {
	var subs []Subscription
	if s.Catalog != nil {
		var err error
		if subs, err = s.Catalog(ctx); err != nil {
			return Result{}, err
		}
	}
	period := 12 * time.Hour
	if s.Period != nil {
		if p := s.Period(ctx); p > 0 {
			period = p
		}
	}
	periodMs := period.Milliseconds()
	fresh := defaultFreshMs
	if s.FreshFor > 0 {
		fresh = s.FreshFor.Milliseconds()
	}

	// 订阅按名字筛;分页前的总数是筛完的
	search := strings.ToLower(strings.TrimSpace(q.Search))
	var picked []Subscription
	for _, sub := range subs {
		if search == "" || strings.Contains(strings.ToLower(sub.Name), search) {
			picked = append(picked, sub)
		}
	}

	// 下载历史、延迟历史都在拿本服务的锁之前取好(它们各有自己的锁,避免和观察钩子互相等)
	bw := map[string][]BWSample{}
	if s.Bandwidth != nil {
		for _, sub := range picked {
			for _, n := range sub.Nodes {
				if _, ok := bw[n.Tag]; !ok {
					bw[n.Tag] = s.Bandwidth(n.Tag)
				}
			}
		}
	}
	var hist []HistSample
	if q.Details != "" && q.Node != "" && s.History != nil {
		hist = s.History(ctx, q.Node)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	nowMs := s.now().UnixMilli()

	type built struct{ sq SubscriptionQuality }
	all := make([]built, 0, len(picked))
	for _, sub := range picked {
		union := map[int64][]byte{}
		rows := make([]nodeRow, 0, len(sub.Nodes))
		nodes := make([]Node, 0, len(sub.Nodes))
		seenTag := map[string]bool{}
		for _, ni := range sub.Nodes {
			if seenTag[ni.Tag] {
				continue
			}
			seenTag[ni.Tag] = true
			var sm summary
			hours := s.data.Nodes[ni.Tag]
			if hours != nil {
				sm = hours.summarize(nowMs, union)
			}
			ev := evaluate(sm, downloadOf(bw[ni.Tag], nowMs, periodMs), nowMs, periodMs, fresh)
			rows = append(rows, nodeRow{ev: ev, up: sm.Up, down: sm.Down})
			n := Node{
				Availability: ev.Availability, Latency: ev.Latency, Jitter: ev.Jitter, Download: ev.Download,
				State: ev.State, Score: ev.Score, Provisional: ev.Provisional, Checks: ev.Checks,
				FirstAt: sm.FirstAt, LastAt: sm.LastAt, DownloadAt: ev.DownloadAt,
				LowerBound: ev.LowerBound, DownloadStale: ev.DownloadStale,
				ActiveMs: sm.ActiveMs, Up: sm.Up, Down: sm.Down,
				Tag: ni.Tag, Protocol: ni.Protocol, Region: ni.Region, Inactive: !sub.Enabled,
			}
			if q.Details == sub.ID && q.Node == ni.Tag {
				c := &Charts{Latency: []Sample{}}
				if sub.Enabled {
					c.Latency = latencyChart(hours.probeSeries(nowMs), hist, nowMs)
					var series []Sample
					for _, p := range bw[ni.Tag] {
						smp := Sample{At: p.At, Value: p.Mbps, Failed: p.Failed, LowerBound: p.LowerBound}
						if p.Failed {
							smp.Value = 0
						}
						series = append(series, smp)
					}
					sort.SliceStable(series, func(i, j int) bool { return series[i].At < series[j].At })
					c.BandwidthEnabled = sub.BandwidthCheck || len(series) > 0
					if c.BandwidthEnabled {
						c.Bandwidth = series
						if c.Bandwidth == nil {
							c.Bandwidth = []Sample{}
						}
					}
				}
				n.Charts = c
			}
			nodes = append(nodes, n)
		}
		ag := aggregateNodes(rows)
		var active int64
		for _, b := range union {
			active += popcountMs(b)
		}
		sq := SubscriptionQuality{
			ID: sub.ID, Name: sub.Name, Mode: sub.Mode, Enabled: sub.Enabled, BandwidthCheck: sub.BandwidthCheck,
			Total: ag.Total, Usable: ag.Usable, Failed: ag.Failed, Unknown: ag.Unknown, Assessed: ag.Assessed,
			Coverage: ag.Coverage, Score: ag.Score, Grade: ag.Grade, Checks: ag.Checks, Provisional: ag.Provisional,
			Availability: ag.Availability, Latency: ag.Latency, Jitter: ag.Jitter, Download: ag.Download, Upload: ag.Upload,
			BandwidthCount: ag.BandwidthCount, Up: ag.Up, Down: ag.Down, ActiveMs: active,
		}
		if q.Details == sub.ID {
			sort.SliceStable(nodes, func(i, j int) bool { return availKey(nodes[i].Availability) > availKey(nodes[j].Availability) })
			np, nsz, from, to := pageOf(len(nodes), q.NodePage, q.NodePageSize, q.AllNodes)
			sq.Nodes = append([]Node{}, nodes[from:to]...)
			sq.NodePage, sq.NodePageSize = np, nsz
		}
		all = append(all, built{sq: sq})
	}
	sort.SliceStable(all, func(i, j int) bool { return availKey(all[i].sq.Availability) > availKey(all[j].sq.Availability) })
	page, size, from, to := pageOf(len(all), q.Page, q.PageSize, q.AllSubscriptions)
	out := Result{
		At: nowMs, WindowHours: windowHours, PeriodHours: float64(periodMs) / float64(hourMs),
		Total: len(all), Page: page, PageSize: size, Subscriptions: make([]SubscriptionQuality, 0, to-from),
	}
	for _, b := range all[from:to] {
		out.Subscriptions = append(out.Subscriptions, b.sq)
	}
	return out, nil
}
