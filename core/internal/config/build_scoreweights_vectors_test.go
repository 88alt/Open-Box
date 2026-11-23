package config

import (
	"strings"
	"testing"
)

// 自写测试向量:BuildDetailed 把档案的 nodeScoreWeights 写进开着带宽择优的 urltest / loadbalance
// (字段名 node_score_weights,取自上游公开的 sing-box 魔改源码);没有 / 不合法 / 没开带宽 / disableBandwidth 都不写。
func scoreWeightsArgs(profileExtra, groupExtra string) string {
	return `{"nodes":[` + sk("A", "s1.example.com", 1) + `],"userGroups":[` +
		`{"id":"u","name":"U","type":"urltest","mode":"static","members":["A"]` + groupExtra + `},` +
		`{"id":"l","name":"L","type":"loadbalance","mode":"static","members":["A"]` + groupExtra + `}],` +
		`"profile":{"dns":{}` + profileExtra + `}}`
}

func TestBuildNodeScoreWeights(t *testing.T) {
	const w = `,"nodeScoreWeights":{"latency":50,"download":30,"jitter":20}`
	// bhJSON 把对象重排成按键名字母序,所以权重内部是 download jitter latency
	const want = `"node_score_weights":{"download":30,"jitter":20,"latency":50}`
	cases := []struct {
		name, profile, group string
		wantN                int // 配置里 node_score_weights 出现的次数
	}{
		{"开着带宽且有权重:urltest 与 loadbalance 各一份", w, `,"bandwidth":true`, 2},
		{"没开带宽不写", w, ``, 0},
		{"客户端配置 disableBandwidth 不写", w + `,"disableBandwidth":true`, `,"bandwidth":true`, 0},
		{"权重不合计 100 不写", `,"nodeScoreWeights":{"latency":50,"download":30,"jitter":30}`, `,"bandwidth":true`, 0},
		{"档案没有权重不写", ``, `,"bandwidth":true`, 0},
	}
	for _, c := range cases {
		res := bhMust(t, scoreWeightsArgs(c.profile, c.group))
		got := bhJSON(t, res["config"])
		if n := strings.Count(got, `"node_score_weights"`); n != c.wantN {
			t.Errorf("%s: node_score_weights 出现 %d 次,期望 %d", c.name, n, c.wantN)
		}
		if c.wantN > 0 && strings.Count(got, want) != c.wantN {
			t.Errorf("%s: 期望 %d 处 %s", c.name, c.wantN, want)
		}
	}
}
