package groups

import (
	"encoding/json"
	"testing"
)

// 自写测试向量:排除关键词(对齐上游 v0.1.472 面板的 excludeKeywords / excludeKeywordMode)。期望值按规则人工推算:
//   - 只对动态组生效:先按 keywords 选(没写 = 全部非链式节点),再剔除命中排除词的节点;
//   - excludeKeywordMode 只有恰好是 "and" 才是 and:and = 所有(非空)排除词都命中才移出,否则 or = 命中任一就移出;
//   - 排除词全是空串时不排除任何节点;
//   - 归一化:excludeKeywords 只收非空字符串;非空才序列化 excludeKeywords / excludeKeywordMode(紧跟 keywords),
//     故障转移与内置出站强制清空。

func TestEmitDynamicExcludeVectors(t *testing.T) {
	nodes := `[{"tag":"HK-01","regionName":"香港"},{"tag":"HK-02 倍率","regionName":"香港"},{"tag":"US-01","regionName":"美国"},` +
		`{"tag":"US-02 倍率","regionName":"美国"},{"tag":"Chain-HK","regionName":"香港","chain":true}]`
	run := func(name, extra string, want ...string) {
		t.Run(name, func(t *testing.T) {
			w := er{outs: all(selOb("D", want...)), public: pub("D")}
			if len(want) == 0 {
				w = er{outs: all(selOb("D", "直连")), ph: []string{"D"}, public: pub("D")}
			}
			checkEmit(t, `[{"id":"d","name":"D","mode":"dynamic"`+extra+`}]`, nodes, `{}`, w)
		})
	}
	run("没有包含词:全部减去排除", `,"excludeKeywords":["倍率"]`, "HK-01", "US-01")
	run("包含词再排除", `,"keywords":["香港"],"excludeKeywords":["倍率"]`, "HK-01")
	run("or 任一排除词命中就移出", `,"excludeKeywords":["02","美国"]`, "HK-01")
	run("and 要全部排除词命中才移出", `,"excludeKeywordMode":"and","excludeKeywords":["02","美国"]`, "HK-01", "HK-02 倍率", "US-01")
	run("and 全部命中", `,"excludeKeywordMode":"and","excludeKeywords":["02","倍率"]`, "HK-01", "US-01")
	run("排除模式独立于包含模式", `,"keywordMode":"and","keywords":["香港","01"],"excludeKeywordMode":"or","excludeKeywords":["hk"]`)
	run("排除模式不是 and 都是 or", `,"excludeKeywordMode":"AND","excludeKeywords":["02","美国"]`, "HK-01")
	run("排除词全空不排除", `,"excludeKeywords":["", "  "]`, "HK-01", "HK-02 倍率", "US-01", "US-02 倍率")
	run("排除全部挂占位", `,"excludeKeywords":["-0"]`)
	// 静态组不受排除词影响
	checkEmit(t, `[{"id":"s","name":"S","mode":"static","members":["HK-01","US-01"],"excludeKeywords":["hk"]}]`, nodes, `{}`,
		er{outs: all(selOb("S", "HK-01", "US-01")), public: pub("S")})
}

func TestNormalizeGroupsExcludeVectors(t *testing.T) {
	out := func(in string) string {
		b, _ := json.Marshal(NormalizeGroups(gany(t, in)))
		return string(b)
	}
	has := func(s, sub string) bool { return len(s) >= len(sub) && contains(s, sub) }
	g := out(`[{"id":"d","name":"D","mode":"dynamic","keywords":["a"],"excludeKeywords":["x"," ",7,"y"],"excludeKeywordMode":"and"}]`)
	if !has(g, `"keywords":["a"],"excludeKeywords":["x","y"],"excludeKeywordMode":"and","members":[]`) {
		t.Fatalf("排除词没按预期序列化: %s", g)
	}
	g = out(`[{"id":"d","name":"D","mode":"dynamic","excludeKeywords":["x"],"excludeKeywordMode":"AND"}]`)
	if !has(g, `"excludeKeywordMode":"or"`) {
		t.Fatalf("非 and 应归为 or: %s", g)
	}
	g = out(`[{"id":"d","name":"D","mode":"dynamic","excludeKeywords":[]}]`)
	if has(g, "excludeKeyword") {
		t.Fatalf("没有排除词不该序列化: %s", g)
	}
	g = out(`[{"id":"f","name":"F","type":"failover","excludeKeywords":["x"]}]`)
	if has(g, "excludeKeyword") {
		t.Fatalf("故障转移应清空排除词: %s", g)
	}
	g = out(`[{"id":"builtin-direct","name":"直连","excludeKeywords":["x"]}]`)
	if has(g, "excludeKeyword") {
		t.Fatalf("内置出站应清空排除词: %s", g)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
