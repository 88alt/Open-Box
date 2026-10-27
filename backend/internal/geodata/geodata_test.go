package geodata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

func TestValidTag(t *testing.T) {
	cases := []struct {
		tag string
		ok  bool
	}{
		{"geosite-cn", true},
		{"geoip-cn", true},
		{"geosite-geolocation-!cn", true},
		{"geosite-steam@cn", true},
		{"geosite-category-ads-all", true},
		{"geosite-a.b_c", true},
		{"", false},
		{"geosite-", false},
		{"geoip-", false},
		{"cn", false},
		{"list-abc", false},
		{"geosite", false},
		{"geosite-../etc/passwd", false},
		{"geosite-a/b", false},
		{"geosite-a\\b", false},
		{"geosite-a b", false},
		{"geosite-a..b", false},
		{"geosite-中文", false},
		{"geosite-" + strings.Repeat("a", 200), false},
		{"GEOSITE-cn", false},
	}
	for _, c := range cases {
		if got := ValidTag(c.tag); got != c.ok {
			t.Errorf("ValidTag(%q)=%v,想要 %v", c.tag, got, c.ok)
		}
	}
}

// fakeEnv 是一个假 sing-box 加一个规则库目录。
//
// 假 sing-box 只认 `rule-set decompile --output <out> <in.srs>`:每次被调用往 calls 文件追加一行输入文件名,
// 输入名含 "fail" 时报错退出 1,否则把 fixtures/<分类名>.json 拷到 <out>。
type fakeEnv struct {
	lib      *Library
	libDir   string
	fixtures string
	calls    string
	tmp      string
}

func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("用 shell 脚本充当 sing-box,Windows 上跳过")
	}
	root := t.TempDir()
	e := &fakeEnv{
		libDir:   filepath.Join(root, "geodata"),
		fixtures: filepath.Join(root, "fixtures"),
		calls:    filepath.Join(root, "calls"),
		tmp:      filepath.Join(root, "tmp"),
	}
	for _, d := range []string{e.libDir, e.fixtures, e.tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "sing-box")
	script := fmt.Sprintf(`#!/bin/sh
[ "$1" = "rule-set" ] && [ "$2" = "decompile" ] && [ "$3" = "--output" ] || { echo "bad args: $*" >&2; exit 2; }
out="$4"; in="$5"
echo "$in" >> %q
case "$in" in
  *fail*) echo "FATAL decode $in: bad magic (writing $out)" >&2; exit 1;;
esac
name=$(basename "$in" .srs)
cp %q/"$name".json "$out"
`, e.calls, e.fixtures)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e.lib = New(e.libDir, bin, e.tmp)
	return e
}

// add 在规则库里放一个分类:.srs 文件内容随意(假 sing-box 不读),fixture 是假 sing-box 要吐出的 JSON。
func (e *fakeEnv) add(t *testing.T, tag, fixture string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.libDir, tag+".srs"), []byte("srs:"+tag), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.fixtures, tag+".json"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *fakeEnv) callCount(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(e.calls)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

const mixedFixture = `{
  "version": 3,
  "rules": [
    {
      "domain": ["a.example.com", "b.example.com"],
      "domain_suffix": [".example.com", "example.org"],
      "domain_keyword": ["kw"],
      "domain_regex": ["^r[0-9]+\\.example$"],
      "port": [443]
    },
    {"ip_cidr": "10.0.0.0/8", "domain_suffix": [".example.com"], "domain": null},
    {"type": "logical", "mode": "and", "rules": [{"domain": ["never.example"]}]},
    {"port": [80]},
    {"ip_cidr": ["192.0.2.0/24", "", "10.0.0.0/8"]}
  ]
}`

func TestEntriesParsesAndDedups(t *testing.T) {
	e := newFakeEnv(t)
	e.add(t, "geosite-mixed", mixedFixture)
	res, err := e.lib.Entries(context.Background(), "geosite-mixed")
	if err != nil {
		t.Fatal(err)
	}
	want := []rulelist.Entry{
		{Type: "domain", Value: "a.example.com"},
		{Type: "domain", Value: "b.example.com"},
		{Type: "domain_suffix", Value: ".example.com"},
		{Type: "domain_suffix", Value: "example.org"},
		{Type: "domain_keyword", Value: "kw"},
		{Type: "domain_regex", Value: `^r[0-9]+\.example$`},
		{Type: "ip_cidr", Value: "10.0.0.0/8"}, // 单个字符串写法
		{Type: "ip_cidr", Value: "192.0.2.0/24"},
	}
	if fmt.Sprint(res.Entries) != fmt.Sprint(want) {
		t.Fatalf("条目不对:\n got  %v\n want %v", res.Entries, want)
	}
	// 逻辑规则和只有端口的规则各算一条被忽略的
	if res.Skipped != 2 || res.Truncated {
		t.Fatalf("Skipped=%d Truncated=%v", res.Skipped, res.Truncated)
	}
	// 临时目录必须清理干净
	if left, _ := os.ReadDir(e.tmp); len(left) != 0 {
		t.Fatalf("残留临时文件: %v", left)
	}
}

func TestEntriesEmptyRuleSetIsNotNil(t *testing.T) {
	e := newFakeEnv(t)
	e.add(t, "geoip-empty", `{"version":3,"rules":[]}`)
	res, err := e.lib.Entries(context.Background(), "geoip-empty")
	if err != nil {
		t.Fatal(err)
	}
	if res.Entries == nil || len(res.Entries) != 0 {
		t.Fatalf("空分类应得到空切片而不是 nil: %#v", res.Entries)
	}
}

func TestEntriesErrors(t *testing.T) {
	e := newFakeEnv(t)
	e.add(t, "geosite-ok", `{"version":3,"rules":[{"domain":["x.example"]}]}`)
	e.add(t, "geosite-fail", `{}`)
	e.add(t, "geosite-garbage", `这不是 JSON`)
	e.add(t, "geosite-shape", `{"version":3,"rules":{"domain":"x"}}`)
	if err := os.Mkdir(filepath.Join(e.libDir, "geosite-dir.srs"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := e.lib.Entries(ctx, "geosite-nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("没有的分类: %v", err)
	}
	if _, err := e.lib.Entries(ctx, "geosite-dir"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("目录不算分类文件: %v", err)
	}
	for _, bad := range []string{"", "cn", "geosite-../x", "list-abc"} {
		if _, err := e.lib.Entries(ctx, bad); !errors.Is(err, ErrInvalidTag) {
			t.Fatalf("%q 应是 ErrInvalidTag: %v", bad, err)
		}
	}
	if _, err := New(filepath.Join(e.libDir, "missing"), e.lib.SingBox, e.tmp).Entries(ctx, "geosite-ok"); !errors.Is(err, ErrNoLibrary) {
		t.Fatalf("规则库目录不存在: %v", err)
	}
	if _, err := New(e.libDir, "no-such-sing-box-xyz", e.tmp).Entries(ctx, "geosite-ok"); !errors.Is(err, ErrNoSingBox) {
		t.Fatalf("没有 sing-box: %v", err)
	}

	var de *DecodeError
	_, err := e.lib.Entries(ctx, "geosite-fail")
	if !errors.As(err, &de) {
		t.Fatalf("sing-box 失败应是 *DecodeError: %v", err)
	}
	if !strings.Contains(de.Msg, "bad magic") || !strings.Contains(de.Msg, "geosite-fail.srs") ||
		strings.Contains(de.Msg, e.libDir) || strings.Contains(de.Msg, e.tmp) {
		t.Fatalf("错误应带原因且不含真实路径: %q", de.Msg)
	}
	for _, tag := range []string{"geosite-garbage", "geosite-shape"} {
		if _, err := e.lib.Entries(ctx, tag); !errors.As(err, &de) {
			t.Fatalf("%s 的结果不是合法规则集 JSON,应是 *DecodeError: %v", tag, err)
		}
	}
	if left, _ := os.ReadDir(e.tmp); len(left) != 0 {
		t.Fatalf("失败后也要清理临时文件: %v", left)
	}
	// 失败不缓存:再来一次还是失败而不是读到半截结果
	if _, err := e.lib.Entries(ctx, "geosite-fail"); err == nil {
		t.Fatal("失败的分类不应被缓存")
	}
}

func TestEntriesFileTooBig(t *testing.T) {
	e := newFakeEnv(t)
	p := filepath.Join(e.libDir, "geosite-big.srs")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFileBytes + 1); err != nil { // 稀疏文件,不占磁盘
		t.Fatal(err)
	}
	f.Close()
	var de *DecodeError
	if _, err := e.lib.Entries(context.Background(), "geosite-big"); !errors.As(err, &de) || !strings.Contains(de.Msg, "太大") {
		t.Fatalf("err=%v", err)
	}
	if e.callCount(t) != 0 {
		t.Fatal("超大文件不该交给 sing-box")
	}
}

func TestEntriesCache(t *testing.T) {
	e := newFakeEnv(t)
	for _, tag := range []string{"geosite-a", "geosite-b", "geosite-c"} {
		e.add(t, tag, `{"version":3,"rules":[{"domain":["`+tag+`.example"]}]}`)
	}
	ctx := context.Background()
	get := func(tag string) *Result {
		t.Helper()
		r, err := e.lib.Entries(ctx, tag)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	r1 := get("geosite-a")
	r2 := get("geosite-a")
	if e.callCount(t) != 1 || r1 != r2 {
		t.Fatalf("同一分类第二次应走缓存: calls=%d", e.callCount(t))
	}

	// 文件变了(大小或修改时间)就重新解码
	e.add(t, "geosite-a", `{"version":3,"rules":[{"domain":["changed.example"]}]}`)
	if err := os.WriteFile(filepath.Join(e.libDir, "geosite-a.srs"), []byte("srs:changed-and-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	r3 := get("geosite-a")
	if e.callCount(t) != 2 || r3.Entries[0].Value != "changed.example" {
		t.Fatalf("文件变了应重新解码: calls=%d %v", e.callCount(t), r3.Entries)
	}

	// 最多缓存 2 个:a、b、c 依次读,a 被挤掉
	get("geosite-b")
	get("geosite-c") // 缓存里变成 b、c
	before := e.callCount(t)
	get("geosite-b")
	get("geosite-c")
	if e.callCount(t) != before {
		t.Fatal("b、c 应仍在缓存里")
	}
	get("geosite-a")
	if e.callCount(t) != before+1 {
		t.Fatal("a 已被挤出缓存,应重新解码")
	}
}

func TestCachePutSkipsHuge(t *testing.T) {
	l := New("d", "s", "")
	l.cachePut("geosite-huge", stamp{1, 1}, &Result{Entries: make([]rulelist.Entry, cacheMaxEntries+1)})
	if l.cacheGet("geosite-huge", stamp{1, 1}) != nil {
		t.Fatal("过大的结果不该常驻缓存")
	}
	// 两个中等的合起来超总量:挤掉旧的
	half := cacheMaxEntries/2 + 1
	l.cachePut("geosite-1", stamp{1, 1}, &Result{Entries: make([]rulelist.Entry, half)})
	l.cachePut("geosite-2", stamp{1, 1}, &Result{Entries: make([]rulelist.Entry, half)})
	if l.cacheGet("geosite-1", stamp{1, 1}) != nil || l.cacheGet("geosite-2", stamp{1, 1}) == nil {
		t.Fatal("总量超限时应挤掉最旧的")
	}
}

func TestEntriesWaitsAndCancels(t *testing.T) {
	e := newFakeEnv(t)
	e.add(t, "geosite-a", `{"version":3,"rules":[{"domain":["a.example"]}]}`)
	e.lib.sem <- struct{}{} // 假装有别的解码在占着
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := e.lib.Entries(ctx, "geosite-a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("排队时超时应返回 ctx 错误: %v", err)
	}
	<-e.lib.sem
	if e.callCount(t) != 0 {
		t.Fatal("没排上队就不该起 sing-box")
	}
	// 已缓存的分类不必排队
	if _, err := e.lib.Entries(context.Background(), "geosite-a"); err != nil {
		t.Fatal(err)
	}
	e.lib.sem <- struct{}{}
	if _, err := e.lib.Entries(ctx, "geosite-a"); err != nil {
		t.Fatalf("命中缓存时不该受排队影响: %v", err)
	}
	<-e.lib.sem
}

func TestHas(t *testing.T) {
	e := newFakeEnv(t)
	e.add(t, "geosite-a", `{}`)
	if !e.lib.Has("geosite-a") || e.lib.Has("geosite-b") || e.lib.Has("../x") || e.lib.Has("geosite-../x") {
		t.Fatal("Has 判断不对")
	}
}

func TestConvertTruncates(t *testing.T) {
	doms := make(listable, MaxEntries+5)
	for i := range doms {
		doms[i] = fmt.Sprintf("d%d.example", i)
	}
	res := convert(&source{Rules: []rule{{Domain: doms}, {DomainSuffix: listable{"later.example"}}}})
	if len(res.Entries) != MaxEntries || !res.Truncated {
		t.Fatalf("len=%d truncated=%v", len(res.Entries), res.Truncated)
	}
}

func TestListableForms(t *testing.T) {
	var s source
	if err := jsonUnmarshalStr(`{"rules":[{"domain":"one"},{"domain":["a","b"]},{"domain":null},{"domain":[]}]}`, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Rules[0].Domain) != 1 || len(s.Rules[1].Domain) != 2 || len(s.Rules[2].Domain) != 0 || len(s.Rules[3].Domain) != 0 {
		t.Fatalf("%+v", s.Rules)
	}
	if err := jsonUnmarshalStr(`{"rules":[{"domain":5}]}`, &s); err == nil {
		t.Fatal("数字不是合法写法")
	}
}

// 真实 sing-box:先把源码规则集编成 .srs 放进规则库,再经 Library 解回来。
// 内核编译时会重排 / 合并同类项,所以按集合比较,不比顺序。
func TestRealSingBoxRoundTrip(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	root := t.TempDir()
	lib := filepath.Join(root, "geodata")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	compile := func(tag, src string) {
		t.Helper()
		in := filepath.Join(root, tag+".json")
		if err := os.WriteFile(in, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, "rule-set", "compile", "--output", filepath.Join(lib, tag+".srs"), in).CombinedOutput(); err != nil {
			t.Fatalf("compile %s: %v\n%s", tag, err, out)
		}
	}
	compile("geosite-it", `{"version":3,"rules":[{"domain":["a.example.com","b.example.com"],"domain_suffix":[".example.org"],"domain_keyword":["kw"],"domain_regex":["^r[0-9]+\\.example$"]}]}`)
	compile("geoip-it", `{"version":3,"rules":[{"ip_cidr":["10.0.0.0/8","192.0.2.0/24","2001:db8::/32"]}]}`)

	l := New(lib, bin, filepath.Join(root, "tmp-does-not-exist-yet"))
	if err := os.MkdirAll(l.TmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	flat := func(r *Result) []string {
		out := make([]string, 0, len(r.Entries))
		for _, e := range r.Entries {
			out = append(out, e.Type+"|"+e.Value)
		}
		sort.Strings(out)
		return out
	}
	site, err := l.Entries(context.Background(), "geosite-it")
	if err != nil {
		t.Fatal(err)
	}
	wantSite := []string{
		"domain|a.example.com", "domain|b.example.com",
		"domain_keyword|kw", `domain_regex|^r[0-9]+\.example$`,
	}
	got := flat(site)
	for _, w := range wantSite {
		if !contains(got, w) {
			t.Errorf("geosite-it 缺 %q,实际 %v", w, got)
		}
	}
	// .example.org 编译后写法由内核决定(domain_suffix 的 .example.org 或 example.org),只要求出现在后缀类里
	hasSuffix := false
	for _, e := range site.Entries {
		if e.Type == "domain_suffix" && strings.HasSuffix(e.Value, "example.org") {
			hasSuffix = true
		}
	}
	if !hasSuffix {
		t.Errorf("geosite-it 缺 example.org 后缀,实际 %v", got)
	}

	ip, err := l.Entries(context.Background(), "geoip-it")
	if err != nil {
		t.Fatal(err)
	}
	gotIP := flat(ip)
	for _, w := range []string{"ip_cidr|10.0.0.0/8", "ip_cidr|192.0.2.0/24", "ip_cidr|2001:db8::/32"} {
		if !contains(gotIP, w) {
			t.Errorf("geoip-it 缺 %q,实际 %v", w, gotIP)
		}
	}
	if _, err := l.Entries(context.Background(), "geosite-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}

	// 一个损坏的 .srs:sing-box 解不了,要如实报错而不是返回空
	if err := os.WriteFile(filepath.Join(lib, "geosite-corrupt.srs"), []byte("not a rule set"), 0o644); err != nil {
		t.Fatal(err)
	}
	var de *DecodeError
	if _, err := l.Entries(context.Background(), "geosite-corrupt"); !errors.As(err, &de) {
		t.Fatalf("损坏的 .srs 应是 *DecodeError: %v", err)
	}
	if left, _ := os.ReadDir(l.TmpDir); len(left) != 0 {
		t.Fatalf("残留临时文件: %v", left)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func jsonUnmarshalStr(s string, v any) error { return json.Unmarshal([]byte(s), v) }
