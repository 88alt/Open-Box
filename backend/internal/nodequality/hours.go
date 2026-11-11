package nodequality

import (
	"math/bits"
	"sort"
)

// 按小时存的原始统计,结构和口径对齐上游 node_quality_hours 表(只是换成 JSON 文件存):
//   - 每个节点每个整点一条:成功 / 失败次数、第一次和最近一次检测的时刻、最近一次是否成功、
//     这一小时的探测序列(最多 60 条 [时刻, 延时])、上行 / 下行字节、活跃槽位;
//   - 活跃槽位:一小时切成 1800 个 2 秒的槽,业务流量在哪一段时间里跑过就把那几个槽置位,
//     「使用时长」= 置位的槽数 × 2 秒。订阅的使用时长是它名下各节点槽位的并集,并发使用只计一次;
//   - 24 小时窗口:从「当前整点往前 23 个整点」起算,更早的整点在落盘时丢掉。

const (
	hourMs       = int64(3600_000)
	slotMs       = int64(2000)
	slotsPerHour = int(hourMs / slotMs)
	slotBytes    = (slotsPerHour + 7) / 8 // 225
	windowHours  = 24
	maxProbes    = 60 // 每个整点最多留几条探测
	maxKeep      = 10 // 延时 / 波动只看最近 10 个样本
	// 两次探测相隔不到这么久才算「连续」,才计波动。
	jitterGapMs = int64(10 * 60 * 1000)
	// 流量增量按多长的时间铺开成活跃槽位(上限)。
	maxActiveSpanMs = int64(10_000)
	// 检测时刻最多能比现在晚多少(时钟误差)。
	futureSlackMs = int64(60_000)
)

type hourData struct {
	Success int        `json:"s,omitempty"`
	Failure int        `json:"f,omitempty"`
	FirstAt int64      `json:"first,omitempty"`
	LastAt  int64      `json:"last,omitempty"`
	LastOK  bool       `json:"ok,omitempty"`
	Up      int64      `json:"u,omitempty"`
	Down    int64      `json:"d,omitempty"`
	Slots   []byte     `json:"slots,omitempty"` // 活跃槽位位图,base64
	Probes  [][2]int64 `json:"p,omitempty"`     // [时刻毫秒, 延时毫秒];延时 0 = 失败
}

// nodeHours 是一个节点的全部整点:键是 时刻 / 3600000。
type nodeHours map[int64]*hourData

func hourOf(ms int64) int64 { return floorDiv(ms, hourMs) }

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func (n nodeHours) get(h int64) *hourData {
	d := n[h]
	if d == nil {
		d = &hourData{}
		n[h] = d
	}
	return d
}

// recordProbe 记一次探测。时刻不在窗口里(早于 24 小时、晚于现在 1 分钟)、延时为负、
// 或不比这个整点里最近一次更晚(乱序、重复)都不记,返回是否记了。
func (n nodeHours) recordProbe(at int64, delay int, nowMs int64) bool {
	if delay < 0 || at > nowMs+futureSlackMs || nowMs-at > windowHours*hourMs {
		return false
	}
	if old := n[hourOf(at)]; old != nil && at <= old.LastAt {
		return false
	}
	d := n.get(hourOf(at))
	ok := delay > 0
	if ok {
		d.Success++
	} else {
		d.Failure++
	}
	if d.FirstAt == 0 {
		d.FirstAt = at
	}
	d.LastAt, d.LastOK = at, ok
	d.Probes = append(d.Probes, [2]int64{at, int64(delay)})
	if len(d.Probes) > maxProbes {
		d.Probes = d.Probes[len(d.Probes)-maxProbes:]
	}
	return true
}

// recordTraffic 记一轮业务流量:字节记在 at 所在的整点,活跃槽位按 [at-span, at) 每 2 秒一个置位。
func (n nodeHours) recordTraffic(at int64, up, down, durationMs int64) {
	d := n.get(hourOf(at))
	if up > 0 {
		d.Up += up
	}
	if down > 0 {
		d.Down += down
	}
	span := durationMs
	if span < 0 {
		span = 0
	}
	if span > maxActiveSpanMs {
		span = maxActiveSpanMs
	}
	for t := at - span; t < at; t += slotMs {
		hd := n.get(hourOf(t))
		if len(hd.Slots) != slotBytes {
			grown := make([]byte, slotBytes)
			copy(grown, hd.Slots)
			hd.Slots = grown
		}
		i := int(floorDiv(t-hourOf(t)*hourMs, slotMs))
		hd.Slots[i>>3] |= 1 << (i % 8)
	}
}

func popcountMs(b []byte) int64 {
	var c int
	for _, x := range b {
		c += bits.OnesCount8(x)
	}
	return int64(c) * slotMs
}

// summary 是一个节点 24 小时窗口里的汇总。
type summary struct {
	Success, Failure int
	FirstAt, LastAt  int64
	LastOK           bool
	Up, Down         int64
	ActiveMs         int64
	Latency          *float64 // 最近最多 10 次成功探测的中位数
	LatencyCount     int
	Jitter           *float64 // 最近最多 10 个「连续两次成功探测的延时差」的平均
	JitterCount      int
}

// summarize 汇总 24 小时窗口。union 不为 nil 时把各整点的活跃槽位并进去(订阅的使用时长用)。
func (n nodeHours) summarize(nowMs int64, union map[int64][]byte) summary {
	var sm summary
	if len(n) == 0 {
		return sm
	}
	first := hourOf(nowMs) - (windowHours - 1)
	hrs := make([]int64, 0, len(n))
	for h := range n {
		if h >= first {
			hrs = append(hrs, h)
		}
	}
	sort.Slice(hrs, func(i, j int) bool { return hrs[i] < hrs[j] })
	var lats, jits []float64
	var prev [2]int64
	havePrev := false
	for _, h := range hrs {
		d := n[h]
		if union != nil && len(d.Slots) > 0 {
			u := union[h]
			if u == nil {
				u = make([]byte, slotBytes)
				union[h] = u
			}
			for i := 0; i < len(d.Slots) && i < len(u); i++ {
				u[i] |= d.Slots[i]
			}
		}
		sm.Success += d.Success
		sm.Failure += d.Failure
		sm.Up += d.Up
		sm.Down += d.Down
		sm.ActiveMs += popcountMs(d.Slots)
		if d.FirstAt != 0 && (sm.FirstAt == 0 || d.FirstAt < sm.FirstAt) {
			sm.FirstAt = d.FirstAt
		}
		if d.LastAt > sm.LastAt {
			sm.LastAt, sm.LastOK = d.LastAt, d.LastOK
		}
		for _, p := range d.Probes {
			if p[0] > nowMs || nowMs-p[0] >= windowHours*hourMs {
				continue
			}
			if p[1] > 0 {
				lats = append(lats, float64(p[1]))
				if havePrev && prev[1] > 0 && p[0]-prev[0] < jitterGapMs {
					diff := p[1] - prev[1]
					if diff < 0 {
						diff = -diff
					}
					jits = append(jits, float64(diff))
				}
			}
			prev, havePrev = p, true
		}
	}
	if len(lats) > maxKeep {
		lats = lats[len(lats)-maxKeep:]
	}
	if len(jits) > maxKeep {
		jits = jits[len(jits)-maxKeep:]
	}
	sm.Latency, sm.LatencyCount = median(lats), len(lats)
	sm.Jitter, sm.JitterCount = meanOf(jits), len(jits)
	return sm
}

// probeSeries 取窗口内最近 60 条探测(图表用),从旧到新。
func (n nodeHours) probeSeries(nowMs int64) []Sample {
	first := hourOf(nowMs) - (windowHours - 1)
	hrs := make([]int64, 0, len(n))
	for h := range n {
		if h >= first {
			hrs = append(hrs, h)
		}
	}
	sort.Slice(hrs, func(i, j int) bool { return hrs[i] < hrs[j] })
	var all [][2]int64
	for _, h := range hrs {
		for _, p := range n[h].Probes {
			if p[0] <= nowMs && nowMs-p[0] < windowHours*hourMs && p[1] >= 0 {
				all = append(all, p)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i][0] < all[j][0] })
	if len(all) > maxProbes {
		all = all[len(all)-maxProbes:]
	}
	out := make([]Sample, 0, len(all))
	for _, p := range all {
		s := Sample{At: p[0], Value: float64(p[1])}
		if p[1] == 0 {
			s.Failed = true
		}
		out = append(out, s)
	}
	return out
}

// trim 丢掉窗口外的整点,返回是否还有内容。
func (n nodeHours) trim(nowMs int64) bool {
	first := hourOf(nowMs) - (windowHours - 1)
	for h := range n {
		if h < first {
			delete(n, h)
		}
	}
	return len(n) > 0
}
