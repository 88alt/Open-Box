// Package bandwidth 是带宽择优(重写):测速参数、排队、状态与历史。
// 只依据前端 OpenboxBandwidth* 类型与 STATUS 口径,不参照已回退的 3414adff 实现。
package bandwidth

import (
	"fmt"
	"math"
)

// Settings 对应档案 bandwidth / OpenboxBandwidthSettings。
type Settings struct {
	IntervalHours int `json:"intervalHours"`
	MaxDownloadMB int `json:"maxDownloadMB"`
}

const (
	minIntervalHours = 1
	maxIntervalHours = 24
	minDownloadMB    = 1
	maxDownloadMB    = 1024
)

// DefaultSettings 默认测速参数。
func DefaultSettings() Settings {
	return Settings{IntervalHours: 6, MaxDownloadMB: 50}
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
	if x, ok := m["maxDownloadMB"]; ok {
		n, err := asInt(x)
		if err != nil || n < minDownloadMB || n > maxDownloadMB {
			return Settings{}, fmt.Errorf("maxDownloadMB must be an integer between %d and %d", minDownloadMB, maxDownloadMB)
		}
		out.MaxDownloadMB = n
	}
	return out, nil
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
