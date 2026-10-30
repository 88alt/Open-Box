package singbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

type fakeLoader struct {
	res   map[string]*rulelist.Result
	errs  map[string]error
	calls int
}

func (f *fakeLoader) Load(_ context.Context, u string) (*rulelist.Result, error) {
	f.calls++
	if err := f.errs[u]; err != nil {
		return nil, err
	}
	if r := f.res[u]; r != nil {
		return r, nil
	}
	return nil, errors.New("没有这个链接")
}

func ent(t, v string) rulelist.Entry { return rulelist.Entry{Type: t, Value: v} }

func TestCollectRuleURLs(t *testing.T) {
	routing := map[string]any{
		"policies": []any{
			map[string]any{"name": "a", "ruleUrls": []any{"https://e.com/a.list", " HTTP://e.com/b.list ", "ftp://x/y", "", 5}},
			map[string]any{"name": "b", "ruleUrls": []any{"https://e.com/a.list", "https://e.com/c.list"}}, // 重复的只留一次
		},
		"custom": map[string]any{"rules": []any{
			map[string]any{"type": "ruleUrl", "value": "https://e.com/d.list", "outbound": "direct"},
			map[string]any{"type": "ruleUrl", "value": "not-a-url"},
			map[string]any{"type": "domain", "value": "https://e.com/ignored"},
			map[string]any{"type": "ruleUrl", "value": "https://e.com/c.list"}, // 与站点集里重复
		}},
	}
	got := CollectRuleURLs(routing)
	want := []string{"https://e.com/a.list", "HTTP://e.com/b.list", "https://e.com/c.list", "https://e.com/d.list"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if len(CollectRuleURLs(nil)) != 0 || len(CollectRuleURLs(map[string]any{})) != 0 {
		t.Fatal("没有 routing 应返回空")
	}
}

func TestSplitEntries(t *testing.T) {
	dom, ip, dropped := splitEntries([]rulelist.Entry{ent("domain_suffix", "a.com"), ent("ip_cidr", "1.0.0.0/8"), ent("domain", "x.a.com"), ent("domain_suffix", "b.com"), ent("domain_regex", "^ok.*"), ent("domain_regex", "([bad")})
	if dropped != 1 || !reflect.DeepEqual(dom["domain_regex"], []string{"^ok.*"}) {
		t.Fatalf("dropped=%d regex=%v", dropped, dom["domain_regex"])
	}
	if !reflect.DeepEqual(dom["domain_suffix"], []string{"a.com", "b.com"}) || !reflect.DeepEqual(dom["domain"], []string{"x.a.com"}) {
		t.Fatalf("dom=%v", dom)
	}
	if !reflect.DeepEqual(ip["ip_cidr"], []string{"1.0.0.0/8"}) {
		t.Fatalf("ip=%v", ip)
	}
	d2, i2, _ := splitEntries([]rulelist.Entry{ent("ip_cidr", "::1/128")})
	if d2 != nil || i2 == nil {
		t.Fatalf("d2=%v i2=%v", d2, i2)
	}
	d3, i3, _ := splitEntries(nil)
	if d3 != nil || i3 != nil {
		t.Fatal("空名单两份都应为 nil")
	}
}

func decompile(t *testing.T, bin, srs string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "o.json")
	if b, err := exec.Command(bin, "rule-set", "decompile", "--output", out, srs).CombinedOutput(); err != nil {
		t.Fatalf("decompile %s: %v %s", srs, err, b)
	}
	b, _ := os.ReadFile(out)
	return string(b)
}

func TestSyncWithRealSingBox(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	dir := filepath.Join(t.TempDir(), "rules")
	const both, onlyDom, onlyIP, empty = "https://e.com/both.list", "https://e.com/dom.list", "https://e.com/ip.list", "https://e.com/empty.list"
	ld := &fakeLoader{res: map[string]*rulelist.Result{
		both:    {Entries: []rulelist.Entry{ent("domain_suffix", "baidu.com"), ent("domain_keyword", "tieba"), ent("ip_cidr", "1.0.1.0/24")}},
		onlyDom: {Entries: []rulelist.Entry{ent("domain", "a.example.com")}},
		onlyIP:  {Entries: []rulelist.Entry{ent("ip_cidr", "10.0.0.0/8"), ent("ip_cidr", "2001:db8::/32")}},
		empty:   {Entries: nil},
	}}
	l := ListFiles{Rules: ld, SingBox: bin, Dir: dir}
	ctx := context.Background()
	rep, err := l.Sync(ctx, []string{both, onlyDom, onlyIP, empty})
	if err != nil || len(rep.Issues) != 0 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	tg := policyentries.ListTagForURL
	wantShapes := map[string]ListShape{
		tg(both): {true, true}, tg(onlyDom): {true, false}, tg(onlyIP): {false, true}, tg(empty): {false, false},
	}
	if !reflect.DeepEqual(rep.Shapes, wantShapes) {
		t.Fatalf("shapes=%+v", rep.Shapes)
	}
	if len(rep.Written) != 4 { // both 两份 + dom 一份 + ip 一份
		t.Fatalf("written=%v", rep.Written)
	}
	// 文件存在与否和形状一致;域名那份和 IP 那份内容分开
	for tag, sh := range wantShapes {
		if hasSRS(filepath.Join(dir, tag+".srs")) != sh.Domain || hasSRS(filepath.Join(dir, tag+"-ip.srs")) != sh.IP {
			t.Errorf("%s 文件与形状 %+v 不符", tag, sh)
		}
	}
	d := decompile(t, bin, filepath.Join(dir, tg(both)+".srs"))
	if !strings.Contains(d, "baidu.com") || !strings.Contains(d, "tieba") || strings.Contains(d, "1.0.1.0") {
		t.Fatalf("域名那份: %s", d)
	}
	i := decompile(t, bin, filepath.Join(dir, tg(both)+"-ip.srs"))
	if !strings.Contains(i, "1.0.1.0/24") || strings.Contains(i, "baidu") {
		t.Fatalf("IP 那份: %s", i)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 4 {
		t.Fatalf("目录里应正好 4 个文件(无临时文件残留): %v", ents)
	}

	// 第二次:内容没变,不改写
	rep, _ = l.Sync(ctx, []string{both, onlyDom, onlyIP, empty})
	if len(rep.Written) != 0 || len(rep.Kept) != 4 {
		t.Fatalf("第二次 written=%v kept=%v", rep.Written, rep.Kept)
	}

	// 名单变了:ip 那份没了 → 删旧文件;域名那份改写
	ld.res[both] = &rulelist.Result{Entries: []rulelist.Entry{ent("domain_suffix", "baidu.com"), ent("domain_suffix", "qq.com")}}
	rep, _ = l.Sync(ctx, []string{both, onlyDom, onlyIP, empty})
	if rep.Shapes[tg(both)] != (ListShape{true, false}) || hasSRS(filepath.Join(dir, tg(both)+"-ip.srs")) {
		t.Fatalf("shape=%+v", rep.Shapes[tg(both)])
	}
	if !strings.Contains(decompile(t, bin, filepath.Join(dir, tg(both)+".srs")), "qq.com") {
		t.Fatal("域名那份没有更新")
	}

	// 链接不再被引用:对应文件被清掉;不认识的文件不碰
	os.WriteFile(filepath.Join(dir, "geosite-cn.srs"), fakeSRS("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "list-zzzz.srs"), []byte("x"), 0o600) // 不是 8 位十六进制,不碰
	rep, _ = l.Sync(ctx, []string{onlyDom})
	if len(rep.Pruned) == 0 || hasSRS(filepath.Join(dir, tg(both)+".srs")) || hasSRS(filepath.Join(dir, tg(onlyIP)+"-ip.srs")) {
		t.Fatalf("pruned=%v", rep.Pruned)
	}
	for _, keep := range []string{"geosite-cn.srs", "list-zzzz.srs", tg(onlyDom) + ".srs"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("%s 不该被删: %v", keep, err)
		}
	}
}

func TestSyncFailureFallsBackToOldFiles(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	dir := t.TempDir()
	const u = "https://e.com/x.list"
	ld := &fakeLoader{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{ent("domain", "a.com"), ent("ip_cidr", "9.9.9.0/24")}}}}
	l := ListFiles{Rules: ld, SingBox: bin, Dir: dir}
	ctx := context.Background()
	tag := policyentries.ListTagForURL(u)
	if rep, _ := l.Sync(ctx, []string{u}); len(rep.Issues) != 0 {
		t.Fatalf("首次: %+v", rep)
	}
	// 之后拉取失败:沿用旧文件,形状按磁盘上有的两份,问题标 UsedOld
	ld.errs = map[string]error{u: errors.New("连接超时")}
	rep, err := l.Sync(ctx, []string{u})
	if err != nil || len(rep.Issues) != 1 || !rep.Issues[0].UsedOld || !strings.Contains(rep.Issues[0].Reason, "连接超时") {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	if rep.Shapes[tag] != (ListShape{true, true}) || len(rep.Pruned) != 0 {
		t.Fatalf("shapes=%+v pruned=%v", rep.Shapes, rep.Pruned)
	}
	// 从来没成功过的链接:没有形状,问题不是 UsedOld
	rep, _ = l.Sync(ctx, []string{u, "https://e.com/never.list"})
	var never *ListIssue
	for i := range rep.Issues {
		if rep.Issues[i].Tag == policyentries.ListTagForURL("https://e.com/never.list") {
			never = &rep.Issues[i]
		}
	}
	if never == nil || never.UsedOld {
		t.Fatalf("issues=%+v", rep.Issues)
	}
	if _, has := rep.Shapes[never.Tag]; has {
		t.Fatal("没成功过的不该有形状")
	}
}

func TestSyncNoSingBoxAndNilLoaderAndCancel(t *testing.T) {
	dir := t.TempDir()
	const u = "https://e.com/x.list"
	ld := &fakeLoader{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{ent("domain", "a.com")}}}}
	rep, err := ListFiles{Rules: ld, SingBox: "no-such-sing-box", Dir: dir}.Sync(context.Background(), []string{u})
	if err != nil || len(rep.Issues) != 1 || !strings.Contains(rep.Issues[0].Reason, ErrNoSingBox.Error()) {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	rep, _ = ListFiles{Dir: dir}.Sync(context.Background(), []string{u})
	if len(rep.Issues) != 1 || rep.Issues[0].UsedOld {
		t.Fatalf("rep=%+v", rep)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (ListFiles{Rules: ld, Dir: dir}).Sync(ctx, []string{u}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if ld.calls != 1 { // 取消后不再拉取
		t.Fatalf("calls=%d", ld.calls)
	}
}

// 非法正则:丢掉并计数,不让它拖垮整份规则集;只剩非法正则的链接,形状全假。
func TestSyncDropsInvalidRegex(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	dir := t.TempDir()
	const mixed, onlyBad = "https://e.com/mixed.list", "https://e.com/bad.list"
	ld := &fakeLoader{res: map[string]*rulelist.Result{
		mixed:   {Entries: []rulelist.Entry{ent("domain_regex", "([unclosed"), ent("domain_regex", `^ad\d+\.example\.com$`)}},
		onlyBad: {Entries: []rulelist.Entry{ent("domain_regex", "([unclosed")}},
	}}
	rep, _ := ListFiles{Rules: ld, SingBox: bin, Dir: dir}.Sync(context.Background(), []string{mixed, onlyBad})
	tg := policyentries.ListTagForURL
	if len(rep.Issues) != 0 || rep.Dropped[tg(mixed)] != 1 || rep.Dropped[tg(onlyBad)] != 1 {
		t.Fatalf("rep=%+v", rep)
	}
	if rep.Shapes[tg(mixed)] != (ListShape{true, false}) || rep.Shapes[tg(onlyBad)] != (ListShape{false, false}) {
		t.Fatalf("shapes=%+v", rep.Shapes)
	}
	if !strings.Contains(decompile(t, bin, filepath.Join(dir, tg(mixed)+".srs")), "ad") {
		t.Fatal("好的正则应保留")
	}
}

// 编译失败(这里用一个必失败的假 sing-box):如实记成问题,不影响别的处理,不留临时文件。
func TestSyncCompileFailureReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 sing-box 是 /bin/sh 脚本,Windows 上跑不了")
	}
	dir := t.TempDir()
	fake := filepath.Join(t.TempDir(), "sing-box")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho boom >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const u = "https://e.com/x.list"
	ld := &fakeLoader{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{ent("domain", "a.com")}}}}
	rep, err := ListFiles{Rules: ld, SingBox: fake, Dir: dir}.Sync(context.Background(), []string{u})
	if err != nil || len(rep.Issues) != 1 || rep.Issues[0].UsedOld || !strings.Contains(rep.Issues[0].Reason, "编译规则集失败") || !strings.Contains(rep.Issues[0].Reason, "boom") {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	if _, has := rep.Shapes[policyentries.ListTagForURL(u)]; has {
		t.Fatal("失败且没有旧文件,不该有形状")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("不该留文件: %v", ents)
	}
}

// 真实 core:档案里有规则集链接 → ListedBuilder 编 .srs 并把形状交给 core → 配置引用域名 / IP 两份 → 落盘后一个都不缺。
func TestListedBuilderWithRealCore(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	e := newEnv(t, System{})
	e.addSub(t, "A", twoNodes)
	const u = "https://e.com/mix.list"
	if _, err := e.prof.Patch(context.Background(), map[string]any{"routing": map[string]any{
		"policies": []any{map[string]any{"name": "测试", "ruleUrls": []any{u}, "outbound": "direct"}},
	}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rules := filepath.Join(root, "rules")
	ld := &fakeLoader{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{ent("domain_suffix", "baidu.com"), ent("ip_cidr", "1.0.1.0/24")}}}}
	b := ListedBuilder{Gen: e.gen, Lists: ListFiles{Rules: ld, SingBox: bin, Dir: rules}}
	built, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(built.ListIssues) != 0 {
		t.Fatalf("issues=%v", built.ListIssues)
	}
	tag := policyentries.ListTagForURL(u)
	raw, _ := built.JSON()
	if !strings.Contains(string(raw), `"`+tag+`"`) || !strings.Contains(string(raw), `"`+tag+`-ip"`) {
		t.Fatalf("配置里应同时引用 %s 和 %s-ip", tag, tag)
	}
	// 配置里的路径换到临时目录后,WriteLocalSets 对这两份都应认账(已由 Sync 落盘),不报缺
	moved := strings.ReplaceAll(string(raw), `"/data/`, `"`+root+`/`)
	built.rawConfigBytes = []byte(moved)
	cfgMap := map[string]any{}
	if err := jsonUnmarshal([]byte(moved), &cfgMap); err != nil {
		t.Fatal(err)
	}
	rep, err := Files{SingBox: bin}.WriteLocalSets(context.Background(), cfgMap, built.DirectHosts)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rep.Missing {
		if strings.HasPrefix(m.Tag, "list-") {
			t.Fatalf("list-* 不该缺: %+v", m)
		}
	}
}

// 没处理好的链接:ListedBuilder 把原因记进 Built.ListIssues,配置里引用的 list-* 在 WriteLocalSets 里报缺。
func TestListedBuilderReportsIssue(t *testing.T) {
	e := newEnv(t, System{})
	e.addSub(t, "A", twoNodes)
	const u = "https://e.com/down.list"
	if _, err := e.prof.Patch(context.Background(), map[string]any{"routing": map[string]any{
		"policies": []any{map[string]any{"name": "测试", "ruleUrls": []any{u}, "outbound": "direct"}},
	}}); err != nil {
		t.Fatal(err)
	}
	ld := &fakeLoader{errs: map[string]error{u: errors.New("连接超时")}}
	b := ListedBuilder{Gen: e.gen, Lists: ListFiles{Rules: ld, Dir: t.TempDir()}}
	built, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tag := policyentries.ListTagForURL(u)
	if !strings.Contains(built.ListIssues[tag], "连接超时") {
		t.Fatalf("issues=%v", built.ListIssues)
	}
}
