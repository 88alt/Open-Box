// Package bandwidth 是带宽择优(重写):测速参数、排队、状态与历史。
// 只依据前端 OpenboxBandwidth* 类型与 STATUS 口径,不参照已回退的 3414adff 实现。
package bandwidth

import (
	"fmt"
	"math"
	"time"
)

// Settings 对应档案 bandwidth / OpenboxBandwidthSettings。
type Settings struct {
	IntervalHours int `json:"intervalHours"`
	// IntervalUnit 是面板上周期的显示单位("hours" / "days");IntervalHours 始终是换算后的小时数。
	IntervalUnit  string `json:"intervalUnit"`
	MaxDownloadMB int    `json:"maxDownloadMB"`
	// StartHour 是按「天」测试时每天几点开始(本地时间 0~23,默认 4);按小时测试时不起作用。
	// 对齐上游 v0.1.481 的 bandwidth.startHour。
	StartHour int `json:"startHour"`
}

// 周期显示单位。
const (
	UnitHours = "hours"
	UnitDays  = "days"
)

const (
	minIntervalHours = 1
	maxIntervalHours = 720
	minDownloadMB    = 1
	maxDownloadMB    = 1024
	// DefaultStartHour 按天测试时的默认开始时刻(上游面板在没写 startHour 时显示 04:00)。
	DefaultStartHour = 4
)

// DefaultSettings 默认测速参数。
// 12 小时 / 64 MB 对齐上游 v0.1.488 面板的缺省值(它读不到档案里的 bandwidth 时按 12 / 64 显示);
// 4 点对齐上游按天测试的默认开始时刻。
func DefaultSettings() Settings {
	return Settings{IntervalHours: 12, IntervalUnit: UnitHours, MaxDownloadMB: 64, StartHour: DefaultStartHour}
}

// NormalizeSettings 规整档案里的 bandwidth;非法返回错误信息(400)。
func NormalizeSettings(v any) (Settings, error) {
	def := DefaultSettings()
	if v == nil {
		return def, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Settings{}, fmt.Errorf("bandwidth must be an object")
	}
	out := def
	if x, ok := m["intervalHours"]; ok {
		n, err := asInt(x)
		if err != nil || n < minIntervalHours || n > maxIntervalHours {
			return Settings{}, fmt.Errorf("intervalHours must be an integer between %d and %d", minIntervalHours, maxIntervalHours)
		}
		out.IntervalHours = n
	}
	if x, ok := m["intervalUnit"]; ok {
		u, isStr := x.(string)
		if !isStr || (u != UnitHours && u != UnitDays) {
			return Settings{}, fmt.Errorf("intervalUnit must be %q or %q", UnitHours, UnitDays)
		}
		out.IntervalUnit = u
	}
	if x, ok := m["maxDownloadMB"]; ok {
		n, err := asInt(x)
		if err != nil || n < minDownloadMB || n > maxDownloadMB {
			return Settings{}, fmt.Errorf("maxDownloadMB must be an integer between %d and %d", minDownloadMB, maxDownloadMB)
		}
		out.MaxDownloadMB = n
	}
	if x, ok := m["startHour"]; ok {
		n, err := asInt(x)
		if err != nil || n < 0 || n > 23 {
			return Settings{}, fmt.Errorf("startHour must be an integer between 0 and 23")
		}
		out.StartHour = n
	}
	return out, nil
}

// Days 是按天测试时的间隔天数:IntervalHours 折成天、四舍五入、至少 1 天(与上游面板的帮助文字同口径)。
func (s Settings) Days() int {
	d := int(math.Round(float64(s.IntervalHours) / 24))
	if d < 1 {
		d = 1
	}
	return d
}

// NextDue 给出上一次测完的时刻 completed 之后,这个节点下一次该测的时刻。
// 按小时:completed + IntervalHours。按天:completed 所在日期(completed 所在时区的日历日)往后数 Days() 天、当天 StartHour 点整,
// 用日历日期推算,夏令时切换日也还是那天的那个整点。
func (s Settings) NextDue(completed time.Time) time.Time {
	if s.IntervalUnit != UnitDays {
		h := s.IntervalHours
		if h < minIntervalHours {
			h = minIntervalHours
		}
		return completed.Add(time.Duration(h) * time.Hour)
	}
	y, mo, d := completed.Date()
	return time.Date(y, mo, d+s.Days(), s.StartHour, 0, 0, 0, completed.Location())
}

func asInt(v any) (int, error) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("not int")
		}
		return int(n), nil
	case int:
		return n, nil
	case int64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("not int")
	}
}
