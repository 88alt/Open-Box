package targetrouting

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

type fakeSrc struct {
	routing policyentries.Routing
	entries map[string][]policyentries.Entry // 按站点集 id
	missing map[string][]string
	err     error
	expand  int
}

func (f *fakeSrc) Routing(context.Context) (policyentries.Routing, error) { return f.routing, f.err }
func (f *fakeSrc) ExpandPolicy(_ context.Context, p policyentries.Policy) ([]policyentries.Entry, []string, error) {
	f.expand++
	return f.entries[p.ID], f.missing[p.ID], nil
}

func custom(typ, v string) policyentries.Entry {
	return policyentries.Entry{Type: typ, Family: "x", Content: v, Source: policyentries.SourceCustom}
}
func fromSet(typ, v, src string) policyentries.Entry {
	return policyentries.Entry{Type: typ, Family: "x", Content: v, Source: src}
}

func newFake() *fakeSrc {
	return &fakeSrc{
		routing: policyentries.Routing{Policies: []policyentries.Policy{
			{ID: "a", Name: "流媒体", Default: "美国", Enabled: true},
			{ID: "off", Name: "停用的", Default: "direct", Enabled: false},
			{ID: "b", Name: "国内", Default: "direct", Enabled: true},
		}},
		entries: map[string][]policyentries.Entry{
			"a": {
				custom("domain", "www.netflix.com"),
				custom("domain_suffix", "netflix.com"),
				custom("domain_keyword", "flix"),
				custom("ip_cidr", "8.8.8.0/24"),
				fromSet("domain_suffix", "nflxvideo.net", "geosite-netflix"),
				fromSet("domain_regex", `^cdn[0-9]+\.netflix\.com$`, "geosite-netflix"),
			},
			"off": {custom("domain_suffix", "netflix.com")},
			"b": {
				fromSet("domain_suffix", "netflix.com", "geosite-cn"),
				custom("ip_cidr", "2001:db8::/32"),
			},
		},
	}
}

func TestParseTarget(t *testing.T) {
	cases := []struct {
		text, typ string
		want      Addition
		bad       bool
	}{
		{"WWW.Example.COM.", "domain", Addition{"domain", "www.example.com"}, false},
		{"  a.b  ", "domain_suffix", Addition{"domain_suffix", "a.b"}, false},
		{"*.Example.com", "domain_suffix", Addition{"domain_suffix", "example.com"}, false},
		{".example.com", "domain_suffix", Addition{"domain_suffix", "example.com"}, false},
		{"_dmarc.example.com", "domain", Addition{"domain", "_dmarc.example.com"}, false},
		{"1.2.3.4", "domain", Addition{}, true}, // IP 不是域名
		{"bad host", "domain", Addition{}, true},
		{"-a.com", "domain", Addition{}, true},
		{"a..com", "domain", Addition{}, true},
		{"中文.com", "domain", Addition{}, true},
		{"", "domain", Addition{}, true},
		{"1.2.3.4", "ip", Addition{"ip_cidr", "1.2.3.4/32"}, false},
		{"[2001:db8::1]", "ip", Addition{"ip_cidr", "2001:db8::1/128"}, false},
		{"::ffff:1.2.3.4", "ip", Addition{"ip_cidr", "1.2.3.4/32"}, false},
		{"fe80::1%eth0", "ip", Addition{}, true},
		{"example.com", "ip", Addition{}, true},
		{"192.168.1.77/24", "ip_cidr", Addition{"ip_cidr", "192.168.1.0/24"}, false},
		{"10.0.0.1", "ip_cidr", Addition{"ip_cidr", "10.0.0.1/32"}, false},
		{"[2001:db8::5]/64", "ip_cidr", Addition{"ip_cidr", "2001:db8::/64"}, false},
		{"1.2.3.4/33", "ip_cidr", Addition{}, true},
		{"x", "bogus", Addition{}, true},
		// 自动判断
		{"example.com", "", Addition{"domain", "example.com"}, false},
		{"1.1.1.1", "", Addition{"ip_cidr", "1.1.1.1/32"}, false},
		{"1.1.1.0/24", "", Addition{"ip_cidr", "1.1.1.0/24"}, false},
	}
	for _, c := range cases {
		got, err := parseTarget(c.text, c.typ)
		if c.bad {
			var in *InputError
			if !errors.As(err, &in) {
				t.Errorf("parseTarget(%q,%q) 应报 InputError,得到 %v / %+v", c.text, c.typ, err, got.add)
			}
			continue
		}
		if err != nil || got.add != c.want {
			t.Errorf("parseTarget(%q,%q) = %+v, %v;想要 %+v", c.text, c.typ, got.add, err, c.want)
		}
	}
}

func TestFindDomainOrderAndSources(t *testing.T) {
	src := newFake()
	s := New(src, nil)
	res, err := s.Find(context.Background(), "www.netflix.com", "domain", false)
	if err != nil {
		t.Fatal(err)
	}
	wantPol := []Policy{{"a", "流媒体"}, {"b", "国内"}} // 停用的不出现
	if !reflect.DeepEqual(res.Policies, wantPol) {
		t.Errorf("policies = %+v", res.Policies)
	}
	want := []Match{
		{SourcePolicy, "流媒体", "a", "domain", "www.netflix.com", "美国", true},
		{SourcePolicy, "流媒体", "a", "domain_suffix", "netflix.com", "美国", true},
		{SourcePolicy, "流媒体", "a", "domain_keyword", "flix", "美国", true},
		{SourceRuleset, "国内", "b", "domain_suffix", "netflix.com", "direct", false},
	}
	if !reflect.DeepEqual(res.Matches, want) {
		t.Errorf("命中不符:\n得到 %+v\n想要 %+v", res.Matches, want)
	}
	if res.Addition == nil || *res.Addition != (Addition{"domain", "www.netflix.com"}) {
		t.Errorf("addition = %+v", res.Addition)
	}
	if res.Incomplete || res.First != nil {
		t.Errorf("incomplete/first 不该有: %+v", res)
	}
}

func TestFindSuffixRegexAndBoundaries(t *testing.T) {
	s := New(newFake(), nil)
	// 后缀匹配只认整段:notnetflix.com 不命中 netflix.com
	res, _ := s.Find(context.Background(), "notnetflix.com", "domain", false)
	for _, m := range res.Matches {
		if m.Type == "domain_suffix" {
			t.Errorf("notnetflix.com 不该命中后缀 netflix.com: %+v", m)
		}
	}
	if len(res.Matches) != 1 || res.Matches[0].Type != "domain_keyword" { // 只有关键字 flix 命中
		t.Errorf("命中 = %+v", res.Matches)
	}
	// 正则
	res, _ = s.Find(context.Background(), "cdn12.netflix.com", "domain", false)
	var gotRegex bool
	for _, m := range res.Matches {
		if m.Type == "domain_regex" && m.Source == SourceRuleset && !m.Removable {
			gotRegex = true
		}
	}
	if !gotRegex {
		t.Errorf("正则没命中: %+v", res.Matches)
	}
	// 输入的是后缀时,按主机名匹配
	res, _ = s.Find(context.Background(), "nflxvideo.net", "domain_suffix", false)
	if len(res.Matches) != 1 || res.Matches[0].Value != "nflxvideo.net" {
		t.Errorf("后缀目标命中 = %+v", res.Matches)
	}
}

func TestFindIP(t *testing.T) {
	s := New(newFake(), nil)
	ctx := context.Background()
	res, err := s.Find(ctx, "8.8.8.8", "ip", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 || res.Matches[0].Value != "8.8.8.0/24" || !res.Matches[0].Removable {
		t.Errorf("IP 命中 = %+v", res.Matches)
	}
	// IP 段:条目要把整段包住才算
	res, _ = s.Find(ctx, "8.8.8.0/25", "ip_cidr", false)
	if len(res.Matches) != 1 {
		t.Errorf("/25 应被 /24 包住: %+v", res.Matches)
	}
	res, _ = s.Find(ctx, "8.8.0.0/16", "ip_cidr", false)
	if len(res.Matches) != 0 {
		t.Errorf("/16 比 /24 大,不该命中: %+v", res.Matches)
	}
	// IPv6
	res, _ = s.Find(ctx, "2001:db8::1", "ip", false)
	if len(res.Matches) != 1 || res.Matches[0].PolicyID != "b" {
		t.Errorf("IPv6 命中 = %+v", res.Matches)
	}
	// 域名条目不会被 IP 命中
	res, _ = s.Find(ctx, "1.2.3.4", "ip", false)
	if len(res.Matches) != 0 || res.Addition == nil || res.Addition.Value != "1.2.3.4/32" {
		t.Errorf("无命中: %+v %+v", res.Matches, res.Addition)
	}
}

func TestFindIncompleteAndFirst(t *testing.T) {
	src := newFake()
	src.missing = map[string][]string{"b": {"geosite-gone"}}
	s := New(src, nil)
	res, _ := s.Find(context.Background(), "nothing.example", "domain", false)
	if !res.Incomplete || len(res.Matches) != 0 {
		t.Errorf("规则集缺失应标 incomplete: %+v", res)
	}
	// first=1:回第一个命中,忽略 type,不给 addition,键 first 始终在
	src.expand = 0
	res, err := s.Find(context.Background(), "www.netflix.com", "ip", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.First == nil || res.First.PolicyID != "a" || res.First.Type != "domain" || res.Addition != nil {
		t.Errorf("first = %+v addition=%+v", res.First, res.Addition)
	}
	if src.expand != 1 {
		t.Errorf("第一个站点集就命中了,不该再展开后面的:展开了 %d 次", src.expand)
	}
	if len(res.Matches) != 1 {
		t.Errorf("first 模式 matches = %+v", res.Matches)
	}
	res, _ = s.Find(context.Background(), "nothing.example", "", true)
	if res.First != nil {
		t.Errorf("没命中 first 应为 nil")
	}
}

func TestFindErrors(t *testing.T) {
	src := newFake()
	src.err = ErrCoreUnavailable
	if _, err := New(src, nil).Find(context.Background(), "a.com", "domain", false); !errors.Is(err, ErrCoreUnavailable) {
		t.Errorf("err = %v", err)
	}
	var in *InputError
	if _, err := New(newFake(), nil).Find(context.Background(), "not valid", "domain", false); !errors.As(err, &in) {
		t.Errorf("err = %v", err)
	}
}

// memStore 是内存版站点集存储:change 返回错误就什么都不留(和真实存储一样)。
type memStore struct {
	list   []any
	writes int
}

func (m *memStore) UpdatePolicies(_ context.Context, change func([]any) ([]any, error)) ([]any, error) {
	next, err := change(profile.Clone(m.list).([]any))
	if err != nil {
		return nil, err
	}
	m.list = next
	m.writes++
	return next, nil
}

func policyList() []any {
	return []any{
		map[string]any{"id": "a", "name": "流媒体", "domainSuffix": []any{"netflix.com"}, "notes": map[string]any{"domainSuffix:netflix.com": "备注", "ruleset:geosite-cn": "别动"}},
		map[string]any{"name": "没写 id", "enabled": true}, // id 按核心的规矩 = policy-1
		map[string]any{"id": "off", "name": "停用的", "enabled": false},
	}
}

func TestAdd(t *testing.T) {
	ms := &memStore{list: policyList()}
	s := New(newFake(), ms)
	ctx := context.Background()

	c, err := s.Add(ctx, "Www.Example.com.", "domain", []string{"a", "policy-1", "a"})
	if err != nil || !c.Changed {
		t.Fatalf("Add = %+v, %v", c, err)
	}
	for _, id := range []string{"a", "policy-1"} {
		m, _ := findPolicy(ms.list, id)
		if got := strList(m["domain"]); !reflect.DeepEqual(got, []string{"www.example.com"}) {
			t.Errorf("%s 的 domain = %v", id, got)
		}
	}
	// 重复添加(规整后相同)→ 没变化、不写盘
	w := ms.writes
	c, err = s.Add(ctx, "WWW.EXAMPLE.COM", "domain", []string{"a"})
	if err != nil || c.Changed || ms.writes != w {
		t.Errorf("重复添加应无变化: %+v %v writes %d→%d", c, err, w, ms.writes)
	}
	// 后缀 / IP / IP 段落到各自的键
	if _, err := s.Add(ctx, "example.org", "domain_suffix", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "9.9.9.9", "ip", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "10.1.2.3/8", "ip_cidr", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	m, _ := findPolicy(ms.list, "a")
	if got := strList(m["domainSuffix"]); !reflect.DeepEqual(got, []string{"netflix.com", "example.org"}) {
		t.Errorf("domainSuffix = %v", got)
	}
	if got := strList(m["ipCidr"]); !reflect.DeepEqual(got, []string{"9.9.9.9/32", "10.0.0.0/8"}) {
		t.Errorf("ipCidr = %v", got)
	}
	// 已有的 IP 段换种写法再加,也算重复
	m["ipCidr"] = []any{"10.5.5.5/8"}
	if c, _ := s.Add(ctx, "10.0.0.0/8", "ip_cidr", []string{"a"}); c.Changed {
		t.Errorf("10.5.5.5/8 与 10.0.0.0/8 是同一段")
	}
}

func TestAddErrorsWriteNothing(t *testing.T) {
	ms := &memStore{list: policyList()}
	s := New(newFake(), ms)
	ctx := context.Background()
	before := profile.Clone(ms.list)
	cases := []struct {
		name string
		ids  []string
		typ  string
		text string
	}{
		{"没选分流组", nil, "domain", "a.com"},
		{"空白 id", []string{"  "}, "domain", "a.com"},
		{"不存在", []string{"a", "nope"}, "domain", "a.com"}, // 第一个已改,第二个不存在 → 整体不写
		{"已停用", []string{"off"}, "domain", "a.com"},
		{"目标不合法", []string{"a"}, "domain", "not valid"},
		{"缺类型", []string{"a"}, "", "a.com"},
		{"太多", make([]string, 65), "domain", "a.com"},
	}
	for i := range cases[6].ids {
		cases[6].ids[i] = string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	for _, c := range cases {
		var in *InputError
		if _, err := s.Add(ctx, c.text, c.typ, c.ids); !errors.As(err, &in) {
			t.Errorf("%s: 应报 InputError,得到 %v", c.name, err)
		}
		if !reflect.DeepEqual(ms.list, before) || ms.writes != 0 {
			t.Fatalf("%s: 出错时不该留下改动", c.name)
		}
	}
}

func TestRemove(t *testing.T) {
	ms := &memStore{list: policyList()}
	s := New(newFake(), ms)
	ctx := context.Background()

	c, err := s.Remove(ctx, RemoveRequest{Source: SourcePolicy, PolicyID: "a", Type: "domain_suffix", Value: "NetFlix.com"})
	if err != nil || !c.Changed {
		t.Fatalf("Remove = %+v, %v", c, err)
	}
	m, _ := findPolicy(ms.list, "a")
	if got := strList(m["domainSuffix"]); len(got) != 0 {
		t.Errorf("domainSuffix = %v", got)
	}
	notes := m["notes"].(map[string]any)
	if _, ok := notes["domainSuffix:netflix.com"]; ok {
		t.Errorf("备注应一并删掉: %v", notes)
	}
	if notes["ruleset:geosite-cn"] != "别动" {
		t.Errorf("别的备注不该动: %v", notes)
	}
	// 幂等:再删一次没变化
	if c, err := s.Remove(ctx, RemoveRequest{Source: SourcePolicy, PolicyID: "a", Type: "domain_suffix", Value: "netflix.com"}); err != nil || c.Changed {
		t.Errorf("再删应无变化: %+v %v", c, err)
	}
	// camelCase 的类型名也认
	m["ipCidr"] = []any{"1.1.1.0/24"}
	if c, err := s.Remove(ctx, RemoveRequest{Source: SourcePolicy, PolicyID: "a", Type: "ipCidr", Value: "1.1.1.9/24"}); err != nil || !c.Changed {
		t.Errorf("camelCase 类型应能删: %+v %v", c, err)
	}
	// 错误
	bad := []RemoveRequest{
		{Source: SourceRuleset, PolicyID: "a", Type: "domain_suffix", Value: "x.com"},
		{Source: SourcePolicy, PolicyID: "nope", Type: "domain", Value: "x.com"},
		{Source: SourcePolicy, PolicyID: "a", Type: "domain_regex", Value: "x"},
		{Source: SourcePolicy, PolicyID: "", Type: "domain", Value: "x.com"},
		{Source: SourcePolicy, PolicyID: "a", Type: "domain", Value: " "},
	}
	for _, r := range bad {
		var in *InputError
		if _, err := s.Remove(ctx, r); !errors.As(err, &in) {
			t.Errorf("%+v 应报 InputError,得到 %v", r, err)
		}
	}
}

// 用真实的档案服务和存储跑一遍增删:确认 UpdatePolicies 与 Check 配合,档案里别的东西不动。
func TestAddRemoveOnRealProfile(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prof := profile.New(st, nil, nil)
	ctx := context.Background()
	if _, err := prof.Patch(ctx, map[string]any{
		"ipv6":    true,
		"routing": map[string]any{"policies": policyList()},
	}); err != nil {
		t.Fatal(err)
	}
	s := New(newFake(), prof)
	if c, err := s.Add(ctx, "api.example.com", "domain", []string{"policy-1"}); err != nil || !c.Changed {
		t.Fatalf("Add = %+v, %v", c, err)
	}
	got, _ := prof.Get(ctx)
	if got["ipv6"] != true {
		t.Errorf("档案别的字段被动了: ipv6 = %v", got["ipv6"])
	}
	pol := got["routing"].(map[string]any)["policies"].([]any)
	if len(pol) != 3 {
		t.Fatalf("站点集个数变了: %d", len(pol))
	}
	if d := strList(pol[1].(map[string]any)["domain"]); !reflect.DeepEqual(d, []string{"api.example.com"}) {
		t.Errorf("domain = %v", d)
	}
	if c, err := s.Remove(ctx, RemoveRequest{Source: SourcePolicy, PolicyID: "policy-1", Type: "domain", Value: "api.example.com"}); err != nil || !c.Changed {
		t.Fatalf("Remove = %+v, %v", c, err)
	}
	got, _ = prof.Get(ctx)
	pol = got["routing"].(map[string]any)["policies"].([]any)
	if d := strList(pol[1].(map[string]any)["domain"]); len(d) != 0 {
		t.Errorf("删完 domain = %v", d)
	}
}
