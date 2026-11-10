package nodequality

import "math"

// 评分模型。权重、门槛取自前端 zh.ts 的 qualityModelHelp:
// 单节点:可用率 35%、延时 20%、HTTP 探测波动 15%、下载带宽 20%、上传带宽 10%;
// 至少观察 1 小时、20 次检测、10 次成功并有近期下载样本才给暂估分;
// 上传缺测时按其余已测权重归一,不标完整评分(订阅的 provisional)。
// 下面各项折成 0~100 的线性刻度、等级分档、过期时长是自己定的(说明文字没写),待和上游真实响应对表。
const (
	wAvail  = 0.35
	wLat    = 0.20
	wJitter = 0.15
	wDown   = 0.20
	wUp     = 0.10

	latGoodMs, latBadMs   = 100.0, 1000.0 // ≤100ms 满分,≥1000ms 零分
	jitGoodMs, jitBadMs   = 10.0, 200.0
	downFullMbps          = 100.0 // ≥100Mbps 满分
	upFullMbps            = 50.0  // ≥50Mbps 满分
	minObserveMs          = int64(3600_000)
	minChecks, minSuccess = 20, 10

	// 最近一次检测早于这个时长就算「已过期」(stale),不计入已评估。
	staleAfterMs = int64(6 * 3600_000)
	// 覆盖率不足这个值,订阅整体显示「待补数据」(前端也以 0.8 为界)。
	minCoverage = 0.8
)

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func linearDown(v, good, bad float64) float64 { // 越小越好
	return 100 * clamp01((bad-v)/(bad-good))
}

// nodeScore 返回单节点分数(0~100,四舍五入)。不满足门槛返回 nil。
// provisional 为真表示上传缺测(现在后端没有上传测速,恒为真)。
func nodeScore(st stats, download, upload *float64, observedMs int64) (score *int, provisional bool) {
	if observedMs < minObserveMs || st.checks < minChecks || st.ok < minSuccess || download == nil {
		return nil, false
	}
	type part struct{ w, v float64 }
	parts := []part{
		{wAvail, 100 * float64(st.ok) / float64(st.checks)},
		{wLat, linearDown(float64(st.latSum)/float64(st.ok), latGoodMs, latBadMs)},
		{wDown, 100 * clamp01(*download/downFullMbps)},
	}
	if st.jitN > 0 {
		parts = append(parts, part{wJitter, linearDown(float64(st.jitSum)/float64(st.jitN), jitGoodMs, jitBadMs)})
	}
	if upload != nil {
		parts = append(parts, part{wUp, 100 * clamp01(*upload/upFullMbps)})
	}
	var sw, sv float64
	for _, p := range parts {
		sw += p.w
		sv += p.w * p.v
	}
	n := int(math.Round(sv / sw))
	return &n, upload == nil
}

// grade 按订阅整体分档;覆盖率不足或没有分数是 pending。
func grade(score *int, coverage float64) string {
	if score == nil || coverage < minCoverage {
		return "pending"
	}
	switch {
	case *score >= 90:
		return "excellent"
	case *score >= 75:
		return "good"
	case *score >= 60:
		return "fair"
	}
	return "attention"
}
