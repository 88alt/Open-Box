package nodequality

import (
	"math"
	"sort"
)

// 评分模型,口径对齐上游 v0.1.423 的服务端(panel/server/index.mjs 里 node-quality 的评分与汇总):
//   - 单节点只看三项:延时 40%、下载带宽 30%、HTTP 探测波动 30%;可用率不进分数(只展示),上传没有测速来源恒为 null;
//   - 每一项按分段线性表折成 0~100 分;没测到的项不计、其余项按权重归一;
//   - 只有「当前状态是 healthy 且有延时」才给分;分数为 四舍五入(Σ 分项 × 权重 / Σ 权重);
//   - 暂估(provisional):缺下载、缺波动、延时样本不足 10 个、波动样本不足 10 个、下载样本不足 10 个,任一成立;
//   - 订阅汇总:已评估 = 状态 down 的 + 有分数的;覆盖率 = 已评估 / 总数;分数 = 健康节点分数之和 / 已评估数
//     (down 的节点按 0 分计入);覆盖率不足 0.8 是 pending,有 down 是 attention,否则 85 / 75 / 60 分档。

const (
	wLatency  = 40.0
	wDownload = 30.0
	wJitter   = 30.0

	// 探测结果的「新鲜」时限:最近一次检测比现在早这么久(或在将来)就是 stale。
	// 上游按节点从带宽管理器取这个时限、取不到用 10 分钟;这里没有按节点的来源,统一 10 分钟。
	defaultFreshMs = int64(10 * 60 * 1000)
	// 评分要的样本个数(延时、波动、下载各 10 个才算完整评分)。
	fullSamples = 10
	// 订阅覆盖率低于这个值,整体是 pending。
	minCoverage = 0.8
	// 默认测速周期(档案 bandwidth.intervalHours 取不到时)。
	defaultPeriodMs = int64(12 * hourMs)
)

type knot struct{ x, y float64 }

var (
	latencyKnots  = []knot{{0, 100}, {50, 100}, {100, 90}, {200, 75}, {400, 50}, {1000, 0}}
	jitterKnots   = []knot{{0, 100}, {5, 100}, {10, 90}, {20, 75}, {40, 50}, {150, 0}}
	downloadKnots = []knot{{0, 0}, {3, 0}, {10, 30}, {30, 55}, {50, 70}, {100, 85}, {300, 100}}
)

// interp 在分段线性表上取值:小于等于第一个点取第一个点的分,超过最后一个点取最后一个点的分,中间线性插值后四舍五入。
func interp(v float64, k []knot) float64 {
	if v <= k[0].x {
		return k[0].y
	}
	for i := 1; i < len(k); i++ {
		if v <= k[i].x {
			a, b := k[i-1], k[i]
			return math.Floor(a.y + (b.y-a.y)*(v-a.x)/(b.x-a.x) + 0.5)
		}
	}
	return k[len(k)-1].y
}

// median 取中位数;空返回 nil。不改传入的切片。
func median(in []float64) *float64 {
	if len(in) == 0 {
		return nil
	}
	v := append([]float64(nil), in...)
	sort.Float64s(v)
	m := len(v) / 2
	r := v[m]
	if len(v)%2 == 0 {
		r = (v[m-1] + v[m]) / 2
	}
	return &r
}

// meanOf 取平均;空返回 nil。
func meanOf(in []float64) *float64 {
	if len(in) == 0 {
		return nil
	}
	var s float64
	for _, x := range in {
		s += x
	}
	r := s / float64(len(in))
	return &r
}

// download 是一个节点最近的下载测速汇总(最近最多 10 次成功测速的平均)。
type download struct {
	At         int64
	Mbps       float64
	Count      int
	LowerBound bool
}

// evaluated 是一个节点按上游口径算出的结果。
type evaluated struct {
	Availability  *float64
	Latency       *float64
	Jitter        *float64
	Download      *float64
	State         string
	Score         *int
	Provisional   bool
	Checks        int
	DownloadAt    *int64
	LowerBound    bool
	DownloadStale bool
}

// evaluate 把一个节点 24 小时的统计和下载汇总折成状态与分数。nowMs 是现在,periodMs 是测速周期,freshMs 是新鲜时限。
func evaluate(sm summary, dl *download, nowMs, periodMs, freshMs int64) evaluated {
	var ev evaluated
	ev.Checks = sm.Success + sm.Failure
	if ev.Checks > 0 {
		a := 100 * float64(sm.Success) / float64(ev.Checks)
		ev.Availability = &a
	}
	ev.Latency, ev.Jitter = sm.Latency, sm.Jitter

	switch {
	case sm.LastAt == 0:
		ev.State = "unknown"
	case nowMs-sm.LastAt < 0 || nowMs-sm.LastAt > freshMs:
		ev.State = "stale"
	case sm.LastOK:
		ev.State = "healthy"
	default:
		ev.State = "down"
	}

	var age int64
	if dl != nil {
		age = nowMs - dl.At
		ev.DownloadAt = &dl.At
		ev.LowerBound = dl.LowerBound
		// 下载样本超过两个周期不再算「当前」
		if dl.Mbps > 0 && age >= 0 && age < 2*periodMs {
			m := dl.Mbps
			ev.Download = &m
		}
		ev.DownloadStale = ev.Download != nil && age > periodMs
	}

	if ev.State == "healthy" && ev.Latency != nil {
		var sw, sv float64
		add := func(v *float64, w float64, k []knot) {
			if v != nil {
				sw += w
				sv += w * interp(*v, k)
			}
		}
		add(ev.Latency, wLatency, latencyKnots)
		add(ev.Download, wDownload, downloadKnots)
		add(ev.Jitter, wJitter, jitterKnots)
		sc := int(math.Floor(sv/sw + 0.5))
		ev.Score = &sc
		ev.Provisional = ev.Download == nil || ev.Jitter == nil ||
			sm.LatencyCount < fullSamples || sm.JitterCount < fullSamples ||
			dl == nil || dl.Count < fullSamples
	}
	return ev
}

// aggregate 是订阅汇总(只算数,不含 id / 名字 / 使用时长)。
type aggregate struct {
	Total, Usable, Failed, Unknown, Assessed int
	Coverage                                 float64
	Score                                    *int
	Grade                                    string
	Checks                                   int
	Provisional                              bool
	Availability, Latency, Jitter            *float64
	Download, Upload                         *float64
	BandwidthCount                           int
	Up, Down                                 int64
}

type nodeRow struct {
	ev       evaluated
	up, down int64
}

func aggregateNodes(rows []nodeRow) aggregate {
	var ag aggregate
	ag.Total = len(rows)
	var scoreSum int
	var avail, lats, jits, downs []float64
	for _, r := range rows {
		ag.Checks += r.ev.Checks
		ag.Up += r.up
		ag.Down += r.down
		if r.ev.Availability != nil {
			avail = append(avail, *r.ev.Availability)
		}
		isDown := r.ev.State == "down"
		hasScore := r.ev.Score != nil
		if isDown || hasScore {
			ag.Assessed++
		}
		if isDown {
			ag.Failed++
		}
		if !isDown && !hasScore {
			ag.Unknown++
		}
		if r.ev.State != "healthy" {
			continue
		}
		ag.Usable++
		if hasScore {
			scoreSum += *r.ev.Score
			if r.ev.Provisional {
				ag.Provisional = true
			}
		}
		if r.ev.Latency != nil {
			lats = append(lats, *r.ev.Latency)
		}
		if r.ev.Jitter != nil {
			jits = append(jits, *r.ev.Jitter)
		}
		if r.ev.Download != nil {
			downs = append(downs, *r.ev.Download)
			ag.BandwidthCount++
		}
	}
	if ag.Total > 0 {
		ag.Coverage = float64(ag.Assessed) / float64(ag.Total)
	}
	if ag.Assessed > 0 {
		sc := int(math.Floor(float64(scoreSum)/float64(ag.Assessed) + 0.5))
		ag.Score = &sc
	}
	switch {
	case ag.Coverage < minCoverage:
		ag.Grade = "pending"
	case ag.Failed > 0:
		ag.Grade = "attention"
	case ag.Score != nil && *ag.Score >= 85:
		ag.Grade = "excellent"
	case ag.Score != nil && *ag.Score >= 75:
		ag.Grade = "good"
	case ag.Score != nil && *ag.Score >= 60:
		ag.Grade = "fair"
	default:
		ag.Grade = "attention"
	}
	ag.Availability = meanOf(avail)
	ag.Latency, ag.Jitter, ag.Download = median(lats), median(jits), median(downs)
	return ag
}
