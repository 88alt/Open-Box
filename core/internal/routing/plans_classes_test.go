package routing

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/core/internal/protocol"
)

// 自写测试向量:classes(policyClasses / classOf)以及 plans.go 里的两个小工具 jsTruthy / cloneStrs。
// 输入自己拟,期望值按规则人工推算:
//   - classOf:选中的出站恰好等于当前直连名 → direct,等于当前拒绝名 → block,其余一律 proxy;先判直连再判拒绝;
//   - policyClasses:键是「生效的站点集名 + 兜底名」,停用的 / 没条件的 / 叫「其他」或叫兜底名的站点集不在里面;
//     值取站点集此刻选中的出站(selections 里有就顺着下钻到叶子,没有就看 default 落到成员表哪一项);同名站点集后写的赢;
//   - 内置出站改名后只认新名字,老记号 direct / block 只是普通名字。

func clsNoGeo() { setAvailableGeoTags(nil) }

const clsMembers = `["direct","block","美国","日本"]`

// clsRun 直接调 policyClasses;空串表示用缺省值(selections 缺省 {},内置缺省 nil)。
func clsRun(t *testing.T, routing, members, builtin, sel string) any {
	t.Helper()
	clsNoGeo()
	if routing == "" {
		routing = `{}`
	}
	if members == "" {
		members = clsMembers
	}
	if builtin == "" {
		builtin = `null`
	}
	if sel == "" {
		sel = `{}`
	}
	got := policyClasses(mustJSON(t, routing), parseMembers(mustJSON(t, members)), parseBuiltin(mustJSON(t, builtin)), parseSelections(mustJSON(t, sel)))
	return roundTrip(t, got)
}

func clsCheck(t *testing.T, name string, got any, want string) {
	t.Helper()
	w := mustJSON(t, want)
	if !reflect.DeepEqual(got, w) {
		gb, _ := json.Marshal(got)
		t.Errorf("%s 输出不一致\n有: %s\n要: %s", name, gb, want)
	}
}

func TestClassOf(t *testing.T) {
	def := defaultBuiltin
	renamed := builtinTags{"直连", "拒绝", true, true}
	same := builtinTags{"", "", false, false} // 内置对象缺键:两个名字都是空串(parseBuiltin 不补默认)
	for _, c := range []struct {
		name   string
		chosen string
		b      builtinTags
		want   string
	}{
		{"缺省:direct", "direct", def, "direct"},
		{"缺省:block", "block", def, "block"},
		{"缺省:节点", "美国", def, "proxy"},
		{"缺省:空串", "", def, "proxy"},
		{"缺省:大小写不同的 Direct 不算", "Direct", def, "proxy"},
		{"缺省:中文名「直连」只是普通名字", "直连", def, "proxy"},
		{"改名:新直连名", "直连", renamed, "direct"},
		{"改名:老记号 direct 变成普通名字", "direct", renamed, "proxy"},
		{"改名:新拒绝名", "拒绝", renamed, "block"},
		{"改名:老记号 block 变成普通名字", "block", renamed, "proxy"},
		{"两个名字都是空串时,空串先判成直连", "", same, "direct"},
		{"两个名字都是空串时,别的名字还是 proxy", "美国", same, "proxy"},
	} {
		if got := classOf(c.chosen, c.b); got != c.want {
			t.Errorf("%s: classOf(%q)=%q 要 %q", c.name, c.chosen, got, c.want)
		}
	}
}

func TestPolicyClassesBasics(t *testing.T) {
	// 站点集此刻各走哪一类;停用的、没条件的、叫「其他」的都不在结果里,兜底「其他」固定在
	routing := `{"fallbackDefault":"direct","policies":[
		{"name":"流媒体","default":"美国","domain":["a.example"]},
		{"name":"下载","default":"direct","domainSuffix":["b.example"]},
		{"name":"广告","default":"block","rulesets":["geosite-ads"]},
		{"name":"办公","default":"","ipCidr":["10.0.0.0/8"]},
		{"name":"停用","default":"美国","enabled":false,"domain":["c.example"]},
		{"name":"没条件","default":"美国"},
		{"name":"其他","default":"美国","domain":["d.example"]}
	]}`
	clsCheck(t, "各类出口", clsRun(t, routing, "", "", ""),
		`{"流媒体":"proxy","下载":"direct","广告":"block","办公":"direct","其他":"direct"}`)

	// 成员表里没有拒绝:default 写 block 落到成员第一项
	clsCheck(t, "成员表里没有拒绝时落到第一项", clsRun(t, routing, `["direct","美国"]`, "", ""),
		`{"流媒体":"proxy","下载":"direct","广告":"direct","办公":"direct","其他":"direct"}`)
	// 成员第一项是节点:default 空着 / 指向不存在的组都落到它
	clsCheck(t, "default 空着落到成员第一项(节点)",
		clsRun(t, `{"fallbackDefault":"","policies":[{"name":"A","default":"","domain":["a.example"]},{"name":"B","default":"ghost","domain":["b.example"]}]}`, `["美国","direct"]`, "", ""),
		`{"A":"proxy","B":"proxy","其他":"proxy"}`)
	// 成员表为空:选不出任何出站,按 proxy
	clsCheck(t, "成员表为空一律 proxy", clsRun(t, routing, `[]`, "", ""),
		`{"流媒体":"proxy","下载":"proxy","广告":"proxy","办公":"proxy","其他":"proxy"}`)
	// proxy 记号:成员里第一个不是直连也不是拒绝的
	clsCheck(t, "default=proxy 取第一个非内置成员",
		clsRun(t, `{"fallbackDefault":"proxy","policies":[{"name":"A","default":"proxy","domain":["a.example"]}]}`, `["direct","block","日本","美国"]`, "", ""),
		`{"A":"proxy","其他":"proxy"}`)
	clsCheck(t, "default=proxy 但成员里只有内置:落到第一项",
		clsRun(t, `{"fallbackDefault":"proxy","policies":[{"name":"A","default":"proxy","domain":["a.example"]}]}`, `["direct","block"]`, "", ""),
		`{"A":"direct","其他":"direct"}`)
}

func TestPolicyClassesNames(t *testing.T) {
	// 兜底改名:键跟着改;站点集叫「其他」仍然丢(常量),叫兜底名的也丢
	clsCheck(t, "兜底改名",
		clsRun(t, `{"fallbackName":"漏网","fallbackDefault":"美国","policies":[
			{"name":"其他","default":"美国","domain":["a.example"]},
			{"name":"漏网","default":"美国","domain":["b.example"]},
			{"name":"正常","default":"direct","domain":["c.example"]}]}`, "", "", ""),
		`{"正常":"direct","漏网":"proxy"}`)
	// 同名站点集:后写的赢
	clsCheck(t, "同名后写的赢",
		clsRun(t, `{"fallbackDefault":"direct","policies":[
			{"name":"重名","default":"direct","domain":["a.example"]},
			{"name":"重名","default":"美国","domain":["b.example"]}]}`, "", "", ""),
		`{"重名":"proxy","其他":"direct"}`)
	// 空 / 非对象的分流档案:只有兜底
	for _, in := range []string{`null`, `5`, `[]`, `"x"`, `{}`} {
		clsCheck(t, "无站点集的档案 "+in, clsRun(t, in, "", "", ""), `{"其他":"direct"}`)
	}
	clsCheck(t, "无站点集且成员为空", clsRun(t, `{}`, `[]`, "", ""), `{"其他":"proxy"}`)
	// 没写 name 的站点集按序号补「策略-N」
	clsCheck(t, "缺名字按序号补", clsRun(t, `{"fallbackDefault":"direct","policies":[{"default":"美国","domain":["a.example"]},{"name":"  ","default":"direct","domain":["b.example"]}]}`, "", "", ""),
		`{"策略-1":"proxy","策略-2":"direct","其他":"direct"}`)
}

func TestPolicyClassesSelections(t *testing.T) {
	routing := `{"fallbackDefault":"direct","policies":[
		{"name":"流媒体","default":"美国","domain":["a.example"]},
		{"name":"下载","default":"direct","domain":["b.example"]}]}`
	for _, c := range []struct{ name, sel, want string }{
		{"没有选择走 default", `{}`, `{"流媒体":"proxy","下载":"direct","其他":"direct"}`},
		{"选择盖过 default(直连改走节点)", `{"下载":"日本"}`, `{"流媒体":"proxy","下载":"proxy","其他":"direct"}`},
		{"选择盖过 default(节点改直连)", `{"流媒体":"direct"}`, `{"流媒体":"direct","下载":"direct","其他":"direct"}`},
		{"选择拒绝", `{"流媒体":"block"}`, `{"流媒体":"block","下载":"direct","其他":"direct"}`},
		{"兜底也能被选择盖过", `{"其他":"美国"}`, `{"流媒体":"proxy","下载":"direct","其他":"proxy"}`},
		{"顺着 selector 链下钻到叶子", `{"流媒体":"自动","自动":"direct"}`, `{"流媒体":"direct","下载":"direct","其他":"direct"}`},
		{"链到节点就是 proxy", `{"下载":"自动","自动":"美国"}`, `{"流媒体":"proxy","下载":"proxy","其他":"direct"}`},
		{"非字符串的选择值不等于任何出站名", `{"下载":123,"流媒体":null}`, `{"流媒体":"proxy","下载":"proxy","其他":"direct"}`},
		{"选择成环停在环上的名字,按 proxy", `{"下载":"甲","甲":"乙","乙":"甲"}`, `{"流媒体":"proxy","下载":"proxy","其他":"direct"}`},
		{"与站点集无关的选择不影响结果", `{"无关":"block"}`, `{"流媒体":"proxy","下载":"direct","其他":"direct"}`},
	} {
		clsCheck(t, c.name, clsRun(t, routing, "", "", c.sel), c.want)
	}
	// 选择是 JSON null / 数组 / 字符串:当没有选择
	for _, sel := range []string{`null`, `[]`, `"x"`, `5`} {
		clsCheck(t, "非对象的 selections "+sel, clsRun(t, routing, "", "", sel), `{"流媒体":"proxy","下载":"direct","其他":"direct"}`)
	}
}

func TestPolicyClassesRenamedBuiltins(t *testing.T) {
	routing := `{"fallbackDefault":"direct","policies":[
		{"name":"A","default":"direct","domain":["a.example"]},
		{"name":"B","default":"block","domain":["b.example"]},
		{"name":"C","default":"美国","domain":["c.example"]}]}`
	renamed := `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`
	members := `["直连","拒绝","美国"]`
	clsCheck(t, "改名后 default 的老记号照样指向新名字", clsRun(t, routing, members, renamed, ""),
		`{"A":"direct","B":"block","C":"proxy","其他":"direct"}`)
	clsCheck(t, "选择写新名字", clsRun(t, routing, members, renamed, `{"C":"拒绝","A":"直连"}`),
		`{"A":"direct","B":"block","C":"block","其他":"direct"}`)
	clsCheck(t, "选择写老记号 direct / block 变成普通名字", clsRun(t, routing, members, renamed, `{"C":"direct","A":"block"}`),
		`{"A":"proxy","B":"block","C":"proxy","其他":"direct"}`)
	// 没改名时成员里叫「直连」的不是内置直连:default=direct 在成员里找不到 direct,落到第一项「直连」,算节点
	clsCheck(t, "没改名时成员叫「直连」不算直连", clsRun(t, routing, members, "", ""),
		`{"A":"proxy","B":"proxy","C":"proxy","其他":"proxy"}`)
	// 内置对象缺键:direct / block 名字都是空串,default=direct 找不到成员,落到第一项,既不等于空串直连名也不等于拒绝名
	clsCheck(t, "内置对象缺键", clsRun(t, routing, `["direct","block","美国"]`, `{}`, ""),
		`{"A":"proxy","B":"proxy","C":"proxy","其他":"proxy"}`)
}

func TestPolicyClassesCustomDoesNotLeak(t *testing.T) {
	// 前置自定义分流不进 classes
	clsCheck(t, "自定义分流不出现",
		clsRun(t, `{"fallbackDefault":"direct","custom":{"enabled":true,"rules":[{"type":"domain","value":"x.example","outbound":"美国"}]},"policies":[{"name":"A","default":"美国","domain":["a.example"]}]}`, "", "", ""),
		`{"A":"proxy","其他":"direct"}`)
}

func TestPolicyClassesDoesNotMutateInput(t *testing.T) {
	routing := mustJSON(t, `{"fallbackDefault":"direct","policies":[{"name":"A","default":"美国","domain":["a.example"],"notes":{"a.example":"备注"}}]}`)
	before, _ := json.Marshal(routing)
	sel := map[string]any{"A": "direct"}
	selBefore, _ := json.Marshal(sel)
	_ = policyClasses(routing, []string{"direct", "美国"}, defaultBuiltin, sel)
	after, _ := json.Marshal(routing)
	selAfter, _ := json.Marshal(sel)
	if string(before) != string(after) || string(selBefore) != string(selAfter) {
		t.Errorf("入参被改写:\n%s\n%s", before, after)
	}
}

// 走完整的协议入口:缺省参数、非法形状参数的处理(planArgs)。
func TestClassesHandlerArgs(t *testing.T) {
	reg := protocol.NewRegistry()
	Register(reg)
	run := func(args string) any {
		t.Helper()
		return call(t, reg, []byte(`{"protocol":1,"method":"classes","args":`+args+`}`))
	}
	routing := `{"fallbackDefault":"美国","policies":[{"name":"A","default":"direct","domain":["a.example"]}]}`
	// 只给 routing:成员缺省 ["direct"],兜底 default=美国 不在成员里,落到第一项 direct
	clsCheck(t, "只给 routing", run(`[`+routing+`]`), `{"A":"direct","其他":"direct"}`)
	clsCheck(t, "成员显式 null 同缺省", run(`[`+routing+`,null]`), `{"A":"direct","其他":"direct"}`)
	clsCheck(t, "成员不是数组按缺省 [direct]", run(`[`+routing+`,"x"]`), `{"A":"direct","其他":"direct"}`)
	clsCheck(t, "成员给全", run(`[`+routing+`,["direct","美国"]]`), `{"A":"direct","其他":"proxy"}`)
	clsCheck(t, "成员里非字符串占位成空串,不会等于任何出站名", run(`[`+routing+`,[1,"direct"]]`), `{"A":"direct","其他":"proxy"}`)
	clsCheck(t, "内置不是对象按缺省", run(`[`+routing+`,["direct","美国"],"x"]`), `{"A":"direct","其他":"proxy"}`)
	clsCheck(t, "内置改名", run(`[`+routing+`,["直连","美国"],{"direct":"直连","block":"拒绝"}]`), `{"A":"direct","其他":"proxy"}`)
	clsCheck(t, "选择经协议带进来", run(`[`+routing+`,["direct","美国"],null,{"A":"美国"}]`), `{"A":"proxy","其他":"proxy"}`)
	clsCheck(t, "选择不是对象按没有", run(`[`+routing+`,["direct","美国"],null,[1]]`), `{"A":"direct","其他":"proxy"}`)
	// 没有任何参数:routing 是 undefined,当空档案
	clsCheck(t, "没有参数", run(`[]`), `{"其他":"direct"}`)
}

func TestJsTruthy(t *testing.T) {
	for _, c := range []struct {
		name string
		v    any
		want bool
	}{
		{"nil(undefined / null)", nil, false},
		{"false", false, false},
		{"true", true, true},
		{"0", float64(0), false},
		{"负零", math.Copysign(0, -1), false},
		{"NaN", math.NaN(), false},
		{"1", float64(1), true},
		{"-1", float64(-1), true},
		{"0.5", 0.5, true},
		{"正无穷", math.Inf(1), true},
		{"空串", "", false},
		{"字符串 0", "0", true},
		{"字符串 false", "false", true},
		{"空白串", " ", true},
		{"空数组", []any{}, true},
		{"空对象", map[string]any{}, true},
	} {
		if got := jsTruthy(c.v); got != c.want {
			t.Errorf("%s: jsTruthy(%#v)=%v 要 %v", c.name, c.v, got, c.want)
		}
	}
}

func TestCloneStrs(t *testing.T) {
	if got := cloneStrs(nil); got == nil || len(got) != 0 {
		t.Errorf("nil 应得到非 nil 空切片: %#v", got)
	}
	src := []string{"a", "b"}
	got := cloneStrs(src)
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("内容不对: %v", got)
	}
	got[0] = "x"
	if src[0] != "a" {
		t.Error("拷贝应与原切片互不影响")
	}
	if b, _ := json.Marshal(cloneStrs([]string{})); string(b) != "[]" {
		t.Errorf("空切片应序列化成 []: %s", b)
	}
}
