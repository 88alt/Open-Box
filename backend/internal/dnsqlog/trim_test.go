package dnsqlog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func logLine(domain string) string {
	return "Oct 28 10:00:00 dnsmasq[1]: query[A] " + domain + " from 1.1.1.1\n"
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestTrimOwnedFileAboveLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("后端只跑在 OpenWrt / Debian 上")
	}
	path := filepath.Join(t.TempDir(), "dnsmasq.log")
	appendTo(t, path, "")
	l := New()
	l.TrimAbove = 300
	var owned atomic.Bool
	owned.Store(true)
	l.Owned = owned.Load
	stop := startRun(t, l, path, nil)
	defer stop()
	waitFor(t, "连上", l.Connected)

	for i := 0; i < 8; i++ { // 8 × 约 60 字节,超过 300
		appendTo(t, path, logLine(fmt.Sprintf("a%d.org", i)))
	}
	waitFor(t, "8 条都读进记录", func() bool { return total(l) == 8 })
	waitFor(t, "文件被清空", func() bool { return fileSize(t, path) == 0 })

	// 清空后 dnsmasq 接着从头写:新内容照样读得到,已经读进的记录不受影响
	appendTo(t, path, logLine("b.org"))
	waitFor(t, "清空后新写的内容", func() bool { return total(l) == 9 })
}

func TestTrimLeavesFileAloneUnlessOwned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("后端只跑在 OpenWrt / Debian 上")
	}
	cases := []struct {
		name  string
		above int64
		owned func() bool
	}{
		{"不是我们让写的", 300, func() bool { return false }},
		{"没接 Owned", 300, nil},
		{"没设上限", 0, func() bool { return true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dnsmasq.log")
			appendTo(t, path, "")
			l := New()
			l.TrimAbove, l.Owned = c.above, c.owned
			stop := startRun(t, l, path, nil)
			defer stop()
			waitFor(t, "连上", l.Connected)
			for i := 0; i < 8; i++ {
				appendTo(t, path, logLine(fmt.Sprintf("a%d.org", i)))
			}
			waitFor(t, "8 条都读进记录", func() bool { return total(l) == 8 })
			time.Sleep(60 * time.Millisecond) // 留十几个轮询间隔:该清早清了
			if n := fileSize(t, path); n <= 300 {
				t.Fatalf("不该清空,文件只剩 %d 字节", n)
			}
		})
	}
}

func TestFollowNoticesRemovedFileAndReopens(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上删不掉正被读的文件;后端只跑在 OpenWrt / Debian 上")
	}
	path := filepath.Join(t.TempDir(), "dnsmasq.log")
	appendTo(t, path, "")
	l := New()
	stop := startRun(t, l, path, nil)
	defer stop()
	waitFor(t, "连上", l.Connected)

	// 撤销接管会把日志文件删掉:这时不再算「已连接」
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "文件没了就不算连上", func() bool { return !l.Connected() })

	// 以后重新接管,dnsmasq 又建出同名文件:从头读
	appendTo(t, path, logLine("c.org"))
	waitFor(t, "重新连上", l.Connected)
	waitFor(t, "新文件里的内容", func() bool { return total(l) == 1 })
}
