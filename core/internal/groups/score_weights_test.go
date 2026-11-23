package groups

import (
	"encoding/json"
	"strings"
	"testing"
)

// 自写测试向量:节点评分权重(档案 nodeScoreWeights → 出站的 node_score_weights)。
// 字段名与校验取自上游公开发布的 sing-box 魔改源码(option/openbox_load_balance.go 的 NodeScoreWeights:
// latency / download / jitter,各 0~100,三项相加 100);期望值人工写出:
//   - 只有对象、三项齐全、都是数字、各在 0~100、合计 100(容许 1e-9)才写,别的一律不写(内核用固定的 40 / 30 / 30);
//   - 只写在开着带宽择优的 urltest / loadbalance 上(含故障转移多节点自动页签的内部 urltest);selector、没开带宽、
//     disableBandwidth(客户端配置)都不写;
//   - 序列化键序:bandwidth 之后紧跟 node_score_weights,权重内部 latency download jitter;老档案(没有这项)产出不变。

func TestScoreWeightsOf(t *testing.T) {
	obj := func(l, d, j any) map[string]any { return map[string]any{"latency": l, "download": d, "jitter": j} }
	ok := []struct {
		name string
		in   any
		want ScoreWeights
	}{
		{"默认 40/30/30", obj(40.0, 30.0, 30.0), ScoreWeights{40, 30, 30}},
		{"只看延迟", obj(100.0, 0.0, 0.0), ScoreWeights{100, 0, 0}},
		{"小数合计 100", obj(33.5, 33.5, 33.0), ScoreWeights{33.5, 33.5, 33}},
		{"浮点误差内", obj(0.1, 0.2, 99.7), ScoreWeights{0.1, 0.2, 99.7}},
	}
	for _, c := range ok {
		got := ScoreWeightsOf(c.in)
		if got == nil || *got != c.want {
			t.Errorf("%s: 得到 %v 期望 %v", c.name, got, c.want)
		}
	}
	bad := []struct {
		name string
		in   any
	}{
		{"nil", nil},
		{"字符串", "40/30/30"},
		{"数组", []any{40.0, 30.0, 30.0}},
		{"缺 jitter", map[string]any{"latency": 50.0, "download": 50.0}},
		{"缺 latency", map[string]any{"download": 50.0, "jitter": 50.0}},
		{"字符串数值", obj("40", 30.0, 30.0)},
		{"null 项", obj(nil, 50.0, 50.0)},
		{"负数", obj(-10.0, 60.0, 50.0)},
		{"超过 100", obj(120.0, -10.0, -10.0)},
		{"合计 99", obj(40.0, 30.0, 29.0)},
		{"合计 101", obj(40.0, 31.0, 30.0)},
		{"合计超出误差", obj(40.0, 30.0, 30.000001)},
	}
	for _, c := range bad {
		if got := ScoreWeightsOf(c.in); got != nil {
			t.Errorf("%s: 不合法要返回 nil,得到 %v", c.name, *got)
		}
	}
}

var swOpts = map[string]any{"nodeScoreWeights": map[string]any{"latency": 50.0, "download": 30.0, "jitter": 20.0}}

func swWith(extra map[string]any) map[string]any {
	o := map[string]any{}
	for k, v := range swOpts {
		o[k] = v
	}
	for k, v := range extra {
		o[k] = v
	}
	return o
}

func TestScoreWeightsEmitUrltestAndLoadBalance(t *testing.T) {
	const want = `"bandwidth":true,"node_score_weights":{"latency":50,"download":30,"jitter":20}`
	for _, typ := range []string{"urltest", "loadbalance"} {
		b, _ := json.Marshal(lbOutbound(t, bwEmit(t, bwGroup(typ, map[string]any{"bandwidth": true}), swOpts), "G"))
		if !strings.Contains(string(b), want) {
			t.Errorf("%s 序列化缺权重或键序不对: %s", typ, b)
		}
		// 没开带宽:不写
		off, _ := json.Marshal(lbOutbound(t, bwEmit(t, bwGroup(typ, nil), swOpts), "G"))
		if strings.Contains(string(off), "node_score_weights") {
			t.Errorf("%s 没开带宽不该写权重: %s", typ, off)
		}
		// 客户端配置:disableBandwidth 压住
		c, _ := json.Marshal(lbOutbound(t, bwEmit(t, bwGroup(typ, map[string]any{"bandwidth": true}), swWith(map[string]any{"disableBandwidth": true})), "G"))
		if strings.Contains(string(c), "node_score_weights") {
			t.Errorf("%s disableBandwidth 时不该写权重: %s", typ, c)
		}
		// 权重不合法 / 没有:不写,其余照旧
		for _, in := range []any{nil, map[string]any{"latency": 50.0, "download": 30.0, "jitter": 30.0}, "x"} {
			n, _ := json.Marshal(lbOutbound(t, bwEmit(t, bwGroup(typ, map[string]any{"bandwidth": true}), map[string]any{"nodeScoreWeights": in}), "G"))
			if strings.Contains(string(n), "node_score_weights") || !strings.Contains(string(n), `"bandwidth":true`) {
				t.Errorf("%s 权重 %v 不合法应只写 bandwidth: %s", typ, in, n)
			}
		}
	}
}

func TestScoreWeightsSelectorNever(t *testing.T) {
	b, _ := json.Marshal(lbOutbound(t, bwEmit(t, bwGroup("selector", map[string]any{"bandwidth": true}), swOpts), "G"))
	if strings.Contains(string(b), "node_score_weights") {
		t.Errorf("selector 不写权重: %s", b)
	}
}

func TestScoreWeightsFailoverLanes(t *testing.T) {
	lanes := []any{
		map[string]any{"id": "a", "name": "", "icon": "", "members": []any{"N0", "N1"}},                 // 多节点,自动择优
		map[string]any{"id": "b", "name": "", "icon": "", "members": []any{"N2"}},                       // 单节点
		map[string]any{"id": "c", "name": "", "icon": "", "members": []any{"N0", "N1"}, "manual": true}, // 手动
	}
	g := map[string]any{"id": "f1", "name": "FO", "type": "failover", "mode": "static", "lanes": lanes, "bandwidth": true}
	var with int
	for _, o := range bwEmit(t, []any{g}, swOpts).Outbounds {
		if o.NodeScoreWeights != nil {
			with++
			if o.Type != "urltest" || !o.Bandwidth || *o.NodeScoreWeights != (ScoreWeights{50, 30, 20}) {
				t.Errorf("%s(%s)权重 %v 不对", o.Tag, o.Type, *o.NodeScoreWeights)
			}
		}
	}
	if with != 1 {
		t.Errorf("只有多节点自动页签的内部 urltest 带权重,得到 %d 个", with)
	}
	for _, o := range bwEmit(t, []any{g}, swWith(map[string]any{"disableBandwidth": true})).Outbounds {
		if o.NodeScoreWeights != nil {
			t.Errorf("disableBandwidth 时 %s 不该有权重", o.Tag)
		}
	}
}

// 老档案(没有 nodeScoreWeights)的出站序列化不变。
func TestScoreWeightsAbsentUnchanged(t *testing.T) {
	b, _ := json.Marshal(lbOutbound(t, bwEmit(t, bwGroup("urltest", map[string]any{"bandwidth": true}), map[string]any{}), "G"))
	if strings.Contains(string(b), "node_score_weights") {
		t.Errorf("没有档案权重不该写: %s", b)
	}
}
