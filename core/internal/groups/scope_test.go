package groups

import (
	"encoding/json"
	"strings"
	"testing"
)

// 自写测试向量:动态组的订阅范围(对齐上游 v0.1.476 面板的 subscriptionIds / chain-proxy)。期望值按规则人工推算:
//   - 没有范围 = 所有非链式节点按关键词挑(和以前一样,链式代理的节点不进动态组);
//   - 有范围 = 只在 subscriptionId 属于范围的节点里挑;范围里写 chain-proxy,链式代理的节点才是候选;
//   - 链式节点的上游线路绕回这个组(直接指向它,或经静态组 / 别的组再回到它)就不收,别的链式节点照收;
//   - 归一化:只对动态组保留 subscriptionIds,去空去重;老记录的单个 subscriptionId 折进数组。

const scopeNodes = `[{"tag":"A-1","subscriptionId":"s1"},{"tag":"A-2","subscriptionId":"s1"},{"tag":"B-1","subscriptionId":"s2"},` +
	`{"tag":"C-1","chain":true,"detour":"X"},{"tag":"Chain-US","chain":true,"detour":"A-1"}]`

func TestEmitDynamicScopeVectors(t *testing.T) {
	run := func(name, extra string, want ...string) {
		t.Run(name, func(t *testing.T) {
			w := er{outs: all(selOb("D", want...)), public: pub("D")}
			if len(want) == 0 {
				w = er{outs: all(selOb("D", "直连")), ph: []string{"D"}, public: pub("D")}
			}
			checkEmit(t, `[{"id":"d","name":"D","mode":"dynamic"`+extra+`}]`, scopeNodes, `{}`, w)
		})
	}
	run("没有范围:非链式节点全进,链式不进", ``, "A-1", "A-2", "B-1")
	run("一个订阅", `,"subscriptionIds":["s1"]`, "A-1", "A-2")
	run("两个订阅", `,"subscriptionIds":["s1","s2"]`, "A-1", "A-2", "B-1")
	run("老记录的单个 subscriptionId", `,"subscriptionId":"s2"`, "B-1")
	run("范围再加关键词", `,"subscriptionIds":["s1","s2"],"keywords":["-1"]`, "A-1", "B-1")
	run("只选链式代理", `,"subscriptionIds":["chain-proxy"]`, "C-1", "Chain-US")
	run("订阅加链式代理", `,"subscriptionIds":["s2","chain-proxy"]`, "B-1", "C-1", "Chain-US")
	run("范围里的订阅不存在:空组挂占位", `,"subscriptionIds":["gone"]`)
}

func TestEmitDynamicChainCycle(t *testing.T) {
	nodes := `[{"tag":"A-1","subscriptionId":"s1"},{"tag":"C-1","chain":true,"detour":"A-1"},` +
		`{"tag":"Self","chain":true,"detour":"D"},{"tag":"Via","chain":true,"detour":"S"}]`
	groups := `[{"id":"d","name":"D","mode":"dynamic","subscriptionIds":["chain-proxy"]},` +
		`{"id":"s","name":"S","mode":"static","members":["D"]}]`
	// Self 直接指回 D、Via 经 S → D 绕回来:都不收;C-1 的上游是普通节点,照收
	checkEmit(t, groups, nodes, `{}`, er{outs: all(selOb("D", "C-1"), selOb("S", "D")), public: pub("D", "S")})
}

func TestNormalizeGroupsScopeVectors(t *testing.T) {
	out := func(in string) string {
		b, _ := json.Marshal(NormalizeGroups(gany(t, in)))
		return string(b)
	}
	g := out(`[{"id":"d","name":"D","mode":"dynamic","subscriptionIds":["s1"," ","s1","chain-proxy",7]}]`)
	if !strings.Contains(g, `"subscriptionIds":["s1","chain-proxy"]`) {
		t.Fatalf("范围没按预期保留: %s", g)
	}
	g = out(`[{"id":"d","name":"D","mode":"dynamic","subscriptionId":"s9"}]`)
	if !strings.Contains(g, `"subscriptionIds":["s9"]`) || strings.Contains(g, `"subscriptionId"`) {
		t.Fatalf("老字段要折进数组: %s", g)
	}
	g = out(`[{"id":"d","name":"D","mode":"dynamic","subscriptionIds":[]}]`)
	if strings.Contains(g, "subscription") {
		t.Fatalf("没有范围不该序列化: %s", g)
	}
	for _, in := range []string{
		`[{"id":"s","name":"S","mode":"static","subscriptionIds":["s1"]}]`,
		`[{"id":"f","name":"F","type":"failover","subscriptionIds":["s1"]}]`,
	} {
		if g = out(in); strings.Contains(g, "subscription") {
			t.Fatalf("静态组 / 故障转移不带范围: %s", g)
		}
	}
}
