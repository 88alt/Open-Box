package routing

import (
	"reflect"
	"testing"
)

// 自写测试向量:输入按 outbound.go 注释里写的规则自己拟,期望值人工推算,不来自别的程序的输出。
// ListTagForUrl 的哈希值用独立的 Python 小脚本按 FNV-1a 32 位(逐个 UTF-16 码元)另算一遍核对。
// export.go 只是把包内函数露出去,这里一并用手写期望值把每个出口各点一遍。

func TestParseBuiltin(t *testing.T) {
	def := builtinTags{"direct", "block", true, true}
	// 不是对象:整个回落成默认(两个开关为真)
	for _, v := range []any{nil, "x", 3.0, true, []any{}, []any{"direct"}} {
		if got := parseBuiltin(v); got != def {
			t.Errorf("parseBuiltin(%#v) = %#v,想要默认 %#v", v, got, def)
		}
	}
	// 是对象:缺的键不补默认,名字空串、开关为假
	if got, want := parseBuiltin(map[string]any{}), (builtinTags{}); got != want {
		t.Errorf("空对象:got %#v,想要 %#v", got, want)
	}
	cases := []struct {
		name string
		in   map[string]any
		want builtinTags
	}{
		{"改名并启用", map[string]any{"direct": "直连", "block": "拒绝", "directEnabled": true, "blockEnabled": true}, builtinTags{"直连", "拒绝", true, true}},
		{"开关按 JS 真假:非空串、非零数为真", map[string]any{"direct": "d", "block": "b", "directEnabled": "x", "blockEnabled": 2.0}, builtinTags{"d", "b", true, true}},
		{"开关按 JS 真假:空串、0、nil、false 为假", map[string]any{"direct": "d", "block": "b", "directEnabled": "", "blockEnabled": 0.0}, builtinTags{"d", "b", false, false}},
		{"开关为 nil / false", map[string]any{"direct": "d", "block": "b", "directEnabled": nil, "blockEnabled": false}, builtinTags{"d", "b", false, false}},
		{"空数组、空对象在 JS 里是真值", map[string]any{"directEnabled": []any{}, "blockEnabled": map[string]any{}}, builtinTags{"", "", true, true}},
		{"名字不是字符串:当空串", map[string]any{"direct": 3.0, "block": nil, "directEnabled": true}, builtinTags{"", "", true, false}},
	}
	for _, c := range cases {
		if got := parseBuiltin(c.in); got != c.want {
			t.Errorf("%s:got %#v,想要 %#v", c.name, got, c.want)
		}
	}
}

func TestParseMembers(t *testing.T) {
	// 缺省或不是数组:按 ["direct"]
	for _, v := range []any{nil, "a", 1.0, true, map[string]any{}} {
		if got := parseMembers(v); !reflect.DeepEqual(got, []string{"direct"}) {
			t.Errorf("parseMembers(%#v) = %#v,想要 [direct]", v, got)
		}
	}
	// 空数组:非 nil 的空切片(序列化成 [])
	if got := parseMembers([]any{}); got == nil || len(got) != 0 {
		t.Errorf("空数组:got %#v", got)
	}
	// 非字符串成员占位成空串,位置不动
	got := parseMembers([]any{"a", 3.0, nil, "b", true})
	if want := []string{"a", "", "", "b", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("混排:got %#v,想要 %#v", got, want)
	}
}

func TestParseSelections(t *testing.T) {
	m := map[string]any{"P": "美国", "Q": nil}
	if got := parseSelections(m); !reflect.DeepEqual(got, m) {
		t.Errorf("对象原样带出:got %#v", got)
	}
	for _, v := range []any{nil, "x", 1.0, []any{"P"}, true} {
		got := parseSelections(v)
		if got == nil || len(got) != 0 {
			t.Errorf("parseSelections(%#v) = %#v,想要非 nil 空表", v, got)
		}
	}
}

func TestEffectiveOutbound(t *testing.T) {
	b := builtinTags{direct: "direct", block: "block"}
	rn := builtinTags{direct: "直连", block: "拒绝"}
	cases := []struct {
		name    string
		def     string
		members []string
		b       builtinTags
		want    string
	}{
		{"默认指向成员:原样", "美国", []string{"direct", "美国"}, b, "美国"},
		{"direct 在成员里:不因排第二而落到第一项", "direct", []string{"美国", "direct"}, b, "direct"},
		{"block 在成员里", "block", []string{"direct", "block"}, b, "block"},
		{"direct 改名后换成新名字", "direct", []string{"美国", "直连"}, rn, "直连"},
		{"direct 改名后成员里只有旧名字:落第一项", "direct", []string{"direct", "美国"}, rn, "direct"},
		{"block 改名后换成新名字", "block", []string{"拒绝", "美国"}, rn, "拒绝"},
		{"默认指向已不存在的组:落第一项", "已删除的组", []string{"美国", "香港"}, b, "美国"},
		{"默认空着:落第一项", "", []string{"香港", "美国"}, b, "香港"},
		{"区分大小写", "US", []string{"美国", "us"}, b, "美国"},
		{"proxy:取第一个非内置成员", "proxy", []string{"direct", "block", "美国", "香港"}, b, "美国"},
		{"proxy:内置成员全在前也跳过", "proxy", []string{"block", "direct", "香港"}, b, "香港"},
		{"proxy:只有内置成员:落第一项", "proxy", []string{"direct", "block"}, b, "direct"},
		{"proxy:只有内置成员且 block 在前", "proxy", []string{"block", "direct"}, b, "block"},
		{"proxy 本身就是成员:直接命中", "proxy", []string{"direct", "proxy", "美国"}, b, "proxy"},
		{"proxy:改名后按新名字判内置", "proxy", []string{"直连", "拒绝", "美国"}, rn, "美国"},
		{"proxy:改名后旧名字不算内置", "proxy", []string{"direct", "美国"}, rn, "direct"},
		{"空成员", "direct", nil, b, ""},
		{"空成员(非 nil)", "proxy", []string{}, b, ""},
		{"内置名字是空串且成员里有空串:命中它", "direct", []string{"", "x"}, builtinTags{}, ""},
		{"内置名字是空串、成员里没有:落第一项", "direct", []string{"x", "y"}, builtinTags{}, "x"},
	}
	for _, c := range cases {
		if got := effectiveOutbound(c.def, c.members, c.b); got != c.want {
			t.Errorf("%s:got %q,想要 %q", c.name, got, c.want)
		}
	}
}

func TestResolveSelectionLeaf(t *testing.T) {
	const nonString = "\x00non-string"
	chain := map[string]any{}
	for i := 0; i < 20; i++ { // n0→n1→…→n20
		chain["n"+itoa(i)] = "n" + itoa(i+1)
	}
	cases := []struct {
		name string
		sel  map[string]any
		from string
		want string
	}{
		{"没有这一项:原名", map[string]any{"x": "y"}, "P", "P"},
		{"nil 表:原名", nil, "P", "P"},
		{"一跳", map[string]any{"P": "美国"}, "P", "美国"},
		{"两跳,末端没有选择", map[string]any{"P": "组A", "组A": "香港"}, "P", "香港"},
		{"三跳", map[string]any{"a": "b", "b": "c", "c": "d"}, "a", "d"},
		{"从中间开始", map[string]any{"a": "b", "b": "c", "c": "d"}, "b", "d"},
		{"两个互指:回到起点", map[string]any{"a": "b", "b": "a"}, "a", "a"},
		{"两个互指:从另一头", map[string]any{"a": "b", "b": "a"}, "b", "b"},
		{"三个绕圈", map[string]any{"a": "b", "b": "c", "c": "a"}, "a", "a"},
		{"指向自己", map[string]any{"a": "a"}, "a", "a"},
		{"不在环上的入口走进环:停在第二次遇到的点", map[string]any{"s": "a", "a": "b", "b": "a"}, "s", "a"},
		{"选择值是数字", map[string]any{"a": 3.0}, "a", nonString},
		{"选择值是 nil", map[string]any{"a": nil}, "a", nonString},
		{"途中遇到非字符串", map[string]any{"a": "b", "b": nil}, "a", nonString},
		{"选择值是数组", map[string]any{"a": []any{"b"}}, "a", nonString},
		{"名字是空串", map[string]any{"": "x"}, "", "x"},
		{"选择值是空串:当普通名字停下", map[string]any{"a": ""}, "a", ""},
		{"最多下钻 16 层", chain, "n0", "n16"},
		{"从 n3 起也只下钻 16 层(n19 本来还有下一跳)", chain, "n3", "n19"},
		{"不到 16 层走到链尾:停在链尾", chain, "n5", "n20"},
		{"恰好 16 跳走到链尾", chain, "n4", "n20"},
	}
	for _, c := range cases {
		if got := resolveSelectionLeaf(c.sel, c.from); got != c.want {
			t.Errorf("%s:got %q,想要 %q", c.name, got, c.want)
		}
	}
}

func TestPolicyChosenOutbound(t *testing.T) {
	const nonString = "\x00non-string"
	b := builtinTags{direct: "direct", block: "block"}
	members := []string{"direct", "美国"}
	cases := []struct {
		name string
		sel  map[string]any
		def  string
		want string
	}{
		{"没有选择:走默认", map[string]any{}, "美国", "美国"},
		{"没有选择:默认 direct 且在成员里", nil, "direct", "direct"},
		{"没有选择:默认不在成员里落第一项", map[string]any{}, "没了", "direct"},
		{"别的名字的选择不影响", map[string]any{"Q": "美国"}, "direct", "direct"},
		{"有选择:用选择", map[string]any{"P": "美国"}, "direct", "美国"},
		{"有选择:下钻到叶子", map[string]any{"P": "组A", "组A": "香港"}, "direct", "香港"},
		{"选择的键在但值是 nil:不走默认,返回非字符串占位", map[string]any{"P": nil}, "direct", nonString},
		{"选择值不在成员里也照用", map[string]any{"P": "不存在"}, "direct", "不存在"},
	}
	for _, c := range cases {
		if got := policyChosenOutbound("P", c.def, members, b, c.sel); got != c.want {
			t.Errorf("%s:got %q,想要 %q", c.name, got, c.want)
		}
	}
}

func TestPolicyGoesDirect(t *testing.T) {
	b := builtinTags{direct: "direct", block: "block"}
	rn := builtinTags{direct: "直连", block: "拒绝"}
	cases := []struct {
		name    string
		sel     map[string]any
		def     string
		members []string
		b       builtinTags
		want    bool
	}{
		{"选择就是直连", map[string]any{"P": "direct"}, "美国", []string{"美国"}, b, true},
		{"选择是节点", map[string]any{"P": "美国"}, "direct", []string{"direct", "美国"}, b, false},
		{"选择是拒绝", map[string]any{"P": "block"}, "direct", []string{"direct", "block"}, b, false},
		{"下钻后到直连", map[string]any{"P": "组A", "组A": "direct"}, "美国", []string{"美国"}, b, true},
		{"没有选择:默认直连", map[string]any{}, "direct", []string{"美国", "direct"}, b, true},
		{"没有选择:默认节点", map[string]any{}, "美国", []string{"direct", "美国"}, b, false},
		{"没有选择:默认丢了落第一项恰好是直连", map[string]any{}, "没了", []string{"direct", "美国"}, b, true},
		{"改名后旧名字不算直连", map[string]any{"P": "direct"}, "美国", []string{"direct"}, rn, false},
		{"改名后新名字算直连", map[string]any{"P": "直连"}, "美国", []string{"美国"}, rn, true},
		{"选择值不是字符串:不算直连", map[string]any{"P": 1.0}, "direct", []string{"direct"}, b, false},
		{"空成员且直连名字为空串:空等于空,算直连", map[string]any{}, "direct", nil, builtinTags{}, true},
	}
	for _, c := range cases {
		if got := policyGoesDirect("P", c.def, c.members, c.b, c.sel); got != c.want {
			t.Errorf("%s:got %v,想要 %v", c.name, got, c.want)
		}
	}
}

func TestCustomPolicyActive(t *testing.T) {
	one := []CustomRule{{Type: "geosite", Value: "cn", Outbound: "direct"}}
	cases := []struct {
		name string
		c    CustomPolicy
		want bool
	}{
		{"启用且有规则", CustomPolicy{Enabled: true, Rules: one}, true},
		{"启用但没有规则(nil)", CustomPolicy{Enabled: true}, false},
		{"启用但规则为空切片", CustomPolicy{Enabled: true, Rules: []CustomRule{}}, false},
		{"有规则但没启用", CustomPolicy{Rules: one}, false},
		{"都没有", CustomPolicy{}, false},
		{"两条规则", CustomPolicy{Enabled: true, Rules: append(one, one...)}, true},
	}
	for _, c := range cases {
		if got := customPolicyActive(c.c); got != c.want {
			t.Errorf("%s:got %v,想要 %v", c.name, got, c.want)
		}
	}
}

func TestCustomOutboundTag(t *testing.T) {
	b := builtinTags{direct: "direct", block: "block"}
	rn := builtinTags{direct: "直连", block: "拒绝"}
	cases := []struct {
		name string
		out  string
		b    builtinTags
		want string
	}{
		{"direct 换成当前直连名", "direct", rn, "直连"},
		{"block 换成当前拒绝名", "block", rn, "拒绝"},
		{"没改名时原样", "direct", b, "direct"},
		{"节点组名原样", "美国", rn, "美国"},
		{"大小写敏感:Direct 不是内置", "Direct", rn, "Direct"},
		{"改名后的新名字再传进来也原样", "直连", rn, "直连"},
		{"空串原样", "", rn, ""},
		{"内置名字是空串时 direct 换成空串", "direct", builtinTags{}, ""},
	}
	for _, c := range cases {
		if got := customOutboundTag(CustomRule{Outbound: c.out}, c.b); got != c.want {
			t.Errorf("%s:got %q,想要 %q", c.name, got, c.want)
		}
	}
}

func TestCustomRuleTag(t *testing.T) {
	cases := []struct {
		name string
		r    CustomRule
		want string
	}{
		{"geosite", CustomRule{Type: "geosite", Value: "cn"}, "geosite-cn"},
		{"geoip", CustomRule{Type: "geoip", Value: "cn"}, "geoip-cn"},
		{"geosite 值为空:只剩连字符", CustomRule{Type: "geosite", Value: ""}, "geosite-"},
		{"ruleset 原样用值", CustomRule{Type: "ruleset", Value: "my-set"}, "my-set"},
		{"ruleset 值为空", CustomRule{Type: "ruleset"}, ""},
		{"ruleUrl:FNV-1a 32 位(逐码元)", CustomRule{Type: "ruleUrl", Value: "https://a.com/x.list"}, "list-67c9a30d"},
		{"ruleUrl:前后空白先去掉", CustomRule{Type: "ruleUrl", Value: "  https://a.com/x.list\t\n"}, "list-67c9a30d"},
		{"ruleUrl:含中文按 UTF-16 码元", CustomRule{Type: "ruleUrl", Value: "https://例.com/清单"}, "list-c13782c7"},
		{"ruleUrl:空白串得空标签", CustomRule{Type: "ruleUrl", Value: " \t "}, ""},
		{"ruleUrl:空串得空标签", CustomRule{Type: "ruleUrl"}, ""},
		{"别的类型得空串", CustomRule{Type: "domain", Value: "a.com"}, ""},
		{"类型空串", CustomRule{Value: "cn"}, ""},
		{"类型大小写敏感", CustomRule{Type: "GeoSite", Value: "cn"}, ""},
	}
	for _, c := range cases {
		if got := customRuleTag(c.r); got != c.want {
			t.Errorf("%s:got %q,想要 %q", c.name, got, c.want)
		}
	}
}

func TestExportBuiltin(t *testing.T) {
	var _ builtinTags = Builtin{} // 别名,不是新类型
	b := NewBuiltin("直连", "拒绝", true, false)
	if b != (builtinTags{"直连", "拒绝", true, false}) {
		t.Errorf("NewBuiltin:got %#v", b)
	}
	if b.Direct() != "直连" || b.Block() != "拒绝" {
		t.Errorf("Direct/Block:got %q / %q", b.Direct(), b.Block())
	}
	// 两个开关不会被互换或丢掉:用不同的值各造一次
	if got := NewBuiltin("d", "b", false, true); got.directEnabled || !got.blockEnabled || got.direct != "d" || got.block != "b" {
		t.Errorf("参数顺序:got %#v", got)
	}
}

func TestExportWrappers(t *testing.T) {
	rn := NewBuiltin("直连", "拒绝", true, true)
	if got := EffectiveOutbound("direct", []string{"美国", "直连"}, rn); got != "直连" {
		t.Errorf("EffectiveOutbound:got %q", got)
	}
	if got := EffectiveOutbound("proxy", []string{"直连", "拒绝", "香港"}, rn); got != "香港" {
		t.Errorf("EffectiveOutbound(proxy):got %q", got)
	}
	noDirect := NewBuiltin("direct", "block", false, true)
	if got, want := PolicyOutboundOptions([]string{"direct", "美国", "block"}, noDirect), []string{"美国", "block"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PolicyOutboundOptions:got %#v,想要 %#v", got, want)
	}
	if got, want := PolicyOutboundOptions(nil, rn), []string{"直连"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PolicyOutboundOptions(空):got %#v,想要 %#v", got, want)
	}
	cp := CustomPolicy{Enabled: true, Rules: []CustomRule{{Type: "geoip", Value: "cn", Outbound: "block"}}}
	if !CustomPolicyActive(cp) || CustomPolicyActive(CustomPolicy{Rules: cp.Rules}) {
		t.Error("CustomPolicyActive 开关或规则判断不对")
	}
	if got := CustomOutboundTag(CustomRule{Outbound: "block"}, rn); got != "拒绝" {
		t.Errorf("CustomOutboundTag(block):got %q", got)
	}
	if got := CustomOutboundTag(CustomRule{Outbound: "美国"}, rn); got != "美国" {
		t.Errorf("CustomOutboundTag(节点组):got %q", got)
	}
	if got := ParseRuleLists(nil); got == nil || len(got) != 0 {
		t.Errorf("ParseRuleLists(nil):got %#v,想要非 nil 空表", got)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for ; i > 0; i /= 10 {
		s = string(rune('0'+i%10)) + s
	}
	return s
}
