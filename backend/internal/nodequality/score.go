package nodequality

import (
	"math"
	"sort"

	"github.com/timeflysoon/Open-Box/backend/internal/scoreweights"
)

// 评分模型,口径对齐上游 v0.1.423 的服务端(panel/server/index.mjs 里 node-quality 的评分与汇总):
//   - 单节点只看三项:延时、下载带宽、HTTP 探测波动,权重取档案 nodeScoreWeights(默认 40 / 30 / 30,面板可调);可用率不进分数(只展示),上传没有测速来源恒为 null;
//   - 每一项按分段线性表折成 0~100 分;没测到的项不计、其余项按权重归一;
//   - 只有「当前状态是 healthy 且有延时」才给分;分数为 四舍五入(Σ 分项 × 权重 / Σ 权重);
//   - 暂估(provisional):缺下载、缺波动、延时样本不足 10 个、波动样本不足 10 个、下载样本不足 10 个,任一成立;
//   - 订阅汇总:已评估 = 状态 down 的 + 有分数的;覆盖率 = 已评估 / 总数;分数 = 健康节点分数之和 / 已评估数
//     (down 的节点按 0 分计入);覆盖率不足 0.8 是 pending,有 down 是 attention,否则 85 / 75 / 60 分档。

const (
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

// download 是一个节点最近的下载测速汇总。
// v0.1.491 起:Mbps 是成功样本的 0.65 衰减加权值,Fails 是窗口内的失败次数,
// Failed 表示「已判为带宽失败」(无成功样本 / 累计 5 次失败)。
type download struct {
	At         int64
	Mbps       float64
	Count      int
	LowerBound bool
	Fails      int
	// FailStreak 是窗口内「最新连续失败」的次数(v0.1.492 起:连续 3 次即判带宽失败)
	FailStreak int
	Failed     bool
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
	// DownloadFailed 对上游 v0.1.490 的 downloadFailed:最近一次带宽测速失败(测过但没拿到正数)。
	// 与「从未测过」(dl == nil)分开:两者带宽项都按 0 分计入,但前者标「带宽失败」、后者标「未测带宽」。
	DownloadFailed bool
}

// evaluate 把一个节点 24 小时的统计和下载汇总折成状态与分数。nowMs 是现在,periodMs 是测速周期,freshMs 是新鲜时限。
func evaluate(sm summary, dl *download, nowMs, periodMs, freshMs int64, w scoreweights.Weights) evaluated {
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
		// 测过但没拿到正数 Mbps(超时 / 失败)算「带宽失败」;从没测过(dl == nil)不算。
		ev.DownloadFailed = dl.Mbps <= 0
		// 下载样本超过两个周期不再算「当前」
		if dl.Mbps > 0 && age >= 0 && age < 2*periodMs {
			m := dl.Mbps
			ev.Download = &m
		}
		ev.DownloadStale = ev.Download != nil && age > periodMs
	}

	if ev.State == "healthy" && ev.Latency != nil {
		// v0.1.490 起口径固定:分母恒为总权重(三项之和 = 100),**缺测项按 0 分计入**,
		// 不再像旧版那样跳过缺测项、把其余维度的权重归一化放大。
		// 为什么这样改:旧口径下「只测到延时、没测带宽」的节点会因为分母变小而虚高,
		// 反超真正实测过的节点,选路时把流量引到没验证过的节点上(上游 490 的 release notes
		// 与面板文案都是这个说法,见 i18n 的 nodeScoreWeightsHelpIntro / bandwidthHealthFirst)。
		// 只有一项权重为 0% 时该项才真正「不参与」——权重是 0 就不影响分母,与旧版一致。
		total := w.Latency + w.Download + w.Jitter
		var sv float64
		add := func(v *float64, wt float64, k []knot) {
			if wt <= 0 {
				return // 权重 0%:该项被排除,不计入分母也不计分
			}
			if v != nil {
				sv += wt * interp(*v, k)
			}
			// v 为 nil(缺测 / 测速失败):这一项按 0 分计入,分母仍然包含它
		}
		add(ev.Latency, w.Latency, latencyKnots)
		add(ev.Download, w.Download, downloadKnots)
		add(ev.Jitter, w.Jitter, jitterKnots)
		// v0.1.491:窗口内的带宽测速失败次数按 1.5 倍几何阶梯**扣减下载单项分**
		// (默认 30% 权重下依次扣 3 / 7.5 / 14.25 / 24.375 / 30 分),按带宽权重等比折算,
		// **不倒扣延时与抖动**——所以这里是从 sv 里减掉「下载那一项已有的分」,
		// 而不是直接把总分压低。下载分扣到 0 即视为带宽失败(标记由 downloadOf 给出)。
		if downloadDroppedOut := ev.Download != nil && dl != nil && dl.Fails > 0 && w.Download > 0; downloadDroppedOut {
			// 扣减量由 bwDrop 给出(封闭公式,见 query.go 里的推导:相邻差的公比 1.5)。
			// 权重不是 30% 时按比例折算,保证等比缩放。
			drop := bwDrop(dl.Fails)
			// 折算:扣的是「下载项分数 ×(权重/100)」,再乘 (100/权重) 还原成下载项自己的分,
			// 等价于下载项分数减去 drop*(30/权重) 的分,保证按权重等比缩放。
			scaled := drop * (bwDropBaseWeight / w.Download)
			cur := interp(*ev.Download, downloadKnots)
			sv -= w.Download * (cur - math.Max(cur-scaled, 0))
			if cur <= scaled {
				ev.DownloadFailed = true
			}
		}
		if total > 0 {
			sc := int(math.Floor(sv/total + 0.5))
			if sc < 0 {
				sc = 0
			}
			ev.Score = &sc
		}
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
