package routing

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/protocol"
)

// 自写测试向量:migrateStoredRouting(老档案的 routing 迁到新模型)。输入自己拟,期望值按下面的规则人工推算:
//   - 档案里一个老字段都没有(regions / regionId / regionMode / outboundOptions / adBlock / adRuleset / categories /
//     directRulesets / fallback / proxyTag,只看键在不在,值是 null 也算在)→ 原样返回,changed 为假;
//     输入不是对象(null / 数组 / 字符串 / 数字 / 布尔)当成 {};
//   - 有老字段 → changed 为真,十个老字段全部删掉,其余键(custom、fallbackName 等)原样带过去;
//   - 站点集:没有 policies 数组,或 policies 为空且 categories 非空 / directRulesets 里有不在 [geosite-cn, geoip-cn] 的项,
//     才从老字段翻译(categories 逐条一个站点集,id 为 legacy-+规则集名,default 取 target;
//     directRulesets 去掉 geosite-cn / geoip-cn 后剩下的合成一个「始终直连」站点集);其余情形用户自己的 policies 原样保留
//     (不归一化,连形状错的项也不动);
//   - 地区层:档案里 fallbackDefault 是非空字符串说明已迁过,不再翻译;否则按 regions(没写 / 为空用内置三个地区)
//     与 regionId(优先)/ regionMode(CN→cn、HKMO→hkmo、OTHER→other)选一个地区,按 direct / proxy 各拆成最多一个站点集
//     (id 为 region-<地区id>-<动作>,名字为 <地区名>·直连 / ·代理,图标为地球),地区的兜底写进 fallbackDefault;
//     翻译出来的站点集排在 categories / directRulesets 翻出来的后面;
//   - 只有 policies 翻译出东西、或原本就有 policies 数组时才写 policies 键(作为补丁合并时不能把现有站点集清空)。

const migIcon = "globe:earth-meridians"

// migPol 拼一份迁移产出的站点集(enabled 为真、其余列表默认为空)。
func migPol(id, name, icon, def, rulesets, domain, suffix, cidr string) string {
	for _, p := range []*string{&rulesets, &domain, &suffix, &cidr} {
		if *p == "" {
			*p = "[]"
		}
	}
	return fmt.Sprintf(`{"id":%q,"name":%q,"icon":%q,"default":%q,"enabled":true,"ruleUrls":[],"rulesets":%s,"domain":%s,"domainSuffix":%s,"domainKeyword":[],"ipCidr":%s,"notes":{}}`,
		id, name, icon, def, rulesets, domain, suffix, cidr)
}

func migPols(ps ...string) string { return "[" + strings.Join(ps, ",") + "]" }

// migRun 跑一次迁移:顺带检查不改入参。geo 为 nil 表示没有规则库名单。
func migRun(t *testing.T, in string, geo []string) (any, bool) {
	t.Helper()
	if geo == nil {
		setAvailableGeoTags(nil)
	} else {
		setAvailableGeoTags(&geo)
	}
	defer setAvailableGeoTags(nil)
	v := mustJSON(t, in)
	before, _ := json.Marshal(v)
	out, changed, _ := migrateStoredRoutingObj(v)
	after, _ := json.Marshal(v)
	if string(before) != string(after) {
		t.Errorf("迁移改了入参\n前: %s\n后: %s", before, after)
	}
	return roundTrip(t, out), changed
}

func TestMigrateVectors(t *testing.T) {
	cnDirect := migPol("region-cn-direct", "中国大陆·直连", migIcon, "direct", `["geosite-cn","geoip-cn"]`, "", "", "")
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
		geo     []string
	}{
		// ---- 没有老字段:原样返回 ----
		{"null", `null`, `{}`, false, nil},
		{"数组", `[1,2]`, `{}`, false, nil},
		{"字符串", `"regions"`, `{}`, false, nil},
		{"数字", `5`, `{}`, false, nil},
		{"布尔", `true`, `{}`, false, nil},
		{"空对象", `{}`, `{}`, false, nil},
		{"新模型原样", `{"policies":[{"name":"a"}],"fallbackDefault":"direct","custom":{"enabled":true}}`,
			`{"policies":[{"name":"a"}],"fallbackDefault":"direct","custom":{"enabled":true}}`, false, nil},
		{"近似键名不算老字段", `{"regionID":"cn","Regions":[],"proxytag":1,"fallbackname":"x","adblock":true,"category":[]}`,
			`{"regionID":"cn","Regions":[],"proxytag":1,"fallbackname":"x","adblock":true,"category":[]}`, false, nil},

		// ---- 十个老字段单独出现:只看键在不在 ----
		{"只有 regions", `{"regions":1}`, `{}`, true, nil},
		{"只有 regionId", `{"regionId":1}`, `{}`, true, nil},
		{"只有 regionMode", `{"regionMode":1}`, `{}`, true, nil},
		{"只有 outboundOptions", `{"outboundOptions":1}`, `{}`, true, nil},
		{"只有 adBlock", `{"adBlock":1}`, `{}`, true, nil},
		{"只有 adRuleset", `{"adRuleset":1}`, `{}`, true, nil},
		{"只有 categories", `{"categories":1}`, `{}`, true, nil},
		{"只有 directRulesets", `{"directRulesets":1}`, `{}`, true, nil},
		{"只有 fallback", `{"fallback":1}`, `{}`, true, nil},
		{"只有 proxyTag", `{"proxyTag":1}`, `{}`, true, nil},
		{"值是 null 也算在", `{"adBlock":null}`, `{}`, true, nil},
		{"老字段删掉、别的键带走", `{"proxyTag":"节点选择","custom":{"enabled":false},"fallbackName":"兜","policies":[{"id":"p1","name":"A","rulesets":["geosite-x"]}],"fallbackDefault":"proxy"}`,
			`{"custom":{"enabled":false},"fallbackName":"兜","policies":[{"id":"p1","name":"A","rulesets":["geosite-x"]}],"fallbackDefault":"proxy"}`, true, nil},

		// ---- categories ----
		{"categories 逐条一个站点集", `{"categories":[{"ruleset":"geosite-netflix","target":"美国"},{"ruleset":" geosite-youtube ","target":" "},{"ruleset":""},{"target":"x"},"str",null],"fallbackDefault":"direct"}`,
			`{"policies":` + migPols(
				migPol("legacy-geosite-netflix", "geosite-netflix", "", "美国", `["geosite-netflix"]`, "", "", ""),
				migPol("legacy-geosite-youtube", "geosite-youtube", "", "", `["geosite-youtube"]`, "", "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"空 categories 且没有 policies 不写 policies", `{"categories":[],"fallbackDefault":"direct"}`, `{"fallbackDefault":"direct"}`, true, nil},
		{"categories 全是废项也不写 policies", `{"categories":[{"ruleset":" "},5],"fallbackDefault":"direct"}`, `{"fallbackDefault":"direct"}`, true, nil},
		{"空 policies 加非空 categories 要翻译", `{"categories":[{"ruleset":"geosite-a"}],"policies":[],"fallbackDefault":"direct"}`,
			`{"policies":` + migPols(migPol("legacy-geosite-a", "geosite-a", "", "", `["geosite-a"]`, "", "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"空 policies 加空 categories 保持空数组", `{"categories":[],"policies":[],"fallbackDefault":"direct"}`, `{"policies":[],"fallbackDefault":"direct"}`, true, nil},
		{"有 policies 时 categories 只删不译", `{"categories":[{"ruleset":"geosite-a"}],"policies":[{"id":"u","name":"U","rulesets":["geosite-u"]}],"fallbackDefault":"direct"}`,
			`{"policies":[{"id":"u","name":"U","rulesets":["geosite-u"]}],"fallbackDefault":"direct"}`, true, nil},
		{"用户的 policies 不归一化、形状错的项也留着", `{"policies":[1,null,{"name":"keep"}],"adBlock":true}`, `{"policies":[1,null,{"name":"keep"}]}`, true, nil},

		// ---- directRulesets ----
		{"directRulesets 去掉 cn 后合成始终直连", `{"directRulesets":["geosite-cn","geoip-cn","geosite-private"," geoip-lan ",5,""],"fallbackDefault":"direct"}`,
			`{"policies":` + migPols(migPol("legacy-direct", "始终直连", "", "direct", `["geosite-private","geoip-lan"]`, "", "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"directRulesets 只有 cn 不译", `{"directRulesets":["geosite-cn","geoip-cn"],"fallbackDefault":"direct"}`, `{"fallbackDefault":"direct"}`, true, nil},
		{"空 policies 加只有 cn 的 directRulesets 保持空数组", `{"directRulesets":["geosite-cn","geoip-cn"],"policies":[],"fallbackDefault":"direct"}`,
			`{"policies":[],"fallbackDefault":"direct"}`, true, nil},
		{"directRulesets 有非字符串项算没迁过但译不出东西", `{"directRulesets":["geosite-cn",5],"policies":[],"fallbackDefault":"direct"}`,
			`{"policies":[],"fallbackDefault":"direct"}`, true, nil},
		{"directRulesets 有重复的 cn 之外项", `{"directRulesets":["geosite-x","geosite-x"],"fallbackDefault":"direct"}`,
			`{"policies":` + migPols(migPol("legacy-direct", "始终直连", "", "direct", `["geosite-x"]`, "", "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"categories 在前、始终直连在后", `{"categories":[{"ruleset":"geosite-a","target":"proxy"}],"directRulesets":["geoip-cn","geosite-b"],"fallbackDefault":"direct"}`,
			`{"policies":` + migPols(
				migPol("legacy-geosite-a", "geosite-a", "", "proxy", `["geosite-a"]`, "", "", ""),
				migPol("legacy-direct", "始终直连", "", "direct", `["geosite-b"]`, "", "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"directRulesets 不是数组", `{"directRulesets":"geosite-x","fallbackDefault":"direct"}`, `{"fallbackDefault":"direct"}`, true, nil},

		// ---- 地区层:内置三个地区 ----
		{"regionId cn", `{"regionId":"cn"}`, `{"policies":` + migPols(cnDirect) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"regionMode CN", `{"regionMode":"CN"}`, `{"policies":` + migPols(cnDirect) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"regionMode HKMO 没有规则只给兜底", `{"regionMode":"HKMO"}`, `{"fallbackDefault":"direct"}`, true, nil},
		{"regionMode OTHER 走代理那份", `{"regionMode":"OTHER"}`,
			`{"policies":` + migPols(migPol("region-other-proxy", "其他地区·代理", migIcon, "proxy", `["geosite-cn","geoip-cn"]`, "", "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"regionId 优先于 regionMode", `{"regionId":"hkmo","regionMode":"CN"}`, `{"fallbackDefault":"direct"}`, true, nil},
		{"regionId 空白时看 regionMode", `{"regionId":" ","regionMode":"OTHER"}`,
			`{"policies":` + migPols(migPol("region-other-proxy", "其他地区·代理", migIcon, "proxy", `["geosite-cn","geoip-cn"]`, "", "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"regionId 前后空白去掉再匹配", `{"regionId":" hkmo "}`, `{"fallbackDefault":"direct"}`, true, nil},
		{"regionId 查不到退第一个", `{"regionId":"zzz"}`, `{"policies":` + migPols(cnDirect) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"regionId 大小写敏感", `{"regionId":"HKMO"}`, `{"policies":` + migPols(cnDirect) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"regionMode 不认识退第一个", `{"regionMode":"XX"}`, `{"policies":` + migPols(cnDirect) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"regionMode 小写不认", `{"regionMode":"other"}`, `{"policies":` + migPols(cnDirect) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"regions 为空数组用内置", `{"regions":[],"regionId":"other"}`,
			`{"policies":` + migPols(migPol("region-other-proxy", "其他地区·代理", migIcon, "proxy", `["geosite-cn","geoip-cn"]`, "", "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"regions 不是数组用内置", `{"regions":"x","regionId":"hkmo"}`, `{"fallbackDefault":"direct"}`, true, nil},

		// ---- 地区层:自己写的地区 ----
		{"自定义地区拆直连 / 代理两份", `{"regions":[{"id":"r1","name":"家里","rules":[` +
			`{"type":"domainSuffix","value":"lan","action":"direct"},{"type":"ipcidr","value":"10.0.0.0/8"},` +
			`{"type":"ipCidr","value":"192.168.0.0/16","action":"proxy"},{"type":"domain","value":"a.com","action":"bogus"},` +
			`{"type":"geoip","value":"private","action":"direct"},{"type":"nope","value":"x"},{"type":"geosite","value":" "}],"catchAll":"proxy"}],"regionId":"r1"}`,
			`{"policies":` + migPols(
				migPol("region-r1-direct", "家里·直连", migIcon, "direct", `["geoip-private"]`, `["a.com"]`, `["lan"]`, `["10.0.0.0/8"]`),
				migPol("region-r1-proxy", "家里·代理", migIcon, "proxy", "", "", "", `["192.168.0.0/16"]`)) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"域名不去重、规则集去重", `{"regions":[{"id":"d","name":"重复","rules":[{"type":"domain","value":"a.com"},{"type":"domain","value":"a.com"},{"type":"geosite","value":"x"},{"type":"geosite","value":"x"}],"catchAll":"direct"}],"regionId":"d"}`,
			`{"policies":` + migPols(migPol("region-d-direct", "重复·直连", migIcon, "direct", `["geosite-x"]`, `["a.com","a.com"]`, "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"地区名前后空白去掉", `{"regions":[{"id":"s","name":" 家 ","rules":[{"type":"domain","value":"h.lan"}]}],"regionId":"s"}`,
			`{"policies":` + migPols(migPol("region-s-direct", "家·直连", migIcon, "direct", "", `["h.lan"]`, "", "")) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"没有 regionId 与 regionMode 用 regions 第一个", `{"regions":[{"id":"a","name":"A","rules":[{"type":"geosite","value":"x"}]}]}`,
			`{"policies":` + migPols(migPol("region-a-direct", "A·直连", migIcon, "direct", `["geosite-x"]`, "", "", "")) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"选第二个地区", `{"regions":[{"id":"a","name":"A","rules":[{"type":"domain","value":"a.com"}]},{"id":"b","name":"B","rules":[{"type":"domain","value":"b.com"}],"catchAll":"direct"}],"regionId":"b"}`,
			`{"policies":` + migPols(migPol("region-b-direct", "B·直连", migIcon, "direct", "", `["b.com"]`, "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"id 重复取第一个", `{"regions":[{"id":"x","name":"一","rules":[{"type":"domain","value":"1.com"}]},{"id":"x","name":"二","rules":[{"type":"domain","value":"2.com"}]}],"regionId":"x"}`,
			`{"policies":` + migPols(migPol("region-x-direct", "一·直连", migIcon, "direct", "", `["1.com"]`, "", "")) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"regionMode 在自定义地区里按 id 找", `{"regions":[{"id":"cn","name":"C","rules":[{"type":"domain","value":"c.com"}]},{"id":"other","name":"O","rules":[{"type":"domain","value":"o.com","action":"proxy"}],"catchAll":"direct"}],"regionMode":"OTHER"}`,
			`{"policies":` + migPols(migPol("region-other-proxy", "O·代理", migIcon, "proxy", "", `["o.com"]`, "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"regionMode CN 在自定义地区里按 id 找", `{"regions":[{"id":"a","name":"A","rules":[{"type":"domain","value":"a.com"}]},{"id":"cn","name":"C","rules":[{"type":"domain","value":"c.com"}],"catchAll":"direct"}],"regionMode":"CN"}`,
			`{"policies":` + migPols(migPol("region-cn-direct", "C·直连", migIcon, "direct", "", `["c.com"]`, "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"regionMode HKMO 在自定义地区里按 id 找", `{"regions":[{"id":"a","name":"A","rules":[{"type":"domain","value":"a.com"}]},{"id":"hkmo","name":"H","rules":[{"type":"domain","value":"h.com"}],"catchAll":"direct"}],"regionMode":"HKMO"}`,
			`{"policies":` + migPols(migPol("region-hkmo-direct", "H·直连", migIcon, "direct", "", `["h.com"]`, "", "")) + `,"fallbackDefault":"direct"}`, true, nil},
		{"没写 id 的地区对不上 regionId 退第一个", `{"regions":[{"id":"a","name":"A","rules":[{"type":"geosite","value":"x"}]},{"name":"B","rules":[{"type":"geosite","value":"y"}],"catchAll":"direct"}],"regionId":"region-1"}`,
			`{"policies":` + migPols(migPol("region-a-direct", "A·直连", migIcon, "direct", `["geosite-x"]`, "", "", "")) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"选中的地区没写 id 和名字用缺省", `{"regions":[{"rules":[{"type":"domain","value":"a.com"}]}]}`,
			`{"policies":` + migPols(migPol("region-region-0-direct", "地区-1·直连", migIcon, "direct", "", `["a.com"]`, "", "")) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"地区不是对象也能当一个空地区", `{"regions":[1]}`, `{"fallbackDefault":"proxy"}`, true, nil},
		{"老式地区:rulesets 加 target 拆 geosite / geoip", `{"regions":[{"id":"old","name":"旧","rulesets":["geosite-cn","ads","geoip-lan"," geoip-cn ","geosite-geoip-x",5],"target":"proxy"}],"regionId":"old"}`,
			`{"policies":` + migPols(migPol("region-old-proxy", "旧·代理", migIcon, "proxy", `["geosite-cn","geosite-ads","geoip-lan","geoip-cn","geosite-geoip-x"]`, "", "", "")) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"老式地区:target 缺省或写错按直连", `{"regions":[{"id":"o1","name":"甲","rulesets":["geosite-a"],"target":" proxy "}],"regionId":"o1"}`,
			`{"policies":` + migPols(migPol("region-o1-direct", "甲·直连", migIcon, "direct", `["geosite-a"]`, "", "", "")) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"rules 是数组时不再看老式 rulesets", `{"regions":[{"id":"n","name":"新","rules":[],"rulesets":["geosite-a"]}],"regionId":"n"}`, `{"fallbackDefault":"proxy"}`, true, nil},
		{"rules 是 null 时看老式 rulesets", `{"regions":[{"id":"n","name":"新","rules":null,"rulesets":["geosite-a"]}],"regionId":"n"}`,
			`{"policies":` + migPols(migPol("region-n-direct", "新·直连", migIcon, "direct", `["geosite-a"]`, "", "", "")) + `,"fallbackDefault":"proxy"}`, true, nil},

		// ---- fallbackDefault 是「地区层已迁过」的标记 ----
		{"fallbackDefault 已有值不再翻地区", `{"regionId":"cn","fallbackDefault":"direct"}`, `{"fallbackDefault":"direct"}`, true, nil},
		{"fallbackDefault 空白当没写", `{"regionId":"cn","fallbackDefault":"  "}`, `{"policies":` + migPols(cnDirect) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"fallbackDefault 非字符串当没写", `{"regionId":"hkmo","fallbackDefault":5}`, `{"fallbackDefault":"direct"}`, true, nil},
		{"地区站点集排在 categories 后、用户站点集后", `{"categories":[{"ruleset":"geosite-a"}],"regionId":"cn"}`,
			`{"policies":` + migPols(migPol("legacy-geosite-a", "geosite-a", "", "", `["geosite-a"]`, "", "", ""), cnDirect) + `,"fallbackDefault":"proxy"}`, true, nil},
		{"用户的 policies 在前、地区站点集追加", `{"policies":[{"id":"u","name":"U"}],"regionId":"cn"}`,
			`{"policies":[{"id":"u","name":"U"},` + cnDirect + `],"fallbackDefault":"proxy"}`, true, nil},

		// ---- 随包规则库里已没有的规则集 ----
		{"地区站点集里缺的规则集进 missingRulesets", `{"regionId":"cn"}`,
			`{"policies":` + migPols(strings.Replace(migPol("region-cn-direct", "中国大陆·直连", migIcon, "direct", `["geosite-cn"]`, "", "", ""),
				`"notes":{}`, `"notes":{},"missingRulesets":["geoip-cn"]`, 1)) + `,"fallbackDefault":"proxy"}`, true, []string{"geosite-cn"}},
		{"categories 的规则集不在名单里:站点集照出、规则集清空", `{"categories":[{"ruleset":"geosite-x","target":"美国"}],"fallbackDefault":"direct"}`,
			`{"policies":` + migPols(strings.Replace(migPol("legacy-geosite-x", "geosite-x", "", "美国", `[]`, "", "", ""),
				`"notes":{}`, `"notes":{},"missingRulesets":["geosite-x"]`, 1)) + `,"fallbackDefault":"direct"}`, true, []string{"geosite-y"}},
		{"名单只管 geoip- / geosite- 前缀", `{"categories":[{"ruleset":"list-1234abcd"}],"fallbackDefault":"direct"}`,
			`{"policies":` + migPols(migPol("legacy-list-1234abcd", "list-1234abcd", "", "", `["list-1234abcd"]`, "", "", "")) + `,"fallbackDefault":"direct"}`, true, []string{}},
	}
	for _, c := range cases {
		got, changed := migRun(t, c.in, c.geo)
		if changed != c.changed {
			t.Errorf("%s: changed=%v 要 %v", c.name, changed, c.changed)
		}
		if w := mustJSON(t, c.want); !reflect.DeepEqual(got, w) {
			gb, _ := json.Marshal(got)
			t.Errorf("%s 输出不一致\n输入: %s\n有: %s\n要: %s", c.name, c.in, gb, c.want)
			continue
		}
		// 幂等:迁完的结果再迁一次,不再有老字段,changed 为假,内容不变
		if c.geo == nil {
			gb, _ := json.Marshal(got)
			again, changed2 := migRun(t, string(gb), nil)
			if changed2 || !reflect.DeepEqual(again, got) {
				t.Errorf("%s: 迁移不幂等(changed=%v)", c.name, changed2)
			}
		}
	}
}

func TestPolicyToAny(t *testing.T) {
	p := Policy{
		ID: "i", Name: "n", Icon: "c", Default: "proxy", Enabled: false,
		RuleUrls: []string{"https://e.com/a"}, Rulesets: []string{"geosite-a", "list-1"},
		Domain: []string{"a.com"}, DomainSuffix: []string{"b.com"}, DomainKeyword: []string{"kw"}, IPCidr: []string{"1.0.0.0/8"},
		Notes: map[string]string{"geosite-a": "备注"},
	}
	m := policyToAny(p)
	got := roundTrip(t, m)
	want := mustJSON(t, `{"id":"i","name":"n","icon":"c","default":"proxy","enabled":false,"ruleUrls":["https://e.com/a"],"rulesets":["geosite-a","list-1"],`+
		`"domain":["a.com"],"domainSuffix":["b.com"],"domainKeyword":["kw"],"ipCidr":["1.0.0.0/8"],"notes":{"geosite-a":"备注"}}`)
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.Marshal(got)
		t.Errorf("policyToAny 不一致\n有: %s", gb)
	}
	// notes 是拷贝,改产物不影响原站点集
	m["notes"].(map[string]any)["x"] = "y"
	if _, ok := p.Notes["x"]; ok {
		t.Error("notes 没拷贝,改产物写穿了原站点集")
	}
	// 缺的规则集非空才带键
	p.MissingRulesets = []string{"geoip-cn"}
	if got := roundTrip(t, policyToAny(p)).(map[string]any)["missingRulesets"]; !reflect.DeepEqual(got, []any{"geoip-cn"}) {
		t.Errorf("missingRulesets=%v 要 [geoip-cn]", got)
	}
	// 空列表序列化成 [] 不是 null;空 notes 是 {}
	b, _ := json.Marshal(policyToAny(Policy{}))
	wantEmpty := `{"default":"","domain":[],"domainKeyword":[],"domainSuffix":[],"enabled":false,"icon":"","id":"","ipCidr":[],"name":"","notes":{},"ruleUrls":[],"rulesets":[]}`
	if string(b) != wantEmpty {
		t.Errorf("空站点集序列化\n有: %s\n要: %s", b, wantEmpty)
	}
}

func TestNormalizeRegionRule(t *testing.T) {
	for _, c := range []struct {
		in   string
		ok   bool
		want regionRule
	}{
		{`{"type":"geosite","value":"cn"}`, true, regionRule{"geosite", "cn", "direct"}},
		{`{"type":"geoip","value":" x ","action":"proxy"}`, true, regionRule{"geoip", "x", "proxy"}},
		{`{"type":"domain","value":"a.com","action":"direct"}`, true, regionRule{"domain", "a.com", "direct"}},
		{`{"type":"domainSuffix","value":"a.com"}`, true, regionRule{"domainSuffix", "a.com", "direct"}},
		{`{"type":"ipcidr","value":"1.1.1.0/24","action":"proxy"}`, true, regionRule{"ipcidr", "1.1.1.0/24", "proxy"}},
		{`{"type":"ipCidr","value":"1.1.1.0/24"}`, true, regionRule{"ipCidr", "1.1.1.0/24", "direct"}},
		{`{"type":"domain","value":"a.com","action":"PROXY"}`, true, regionRule{"domain", "a.com", "direct"}},
		{`{"type":"domain","value":"a.com","action":5}`, true, regionRule{"domain", "a.com", "direct"}},
		{`{"type":"domain","value":"a.com","action":"block"}`, true, regionRule{"domain", "a.com", "direct"}},
		{`{"type":"domainKeyword","value":"a"}`, false, regionRule{}},
		{`{"type":"GEOSITE","value":"a"}`, false, regionRule{}},
		{`{"type":"port","value":"80"}`, false, regionRule{}},
		{`{"type":5,"value":"a"}`, false, regionRule{}},
		{`{"value":"a"}`, false, regionRule{}},
		{`{"type":"geosite"}`, false, regionRule{}},
		{`{"type":"geosite","value":""}`, false, regionRule{}},
		{`{"type":"geosite","value":"  "}`, false, regionRule{}},
		{`{"type":"geosite","value":5}`, false, regionRule{}},
		{`"geosite"`, false, regionRule{}},
		{`null`, false, regionRule{}},
		{`[]`, false, regionRule{}},
	} {
		got, ok := normalizeRegionRule(mustJSON(t, c.in))
		if ok != c.ok || got != c.want {
			t.Errorf("%s → %v %v 要 %v %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestRegionRuleTagAndTarget(t *testing.T) {
	for _, c := range []struct {
		r    regionRule
		want string
	}{
		{regionRule{"geosite", "cn", "direct"}, "geosite-cn"},
		{regionRule{"geoip", "cn", "direct"}, "geoip-cn"},
		{regionRule{"domain", "a.com", "direct"}, ""},
		{regionRule{"domainSuffix", "a.com", "direct"}, ""},
		{regionRule{"ipcidr", "1.0.0.0/8", "direct"}, ""},
		{regionRule{"ipCidr", "1.0.0.0/8", "direct"}, ""},
	} {
		if got := regionRuleTag(c.r); got != c.want {
			t.Errorf("regionRuleTag(%v)=%q 要 %q", c.r, got, c.want)
		}
	}
	for _, c := range []struct {
		in   any
		want string
		ok   bool
	}{
		{"direct", "direct", true}, {"proxy", "proxy", true}, {"Direct", "", false}, {" proxy", "", false},
		{"", "", false}, {"block", "", false}, {nil, "", false}, {true, "", false}, {float64(1), "", false}, {[]any{"proxy"}, "", false},
	} {
		got, ok := targetOf(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("targetOf(%v)=%q %v 要 %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func migRegionStr(r region) string {
	var rs []string
	for _, x := range r.rules {
		rs = append(rs, x.typ+":"+x.value+":"+x.action)
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s", r.id, r.name, r.matchID, r.catchAll, strings.Join(rs, ","))
}

func TestNormalizeRegion(t *testing.T) {
	for _, c := range []struct {
		name  string
		in    string
		index int
		want  string // id|name|matchID|catchAll|rules
	}{
		{"全空", `{}`, 2, "region-2|地区-3||proxy|"},
		{"null", `null`, 0, "region-0|地区-1||proxy|"},
		{"id 与名字去空白", `{"id":" a ","name":" N "}`, 0, "a|N|a|proxy|"},
		{"id 非字符串", `{"id":5,"name":["x"]}`, 1, "region-1|地区-2||proxy|"},
		{"id 空白", `{"id":"  ","name":""}`, 4, "region-4|地区-5||proxy|"},
		{"rules 数组里的废项丢掉", `{"id":"r","rules":[{"type":"geosite","value":"a"},{"type":"x","value":"b"},1,{"type":"domain","value":"c","action":"proxy"}]}`, 0,
			"r|地区-1|r|proxy|geosite:a:direct,domain:c:proxy"},
		{"rules 空数组压过老式 rulesets", `{"id":"r","rules":[],"rulesets":["geosite-a"]}`, 0, "r|地区-1|r|proxy|"},
		{"rules 非数组走老式 rulesets", `{"id":"r","rules":{},"rulesets":["geosite-a"]}`, 0, "r|地区-1|r|proxy|geosite:a:direct"},
		{"老式 rulesets 拆前缀并按 target", `{"id":"r","rulesets":["geosite-cn","geoip-cn","ads","geoip-lan"," geoip-x ","geosite-geoip-y",7,""],"target":"proxy"}`, 0,
			"r|地区-1|r|proxy|geosite:cn:proxy,geoip:cn:proxy,geosite:ads:proxy,geoip:lan:proxy,geoip:x:proxy,geosite:geoip-y:proxy"},
		{"老式 rulesets 前缀必须带横线才算 geoip", `{"id":"r","rulesets":["geoipx","geoip","geoip-lan"]}`, 0,
			"r|地区-1|r|proxy|geosite:geoipx:direct,geosite:geoip:direct,geoip:lan:direct"},
		{"老式 target 缺省按直连", `{"id":"r","rulesets":["geosite-a"]}`, 0, "r|地区-1|r|proxy|geosite:a:direct"},
		{"老式 target 写错按直连", `{"id":"r","rulesets":["geosite-a"],"target":"PROXY"}`, 0, "r|地区-1|r|proxy|geosite:a:direct"},
		{"老式 target 带空白按直连", `{"id":"r","rulesets":["geosite-a"],"target":" proxy "}`, 0, "r|地区-1|r|proxy|geosite:a:direct"},
		{"catchAll direct", `{"catchAll":"direct"}`, 0, "region-0|地区-1||direct|"},
		{"catchAll proxy", `{"catchAll":"proxy"}`, 0, "region-0|地区-1||proxy|"},
		{"catchAll 在前", `{"catchAll":"direct","fallback":"proxy"}`, 0, "region-0|地区-1||direct|"},
		{"catchAll 在前(反过来)", `{"catchAll":"proxy","fallback":"direct"}`, 0, "region-0|地区-1||proxy|"},
		{"catchAll 写错但是真值按直连", `{"catchAll":"bogus"}`, 0, "region-0|地区-1||direct|"},
		{"catchAll 大写按直连", `{"catchAll":"PROXY"}`, 0, "region-0|地区-1||direct|"},
		{"catchAll 写错但是真值时不再看 fallback", `{"catchAll":"bogus","fallback":"proxy"}`, 0, "region-0|地区-1||direct|"},
		{"catchAll 空串看 fallback", `{"catchAll":"","fallback":"proxy"}`, 0, "region-0|地区-1||proxy|"},
		{"catchAll 空串看 fallback direct", `{"catchAll":"","fallback":"direct"}`, 0, "region-0|地区-1||direct|"},
		{"catchAll null 看 fallback 写错", `{"catchAll":null,"fallback":"bogus"}`, 0, "region-0|地区-1||direct|"},
		{"catchAll 0 当没写", `{"catchAll":0}`, 0, "region-0|地区-1||proxy|"},
		{"catchAll false 且 fallback false", `{"catchAll":false,"fallback":false}`, 0, "region-0|地区-1||proxy|"},
		{"catchAll true 按直连", `{"catchAll":true}`, 0, "region-0|地区-1||direct|"},
		{"catchAll 1 按直连", `{"catchAll":1}`, 0, "region-0|地区-1||direct|"},
		{"catchAll 空数组是真值", `{"catchAll":[]}`, 0, "region-0|地区-1||direct|"},
		{"catchAll 空对象是真值", `{"catchAll":{}}`, 0, "region-0|地区-1||direct|"},
		{"只有 fallback", `{"fallback":"direct"}`, 0, "region-0|地区-1||direct|"},
	} {
		if got := migRegionStr(normalizeRegion(mustJSON(t, c.in), c.index)); got != c.want {
			t.Errorf("%s:\n有: %s\n要: %s", c.name, got, c.want)
		}
	}
}

func TestMigrateRegionDirect(t *testing.T) {
	setAvailableGeoTags(nil)
	// 没有任何地区信息:什么都不翻、兜底为空
	for _, in := range []string{`{}`, `{"regions":[]}`, `{"regions":"x"}`, `{"regionId":" ","regionMode":5}`, `{"regionId":5}`} {
		ps, fb := migrateRegion(mustJSON(t, in).(map[string]any))
		if len(ps) != 0 || fb != "" {
			t.Errorf("%s → %d 个站点集、兜底 %q 要 0 个、空", in, len(ps), fb)
		}
	}
	// regionId 写了但不是字符串、regionMode 非空:也走翻译
	ps, fb := migrateRegion(mustJSON(t, `{"regionMode":"HKMO"}`).(map[string]any))
	if len(ps) != 0 || fb != "direct" {
		t.Errorf("HKMO → %d 个、兜底 %q 要 0 个、direct", len(ps), fb)
	}
	// 站点集下标:直连在前、代理在后,第二个的序号是 1(只影响没写 id / 名字时的缺省,这里 id 名字都是给定的)
	ps, _ = migrateRegion(mustJSON(t, `{"regions":[{"id":"r","name":"R","rules":[{"type":"domain","value":"a"},{"type":"domain","value":"b","action":"proxy"}]}],"regionId":"r"}`).(map[string]any))
	if len(ps) != 2 || ps[0].ID != "region-r-direct" || ps[1].ID != "region-r-proxy" || ps[0].Default != "direct" || ps[1].Default != "proxy" {
		t.Errorf("直连在前代理在后:%+v", ps)
	}
}

func TestMigrateHandler(t *testing.T) {
	reg := protocol.NewRegistry()
	Register(reg)
	req := func(args string, geo string) []byte {
		return []byte(`{"protocol":1,"method":"migrateStoredRouting","args":` + args + geo + `}`)
	}
	run := func(args, geo string) any { return call(t, reg, req(args, geo)) }
	check := func(name string, got any, want string) {
		t.Helper()
		if w := mustJSON(t, want); !reflect.DeepEqual(got, w) {
			gb, _ := json.Marshal(got)
			t.Errorf("%s 不一致\n有: %s\n要: %s", name, gb, want)
		}
	}
	check("缺参数", run(`[]`, ``), `{"routing":{},"changed":false}`)
	check("null", run(`[null]`, ``), `{"routing":{},"changed":false}`)
	check("数组", run(`[[1]]`, ``), `{"routing":{},"changed":false}`)
	check("无老字段", run(`[{"policies":[]}]`, ``), `{"routing":{"policies":[]},"changed":false}`)
	check("有老字段", run(`[{"proxyTag":"p","regionId":"hkmo"}]`, ``), `{"routing":{"fallbackDefault":"direct"},"changed":true}`)
	cn := migPol("region-cn-direct", "中国大陆·直连", migIcon, "direct", `["geosite-cn","geoip-cn"]`, "", "", "")
	check("无名单", run(`[{"regionId":"cn"}]`, ``), `{"routing":{"policies":[`+cn+`],"fallbackDefault":"proxy"},"changed":true}`)
	// 请求里带规则库名单:缺的规则集进 missingRulesets;下一次没带名单的调用不受影响
	miss := strings.Replace(migPol("region-cn-direct", "中国大陆·直连", migIcon, "direct", `["geosite-cn"]`, "", "", ""),
		`"notes":{}`, `"notes":{},"missingRulesets":["geoip-cn"]`, 1)
	check("带名单", run(`[{"regionId":"cn"}]`, `,"geoTags":["geosite-cn"]`), `{"routing":{"policies":[`+miss+`],"fallbackDefault":"proxy"},"changed":true}`)
	check("名单不串到下一次", run(`[{"regionId":"cn"}]`, ``), `{"routing":{"policies":[`+cn+`],"fallbackDefault":"proxy"},"changed":true}`)
}
