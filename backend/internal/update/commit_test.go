package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(b)
}

// seed 摆好一个安装根、一个状态目录和一个暂存目录。
func seed(t *testing.T) (root, staging, stateDir, journal string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "opt", "open-box")
	staging = filepath.Join(base, "data", "update", "v0.1.484-main.2", "openbox-v0.1.484-main.2-linux-amd64")
	stateDir = filepath.Join(base, "data", "state")
	journal = filepath.Join(base, "data", "update", JournalName)

	// 安装根:要换的 + 不该碰的
	writeFile(t, filepath.Join(root, "bin", "openbox-backend"), "old-backend")
	writeFile(t, filepath.Join(root, "bin", "sing-box"), "old-singbox")
	writeFile(t, filepath.Join(root, "panel", "dist", "index.html"), "old-html")
	writeFile(t, filepath.Join(root, "geodata", "geosite-cn.srs"), "old-geo")
	writeFile(t, filepath.Join(root, "etc", "profile.json"), `{"keep":"me"}`)
	writeFile(t, filepath.Join(root, "data", "store.json"), `{"keep":"me too"}`)

	// 状态目录:提交前要打快照的
	writeFile(t, filepath.Join(stateDir, "app.json"), `{"v":1}`)
	writeFile(t, filepath.Join(stateDir, "cache.db"), "not-a-json")

	// 暂存目录:新版本
	writeFile(t, filepath.Join(staging, "bin", "openbox-backend"), "new-backend")
	writeFile(t, filepath.Join(staging, "bin", "sing-box"), "new-singbox")
	writeFile(t, filepath.Join(staging, "panel", "dist", "index.html"), "new-html")
	writeFile(t, filepath.Join(staging, "geodata", "geosite-cn.srs"), "new-geo")
	writeFile(t, filepath.Join(staging, "openwrt", "bin", "openbox-kernel"), "new-kernel")
	return root, staging, stateDir, journal
}

func req(root, staging, stateDir, journal string) CommitRequest {
	return CommitRequest{
		Root: root, Staging: staging, Journal: journal, StateDir: stateDir,
		From: "v0.1.484-main.1", To: "v0.1.484-main.2",
		Now: time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC),
	}
}

func TestCommitThenRollback(t *testing.T) {
	root, staging, stateDir, journal := seed(t)
	ctx := context.Background()
	j, err := Commit(ctx, req(root, staging, stateDir, journal), Ops{})
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JournalRestarting || len(j.Entries) != 4 {
		t.Fatalf("%+v", j)
	}
	// 换位清单:bin / panel/dist / geodata / openwrt(暂存里有才换)
	var got []string
	for _, e := range j.Entries {
		got = append(got, e.Path)
		if !e.Done {
			t.Errorf("没标完成: %s", e.Path)
		}
	}
	if strings.Join(got, ",") != "bin,panel/dist,geodata,openwrt" {
		t.Errorf("换位清单: %v", got)
	}
	// 新内容到位
	for name, want := range map[string]string{
		"bin/openbox-backend":        "new-backend",
		"bin/sing-box":               "new-singbox",
		"panel/dist/index.html":      "new-html",
		"geodata/geosite-cn.srs":     "new-geo",
		"openwrt/bin/openbox-kernel": "new-kernel",
	} {
		if got := readFile(t, filepath.Join(root, filepath.FromSlash(name))); got != want {
			t.Errorf("%s = %q, 想要 %q", name, got, want)
		}
	}
	// 不该碰的还在
	if readFile(t, filepath.Join(root, "etc", "profile.json")) != `{"keep":"me"}` ||
		readFile(t, filepath.Join(root, "data", "store.json")) != `{"keep":"me too"}` {
		t.Error("etc / data 不该被碰")
	}
	// 旧的备份还在(回滚要用)
	if readFile(t, filepath.Join(root, "bin.old", "openbox-backend")) != "old-backend" {
		t.Error("旧 bin 没有备份")
	}
	// 日志落盘了
	onDisk, err := ReadJournal(journal)
	if err != nil || onDisk == nil || onDisk.Status != JournalRestarting || onDisk.To != "v0.1.484-main.2" {
		t.Fatalf("%+v %v", onDisk, err)
	}

	// 回滚
	if err := Rollback(ctx, journal, onDisk, Ops{}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"bin/openbox-backend":    "old-backend",
		"bin/sing-box":           "old-singbox",
		"panel/dist/index.html":  "old-html",
		"geodata/geosite-cn.srs": "old-geo",
	} {
		if got := readFile(t, filepath.Join(root, filepath.FromSlash(name))); got != want {
			t.Errorf("回滚后 %s = %q, 想要 %q", name, got, want)
		}
	}
	// openwrt 是新增的(原来没有):回滚要把它删掉
	if _, err := os.Stat(filepath.Join(root, "openwrt")); !os.IsNotExist(err) {
		t.Errorf("回滚应删掉新加的 openwrt: %v", err)
	}
	// .old / .new 都清干净
	for _, p := range []string{"bin.old", "bin.new", "panel/dist.old", "panel/dist.new", "geodata.old"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); !os.IsNotExist(err) {
			t.Errorf("回滚后不该留下 %s: %v", p, err)
		}
	}
	after, _ := ReadJournal(journal)
	if after.Status != JournalRolledBack {
		t.Errorf("状态应是 rolled-back: %+v", after)
	}
}

// failRenameAt 让第 n 次改名失败(从 1 数),用来逐步骤注入故障。
func failRenameAt(n int) (Ops, *int) {
	calls := 0
	return Ops{Rename: func(a, b string) error {
		calls++
		if calls == n {
			return errors.New("注入的改名失败")
		}
		return os.Rename(a, b)
	}}, &calls
}

func TestCommitFailureAtEveryStepRollsBack(t *testing.T) {
	// 先数一遍成功时要改几次名:4 个目录 × 2 次(挪 .new / 换位)+ 有的条目还要挪 .old
	_, staging0, stateDir0, journal0 := seed(t)
	root0, _, _, _ := seed(t)
	ops, calls := failRenameAt(-1)
	if _, err := Commit(context.Background(), req(root0, staging0, stateDir0, journal0), ops); err != nil {
		t.Fatal(err)
	}
	total := *calls
	if total < 8 {
		t.Fatalf("改名次数太少,注入测试没意义: %d", total)
	}

	for n := 1; n <= total; n++ {
		root, staging, stateDir, journal := seed(t)
		ops, _ := failRenameAt(n)
		j, err := Commit(context.Background(), req(root, staging, stateDir, journal), ops)
		if err == nil {
			t.Fatalf("第 %d 次改名应该失败", n)
		}
		if j == nil {
			t.Fatalf("第 %d 次:失败也要给日志", n)
		}
		if err := Rollback(context.Background(), journal, j, Ops{}); err != nil {
			t.Fatalf("第 %d 次:回滚失败 %v", n, err)
		}
		// 回滚后必须与升级前一模一样
		for name, want := range map[string]string{
			"bin/openbox-backend":    "old-backend",
			"bin/sing-box":           "old-singbox",
			"panel/dist/index.html":  "old-html",
			"geodata/geosite-cn.srs": "old-geo",
			"etc/profile.json":       `{"keep":"me"}`,
		} {
			if got := readFile(t, filepath.Join(root, filepath.FromSlash(name))); got != want {
				t.Errorf("第 %d 次改名失败后 %s = %q, 想要 %q", n, name, got, want)
			}
		}
		for _, p := range []string{"bin.old", "bin.new", "panel/dist.old", "panel/dist.new", "geodata.old", "geodata.new"} {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); !os.IsNotExist(err) {
				t.Errorf("第 %d 次:回滚后不该留下 %s", n, p)
			}
		}
	}
}

func TestCommitSkipsMissingGeodata(t *testing.T) {
	root, staging, stateDir, journal := seed(t)
	// 包里不带规则库:用户自己下的规则集不能被删
	if err := os.RemoveAll(filepath.Join(staging, "geodata")); err != nil {
		t.Fatal(err)
	}
	j, err := Commit(context.Background(), req(root, staging, stateDir, journal), Ops{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range j.Entries {
		if e.Path == "geodata" {
			t.Error("暂存里没有 geodata 就不该换")
		}
	}
	if got := readFile(t, filepath.Join(root, "geodata", "geosite-cn.srs")); got != "old-geo" {
		t.Errorf("规则库被动了: %q", got)
	}
}

func TestCommitNeedsBin(t *testing.T) {
	root, staging, stateDir, journal := seed(t)
	if err := os.RemoveAll(filepath.Join(staging, "bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(context.Background(), req(root, staging, stateDir, journal), Ops{}); err == nil || !strings.Contains(err.Error(), "bin/") {
		t.Fatalf("%v", err)
	}
	// 一个都不换的时候也要拒绝,不能「成功但什么都没做」
	if err := os.RemoveAll(staging); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(context.Background(), req(root, staging, stateDir, journal), Ops{}); err == nil || !strings.Contains(err.Error(), "没有可替换") {
		t.Fatalf("%v", err)
	}
}

func TestJournalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, JournalName)
	if j, err := ReadJournal(p); j != nil || err != nil {
		t.Fatalf("没有日志时应是 nil: %+v %v", j, err)
	}
	want := &Journal{Status: JournalCommitting, From: "v1", To: "v2", Root: "/opt/open-box", StartedAt: "2026-11-25T03:00:00Z",
		Entries: []JournalEntry{{Path: "bin", Done: true}}}
	if err := WriteJournal(p, want); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Error("不该留下 .tmp")
	}
	got, err := ReadJournal(p)
	if err != nil || got.Status != want.Status || got.From != want.From || len(got.Entries) != 1 || !got.Entries[0].Done {
		t.Fatalf("%+v %v", got, err)
	}
	if err := os.WriteFile(p, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJournal(p); err == nil || !strings.Contains(err.Error(), "读不了") {
		t.Errorf("坏日志要报错: %v", err)
	}
}

func TestSnapshotRestoreState(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	writeFile(t, filepath.Join(stateDir, "app.json"), `{"v":1}`)
	writeFile(t, filepath.Join(stateDir, "sub", "deep.json"), `{"deep":true}`)
	writeFile(t, filepath.Join(stateDir, "cache.db"), "binary")
	snap := filepath.Join(dir, "state.tar.gz")
	if err := SnapshotState(stateDir, snap); err != nil {
		t.Fatal(err)
	}
	// 改掉状态文件,再恢复
	writeFile(t, filepath.Join(stateDir, "app.json"), `{"v":2}`)
	writeFile(t, filepath.Join(stateDir, "new.json"), `{"new":true}`)
	if err := RestoreState(snap, stateDir); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(stateDir, "app.json")); got != `{"v":1}` {
		t.Errorf("状态没恢复: %q", got)
	}
	// 快照只收 .json 且只收顶层:cache.db 与子目录里的不进
	if got := readFile(t, filepath.Join(stateDir, "cache.db")); got != "binary" {
		t.Errorf("cache.db 不该被动: %q", got)
	}
	// 升级之后新写的文件不回滚掉
	if got := readFile(t, filepath.Join(stateDir, "new.json")); got != `{"new":true}` {
		t.Errorf("新文件不该删: %q", got)
	}
	// 状态目录不存在时写空快照,不报错
	empty := filepath.Join(dir, "none.tar.gz")
	if err := SnapshotState(filepath.Join(dir, "nope"), empty); err != nil {
		t.Fatalf("没有状态目录不该报错: %v", err)
	}
	if err := RestoreState(empty, filepath.Join(dir, "restored")); err != nil {
		t.Fatalf("空快照恢复: %v", err)
	}
}
