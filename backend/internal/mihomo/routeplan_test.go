package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// 对拍的向量是用上游 v0.1.462 Node(bfd33a70 的 panel/server/index.mjs)里那几个函数原样抽出来、
// 在 Node 22 里跑出来的结果(见 testdata/routeplan_upstream.json 的键名):nr / jn / ug / Ne / yt。
// 只取了不带自定义 DNS 的终端分流:Kt 依赖一串 URL 解析函数,这里用 null 顶替;
// 自定义 DNS 的取舍另有用例(TestRouteDNS)。
type routePlanVectors struct {
	DirectTag   string          `json:"directTag"`
	Routes      json.RawMessage `json:"routes"`
	RoutesNoTag json.RawMessage `json:"routesNoTag"`
	NotArray    json.RawMessage `json:"notArray"`
	Stable      []string        `json:"stable"`
	PlanKey     string          `json:"planKey"`
	PlanKeyEmpt string          `json:"planKeyEmpty"`
	PlanKeyNull string          `json:"planKeyNull"`
	UG          string          `json:"ug"`
	UGNoFB      string          `json:"ugNoFallback"`
	YT          []bool          `json:"yt"`
}

func loadRoutePlanVectors(t *testing.T) routePlanVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/routeplan_upstream.json")
	if err != nil {
		t.Fatal(err)
	}
	var v routePlanVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNormalizeClientRoutesUpstream(t *testing.T) {
	v := loadRoutePlanVectors(t)
	routes := []any{
		map[string]any{"id": "r1", "name": "手机", "enabled": true, "sources": []any{"192.168.1.5", "192.168.1.5/32", "192.168.1.0/24"}, "outbound": "节点选择"},
		map[string]any{"id": "r2", "name": "停用", "enabled": false, "sources": []any{"10.0.0.2"}, "outbound": "x"},
		map[string]any{"id": "r3", "name": "只管进内核", "enabled": true, "admit": true, "sources": []any{"10.0.0.3"}, "outbound": "x"},
		map[string]any{"id": "r4", "name": "绕开", "enabled": true, "bypass": true, "sources": []any{"10.0.0.4"}, "outbound": ""},
		map[string]any{"id": "r5", "name": "绕开2", "bypass": true, "sources": []any{"10.0.0.5/24"}, "outbound": "  节点选择  "},
		map[string]any{"id": "r6", "name": "没出口", "sources": []any{"10.0.0.6"}, "outbound": "   "},
		map[string]any{"id": "r7", "name": "没来源", "sources": []any{}, "outbound": "节点选择"},
		map[string]any{"id": "r8", "name": "来源不合法", "sources": []any{"nope", "1.2.3.4/33", ""}, "outbound": "节点选择"},
		map[string]any{"id": "r9", "name": "按 MAC", "match": "mac", "sources": []any{"10.0.0.9"}, "macs": []any{"AA-BB-CC-DD-EE-01", "aa:bb:cc:dd:ee:01", "坏"}, "outbound": "节点选择"},
		map[string]any{"id": "r10", "name": "MAC 绕开", "bypass": true, "macs": []any{"AA:BB:CC:DD:EE:02"}, "outbound": ""},
		map[string]any{"id": "r11", "name": "MAC 只进内核", "admit": true, "macs": []any{"AA:BB:CC:DD:EE:03"}, "outbound": "节点选择"},
		map[string]any{"id": "r12", "name": "MAC 不算", "macs": []any{"AA:BB:CC:DD:EE:04"}, "sources": []any{"10.0.0.12"}, "outbound": "节点选择"},
		map[string]any{"id": "r13", "name": "v6", "sources": []any{"fd00::5", "fd00::5/64", "FD00::5"}, "outbound": "direct"},
		map[string]any{"id": 14, "name": 15, "sources": []any{"10.0.0.14"}, "outbound": "节点选择"},
		nil, 7, "x",
	}
	got := NormalizeClientRoutes(routes, v.DirectTag)
	assertRoutesEqual(t, "带 directTag", v.Routes, got)

	gotNoTag := NormalizeClientRoutes(routes, "")
	assertRoutesEqual(t, "不带 directTag", v.RoutesNoTag, gotNoTag)

	assertRoutesEqual(t, "不是数组", v.NotArray, NormalizeClientRoutes("x", v.DirectTag))
}

func assertRoutesEqual(t *testing.T, label string, wantRaw json.RawMessage, got []ClientRoute) {
	t.Helper()
	var want []ClientRoute
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatalf("%s: 向量读不出来: %v", label, err)
	}
	if want == nil {
		want = []ClientRoute{}
	}
	if got == nil {
		got = []ClientRoute{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s 与上游不一致\n got: %s\nwant: %s", label, encodeNoHTML(got), encodeNoHTML(want))
	}
}

func TestStableJSONUpstream(t *testing.T) {
	v := loadRoutePlanVectors(t)
	got := []string{
		StableJSON(map[string]any{"b": 1, "a": []any{1, 2}, "c": nil}),
		StableJSON([]any{true, "x", []any{}, nil}),
		StableJSON("a<b&c>"),
	}
	if !reflect.DeepEqual(got, v.Stable) {
		t.Fatalf("StableJSON\n got: %q\nwant: %q", got, v.Stable)
	}
}

func TestBypassPlanKeyUpstream(t *testing.T) {
	v := loadRoutePlanVectors(t)
	p := BypassPlan{
		Enabled: true,
		Sets:    []string{"b", "a"},
		Pending: []PendingItem{
			{Policy: "p2", Sets: []string{"z", "a"}, Against: []EarlierRule{{Name: "n1", Geoip: []string{"g2", "g1"}, Cidrs: []string{"2/24", "1/24"}, Lists: []string{"l1"}}}},
			{Policy: "p1", Sets: []string{"m"}, Against: []EarlierRule{}},
		},
		FakeIP: true,
		Reason: "r",
	}
	if got := BypassPlanKey(&p); got != v.PlanKey {
		t.Fatalf("旁路计划指纹\n got: %s\nwant: %s", got, v.PlanKey)
	}
	empty := BypassPlan{Sets: []string{}, Pending: []PendingItem{}}
	if got := BypassPlanKey(&empty); got != v.PlanKeyEmpt {
		t.Fatalf("空的旁路计划指纹\n got: %s\nwant: %s", got, v.PlanKeyEmpt)
	}
	if got := BypassPlanKey(nil); got != v.PlanKeyNull {
		t.Fatalf("nil 应回空串: %q", got)
	}
}

func TestRoutePlanKeyUpstream(t *testing.T) {
	v := loadRoutePlanVectors(t)
	with := RoutePlan{
		Plan:     BypassPlan{Sets: []string{"a"}, Pending: []PendingItem{}, FakeIP: true},
		DirectIP: DirectIPPlan{CIDRs: []string{"1.1.1.0/24", "2.2.2.0/24"}},
		Fallback: &SourceScopedFallback{NeedSets: []string{"s2", "s1"}, NeedCidrs: []string{"c"}},
		Scoped:   true,
	}
	if got := RoutePlanKey(with); got != v.UG {
		t.Fatalf("指纹\n got: %s\nwant: %s", got, v.UG)
	}
	without := RoutePlan{
		Plan:     BypassPlan{Sets: []string{}, Pending: []PendingItem{}},
		DirectIP: DirectIPPlan{CIDRs: []string{}},
	}
	if got := RoutePlanKey(without); got != v.UGNoFB {
		t.Fatalf("没有兜底时的指纹\n got: %s\nwant: %s", got, v.UGNoFB)
	}
}

func TestFakeIPForProxyUpstream(t *testing.T) {
	v := loadRoutePlanVectors(t)
	profiles := []map[string]any{
		{"dns": map[string]any{"fakeIpForProxy": true}},
		{"dns": map[string]any{"fakeIpForProxy": true, "split": false}},
		{},
		{"dns": map[string]any{"split": true}},
	}
	for i, p := range profiles {
		if got := FakeIPForProxy(p); got != v.YT[i] {
			t.Errorf("第 %d 组: got %v want %v", i+1, got, v.YT[i])
		}
	}
}

// 自定义 DNS 的取舍按上游 Kt / Br 的要点:没有 url、url 太长、udp / tcp 带 bootstrap、
// DoH / DoT 用域名却不给 bootstrap、bootstrap 不是 IP,都当没有。
func TestRouteDNS(t *testing.T) {
	cases := []struct {
		name  string
		in    any
		want  *RouteDNS
		wantB bool
	}{
		{"DoH 域名 + bootstrap", map[string]any{"url": "https://dns.google/dns-query", "bootstrap": "8.8.8.8"}, &RouteDNS{URL: "https://dns.google/dns-query", Bootstrap: "8.8.8.8"}, true},
		{"DoH 域名没 bootstrap", map[string]any{"url": "https://dns.google/dns-query"}, nil, false},
		{"DoH 用 IP", map[string]any{"url": "https://1.1.1.1/dns-query"}, &RouteDNS{URL: "https://1.1.1.1/dns-query"}, true},
		{"DoT 用 IP", map[string]any{"url": "tls://9.9.9.9:853"}, &RouteDNS{URL: "tls://9.9.9.9:853"}, true},
		{"DoT 用域名 + 非 IP bootstrap", map[string]any{"url": "tls://dns.quad9.net", "bootstrap": "dns.google"}, nil, false},
		{"udp 配 IP", map[string]any{"url": "udp://223.5.5.5:53"}, &RouteDNS{URL: "udp://223.5.5.5:53"}, true},
		{"udp 带 bootstrap", map[string]any{"url": "udp://223.5.5.5:53", "bootstrap": "8.8.8.8"}, nil, false},
		{"没有 url", map[string]any{"bootstrap": "8.8.8.8"}, nil, false},
		{"url 太长", map[string]any{"url": "https://" + string(make([]byte, 2100))}, nil, false},
		{"不是对象", "https://dns.google/dns-query", nil, false},
		{"不认识的协议", map[string]any{"url": "quic://1.1.1.1"}, nil, false},
	}
	for _, c := range cases {
		got, ok := routeDNS(c.in)
		if ok != c.wantB || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v %v, want %+v %v", c.name, got, ok, c.want, c.wantB)
		}
	}
}

// 假 core:按方法回固定 JSON,并记下参数个数与内容。
type planFakeCore struct {
	methods []string
	args    [][]any
	bypass  string
	entry   []string // 依次作为 entryPlan 的返回
	routing string
	err     error  // 所有方法都失败
	fail    string // 只让这个方法失败
}

func (f *planFakeCore) call(_ context.Context, method string, in ...any) (json.RawMessage, error) {
	f.methods = append(f.methods, method)
	f.args = append(f.args, in)
	if f.err != nil {
		return nil, f.err
	}
	if f.fail == method {
		return nil, fmt.Errorf("%s 失败", method)
	}
	switch method {
	case "bypassPlan":
		if len(in) != 2 {
			return nil, fmt.Errorf("bypassPlan 要两个参数,给了 %d", len(in))
		}
		return json.RawMessage(f.bypass), nil
	case "entryPlan":
		if len(in) != 2 {
			return nil, fmt.Errorf("entryPlan 要两个参数,给了 %d", len(in))
		}
		if len(f.entry) == 0 {
			return json.RawMessage(`{"mode":"blacklist","reason":"","fakeIp":false,"needSets":[],"needCidrs":[],"directAnswer":false}`), nil
		}
		out := f.entry[0]
		f.entry = f.entry[1:]
		return json.RawMessage(out), nil
	case "normalizeRouting":
		return json.RawMessage(f.routing), nil
	}
	return nil, fmt.Errorf("未知方法 %s", method)
}

func (f *planFakeCore) lastArgsFor(method string) []any {
	for i := len(f.methods) - 1; i >= 0; i-- {
		if f.methods[i] == method {
			return f.args[i]
		}
	}
	return nil
}

const routingJSON = `{"custom":null,"policies":[],"activePolicies":[{"id":"p0","name":"国内","default":"direct","enabled":true,"ipCidr":["10.0.0.0/8"],"rulesets":["geosite-cn"]}],"fallback":{"name":"其他","icon":"x","default":""}}`

func planInput(profile map[string]any) RoutePlanInput {
	return RoutePlanInput{
		Profile: profile,
		Builtin: BuiltinNames{Direct: "DIRECT", Block: "REJECT"},
		Members: []string{"DIRECT", "REJECT", "节点选择", "自动选择"},
		Model: &GroupsModel{
			Groups:  []map[string]any{{"name": "节点选择", "type": "select"}, {"name": "自动选择", "type": "url-test"}},
			First:   map[string]string{"节点选择": "自动选择", "自动选择": "n1"},
			Targets: map[string]string{"direct": "DIRECT", "block": "REJECT"},
		},
	}
}

func TestCompilerRoutePlanScoped(t *testing.T) {
	fc := &planFakeCore{
		bypass:  `{"enabled":true,"sets":["geoip-cn"],"pending":[],"fakeIp":true,"reason":""}`,
		entry:   []string{`{"mode":"whitelist","reason":"入口默认放行","fakeIp":true,"needSets":["geoip-xx"],"needCidrs":["2.2.2.0/24"],"directAnswer":true}`},
		routing: routingJSON,
	}
	c := NewCompiler(fc.call)
	in := planInput(map[string]any{
		"routing": map[string]any{"policies": []any{}},
		"dns":     map[string]any{"mode": "dnsmasq", "fakeIpForProxy": true},
		"clientRoutes": []any{
			map[string]any{"id": "a", "name": "手机", "sources": []any{"10.0.0.2"}, "outbound": "节点选择"},
		},
	})
	plan, err := c.RoutePlan(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// 有终端被指定走非直连出口:旁路计划与入口计划都不该看到终端分流。
	if !plan.Scoped {
		t.Fatal("应判成有终端走代理")
	}
	for _, m := range []string{"bypassPlan", "entryPlan"} {
		args := fc.lastArgsFor(m)
		if len(args) != 2 {
			t.Fatalf("%s 参数个数 %d", m, len(args))
		}
		opts, ok := args[1].(PlanOptions)
		if !ok {
			t.Fatalf("%s 的第二个参数应是 PlanOptions: %T", m, args[1])
		}
		if len(opts.ClientRoutes) != 0 {
			t.Errorf("%s 的终端分流应被清空: %+v", m, opts.ClientRoutes)
		}
		if opts.Builtin.Direct != "DIRECT" || opts.DNSMode != "dnsmasq" || !opts.FakeIP {
			t.Errorf("%s 的选项不对: %+v", m, opts)
		}
		if got, _ := opts.Selections["节点选择"].(string); got != "自动选择" {
			t.Errorf("%s 的选中项应来自组的第一选择: %v", m, opts.Selections)
		}
	}
	if plan.Fallback == nil || !reflect.DeepEqual(plan.Fallback.NeedSets, []string{"geoip-xx"}) {
		t.Fatalf("白名单模式下应有来源收窄的兜底: %+v", plan.Fallback)
	}
	if !reflect.DeepEqual(plan.DirectIP.CIDRs, []string{"10.0.0.0/8"}) {
		t.Errorf("直连 IP 计划: %+v", plan.DirectIP)
	}
	if plan.Key == "" || plan.Key != RoutePlanKey(plan) {
		t.Errorf("指纹: %q", plan.Key)
	}
}

func TestCompilerRoutePlanNotScoped(t *testing.T) {
	fc := &planFakeCore{
		bypass:  `{"enabled":false,"sets":[],"pending":[],"fakeIp":false,"reason":"直连不进内核已关闭"}`,
		routing: routingJSON,
	}
	c := NewCompiler(fc.call)
	in := planInput(map[string]any{
		"routing": map[string]any{},
		"clientRoutes": []any{
			map[string]any{"id": "a", "name": "绕开", "bypass": true, "sources": []any{"10.0.0.2"}, "outbound": ""},
			map[string]any{"id": "b", "name": "白名单", "admit": true, "sources": []any{"10.0.0.3"}, "outbound": "节点选择"},
		},
	})
	plan, err := c.RoutePlan(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Scoped {
		t.Fatal("只有 bypass 与 admit 的行不算走代理")
	}
	// 没开 FakeIP:直连 IP 计划是空的,也不该调 normalizeRouting?仍要调一次(它同时给「用户规则集」判断用)。
	if len(plan.DirectIP.CIDRs) != 0 {
		t.Errorf("没开 FakeIP 不该有直连 IP 段: %+v", plan.DirectIP)
	}
	opts := fc.lastArgsFor("bypassPlan")[1].(PlanOptions)
	if len(opts.ClientRoutes) != 1 || opts.ClientRoutes[0].Outbound != "DIRECT" {
		t.Errorf("旁路计划应带上看得到终端分流(只有一条放行的): %+v", opts.ClientRoutes)
	}
	if plan.Fallback != nil {
		t.Errorf("没有终端走代理时不该有兜底: %+v", plan.Fallback)
	}
}

// 白名单模式下,只要有站点集引用了用户规则集(list-*)且不走直连,就不给来源收窄的兜底(与上游 si 一致)。
func TestCompilerRoutePlanFallbackNeedsNoProxyUserRuleset(t *testing.T) {
	whitelist := []string{`{"mode":"whitelist","reason":"x","fakeIp":true,"needSets":["geoip-xx"],"needCidrs":[],"directAnswer":true}`}
	for _, c := range []struct {
		name    string
		routing string
		want    bool
	}{
		{"走直连的用户规则集", `{"activePolicies":[{"name":"广告","default":"direct","rulesets":["list-abc"]}],"fallback":{"name":"其他","default":""}}`, true},
		{"走代理的用户规则集", `{"activePolicies":[{"name":"广告","default":"proxy","rulesets":["list-abc"]}],"fallback":{"name":"其他","default":""}}`, false},
		{"只有随包规则集", `{"activePolicies":[{"name":"国内","default":"proxy","rulesets":["geosite-cn"]}],"fallback":{"name":"其他","default":""}}`, true},
	} {
		fc := &planFakeCore{bypass: `{"enabled":true,"sets":[],"pending":[],"fakeIp":true,"reason":""}`, entry: whitelist, routing: c.routing}
		plan, err := NewCompiler(fc.call).RoutePlan(context.Background(), planInput(map[string]any{
			"routing":      map[string]any{},
			"dns":          map[string]any{"fakeIpForProxy": true},
			"clientRoutes": []any{map[string]any{"sources": []any{"10.0.0.2"}, "outbound": "节点选择"}},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if (plan.Fallback != nil) != c.want {
			t.Errorf("%s: 兜底 %+v,希望有=%v", c.name, plan.Fallback, c.want)
		}
	}
}

// 出口是内置直连、但没标 bypass 的行:仍算「有终端要进内核」(旁路计划看不到终端分流),
// 但 si 那边不算「指定走非直连出口」,所以没有来源收窄的兜底。
func TestCompilerRoutePlanScopedButDirect(t *testing.T) {
	fc := &planFakeCore{
		bypass:  `{"enabled":true,"sets":[],"pending":[],"fakeIp":true,"reason":""}`,
		entry:   []string{`{"mode":"whitelist","reason":"x","fakeIp":true,"needSets":["geoip-xx"],"needCidrs":[],"directAnswer":true}`},
		routing: routingJSON,
	}
	plan, err := NewCompiler(fc.call).RoutePlan(context.Background(), planInput(map[string]any{
		"routing":      map[string]any{},
		"dns":          map[string]any{"fakeIpForProxy": true},
		"clientRoutes": []any{map[string]any{"sources": []any{"10.0.0.2"}, "outbound": "DIRECT"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Scoped {
		t.Fatal("没标 bypass 的终端要进内核,应算 scoped")
	}
	if opts := fc.lastArgsFor("bypassPlan")[1].(PlanOptions); len(opts.ClientRoutes) != 0 {
		t.Errorf("旁路计划不该看到终端分流: %+v", opts.ClientRoutes)
	}
	if plan.Fallback != nil {
		t.Errorf("没指定非直连出口时不该有兜底: %+v", plan.Fallback)
	}
	if got := fc.lastArgsFor("entryPlan"); got != nil {
		t.Errorf("不该再调一次 entryPlan: %v", got)
	}
}

// 传给 core 的选项字段名与键序和上游字面量一致(core 按这些名字解结构)。
func TestPlanOptionsJSON(t *testing.T) {
	got := encodeNoHTML(PlanOptions{
		Members: []string{"direct"}, Builtin: BuiltinNames{Direct: "DIRECT", Block: "REJECT"},
		FakeIP: true, DNSMode: "hijack", ClientRoutes: []ClientRoute{},
	})
	want := `{"members":["direct"],"builtin":{"direct":"DIRECT","block":"REJECT"},"selections":null,"clientRoutes":[],"fakeIp":true,"dnsMode":"hijack"}`
	if got != want {
		t.Fatalf("选项 JSON\n got: %s\nwant: %s", got, want)
	}
	// members 为 nil 时是 null(core 按 ["direct"] 处理),空切片就是空表,两者不能混。
	if got := encodeNoHTML(PlanOptions{Members: nil}); !strings.Contains(got, `"members":null`) {
		t.Errorf("nil 成员表: %s", got)
	}
}

func TestCompilerRoutePlanErrors(t *testing.T) {
	boom := errors.New("core 挂了")
	for _, c := range []struct{ name, routing, bypass, entry, want string }{
		{"旁路计划失败", routingJSON, "", "", "core 挂了"},
		{"入口计划失败", routingJSON, `{"enabled":true,"sets":[],"pending":[],"fakeIp":false,"reason":""}`, "", "core 挂了"},
	} {
		fc := &planFakeCore{bypass: c.bypass, routing: c.routing}
		if c.name == "旁路计划失败" {
			fc.err = boom
		} else {
			fc.fail = "entryPlan"
		}
		_, err := NewCompiler(fc.call).RoutePlan(context.Background(), planInput(map[string]any{
			"routing":      map[string]any{},
			"dns":          map[string]any{"fakeIpForProxy": true},
			"clientRoutes": []any{map[string]any{"sources": []any{"10.0.0.2"}, "outbound": "节点选择"}},
		}))
		if err == nil {
			t.Fatalf("%s: 应报错", c.name)
		}
	}
	// normalizeRouting 的返回值形状不对要报错
	fc := &planFakeCore{bypass: `{"enabled":true,"sets":[],"pending":[],"fakeIp":true,"reason":""}`, routing: `[1]`}
	if _, err := NewCompiler(fc.call).RoutePlan(context.Background(), planInput(map[string]any{
		"routing": map[string]any{}, "dns": map[string]any{"fakeIpForProxy": true},
	})); err == nil {
		t.Fatal("normalizeRouting 返回值不对应报错")
	}
}

func TestRouteSelections(t *testing.T) {
	model := &GroupsModel{
		Groups: []map[string]any{
			{"name": "节点选择", "type": "select"},
			{"name": "自动选择", "type": "url-test"},
			{"name": "国内", "type": "select"},
			{"name": "广告", "type": "select"},
		},
		First:   map[string]string{"节点选择": "自动选择", "自动选择": "n1", "国内": "DIRECT", "广告": "REJECT"},
		Targets: map[string]string{"direct": "DIRECT", "block": "REJECT"},
	}
	b := BuiltinNames{Direct: "内置直连", Block: "内置拒绝"}
	sel := RouteSelections(model, nil, b, map[string]any{"用户另选": "x"})
	if sel["节点选择"] != "自动选择" || sel["自动选择"] != "自动选择" {
		t.Errorf("select 组取第一选择,不是 select 的组取自己的名字: %v", sel)
	}
	if sel["国内"] != "内置直连" || sel["广告"] != "内置拒绝" {
		t.Errorf("targets 里的 direct / block 要换成内置出站的名字: %v", sel)
	}
	if sel["用户另选"] != "x" {
		t.Errorf("用户自己的选中项要保留: %v", sel)
	}
	// 给了 first 就用它
	sel = RouteSelections(model, map[string]string{"节点选择": "D"}, b, nil)
	if sel["节点选择"] != "D" {
		t.Errorf("first 应优先: %v", sel)
	}
	// 没有组模型:原样返回用户选中项
	if got := RouteSelections(nil, nil, b, map[string]any{"a": "b"}); got["a"] != "b" || len(got) != 1 {
		t.Errorf("没有组模型: %v", got)
	}
}
