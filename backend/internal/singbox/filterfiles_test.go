package singbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsfilter"
)

// fakeCompiler 是假的 sing-box:`rule-set compile --output OUT IN` 把 IN 的内容加上 SRS 头写到 OUT,
// 同时在 LOG 里记一行(用来数编译了几次)。
func fakeCompiler(t *testing.T) (bin, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("用 shell 脚本充当 sing-box,Windows 上跳过")
	}
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "sing-box"), filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"[ \"$1\" = rule-set ] && [ \"$2\" = compile ] && [ \"$3\" = --output ] || { echo \"bad args: $*\" >&2; exit 2; }\n" +
		"echo \"$4\" >> '" + log + "'\n" +
		"printf 'SRS\\003' > \"$4\" && cat \"$5\" >> \"$4\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func groupsFor(t *testing.T) []dnsfilter.Group {
	t.Helper()
	s := dnsfilter.Default()
	s.Enabled = true
	s.Lists = []dnsfilter.List{{ID: "a", URL: "https://x/a", Enabled: true}}
	s.AllowDomains = []string{"keep.test"}
	p, err := dnsfilter.ParseList([]byte("||ads.test^\n||imp.test^$important\n@@||ok.ads.test^\n"))
	if err != nil {
		t.Fatal(err)
	}
	return dnsfilter.Compile(s, map[string]*dnsfilter.Parsed{"a": p}).Groups
}

func TestFilterFilesSyncCompilesOnceAndWritesSource(t *testing.T) {
	bin, log := fakeCompiler(t)
	dir := filepath.Join(t.TempDir(), "rules")
	groups := groupsFor(t)
	f := FilterFiles{SingBox: bin}
	if err := f.Sync(context.Background(), dir, groups); err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		b, err := os.ReadFile(filepath.Join(dir, g.Tag+".srs"))
		if err != nil || !strings.HasPrefix(string(b), "SRS") {
			t.Fatalf("%s 没编出来: %v", g.Tag, err)
		}
		var src struct {
			Version int              `json:"version"`
			Rules   []map[string]any `json:"rules"`
		}
		if err := json.Unmarshal(b[4:], &src); err != nil || src.Version == 0 || len(src.Rules) != 1 {
			t.Fatalf("%s 的源码不对: %v %s", g.Tag, err, b[4:])
		}
	}
	calls := func() int { b, _ := os.ReadFile(log); return strings.Count(string(b), "\n") }
	if calls() != len(groups) {
		t.Fatalf("编译次数 %d 应等于组数 %d", calls(), len(groups))
	}
	if err := f.Sync(context.Background(), dir, groups); err != nil || calls() != len(groups) {
		t.Fatalf("文件已存在不该重编: calls=%d err=%v", calls(), err)
	}
}

func TestFilterFilesPruneOnlyTouchesStaleFilterFiles(t *testing.T) {
	dir := t.TempDir()
	groups := groupsFor(t)
	keep := filepath.Join(dir, groups[0].Tag+".srs")
	stale := filepath.Join(dir, dnsfilter.TagPrefix+"a-0000000000000000.srs")
	others := []string{"list-aaaaaaaa.srs", "geosite-cn.srs", "obnode-direct.srs", dnsfilter.TagPrefix + "a-1.txt"}
	for _, p := range append([]string{keep, stale}, others...) {
		if p == others[0] || p == others[1] || p == others[2] || p == others[3] {
			p = filepath.Join(dir, p)
		}
		if err := os.WriteFile(p, []byte("SRS"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gone := FilterFiles{}.Prune(dir, groups[:1])
	if len(gone) < 1 {
		t.Fatalf("应删掉不在清单里的过滤文件: %v", gone)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("旧的过滤规则集没删")
	}
	for _, p := range append([]string{keep}, others...) {
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("不该删 %s: %v", p, err)
		}
	}
	if got := (FilterFiles{}).Prune(filepath.Join(dir, "nope"), nil); got != nil {
		t.Errorf("目录不存在应什么都不做: %v", got)
	}
}

func TestFilterFilesSyncCompileFailureLeavesNoFile(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "sing-box")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho boom >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err := FilterFiles{SingBox: fake}.Sync(context.Background(), dir, groupsFor(t))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("应带上 sing-box 的报错: %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("失败不该留文件: %v", ents)
	}
}
