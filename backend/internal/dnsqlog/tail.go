package dnsqlog

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// DefaultPollInterval 是跟读日志文件的轮询间隔。
const DefaultPollInterval = 500 * time.Millisecond

// DefaultTrimAbove 是日志文件的大小上限:读完之后文件超过它就清空(见 Log.TrimAbove)。
// 日志是边写边读的,清掉的都是已经读进记录的部分;4 MiB 大约是一万几千次查询。
const DefaultTrimAbove int64 = 4 << 20

// StateFile 是落盘文件名(存在 store 的目录里)。
const StateFile = "dns-query-log"

// Persister 是落盘用的存储(*store.Store 满足)。
type Persister interface {
	Load(name string, v any) (bool, error)
	Save(name string, v any) error
}

// Run 跟读 path 指向的 dnsmasq 日志文件,把新增的行交给 l.Feed,直到 ctx 结束。
// 第一次打开时从文件末尾开始(不回放旧日志);文件被轮转(换了文件)或截断就从头读新文件。
// 打不开就每个轮询间隔重试,期间 Connected 为假。path 为空什么都不做、立刻返回。
// 另外每 30 秒把有变化的记录和统计写进 p(p 为 nil 不落盘),退出前再写一次。
func Run(ctx context.Context, l *Log, path string, p Persister, interval time.Duration, now func() time.Time) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if now == nil {
		now = time.Now
	}
	if p != nil {
		var s Snapshot
		if ok, err := p.Load(StateFile, &s); err != nil {
			slog.Warn("读取 DNS 查询记录失败,从空记录开始", "error", err)
		} else if ok {
			l.Restore(s)
		}
		done := make(chan struct{})
		defer func() { <-done }()
		fctx, stop := context.WithCancel(ctx)
		defer stop()
		go func() {
			defer close(done)
			flushLoop(fctx, l, p)
		}()
	}
	if strings.TrimSpace(path) == "" {
		<-ctx.Done()
		return
	}
	follow(ctx, l, path, interval, now)
	l.SetConnected(false)
}

func flushLoop(ctx context.Context, l *Log, p Persister) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	flush := func() {
		if s, ok := l.Snapshot(); ok {
			if err := p.Save(StateFile, s); err != nil {
				slog.Warn("保存 DNS 查询记录失败", "error", err)
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case <-t.C:
			flush()
		}
	}
}

func follow(ctx context.Context, l *Log, path string, interval time.Duration, now func() time.Time) {
	first := true
	for ctx.Err() == nil {
		f, err := os.Open(path)
		if err != nil {
			l.SetConnected(false)
			if !sleep(ctx, interval) {
				return
			}
			continue
		}
		l.SetConnected(true)
		if first {
			first = false
			if _, err := f.Seek(0, io.SeekEnd); err != nil {
				f.Close()
				l.SetConnected(false)
				if !sleep(ctx, interval) {
					return
				}
				continue
			}
		}
		readUntilRotated(ctx, l, f, path, interval, now)
		f.Close()
	}
}

// readUntilRotated 一直读 f,直到文件被换掉(轮转)、被截断或 ctx 结束。
func readUntilRotated(ctx context.Context, l *Log, f *os.File, path string, interval time.Duration, now func() time.Time) {
	r := bufio.NewReaderSize(f, 64<<10)
	var partial []byte
	off, _ := f.Seek(0, io.SeekCurrent) // 已读到的位置(第一次打开是文件末尾),截断判断要用它
	for ctx.Err() == nil {
		chunk, err := r.ReadSlice('\n')
		off += int64(len(chunk))
		switch {
		case err == nil:
			line := append(partial, chunk...)
			partial = partial[:0]
			l.Feed(strings.TrimRight(string(line), "\r\n"), now())
		case errors.Is(err, bufio.ErrBufferFull):
			partial = partial[:0] // 一行超过缓冲区:不是 dnsmasq 写的,丢掉这一段
		case errors.Is(err, io.EOF):
			if len(partial)+len(chunk) <= maxLine {
				partial = append(partial, chunk...)
			} else {
				partial = partial[:0]
			}
			if rotated(f, path, off) {
				return
			}
			if len(partial) == 0 && l.trim(f, path, off) {
				off = 0
				r.Reset(f)
			}
			if !sleep(ctx, interval) {
				return
			}
		default:
			return
		}
	}
}

// trim 在文件超过上限、而且我们已经读到最后一个字节时把它清空,并把 f 的读位置挪回开头。
// 清空前再看一眼大小:这一刻有新写入的就先不清,读完下一轮再说,不丢那几行。
func (l *Log) trim(f *os.File, path string, off int64) bool {
	if l.TrimAbove <= 0 || off <= l.TrimAbove || l.Owned == nil || !l.Owned() {
		return false
	}
	fi, err := f.Stat()
	if err != nil || fi.Size() != off {
		return false
	}
	if err := os.Truncate(path, 0); err != nil {
		slog.Warn("清空 DNS 查询日志失败", "path", path, "error", err)
		return false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	return true
}

// rotated 判断 path 现在是不是另一个文件、已经不存在(被删了,比如撤销接管时),或者比已读的位置还短(被截断)。
// 其它读不到 path 的情况当作「还没换」,下一轮再看。
func rotated(f *os.File, path string, off int64) bool {
	cur, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	mine, err := f.Stat()
	if err != nil {
		return true
	}
	if !os.SameFile(cur, mine) {
		return true
	}
	return cur.Size() < off
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
