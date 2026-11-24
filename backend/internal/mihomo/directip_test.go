package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// directip_upstream.json:把上游 v0.1.462 Node 里的 ai(连同 Kr / sa / Os / zt / 网段小工具)原样从压缩源码里抽出来,
// 在 Node 22 里喂 446 组输入(26 组手写边角 + 420 组固定种子的随机:自定义分流各类型与出站、站点集的默认出站 / 端口 /
// 规则集 / 网段,候选成员、选中项链与环、内置出站改名、FakeIP、关闭、单独指定的目标)得到的返回。
// 输入里的 routing 已经是 normalizeRouting 规整后的样子(Node 里把 ke 换成恒等函数)。
type directIPVector struct {
	Routing json.RawMessage `json:"routing"`
	Opts    struct {
		Members    *[]string      `json:"members"`
		Builtin    *BuiltinNames2 `json:"builtin"`
		Selections map[string]any `json:"selections"`
		FakeIP     *bool          `json:"fakeIp"`
		Enabled    *bool          `json:"enabled"`
	} `json:"opts"`
	Want DirectIPPlan `json:"want"`
}

// BuiltinNames2 只为解码测试向量里的 {direct, block, ...}。
type BuiltinNames2 struct {
	Direct string `json:"direct"`
	Block  string `json:"block"`
}

func TestBuildDirectIPPlanMatchesUpstream(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "directip_upstream.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vs []directIPVector
	if err := json.Unmarshal(raw, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) < 400 {
		t.Fatalf("向量太少:%d", len(vs))
	}
	withCIDRs, early := 0, 0
	for i, v := range vs {
		var rm RoutingModel
		if err := json.Unmarshal(v.Routing, &rm); err != nil {
			t.Fatalf("#%d 解码 routing: %v", i, err)
		}
		opt := DirectIPOptions{Selections: v.Opts.Selections}
		if v.Opts.Members != nil {
			opt.Members = *v.Opts.Members
			if opt.Members == nil {
				opt.Members = []string{}
			}
		}
		if v.Opts.Builtin != nil {
			opt.Builtin = BuiltinNames{Direct: v.Opts.Builtin.Direct, Block: v.Opts.Builtin.Block}
		}
		opt.FakeIP = v.Opts.FakeIP != nil && *v.Opts.FakeIP
		opt.Disabled = v.Opts.Enabled != nil && !*v.Opts.Enabled

		got := BuildDirectIPPlan(rm, opt)
		if !reflect.DeepEqual(got, v.Want) {
			t.Fatalf("#%d 与上游不一致\nrouting=%s\nopts=%+v\n得到 %+v\n上游 %+v", i, v.Routing, opt, got, v.Want)
		}
		if len(got.CIDRs) > 0 {
			withCIDRs++
		}
		if got.Key == "" {
			early++
		}
	}
	if withCIDRs < 100 || early < 50 {
		t.Fatalf("向量覆盖不够:有网段 %d、提前放弃 %d", withCIDRs, early)
	}
}

func boolp(b bool) *bool { return &b }

func TestBuildDirectIPPlanCases(t *testing.T) {
	pol := func(name, def string, cidrs ...string) RoutingPolicy {
		return RoutingPolicy{Name: name, Default: def, IPCidr: cidrs, Rulesets: []string{}}
	}
	tests := []struct {
		name   string
		r      RoutingModel
		opt    DirectIPOptions
		cidrs  []string
		reason string
	}{
		{"没有分流", RoutingModel{}, DirectIPOptions{}, []string{}, "没有可旁路的直连 IP 段"},
		{"关闭", RoutingModel{ActivePolicies: []RoutingPolicy{pol("a", "direct", "10.0.0.0/8")}}, DirectIPOptions{Disabled: true}, []string{}, "直连不进内核已关闭"},
		{"直连站点集的网段计入", RoutingModel{ActivePolicies: []RoutingPolicy{pol("a", "direct", "10.0.0.0/8", "1.1.1.1")}},
			DirectIPOptions{}, []string{"1.1.1.1/32", "10.0.0.0/8"}, "直连 IP 段 2 计入入口"},
		{"走代理的站点集在前:占着的网段从后面的直连里扣掉",
			RoutingModel{ActivePolicies: []RoutingPolicy{pol("a", "proxy", "10.1.0.0/16"), pol("b", "direct", "10.0.0.0/8")}},
			DirectIPOptions{Members: []string{"direct", "自动选择"}},
			[]string{"10.0.0.0/16", "10.2.0.0/15", "10.4.0.0/14", "10.8.0.0/13", "10.16.0.0/12", "10.32.0.0/11", "10.64.0.0/10", "10.128.0.0/9"},
			"直连 IP 段 8 计入入口"},
		{"前面的规则集走代理:其后的直连网段不放行",
			RoutingModel{ActivePolicies: []RoutingPolicy{
				{Name: "a", Default: "proxy", IPCidr: []string{}, Rulesets: []string{"geoip-cn"}},
				pol("b", "direct", "20.0.0.0/8")}},
			DirectIPOptions{Members: []string{"direct", "自动选择"}},
			[]string{}, "站点集「a」排在前面且可能包含 IP 段"},
		{"开了 FakeIP:假 IP 池扣掉",
			RoutingModel{ActivePolicies: []RoutingPolicy{pol("a", "direct", "198.19.0.0/16", "1.1.1.0/24")}},
			DirectIPOptions{FakeIP: true}, []string{"1.1.1.0/24"}, "直连 IP 段 1 计入入口"},
		{"按端口分流的站点集",
			RoutingModel{ActivePolicies: []RoutingPolicy{{Name: "a", Default: "direct", Ports: []string{"443"}, IPCidr: []string{}, Rulesets: []string{}}}},
			DirectIPOptions{}, []string{}, "站点集「a」按端口分流,入口按 IP 判不了"},
		{"自定义分流端口走代理",
			RoutingModel{Custom: &CustomPolicy{Rules: []CustomRule{{Type: "port", Value: "80", Outbound: "proxy"}}}},
			DirectIPOptions{}, []string{}, "前置自定义分流「80」按端口走代理,入口不能只按 IP 放行"},
		{"自定义分流 enabled=false 不看",
			RoutingModel{Custom: &CustomPolicy{Enabled: boolp(false), Rules: []CustomRule{{Type: "port", Value: "80", Outbound: "proxy"}}}},
			DirectIPOptions{}, []string{}, "没有可旁路的直连 IP 段"},
		{"单独指定走代理的域名",
			RoutingModel{ActivePolicies: []RoutingPolicy{pol("a", "direct", "10.0.0.0/8")}, TargetOverrides: []TargetOverride{{Name: "a", Default: "proxy"}}},
			DirectIPOptions{Members: []string{"direct", "自动选择"}}, []string{}, "有单独指定走代理的域名,入口无法按真实 IP 区分"},
		{"选中项让站点集走代理",
			RoutingModel{ActivePolicies: []RoutingPolicy{pol("a", "direct", "10.0.0.0/8")}},
			DirectIPOptions{Selections: map[string]any{"a": "自动选择"}}, []string{}, "没有可旁路的直连 IP 段"},
		{"选中项的值不是字符串:落不到直连",
			RoutingModel{ActivePolicies: []RoutingPolicy{pol("a", "direct", "10.0.0.0/8")}},
			DirectIPOptions{Selections: map[string]any{"a": 5.0}}, []string{}, "没有可旁路的直连 IP 段"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildDirectIPPlan(tc.r, tc.opt)
			if !reflect.DeepEqual(got.CIDRs, tc.cidrs) || got.Reason != tc.reason {
				t.Fatalf("得到 %+v,要 cidrs=%v reason=%q", got, tc.cidrs, tc.reason)
			}
			if len(got.CIDRs) == 0 && got.Key != "" && got.Key != "[]" {
				t.Fatalf("key = %q", got.Key)
			}
		})
	}
}

func TestDirectIPPlanKeyAndEmptyShape(t *testing.T) {
	// 关闭 / 提前放弃:Key 是空串;有计划:Key 是排序后的 JSON;算完一个都不剩:Key = "[]"。
	off := BuildDirectIPPlan(RoutingModel{}, DirectIPOptions{Disabled: true})
	if off.Key != "" || off.CIDRs == nil || len(off.CIDRs) != 0 {
		t.Fatalf("关闭: %+v", off)
	}
	none := BuildDirectIPPlan(RoutingModel{}, DirectIPOptions{})
	if none.Key != "[]" || none.CIDRs == nil {
		t.Fatalf("没有可旁路: %+v", none)
	}
	b, _ := json.Marshal(none)
	if string(b) != `{"cidrs":[],"key":"[]","reason":"没有可旁路的直连 IP 段"}` {
		t.Fatalf("JSON 形状: %s", b)
	}
	two := BuildDirectIPPlan(RoutingModel{ActivePolicies: []RoutingPolicy{{Name: "a", Default: "direct", IPCidr: []string{"20.0.0.0/8", "10.0.0.0/8"}, Rulesets: []string{}}}}, DirectIPOptions{})
	if two.Key != `["10.0.0.0/8","20.0.0.0/8"]` {
		t.Fatalf("key = %s", two.Key)
	}
}

func TestPickMemberAndFollowSelection(t *testing.T) {
	b := BuiltinNames{Direct: "direct", Block: "block"}
	if v, ok := pickMember("proxy", []string{"direct", "block", "香港"}, b); !ok || v != "香港" {
		t.Fatalf("proxy 取第一个非内置: %q %v", v, ok)
	}
	if v, ok := pickMember("没有这个", []string{"a", "b"}, b); !ok || v != "a" {
		t.Fatalf("取第一个: %q %v", v, ok)
	}
	if _, ok := pickMember("direct", nil, b); ok {
		t.Fatal("成员为空应落不到任何出站")
	}
	if v, ok := followSelection(map[string]any{"a": "b", "b": "c"}, "a"); !ok || v != "c" {
		t.Fatalf("跟到底: %q %v", v, ok)
	}
	if v, ok := followSelection(map[string]any{"a": "b", "b": "a"}, "a"); !ok || v != "a" {
		t.Fatalf("环:停在走过的键 %q %v", v, ok)
	}
}

func TestCompilerDirectIPPlan(t *testing.T) {
	var gotMethod string
	var gotInput any
	call := func(_ context.Context, method string, args ...any) (json.RawMessage, error) {
		gotMethod = method
		if len(args) > 0 {
			gotInput = args[0]
		}
		return json.RawMessage(`{"custom":{"name":"前置自定义分流","enabled":true,"rules":[{"type":"ipCidr","value":"9.9.9.0/24","outbound":"direct"}]},` +
			`"policies":[],"activePolicies":[{"id":"p0","name":"国内","default":"direct","enabled":true,"ipCidr":["10.0.0.0/8"],"rulesets":["geosite-cn"]}],` +
			`"fallback":{"name":"其他","icon":"x","default":""}}`), nil
	}
	c := NewCompiler(call)
	routing := map[string]any{"policies": []any{}}
	plan, err := c.DirectIPPlan(context.Background(), routing, DirectIPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "normalizeRouting" || !reflect.DeepEqual(gotInput, routing) {
		t.Fatalf("调 core: %s %v", gotMethod, gotInput)
	}
	if !reflect.DeepEqual(plan.CIDRs, []string{"9.9.9.0/24", "10.0.0.0/8"}) || plan.Reason != "直连 IP 段 2 计入入口" {
		t.Fatalf("plan = %+v", plan)
	}

	boom := errors.New("core 挂了")
	if _, err := NewCompiler(func(context.Context, string, ...any) (json.RawMessage, error) { return nil, boom }).
		DirectIPPlan(context.Background(), nil, DirectIPOptions{}); !errors.Is(err, boom) {
		t.Fatalf("错误要原样传出来: %v", err)
	}
	if _, err := NewCompiler(func(context.Context, string, ...any) (json.RawMessage, error) { return json.RawMessage(`[1]`), nil }).
		DirectIPPlan(context.Background(), nil, DirectIPOptions{}); err == nil || !strings.Contains(err.Error(), "normalizeRouting") {
		t.Fatalf("返回值不对要报错: %v", err)
	}
}
