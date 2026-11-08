package routing

import (
	"bytes"
	"regexp"
	"testing"
)

// 自写测试向量:输入按 fingerprint.go 注释里写的规则自己拟,期望的 JSON 文本逐字手写(按 JS 的
// JSON.stringify 规则:无空白、键序固定、U+2028/2029 不转义);指纹的十六进制值用独立的 Python 小脚本
// 按同样的 JSON 文本另算 sha256 前 16 位核对,不来自别的程序的输出。
// golden_test 里 routingFingerprint 的条目仍作回归。

func TestQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", `""`},
		{"普通字符", "abc 123", `"abc 123"`},
		{"双引号", `a"b`, `"a\"b"`},
		{"反斜杠", `a\b`, `"a\\b"`},
		{"反斜杠在引号前", `\"`, `"\\\""`},
		{"五个短转义", "\b\f\n\r\t", `"\b\f\n\r\t"`},
		{"NUL", "\x00", `"\u0000"`},
		{"其余控制符写成小写十六进制", "\x01\x0b\x0e\x1b\x1f", `"\u0001\u000b\u000e\u001b\u001f"`},
		{"0x20 起不转义", " !", `" !"`},
		{"斜杠不转义", "a/b", `"a/b"`},
		{"DEL(0x7f)不转义", "\x7f", "\"\x7f\""},
		{"拉丁扩展不转义", "é", `"é"`},
		{"中文原样", "中文", `"中文"`},
		{"emoji 原样(JS 里是合法代理对)", "😀", `"😀"`},
		{"U+2028 / U+2029 原样,不像 Go 的 encoding/json 那样转义", "a\u2028b\u2029c", "\"a\u2028b\u2029c\""},
		{"合法的 U+FFFD 原样", "\ufffd", "\"\ufffd\""},
		// 有意保留的差异:非法 UTF-8 在 JS 里是孤立代理(写成 \udxxx),Go 拿不到原值,统一按替换符输出
		{"非法 UTF-8 按替换符", "a\xffb", "\"a\ufffdb\""},
		{"混合", "\"\\\n中", `"\"\\\n中"`},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		quote(&buf, c.in)
		if got := buf.String(); got != c.want {
			t.Errorf("%s:quote(%q) = %s,想要 %s", c.name, c.in, got, c.want)
		}
	}
}

func TestWriteHelpers(t *testing.T) {
	w := func(f func(*bytes.Buffer)) string {
		var buf bytes.Buffer
		f(&buf)
		return buf.String()
	}
	strs := []struct {
		name string
		in   []string
		want string
	}{
		{"nil 写成 []", nil, `[]`},
		{"空切片写成 []", []string{}, `[]`},
		{"一项", []string{"a"}, `["a"]`},
		{"多项保序,逗号后没有空格", []string{"b", "a", "b"}, `["b","a","b"]`},
		{"项里的特殊字符转义", []string{`x"y`, "\n"}, `["x\"y","\n"]`},
		{"空串项保留", []string{"", "a"}, `["","a"]`},
	}
	for _, c := range strs {
		if got := w(func(b *bytes.Buffer) { writeStrs(b, c.in) }); got != c.want {
			t.Errorf("writeStrs %s:got %s,想要 %s", c.name, got, c.want)
		}
	}
	if got := w(func(b *bytes.Buffer) { writeBool(b, true) }); got != "true" {
		t.Errorf("writeBool(true) = %s", got)
	}
	if got := w(func(b *bytes.Buffer) { writeBool(b, false) }); got != "false" {
		t.Errorf("writeBool(false) = %s", got)
	}
	if got := w(func(b *bytes.Buffer) { writeField(b, "id", true) }); got != `"id":` {
		t.Errorf("writeField(first) = %s", got)
	}
	if got := w(func(b *bytes.Buffer) { writeField(b, "name", false) }); got != `,"name":` {
		t.Errorf("writeField(not first) = %s", got)
	}
	if got := w(func(b *bytes.Buffer) { writeField(b, `a"b`, false) }); got != `,"a\"b":` {
		t.Errorf("writeField 键名也要转义:%s", got)
	}
}

func TestWritePolicy(t *testing.T) {
	full := Policy{
		ID: "p1", Name: "名", Icon: "i", Default: "direct", Enabled: true,
		RuleUrls: []string{"https://a.com/x"}, Rulesets: []string{"geosite-cn"}, Domain: []string{"a.com"},
		DomainSuffix: nil, DomainKeyword: []string{}, IPCidr: []string{"1.1.1.0/24"},
		Notes: map[string]string{"domain:a.com": "备注"},
	}
	const wantFull = `{"id":"p1","name":"名","icon":"i","enabled":true,"ruleUrls":["https://a.com/x"],"rulesets":["geosite-cn"],"domain":["a.com"],"domainSuffix":[],"domainKeyword":[],"ipCidr":["1.1.1.0/24"]}`
	withMissing := full
	withMissing.MissingRulesets = []string{"geosite-x", "list-0a"}
	disabled := Policy{ID: "q", Name: "停用", Enabled: false}
	cases := []struct {
		name string
		p    Policy
		want string
	}{
		{"default 和 notes 不进输出", full, wantFull},
		{"default 变了输出不变", Policy{ID: "p1", Name: "名", Icon: "i", Default: "别的", Enabled: true,
			RuleUrls: full.RuleUrls, Rulesets: full.Rulesets, Domain: full.Domain, IPCidr: full.IPCidr}, wantFull},
		{"missingRulesets 非空才带,放在最后", withMissing, wantFull[:len(wantFull)-1] + `,"missingRulesets":["geosite-x","list-0a"]}`},
		{"missingRulesets 是空切片不带", Policy{ID: "q", Name: "停用", MissingRulesets: []string{}},
			`{"id":"q","name":"停用","icon":"","enabled":false,"ruleUrls":[],"rulesets":[],"domain":[],"domainSuffix":[],"domainKeyword":[],"ipCidr":[]}`},
		{"停用与全空", disabled,
			`{"id":"q","name":"停用","icon":"","enabled":false,"ruleUrls":[],"rulesets":[],"domain":[],"domainSuffix":[],"domainKeyword":[],"ipCidr":[]}`},
		{"字符串里的特殊字符转义", Policy{ID: `a"b`, Name: "x\ny", Icon: `\`},
			`{"id":"a\"b","name":"x\ny","icon":"\\","enabled":false,"ruleUrls":[],"rulesets":[],"domain":[],"domainSuffix":[],"domainKeyword":[],"ipCidr":[]}`},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		writePolicy(&buf, c.p)
		if got := buf.String(); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
	// writePolicies:方括号、逗号、保序;空 / nil 写成 []
	var buf bytes.Buffer
	writePolicies(&buf, nil)
	if buf.String() != `[]` {
		t.Errorf("writePolicies(nil) = %s", buf.String())
	}
	buf.Reset()
	writePolicies(&buf, []Policy{})
	if buf.String() != `[]` {
		t.Errorf("writePolicies(空) = %s", buf.String())
	}
	const wantDisabled = `{"id":"q","name":"停用","icon":"","enabled":false,"ruleUrls":[],"rulesets":[],"domain":[],"domainSuffix":[],"domainKeyword":[],"ipCidr":[]}`
	buf.Reset()
	writePolicies(&buf, []Policy{disabled, full})
	if want := `[` + wantDisabled + `,` + wantFull + `]`; buf.String() != want {
		t.Errorf("writePolicies 两项:\n got %s\nwant %s", buf.String(), want)
	}
}

func TestStringify(t *testing.T) {
	const pol = `{"id":"p1","name":"名","icon":"i","enabled":true,"ruleUrls":[],"rulesets":["geosite-cn"],"domain":[],"domainSuffix":[],"domainKeyword":[],"ipCidr":[]}`
	p := Policy{ID: "p1", Name: "名", Icon: "i", Default: "x", Enabled: true, Rulesets: []string{"geosite-cn"},
		Notes: map[string]string{"k": "v"}}
	cases := []struct {
		name string
		r    Routing
		want string
	}{
		{"全空", Routing{},
			`{"custom":{"name":"","icon":"","iconScale":0,"enabled":false,"rules":[]},"policies":[],"activePolicies":[],"fallback":{"name":"","icon":""}}`},
		{"fallback.default 与规则备注不进输出",
			Routing{
				Custom:   CustomPolicy{Name: "C", Icon: "ci", Enabled: true, Rules: []CustomRule{{Type: "domain", Value: "a.com", Outbound: "direct", Note: "只给界面看"}}},
				Fallback: Fallback{Name: "F", Icon: "fi", Default: "proxy"},
			},
			`{"custom":{"name":"C","icon":"ci","iconScale":0,"enabled":true,"rules":[{"type":"domain","value":"a.com","outbound":"direct"}]},"policies":[],"activePolicies":[],"fallback":{"name":"F","icon":"fi"}}`},
		{"三处 missingRulesets 都有,位置各在末尾",
			Routing{
				Custom: CustomPolicy{Name: "C", Icon: "ci", IconScale: 2, Enabled: true,
					Rules:           []CustomRule{{Type: "geosite", Value: "cn", Outbound: "direct"}, {Type: "port", Value: "443", Outbound: "美国"}},
					MissingRulesets: []string{"geosite-gone"}},
				Policies:        []Policy{p},
				ActivePolicies:  []Policy{p},
				Fallback:        Fallback{Name: "F", Icon: "fi"},
				MissingRulesets: []MissingRuleset{{"geosite-gone", "C"}, {"list-0001", "P"}},
			},
			`{"custom":{"name":"C","icon":"ci","iconScale":2,"enabled":true,"rules":[{"type":"geosite","value":"cn","outbound":"direct"},{"type":"port","value":"443","outbound":"美国"}],"missingRulesets":["geosite-gone"]},` +
				`"policies":[` + pol + `],"activePolicies":[` + pol + `],"fallback":{"name":"F","icon":"fi"},` +
				`"missingRulesets":[{"tag":"geosite-gone","owner":"C"},{"tag":"list-0001","owner":"P"}]}`},
		{"policies 与 activePolicies 各写各的",
			Routing{Policies: []Policy{p}, ActivePolicies: nil},
			`{"custom":{"name":"","icon":"","iconScale":0,"enabled":false,"rules":[]},"policies":[` + pol + `],"activePolicies":[],"fallback":{"name":"","icon":""}}`},
		{"空切片的 missingRulesets 不带",
			Routing{Custom: CustomPolicy{MissingRulesets: []string{}}, MissingRulesets: []MissingRuleset{}},
			`{"custom":{"name":"","icon":"","iconScale":0,"enabled":false,"rules":[]},"policies":[],"activePolicies":[],"fallback":{"name":"","icon":""}}`},
	}
	for _, c := range cases {
		if got := string(c.r.stringify()); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
	// iconScale 是整数(归一化时已保证):写成不带小数点和指数的十进制,负数带负号
	for scale, want := range map[float64]string{0: "0", 1: "1", -5: "-5", 9007199254740992: "9007199254740992", 1e18: "1000000000000000000"} {
		got := string(Routing{Custom: CustomPolicy{IconScale: scale}}.stringify())
		if wantHead := `{"custom":{"name":"","icon":"","iconScale":` + want + `,"enabled"`; !bytes.HasPrefix([]byte(got), []byte(wantHead)) {
			t.Errorf("iconScale %v:got %s", scale, got)
		}
	}
}

// 指纹的原始输入:没有老字段,不走迁移。
func fpRaw(mod func(m map[string]any)) map[string]any {
	m := map[string]any{
		"custom": map[string]any{
			"name": "自定义", "icon": "i", "iconScale": 1.0, "enabled": true,
			"rules": []any{map[string]any{"type": "domain", "value": "a.com", "outbound": "direct", "note": "n"}},
		},
		"policies": []any{map[string]any{
			"id": "p1", "name": "流媒体", "icon": "x", "default": "美国", "enabled": true, "domain": []any{"b.com"},
		}},
		"fallbackName": "兜底", "fallbackIcon": "fi", "fallbackDefault": "direct",
	}
	if mod != nil {
		mod(m)
	}
	return m
}

func fpPolicy(m map[string]any) map[string]any {
	return m["policies"].([]any)[0].(map[string]any)
}

func fpCustom(m map[string]any) map[string]any { return m["custom"].(map[string]any) }

func fpRule(m map[string]any) map[string]any {
	return fpCustom(m)["rules"].([]any)[0].(map[string]any)
}

func TestRoutingFingerprintValues(t *testing.T) {
	// 期望值:对手写的 JSON 文本(见各条注释)用 Python hashlib.sha256 取前 16 位十六进制
	cases := []struct {
		name string
		in   any
		want string
	}{
		// {"custom":{"name":"前置自定义分流","icon":"misc:pin","iconScale":0,"enabled":true,"rules":[]},"policies":[],"activePolicies":[],"fallback":{"name":"其他","icon":"globe:earth-meridians"}}
		{"nil 取全部默认", nil, "d8a2998b77cba36e"},
		{"空对象同上", map[string]any{}, "d8a2998b77cba36e"},
		{"数组按空对象", []any{"x"}, "d8a2998b77cba36e"},
		{"字符串按空对象", "x", "d8a2998b77cba36e"},
		{"数字按空对象", 3.0, "d8a2998b77cba36e"},
		{"布尔按空对象", true, "d8a2998b77cba36e"},
		{"未知键不参与", map[string]any{"foo": 1.0, "bar": []any{"x"}}, "d8a2998b77cba36e"},
		// 同上,只是 iconScale 为 1 / -5 / 2^53 / 1e18
		{"iconScale=1", map[string]any{"custom": map[string]any{"iconScale": 1.0}}, "92d7bda0e2d8c647"},
		{"iconScale=-5", map[string]any{"custom": map[string]any{"iconScale": -5.0}}, "cd2f6f50c65c12c8"},
		{"iconScale=2^53", map[string]any{"custom": map[string]any{"iconScale": 9007199254740992.0}}, "cf41dc4b56b19ed7"},
		{"iconScale=1e18", map[string]any{"custom": map[string]any{"iconScale": 1e18}}, "875c42c0ff82345d"},
		{"iconScale 非整数按 0", map[string]any{"custom": map[string]any{"iconScale": 1.5}}, "d8a2998b77cba36e"},
		{"iconScale 是字符串按 0", map[string]any{"custom": map[string]any{"iconScale": "abc"}}, "d8a2998b77cba36e"},
		// {"custom":{"name":"自定义","icon":"i","iconScale":1,"enabled":true,"rules":[{"type":"domain","value":"a.com","outbound":"direct"}]},
		//  "policies":[P],"activePolicies":[P],"fallback":{"name":"兜底","icon":"fi"}}
		//  P = {"id":"p1","name":"流媒体","icon":"x","enabled":true,"ruleUrls":[],"rulesets":[],"domain":["b.com"],"domainSuffix":[],"domainKeyword":[],"ipCidr":[]}
		{"完整输入", fpRaw(nil), "a0c04f5fd39b902f"},
		// 同上,P 的 enabled 为 false,activePolicies 为 []
		{"站点集停用:policies 里还在,activePolicies 里没有", fpRaw(func(m map[string]any) { fpPolicy(m)["enabled"] = false }), "a6705a0f1aa55811"},
		// 同 nil 的默认,custom.name 为 a"b\c<U+2028><U+1F600><U+0001>,custom.icon 为 i:引号、反斜杠、控制符转义,U+2028 与 emoji 原样
		{"名字里的特殊字符", map[string]any{"custom": map[string]any{"name": "a\"b\\c\u2028😀\x01", "icon": "i"}}, "f39df19225e4e1d2"},
	}
	for _, c := range cases {
		if got := routingFingerprint(c.in); got != c.want {
			t.Errorf("%s:got %s,想要 %s", c.name, got, c.want)
		}
	}
}

func TestRoutingFingerprintInvariance(t *testing.T) {
	base := routingFingerprint(fpRaw(nil))
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(base) {
		t.Fatalf("指纹应是 16 位小写十六进制:%q", base)
	}
	// 这些改动不算「分流改过」,指纹要和 base 一样
	same := map[string]func(m map[string]any){
		"站点集默认出口":   func(m map[string]any) { fpPolicy(m)["default"] = "直连" },
		"站点集默认出口清空": func(m map[string]any) { delete(fpPolicy(m), "default") },
		"站点集备注":     func(m map[string]any) { fpPolicy(m)["notes"] = map[string]any{"domain:b.com": "备注"} },
		"自定义规则备注":   func(m map[string]any) { fpRule(m)["note"] = "换一句" },
		"自定义规则去掉备注": func(m map[string]any) { delete(fpRule(m), "note") },
		"兜底默认出口":    func(m map[string]any) { m["fallbackDefault"] = "proxy" },
		"兜底默认出口没写":  func(m map[string]any) { delete(m, "fallbackDefault") },
		"顶层未知键":     func(m map[string]any) { m["extra"] = "x" },
		"站点集未知键":    func(m map[string]any) { fpPolicy(m)["extra"] = 1.0 },
		"自定义规则未知键":  func(m map[string]any) { fpRule(m)["extra"] = 1.0 },
	}
	for name, mod := range same {
		if got := routingFingerprint(fpRaw(mod)); got != base {
			t.Errorf("%s:指纹不该变,got %s,base %s", name, got, base)
		}
	}
	// -0 与 0、缺省一样(JSON.stringify(-0) 是 "0")
	negZero := func() float64 { z := 0.0; return -z }()
	if routingFingerprint(map[string]any{"custom": map[string]any{"iconScale": negZero}}) != routingFingerprint(nil) {
		t.Error("iconScale 为 -0 时指纹应与缺省一样")
	}
}

func TestRoutingFingerprintSensitivity(t *testing.T) {
	// 这些改动都算「分流改过」,指纹要和 base 不同,而且彼此也都不同
	diff := map[string]func(m map[string]any){
		"站点集 id": func(m map[string]any) { fpPolicy(m)["id"] = "p2" },
		"站点集名":   func(m map[string]any) { fpPolicy(m)["name"] = "游戏" },
		"站点集图标":  func(m map[string]any) { fpPolicy(m)["icon"] = "y" },
		"站点集停用":  func(m map[string]any) { fpPolicy(m)["enabled"] = false },
		"域名增一条":  func(m map[string]any) { fpPolicy(m)["domain"] = []any{"b.com", "c.com"} },
		"域名换一条":  func(m map[string]any) { fpPolicy(m)["domain"] = []any{"c.com"} },
		"域名后缀":   func(m map[string]any) { fpPolicy(m)["domainSuffix"] = []any{"b.com"} },
		"域名关键词":  func(m map[string]any) { fpPolicy(m)["domainKeyword"] = []any{"b.com"} },
		"IP 段":   func(m map[string]any) { fpPolicy(m)["ipCidr"] = []any{"1.1.1.0/24"} },
		"规则集":    func(m map[string]any) { fpPolicy(m)["rulesets"] = []any{"geosite-cn"} },
		"规则集链接":  func(m map[string]any) { fpPolicy(m)["ruleUrls"] = []any{"https://a.com/x.list"} },
		"再加一个站点集": func(m map[string]any) {
			m["policies"] = append(m["policies"].([]any), map[string]any{"id": "p2", "name": "游戏", "domain": []any{"g.com"}})
		},
		"自定义名":          func(m map[string]any) { fpCustom(m)["name"] = "另一个" },
		"自定义图标":         func(m map[string]any) { fpCustom(m)["icon"] = "j" },
		"自定义 iconScale": func(m map[string]any) { fpCustom(m)["iconScale"] = 2.0 },
		"自定义停用":         func(m map[string]any) { fpCustom(m)["enabled"] = false },
		"自定义规则类型":       func(m map[string]any) { fpRule(m)["type"] = "domainSuffix" },
		"自定义规则值":        func(m map[string]any) { fpRule(m)["value"] = "b.com" },
		"自定义规则出口":       func(m map[string]any) { fpRule(m)["outbound"] = "block" },
		"自定义再加一条": func(m map[string]any) {
			c := fpCustom(m)
			c["rules"] = append(c["rules"].([]any), map[string]any{"type": "domain", "value": "z.com", "outbound": "direct"})
		},
		"兜底名":  func(m map[string]any) { m["fallbackName"] = "其它" },
		"兜底图标": func(m map[string]any) { m["fallbackIcon"] = "fj" },
	}
	base := routingFingerprint(fpRaw(nil))
	seen := map[string]string{base: "base"}
	for name, mod := range diff {
		got := routingFingerprint(fpRaw(mod))
		if prev, dup := seen[got]; dup {
			t.Errorf("%s:指纹 %s 与「%s」撞了", name, got, prev)
		}
		seen[got] = name
	}
	// 顺序也算:域名换位、规则换位、站点集换位
	a := fpRaw(func(m map[string]any) { fpPolicy(m)["domain"] = []any{"b.com", "c.com"} })
	b := fpRaw(func(m map[string]any) { fpPolicy(m)["domain"] = []any{"c.com", "b.com"} })
	if routingFingerprint(a) == routingFingerprint(b) {
		t.Error("域名顺序不同,指纹应不同")
	}
	two := func(first, second string) map[string]any {
		return fpRaw(func(m map[string]any) {
			m["policies"] = []any{
				map[string]any{"id": first, "name": first, "domain": []any{first + ".com"}},
				map[string]any{"id": second, "name": second, "domain": []any{second + ".com"}},
			}
		})
	}
	if routingFingerprint(two("a", "b")) == routingFingerprint(two("b", "a")) {
		t.Error("站点集顺序不同,指纹应不同")
	}
}
