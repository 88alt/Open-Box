package dnsdecide

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFileSetsSourceAndInline(t *testing.T) {
	dir := t.TempDir()
	src := write(t, dir, "a.json", `{"version":3,"rules":[
	  {"domain":["exact.test"],"domain_suffix":["suffix.test"],"domain_keyword":["kw"],"domain_regex":["^re-\\d+\\.test$"]},
	  {"domain_suffix":[".dot.test"]}
	]}`)
	f := &FileSets{Defs: map[string]SetDef{
		"src":    {Type: "local", Format: "source", Path: src},
		"inline": {Type: "inline", Rules: []any{map[string]any{"domain_suffix": []any{"in.test"}}}},
	}}
	ctx := context.Background()
	for _, c := range []struct {
		tag, d string
		want   bool
	}{
		{"src", "exact.test", true}, {"src", "x.exact.test", false}, {"src", "a.suffix.test", true}, {"src", "suffix.test", true},
		{"src", "myKW.example", false /* 关键字按小写比,d 已是小写才会传进来 */}, {"src", "mykw.example", true},
		{"src", "re-42.test", true}, {"src", "re-x.test", false}, {"src", "dot.test", false}, {"src", "a.dot.test", true},
		{"inline", "a.in.test", true}, {"inline", "out.test", false},
	} {
		got, err := f.Match(ctx, c.tag, c.d)
		if err != nil || got != c.want {
			t.Errorf("%s %s = %v, %v; want %v", c.tag, c.d, got, err, c.want)
		}
	}
}

func TestFileSetsPartialRules(t *testing.T) {
	dir := t.TempDir()
	// geoip 一类:只有 IP 条件 → 对只有域名的查询判断不了
	ip := write(t, dir, "ip.json", `{"version":3,"rules":[{"ip_cidr":["1.0.0.0/8"]}]}`)
	// 域名和 IP 条件同时出现(「且」):域名命中也不能说整条命中
	mixed := write(t, dir, "mixed.json", `{"version":3,"rules":[{"domain":["a.test"],"ip_cidr":["1.0.0.0/8"]},{"domain":["b.test"]}]}`)
	logical := write(t, dir, "logical.json", `{"version":3,"rules":[{"type":"logical","mode":"and","rules":[{"domain":["a.test"]}]}]}`)
	f := &FileSets{Defs: map[string]SetDef{
		"ip": {Type: "local", Format: "source", Path: ip}, "mixed": {Type: "local", Format: "source", Path: mixed},
		"logical": {Type: "local", Format: "source", Path: logical},
	}}
	ctx := context.Background()
	if _, err := f.Match(ctx, "ip", "a.test"); !errors.Is(err, ErrUnknownSet) {
		t.Errorf("只有 IP 条件应该判断不了: %v", err)
	}
	if ok, err := f.Match(ctx, "mixed", "b.test"); err != nil || !ok { // 纯域名那条命中就是命中
		t.Errorf("纯域名规则命中应该直接算命中: %v %v", ok, err)
	}
	if _, err := f.Match(ctx, "mixed", "a.test"); !errors.Is(err, ErrUnknownSet) {
		t.Errorf("带 IP 条件的规则里的域名不能当命中: %v", err)
	}
	if _, err := f.Match(ctx, "logical", "a.test"); !errors.Is(err, ErrUnknownSet) {
		t.Errorf("逻辑规则判断不了: %v", err)
	}
}

func TestFileSetsFailureModes(t *testing.T) {
	dir := t.TempDir()
	bad := write(t, dir, "bad.json", "{坏了")
	f := &FileSets{Defs: map[string]SetDef{
		"missing": {Type: "local", Format: "source", Path: filepath.Join(dir, "nope.json")},
		"bad":     {Type: "local", Format: "source", Path: bad},
		"remote":  {Type: "remote", Path: "https://x"},
		"nopath":  {Type: "local"},
		"dir":     {Type: "local", Format: "source", Path: dir},
	}}
	for _, tag := range []string{"missing", "bad", "remote", "nopath", "dir", "not-in-config"} {
		_, err := f.Match(context.Background(), tag, "a.test")
		if !errors.Is(err, ErrUnknownSet) {
			t.Errorf("%s 应该是 ErrUnknownSet: %v", tag, err)
		}
	}
}

func TestFileSetsCacheFollowsFileChanges(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "s.json", `{"rules":[{"domain":["old.test"]}]}`)
	f := &FileSets{Defs: map[string]SetDef{"s": {Type: "local", Format: "source", Path: p}}}
	ctx := context.Background()
	if ok, _ := f.Match(ctx, "s", "old.test"); !ok {
		t.Fatal("old 应该命中")
	}
	// 改写文件(大小和时间都变)后下一次要读到新内容
	write(t, dir, "s.json", `{"rules":[{"domain":["new-domain.test"]}]}`)
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(p, later, later)
	if ok, _ := f.Match(ctx, "s", "old.test"); ok {
		t.Error("文件变了,旧内容不该还在")
	}
	if ok, _ := f.Match(ctx, "s", "new-domain.test"); !ok {
		t.Error("应该读到新内容")
	}
	if len(f.cache) != 1 {
		t.Errorf("同一个文件只留一份缓存: %d", len(f.cache))
	}
}

func TestFileSetsCacheLimit(t *testing.T) {
	dir := t.TempDir()
	defs := map[string]SetDef{}
	for i := 0; i < cacheSize+3; i++ {
		name := string(rune('a'+i)) + ".json"
		defs[name] = SetDef{Type: "local", Format: "source", Path: write(t, dir, name, `{"rules":[]}`)}
	}
	f := &FileSets{Defs: defs}
	for tag := range defs {
		_, _ = f.Match(context.Background(), tag, "x.test")
	}
	if len(f.cache) != cacheSize {
		t.Errorf("缓存应该不超过 %d 份: %d", cacheSize, len(f.cache))
	}
}

// 用假的 sing-box(一个 shell 脚本)检验解码二进制规则集时的调用方式:rule-set decompile --output <文件> <规则集>。
func TestFileSetsBinaryViaFakeSingBox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 shell 脚本当假 sing-box,Windows 上跳过")
	}
	dir := t.TempDir()
	srs := write(t, dir, "geosite-x.srs", "SRS-binary-placeholder")
	script := write(t, dir, "fake-sing-box", `#!/bin/sh
[ "$1" = rule-set ] && [ "$2" = decompile ] && [ "$3" = --output ] || { echo "bad args: $*" >&2; exit 2; }
grep -q SRS-binary-placeholder "$5" || { echo "bad input" >&2; exit 3; }
printf '{"version":3,"rules":[{"domain_suffix":["from-binary.test"]}]}' > "$4"
`)
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &FileSets{SingBox: script, TmpDir: dir, Defs: map[string]SetDef{"x": {Type: "local", Format: "binary", Path: srs}}}
	if ok, err := f.Match(context.Background(), "x", "a.from-binary.test"); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	// 解码用的临时目录用完要清掉
	left, _ := filepath.Glob(filepath.Join(dir, "dnsdecide-*"))
	if len(left) != 0 {
		t.Errorf("临时目录没清: %v", left)
	}
	// sing-box 解码失败:判断不了,错误信息里不带路径
	f2 := &FileSets{SingBox: write(t, dir, "failing", "#!/bin/sh\necho boom >&2\nexit 1\n"), Defs: f.Defs}
	_ = os.Chmod(f2.SingBox, 0o700)
	_, err := f2.Match(context.Background(), "x", "a.test")
	if !errors.Is(err, ErrUnknownSet) || strings.Contains(err.Error(), dir) {
		t.Errorf("解码失败应该是 ErrUnknownSet 且不带路径: %v", err)
	}
	// 没有 sing-box
	f3 := &FileSets{SingBox: filepath.Join(dir, "does-not-exist"), Defs: f.Defs}
	if _, err := f3.Match(context.Background(), "x", "a.test"); !errors.Is(err, ErrUnknownSet) {
		t.Errorf("没有 sing-box: %v", err)
	}
}

func TestSetDefsFromConfig(t *testing.T) {
	defs := SetDefsFromConfig(map[string]any{"route": map[string]any{"rule_set": []any{
		map[string]any{"tag": "a", "type": "local", "format": "binary", "path": "/r/a.srs"},
		map[string]any{"tag": "b", "type": "inline", "rules": []any{map[string]any{"domain": []any{"x"}}}},
		map[string]any{"type": "local"}, // 没有 tag:忽略
	}}})
	if len(defs) != 2 || defs["a"].Path != "/r/a.srs" || defs["a"].Format != "binary" || len(defs["b"].Rules) != 1 {
		t.Errorf("%+v", defs)
	}
	if len(SetDefsFromConfig(map[string]any{})) != 0 {
		t.Error("没有 route 应该是空")
	}
}
