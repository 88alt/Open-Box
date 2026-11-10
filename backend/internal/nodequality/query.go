package nodequality

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
)

// Query 对应前端 OpenboxNodeQualityQuery。
type Query struct {
	AllSubscriptions bool   // 目前不改变结果:总是返回全部启用的订阅
	Search           string // 订阅名或节点 tag / 地区 / 协议含这段文字(不分大小写)
	Details          string // 要展开节点列表的订阅 id
	NodePageSize     int    // 展开时先给几个节点,默认 10,最多 500
	AllNodes         bool   // 展开时给全部节点
	Node             string // 要带图表数据的节点 tag(须在展开的列表里)
}

// Sample 对应 OpenboxQualitySample。
type Sample struct {
	At     int64   `json:"at"`
	Value  float64 `json:"value"`
	Failed bool    `json:"failed,omitempty"`
}

// Charts 对应节点的 charts。
type Charts struct {
	Latency   []Sample `json:"latency"`
	Bandwidth []Sample `json:"bandwidth"`
}

// Node 对应 OpenboxQualityNode。
type Node struct {
	Tag          string   `json:"tag"`
	Region       string   `json:"region"`
	Protocol     string   `json:"protocol"`
	State        string   `json:"state"`
	Score        *int     `json:"score"`
	Availability *float64 `json:"availability"`
	Latency      *float64 `json:"latency"`
	Jitter       *float64 `json:"jitter"`
	Download     *float64 `json:"download"`
	Upload       *float64 `json:"upload"`
	ActiveMs     int64    `json:"activeMs"`
	Up           int64    `json:"up"`
	Down         int64    `json:"down"`
	LastAt       *int64   `json:"lastAt"`
	Checks       int      `json:"checks"`
	Charts       *Charts  `json:"charts,omitempty"`
}

// SubscriptionQuality 对应 OpenboxQualitySubscription。
type SubscriptionQuality struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Mode           string   `json:"mode"`
	Usable         int      `json:"usable"`
	Total          int      `json:"total"`
	Failed         int      `json:"failed"`
	Unknown        int      `json:"unknown"`
	Assessed       int      `json:"assessed"`
	Coverage       float64  `json:"coverage"`
	Score          *int     `json:"score"`
	Grade          string   `json:"grade"`
	Provisional    bool     `json:"provisional,omitempty"`
	BandwidthCount int      `json:"bandwidthCount"`
	Availability   *float64 `json:"availability"`
	Latency        *float64 `json:"latency"`
	Jitter         *float64 `json:"jitter"`
	Download       *float64 `json:"download"`
	Upload         *float64 `json:"upload"`
	ActiveMs       int64    `json:"activeMs"`
	Up             int64    `json:"up"`
	Down           int64    `json:"down"`
	LastAt         *int64   `json:"lastAt"`
	Checks         int      `json:"checks"`
	Nodes          []Node   `json:"nodes,omitempty"`
}

// Result 对应 OpenboxNodeQuality。
type Result struct {
	Subscriptions []SubscriptionQuality `json:"subscriptions"`
}

type stats struct {
	checks, ok int
	latSum     int64
	jitSum     int64
	jitN       int
	up, down   int64
	active     int64
}

func (e *entry) window(now time.Time) stats {
	var st stats
	if e == nil {
		return st
	}
	cut := now.Unix() - windowHours*3600
	for _, b := range e.Buckets {
		if b.H+3600 <= cut {
			continue
		}
		st.checks += b.Checks
		st.ok += b.OK
		st.latSum += b.LatSum
		st.jitSum += b.JitSum
		st.jitN += b.JitN
		st.up += b.Up
		st.down += b.Down
		st.active += b.Active
	}
	return st
}

func r1(v float64) *float64 {
	x := math.Round(v*10) / 10
	return &x
}

func median(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	sort.Float64s(v)
	m := v[len(v)/2]
	if len(v)%2 == 0 {
		m = (v[len(v)/2-1] + v[len(v)/2]) / 2
	}
	return r1(m)
}

func mean(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return r1(s / float64(len(v)))
}

// latestBandwidth 取窗口内最近一次成功的下载测速。
func latestBandwidth(e *entry, now time.Time, win time.Duration) *float64 {
	if e == nil {
		return nil
	}
	cut := now.Add(-win).UnixMilli()
	for i := len(e.BW) - 1; i >= 0; i-- {
		p := e.BW[i]
		if p.At >= cut && !p.Failed {
			return r1(p.V)
		}
	}
	return nil
}

func samples(in []point) []Sample {
	out := make([]Sample, 0, len(in))
	for _, p := range in {
		out = append(out, Sample{At: p.At, Value: p.V, Failed: p.Failed})
	}
	return out
}

func stateOf(e *entry, st stats, nowMs int64) string {
	switch {
	case e == nil || st.checks == 0 || e.Last == 0:
		return "unknown"
	case nowMs-e.Last > staleAfterMs:
		return "stale"
	case !e.LastOK:
		return "down"
	}
	return "healthy"
}

func nodeSortKey(n Node) (int, float64, float64) {
	rank := map[string]int{"healthy": 0, "down": 1, "stale": 2, "unknown": 3}[n.State]
	score, lat := 0.0, 1e12
	if n.Score != nil {
		score = float64(*n.Score)
	}
	if n.Latency != nil {
		lat = *n.Latency
	}
	return rank, -score, lat
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
	bwWin := 12 * time.Hour
	if s.BandwidthWindow != nil {
		if w := s.BandwidthWindow(ctx); w > 0 {
			bwWin = w
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	now := s.now()
	nowMs := now.UnixMilli()
	search := strings.ToLower(strings.TrimSpace(q.Search))
	size := q.NodePageSize
	if size <= 0 {
		size = 10
	}
	if size > 500 {
		size = 500
	}
	out := Result{Subscriptions: []SubscriptionQuality{}}
	for _, sub := range subs {
		nodes := make([]Node, 0, len(sub.Nodes))
		var lats, jits, downs, ups []float64
		var checks, oks int
		var lastAt int64
		sq := SubscriptionQuality{ID: sub.ID, Name: sub.Name, Mode: sub.Mode, Total: len(sub.Nodes)}
		scoreSum, assessed, scored := 0, 0, 0
		provisional := false
		for _, ni := range sub.Nodes {
			e := s.data.Nodes[ni.Tag]
			st := e.window(now)
			n := Node{Tag: ni.Tag, Region: ni.Region, Protocol: ni.Protocol, ActiveMs: st.active, Up: st.up, Down: st.down, Checks: st.checks}
			n.State = stateOf(e, st, nowMs)
			if e != nil && e.Last > 0 && st.checks > 0 {
				la := e.Last
				n.LastAt = &la
				if la > lastAt {
					lastAt = la
				}
			}
			if st.checks > 0 {
				n.Availability = r1(100 * float64(st.ok) / float64(st.checks))
				checks += st.checks
				oks += st.ok
			}
			if st.ok > 0 {
				l := float64(st.latSum) / float64(st.ok)
				n.Latency = r1(l)
				lats = append(lats, l)
			}
			if st.jitN > 0 {
				j := float64(st.jitSum) / float64(st.jitN)
				n.Jitter = r1(j)
				jits = append(jits, j)
			}
			n.Download = latestBandwidth(e, now, bwWin)
			if n.Download != nil {
				downs = append(downs, *n.Download)
				sq.BandwidthCount++
			}
			// 上传:后端没有上传测速,恒为 null(前端显示「上传未检测」),不拿业务上传流量冒充
			if n.State == "healthy" {
				sq.Usable++
				var observed int64
				if e != nil && e.First > 0 {
					observed = e.Last - e.First
				}
				if sc, prov := nodeScore(st, n.Download, n.Upload, observed); sc != nil {
					n.Score = sc
					assessed++
					scored++
					scoreSum += *sc
					provisional = provisional || prov
				}
			}
			switch n.State {
			case "down":
				sq.Failed++
				assessed++ // 确认失败的节点按 0 分计入
			case "unknown", "stale":
				sq.Unknown++
			}
			nodes = append(nodes, n)
		}
		sq.Assessed = assessed
		if sq.Total > 0 {
			sq.Coverage = math.Round(float64(assessed)/float64(sq.Total)*1000) / 1000
		}
		if assessed > 0 {
			sc := int(math.Round(float64(scoreSum) / float64(assessed)))
			sq.Score = &sc
		}
		sq.Provisional = provisional && sq.Score != nil
		sq.Grade = grade(sq.Score, sq.Coverage)
		sq.Checks = checks
		if checks > 0 {
			sq.Availability = r1(100 * float64(oks) / float64(checks))
		}
		sq.Latency, sq.Jitter = mean(lats), mean(jits)
		sq.Download, sq.Upload = median(downs), median(ups)
		if lastAt > 0 {
			sq.LastAt = &lastAt
		}
		ss := s.data.Subs[sub.ID].window(now)
		sq.ActiveMs, sq.Up, sq.Down = ss.active, ss.up, ss.down

		subMatch := search == "" || strings.Contains(strings.ToLower(sub.Name), search)
		var matched []Node
		for _, n := range nodes {
			if search == "" || strings.Contains(strings.ToLower(n.Tag+" "+n.Region+" "+n.Protocol), search) {
				matched = append(matched, n)
			}
		}
		if !subMatch && len(matched) == 0 {
			continue
		}
		if sub.ID == q.Details {
			list := nodes
			if !subMatch {
				list = matched
			}
			list = append([]Node(nil), list...)
			sort.SliceStable(list, func(i, j int) bool {
				ri, si, li := nodeSortKey(list[i])
				rj, sj, lj := nodeSortKey(list[j])
				if ri != rj {
					return ri < rj
				}
				if si != sj {
					return si < sj
				}
				return li < lj
			})
			if !q.AllNodes && len(list) > size {
				list = list[:size]
			}
			if q.Node != "" {
				for i := range list {
					if list[i].Tag == q.Node {
						e := s.data.Nodes[q.Node]
						c := &Charts{Latency: []Sample{}, Bandwidth: []Sample{}}
						if e != nil {
							c.Latency, c.Bandwidth = samples(e.Lat), samples(e.BW)
						}
						list[i].Charts = c
					}
				}
			}
			sq.Nodes = list
		}
		out.Subscriptions = append(out.Subscriptions, sq)
	}
	return out, nil
}
