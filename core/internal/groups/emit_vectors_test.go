package groups

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 自写测试向量:emit.go 的 EmitUserGroups(含协议入口 emitGroups)。输入自己拟,期望值按下面的规则人工推算,JSON 逐字节比:
//   - 结果键序 outbounds dropped placeholders builtin internalTags publicTags failover;出站键序 type tag outbounds url interval
//     tolerance idle_timeout default interrupt_exist_connections expected_status bandwidth(负载均衡:type tag outbounds url interval
//     expected_status bandwidth,outbounds 空也要写);
//   - 出站按「启用的组(直连永远算启用)在归一化列表里的顺序」写:直连 / 拒绝各写成 direct / block 出站,名字是它们现在的名字;
//     停用的拒绝不写;停用的普通组不写也不进 dropped;
//   - 静态成员:节点(链式节点也算)、没被丢掉的组、启用的内置直连 / 拒绝的当前名字,剔除重复 / 自己 / 不存在的;
//     动态组按关键词从非链式节点里挑(关键词看「地区名 + 空格 + 节点 tag」归一化后的文本):没写关键词 = 全部,or = 任一命中,and = 全部命中;
//   - 空成员的 selector / urltest 挂直连占位(写当前直连名,不管直连启用与否)并把组名记进 placeholders;负载均衡只收真实节点,空了照写空数组,
//     不挂占位,超过 64 个报错;
//   - 环:按依赖顺序逐个接纳静态 selector / urltest 组(引用了另一个「还没被接纳的启用组」就等),接纳不了的整组丢掉记 cycle,
//     引用被丢掉的组的组也一并丢掉;动态组 / 故障转移 / 负载均衡不依赖别的组;输出顺序仍按列表顺序;
//   - urltest:url 取组自己的、没有用选项里的 testUrl、再没有用默认地址;interval 缺省 300s,idle_timeout:interval 大于 idle(都能解析)时用 interval,否则 idle;
//     expected_status 组自己的(写错当没写)优先,否则选项里的;选项 disableBandwidth 为真值时不写 bandwidth;
//   - 故障转移:每个页签取存在的节点,0 个为 empty、1 个为 single(直接引用节点)、多个按手动(selector,默认第一个)或自动(urltest)生成
//     内部子组 __fo:<组 id>:<页签 id>(与已有标签撞车就补 ~);对外 selector 是各页签引用(去重)加拒绝,默认第一个引用、没有则默认拒绝,
//     interrupt_exist_connections 为真;拒绝被停用时用内部拒绝 __fo:reject(只生成一次,算内部标签);publicTags 是不含内部标签的出站。

const emitURL = "http://www.gstatic.com/generate_204"
const emitBuiltin = `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`

func jl(s ...string) string {
	if len(s) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func nd(tags ...string) string {
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = fmt.Sprintf(`{"tag":%q}`, t)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func dirOb(tag string) string { return fmt.Sprintf(`{"type":"direct","tag":%q}`, tag) }
func blkOb(tag string) string { return fmt.Sprintf(`{"type":"block","tag":%q}`, tag) }
func selOb(tag string, members ...string) string {
	return fmt.Sprintf(`{"type":"selector","tag":%q,"outbounds":%s}`, tag, jl(members...))
}
func utOb(tag, url, interval, tol, idle, exp, bw string, members ...string) string {
	if exp != "" {
		exp = fmt.Sprintf(`,"expected_status":%q`, exp)
	}
	if bw != "" {
		bw = `,"bandwidth":true`
	}
	return fmt.Sprintf(`{"type":"urltest","tag":%q,"outbounds":%s,"url":%q,"interval":%q,"tolerance":%s,"idle_timeout":%q%s%s}`, tag, jl(members...), url, interval, tol, idle, exp, bw)
}
func lbOb(tag, url, interval, exp, bw string, members ...string) string {
	if exp != "" {
		exp = fmt.Sprintf(`,"expected_status":%q`, exp)
	}
	if bw != "" {
		bw = `,"bandwidth":true`
	}
	return fmt.Sprintf(`{"type":"loadbalance","tag":%q,"outbounds":%s,"url":%q,"interval":%q%s%s}`, tag, jl(members...), url, interval, exp, bw)
}
func foSel(tag, def string, members ...string) string {
	return fmt.Sprintf(`{"type":"selector","tag":%q,"outbounds":%s,"default":%q,"interrupt_exist_connections":true}`, tag, jl(members...), def)
}

// er 是期望的整个结果;空字段取缺省。
type er struct {
	outs                 []string
	dropped              string
	ph, internal, public []string
	builtin              string
	failover             []string
}

func (e er) String() string {
	b := e.builtin
	if b == "" {
		b = emitBuiltin
	}
	return fmt.Sprintf(`{"outbounds":[%s],"dropped":[%s],"placeholders":%s,"builtin":%s,"internalTags":%s,"publicTags":%s,"failover":[%s]}`,
		strings.Join(e.outs, ","), e.dropped, jl(e.ph...), b, jl(e.internal...), jl(e.public...), strings.Join(e.failover, ","))
}

// all 把中间的出站夹进直连 / 拒绝;pub 是对应的公开标签(直连 + 中间 + 拒绝)。
func all(mid ...string) []string {
	return append(append([]string{dirOb("直连")}, mid...), blkOb("拒绝"))
}
func pub(mid ...string) []string { return append(append([]string{"直连"}, mid...), "拒绝") }

func runEmit(t *testing.T, groups, nodes, options string) (string, error) {
	t.Helper()
	g, n, o := gany(t, groups), gany(t, nodes), gany(t, options)
	bg, bn, bo := gtext(t, g), gtext(t, n), gtext(t, o)
	res, err := EmitUserGroups(g, n, o)
	if gtext(t, g) != bg || gtext(t, n) != bn || gtext(t, o) != bo {
		t.Error("改了入参")
	}
	if err != nil {
		return "", err
	}
	return gtext(t, res), nil
}

func checkEmit(t *testing.T, groups, nodes, options string, want er) {
	t.Helper()
	got, err := runEmit(t, groups, nodes, options)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if got != want.String() {
		t.Errorf("不一致\n有: %s\n要: %s", got, want.String())
	}
}

func TestEmitBasicsVectors(t *testing.T) {
	empty := er{outs: all(), public: pub()}
	for name, c := range map[string][3]string{
		"全 null":     {`null`, `null`, `null`},
		"空数组":        {`[]`, `[]`, `{}`},
		"选项不是对象":     {`[]`, `[]`, `"x"`},
		"节点 0":       {`[]`, `0`, `{}`},
		"节点空串":       {`[]`, `""`, `{}`},
		"节点 false":   {`[]`, `false`, `{}`},
		"组不是数组":      {`{"a":1}`, `[{"tag":"A"}]`, `{}`},
		"节点不出现在组出站里": {`[]`, `[{"tag":"A"},{"tag":"B"}]`, `{}`},
	} {
		t.Run(name, func(t *testing.T) { checkEmit(t, c[0], c[1], c[2], empty) })
	}
	for _, nodes := range []string{`"x"`, `5`, `true`, `{"a":1}`} {
		if _, err := runEmit(t, `[]`, nodes, `{}`); err == nil || err.Error() != "nodes 不是数组" {
			t.Errorf("nodes=%s 该报「nodes 不是数组」,得到 %v", nodes, err)
		}
	}
}

func TestEmitSelectorMembersVectors(t *testing.T) {
	// 剔除:重复 / 自己 / 不存在 / 停用的组;留下:节点(含链式)、启用的内置出站名
	checkEmit(t,
		`[{"id":"g","name":"手动","members":["A","B","C","手动","直连","拒绝","A","Off","R"]},{"id":"o","name":"Off","enabled":false}]`,
		`[{"tag":"A"},{"tag":"B"},{"tag":"R","chain":true}]`, `{}`,
		er{outs: all(selOb("手动", "A", "B", "直连", "拒绝", "R")), public: pub("手动")})
	// 成员顺序按用户写的
	checkEmit(t, `[{"id":"g","name":"S","members":["B","A"]}]`, nd("A", "B"), `{}`,
		er{outs: all(selOb("S", "B", "A")), public: pub("S")})
	// 组在列表里的位置决定输出顺序:内置直连放中间时按它的位置
	checkEmit(t, `[{"id":"g","name":"S","members":["A"]},{"id":"builtin-direct","name":"直连"},{"id":"h","name":"T","members":["A"]}]`, nd("A"), `{}`,
		er{outs: []string{selOb("S", "A"), dirOb("直连"), selOb("T", "A"), blkOb("拒绝")}, public: []string{"S", "直连", "T", "拒绝"}})
	// 成员可以是别的启用组
	checkEmit(t, `[{"id":"a","name":"A1","members":["N"]},{"id":"b","name":"B1","members":["A1","N"]}]`, nd("N"), `{}`,
		er{outs: all(selOb("A1", "N"), selOb("B1", "A1", "N")), public: pub("A1", "B1")})
	// 节点名字叫直连:既是节点又是内置名,只留一次
	checkEmit(t, `[{"id":"g","name":"S","members":["直连","直连"]}]`, nd("直连"), `{}`,
		er{outs: all(selOb("S", "直连")), public: pub("S")})
}

func TestEmitPlaceholderVectors(t *testing.T) {
	checkEmit(t, `[{"id":"a","name":"S1"},{"id":"b","name":"S2","members":["nope"]},{"id":"c","name":"U","type":"urltest"}]`, nd("A"), `{}`,
		er{outs: all(selOb("S1", "直连"), selOb("S2", "直连"), utOb("U", emitURL, "300s", "100", "12h", "", "", "直连")),
			ph: []string{"S1", "S2", "U"}, public: pub("S1", "S2", "U")})
	// 直连改名后占位和成员都用新名字
	checkEmit(t, `[{"id":"builtin-direct","name":"DIRECT"},{"id":"a","name":"S","members":["DIRECT","直连"]},{"id":"b","name":"E"}]`, nd("A"), `{}`,
		er{outs: []string{dirOb("DIRECT"), selOb("S", "DIRECT"), selOb("E", "DIRECT"), blkOb("拒绝")}, ph: []string{"E"}, public: []string{"DIRECT", "S", "E", "拒绝"},
			builtin: `{"direct":"DIRECT","block":"拒绝","directEnabled":true,"blockEnabled":true}`})
	// 直连被停用:直连出站照写,但不能当成员(被剔除后挂占位,占位仍写直连名)
	checkEmit(t, `[{"id":"builtin-direct","name":"直连","enabled":false},{"id":"a","name":"S","members":["直连","A"]},{"id":"b","name":"T","members":["直连"]}]`, nd("A"), `{}`,
		er{outs: all(selOb("S", "A"), selOb("T", "直连")), ph: []string{"T"}, public: pub("S", "T"),
			builtin: `{"direct":"直连","block":"拒绝","directEnabled":false,"blockEnabled":true}`})
	// 拒绝被停用:不写拒绝出站,也不能当成员
	checkEmit(t, `[{"id":"builtin-block","name":"拒绝","enabled":false},{"id":"a","name":"S","members":["拒绝","A"]}]`, nd("A"), `{}`,
		er{outs: []string{dirOb("直连"), selOb("S", "A")}, public: []string{"直连", "S"},
			builtin: `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":false}`})
	// 拒绝改名
	checkEmit(t, `[{"id":"a","name":"S","members":["BLOCK","拒绝"]},{"id":"builtin-block","name":"BLOCK"}]`, nd("A"), `{}`,
		er{outs: []string{dirOb("直连"), selOb("S", "BLOCK"), blkOb("BLOCK")}, public: []string{"直连", "S", "BLOCK"},
			builtin: `{"direct":"直连","block":"BLOCK","directEnabled":true,"blockEnabled":true}`})
}

func TestEmitUrltestVectors(t *testing.T) {
	type c struct {
		name, extra, options, want string
	}
	ut := func(url, interval, tol, idle, exp, bw string) string {
		return utOb("U", url, interval, tol, idle, exp, bw, "A", "B")
	}
	cases := []c{
		{"缺省", ``, `{}`, ut(emitURL, "300s", "100", "12h", "", "")},
		{"选项 testUrl", ``, `{"testUrl":"http://t/"}`, ut("http://t/", "300s", "100", "12h", "", "")},
		{"选项 testUrl 空串用默认", ``, `{"testUrl":""}`, ut(emitURL, "300s", "100", "12h", "", "")},
		{"选项 testUrl 非字符串用默认", ``, `{"testUrl":5}`, ut(emitURL, "300s", "100", "12h", "", "")},
		{"组自己的 testUrl 优先", `"testUrl":"http://own/"`, `{"testUrl":"http://t/"}`, ut("http://own/", "300s", "100", "12h", "", "")},
		{"选项状态码", ``, `{"expectedStatus":"204"}`, ut(emitURL, "300s", "100", "12h", "204", "")},
		{"选项状态码规范化", ``, `{"expectedStatus":"200, 302"}`, ut(emitURL, "300s", "100", "12h", "200/302", "")},
		{"选项状态码写错不写", ``, `{"expectedStatus":"bad"}`, ut(emitURL, "300s", "100", "12h", "", "")},
		{"选项状态码星号不写", ``, `{"expectedStatus":"*"}`, ut(emitURL, "300s", "100", "12h", "", "")},
		{"组状态码优先", `"expectedStatus":"200-299"`, `{"expectedStatus":"204"}`, ut(emitURL, "300s", "100", "12h", "200-299", "")},
		{"组状态码写错回落到选项", `"expectedStatus":"bad"`, `{"expectedStatus":"204"}`, ut(emitURL, "300s", "100", "12h", "204", "")},
		{"带宽", `"bandwidth":true`, `{}`, ut(emitURL, "300s", "100", "12h", "", "1")},
		{"选项 disableBandwidth true", `"bandwidth":true`, `{"disableBandwidth":true}`, ut(emitURL, "300s", "100", "12h", "", "")},
		{"选项 disableBandwidth 1", `"bandwidth":true`, `{"disableBandwidth":1}`, ut(emitURL, "300s", "100", "12h", "", "")},
		{"选项 disableBandwidth 非空串", `"bandwidth":true`, `{"disableBandwidth":"x"}`, ut(emitURL, "300s", "100", "12h", "", "")},
		{"选项 disableBandwidth false", `"bandwidth":true`, `{"disableBandwidth":false}`, ut(emitURL, "300s", "100", "12h", "", "1")},
		{"选项 disableBandwidth 0 与空串", `"bandwidth":true`, `{"disableBandwidth":0}`, ut(emitURL, "300s", "100", "12h", "", "1")},
		{"选项 disableBandwidth 空串", `"bandwidth":true`, `{"disableBandwidth":""}`, ut(emitURL, "300s", "100", "12h", "", "1")},
		{"没开带宽", `"bandwidth":false`, `{}`, ut(emitURL, "300s", "100", "12h", "", "")},
		// idle_timeout
		{"interval 大于 idle 用 interval", `"interval":"5m","idleTimeout":"1m"`, `{}`, ut(emitURL, "5m", "100", "5m", "", "")},
		{"interval 大于默认 idle", `"interval":"13h"`, `{}`, ut(emitURL, "13h", "100", "13h", "", "")},
		{"interval 24h", `"interval":"24h"`, `{}`, ut(emitURL, "24h", "100", "24h", "", "")},
		{"interval 小于 idle", `"interval":"1h"`, `{}`, ut(emitURL, "1h", "100", "12h", "", "")},
		{"interval 与 idle 写法不同但毫秒相等", `"interval":"1800s","idleTimeout":"30m"`, `{}`, ut(emitURL, "1800s", "100", "30m", "", "")},
		{"interval 等于 idle", `"interval":"30m","idleTimeout":"30m"`, `{}`, ut(emitURL, "30m", "100", "30m", "", "")},
		{"interval 写错", `"interval":"abc","idleTimeout":"1m"`, `{}`, ut(emitURL, "abc", "100", "1m", "", "")},
		{"interval 0s", `"interval":"0s"`, `{}`, ut(emitURL, "0s", "100", "12h", "", "")},
		{"idle 写错原样", `"interval":"5m","idleTimeout":"abc"`, `{}`, ut(emitURL, "5m", "100", "abc", "", "")},
		{"idle 比 interval 小的毫秒", `"interval":"1000ms","idleTimeout":"500ms"`, `{}`, ut(emitURL, "1000ms", "100", "1000ms", "", "")},
		// tolerance
		{"tolerance 0 也写", `"tolerance":0`, `{}`, ut(emitURL, "300s", "0", "12h", "", "")},
		{"tolerance 取整", `"tolerance":50.9`, `{}`, ut(emitURL, "300s", "50", "12h", "", "")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			extra := ""
			if c.extra != "" {
				extra = "," + c.extra
			}
			checkEmit(t, `[{"id":"u","name":"U","type":"urltest","members":["A","B"]`+extra+`}]`, nd("A", "B"), c.options,
				er{outs: all(c.want), public: pub("U")})
		})
	}
}

func TestEmitLoadBalanceVectors(t *testing.T) {
	g := func(extra string) string {
		return `[{"id":"l","name":"L","type":"loadbalance","members":["A","B"]` + extra + `}]`
	}
	lb := func(url, interval, exp, bw string, m ...string) string {
		return lbOb("L", url, interval, exp, bw, m...)
	}
	checkEmit(t, g(``), nd("A", "B"), `{}`, er{outs: all(lb(emitURL, "120s", "", "", "A", "B")), public: pub("L")})
	checkEmit(t, g(`,"interval":"30s","testUrl":"http://own/","expectedStatus":"204"`), nd("A", "B"), `{"testUrl":"http://t/"}`,
		er{outs: all(lb("http://own/", "30s", "204", "", "A", "B")), public: pub("L")})
	checkEmit(t, g(``), nd("A", "B"), `{"testUrl":"http://t/","expectedStatus":"200-299"}`,
		er{outs: all(lb("http://t/", "120s", "200-299", "", "A", "B")), public: pub("L")})
	checkEmit(t, g(`,"bandwidth":true`), nd("A", "B"), `{}`, er{outs: all(lb(emitURL, "120s", "", "1", "A", "B")), public: pub("L")})
	checkEmit(t, g(`,"bandwidth":true`), nd("A", "B"), `{"disableBandwidth":true}`, er{outs: all(lb(emitURL, "120s", "", "", "A", "B")), public: pub("L")})
	// 只收真实节点:组、内置出站、不存在的名字、自己都不写;链式节点是真实节点
	checkEmit(t, `[{"id":"s","name":"S","members":["A"]},{"id":"l","name":"L","type":"loadbalance","members":["S","直连","拒绝","L","Z","R","A","A"]}]`,
		`[{"tag":"A"},{"tag":"R","chain":true}]`, `{}`,
		er{outs: all(selOb("S", "A"), lb(emitURL, "120s", "", "", "R", "A")), public: pub("S", "L")})
	// 空成员:照写空数组,不挂占位
	checkEmit(t, `[{"id":"l","name":"L","type":"loadbalance"}]`, nd("A"), `{}`,
		er{outs: all(`{"type":"loadbalance","tag":"L","outbounds":[],"url":"` + emitURL + `","interval":"120s"}`), public: pub("L")})
	checkEmit(t, `[{"id":"l","name":"L","type":"loadbalance","members":["S"]},{"id":"s","name":"S","members":["A"]}]`, nd("A"), `{}`,
		er{outs: all(`{"type":"loadbalance","tag":"L","outbounds":[],"url":"`+emitURL+`","interval":"120s"}`, selOb("S", "A")), public: pub("L", "S")})
	// 动态:全部非链式节点
	checkEmit(t, `[{"id":"l","name":"L","type":"loadbalance","mode":"dynamic"}]`, `[{"tag":"A"},{"tag":"R","chain":true},{"tag":"B"}]`, `{}`,
		er{outs: all(lb(emitURL, "120s", "", "", "A", "B")), public: pub("L")})
	// 数量上限 64
	many := func(n int) string {
		tags := make([]string, n)
		for i := range tags {
			tags[i] = fmt.Sprintf("N%d", i)
		}
		return nd(tags...)
	}
	for _, c := range []struct {
		name, groups string
		n            int
		wantErr      bool
	}{
		{"动态 64 个", `[{"id":"l","name":"L","type":"loadbalance","mode":"dynamic"}]`, 64, false},
		{"动态 65 个", `[{"id":"l","name":"L","type":"loadbalance","mode":"dynamic"}]`, 65, true},
		{"静态 65 个全列出", `[{"id":"l","name":"L","type":"loadbalance","members":` + func() string {
			var s []string
			for i := 0; i < 65; i++ {
				s = append(s, fmt.Sprintf("N%d", i))
			}
			return jl(s...)
		}() + `}]`, 65, true},
		{"静态列出 65 个但只有 64 个存在", `[{"id":"l","name":"L","type":"loadbalance","members":` + func() string {
			var s []string
			for i := 0; i < 65; i++ {
				s = append(s, fmt.Sprintf("N%d", i))
			}
			return jl(s...)
		}() + `}]`, 64, false},
	} {
		got, err := runEmit(t, c.groups, many(c.n), `{}`)
		if c.wantErr {
			if err == nil || err.Error() != "load balance supports at most 64 nodes" {
				t.Errorf("%s:该报「load balance supports at most 64 nodes」,得到 %v", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s:不该报错 %v", c.name, err)
			continue
		}
		if !strings.Contains(got, `"tag":"L","outbounds":["N0","N1"`) || !strings.Contains(got, `"N63"]`) || strings.Contains(got, `"N64"`) {
			t.Errorf("%s:成员不对 %s", c.name, got)
		}
	}
}

func TestEmitDynamicVectors(t *testing.T) {
	nodes := `[{"tag":"HK-01","regionName":"香港"},{"tag":"US-01","regionName":"美国"},{"tag":"香港 IPLC 02","regionName":""},` +
		`{"tag":"Chain-HK","regionName":"香港","chain":true},{"tag":"node5","regionName":"\ud83c\udded\ud83c\uddf0 Hong Kong"},{"tag":"JP-us-x","regionName":"日本"}]`
	dyn := func(extra string) string {
		return `[{"id":"d","name":"D","mode":"dynamic","members":["US-01"]` + extra + `}]`
	}
	run := func(name, extra string, want ...string) {
		t.Run(name, func(t *testing.T) {
			w := er{outs: all(selOb("D", want...)), public: pub("D")}
			if len(want) == 0 {
				w = er{outs: all(selOb("D", "直连")), ph: []string{"D"}, public: pub("D")}
			}
			checkEmit(t, dyn(extra), nodes, `{}`, w)
		})
	}
	run("没有关键词:全部非链式节点,显式成员被忽略", ``, "HK-01", "US-01", "香港 IPLC 02", "node5", "JP-us-x")
	run("or 短码 hk(国旗折成 hk)", `,"keywords":["hk"]`, "HK-01", "node5")
	run("or 中文子串", `,"keywords":["香港"]`, "HK-01", "香港 IPLC 02")
	run("or 多个关键词任一命中", `,"keywords":["香港","美国"]`, "HK-01", "US-01", "香港 IPLC 02")
	run("or 多个关键词同时命中同一节点不重复", `,"keywords":["香港","hk"]`, "HK-01", "香港 IPLC 02", "node5")
	run("短码 us 不命中 JP-us-x 之外的字母相邻", `,"keywords":["us"]`, "US-01", "JP-us-x")
	run("关键词大写", `,"keywords":["HK-01"]`, "HK-01")
	run("国旗关键词", `,"keywords":["\ud83c\udded\ud83c\uddf0"]`, "HK-01", "node5")
	run("子串 hong kong", `,"keywords":["hong kong"]`, "node5")
	run("and 全部命中", `,"keywordMode":"and","keywords":["香港","01"]`, "HK-01")
	run("and 有一个不命中就整体不命中", `,"keywordMode":"and","keywords":["香港","美国"]`)
	run("and 单个关键词", `,"keywordMode":"and","keywords":["hk"]`, "HK-01", "node5")
	run("or 没有任何命中挂占位", `,"keywords":["zzz"]`)
	run("keywordMode 不是 and 都是 or", `,"keywordMode":"AND","keywords":["香港","美国"]`, "HK-01", "US-01", "香港 IPLC 02")
	// 匹配文本是「地区名 空格 tag」:短码 us 要靠这个空格才和前面的地区名隔开
	mixed := `[{"tag":"us","regionName":"a"},{"tag":"c1","chain":1},{"tag":"c2","chain":"yes"},{"tag":"n3","chain":0},{"tag":"n4","chain":""},{"tag":"n5","chain":null},{"tag":"n6","chain":false}]`
	checkEmit(t, `[{"id":"d","name":"D","mode":"dynamic","keywords":["us"]}]`, mixed, `{}`, er{outs: all(selOb("D", "us")), public: pub("D")})
	// chain 按 JS 真值判断:1 / "yes" 算链式(动态组不收),0 / "" / null / false 不算
	checkEmit(t, `[{"id":"d","name":"D","mode":"dynamic"}]`, mixed, `{}`, er{outs: all(selOb("D", "us", "n3", "n4", "n5", "n6")), public: pub("D")})
	// allNodes 兼容写法
	checkEmit(t, `[{"id":"d","name":"D","allNodes":true}]`, nd("A", "B"), `{}`, er{outs: all(selOb("D", "A", "B")), public: pub("D")})
	// 动态 urltest 同样
	checkEmit(t, `[{"id":"d","name":"D","type":"urltest","mode":"dynamic","keywords":["a"]}]`, nd("A", "B", "ab"), `{}`,
		er{outs: all(utOb("D", emitURL, "300s", "100", "12h", "", "", "A", "ab")), public: pub("D")})
	// 静态组可以引用动态组,也能点名链式节点
	checkEmit(t, `[{"id":"d","name":"D","mode":"dynamic"},{"id":"s","name":"S","members":["D","R"]}]`, `[{"tag":"A"},{"tag":"R","chain":true}]`, `{}`,
		er{outs: all(selOb("D", "A"), selOb("S", "D", "R")), public: pub("D", "S")})
}

func TestEmitCycleVectors(t *testing.T) {
	d := func(names ...string) string {
		parts := make([]string, len(names))
		for i, n := range names {
			parts[i] = fmt.Sprintf(`{"name":%q,"reason":"cycle"}`, n)
		}
		return strings.Join(parts, ",")
	}
	// 互相引用:两个都丢;引用被丢掉的组的也丢;其余照常
	checkEmit(t, `[{"id":"x","name":"X","members":["Y"]},{"id":"y","name":"Y","members":["X"]},{"id":"z","name":"Z","members":["X","A"]},{"id":"w","name":"W","members":["A"]}]`, nd("A"), `{}`,
		er{outs: all(selOb("W", "A")), dropped: d("X", "Y", "Z"), public: pub("W")})
	// 自己引用自己:不算环,成员里自己被剔除后挂占位
	checkEmit(t, `[{"id":"x","name":"X","members":["X"]}]`, nd("A"), `{}`,
		er{outs: all(selOb("X", "直连")), ph: []string{"X"}, public: pub("X")})
	// 三角环
	checkEmit(t, `[{"id":"a","name":"A1","members":["B1"]},{"id":"b","name":"B1","members":["C1"]},{"id":"c","name":"C1","members":["A1"]}]`, nd("N"), `{}`,
		er{outs: all(), dropped: d("A1", "B1", "C1"), public: pub()})
	// 先引用后定义:不是环,输出顺序仍按列表顺序
	checkEmit(t, `[{"id":"p","name":"P1","members":["P2"]},{"id":"q","name":"P2","members":["A"]}]`, nd("A"), `{}`,
		er{outs: all(selOb("P1", "P2"), selOb("P2", "A")), public: pub("P1", "P2")})
	// 引用停用的组:那个名字不存在,被剔除
	checkEmit(t, `[{"id":"g","name":"G","members":["Off","A"]},{"id":"o","name":"Off","enabled":false,"members":["G"]}]`, nd("A"), `{}`,
		er{outs: all(selOb("G", "A")), public: pub("G")})
	// 动态组不依赖别的组
	checkEmit(t, `[{"id":"d","name":"D","mode":"dynamic","members":["S"]},{"id":"s","name":"S","members":["D"]}]`, nd("A"), `{}`,
		er{outs: all(selOb("D", "A"), selOb("S", "D")), public: pub("D", "S")})
	// 故障转移 / 负载均衡不依赖别的组:引用它们的组不会被连累
	checkEmit(t, `[{"id":"s","name":"S","members":["F","L"]},{"id":"f","name":"F","type":"failover","lanes":[{"id":"l","members":["A"]}]},{"id":"l","name":"L","type":"loadbalance","members":["S"]}]`, nd("A"), `{}`,
		er{outs: all(selOb("S", "F", "L"), foSel("F", "A", "A", "拒绝"), `{"type":"loadbalance","tag":"L","outbounds":[],"url":"`+emitURL+`","interval":"120s"}`),
			public:   pub("S", "F", "L"),
			failover: []string{`{"id":"f","tag":"F","lanes":[{"id":"l","name":"","icon":"","index":0,"members":["A"],"valid":["A"],"mode":"single","ref":"A","subTag":null}],"rejectTag":"拒绝","settings":` + foSettings("300s", 300000, "100", emitURL, "", "5000", "2", "true", "60000") + `}`}})
	// 停用的普通组不进 dropped
	checkEmit(t, `[{"id":"x","name":"X","enabled":false,"members":["Y"]},{"id":"y","name":"Y","enabled":false,"members":["X"]}]`, nd("A"), `{}`,
		er{outs: all(), public: pub()})
}

func foSettings(interval string, intervalMs int, tol, url, exp, timeout, thr, restore, hold string) string {
	e := ""
	if exp != "" {
		e = fmt.Sprintf(`"expectedStatus":%q,`, exp)
	}
	return fmt.Sprintf(`{"interval":%q,"intervalMs":%d,"tolerance":%s,"testUrl":%q,%s"timeoutMs":%s,"failureThreshold":%s,"restorePrimary":%s,"recoveryHoldMs":%s}`,
		interval, intervalMs, tol, url, e, timeout, thr, restore, hold)
}

func TestEmitFailoverVectors(t *testing.T) {
	std := foSettings("300s", 300000, "100", emitURL, "", "5000", "2", "true", "60000")
	lane := func(id, name, icon string, index int, members, valid []string, mode, ref, sub string) string {
		r, s := "null", "null"
		if ref != "" {
			r = fmt.Sprintf("%q", ref)
		}
		if sub != "" {
			s = fmt.Sprintf("%q", sub)
		}
		return fmt.Sprintf(`{"id":%q,"name":%q,"icon":%q,"index":%d,"members":%s,"valid":%s,"mode":%q,"ref":%s,"subTag":%s}`, id, name, icon, index, jl(members...), jl(valid...), mode, r, s)
	}
	state := func(id, tag string, lanes []string, reject, settings string) string {
		return fmt.Sprintf(`{"id":%q,"tag":%q,"lanes":[%s],"rejectTag":%q,"settings":%s}`, id, tag, strings.Join(lanes, ","), reject, settings)
	}

	t.Run("三种页签", func(t *testing.T) {
		checkEmit(t, `[{"id":"f1","name":"FO","type":"failover","lanes":[{"id":"l1","name":"主","icon":"hk","members":["A","Z"]},{"id":"l2","members":["A","B"]},{"id":"l3","members":["A","B"],"manual":true}]}]`, nd("A", "B"), `{}`,
			er{outs: all(
				utOb("__fo:f1:l2", emitURL, "300s", "100", "12h", "", "", "A", "B"),
				`{"type":"selector","tag":"__fo:f1:l3","outbounds":["A","B"],"default":"A"}`,
				foSel("FO", "A", "A", "__fo:f1:l2", "__fo:f1:l3", "拒绝")),
				internal: []string{"__fo:f1:l2", "__fo:f1:l3"}, public: pub("FO"),
				failover: []string{state("f1", "FO", []string{
					lane("l1", "主", "hk", 0, []string{"A", "Z"}, []string{"A"}, "single", "A", ""),
					lane("l2", "", "", 1, []string{"A", "B"}, []string{"A", "B"}, "urltest", "__fo:f1:l2", "__fo:f1:l2"),
					lane("l3", "", "", 2, []string{"A", "B"}, []string{"A", "B"}, "selector", "__fo:f1:l3", "__fo:f1:l3"),
				}, "拒绝", std)}})
	})
	t.Run("没有可用页签:默认拒绝", func(t *testing.T) {
		for _, lanes := range []string{`[{"id":"l1","members":["Z"]},{"id":"l2"}]`, `[]`, `null`} {
			var ls []string
			var st string
			if lanes == `[{"id":"l1","members":["Z"]},{"id":"l2"}]` {
				ls = []string{lane("l1", "", "", 0, []string{"Z"}, nil, "empty", "", ""), lane("l2", "", "", 1, nil, nil, "empty", "", "")}
			}
			st = state("f", "FO", ls, "拒绝", std)
			checkEmit(t, `[{"id":"f","name":"FO","type":"failover","lanes":`+lanes+`}]`, nd("A"), `{}`,
				er{outs: all(foSel("FO", "拒绝", "拒绝")), public: pub("FO"), failover: []string{st}})
		}
	})
	t.Run("拒绝停用:用内部拒绝", func(t *testing.T) {
		checkEmit(t, `[{"id":"f","name":"FO","type":"failover","lanes":[{"id":"l1","members":["A"]}]},{"id":"builtin-block","name":"拒绝","enabled":false}]`, nd("A"), `{}`,
			er{outs: []string{dirOb("直连"), blkOb("__fo:reject"), foSel("FO", "A", "A", "__fo:reject")}, internal: []string{"__fo:reject"}, public: []string{"直连", "FO"},
				builtin:  `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":false}`,
				failover: []string{state("f", "FO", []string{lane("l1", "", "", 0, []string{"A"}, []string{"A"}, "single", "A", "")}, "__fo:reject", std)}})
	})
	t.Run("拒绝停用且没有页签:默认内部拒绝", func(t *testing.T) {
		checkEmit(t, `[{"id":"f","name":"FO","type":"failover"},{"id":"builtin-block","name":"拒绝","enabled":false}]`, nd("A"), `{}`,
			er{outs: []string{dirOb("直连"), blkOb("__fo:reject"), foSel("FO", "__fo:reject", "__fo:reject")}, internal: []string{"__fo:reject"}, public: []string{"直连", "FO"},
				builtin:  `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":false}`,
				failover: []string{state("f", "FO", nil, "__fo:reject", std)}})
	})
	t.Run("内部拒绝只生成一次", func(t *testing.T) {
		checkEmit(t, `[{"id":"f","name":"F1","type":"failover"},{"id":"g","name":"F2","type":"failover"},{"id":"builtin-block","name":"拒绝","enabled":false}]`, nd("A"), `{}`,
			er{outs: []string{dirOb("直连"), blkOb("__fo:reject"), foSel("F1", "__fo:reject", "__fo:reject"), foSel("F2", "__fo:reject", "__fo:reject")}, internal: []string{"__fo:reject"}, public: []string{"直连", "F1", "F2"},
				builtin:  `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":false}`,
				failover: []string{state("f", "F1", nil, "__fo:reject", std), state("g", "F2", nil, "__fo:reject", std)}})
	})
	t.Run("多个页签引用同一个节点只留一次", func(t *testing.T) {
		checkEmit(t, `[{"id":"f","name":"FO","type":"failover","lanes":[{"id":"a","members":["A"]},{"id":"b","members":["A"]},{"id":"c","members":["B"]}]}]`, nd("A", "B"), `{}`,
			er{outs: all(foSel("FO", "A", "A", "B", "拒绝")), public: pub("FO"),
				failover: []string{state("f", "FO", []string{
					lane("a", "", "", 0, []string{"A"}, []string{"A"}, "single", "A", ""),
					lane("b", "", "", 1, []string{"A"}, []string{"A"}, "single", "A", ""),
					lane("c", "", "", 2, []string{"B"}, []string{"B"}, "single", "B", "")}, "拒绝", std)}})
	})
	t.Run("子组标签与节点撞车补波浪线", func(t *testing.T) {
		checkEmit(t, `[{"id":"f1","name":"FO","type":"failover","lanes":[{"id":"l2","members":["A","B"]}]}]`, nd("A", "B", "__fo:f1:l2"), `{}`,
			er{outs: all(utOb("__fo:f1:l2~", emitURL, "300s", "100", "12h", "", "", "A", "B"), foSel("FO", "__fo:f1:l2~", "__fo:f1:l2~", "拒绝")),
				internal: []string{"__fo:f1:l2~"}, public: pub("FO"),
				failover: []string{state("f1", "FO", []string{lane("l2", "", "", 0, []string{"A", "B"}, []string{"A", "B"}, "urltest", "__fo:f1:l2~", "__fo:f1:l2~")}, "拒绝", std)}})
	})
	t.Run("两个同 id 的故障转移组子组标签不重复", func(t *testing.T) {
		checkEmit(t, `[{"id":"f","name":"F1","type":"failover","lanes":[{"id":"l","members":["A","B"]}]},{"id":"f","name":"F2","type":"failover","lanes":[{"id":"l","members":["A","B"]}]}]`, nd("A", "B"), `{}`,
			er{outs: all(
				utOb("__fo:f:l", emitURL, "300s", "100", "12h", "", "", "A", "B"), foSel("F1", "__fo:f:l", "__fo:f:l", "拒绝"),
				utOb("__fo:f:l~", emitURL, "300s", "100", "12h", "", "", "A", "B"), foSel("F2", "__fo:f:l~", "__fo:f:l~", "拒绝")),
				internal: []string{"__fo:f:l", "__fo:f:l~"}, public: pub("F1", "F2"),
				failover: []string{
					state("f", "F1", []string{lane("l", "", "", 0, []string{"A", "B"}, []string{"A", "B"}, "urltest", "__fo:f:l", "__fo:f:l")}, "拒绝", std),
					state("f", "F2", []string{lane("l", "", "", 0, []string{"A", "B"}, []string{"A", "B"}, "urltest", "__fo:f:l~", "__fo:f:l~")}, "拒绝", std)}})
	})
	t.Run("检测参数传到子组和状态", func(t *testing.T) {
		g := `[{"id":"f","name":"FO","type":"failover","interval":"24h","testUrl":"http://x/","expectedStatus":"204","bandwidth":true,"tolerance":50,` +
			`"failover":{"timeoutMs":2000,"failureThreshold":3,"restorePrimary":false,"recoveryHoldMs":0},"lanes":[{"id":"l","members":["A","B"]}]}]`
		st := foSettings("24h", 86400000, "50", "http://x/", "204", "2000", "3", "false", "0")
		l := lane("l", "", "", 0, []string{"A", "B"}, []string{"A", "B"}, "urltest", "__fo:f:l", "__fo:f:l")
		checkEmit(t, g, nd("A", "B"), `{"testUrl":"http://t/","expectedStatus":"200"}`,
			er{outs: all(utOb("__fo:f:l", "http://x/", "24h", "50", "24h", "204", "1", "A", "B"), foSel("FO", "__fo:f:l", "__fo:f:l", "拒绝")),
				internal: []string{"__fo:f:l"}, public: pub("FO"), failover: []string{state("f", "FO", []string{l}, "拒绝", st)}})
		// 带宽被选项关掉;没有自己的状态码用选项的;没有自己的检测地址用选项的
		g2 := `[{"id":"f","name":"FO","type":"failover","bandwidth":true,"lanes":[{"id":"l","members":["A","B"]}]}]`
		st2 := foSettings("300s", 300000, "100", "http://t/", "200", "5000", "2", "true", "60000")
		checkEmit(t, g2, nd("A", "B"), `{"testUrl":"http://t/","expectedStatus":"200","disableBandwidth":true}`,
			er{outs: all(utOb("__fo:f:l", "http://t/", "300s", "100", "12h", "200", "", "A", "B"), foSel("FO", "__fo:f:l", "__fo:f:l", "拒绝")),
				internal: []string{"__fo:f:l"}, public: pub("FO"), failover: []string{state("f", "FO", []string{l}, "拒绝", st2)}})
	})
	t.Run("手动页签也算内部标签,链式节点能进页签", func(t *testing.T) {
		checkEmit(t, `[{"id":"f","name":"FO","type":"failover","lanes":[{"id":"m","members":["R","A"],"manual":true}]}]`, `[{"tag":"A"},{"tag":"R","chain":true}]`, `{}`,
			er{outs: all(`{"type":"selector","tag":"__fo:f:m","outbounds":["R","A"],"default":"R"}`, foSel("FO", "__fo:f:m", "__fo:f:m", "拒绝")),
				internal: []string{"__fo:f:m"}, public: pub("FO"),
				failover: []string{state("f", "FO", []string{lane("m", "", "", 0, []string{"R", "A"}, []string{"R", "A"}, "selector", "__fo:f:m", "__fo:f:m")}, "拒绝", std)}})
	})
	t.Run("手动页签的子组只含存在的节点", func(t *testing.T) {
		checkEmit(t, `[{"id":"f","name":"FO","type":"failover","lanes":[{"id":"m","members":["A","Z","B"],"manual":true}]}]`, nd("A", "B"), `{}`,
			er{outs: all(`{"type":"selector","tag":"__fo:f:m","outbounds":["A","B"],"default":"A"}`, foSel("FO", "__fo:f:m", "__fo:f:m", "拒绝")),
				internal: []string{"__fo:f:m"}, public: pub("FO"),
				failover: []string{state("f", "FO", []string{lane("m", "", "", 0, []string{"A", "Z", "B"}, []string{"A", "B"}, "selector", "__fo:f:m", "__fo:f:m")}, "拒绝", std)}})
	})
	t.Run("手动页签只有一个节点当 single", func(t *testing.T) {
		checkEmit(t, `[{"id":"f","name":"FO","type":"failover","lanes":[{"id":"m","members":["A","Z"],"manual":true}]}]`, nd("A"), `{}`,
			er{outs: all(foSel("FO", "A", "A", "拒绝")), public: pub("FO"),
				failover: []string{state("f", "FO", []string{lane("m", "", "", 0, []string{"A", "Z"}, []string{"A"}, "single", "A", "")}, "拒绝", std)}})
	})
	t.Run("故障转移组被别的组引用,自己的名字撞到子组标签也补波浪线", func(t *testing.T) {
		// 组名字就是子组标签:usedTags 里已有它
		checkEmit(t, `[{"id":"f","name":"__fo:f:l","type":"failover","lanes":[{"id":"l","members":["A","B"]}]}]`, nd("A", "B"), `{}`,
			er{outs: all(utOb("__fo:f:l~", emitURL, "300s", "100", "12h", "", "", "A", "B"), foSel("__fo:f:l", "__fo:f:l~", "__fo:f:l~", "拒绝")),
				internal: []string{"__fo:f:l~"}, public: pub("__fo:f:l"),
				failover: []string{state("f", "__fo:f:l", []string{lane("l", "", "", 0, []string{"A", "B"}, []string{"A", "B"}, "urltest", "__fo:f:l~", "__fo:f:l~")}, "拒绝", std)}})
	})
	t.Run("停用的故障转移组不写", func(t *testing.T) {
		checkEmit(t, `[{"id":"f","name":"FO","type":"failover","enabled":false,"lanes":[{"id":"l","members":["A"]}]}]`, nd("A"), `{}`, er{outs: all(), public: pub()})
	})
}

func TestEmitHandlerVectors(t *testing.T) {
	raw := func(s ...string) []json.RawMessage {
		out := make([]json.RawMessage, len(s))
		for i, x := range s {
			out[i] = json.RawMessage(x)
		}
		return out
	}
	res, err := hEmitGroups(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := gtext(t, res), (er{outs: all(), public: pub()}).String(); got != want {
		t.Errorf("无参数 不一致\n有: %s\n要: %s", got, want)
	}
	res, err = hEmitGroups(nil, raw(`[{"id":"s","name":"S","members":["A"]}]`, `[{"tag":"A"}]`, `{"testUrl":"http://t/"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := gtext(t, res), (er{outs: all(selOb("S", "A")), public: pub("S")}).String(); got != want {
		t.Errorf("不一致\n有: %s\n要: %s", got, want)
	}
	if _, err := hEmitGroups(nil, raw(`[]`, `"x"`)); err == nil {
		t.Error("nodes 不是数组该报错")
	}
	if _, err := hEmitGroups(nil, raw(`{`)); err == nil {
		t.Error("坏 JSON 该报错")
	}
}

func TestEmitOutboundMarshalVectors(t *testing.T) {
	for _, c := range []struct {
		o    Outbound
		want string
	}{
		{Outbound{Type: "loadbalance", Tag: "x"}, `{"type":"loadbalance","tag":"x","outbounds":[]}`},
		{Outbound{Type: "loadbalance", Tag: "x", Outbounds: []string{}}, `{"type":"loadbalance","tag":"x","outbounds":[]}`},
		{Outbound{Type: "loadbalance", Tag: "x", Outbounds: []string{"a"}, URL: "u", Interval: "1s", ExpectedStatus: "204", Bandwidth: true, IdleTimeout: "9s", Default: "a"},
			`{"type":"loadbalance","tag":"x","outbounds":["a"],"url":"u","interval":"1s","expected_status":"204","bandwidth":true}`},
		{Outbound{Type: "selector", Tag: "x"}, `{"type":"selector","tag":"x"}`},
		{Outbound{Type: "direct", Tag: "x"}, `{"type":"direct","tag":"x"}`},
	} {
		if got := gtext(t, c.o); got != c.want {
			t.Errorf("不一致\n有: %s\n要: %s", got, c.want)
		}
	}
}
