package dnsfilter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func parseOf(t *testing.T, body string) *Parsed {
	t.Helper()
	p, err := ParseList([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMatcherSemantics(t *testing.T) {
	s := Settings{
		Enabled: true,
		Lists: []List{
			{ID: "a", Name: "ListA", Enabled: true},
			{ID: "b", Name: "ListB", Enabled: true},
			{ID: "off", Name: "Off", Enabled: false},
		},
		AllowDomains: []string{"mine.ads.com", "*.trusted.org"},
	}
	parsed := map[string]*Parsed{
		"a":   parseOf(t, "||ads.com^\n0.0.0.0 tracker.net\n/^metrics\\d+\\.x\\.io$/\n||both.org^\n@@||ok.ads.com^\n||imp.org^$important\n"),
		"b":   parseOf(t, "||both.org^\n@@||imp.org^\n||trusted.org^\n||unlisted.io^\n"),
		"off": parseOf(t, "||disabled.example^\n"),
	}
	m := NewMatcher(s, parsed)
	cases := []struct {
		domain string
		block  bool
		list   string
	}{
		{"ads.com", true, "ListA"},
		{"x.y.ads.com", true, "ListA"},
		{"ADS.com.", true, "ListA"}, // 大小写和末尾的点
		{"notads.com", false, ""},   // 后缀要按整段比
		{"tracker.net", true, "ListA"},
		{"a.tracker.net", false, ""}, // hosts 行是精确匹配
		{"metrics42.x.io", true, "ListA"},
		{"metricsx.x.io", false, ""},
		{"both.org", true, "ListA"}, // 同时在两份里,取排在前面的
		{"ok.ads.com", false, ""},   // 名单里的普通放行
		{"mine.ads.com", false, ""}, // 用户放行(精确)
		{"www.mine.ads.com", true, "ListA"},
		{"a.trusted.org", false, ""}, // 用户放行(泛域名)
		{"trusted.org", false, ""},
		{"imp.org", true, "ListA"}, // $important 的拦截不被普通放行(B 里的 @@)排除
		{"disabled.example", false, ""},
		{"unlisted.io", true, "ListB"},
		{"", false, ""},
	}
	for _, c := range cases {
		if b, l := m.Check(c.domain); b != c.block || l != c.list {
			t.Errorf("%q: got (%v,%q) want (%v,%q)", c.domain, b, l, c.block, c.list)
		}
	}
	if b, _ := (*Matcher)(nil).Check("ads.com"); b {
		t.Error("nil 的 Matcher 不拦任何域名")
	}
}

func TestMatcherImportantAllow(t *testing.T) {
	s := Settings{Enabled: true, Lists: []List{{ID: "a", Name: "A", Enabled: true}}}
	m := NewMatcher(s, map[string]*Parsed{"a": parseOf(t, "||x.org^$important\n@@||x.org^$important\n||y.org^$important\n")})
	if b, _ := m.Check("x.org"); b {
		t.Error("$important 的放行应排除 $important 的拦截")
	}
	if b, l := m.Check("y.org"); !b || l != "A" {
		t.Errorf("y.org: %v %q", b, l)
	}
}

func TestServiceClassify(t *testing.T) {
	dir := t.TempDir()
	sv, set := newMatchService(t, dir)
	if b, _ := sv.Classify("ads.example.com"); b {
		t.Fatal("还没应用过,不该标被拦截")
	}
	set(true, "||example.com^\n")
	sv.mc.invalidate() // 真实流程里 apply 成功后会做这一步
	if b, l := sv.Classify("ads.example.com"); !b || l != "AdList" {
		t.Fatalf("应用后应命中: %v %q", b, l)
	}
	// 30 秒内不重建:改了缓存文件也不变;过了 TTL 才重新读
	set(true, "||other.org^\n")
	if b, _ := sv.Classify("ads.example.com"); !b {
		t.Error("TTL 内应沿用旧匹配器")
	}
	advance(sv, matcherTTL+time.Second)
	if b, _ := sv.Classify("ads.example.com"); b {
		t.Error("过了 TTL 应按新缓存重建")
	}
	if b, l := sv.Classify("x.other.org"); !b || l != "AdList" {
		t.Errorf("新名单应生效: %v %q", b, l)
	}
	// 过滤关掉:不再标被拦截
	set(false, "||other.org^\n")
	advance(sv, matcherTTL+time.Second)
	if b, _ := sv.Classify("x.other.org"); b {
		t.Error("过滤没启用不该标被拦截")
	}
}

type fixedProfile struct{ p map[string]any }

func (f *fixedProfile) Get(context.Context) (map[string]any, error) { return f.p, nil }

var testNow = time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)

func advance(sv *Service, d time.Duration) { testNow = testNow.Add(d) }

// newMatchService 造一个带名单缓存目录的 Service;set(enabled, 名单内容) 一步改好设置、已应用状态和缓存文件。
func newMatchService(t *testing.T, dir string) (*Service, func(enabled bool, body string)) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prof := &fixedProfile{p: map[string]any{}}
	sv := &Service{Profiles: prof, Store: st, CacheDir: filepath.Join(dir, "cache"), Now: func() time.Time { return testNow }}
	var version int64
	set := func(enabled bool, body string) {
		version++ // 名单重新下载过:更新时间变了,匹配器据此重建
		prof.p = map[string]any{"dns": map[string]any{"filter": map[string]any{
			"enabled": enabled,
			"lists":   []any{map[string]any{"id": "a", "name": "AdList", "url": "https://example.com/a.txt", "enabled": true}},
		}}}
		if err := os.MkdirAll(sv.CacheDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sv.cachePath("a"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := sv.update(func(s *state) {
			s.Lists["a"] = listState{URL: "https://example.com/a.txt", UpdatedAt: version}
			s.Applied = &Applied{Enabled: enabled, Key: "k"}
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 一开始什么都没应用
	prof.p = map[string]any{"dns": map[string]any{"filter": map[string]any{"enabled": true}}}
	return sv, set
}
