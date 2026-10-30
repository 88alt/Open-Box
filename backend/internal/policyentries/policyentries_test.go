package policyentries

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

type fakeProfiles struct {
	prof map[string]any
	err  error
}

func (f fakeProfiles) Get(context.Context) (map[string]any, error) { return f.prof, f.err }

type fakeCore struct {
	rt  Routing
	err error
}

func (f fakeCore) NormalizeRouting(context.Context, map[string]any) (Routing, error) {
	return f.rt, f.err
}

type fakeLists struct {
	res   map[string]*rulelist.Result
	calls []string
}

func (f *fakeLists) Load(_ context.Context, u string) (*rulelist.Result, error) {
	f.calls = append(f.calls, u)
	if r, ok := f.res[u]; ok {
		return r, nil
	}
	return nil, errors.New("拉取失败")
}

func routing() Routing {
	var rt Routing
	rt.Custom.Name = "前置自定义分流"
	rt.Fallback.Name = "其他"
	return rt
}

func newSvc(rt Routing, lists *fakeLists) *Service {
	return New(fakeProfiles{prof: map[string]any{"routing": map[string]any{}}}, fakeCore{rt: rt}, lists)
}

func TestListTagForURL(t *testing.T) {
	// 取自 core 金标准里的向量,两边算法必须一致
	for u, want := range map[string]string{
		"https://e.com/x.list":       "list-8116e1a1",
		"https://example.com/a.list": "list-1aea4a3b",
		"https://例え.jp/😀.list":       "list-959f1739",
		"HTTP://X.org/b":             "list-f6a9a260",
		"  https://e.com/x.list \n":  "list-8116e1a1", // 首尾空白不影响
		"\ufeffhttps://e.com/x.list": "list-8116e1a1", // JS 的 trim 也去 BOM
		"":                           "",
		"   ":                        "",
	} {
		if got := ListTagForURL(u); got != want {
			t.Errorf("ListTagForURL(%q) = %q,想要 %q", u, got, want)
		}
	}
}

func TestURLLabelDropsSecrets(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:pw@example.com/path/a.list?token=SECRET#frag": "example.com/path/a.list",
		"https://example.com/": "example.com",
		"not a url":            "规则集链接",
		"":                     "规则集链接",
	} {
		got := urlLabel(in)
		if got != want {
			t.Errorf("urlLabel(%q) = %q,想要 %q", in, got, want)
		}
		if strings.Contains(got, "SECRET") || strings.Contains(got, "pw") {
			t.Errorf("标签泄露了凭据: %q", got)
		}
	}
}

func TestQueryInputErrors(t *testing.T) {
	s := newSvc(routing(), &fakeLists{})
	for _, q := range []Query{
		{Name: ""}, {Name: "  "}, {Name: "a", Tab: "x"}, {Name: "a", Sort: "x"},
	} {
		var in *InputError
		if _, err := s.Query(context.Background(), q); !errors.As(err, &in) {
			t.Errorf("%+v 应返回 InputError,得到 %v", q, err)
		}
	}
	var nf *NotFoundError
	if _, err := s.Query(context.Background(), Query{Name: "不存在", Limit: 10}); !errors.As(err, &nf) {
		t.Errorf("不存在的站点集应返回 NotFoundError,得到 %v", err)
	}
}

func TestQueryNoCore(t *testing.T) {
	s := New(fakeProfiles{}, nil, &fakeLists{})
	if _, err := s.Query(context.Background(), Query{Name: "a", Limit: 10}); !errors.Is(err, ErrCoreUnavailable) {
		t.Fatalf("无核心应返回 ErrCoreUnavailable,得到 %v", err)
	}
}

func TestQueryProfileAndCoreErrorsPassThrough(t *testing.T) {
	boom := errors.New("boom")
	s := New(fakeProfiles{err: boom}, fakeCore{}, &fakeLists{})
	if _, err := s.Query(context.Background(), Query{Name: "a", Limit: 10}); !errors.Is(err, boom) {
		t.Errorf("档案读取错误应原样返回,得到 %v", err)
	}
	s = New(fakeProfiles{prof: map[string]any{}}, fakeCore{err: boom}, &fakeLists{})
	if _, err := s.Query(context.Background(), Query{Name: "a", Limit: 10}); !errors.Is(err, boom) {
		t.Errorf("核心错误应原样返回,得到 %v", err)
	}
}

func TestFallbackHasNoEntries(t *testing.T) {
	s := newSvc(routing(), &fakeLists{})
	res, err := s.Query(context.Background(), Query{Name: "其他", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fallback || res.Total != 0 || len(res.Entries) != 0 || res.Entries == nil || res.Missing == nil {
		t.Fatalf("兜底应是 fallback、无条目,且 entries / missing 是空数组而不是 null: %+v", res)
	}
}

func policyRouting() (Routing, string) {
	rt := routing()
	const listURL = "https://lists.example.com/a/x.list?token=SECRET"
	rt.Policies = []Policy{{
		Name:          "流媒体",
		Rulesets:      []string{"geosite-netflix", "geoip-us", ListTagForURL(listURL), "list-deadbeef"},
		RuleUrls:      []string{listURL},
		Domain:        []string{"a.com", "a.com"},
		DomainSuffix:  []string{"b.com"},
		DomainKeyword: []string{"kw"},
		IPCidr:        []string{"10.0.0.0/8"},
	}}
	return rt, listURL
}

func fixedLists(u string) *fakeLists {
	return &fakeLists{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{
		{Type: rulelist.TypeDomainSuffix, Value: "c.org"},
		{Type: rulelist.TypeIPCIDR, Value: "192.0.2.0/24"},
		{Type: rulelist.TypeDomainRegex, Value: `^x\d+\.org$`},
	}}}}
}

func TestPolicyEntriesAndMissing(t *testing.T) {
	rt, u := policyRouting()
	s := newSvc(rt, fixedLists(u))
	res, err := s.Query(context.Background(), Query{Name: "流媒体", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"domain", "domain", "a.com", "custom"}, // 重复的 a.com 只留一条
		{"domain_suffix", "domain", "b.com", "custom"},
		{"domain_keyword", "domain", "kw", "custom"},
		{"ip_cidr", "ip", "10.0.0.0/8", "custom"},
		{"domain_suffix", "domain", "c.org", "lists.example.com/a/x.list"},
		{"ip_cidr", "ip", "192.0.2.0/24", "lists.example.com/a/x.list"},
		{"domain_regex", "domain", `^x\d+\.org$`, "lists.example.com/a/x.list"},
	}
	if !reflect.DeepEqual(res.Entries, want) {
		t.Fatalf("条目不符:\n得到 %+v\n想要 %+v", res.Entries, want)
	}
	// 规则库里的分类如实报缺;链接对应的 list-* 不算缺;核心给的另一个 list-* 没有链接可展开,算缺
	if !reflect.DeepEqual(res.Missing, []string{"geosite-netflix", "geoip-us", "list-deadbeef"}) {
		t.Errorf("missing = %v", res.Missing)
	}
	if res.Counts != (Counts{All: 7, Domain: 5, IP: 2}) || res.Total != 7 || res.Matched != 7 || res.HasMore {
		t.Errorf("计数不符: %+v", res)
	}
	for _, e := range res.Entries {
		if strings.Contains(e.Source, "SECRET") {
			t.Errorf("来源泄露了 token: %q", e.Source)
		}
	}
}

func TestListFetchFailureGoesToMissing(t *testing.T) {
	rt, u := policyRouting()
	s := newSvc(rt, &fakeLists{}) // 什么都拉不到
	res, err := s.Query(context.Background(), Query{Name: "流媒体", Limit: 100})
	if err != nil {
		t.Fatalf("单个链接拉取失败不应让整个查询失败: %v", err)
	}
	if len(res.Entries) != 4 {
		t.Errorf("手写条件应照常返回,得到 %d 条", len(res.Entries))
	}
	if !contains(res.Missing, "lists.example.com/a/x.list") {
		t.Errorf("拉取失败的链接应列入 missing: %v", res.Missing)
	}
	if contains(res.Missing, u) {
		t.Errorf("missing 不能带完整链接: %v", res.Missing)
	}
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func TestContextCancelStopsQuery(t *testing.T) {
	rt, u := policyRouting()
	s := newSvc(rt, &fakeLists{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = u
	if _, err := s.Query(ctx, Query{Name: "流媒体", Limit: 10}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled,得到 %v", err)
	}
}

func TestTabSearchSortPaging(t *testing.T) {
	rt, u := policyRouting()
	s := newSvc(rt, fixedLists(u))
	ctx := context.Background()

	res, _ := s.Query(ctx, Query{Name: "流媒体", Tab: "ip", Limit: 100})
	if res.Total != 2 || res.Matched != 2 || res.Counts.All != 7 {
		t.Errorf("ip 档: total=%d matched=%d counts=%+v(计数不随档变)", res.Total, res.Matched, res.Counts)
	}
	for _, e := range res.Entries {
		if e.Family != "ip" {
			t.Errorf("ip 档里混进了 %+v", e)
		}
	}

	// 搜索词不区分大小写,内容和来源都匹配;total 不受搜索影响
	res, _ = s.Query(ctx, Query{Name: "流媒体", Q: "  C.ORG ", Limit: 100})
	if res.Total != 7 || res.Matched != 1 || res.Entries[0].Content != "c.org" {
		t.Errorf("按内容搜索: %+v", res)
	}
	res, _ = s.Query(ctx, Query{Name: "流媒体", Q: "LISTS.example", Limit: 100})
	if res.Matched != 3 {
		t.Errorf("按来源搜索应命中 3 条,得到 %d", res.Matched)
	}

	// 排序:升序 / 降序,相等项保持原顺序
	res, _ = s.Query(ctx, Query{Name: "流媒体", Sort: "content", Limit: 100})
	if got := contents(res.Entries); !reflect.DeepEqual(got[:3], []string{"10.0.0.0/8", "192.0.2.0/24", "^x\\d+\\.org$"}) {
		t.Errorf("content 升序: %v", got)
	}
	res, _ = s.Query(ctx, Query{Name: "流媒体", Sort: "content", Desc: true, Limit: 100})
	if got := contents(res.Entries); got[0] != "kw" || got[len(got)-1] != "10.0.0.0/8" {
		t.Errorf("content 降序: %v", got)
	}
	res, _ = s.Query(ctx, Query{Name: "流媒体", Sort: "type", Limit: 100})
	if res.Entries[0].Type != "domain" || res.Entries[len(res.Entries)-1].Type != "ip_cidr" {
		t.Errorf("type 升序: %+v", res.Entries)
	}

	// 分页
	var seen []string
	for off := 0; ; off += 3 {
		res, _ = s.Query(ctx, Query{Name: "流媒体", Offset: off, Limit: 3})
		seen = append(seen, contents(res.Entries)...)
		if !res.HasMore {
			break
		}
		if off > 100 {
			t.Fatal("分页停不下来")
		}
	}
	if len(seen) != 7 {
		t.Errorf("分页应共得到 7 条,得到 %d: %v", len(seen), seen)
	}
	res, _ = s.Query(ctx, Query{Name: "流媒体", Offset: 999, Limit: 3})
	if res.Offset != 7 || len(res.Entries) != 0 || res.HasMore {
		t.Errorf("越界 offset 应收到末尾: %+v", res)
	}
}

func contents(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Content
	}
	return out
}

func TestCustomPolicy(t *testing.T) {
	rt := routing()
	const listURL = "https://lists.example.com/c.list"
	rt.Custom.Rules = []CustomRule{
		{"domain", "a.com"}, {"domainSuffix", "b.com"}, {"domainKeyword", "kw"}, {"ipCidr", "10.0.0.0/8"},
		{"port", "51820"}, {"geosite", "cn"}, {"geoip", "cn"}, {"ruleset", "geosite-x"}, {"ruleUrl", listURL},
	}
	s := newSvc(rt, &fakeLists{res: map[string]*rulelist.Result{listURL: {Entries: []rulelist.Entry{{Type: rulelist.TypeDomain, Value: "l.org"}}}}})
	res, err := s.Query(context.Background(), Query{Name: "前置自定义分流", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Fallback || len(res.Entries) != 6 {
		t.Fatalf("前置自定义分流: %+v", res)
	}
	if res.Entries[4] != (Entry{"port", "other", "51820", "custom"}) {
		t.Errorf("端口规则应是 other 族: %+v", res.Entries[4])
	}
	if res.Entries[5].Source != "lists.example.com/c.list" {
		t.Errorf("链接来源: %+v", res.Entries[5])
	}
	if !reflect.DeepEqual(res.Missing, []string{"geosite-cn", "geoip-cn", "geosite-x"}) {
		t.Errorf("missing = %v", res.Missing)
	}
	if res.Counts != (Counts{All: 6, Domain: 4, IP: 1}) {
		t.Errorf("counts = %+v(other 族只算进 all)", res.Counts)
	}
}

func TestEntryCapTruncates(t *testing.T) {
	rt := routing()
	big := make([]string, maxEntries+5)
	for i := range big {
		big[i] = fmt.Sprintf("h%d.example", i)
	}
	rt.Policies = []Policy{{Name: "大", Domain: big}}
	s := newSvc(rt, &fakeLists{})
	res, err := s.Query(context.Background(), Query{Name: "大", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != maxEntries || !contains(res.Missing, truncatedNote) {
		t.Errorf("超过上限应截断并提示: total=%d missing=%v", res.Total, res.Missing)
	}
}

// ---- 真实 core:规范化(默认名、去重、拆出 ruleUrls 的 list-* 名)交给核心,这里验证接起来能用 ----

var (
	buildOnce sync.Once
	corePath  string
	buildErr  error
)

func realCore(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "obcore")
		if err != nil {
			buildErr = err
			return
		}
		corePath = filepath.Join(dir, "openbox-core")
		if runtime.GOOS == "windows" {
			corePath += ".exe" // Windows 上没有扩展名的文件运行不了
		}
		cmd := exec.Command("go", "build", "-o", corePath, "./cmd/openbox-core")
		cmd.Dir = filepath.Join("..", "..", "..", "core")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("%w: %s", err, out)
		}
	})
	if buildErr != nil {
		t.Skipf("编译不了 core,跳过: %v", buildErr)
	}
	return corePath
}

func TestWithRealCore(t *testing.T) {
	bin := realCore(t)
	const u = "https://e.com/x.list"
	prof := map[string]any{"routing": map[string]any{
		"policies": []any{
			map[string]any{"name": "美国", "rulesets": []any{"geosite-netflix"}, "ruleUrls": []any{u}, "domainSuffix": []any{"b.com"}},
		},
		"custom": map[string]any{"rules": []any{map[string]any{"type": "domain", "value": "a.com", "outbound": "direct"}}},
	}}
	lists := &fakeLists{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{{Type: rulelist.TypeDomain, Value: "l.org"}}}}}
	s := New(fakeProfiles{prof: prof}, CoreAdapter{C: core.New(bin)}, lists)
	ctx := context.Background()

	res, err := s.Query(ctx, Query{Name: "美国", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(contents(res.Entries), []string{"b.com", "l.org"}) {
		t.Errorf("条目: %+v", res.Entries)
	}
	// 核心把链接折成 list-8116e1a1 追加进 rulesets;它应被识别为已展开,只剩 geosite-netflix 报缺
	if !reflect.DeepEqual(res.Missing, []string{"geosite-netflix"}) {
		t.Errorf("missing = %v(后端算的 list 名必须和核心一致)", res.Missing)
	}
	if res, err = s.Query(ctx, Query{Name: "前置自定义分流", Limit: 100}); err != nil || len(res.Entries) != 1 {
		t.Errorf("前置自定义分流: %+v %v", res, err)
	}
	if res, err = s.Query(ctx, Query{Name: "其他", Limit: 100}); err != nil || !res.Fallback {
		t.Errorf("兜底: %+v %v", res, err)
	}
	var nf *NotFoundError
	if _, err = s.Query(ctx, Query{Name: "没有", Limit: 100}); !errors.As(err, &nf) {
		t.Errorf("不存在: %v", err)
	}
}
