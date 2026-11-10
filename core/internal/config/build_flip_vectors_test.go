package config

import "testing"

// 自写测试向量:BuildDetailed 返回的 flipFlags({开关 tag: 此刻是否开})。
// 期望值人工推算,tag 由 Python 对站点集 id 独立算出(obflip- + sha256(id) 十六进制前 12 位),不引用被测代码:
//   - 开关 = 此刻「不是直连」:站点集默认走节点组 → 开;默认直连 → 关;默认拒绝 → 开(拒绝也不是直连);
//   - 兜底另有固定的 obflip-fallback,按兜底的默认值同样判;
//   - 没有站点集时只有兜底一个开关;
//   - flipFlags 序列化后键按字母序,所以期望串里 p2、p3、p1 的次序是 obflip-3946… < obflip-43bb… < obflip-f645… < obflip-fallback;
//   - 站点集 id 为 p1 / p2 / p3 时 tag 依次是 obflip-f64551fcd6f0 / obflip-3946ca64ff78 / obflip-43bb00d0ce77。

const (
	flipP1 = "obflip-f64551fcd6f0"
	flipP2 = "obflip-3946ca64ff78"
	flipP3 = "obflip-43bb00d0ce77"
)

func flipArgs(pols, fallback string) string {
	// 兜底在 routing 里是 fallbackName / fallbackDefault 两个键(没写就是名字「其他」、默认取成员表第一项即直连)
	fb := ""
	if fallback != "" {
		fb = `,"fallbackName":"其他","fallbackDefault":` + fallback
	}
	return `{"nodes":[` + sk("A", "s1.example.com", 1) + `],"userGroups":[{"id":"s","name":"S","members":["A"]}],` +
		`"profile":{"dns":{},"routing":{"policies":` + pols + fb + `}}}`
}

func TestBuildFlipFlags(t *testing.T) {
	pols := `[{"id":"p1","name":"P1","default":"S","domainSuffix":["p1.example"]},` +
		`{"id":"p2","name":"P2","default":"direct","domainSuffix":["p2.example"]},` +
		`{"id":"p3","name":"P3","default":"block","domainSuffix":["p3.example"]}]`
	cases := []struct{ name, fallback, want string }{
		{"兜底缺省按直连", ``, `{"` + flipP2 + `":false,"` + flipP3 + `":true,"` + flipP1 + `":true,"obflip-fallback":false}`},
		{"兜底走节点组", `"S"`, `{"` + flipP2 + `":false,"` + flipP3 + `":true,"` + flipP1 + `":true,"obflip-fallback":true}`},
		{"兜底直连", `"direct"`, `{"` + flipP2 + `":false,"` + flipP3 + `":true,"` + flipP1 + `":true,"obflip-fallback":false}`},
	}
	for _, c := range cases {
		res := bhMust(t, flipArgs(pols, c.fallback))
		if got := bhJSON(t, res["flipFlags"]); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

// flipFlags 的键必须正好是配置里本地开关规则集的 tag——后端按这个键写开关文件,对不上内核就读不到。
func TestBuildFlipFlagsMatchRuleSets(t *testing.T) {
	pols := `[{"id":"p1","name":"P1","default":"S","domainSuffix":["p1.example"]},{"id":"p2","name":"P2","default":"direct","domainSuffix":["p2.example"]}]`
	res := bhMust(t, flipArgs(pols, ``))
	flags := res["flipFlags"].(map[string]any)
	inConfig := map[string]bool{}
	for _, rs := range bhCfg(res)["route"].(map[string]any)["rule_set"].([]any) {
		m := rs.(map[string]any)
		if tag, _ := m["tag"].(string); len(tag) > 7 && tag[:7] == "obflip-" && m["format"] == "source" {
			inConfig[tag] = true
		}
	}
	if len(flags) != 3 || len(inConfig) != 3 {
		t.Fatalf("flipFlags %d 个,配置里的开关规则集 %d 个:%v / %v", len(flags), len(inConfig), flags, inConfig)
	}
	for tag := range flags {
		if !inConfig[tag] {
			t.Errorf("flipFlags 里的 %s 在配置的 rule_set 里找不到", tag)
		}
	}
}
