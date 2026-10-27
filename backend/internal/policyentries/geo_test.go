package policyentries

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/geodata"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

// fakeGeo 按分类名返回预设结果;没预设的返回 geodata.ErrNotFound。asked 记录被问过的分类名。
type fakeGeo struct {
	res   map[string]*geodata.Result
	errs  map[string]error
	asked []string
}

func (f *fakeGeo) Entries(_ context.Context, tag string) (*geodata.Result, error) {
	f.asked = append(f.asked, tag)
	if err := f.errs[tag]; err != nil {
		return nil, err
	}
	if r := f.res[tag]; r != nil {
		return r, nil
	}
	return nil, geodata.ErrNotFound
}

func geoRes(entries ...rulelist.Entry) *geodata.Result {
	return &geodata.Result{Entries: append([]rulelist.Entry{}, entries...)}
}

func suffix(v string) rulelist.Entry {
	return rulelist.Entry{Type: rulelist.TypeDomainSuffix, Value: v}
}
func cidr(v string) rulelist.Entry { return rulelist.Entry{Type: rulelist.TypeIPCIDR, Value: v} }

func TestPolicyExpandsGeoCategories(t *testing.T) {
	rt := routing()
	rt.Policies = []Policy{{
		Name:     "流媒体",
		Rulesets: []string{"geosite-netflix", "geoip-us", "geosite-gone", "list-deadbeef", "geosite-netflix"},
		Domain:   []string{"a.com"},
	}}
	g := &fakeGeo{res: map[string]*geodata.Result{
		"geosite-netflix": geoRes(suffix("netflix.com"), suffix("nflxvideo.net")),
		"geoip-us":        geoRes(cidr("8.8.8.0/24")),
	}}
	s := newSvc(rt, &fakeLists{}).WithGeo(g)
	res, err := s.Query(context.Background(), Query{Name: "流媒体", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"domain", "domain", "a.com", "custom"},
		{"domain_suffix", "domain", "netflix.com", "geosite-netflix"},
		{"domain_suffix", "domain", "nflxvideo.net", "geosite-netflix"}, // 重复出现的 geosite-netflix 只展开一份
		{"ip_cidr", "ip", "8.8.8.0/24", "geoip-us"},
	}
	if !reflect.DeepEqual(res.Entries, want) {
		t.Fatalf("条目不符:\n得到 %+v\n想要 %+v", res.Entries, want)
	}
	// 规则库里没有的分类、没有链接可展开的 list-* 仍如实报缺
	if !reflect.DeepEqual(res.Missing, []string{"geosite-gone", "list-deadbeef"}) {
		t.Errorf("missing = %v", res.Missing)
	}
	if res.Counts != (Counts{All: 4, Domain: 3, IP: 1}) {
		t.Errorf("counts = %+v", res.Counts)
	}
	// 只去问规则库里可能有的分类名;list-* 不去问
	if !reflect.DeepEqual(g.asked, []string{"geosite-netflix", "geoip-us", "geosite-gone", "geosite-netflix"}) {
		t.Errorf("规则库被问到: %v", g.asked)
	}

	// 搜索同时匹配内容和来源:搜分类名能把这一类全捞出来
	res, err = s.Query(context.Background(), Query{Name: "流媒体", Q: "GEOSITE-NETFLIX", Limit: 100})
	if err != nil || res.Matched != 2 || res.Total != 4 {
		t.Errorf("按来源搜索: %+v %v", res, err)
	}
	// 按 ip 档只看 IP,来源是 geoip 分类
	res, _ = s.Query(context.Background(), Query{Name: "流媒体", Tab: "ip", Limit: 100})
	if len(res.Entries) != 1 || res.Entries[0].Source != "geoip-us" {
		t.Errorf("ip 档: %+v", res.Entries)
	}
}

func TestSameContentFromDifferentCategoriesKeptSeparately(t *testing.T) {
	rt := routing()
	rt.Policies = []Policy{{Name: "p", Rulesets: []string{"geosite-a", "geosite-b"}}}
	g := &fakeGeo{res: map[string]*geodata.Result{
		"geosite-a": geoRes(suffix("x.com"), suffix("x.com")), // 同一分类内重复只留一条
		"geosite-b": geoRes(suffix("x.com")),                  // 另一个分类里的同名条目来源不同,各留一条
	}}
	res, err := newSvc(rt, &fakeLists{}).WithGeo(g).Query(context.Background(), Query{Name: "p", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{"domain_suffix", "domain", "x.com", "geosite-a"}, {"domain_suffix", "domain", "x.com", "geosite-b"}}
	if !reflect.DeepEqual(res.Entries, want) {
		t.Fatalf("%+v", res.Entries)
	}
}

func TestCustomPolicyExpandsGeoRows(t *testing.T) {
	rt := routing()
	rt.Custom.Rules = []CustomRule{
		{"geosite", "cn"}, {"geoip", "cn"}, {"ruleset", "geosite-x"}, {"geosite", "a b"}, {"ruleset", "list-abc"}, {"domain", "d.com"},
	}
	g := &fakeGeo{res: map[string]*geodata.Result{
		"geosite-cn": geoRes(suffix("baidu.com")),
		"geoip-cn":   geoRes(cidr("1.0.1.0/24")),
	}}
	res, err := newSvc(rt, &fakeLists{}).WithGeo(g).Query(context.Background(), Query{Name: "前置自定义分流", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"domain_suffix", "domain", "baidu.com", "geosite-cn"},
		{"ip_cidr", "ip", "1.0.1.0/24", "geoip-cn"},
		{"domain", "domain", "d.com", "custom"},
	}
	if !reflect.DeepEqual(res.Entries, want) {
		t.Fatalf("条目不符:\n得到 %+v\n想要 %+v", res.Entries, want)
	}
	// 名字不合法的("geosite-a b")不去问规则库,和不在库里的一样如实报缺;顺序按规则出现的顺序
	if !reflect.DeepEqual(res.Missing, []string{"geosite-x", "geosite-a b", "list-abc"}) {
		t.Errorf("missing = %v", res.Missing)
	}
	for _, a := range g.asked {
		if strings.ContainsAny(a, " ") || strings.HasPrefix(a, "list-") {
			t.Errorf("不该去问规则库: %q", a)
		}
	}
}

func TestGeoFailuresGoToMissingNotError(t *testing.T) {
	rt := routing()
	rt.Policies = []Policy{{Name: "p", Rulesets: []string{"geosite-nolib", "geosite-nosb", "geosite-broken", "geosite-ok"}}}
	g := &fakeGeo{
		res: map[string]*geodata.Result{"geosite-ok": geoRes(suffix("ok.com"))},
		errs: map[string]error{
			"geosite-nolib":  geodata.ErrNoLibrary,
			"geosite-nosb":   geodata.ErrNoSingBox,
			"geosite-broken": &geodata.DecodeError{Msg: "bad magic"},
		},
	}
	res, err := newSvc(rt, &fakeLists{}).WithGeo(g).Query(context.Background(), Query{Name: "p", Limit: 100})
	if err != nil {
		t.Fatalf("单个分类失败不该让整个查询失败: %v", err)
	}
	if !reflect.DeepEqual(res.Missing, []string{"geosite-nolib", "geosite-nosb", "geosite-broken"}) || len(res.Entries) != 1 {
		t.Errorf("missing=%v entries=%+v", res.Missing, res.Entries)
	}
}

func TestGeoTruncatedIsNoted(t *testing.T) {
	rt := routing()
	rt.Policies = []Policy{{Name: "p", Rulesets: []string{"geosite-big"}}}
	r := geoRes(suffix("a.com"))
	r.Truncated = true
	res, err := newSvc(rt, &fakeLists{}).WithGeo(&fakeGeo{res: map[string]*geodata.Result{"geosite-big": r}}).
		Query(context.Background(), Query{Name: "p", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 1 || !strings.HasPrefix(res.Missing[0], "geosite-big") || !strings.Contains(res.Missing[0], truncatedNote) {
		t.Errorf("规则库分类被截断应有提示: %v", res.Missing)
	}
}

func TestGeoCancelAborts(t *testing.T) {
	rt := routing()
	rt.Policies = []Policy{{Name: "p", Rulesets: []string{"geosite-a"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := &fakeGeo{errs: map[string]error{"geosite-a": context.Canceled}}
	if _, err := newSvc(rt, &fakeLists{}).WithGeo(g).Query(ctx, Query{Name: "p", Limit: 10}); !errors.Is(err, context.Canceled) {
		t.Fatalf("请求取消应把取消错误交回去,而不是当作缺失: %v", err)
	}
}

func TestWithGeoNilKeepsMissing(t *testing.T) {
	rt := routing()
	rt.Policies = []Policy{{Name: "p", Rulesets: []string{"geosite-a"}}}
	res, err := newSvc(rt, &fakeLists{}).WithGeo(nil).Query(context.Background(), Query{Name: "p", Limit: 10})
	if err != nil || !reflect.DeepEqual(res.Missing, []string{"geosite-a"}) || len(res.Entries) != 0 {
		t.Fatalf("没接规则库时仍应列进 missing: %+v %v", res, err)
	}
}

// 真实 core(规范化)+ 真实 sing-box(把源码编成 .srs 放进规则库)+ geodata:整条链接起来能用。
func TestWithRealCoreAndRealGeoLibrary(t *testing.T) {
	coreBin := realCore(t)
	sb, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	root := t.TempDir()
	lib := filepath.Join(root, "geodata")
	tmp := filepath.Join(root, "tmp")
	for _, d := range []string{lib, tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	compile := func(tag, src string) {
		t.Helper()
		in := filepath.Join(root, tag+".json")
		if err := os.WriteFile(in, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(sb, "rule-set", "compile", "--output", filepath.Join(lib, tag+".srs"), in).CombinedOutput(); err != nil {
			t.Fatalf("compile %s: %v\n%s", tag, err, out)
		}
	}
	compile("geosite-netflix", `{"version":3,"rules":[{"domain_suffix":["netflix.com","nflxvideo.net"]}]}`)
	compile("geoip-us", `{"version":3,"rules":[{"ip_cidr":["8.8.8.0/24"]}]}`)

	const u = "https://e.com/x.list"
	prof := map[string]any{"routing": map[string]any{
		"policies": []any{
			map[string]any{"name": "美国", "rulesets": []any{"geosite-netflix", "geoip-us", "geosite-absent"}, "ruleUrls": []any{u}, "domainSuffix": []any{"b.com"}},
		},
		"custom": map[string]any{"rules": []any{
			map[string]any{"type": "geosite", "value": "netflix", "outbound": "direct"},
			map[string]any{"type": "domain", "value": "a.com", "outbound": "direct"},
		}},
	}}
	lists := &fakeLists{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{{Type: rulelist.TypeDomain, Value: "l.org"}}}}}
	s := New(fakeProfiles{prof: prof}, CoreAdapter{C: core.New(coreBin)}, lists).WithGeo(geodata.New(lib, sb, tmp))
	ctx := context.Background()

	res, err := s.Query(ctx, Query{Name: "美国", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{} // 内容 → 来源
	for _, e := range res.Entries {
		got[e.Content] = e.Source
	}
	for content, source := range map[string]string{
		"b.com": "custom", "l.org": "e.com/x.list",
		"netflix.com": "geosite-netflix", "nflxvideo.net": "geosite-netflix", "8.8.8.0/24": "geoip-us",
	} {
		if got[content] != source {
			t.Errorf("%s 的来源 = %q,想要 %q(全部: %v)", content, got[content], source, got)
		}
	}
	// 只剩规则库里确实没有的那个报缺;链接对应的 list-* 仍认得出
	if !reflect.DeepEqual(res.Missing, []string{"geosite-absent"}) {
		t.Errorf("missing = %v", res.Missing)
	}

	res, err = s.Query(ctx, Query{Name: "前置自定义分流", Limit: 100})
	if err != nil || len(res.Missing) != 0 {
		t.Fatalf("前置自定义分流: %+v %v", res, err)
	}
	custom := map[string]string{}
	for _, e := range res.Entries {
		custom[e.Content] = e.Source
	}
	if custom["netflix.com"] != "geosite-netflix" || custom["a.com"] != "custom" {
		t.Errorf("前置自定义分流的 geosite 行没展开: %v", custom)
	}
}
