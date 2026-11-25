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

func TestWatchdogScript(t *testing.T) {
	s := WatchdogScript(WatchdogRequest{
		Dir: "/data/update", Journal: "/data/update/journal.json", Root: "/opt/open-box",
		StateDir: "/data/state", Backup: "/data/update/state-v0.1.484-main.1.tar.gz",
		Entries: []string{"bin", "panel/dist", "openwrt"}, Restart: "/etc/init.d/openbox-backend restart",
	})
	// 日志、安装根、状态目录都要引起来(路径里可能有空格)
	for _, want := range []string{
		`J='/data/update/journal.json'`,
		`R='/opt/open-box'`,
		`S='/data/state'`,
		`B='/data/update/state-v0.1.484-main.1.tar.gz'`,
		`WAIT=90`,
		`grep -q '"status": "healthy"' "$J"`,
		`tar -xzf "$B" -C "$S"`,
		RollbackFlag,
		`/etc/init.d/openbox-backend restart`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("脚本里没有 %q:\n%s", want, s)
		}
	}
	// 回滚要逆序:提交时最后换的先还原
	iBin := strings.Index(s, "p='bin'")
	iPanel := strings.Index(s, "p='panel/dist'")
	iOW := strings.Index(s, "p='openwrt'")
	if !(iOW < iPanel && iPanel < iBin) {
		t.Errorf("回滚顺序应该反过来: openwrt=%d panel=%d bin=%d", iOW, iPanel, iBin)
	}
	// 没有重启命令时不留一行空命令
	if s := WatchdogScript(WatchdogRequest{Journal: "/j", Root: "/r", Entries: []string{"bin"}}); strings.HasSuffix(strings.TrimSpace(s), "\n\n") {
		t.Error("没有重启命令时不该多一个空行")
	}
	// 单引号要转义,别让路径里的引号把脚本搞坏
	if s := WatchdogScript(WatchdogRequest{Journal: "/j'x", Root: "/r"}); !strings.Contains(s, `'/j'\''x'`) {
		t.Errorf("单引号没转义:\n%s", s)
	}
}

func TestStartWatchdogNeedsPaths(t *testing.T) {
	if _, err := StartWatchdog(WatchdogRequest{}); err == nil {
		t.Error("缺路径要报错")
	}
}

// seedJournal 摆一个「提交已完成、正在等自检」的安装根。
func seedJournal(t *testing.T, status string) (root, journal, stateDir string, j *Journal) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "opt", "open-box")
	stateDir = filepath.Join(base, "data", "state")
	journal = filepath.Join(base, "data", "update", JournalName)
	// 新版本已经就位,旧的留在 .old
	writeFile(t, filepath.Join(root, "bin", "openbox-backend"), "new-backend")
	writeFile(t, filepath.Join(root, "bin.old", "openbox-backend"), "old-backend")
	writeFile(t, filepath.Join(stateDir, "app.json"), `{"v":1}`)
	j = &Journal{Status: status, From: "v0.1.484-main.1", To: "v0.1.484-main.2", Root: root,
		StateDir: stateDir, StartedAt: "2026-11-25T03:00:00Z", Entries: []JournalEntry{{Path: "bin", Done: true}}}
	if err := WriteJournal(journal, j); err != nil {
		t.Fatal(err)
	}
	return root, journal, stateDir, j
}

func TestRecoverNothingToDo(t *testing.T) {
	if err := Recover(context.Background(), RecoverDeps{}); err != nil {
		t.Errorf("没接线时什么都不做: %v", err)
	}
	if err := Recover(context.Background(), RecoverDeps{Journal: filepath.Join(t.TempDir(), "nope.json"), Root: "/r"}); err != nil {
		t.Errorf("没有日志时什么都不做: %v", err)
	}
}

func TestRecoverAfterWatchdogRollback(t *testing.T) {
	root, journal, _, _ := seedJournal(t, JournalRestarting)
	flag := filepath.Join(filepath.Dir(journal), RollbackFlag)
	if err := os.WriteFile(flag, []byte("看门狗回滚了\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Recover(context.Background(), RecoverDeps{Journal: journal, Root: root}); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadJournal(journal)
	if got.Status != JournalRolledBack || !strings.Contains(got.Message, "看门狗") {
		t.Errorf("%+v", got)
	}
	if _, err := os.Stat(flag); !os.IsNotExist(err) {
		t.Error("标记要删掉")
	}
	// 回滚之后 .old 已经换回目标了,这里只验证不会 panic、状态对
}

func TestRecoverCommittingRollsBack(t *testing.T) {
	root, journal, _, _ := seedJournal(t, JournalCommitting)
	if err := Recover(context.Background(), RecoverDeps{Journal: journal, Root: root}); err != nil {
		t.Fatal(err)
	}
	// 提交中崩溃:文件要换回旧的
	if got := readFile(t, filepath.Join(root, "bin", "openbox-backend")); got != "old-backend" {
		t.Errorf("没回滚: %q", got)
	}
	got, _ := ReadJournal(journal)
	if got.Status != JournalRolledBack {
		t.Errorf("%+v", got)
	}
}

func TestRecoverRestartingHealthy(t *testing.T) {
	root, journal, _, _ := seedJournal(t, JournalRestarting)
	var called []string
	err := Recover(context.Background(), RecoverDeps{
		Journal: journal, Root: root,
		Health:        func(context.Context) error { called = append(called, "health"); return nil },
		CoreHello:     func(context.Context) error { called = append(called, "core"); return nil },
		KernelRestart: func(context.Context) error { called = append(called, "kernel"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(called, ",") != "health,core,kernel" {
		t.Errorf("自检顺序: %v", called)
	}
	got, _ := ReadJournal(journal)
	if got.Status != JournalHealthy || !strings.Contains(got.Message, "v0.1.484-main.2") {
		t.Errorf("%+v", got)
	}
	// 通过之后旧备份要清掉
	if _, err := os.Stat(filepath.Join(root, "bin.old")); !os.IsNotExist(err) {
		t.Error(".old 该清掉")
	}
}

func TestRecoverRestartingHealthRetries(t *testing.T) {
	root, journal, _, _ := seedJournal(t, JournalRestarting)
	n := 0
	if err := Recover(context.Background(), RecoverDeps{
		Journal: journal, Root: root, HealthTimeout: 2 * time.Second,
		Health: func(context.Context) error {
			n++
			if n < 3 {
				return errors.New("还没起来")
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("重试几次应该能过: %v", err)
	}
	if n != 3 {
		t.Errorf("重试次数: %d", n)
	}
}

func TestRecoverRestartingFailedExits(t *testing.T) {
	root, journal, _, _ := seedJournal(t, JournalRestarting)
	code := -1
	err := Recover(context.Background(), RecoverDeps{
		Journal: journal, Root: root, HealthTimeout: 10 * time.Millisecond,
		Health: func(context.Context) error { return errors.New("起不来") },
		Exit:   func(c int) { code = c },
	})
	if err == nil || !strings.Contains(err.Error(), "起不来") {
		t.Fatalf("%v", err)
	}
	if code != 1 {
		t.Errorf("自检失败要退出让看门狗回滚: %d", code)
	}
	got, _ := ReadJournal(journal)
	if got.Status != JournalFailed || !strings.Contains(got.Message, "自检没过") {
		t.Errorf("%+v", got)
	}
	// 失败时不能清掉 .old,看门狗还要用它回滚
	if _, err := os.Stat(filepath.Join(root, "bin.old")); err != nil {
		t.Errorf(".old 不能在自检失败时清掉: %v", err)
	}
}

func TestCleanUpdateDirKeepsRecoveryFiles(t *testing.T) {
	base := t.TempDir()
	for _, n := range []string{JournalName, RollbackFlag, "watchdog.sh"} {
		writeFile(t, filepath.Join(base, n), "x")
	}
	writeFile(t, filepath.Join(base, "v0.1.484-main.2", "bin", "a"), "x")
	if err := cleanUpdateDir(base); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{JournalName, RollbackFlag, "watchdog.sh"} {
		if _, err := os.Stat(filepath.Join(base, keep)); err != nil {
			t.Errorf("%s 不该删: %v", keep, err)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "v0.1.484-main.2")); !os.IsNotExist(err) {
		t.Error("暂存目录该删")
	}
	// 目录不存在时不报错
	if err := cleanUpdateDir(filepath.Join(base, "nope")); err != nil {
		t.Errorf("%v", err)
	}
}
