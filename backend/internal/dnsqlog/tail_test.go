package dnsqlog

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等不到: %s", what)
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func startRun(t *testing.T, l *Log, path string, p Persister) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); Run(ctx, l, path, p, 5*time.Millisecond, nil) }()
	return func() { cancel(); wg.Wait() }
}

func total(l *Log) int { return l.Records(Query{}).Total }

func TestFollowReadsNewLinesOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dnsmasq.log")
	appendTo(t, path, "Oct 28 10:00:00 dnsmasq[1]: query[A] old.org from 1.1.1.1\n") // 启动前的旧日志不回放
	l := New()
	stop := startRun(t, l, path, nil)
	defer stop()
	waitFor(t, "连上日志文件", l.Connected)
	appendTo(t, path, "Oct 28 10:00:01 dnsmasq[1]: query[A] new.org from 1.1.1.1\nOct 28 10:00:01 dnsmasq[1]: rep")
	waitFor(t, "读到完整的一行", func() bool { return total(l) == 1 })
	appendTo(t, path, "ly new.org is 2.2.2.2\n") // 一行分两次写进来
	waitFor(t, "拼好被拆开的一行", func() bool { return l.Records(Query{Result: "allowed"}).Total == 1 })
	if d := l.Records(Query{}).Rows[0].Domain; d != "new.org" {
		t.Errorf("不该读到旧日志: %s", d)
	}
}

func TestFollowSurvivesRotationAndTruncation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不允许给正被打开的文件改名;后端只跑在 OpenWrt / Debian 上")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "dnsmasq.log")
	appendTo(t, path, "")
	l := New()
	stop := startRun(t, l, path, nil)
	defer stop()
	waitFor(t, "连上", l.Connected)
	line := func(d string) string { return "Oct 28 10:00:00 dnsmasq[1]: query[A] " + d + " from 1.1.1.1\n" }

	appendTo(t, path, line("a.org"))
	waitFor(t, "a", func() bool { return total(l) == 1 })

	// 轮转:旧文件改名,新文件同名,从头读
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	appendTo(t, path, line("b.org"))
	waitFor(t, "轮转后的新文件", func() bool { return total(l) == 2 })

	// 截断:同一个文件被清空后重新写
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond) // 截断后马上又写得比已读的还多,光看大小分不出来;留几个轮询间隔让它看到「变短了」
	appendTo(t, path, line("c.org"))
	waitFor(t, "截断后重新写的内容", func() bool { return total(l) == 3 })
}

func TestFollowFileAppearsLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "later.log")
	l := New()
	stop := startRun(t, l, path, nil)
	defer stop()
	time.Sleep(30 * time.Millisecond)
	if l.Connected() {
		t.Fatal("文件不存在时不应是已连接")
	}
	appendTo(t, path, "")
	waitFor(t, "文件出现后连上", l.Connected)
	// 文件是后来才出现的(此前一直没打开过):从末尾开始,同样不回放
	appendTo(t, path, "Oct 28 10:00:00 dnsmasq[1]: query[A] x.org from 1.1.1.1\n")
	waitFor(t, "读到新行", func() bool { return total(l) == 1 })
}

func TestRunWithoutPathStaysDisconnected(t *testing.T) {
	l := New()
	stop := startRun(t, l, "  ", nil)
	time.Sleep(20 * time.Millisecond)
	if l.Connected() {
		t.Error("没配路径不应是已连接")
	}
	stop()
}

type memPersist struct {
	mu sync.Mutex
	s  *Snapshot
}

func (m *memPersist) Load(_ string, v any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s == nil {
		return false, nil
	}
	*(v.(*Snapshot)) = *m.s
	return true, nil
}
func (m *memPersist) Save(_ string, v any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := v.(Snapshot)
	m.s = &s
	return nil
}

func TestRunRestoresAndFlushesOnExit(t *testing.T) {
	p := &memPersist{}
	first := New()
	feed(first, t0, "query[A] kept.org from 1.1.1.1")
	s, _ := first.Snapshot()
	p.s = &s

	l := New()
	path := filepath.Join(t.TempDir(), "d.log")
	appendTo(t, path, "")
	stop := startRun(t, l, path, p)
	waitFor(t, "载入上次的记录", func() bool { return total(l) == 1 })
	waitFor(t, "连上", l.Connected)
	appendTo(t, path, "Oct 28 10:00:00 dnsmasq[1]: query[A] fresh.org from 1.1.1.1\n")
	waitFor(t, "读到新行", func() bool { return total(l) == 2 })
	stop() // 退出时要把新记录写进去
	p.mu.Lock()
	n := len(p.s.Records)
	p.mu.Unlock()
	if n != 2 {
		t.Errorf("退出时应落盘: %d 条", n)
	}
}
