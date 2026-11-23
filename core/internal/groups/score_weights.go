package groups

import (
	"math"

	"github.com/timeflysoon/Open-Box/core/internal/jsx"
)

// ScoreWeights 是节点评分权重,写进补丁版 sing-box 的 urltest / loadbalance 的 node_score_weights。
// 字段名与校验取自上游公开的 sing-box 魔改源码(option/openbox_load_balance.go 的 NodeScoreWeights):
// latency / download / jitter 各 0~100、三项相加 100;不写时内核用 40 / 30 / 30。
type ScoreWeights struct {
	Latency  float64 `json:"latency"`
	Download float64 `json:"download"`
	Jitter   float64 `json:"jitter"`
}

// ScoreWeightsOf 把档案的 nodeScoreWeights 整理成 *ScoreWeights;不是对象、缺项、非数字、负数、
// 超过 100 或三项之和不是 100(容许 1e-9)一律返回 nil(不写,让内核用默认),与 mihomo 侧的取法一致。
func ScoreWeightsOf(v any) *ScoreWeights {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	var f [3]float64
	for i, k := range []string{"latency", "download", "jitter"} {
		x, ok := jsx.Get(m, k).(float64)
		if !ok || math.IsNaN(x) || x < 0 || x > 100 {
			return nil
		}
		f[i] = x
	}
	if math.Abs(f[0]+f[1]+f[2]-100) >= 1e-9 {
		return nil
	}
	return &ScoreWeights{Latency: f[0], Download: f[1], Jitter: f[2]}
}
