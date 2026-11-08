package groups

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 自写测试向量:group.go 的 NormalizeGroups / builtinTags / BuiltinTags(含协议入口 normalizeGroups、builtinTags)。
// 输入自己拟,期望值按下面的规则人工推算,JSON 逐字节比(键序固定):
//   - 列表不是数组当空;两个内置出站永远在:没有直连就补在最前、没有拒绝就补在最后(各自是固定对象,键序是
//     id kind name type mode icon keywords members enabled,没有 iconScale),有就照用户排的位置;
//   - 一般节点组的键序:id name type mode enabled icon iconScale keywordMode keywords members,
//     之后按类型接 urltest / loadbalance / failover 各自的字段,开着带宽择优才多一个 bandwidth:true,有 kind 的最后带 kind;
//   - type 只认 urltest / selector / failover / loadbalance(原样比较),其余当 selector;mode:故障转移固定 static,
//     其余类型 mode 写了 static / dynamic 就用,否则 allNodes 严格等于 true 才是 dynamic;
//   - id / name 取 trim 后的非空字符串,缺省 group-<下标> / 分组-<下标+1>(下标是在用户原列表里的位置);
//     enabled 只有布尔 false 才为假;keywordMode 只有恰好是 "and" 才是 and;keywords / members 只收非空字符串并 trim,不去重;
//   - 图标:trim 后以 globe: / brand: / misc: 打头(不分大小写)整串转小写;恰好两个英文字母转大写;其余原样;
//     iconScale:数组一律 0,否则按 Number 转,不是有限数得 0,有限数取 floor(x+0.5) 并夹在 ±20;
//   - urltest:testUrl 非空串或空串;expectedStatus 走 NormalizeExpectedStatus,写错得空串;interval 缺省 300s(不校验写法);
//     tolerance 数组当缺省 100,否则按 Number 转,有限且不小于 0 取 floor,不然 100(null 转成 0 所以得 0,没写才是 100);
//     idleTimeout 缺省 12h;bandwidth 严格等于 true;
//   - loadbalance:同样的 testUrl / expectedStatus / bandwidth,interval 在 5s~24h 内原样保留,否则 120s;没有容差和空闲超时;
//   - failover:keywords / members 强制空;lanes 最多 3 个,id 缺省 lane-<序号>,重复的 id 末尾补 ~ 直到不重复,
//     成员去重,name / icon 缺省空串,manual 严格等于 true 才写;interval 在 5s~24h 内保留否则 300s;tolerance、timeoutMs、
//     failureThreshold、recoveryHoldMs 各有区间和缺省(数组当缺省,null 转成 0 所以区间含 0 的 recoveryHoldMs 得 0),取 floor;
//     restorePrimary 只有布尔 false 才为假;
//   - kind 只由固定 id 决定:builtin-direct / builtin-block,类型强制 selector / static、关键词成员清空、图标空了补默认,
//     用户写的内置出站其余字段原样整理(类型特有字段保留,键序是一般节点组的键序加最后的 kind);
//   - builtinTags:各取列表里最后一个直连 / 拒绝,给出名字和是否启用。

func gany(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("坏 JSON %s: %v", s, err)
	}
	return v
}

func gtext(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	pDirect = `{"id":"builtin-direct","kind":"direct","name":"直连","type":"selector","mode":"static","icon":"misc:dart","keywords":[],"members":[],"enabled":true}`
	pBlock  = `{"id":"builtin-block","kind":"block","name":"拒绝","type":"selector","mode":"static","icon":"misc:cross","keywords":[],"members":[],"enabled":true}`
)

// wrap 把中间的节点组夹进两个补出来的内置出站。
func wrap(mid ...string) string {
	parts := append([]string{pDirect}, mid...)
	parts = append(parts, pBlock)
	return "[" + strings.Join(parts, ",") + "]"
}

// sel 是一个 selector 节点组的期望 JSON;空字段取缺省。
type sel struct {
	id, name, mode, icon, kwMode, kws, mems, extra string
	off                                            bool
	scale                                          int
}

func (s sel) String() string {
	d := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	return fmt.Sprintf(`{"id":%q,"name":%q,"type":"selector","mode":%q,"enabled":%t,"icon":%q,"iconScale":%d,"keywordMode":%q,"keywords":%s,"members":%s%s}`,
		s.id, s.name, d(s.mode, "static"), !s.off, s.icon, s.scale, d(s.kwMode, "or"), d(s.kws, "[]"), d(s.mems, "[]"), s.extra)
}

func TestNormalizeGroupsVectors(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"null", `null`, wrap()},
		{"空数组", `[]`, wrap()},
		{"对象", `{"a":1}`, wrap()},
		{"字符串", `"x"`, wrap()},
		{"数字", `3`, wrap()},
		{"空对象", `[{}]`, wrap(sel{id: "group-0", name: "分组-1"}.String())},
		{"非对象元素按空对象", `[null,5,"x",[1],true]`, wrap(
			sel{id: "group-0", name: "分组-1"}.String(), sel{id: "group-1", name: "分组-2"}.String(), sel{id: "group-2", name: "分组-3"}.String(),
			sel{id: "group-3", name: "分组-4"}.String(), sel{id: "group-4", name: "分组-5"}.String())},
		{"id name trim 与缺省", "[{\"id\":\"\\u3000a \",\"name\":\"\\ufeffN\\t\"},{\"id\":\"  \",\"name\":5}]", wrap(
			sel{id: "a", name: "N"}.String(), sel{id: "group-1", name: "分组-2"}.String())},
		{"id name 中间空白保留", `[{"id":"a  b","name":"x  y"}]`, wrap(sel{id: "a  b", name: "x  y"}.String())},
		// type
		{"type 大写不认", `[{"type":"URLTEST"}]`, wrap(sel{id: "group-0", name: "分组-1"}.String())},
		{"type 带空白不认", `[{"type":" urltest"}]`, wrap(sel{id: "group-0", name: "分组-1"}.String())},
		{"type 非字符串", `[{"type":1}]`, wrap(sel{id: "group-0", name: "分组-1"}.String())},
		{"type 未知", `[{"type":"x"}]`, wrap(sel{id: "group-0", name: "分组-1"}.String())},
		// mode
		{"mode dynamic", `[{"mode":"dynamic"}]`, wrap(sel{id: "group-0", name: "分组-1", mode: "dynamic"}.String())},
		{"mode 未知用 allNodes", `[{"mode":"x","allNodes":true}]`, wrap(sel{id: "group-0", name: "分组-1", mode: "dynamic"}.String())},
		{"mode static 压过 allNodes", `[{"mode":"static","allNodes":true}]`, wrap(sel{id: "group-0", name: "分组-1"}.String())},
		{"allNodes 非 true", `[{"allNodes":1},{"allNodes":"true"}]`, wrap(sel{id: "group-0", name: "分组-1"}.String(), sel{id: "group-1", name: "分组-2"}.String())},
		{"allNodes true", `[{"allNodes":true}]`, wrap(sel{id: "group-0", name: "分组-1", mode: "dynamic"}.String())},
		{"mode 大写不认", `[{"mode":"Dynamic"}]`, wrap(sel{id: "group-0", name: "分组-1"}.String())},
		// enabled / keywordMode
		{"enabled false", `[{"enabled":false}]`, wrap(sel{id: "group-0", name: "分组-1", off: true}.String())},
		{"enabled 假值但不是 false", `[{"enabled":0},{"enabled":null},{"enabled":""},{"enabled":"false"}]`, wrap(
			sel{id: "group-0", name: "分组-1"}.String(), sel{id: "group-1", name: "分组-2"}.String(), sel{id: "group-2", name: "分组-3"}.String(), sel{id: "group-3", name: "分组-4"}.String())},
		{"keywordMode and", `[{"keywordMode":"and"}]`, wrap(sel{id: "group-0", name: "分组-1", kwMode: "and"}.String())},
		{"keywordMode 其余都是 or", `[{"keywordMode":"AND"},{"keywordMode":" and"},{"keywordMode":1}]`, wrap(
			sel{id: "group-0", name: "分组-1"}.String(), sel{id: "group-1", name: "分组-2"}.String(), sel{id: "group-2", name: "分组-3"}.String())},
		// keywords / members
		{"keywords members 只收非空字符串并 trim 不去重", `[{"keywords":[" a ","A","a",1,null,"  ",[]],"members":["x","x"," y "]}]`, wrap(
			sel{id: "group-0", name: "分组-1", kws: `["a","A","a"]`, mems: `["x","x","y"]`}.String())},
		{"keywords members 非数组", `[{"keywords":"a","members":{"0":"x"}}]`, wrap(sel{id: "group-0", name: "分组-1"}.String())},
		// 图标
		{"图标前缀转小写", `[{"icon":"Globe:Earth"},{"icon":" MISC:Dart "},{"icon":"BRAND:X-Y"}]`, wrap(
			sel{id: "group-0", name: "分组-1", icon: "globe:earth"}.String(), sel{id: "group-1", name: "分组-2", icon: "misc:dart"}.String(), sel{id: "group-2", name: "分组-3", icon: "brand:x-y"}.String())},
		{"图标两位字母转大写", `[{"icon":"hk"},{"icon":" Us "},{"icon":"jP"}]`, wrap(
			sel{id: "group-0", name: "分组-1", icon: "HK"}.String(), sel{id: "group-1", name: "分组-2", icon: "US"}.String(), sel{id: "group-2", name: "分组-3", icon: "JP"}.String())},
		{"图标其余原样", `[{"icon":"usa"},{"icon":"h1"},{"icon":"My-Icon"},{"icon":"globe"},{"icon":"emoji:X"},{"icon":"港"}]`, wrap(
			sel{id: "group-0", name: "分组-1", icon: "usa"}.String(), sel{id: "group-1", name: "分组-2", icon: "h1"}.String(), sel{id: "group-2", name: "分组-3", icon: "My-Icon"}.String(),
			sel{id: "group-3", name: "分组-4", icon: "globe"}.String(), sel{id: "group-4", name: "分组-5", icon: "emoji:X"}.String(), sel{id: "group-5", name: "分组-6", icon: "港"}.String())},
		{"图标前缀前面有字符不算", `[{"icon":"Xglobe:Y"},{"icon":" globe:A"}]`, wrap(
			sel{id: "group-0", name: "分组-1", icon: "Xglobe:Y"}.String(), sel{id: "group-1", name: "分组-2", icon: "globe:a"}.String())},
		{"图标一位字母不变", `[{"icon":"h"},{"icon":"hk1"}]`, wrap(
			sel{id: "group-0", name: "分组-1", icon: "h"}.String(), sel{id: "group-1", name: "分组-2", icon: "hk1"}.String())},
		{"图标非字符串或空白", `[{"icon":5},{"icon":"  "},{"icon":null}]`, wrap(
			sel{id: "group-0", name: "分组-1"}.String(), sel{id: "group-1", name: "分组-2"}.String(), sel{id: "group-2", name: "分组-3"}.String())},
		// iconScale
		{"iconScale 整数", `[{"iconScale":5},{"iconScale":-5},{"iconScale":0}]`, wrap(
			sel{id: "group-0", name: "分组-1", scale: 5}.String(), sel{id: "group-1", name: "分组-2", scale: -5}.String(), sel{id: "group-2", name: "分组-3"}.String())},
		{"iconScale 四舍五入取 floor(x+0.5)", `[{"iconScale":5.5},{"iconScale":5.4},{"iconScale":-5.5},{"iconScale":-5.6},{"iconScale":-0.4},{"iconScale":0.49999999999999994}]`, wrap(
			sel{id: "group-0", name: "分组-1", scale: 6}.String(), sel{id: "group-1", name: "分组-2", scale: 5}.String(), sel{id: "group-2", name: "分组-3", scale: -5}.String(),
			sel{id: "group-3", name: "分组-4", scale: -6}.String(), sel{id: "group-4", name: "分组-5"}.String(), sel{id: "group-5", name: "分组-6", scale: 1}.String())},
		{"iconScale 夹在 ±20", `[{"iconScale":20},{"iconScale":21},{"iconScale":1000},{"iconScale":-20},{"iconScale":-21}]`, wrap(
			sel{id: "group-0", name: "分组-1", scale: 20}.String(), sel{id: "group-1", name: "分组-2", scale: 20}.String(), sel{id: "group-2", name: "分组-3", scale: 20}.String(),
			sel{id: "group-3", name: "分组-4", scale: -20}.String(), sel{id: "group-4", name: "分组-5", scale: -20}.String())},
		{"iconScale 字符串与布尔转数字", `[{"iconScale":"7"},{"iconScale":" 3 "},{"iconScale":true},{"iconScale":"0x10"},{"iconScale":false},{"iconScale":null},{"iconScale":""}]`, wrap(
			sel{id: "group-0", name: "分组-1", scale: 7}.String(), sel{id: "group-1", name: "分组-2", scale: 3}.String(), sel{id: "group-2", name: "分组-3", scale: 1}.String(),
			sel{id: "group-3", name: "分组-4", scale: 16}.String(), sel{id: "group-4", name: "分组-5"}.String(), sel{id: "group-5", name: "分组-6"}.String(), sel{id: "group-6", name: "分组-7"}.String())},
		{"iconScale 转不成数得 0", `[{"iconScale":"abc"},{"iconScale":{}},{"iconScale":"Infinity"},{"iconScale":"-Infinity"}]`, wrap(
			sel{id: "group-0", name: "分组-1"}.String(), sel{id: "group-1", name: "分组-2"}.String(), sel{id: "group-2", name: "分组-3"}.String(), sel{id: "group-3", name: "分组-4"}.String())},
		{"iconScale 数组一律 0", `[{"iconScale":[]},{"iconScale":[5]},{"iconScale":["7"]}]`, wrap(
			sel{id: "group-0", name: "分组-1"}.String(), sel{id: "group-1", name: "分组-2"}.String(), sel{id: "group-2", name: "分组-3"}.String())},
		// 内置出站位置与补全
		{"只有拒绝:直连补在最前", `[{"id":"builtin-block","name":"B"}]`,
			"[" + pDirect + `,{"id":"builtin-block","name":"B","type":"selector","mode":"static","enabled":true,"icon":"misc:cross","iconScale":0,"keywordMode":"or","keywords":[],"members":[],"kind":"block"}]`},
		{"只有直连:拒绝补在最后", `[{"id":"builtin-direct","name":"D"},{"id":"x","name":"X"}]`,
			`[{"id":"builtin-direct","name":"D","type":"selector","mode":"static","enabled":true,"icon":"misc:dart","iconScale":0,"keywordMode":"or","keywords":[],"members":[],"kind":"direct"},` +
				sel{id: "x", name: "X"}.String() + "," + pBlock + "]"},
		{"两个都有:按用户排的位置", `[{"id":"builtin-block","name":"B"},{"id":"x","name":"X"},{"id":"builtin-direct","name":"D"}]`,
			`[{"id":"builtin-block","name":"B","type":"selector","mode":"static","enabled":true,"icon":"misc:cross","iconScale":0,"keywordMode":"or","keywords":[],"members":[],"kind":"block"},` +
				sel{id: "x", name: "X"}.String() +
				`,{"id":"builtin-direct","name":"D","type":"selector","mode":"static","enabled":true,"icon":"misc:dart","iconScale":0,"keywordMode":"or","keywords":[],"members":[],"kind":"direct"}]`},
		{"内置 id 前后空白也认", "[{\"id\":\" builtin-direct\\u3000\"}]",
			`[{"id":"builtin-direct","name":"分组-1","type":"selector","mode":"static","enabled":true,"icon":"misc:dart","iconScale":0,"keywordMode":"or","keywords":[],"members":[],"kind":"direct"},` + pBlock + "]"},
		{"内置 id 大小写不认", `[{"id":"Builtin-Direct"}]`, wrap(sel{id: "Builtin-Direct", name: "分组-1"}.String())},
		{"用户内置出站:类型 / 模式 / 关键词 / 成员被强制,图标自留", `[{"id":"builtin-direct","name":"我的直连","type":"failover","mode":"dynamic","allNodes":true,"icon":"HK","iconScale":3,"keywordMode":"and","keywords":["a"],"members":["m"],"enabled":false}]`,
			`[{"id":"builtin-direct","name":"我的直连","type":"selector","mode":"static","enabled":false,"icon":"HK","iconScale":3,"keywordMode":"and","keywords":[],"members":[],` +
				`"lanes":[],"testUrl":"","expectedStatus":"","interval":"300s","tolerance":100,"failover":{"timeoutMs":5000,"failureThreshold":2,"restorePrimary":true,"recoveryHoldMs":60000},"kind":"direct"},` + pBlock + "]"},
		{"用户内置 selector:模式 / 关键词 / 成员被强制", `[{"id":"builtin-direct","name":"D","mode":"dynamic","allNodes":true,"keywords":["a"],"members":["m"]}]`,
			`[{"id":"builtin-direct","name":"D","type":"selector","mode":"static","enabled":true,"icon":"misc:dart","iconScale":0,"keywordMode":"or","keywords":[],"members":[],"kind":"direct"},` + pBlock + "]"},
		{"用户内置出站带 urltest 字段原样保留", `[{"id":"builtin-block","type":"urltest","testUrl":"http://x/","bandwidth":true}]`,
			"[" + pDirect + `,{"id":"builtin-block","name":"分组-1","type":"selector","mode":"static","enabled":true,"icon":"misc:cross","iconScale":0,"keywordMode":"or","keywords":[],"members":[],` +
				`"testUrl":"http://x/","expectedStatus":"","interval":"300s","tolerance":100,"idleTimeout":"12h","bandwidth":true,"kind":"block"}]`},
		{"内置出站下标按原列表", `[{"id":"a"},{"id":"builtin-block"}]`,
			"[" + pDirect + "," + sel{id: "a", name: "分组-1"}.String() +
				`,{"id":"builtin-block","name":"分组-2","type":"selector","mode":"static","enabled":true,"icon":"misc:cross","iconScale":0,"keywordMode":"or","keywords":[],"members":[],"kind":"block"}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := gany(t, c.in)
			before := gtext(t, in)
			if got := gtext(t, NormalizeGroups(in)); got != c.want {
				t.Errorf("不一致\n有: %s\n要: %s", got, c.want)
			}
			if after := gtext(t, in); after != before {
				t.Errorf("改了入参\n前: %s\n后: %s", before, after)
			}
		})
	}
}

func TestNormalizeGroupsUrltestVectors(t *testing.T) {
	ut := func(testUrl, expected, interval, tol, idle, bw string) string {
		return sel{id: "g", name: "G", extra: ""}.String()[:0] +
			fmt.Sprintf(`{"id":"g","name":"G","type":"urltest","mode":"static","enabled":true,"icon":"","iconScale":0,"keywordMode":"or","keywords":[],"members":[],"testUrl":%q,"expectedStatus":%q,"interval":%q,"tolerance":%s,"idleTimeout":%q%s}`,
				testUrl, expected, interval, tol, idle, bw)
	}
	cases := []struct{ name, extra, want string }{
		{"缺省", ``, ut("", "", "300s", "100", "12h", "")},
		{"原样", `"testUrl":" http://a/ ","expectedStatus":"200-299","interval":" 5m ","tolerance":50,"idleTimeout":" 1h "`, ut("http://a/", "200-299", "5m", "50", "1h", "")},
		{"testUrl 非字符串", `"testUrl":5`, ut("", "", "300s", "100", "12h", "")},
		{"expectedStatus 写错得空", `"expectedStatus":"abc"`, ut("", "", "300s", "100", "12h", "")},
		{"expectedStatus 规范写法", `"expectedStatus":"200, 302"`, ut("", "200/302", "300s", "100", "12h", "")},
		{"expectedStatus 星号得空", `"expectedStatus":"*"`, ut("", "", "300s", "100", "12h", "")},
		{"interval 不校验写法", `"interval":"abc"`, ut("", "", "abc", "100", "12h", "")},
		{"interval 空白用缺省", `"interval":"  "`, ut("", "", "300s", "100", "12h", "")},
		{"idleTimeout 空白用缺省", `"idleTimeout":"\t"`, ut("", "", "300s", "100", "12h", "")},
		{"tolerance 向下取整", `"tolerance":50.9`, ut("", "", "300s", "50", "12h", "")},
		{"tolerance 0", `"tolerance":0`, ut("", "", "300s", "0", "12h", "")},
		{"tolerance 负数用缺省", `"tolerance":-1`, ut("", "", "300s", "100", "12h", "")},
		{"tolerance -0.5 用缺省", `"tolerance":-0.5`, ut("", "", "300s", "100", "12h", "")},
		{"tolerance 数字串", `"tolerance":" 70.5 "`, ut("", "", "300s", "70", "12h", "")},
		{"tolerance 乱串用缺省", `"tolerance":"abc"`, ut("", "", "300s", "100", "12h", "")},
		{"tolerance null 转 0", `"tolerance":null`, ut("", "", "300s", "0", "12h", "")},
		{"tolerance 空串转 0", `"tolerance":""`, ut("", "", "300s", "0", "12h", "")},
		{"tolerance true 转 1", `"tolerance":true`, ut("", "", "300s", "1", "12h", "")},
		{"tolerance 数组用缺省", `"tolerance":[5]`, ut("", "", "300s", "100", "12h", "")},
		{"tolerance 空数组用缺省", `"tolerance":[]`, ut("", "", "300s", "100", "12h", "")},
		{"tolerance 大数不设上限", `"tolerance":99999999`, ut("", "", "300s", "99999999", "12h", "")},
		{"bandwidth true", `"bandwidth":true`, ut("", "", "300s", "100", "12h", `,"bandwidth":true`)},
		{"bandwidth 非 true 不写", `"bandwidth":1`, ut("", "", "300s", "100", "12h", "")},
		{"bandwidth 字符串 true 不写", `"bandwidth":"true"`, ut("", "", "300s", "100", "12h", "")},
		{"bandwidth false 不写", `"bandwidth":false`, ut("", "", "300s", "100", "12h", "")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			extra := ""
			if c.extra != "" {
				extra = "," + c.extra
			}
			in := gany(t, `[{"id":"g","name":"G","type":"urltest"`+extra+`}]`)
			if got, want := gtext(t, NormalizeGroups(in)), wrap(c.want); got != want {
				t.Errorf("不一致\n有: %s\n要: %s", got, want)
			}
		})
	}
	// 其他类型带上 urltest 的字段会被忽略
	in := gany(t, `[{"id":"g","name":"G","type":"selector","testUrl":"x","interval":"1s","tolerance":5,"bandwidth":true}]`)
	if got, want := gtext(t, NormalizeGroups(in)), wrap(sel{id: "g", name: "G"}.String()); got != want {
		t.Errorf("selector 不该带 urltest 字段\n有: %s\n要: %s", got, want)
	}
}

func TestNormalizeGroupsLoadBalanceVectors(t *testing.T) {
	lb := func(testUrl, expected, interval, bw string) string {
		return fmt.Sprintf(`{"id":"g","name":"G","type":"loadbalance","mode":"static","enabled":true,"icon":"","iconScale":0,"keywordMode":"or","keywords":[],"members":[],"testUrl":%q,"expectedStatus":%q,"interval":%q%s}`,
			testUrl, expected, interval, bw)
	}
	cases := []struct{ name, extra, want string }{
		{"缺省", ``, lb("", "", "120s", "")},
		{"原样", `"testUrl":" http://a/ ","expectedStatus":"204","interval":" 30s "`, lb("http://a/", "204", "30s", "")},
		{"5s 下界保留", `"interval":"5s"`, lb("", "", "5s", "")},
		{"4s 太小回落", `"interval":"4s"`, lb("", "", "120s", "")},
		{"4999ms 太小回落", `"interval":"4999ms"`, lb("", "", "120s", "")},
		{"5000ms 保留", `"interval":"5000ms"`, lb("", "", "5000ms", "")},
		{"24h 上界保留", `"interval":"24h"`, lb("", "", "24h", "")},
		{"24h1s 太大回落", `"interval":"24h1s"`, lb("", "", "120s", "")},
		{"1440m 保留", `"interval":"1440m"`, lb("", "", "1440m", "")},
		{"纯数字当秒", `"interval":"300"`, lb("", "", "300", "")},
		{"纯数字 4 当秒太小", `"interval":"4"`, lb("", "", "120s", "")},
		{"写错回落", `"interval":"abc"`, lb("", "", "120s", "")},
		{"空白回落", `"interval":"  "`, lb("", "", "120s", "")},
		{"非字符串回落", `"interval":300`, lb("", "", "120s", "")},
		{"带宽", `"bandwidth":true`, lb("", "", "120s", `,"bandwidth":true`)},
		{"没有容差和空闲超时", `"tolerance":5,"idleTimeout":"1h"`, lb("", "", "120s", "")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			extra := ""
			if c.extra != "" {
				extra = "," + c.extra
			}
			in := gany(t, `[{"id":"g","name":"G","type":"loadbalance"`+extra+`}]`)
			if got, want := gtext(t, NormalizeGroups(in)), wrap(c.want); got != want {
				t.Errorf("不一致\n有: %s\n要: %s", got, want)
			}
		})
	}
}

func TestNormalizeGroupsFailoverVectors(t *testing.T) {
	type fo struct{ lanes, testUrl, expected, interval, tol, timeout, threshold, restore, hold, bw string }
	build := func(f fo) string {
		d := func(v, def string) string {
			if v == "" {
				return def
			}
			return v
		}
		return fmt.Sprintf(`{"id":"g","name":"G","type":"failover","mode":"static","enabled":true,"icon":"","iconScale":0,"keywordMode":"or","keywords":[],"members":[],"lanes":%s,"testUrl":%q,"expectedStatus":%q,"interval":%q,"tolerance":%s,"failover":{"timeoutMs":%s,"failureThreshold":%s,"restorePrimary":%s,"recoveryHoldMs":%s}%s}`,
			d(f.lanes, "[]"), f.testUrl, f.expected, d(f.interval, "300s"), d(f.tol, "100"), d(f.timeout, "5000"), d(f.threshold, "2"), d(f.restore, "true"), d(f.hold, "60000"), f.bw)
	}
	cases := []struct {
		name, extra string
		want        fo
	}{
		{"缺省", ``, fo{}},
		{"强制 static、清空关键词成员", `"mode":"dynamic","allNodes":true,"keywords":["a"],"members":["m"]`, fo{}},
		{"检测地址与状态码", `"testUrl":" http://a/ ","expectedStatus":"200-299/302"`, fo{testUrl: "http://a/", expected: "200-299/302"}},
		{"状态码写错得空", `"expectedStatus":"x"`, fo{}},
		{"带宽", `"bandwidth":true`, fo{bw: `,"bandwidth":true`}},
		// interval
		{"interval 5s 保留", `"interval":"5s"`, fo{interval: "5s"}},
		{"interval 4s 回落", `"interval":"4s"`, fo{}},
		{"interval 24h 保留", `"interval":"24h"`, fo{interval: "24h"}},
		{"interval 24h1s 回落", `"interval":"24h1s"`, fo{}},
		{"interval 乱写回落", `"interval":"abc"`, fo{}},
		{"interval 纯数字当秒", `"interval":"60"`, fo{interval: "60"}},
		// tolerance 0~60000 取 floor,否则 100
		{"tolerance 0", `"tolerance":0`, fo{tol: "0"}},
		{"tolerance 60000", `"tolerance":60000`, fo{tol: "60000"}},
		{"tolerance 60001 回落", `"tolerance":60001`, fo{}},
		{"tolerance 负数回落", `"tolerance":-1`, fo{}},
		{"tolerance 小数取整", `"tolerance":250.9`, fo{tol: "250"}},
		{"tolerance null 转 0", `"tolerance":null`, fo{tol: "0"}},
		{"tolerance 没写回落", ``, fo{}},
		{"tolerance 数组回落", `"tolerance":[5]`, fo{}},
		{"tolerance 数字串", `"tolerance":"300"`, fo{tol: "300"}},
		// failover 子对象
		{"timeoutMs 边界", `"failover":{"timeoutMs":1000}`, fo{timeout: "1000"}},
		{"timeoutMs 999 回落", `"failover":{"timeoutMs":999}`, fo{}},
		{"timeoutMs 60000", `"failover":{"timeoutMs":60000}`, fo{timeout: "60000"}},
		{"timeoutMs 60001 回落", `"failover":{"timeoutMs":60001}`, fo{}},
		{"timeoutMs null 转 0 回落", `"failover":{"timeoutMs":null}`, fo{}},
		{"timeoutMs 小数取整", `"failover":{"timeoutMs":2500.7}`, fo{timeout: "2500"}},
		{"failureThreshold 边界", `"failover":{"failureThreshold":1}`, fo{threshold: "1"}},
		{"failureThreshold 0 回落", `"failover":{"failureThreshold":0}`, fo{}},
		{"failureThreshold 20", `"failover":{"failureThreshold":20}`, fo{threshold: "20"}},
		{"failureThreshold 21 回落", `"failover":{"failureThreshold":21}`, fo{}},
		{"restorePrimary false", `"failover":{"restorePrimary":false}`, fo{restore: "false"}},
		{"restorePrimary 假值但不是 false", `"failover":{"restorePrimary":0}`, fo{}},
		{"restorePrimary 字符串 false", `"failover":{"restorePrimary":"false"}`, fo{}},
		{"restorePrimary null", `"failover":{"restorePrimary":null}`, fo{}},
		{"recoveryHoldMs 0 保留", `"failover":{"recoveryHoldMs":0}`, fo{hold: "0"}},
		{"recoveryHoldMs null 转 0", `"failover":{"recoveryHoldMs":null}`, fo{hold: "0"}},
		{"recoveryHoldMs 没写回落", `"failover":{}`, fo{}},
		{"recoveryHoldMs 上界", `"failover":{"recoveryHoldMs":86400000}`, fo{hold: "86400000"}},
		{"recoveryHoldMs 超界回落", `"failover":{"recoveryHoldMs":86400001}`, fo{}},
		{"recoveryHoldMs 负数回落", `"failover":{"recoveryHoldMs":-1}`, fo{}},
		{"failover 不是对象", `"failover":"x"`, fo{}},
		{"failover 是数组", `"failover":[{"timeoutMs":2000}]`, fo{}},
		{"failover 数组值回落", `"failover":{"timeoutMs":[2000],"failureThreshold":[3]}`, fo{}},
		// lanes
		{"lanes 非数组", `"lanes":{"0":{"id":"a"}}`, fo{}},
		{"lanes 全缺省", `"lanes":[{},null,5]`, fo{lanes: `[{"id":"lane-1","name":"","icon":"","members":[]},{"id":"lane-2","name":"","icon":"","members":[]},{"id":"lane-3","name":"","icon":"","members":[]}]`}},
		{"lanes 最多 3 个", `"lanes":[{"id":"a"},{"id":"b"},{"id":"c"},{"id":"d"}]`, fo{lanes: `[{"id":"a","name":"","icon":"","members":[]},{"id":"b","name":"","icon":"","members":[]},{"id":"c","name":"","icon":"","members":[]}]`}},
		{"lane 字段", `"lanes":[{"id":" a ","name":" 主线 ","icon":" hk ","members":[" n1 ","n1","n2",1,null,"  "],"manual":true}]`, fo{lanes: `[{"id":"a","name":"主线","icon":"hk","members":["n1","n2"],"manual":true}]`}},
		{"lane manual 非 true 不写", `"lanes":[{"id":"a","manual":1},{"id":"b","manual":"true"},{"id":"c","manual":false}]`, fo{lanes: `[{"id":"a","name":"","icon":"","members":[]},{"id":"b","name":"","icon":"","members":[]},{"id":"c","name":"","icon":"","members":[]}]`}},
		{"lane id 重复补波浪线", `"lanes":[{"id":"a"},{"id":"a"},{"id":"a"}]`, fo{lanes: `[{"id":"a","name":"","icon":"","members":[]},{"id":"a~","name":"","icon":"","members":[]},{"id":"a~~","name":"","icon":"","members":[]}]`}},
		{"lane 缺省 id 与显式 id 撞车", `"lanes":[{"id":"lane-2"},{}]`, fo{lanes: `[{"id":"lane-2","name":"","icon":"","members":[]},{"id":"lane-2~","name":"","icon":"","members":[]}]`}},
		{"lane id 撞车后的波浪线也要不重复", `"lanes":[{"id":"a~"},{"id":"a"},{"id":"a"}]`, fo{lanes: `[{"id":"a~","name":"","icon":"","members":[]},{"id":"a","name":"","icon":"","members":[]},{"id":"a~~","name":"","icon":"","members":[]}]`}},
		{"lane 成员非数组", `"lanes":[{"id":"a","members":"n1"}]`, fo{lanes: `[{"id":"a","name":"","icon":"","members":[]}]`}},
		{"lane 名字图标非字符串", `"lanes":[{"id":"a","name":5,"icon":[]}]`, fo{lanes: `[{"id":"a","name":"","icon":"","members":[]}]`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			extra := ""
			if c.extra != "" {
				extra = "," + c.extra
			}
			in := gany(t, `[{"id":"g","name":"G","type":"failover"`+extra+`}]`)
			if got, want := gtext(t, NormalizeGroups(in)), wrap(build(c.want)); got != want {
				t.Errorf("不一致\n有: %s\n要: %s", got, want)
			}
		})
	}
}

func TestBuiltinTagsVectors(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"没有", `[]`, `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`},
		{"null", `null`, `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`},
		{"改名与停用", `[{"id":"builtin-direct","name":"D","enabled":false},{"id":"builtin-block","name":"B"}]`, `{"direct":"D","block":"B","directEnabled":false,"blockEnabled":true}`},
		{"只改一个", `[{"id":"builtin-block","name":"B","enabled":false}]`, `{"direct":"直连","block":"B","directEnabled":true,"blockEnabled":false}`},
		{"名字缺省按下标", `[{"id":"x"},{"id":"builtin-direct"}]`, `{"direct":"分组-2","block":"拒绝","directEnabled":true,"blockEnabled":true}`},
		{"重复的内置 id 取最后一个", `[{"id":"builtin-direct","name":"D1","enabled":false},{"id":"builtin-direct","name":"D2"},{"id":"builtin-block","name":"B1"},{"id":"builtin-block","name":"B2","enabled":false}]`,
			`{"direct":"D2","block":"B2","directEnabled":true,"blockEnabled":false}`},
		{"普通组名字叫直连不算", `[{"id":"x","name":"直连","enabled":false}]`, `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := gany(t, c.in)
			if got := gtext(t, BuiltinTags(in)); got != c.want {
				t.Errorf("BuiltinTags 不一致\n有: %s\n要: %s", got, c.want)
			}
			if got := gtext(t, builtinTags(NormalizeGroups(in))); got != c.want {
				t.Errorf("builtinTags 不一致\n有: %s\n要: %s", got, c.want)
			}
		})
	}
}

func TestGroupsHandlerVectors(t *testing.T) {
	raw := func(s string) []json.RawMessage { return []json.RawMessage{json.RawMessage(s)} }
	// 缺参数当 null
	for name, args := range map[string][]json.RawMessage{"没有参数": nil, "null": raw(`null`), "空数组": raw(`[]`)} {
		res, err := hNormalizeGroups(nil, args)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := gtext(t, res); got != wrap() {
			t.Errorf("%s: normalizeGroups 不一致\n有: %s\n要: %s", name, got, wrap())
		}
		res, err = hBuiltinTags(nil, args)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := gtext(t, res); got != `{"direct":"直连","block":"拒绝","directEnabled":true,"blockEnabled":true}` {
			t.Errorf("%s: builtinTags 不一致 %s", name, got)
		}
	}
	res, err := hNormalizeGroups(nil, raw(`[{"id":"a","name":"A","iconScale":"3"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := gtext(t, res), wrap(sel{id: "a", name: "A", scale: 3}.String()); got != want {
		t.Errorf("normalizeGroups 不一致\n有: %s\n要: %s", got, want)
	}
	res, err = hBuiltinTags(nil, raw(`[{"id":"builtin-direct","name":"D","enabled":false}]`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := gtext(t, res), `{"direct":"D","block":"拒绝","directEnabled":false,"blockEnabled":true}`; got != want {
		t.Errorf("builtinTags 不一致\n有: %s\n要: %s", got, want)
	}
	if _, err := hNormalizeGroups(nil, raw(`{`)); err == nil {
		t.Error("坏 JSON 参数该报错")
	}
}
