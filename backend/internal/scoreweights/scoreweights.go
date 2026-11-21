// Package scoreweights 是节点评分的三项权重(延时、下载带宽、波动)。
// 对应档案 nodeScoreWeights;面板用一条带两个分隔点的滑块调,三项加起来恒为 100。
package scoreweights

import (
	"fmt"
	"math"
)

// Weights 是三项权重,单位是百分点,和为 100。
type Weights struct {
	Latency  float64 `json:"latency"`
	Download float64 `json:"download"`
	Jitter   float64 `json:"jitter"`
}

// Default 是出厂权重:延时 40、下载 30、波动 30。
func Default() Weights { return Weights{Latency: 40, Download: 30, Jitter: 30} }

const sumTolerance = 1e-6

// Normalize 规整档案里的 nodeScoreWeights:没有就用默认;有就必须三项齐全,
// 每项 0~100 的数,合计 100(容许浮点误差)。非法返回错误信息(400)。
func Normalize(v any) (Weights, error) {
	if v == nil {
		return Default(), nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Weights{}, fmt.Errorf("nodeScoreWeights must be an object")
	}
	var out Weights
	for _, f := range []struct {
		key string
		dst *float64
	}{{"latency", &out.Latency}, {"download", &out.Download}, {"jitter", &out.Jitter}} {
		raw, present := m[f.key]
		if !present {
			return Weights{}, fmt.Errorf("nodeScoreWeights.%s is required", f.key)
		}
		n, ok := toFloat(raw)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100 {
			return Weights{}, fmt.Errorf("nodeScoreWeights.%s must be a number between 0 and 100", f.key)
		}
		*f.dst = round10(n)
	}
	if math.Abs(out.Latency+out.Download+out.Jitter-100) > sumTolerance {
		return Weights{}, fmt.Errorf("nodeScoreWeights must add up to 100")
	}
	return out, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func round10(x float64) float64 { return math.Round(x*1e10) / 1e10 }
