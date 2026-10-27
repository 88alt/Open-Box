package singbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
)

type fakeReloader struct {
	res   map[string]*rulelist.Result
	errs  map[string]error
	calls int
}

func (f *fakeReloader) Reload(_ context.Context, u string) (*rulelist.Result, error) {
	f.calls++
	if err := f.errs[u]; err != nil {
		return nil, err
	}
	if r := f.res[u]; r != nil {
		return r, nil
	}
	return nil, errors.New("没有这个链接")
}

type fakeURLs struct {
	urls []string
	err  error
}

func (f fakeURLs) RuleListURLs(context.Context) ([]string, error) { return f.urls, f.err }

func newRefresher(bin, dir string, fr *fakeReloader, urls ...string) *Refresher {
	return &Refresher{Lists: ListFiles{SingBox: bin, Dir: dir}, Fetch: fr, URLs: fakeURLs{urls: urls}}
}

func TestRefreshWithRealSingBox(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	dir := filepath.Join(t.TempDir(), "rules")
	const mine, other, unsaved = "https://e.com/mine.list", "https://e.com/other.list", "https://e.com/unsaved.list"
	fr := &fakeReloader{res: map[string]*rulelist.Result{
		mine:    {Entries: []rulelist.Entry{ent("domain_suffix", "baidu.com"), ent("domain_keyword", "tieba"), ent("ip_cidr", "1.0.1.0/24")}},
		other:   {Entries: []rulelist.Entry{ent("domain", "o.example.com")}},
		unsaved: {Entries: []rulelist.Entry{ent("domain", "u.example.com")}},
	}}
	ctx := context.Background()
	tg := policyentries.ListTagForURL

	// 先用部署同款的 Sync 把 other 编好,后面确认刷新 mine 不会碰它(也不会被「清理」)
	if rep, err := (ListFiles{Rules: &fakeLoader{res: map[string]*rulelist.Result{other: fr.res[other]}}, SingBox: bin, Dir: dir}).Sync(ctx, []string{other}); err != nil || len(rep.Issues) != 0 {
		t.Fatalf("准备: %+v %v", rep, err)
	}
	otherFile := filepath.Join(dir, tg(other)+".srs")
	otherBefore, _ := os.ReadFile(otherFile)

	r := newRefresher(bin, dir, fr, mine, other) // mine 在档案里被引用;unsaved 没有

	// 第一次:域名 + IP 两份都是新的;档案在用 → 构成变了,要重启
	res, err := r.Refresh(ctx, " "+mine+" ")
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := map[string]int{"domain_suffix": 1, "domain_keyword": 1, "ip_cidr": 1}
	if res.URL != mine || res.Tag != tg(mine) || res.Total != 3 || !reflect.DeepEqual(res.Counts, wantCounts) ||
		!res.NeedsRestart || !res.Changed || !res.Referenced || res.Dropped != 0 {
		t.Fatalf("第一次: %+v", res)
	}
	if !strings.Contains(decompile(t, bin, filepath.Join(dir, tg(mine)+".srs")), "baidu.com") ||
		!strings.Contains(decompile(t, bin, filepath.Join(dir, tg(mine)+"-ip.srs")), "1.0.1.0/24") {
		t.Fatal("两份内容不对")
	}
	if now, _ := os.ReadFile(otherFile); string(now) != string(otherBefore) {
		t.Fatal("别的链接的文件不该被动")
	}

	// 第二次:名单没变 → 什么都不改、不用重启
	res, err = r.Refresh(ctx, mine)
	if err != nil || res.Changed || res.NeedsRestart || fr.calls != 2 {
		t.Fatalf("第二次: %+v %v calls=%d", res, err, fr.calls)
	}

	// 条目变了、构成没变:改写文件,但不用重启
	fr.res[mine] = &rulelist.Result{Entries: []rulelist.Entry{ent("domain_suffix", "baidu.com"), ent("domain_suffix", "qq.com"), ent("ip_cidr", "1.0.1.0/24")}}
	res, err = r.Refresh(ctx, mine)
	if err != nil || !res.Changed || res.NeedsRestart || res.Total != 3 || res.Counts["domain_suffix"] != 2 {
		t.Fatalf("条目变了: %+v %v", res, err)
	}
	if !strings.Contains(decompile(t, bin, filepath.Join(dir, tg(mine)+".srs")), "qq.com") {
		t.Fatal("域名那份没更新")
	}

	// 构成变了(IP 那份没了):删旧文件,要重启
	fr.res[mine] = &rulelist.Result{Entries: []rulelist.Entry{ent("domain_suffix", "baidu.com")}}
	res, err = r.Refresh(ctx, mine)
	if err != nil || !res.NeedsRestart || !res.Changed || hasSRS(filepath.Join(dir, tg(mine)+"-ip.srs")) {
		t.Fatalf("构成变了: %+v %v", res, err)
	}

	// 没被档案引用的链接:照样编好,但不提示重启
	res, err = r.Refresh(ctx, unsaved)
	if err != nil || res.Referenced || res.NeedsRestart || !res.Changed || !hasSRS(filepath.Join(dir, tg(unsaved)+".srs")) {
		t.Fatalf("未引用: %+v %v", res, err)
	}
	if now, _ := os.ReadFile(otherFile); string(now) != string(otherBefore) {
		t.Fatal("别的链接的文件不该被动")
	}

	// 拉取失败:报错,旧文件原样保留
	domBefore, _ := os.ReadFile(filepath.Join(dir, tg(mine)+".srs"))
	fr.errs = map[string]error{mine: &rulelist.FetchError{Msg: "连接超时"}}
	if _, err := r.Refresh(ctx, mine); err == nil {
		t.Fatal("拉取失败应报错")
	} else if fe := new(rulelist.FetchError); !errors.As(err, &fe) {
		t.Fatalf("应原样交回拉取错误: %v", err)
	}
	if now, _ := os.ReadFile(filepath.Join(dir, tg(mine)+".srs")); string(now) != string(domBefore) {
		t.Fatal("失败时旧文件要原样保留")
	}

	// 目录里没有临时文件残留
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("临时文件残留: %s", e.Name())
		}
	}
}

func TestRefreshInputErrors(t *testing.T) {
	fr := &fakeReloader{}
	r := newRefresher("", t.TempDir(), fr)
	for _, u := range []string{"", "   ", "ftp://e.com/a", "e.com/a.list", "file:///etc/passwd"} {
		_, err := r.Refresh(context.Background(), u)
		var ie *RefreshInputError
		if !errors.As(err, &ie) {
			t.Errorf("%q: 应是 RefreshInputError,实际 %v", u, err)
		}
	}
	if fr.calls != 0 {
		t.Fatalf("链接不合格不该联网: %d", fr.calls)
	}
	// 没有拉取服务
	_, err := (&Refresher{Lists: ListFiles{Dir: t.TempDir()}}).Refresh(context.Background(), "https://e.com/a.list")
	var be *RefreshBuildError
	if !errors.As(err, &be) {
		t.Fatalf("没有拉取服务应报 RefreshBuildError: %v", err)
	}
}

func TestRefreshNoSingBox(t *testing.T) {
	const u = "https://e.com/a.list"
	dir := t.TempDir()
	fr := &fakeReloader{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{ent("domain", "a.com")}}}}
	_, err := newRefresher(filepath.Join(t.TempDir(), "没有这个sing-box"), dir, fr, u).Refresh(context.Background(), u)
	if !errors.Is(err, ErrNoSingBox) {
		t.Fatalf("应是 ErrNoSingBox: %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("不该留文件: %v", ents)
	}
}

// 编译失败:报 RefreshBuildError(带 sing-box 的原因),已有的文件不动,不沿用旧文件冒充成功。
func TestRefreshCompileFailureKeepsOldFiles(t *testing.T) {
	const u = "https://e.com/a.list"
	dir := t.TempDir()
	tag := policyentries.ListTagForURL(u)
	old := fakeSRS("old")
	if err := os.WriteFile(filepath.Join(dir, tag+".srs"), old, 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "sing-box")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho boom >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fr := &fakeReloader{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{ent("domain", "a.com")}}}}
	_, err := newRefresher(fake, dir, fr, u).Refresh(context.Background(), u)
	var be *RefreshBuildError
	if !errors.As(err, &be) || !strings.Contains(be.Msg, "boom") || strings.Contains(be.Msg, "沿用") || !strings.Contains(be.Msg, "没有改动") {
		t.Fatalf("err=%v", err)
	}
	if now, _ := os.ReadFile(filepath.Join(dir, tag+".srs")); string(now) != string(old) {
		t.Fatal("旧文件被动了")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("不该留临时文件: %v", ents)
	}
}

// 名单里的正则全是非法的:没有可编的内容,报错而不是假装成功。
func TestRefreshOnlyInvalidRegex(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	const u = "https://e.com/a.list"
	fr := &fakeReloader{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{ent("domain_regex", "([bad")}}}}
	_, err = newRefresher(bin, t.TempDir(), fr, u).Refresh(context.Background(), u)
	var be *RefreshBuildError
	if !errors.As(err, &be) || !strings.Contains(be.Msg, "没有可用的条目") {
		t.Fatalf("err=%v", err)
	}
}

func TestRefreshCanceledAndURLSourceError(t *testing.T) {
	bin, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("没有 sing-box,跳过")
	}
	const u = "https://e.com/a.list"
	fr := &fakeReloader{res: map[string]*rulelist.Result{u: {Entries: []rulelist.Entry{ent("domain", "a.com")}}}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fr.errs = map[string]error{u: ctx.Err()}
	if _, err := newRefresher(bin, t.TempDir(), fr, u).Refresh(ctx, u); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应交回取消错误: %v", err)
	}

	fr.errs = nil
	r := newRefresher(bin, t.TempDir(), fr)
	r.URLs = fakeURLs{err: errors.New("档案坏了")}
	if _, err := r.Refresh(context.Background(), u); err == nil || !strings.Contains(err.Error(), "读取档案失败") {
		t.Fatalf("档案读不到应报错: %v", err)
	}

	// 不接档案来源:一律当作没被引用
	r = newRefresher(bin, t.TempDir(), fr)
	r.URLs = nil
	res, err := r.Refresh(context.Background(), u)
	if err != nil || res.Referenced || res.NeedsRestart {
		t.Fatalf("%+v %v", res, err)
	}
}
