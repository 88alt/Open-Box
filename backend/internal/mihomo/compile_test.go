package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
)

// 假 core:记下每次调用的方法和入参,按方法回固定 JSON。
type fakeCore struct {
	calls  atomic.Int32
	last   map[string]any
	lastAt []any
	groups string
	rules  string
	err    error
}

func (f *fakeCore) call(_ context.Context, method string, args ...any) (json.RawMessage, error) {
	f.calls.Add(1)
	f.lastAt = append([]any{}, args...)
	f.last = map[string]any{}
	if len(args) > 0 {
		b, _ := json.Marshal(args[0])
		_ = json.Unmarshal(b, &f.last)
	}
	f.last["_method"] = method
	if f.err != nil {
		return nil, f.err
	}
	switch method {
	case "mihomoGroups":
		return json.RawMessage(f.groups), nil
	case "mihomoTargetRules":
		return json.RawMessage(f.rules), nil
	}
	return nil, fmt.Errorf("未知方法 %s", method)
}

const groupsJSON = `{
 "proxies":[{"name":"n1","type":"ss","port":8443}],
 "groups":[
  {"name":"节点选择","type":"select","proxies":["自动选择","n1","n2","DIRECT"]},
  {"name":"自动选择","type":"url-test","proxies":["n1","n2"]},
  {"name":"国内","type":"select","proxies":["DIRECT","节点选择"]},
  {"name":"广告","type":"select","proxies":["REJECT","DIRECT"]},
  {"name":"环A","type":"select","proxies":["环B"]},
  {"name":"环B","type":"select","proxies":["环A"]}
 ],
 "first":{"节点选择":"自动选择","自动选择":"n1","国内":"DIRECT","广告":"REJECT","环A":"环B","环B":"环A"},
 "targets":{"direct":"DIRECT","block":"REJECT"},
 "builtins":{"direct":"DIRECT","block":"REJECT"},
 "failover":[]
}`

func TestCompilerGroupsCacheAndSelections(t *testing.T) {
	fc := &fakeCore{groups: groupsJSON}
	c := NewCompiler(fc.call)
	in := GroupsInput{Profile: map[string]any{"a": 1}, Nodes: []any{"n"}, Groups: []any{}, ChainNodes: []any{}, Selections: map[string]any{"节点选择": "n2"}}
	m1, err := c.Groups(context.Background(), in, "")
	if err != nil {
		t.Fatal(err)
	}
	if fc.calls.Load() != 1 || fc.last["_method"] != "mihomoGroups" {
		t.Fatalf("应起一次 core: %d", fc.calls.Load())
	}
	// 传给 core 的 selections 恒为空对象
	if sel, ok := fc.last["selections"].(map[string]any); !ok || len(sel) != 0 {
		t.Errorf("传给 core 的 selections 应为空对象: %v", fc.last["selections"])
	}
	// 选中 n2:挪到最前,First 跟着变
	if got := m1.Groups[0]["proxies"]; !reflect.DeepEqual(got, []any{"n2", "自动选择", "n1", "DIRECT"}) {
		t.Errorf("选中后成员 %v", got)
	}
	if m1.First["节点选择"] != "n2" {
		t.Errorf("First=%v", m1.First)
	}
	// 数字保持整数写法
	b, _ := json.Marshal(m1.Proxies)
	if string(b) != `[{"name":"n1","port":8443,"type":"ss"}]` {
		t.Errorf("proxies %s", b)
	}

	// 相同输入、换选中项:命中缓存,不再起 core,且结果互不影响
	in.Selections = map[string]any{"节点选择": "DIRECT"}
	m2, err := c.Groups(context.Background(), in, "")
	if err != nil || fc.calls.Load() != 1 {
		t.Fatalf("应命中缓存: %v calls=%d", err, fc.calls.Load())
	}
	if got := m2.Groups[0]["proxies"]; !reflect.DeepEqual(got, []any{"DIRECT", "自动选择", "n1", "n2"}) {
		t.Errorf("第二次成员 %v", got)
	}
	if got := m1.Groups[0]["proxies"]; !reflect.DeepEqual(got, []any{"n2", "自动选择", "n1", "DIRECT"}) {
		t.Errorf("第一次的结果被改了: %v", got)
	}
	// 档案变了:换键,重新起
	in.Profile = map[string]any{"a": 2}
	if _, err := c.Groups(context.Background(), in, ""); err != nil || fc.calls.Load() != 2 {
		t.Errorf("输入变了应重新调 core: calls=%d", fc.calls.Load())
	}
	// 给了 baseKey:只看 baseKey,入参变了也命中
	if _, err := c.Groups(context.Background(), in, "K1"); err != nil || fc.calls.Load() != 3 {
		t.Errorf("新 baseKey 应调 core: %d", fc.calls.Load())
	}
	in.Nodes = []any{"changed"}
	if _, err := c.Groups(context.Background(), in, "K1"); err != nil || fc.calls.Load() != 3 {
		t.Errorf("同 baseKey 应命中缓存: %d", fc.calls.Load())
	}
}

func TestCompilerGroupsCacheEvictsOldest(t *testing.T) {
	fc := &fakeCore{groups: groupsJSON}
	c := NewCompiler(fc.call)
	for i := 0; i < 5; i++ { // 容量 4:第 5 个把第 1 个挤掉
		if _, err := c.Groups(context.Background(), GroupsInput{}, fmt.Sprintf("k%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	n := fc.calls.Load()
	c.Groups(context.Background(), GroupsInput{}, "k4") // 还在
	if fc.calls.Load() != n {
		t.Errorf("k4 应命中")
	}
	c.Groups(context.Background(), GroupsInput{}, "k0") // 被挤掉
	if fc.calls.Load() != n+1 {
		t.Errorf("k0 应已被淘汰")
	}
}

func TestCompilerGroupsErrors(t *testing.T) {
	fc := &fakeCore{err: errors.New("boom")}
	c := NewCompiler(fc.call)
	if _, err := c.Groups(context.Background(), GroupsInput{}, ""); err == nil || err.Error() != "boom" {
		t.Errorf("应原样返回 core 的错误: %v", err)
	}
	fc2 := &fakeCore{groups: `"not an object"`}
	if _, err := NewCompiler(fc2.call).Groups(context.Background(), GroupsInput{}, ""); err == nil {
		t.Errorf("返回值形状不对应报错")
	}
	// 出错不进缓存
	fc.err = nil
	fc.groups = groupsJSON
	if _, err := c.Groups(context.Background(), GroupsInput{}, ""); err != nil {
		t.Errorf("恢复后应成功: %v", err)
	}
}

func TestApplySelectionsEdgeCases(t *testing.T) {
	var m GroupsModel
	if err := json.Unmarshal([]byte(groupsJSON), &m); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		sel  map[string]any
		grp  int
		want []any
		/* First 里该组的值 */ first string
	}{
		{"选中的不在成员里:不动", map[string]any{"节点选择": "nope"}, 0, []any{"自动选择", "n1", "n2", "DIRECT"}, "自动选择"},
		{"已经在最前:不动", map[string]any{"节点选择": "自动选择"}, 0, []any{"自动选择", "n1", "n2", "DIRECT"}, "自动选择"},
		{"空串:不动", map[string]any{"节点选择": ""}, 0, []any{"自动选择", "n1", "n2", "DIRECT"}, "自动选择"},
		{"不是字符串:不动", map[string]any{"节点选择": 3}, 0, []any{"自动选择", "n1", "n2", "DIRECT"}, "自动选择"},
		{"direct 别名换成内置出站", map[string]any{"国内": "block"}, 2, []any{"DIRECT", "节点选择"}, "DIRECT"}, // block→REJECT 不在成员里
		{"别名命中", map[string]any{"广告": "direct"}, 3, []any{"DIRECT", "REJECT"}, "DIRECT"},
		{"非 select 组不动", map[string]any{"自动选择": "n2"}, 1, []any{"n1", "n2"}, "n1"},
		{"单成员组不动", map[string]any{"环A": "环A"}, 4, []any{"环B"}, "环B"},
	}
	for _, c := range cases {
		got := m.ApplySelections(c.sel)
		if !reflect.DeepEqual(got.Groups[c.grp]["proxies"], c.want) {
			t.Errorf("%s: 成员 %v,想要 %v", c.name, got.Groups[c.grp]["proxies"], c.want)
		}
		name, _ := got.Groups[c.grp]["name"].(string)
		if got.First[name] != c.first {
			t.Errorf("%s: First[%s]=%s,想要 %s", c.name, name, got.First[name], c.first)
		}
	}
	// 原模型不被改
	if m.Groups[0]["proxies"].([]any)[0] != "自动选择" || m.First["广告"] != "REJECT" {
		t.Errorf("原模型被改了")
	}
}

func TestClassOf(t *testing.T) {
	var m GroupsModel
	if err := json.Unmarshal([]byte(groupsJSON), &m); err != nil {
		t.Fatal(err)
	}
	cls := m.ClassOf()
	cases := map[string]string{
		"国内":     "direct", // select → DIRECT
		"广告":     "reject", // select → REJECT
		"节点选择":   "proxy",  // select → 自动选择(url-test,非 select)
		"自动选择":   "proxy",  // 自己就不是 select
		"环A":     "proxy",  // 绕环
		"n1":     "proxy",  // 不是组,是节点
		"DIRECT": "direct", // 直接问内置出站名
		"REJECT": "reject",
		"没有这个":   "proxy",
	}
	for name, want := range cases {
		if got := cls(name); got != want {
			t.Errorf("ClassOf(%q)=%s,想要 %s", name, got, want)
		}
	}
	// 选中项变了,分类跟着变
	sel := m.ApplySelections(map[string]any{"国内": "节点选择"})
	if got := sel.ClassOf()("国内"); got != "proxy" {
		t.Errorf("国内选了节点选择后应为 proxy,得到 %s", got)
	}
	// 没有 direct / block 目标时,空名字不能被当成直连
	empty := GroupsModel{First: map[string]string{"g": ""}, Groups: []map[string]any{{"name": "g", "type": "select"}}}
	if got := empty.ClassOf()("g"); got != "proxy" {
		t.Errorf("缺目标时应为 proxy: %s", got)
	}
}

const rulesJSON = `{"rules":["DOMAIN,a.com,DIRECT","MATCH,其他"],"firstTerminal":0,"endTerminal":0,"directTerminal":1,"firstPolicy":1}`

func TestCompilerTargetRulesCache(t *testing.T) {
	fc := &fakeCore{rules: rulesJSON}
	c := NewCompiler(fc.call)
	in := RulesInput{Routing: map[string]any{"r": 1}, Providers: map[string]any{"geosite-cn": "domain"}, RuleLists: map[string]any{}, Targets: map[string]any{"direct": "DIRECT"}, DirectDomains: []any{"x.com"}, DirectCIDRs: []any{}, TerminalRoutes: []any{}}
	r1, err := c.TargetRules(context.Background(), in, "")
	if err != nil {
		t.Fatal(err)
	}
	if fc.last["_method"] != "mihomoTargetRules" || fc.last["sections"] != true {
		t.Errorf("方法或 sections: %v", fc.last)
	}
	if r1.DirectTerminal != 1 || r1.FirstPolicy != 1 || len(r1.Rules) != 2 {
		t.Errorf("结果 %+v", r1)
	}
	// 调用者改返回的 Rules 不影响缓存
	r1.Rules[0] = "改过"
	r2, _ := c.TargetRules(context.Background(), in, "")
	if fc.calls.Load() != 1 || r2.Rules[0] != "DOMAIN,a.com,DIRECT" {
		t.Errorf("缓存被改或没命中: calls=%d rules=%v", fc.calls.Load(), r2.Rules)
	}
	// 无 baseKey:routing 变了就是新键
	in.Routing = map[string]any{"r": 2}
	c.TargetRules(context.Background(), in, "")
	if fc.calls.Load() != 2 {
		t.Errorf("routing 变了应重新调: %d", fc.calls.Load())
	}
	// 有 baseKey:routing / targets / directDomains 不进键(baseKey 负责),providers 等进键
	c.TargetRules(context.Background(), in, "B")
	n := fc.calls.Load()
	in.Routing = map[string]any{"r": 3}
	in.Targets = map[string]any{"direct": "X"}
	in.DirectDomains = []any{"y.com"}
	c.TargetRules(context.Background(), in, "B")
	if fc.calls.Load() != n {
		t.Errorf("baseKey 相同、只改 routing / targets / directDomains 应命中缓存")
	}
	in.DirectCIDRs = []any{"1.1.1.0/24"}
	c.TargetRules(context.Background(), in, "B")
	if fc.calls.Load() != n+1 {
		t.Errorf("directCIDRs 变了应重新调")
	}
	c.TargetRules(context.Background(), in, "B2")
	if fc.calls.Load() != n+2 {
		t.Errorf("baseKey 变了应重新调")
	}
}

func TestCompilerTargetRulesErrors(t *testing.T) {
	fc := &fakeCore{err: errors.New("mihomo 不支持规则集 x")}
	if _, err := NewCompiler(fc.call).TargetRules(context.Background(), RulesInput{}, ""); err == nil || err.Error() != "mihomo 不支持规则集 x" {
		t.Errorf("err=%v", err)
	}
	fc2 := &fakeCore{rules: `[1]`}
	if _, err := NewCompiler(fc2.call).TargetRules(context.Background(), RulesInput{}, ""); err == nil {
		t.Errorf("形状不对应报错")
	}
}
