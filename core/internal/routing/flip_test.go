package routing

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"testing"
)

// 自写测试向量:输入按 flip.go 顶部注释的设计意图自己拟,期望值人工推算;哈希前缀用 Python hashlib 独立算出,
// 占位 .srs 用官方发布的 sing-box 1.13.14 `rule-set compile` 编出来逐字节核对。dns_golden.json 里的 flip 条目仍作回归。

func TestFlipConstantsAndTagPredicates(t *testing.T) {
	strs := map[string][2]string{
		"FlipTagPrefix":    {FlipTagPrefix, "obflip-"},
		"FlipFallbackTag":  {FlipFallbackTag, "obflip-fallback"},
		"FlipBypassPrefix": {FlipBypassPrefix, "obflip-byp-"},
		"FlipDirName":      {FlipDirName, "flip"},
		"FlipNeedPrefix":   {FlipNeedPrefix, "obflip-need-"},
		"FlipNeedAll":      {FlipNeedAll, "obflip-need-all"},
		"FlipNeedFakeIP":   {FlipNeedFakeIP, "obflip-need-fakeip"},
		"FlipNeedCidr":     {FlipNeedCidr, "obflip-need-cidr"},
	}
	for name, p := range strs {
		if p[0] != p[1] {
			t.Errorf("%s = %q,想要 %q", name, p[0], p[1])
		}
	}
	if want := []string{"obflip-need-all", "obflip-need-fakeip", "obflip-need-cidr"}; !reflect.DeepEqual(FlipNeedFixed, want) {
		t.Errorf("FlipNeedFixed = %v(顺序固定)", FlipNeedFixed)
	}
	if got := FlipBypassTag("geoip-cn"); got != "obflip-byp-geoip-cn" {
		t.Errorf("FlipBypassTag = %q", got)
	}
	if got := FlipNeedTag("geoip-us"); got != "obflip-need-geoip-us" {
		t.Errorf("FlipNeedTag = %q", got)
	}
	if got := FlipBypassTag(""); got != "obflip-byp-" {
		t.Errorf("FlipBypassTag(\"\") = %q", got)
	}

	// IsFlipTag 看总前缀;IsFlipBypassTag / IsFlipNeedTag 各看自己的前缀,互不串
	cases := []struct {
		tag             string
		flip, byp, need bool
	}{
		{"obflip-", true, false, false},
		{"obflip-fallback", true, false, false},
		{"obflip-1dee6e3ec67d", true, false, false},
		{"obflip-byp-geoip-cn", true, true, false},
		{"obflip-byp-", true, true, false},
		{"obflip-need-all", true, false, true},
		{"obflip-need-", true, false, true},
		{"obflip-byp", true, false, false}, // 少了末尾的横线
		{"obflip-need", true, false, false},
		{"obflip", false, false, false},
		{"xobflip-a", false, false, false},
		{"OBFLIP-a", false, false, false},
		{"xobflip-byp-a", false, false, false}, // 前缀才算,出现在中间不算
		{"a-obflip-need-b", false, false, false},
		{"geoip-cn", false, false, false},
		{"", false, false, false},
	}
	for _, c := range cases {
		if got := IsFlipTag(c.tag); got != c.flip {
			t.Errorf("IsFlipTag(%q) = %v", c.tag, got)
		}
		if got := IsFlipBypassTag(c.tag); got != c.byp {
			t.Errorf("IsFlipBypassTag(%q) = %v", c.tag, got)
		}
		if got := IsFlipNeedTag(c.tag); got != c.need {
			t.Errorf("IsFlipNeedTag(%q) = %v", c.tag, got)
		}
	}
}

func TestFlipFlagTag(t *testing.T) {
	// 取 id 的 SHA-256 前 12 位十六进制;没有 id 退到名字。期望值由 Python hashlib 独立算出
	cases := []struct {
		name string
		p    Policy
		want string
	}{
		{"id 取哈希(SHA-256 标准向量 abc)", Policy{ID: "abc", Name: "国内"}, "obflip-ba7816bf8f01"},
		{"id 优先于名字", Policy{ID: "p-1", Name: "abc"}, "obflip-1dee6e3ec67d"},
		{"没有 id 退到名字", Policy{Name: "国内"}, "obflip-cec69ce03690"},
		{"名字是 abc", Policy{Name: "abc"}, "obflip-ba7816bf8f01"},
		{"id 和名字都空:空串的哈希", Policy{}, "obflip-e3b0c44298fc"},
		{"id 区分大小写", Policy{ID: "P-1"}, "obflip-82988760f79b"},
		{"id 不去空白", Policy{ID: "p-1 "}, "obflip-462877db90a2"},
		{"单字符", Policy{ID: "a"}, "obflip-ca978112ca1b"},
	}
	for _, c := range cases {
		if got := FlipFlagTag(c.p); got != c.want {
			t.Errorf("%s:got %s,想要 %s", c.name, got, c.want)
		}
	}
	// 改名、调序、换图标都不变;id 不同则不同
	a := FlipFlagTag(Policy{ID: "p-1", Name: "旧名字", Icon: "x", Default: "direct"})
	b := FlipFlagTag(Policy{ID: "p-1", Name: "新名字", Icon: "y", Default: "proxy"})
	if a != b {
		t.Errorf("同一个 id 改名后 tag 变了:%s / %s", a, b)
	}
	if FlipFlagTag(Policy{ID: "p-1"}) == FlipFlagTag(Policy{ID: "p-2"}) {
		t.Errorf("不同 id 撞了 tag")
	}
	// tag 要拼进文件路径,只能是前缀加 12 位小写十六进制
	re := regexp.MustCompile(`^obflip-[0-9a-f]{12}$`)
	for _, p := range []Policy{{ID: "../../etc"}, {Name: "a/b\\c"}, {ID: "含 空格\n"}, {ID: "x"}} {
		if tag := FlipFlagTag(p); !re.MatchString(tag) {
			t.Errorf("%+v → %q 不是安全的 tag", p, tag)
		}
	}
}

func TestFlipFlagsOrderedMap(t *testing.T) {
	var z FlipFlags // 零值可用
	if tag, ok := z.Get("a"); ok || tag != "" {
		t.Errorf("零值 Get = (%q,%v)", tag, ok)
	}
	z.Each(func(name, tag string) { t.Errorf("零值不该有项:%s %s", name, tag) })

	var f FlipFlags
	f.set("b", "1")
	f.set("a", "2")
	f.set("c", "3")
	f.set("b", "4") // 同名再写:只改值,不换位置(JS Map)
	var names, tags []string
	f.Each(func(n, tg string) { names = append(names, n); tags = append(tags, tg) })
	if !reflect.DeepEqual(names, []string{"b", "a", "c"}) || !reflect.DeepEqual(tags, []string{"4", "2", "3"}) {
		t.Errorf("顺序或值不对:%v %v", names, tags)
	}
	if tag, ok := f.Get("b"); !ok || tag != "4" {
		t.Errorf("Get(b) = (%q,%v)", tag, ok)
	}
	if _, ok := f.Get("zzz"); ok {
		t.Errorf("不存在的名字 Get 应为 false")
	}
	// 空串也是合法的名字和值
	f.set("", "")
	if tag, ok := f.Get(""); !ok || tag != "" {
		t.Errorf("空名字:(%q,%v)", tag, ok)
	}
}

func flipPairs(m FlipFlags) [][2]string {
	out := [][2]string{}
	m.Each(func(n, tg string) { out = append(out, [2]string{n, tg}) })
	return out
}

func TestFlipFlagMap(t *testing.T) {
	tag := func(id string) string { return FlipFlagTag(Policy{ID: id}) }
	pol := func(id, name string) Policy { return Policy{ID: id, Name: name} }

	t.Run("生效站点集按顺序,最后是兜底", func(t *testing.T) {
		conf := Routing{ActivePolicies: []Policy{pol("p1", "国内"), pol("p2", "国外")}, Fallback: Fallback{Name: "其他"}}
		want := [][2]string{{"国内", tag("p1")}, {"国外", tag("p2")}, {"其他", "obflip-fallback"}}
		if got := flipPairs(FlipFlagMap(conf)); !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("只看 ActivePolicies,不看 Policies", func(t *testing.T) {
		conf := Routing{
			Policies:       []Policy{pol("p1", "停用的"), pol("p2", "生效的")},
			ActivePolicies: []Policy{pol("p2", "生效的")},
			Fallback:       Fallback{Name: "其他"},
		}
		want := [][2]string{{"生效的", tag("p2")}, {"其他", "obflip-fallback"}}
		if got := flipPairs(FlipFlagMap(conf)); !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("兜底没有名字就不进表", func(t *testing.T) {
		conf := Routing{ActivePolicies: []Policy{pol("p1", "A")}, Fallback: Fallback{Default: "proxy"}}
		if got := flipPairs(FlipFlagMap(conf)); !reflect.DeepEqual(got, [][2]string{{"A", tag("p1")}}) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("什么都没有:空表", func(t *testing.T) {
		if got := flipPairs(FlipFlagMap(Routing{})); len(got) != 0 {
			t.Errorf("got %v", got)
		}
	})
	t.Run("兜底和站点集同名:值换成兜底的 tag,位置还在站点集那里(Map 语义)", func(t *testing.T) {
		conf := Routing{ActivePolicies: []Policy{pol("p1", "同名"), pol("p2", "B")}, Fallback: Fallback{Name: "同名"}}
		want := [][2]string{{"同名", "obflip-fallback"}, {"B", tag("p2")}}
		if got := flipPairs(FlipFlagMap(conf)); !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("站点集重名:后写的值覆盖,位置按第一次", func(t *testing.T) {
		conf := Routing{ActivePolicies: []Policy{pol("p1", "甲"), pol("p2", "乙"), pol("p3", "甲")}}
		want := [][2]string{{"甲", tag("p3")}, {"乙", tag("p2")}}
		if got := flipPairs(FlipFlagMap(conf)); !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("站点集没有 id:tag 按名字", func(t *testing.T) {
		conf := Routing{ActivePolicies: []Policy{{Name: "abc"}}}
		want := [][2]string{{"abc", "obflip-ba7816bf8f01"}}
		if got := flipPairs(FlipFlagMap(conf)); !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("经 normalizeRouting 过一遍:生效的站点集有开关,停用的没有", func(t *testing.T) {
		conf := normalizeRouting(mustJSON(t, `{"policies":[
			{"id":"a1","name":"国内","default":"direct","rulesets":["geoip-cn"]},
			{"id":"a2","name":"停掉","default":"proxy","enabled":false,"rulesets":["geoip-us"]}
		],"fallbackDefault":"proxy"}`))
		m := FlipFlagMap(conf)
		if _, ok := m.Get("国内"); !ok {
			t.Errorf("生效的站点集应有开关:%v", flipPairs(m))
		}
		if _, ok := m.Get("停掉"); ok {
			t.Errorf("停用的站点集不该有开关:%v", flipPairs(m))
		}
	})
}

func TestFlipFlagContentAndPlaceholder(t *testing.T) {
	if FlipFlagOn != `{"version":3,"rules":[{"network":["tcp","udp"]}]}` {
		t.Errorf("FlipFlagOn = %s", FlipFlagOn)
	}
	if FlipFlagOff != `{"version":3,"rules":[{"domain":["obflip-off.invalid"]}]}` {
		t.Errorf("FlipFlagOff = %s", FlipFlagOff)
	}
	if FlipFlagContent(true) != FlipFlagOn || FlipFlagContent(false) != FlipFlagOff {
		t.Errorf("FlipFlagContent 选错了")
	}
	for _, s := range []string{FlipFlagOn, FlipFlagOff, FlipPlaceholderSource()} {
		var v map[string]any
		if err := json.Unmarshal([]byte(s), &v); err != nil || v["version"] != 3.0 {
			t.Errorf("%s 不是 version 3 的规则集源码:%v", s, err)
		}
	}

	if want := []string{"192.0.2.255/32", "2001:db8:ffff::ffff/128"}; !reflect.DeepEqual(FlipBypassPlaceholder, want) {
		t.Errorf("FlipBypassPlaceholder = %v", FlipBypassPlaceholder)
	}
	if got, want := FlipPlaceholderSource(), `{"version":3,"rules":[{"ip_cidr":["192.0.2.255/32","2001:db8:ffff::ffff/128"]}]}`; got != want {
		t.Errorf("FlipPlaceholderSource = %s", got)
	}

	// 占位 .srs:官方 sing-box 1.13.14 `rule-set compile` 编 FlipPlaceholderSource() 得到的 46 字节
	srs, err := base64.StdEncoding.DecodeString(FlipPlaceholderSRSBase64)
	if err != nil {
		t.Fatal(err)
	}
	wantHex := "5352530278da62646063648000269603" + "0c4cffc184800223ef8effffa1120cff" + "3105fe3300020000ffff4ad50e78"
	if got := hex.EncodeToString(srs); got != wantHex {
		t.Errorf("占位 .srs 与官方 sing-box 编出来的不一致:\n got  %s\n want %s", got, wantHex)
	}
	if len(srs) != 46 {
		t.Errorf("占位 .srs 应是 46 字节:%d", len(srs))
	}
}

func TestAndRule(t *testing.T) {
	enc := func(m map[string]any) string {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	set := func(tag string) map[string]any { return map[string]any{"rule_set": []any{tag}} }

	cases := []struct {
		name string
		subs []map[string]any
		tail map[string]any
		want string
	}{
		{"两个子规则加动作", []map[string]any{set("a"), set("obflip-1")}, map[string]any{"action": "route", "outbound": "proxy"},
			`{"action":"route","mode":"and","outbound":"proxy","rules":[{"rule_set":["a"]},{"rule_set":["obflip-1"]}],"type":"logical"}`},
		{"空子规则丢掉(nil 与空 map),其余保序", []map[string]any{nil, set("a"), {}, set("b"), nil}, map[string]any{"server": "dns-proxy"},
			`{"mode":"and","rules":[{"rule_set":["a"]},{"rule_set":["b"]}],"server":"dns-proxy","type":"logical"}`},
		{"全是空的:rules 是 [] 不是 null", []map[string]any{nil, {}}, map[string]any{"action": "reject"},
			`{"action":"reject","mode":"and","rules":[],"type":"logical"}`},
		{"没有子规则", nil, map[string]any{"action": "reject"},
			`{"action":"reject","mode":"and","rules":[],"type":"logical"}`},
		{"没有动作", []map[string]any{set("a")}, nil,
			`{"mode":"and","rules":[{"rule_set":["a"]}],"type":"logical"}`},
		{"动作后写,同名键覆盖(JS 展开语义)", []map[string]any{set("a")}, map[string]any{"mode": "or", "type": "x", "rules": 1},
			`{"mode":"or","rules":1,"type":"x"}`},
		{"只有一个键的子规则也留", []map[string]any{{"ip_is_private": true}}, map[string]any{"rcode": "success"},
			`{"mode":"and","rcode":"success","rules":[{"ip_is_private":true}],"type":"logical"}`},
	}
	for _, c := range cases {
		if got := enc(andRule(c.subs, c.tail)); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}

	// 不改入参
	tail := map[string]any{"action": "route"}
	subs := []map[string]any{set("a"), nil}
	andRule(subs, tail)
	if len(tail) != 1 || len(subs) != 2 || subs[1] != nil {
		t.Errorf("入参被改了:%v %v", tail, subs)
	}
}

func TestFlipFlagStates(t *testing.T) {
	tag := func(id string) string { return FlipFlagTag(Policy{ID: id}) }
	conf := Routing{
		ActivePolicies: []Policy{{ID: "p1", Name: "国内"}, {ID: "p2", Name: "国外"}, {ID: "p3", Name: "拦截"}, {ID: "p4", Name: "没算出来"}},
		Fallback:       Fallback{Name: "其他"},
	}
	classes := map[string]string{"国内": "direct", "国外": "proxy", "拦截": "reject", "其他": "direct", "多余的名字": "proxy"}
	got := FlipFlagStates(conf, classes)
	want := map[string]bool{
		tag("p1"):         false, // 直连 → OFF
		tag("p2"):         true,  // 走代理 → ON
		tag("p3"):         true,  // 拒绝也算「不是直连」→ ON
		tag("p4"):         false, // 不在 classes 里按 OFF
		"obflip-fallback": false, // 兜底直连
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}

	// 兜底走代理;类别是空串也算「不是直连」;classes 里的多余名字不进结果
	classes = map[string]string{"国内": "", "其他": "proxy"}
	got = FlipFlagStates(conf, classes)
	want = map[string]bool{tag("p1"): true, tag("p2"): false, tag("p3"): false, tag("p4"): false, "obflip-fallback": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}

	// classes 为 nil:全部 OFF;结果是非 nil 的 map
	got = FlipFlagStates(conf, nil)
	if len(got) != 5 {
		t.Errorf("nil classes:%v", got)
	}
	for k, v := range got {
		if v {
			t.Errorf("nil classes 时 %s 应为 OFF", k)
		}
	}
	if got := FlipFlagStates(Routing{}, nil); got == nil || len(got) != 0 {
		t.Errorf("空配置应返回非 nil 空表:%#v", got)
	}

	// 兜底没名字:没有 obflip-fallback
	got = FlipFlagStates(Routing{ActivePolicies: []Policy{{ID: "p1", Name: "A"}}}, map[string]string{"A": "proxy"})
	if !reflect.DeepEqual(got, map[string]bool{tag("p1"): true}) {
		t.Errorf("got %v", got)
	}
}

func TestFlipBypassCandidates(t *testing.T) {
	conf := Routing{
		Policies: []Policy{{Name: "停用的", Rulesets: []string{"geoip-jp"}}},
		ActivePolicies: []Policy{
			{Name: "A", Rulesets: []string{"geosite-cn", "geoip-cn", "geoip-us", "geoip-cn"}},
			{Name: "B", Rulesets: []string{"geoip-us", "geoip-hk", "GEOIP-xx", "geoip", "geoipx-cn", "my-geoip-cn", ""}},
			{Name: "C"},
			{Name: "D", Rulesets: []string{"geoip-", "geoip-cn"}},
		},
	}
	// geoip- 开头(含恰好只有前缀的)才算;去重、按第一次出现保序;停用的不看
	want := []string{"geoip-cn", "geoip-us", "geoip-hk", "geoip-"}
	if got := FlipBypassCandidates(conf); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	// 没有候选:非 nil 的空切片(序列化成 [])
	for _, c := range []Routing{{}, {ActivePolicies: []Policy{{Name: "A", Rulesets: []string{"geosite-cn"}}}}} {
		got := FlipBypassCandidates(c)
		if got == nil || len(got) != 0 {
			t.Errorf("got %#v", got)
		}
		if b, _ := json.Marshal(got); string(b) != "[]" {
			t.Errorf("序列化成 %s", b)
		}
	}
}
