// Package conntrack 读系统的连接跟踪表(只读),给 terminal-test 的取证用:虚拟终端发出的那几条连接,
// 在 conntrack 里是怎么记的(原方向、回复方向有没有被改写、mark)。自己的设计,依据 Linux 内核 /proc/net/nf_conntrack 的
// 公开格式和 conntrack-tools 的 `conntrack -L` 输出,没有参照上游。
//
// 只读不改:不删表项、不改任何系统状态。两条路:先读 /proc/net/nf_conntrack(逐行扫描,按条件过滤,不把整张表读进内存);
// 打不开时退到 `conntrack -L`(把过滤条件交给命令)。两条路都不行就返回 error,调用方不能把「读不到」当成「没有」。
package conntrack

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// DefaultFile 是内核给的连接跟踪表。
const DefaultFile = "/proc/net/nf_conntrack"

const (
	maxScanLines = 4_000_000 // 扫描行数上限(再大的表也不该占住请求)
	maxEntries   = 2000      // 返回条数上限
	maxLine      = 1 << 16
	cmdOutCap    = 8 << 20
)

// Entry 是一条连接跟踪记录。原方向是终端发出的那一边(Src → Dst),回复方向(R 开头)是对端回来的那一边;
// 回复方向的源 / 目的与原方向不是简单互换时,说明地址或端口被改写过(DNAT / REDIRECT 等)。
type Entry struct {
	Proto      string // tcp / udp / ...
	State      string // TCP 的状态(ESTABLISHED / TIME_WAIT ...),没有为空
	Src, Dst   string
	Sport      int
	Dport      int
	RSrc, RDst string
	RSport     int
	RDport     int
	Mark       uint32
	Unreplied  bool   // 带 [UNREPLIED]:还没收到过回复
	Line       string // 原始一行(去掉首尾空白)
}

var protos = map[string]bool{"tcp": true, "udp": true, "udplite": true, "icmp": true, "icmpv6": true, "sctp": true, "dccp": true, "gre": true}

// Parse 解析一行。不是连接记录(缺协议或缺地址)返回 false。
// 同时认 /proc/net/nf_conntrack(开头带 ipv4 2 …)和 `conntrack -L`(直接 tcp 6 …)两种写法。
func Parse(line string) (Entry, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Entry{}, false
	}
	e := Entry{Line: line}
	reply := false
	var haveSrc, haveDst, haveRSrc, haveRDst bool
	for _, tok := range strings.Fields(line) {
		if k, v, ok := strings.Cut(tok, "="); ok {
			switch k {
			case "src":
				if !haveSrc {
					e.Src, haveSrc = v, true
				} else {
					e.RSrc, haveRSrc, reply = v, true, true
				}
			case "dst":
				if !reply {
					e.Dst, haveDst = v, true
				} else {
					e.RDst, haveRDst = v, true
				}
			case "sport":
				n, _ := strconv.Atoi(v)
				if !reply {
					e.Sport = n
				} else {
					e.RSport = n
				}
			case "dport":
				n, _ := strconv.Atoi(v)
				if !reply {
					e.Dport = n
				} else {
					e.RDport = n
				}
			case "mark":
				if n, err := strconv.ParseUint(v, 10, 32); err == nil {
					e.Mark = uint32(n)
				}
			}
			continue
		}
		switch {
		case tok == "[UNREPLIED]":
			e.Unreplied = true
		case e.Proto == "" && protos[tok]:
			e.Proto = tok
		case e.Proto != "" && e.State == "" && isState(tok):
			e.State = tok
		}
	}
	if e.Proto == "" || !haveSrc || !haveDst || !haveRSrc || !haveRDst {
		return Entry{}, false
	}
	return e, true
}

func isState(s string) bool {
	if len(s) < 3 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r == '_') {
			return false
		}
	}
	return true
}

// Query 是过滤条件,零值 = 不限。地址按 IP 比较(写法不同的同一个地址算同一个)。
type Query struct {
	Proto string
	Src   string // 原方向的源
	Dst   string // 原方向的目的
	Sport int
	Dport int
}

func (q Query) match(e *Entry) bool {
	if q.Proto != "" && e.Proto != q.Proto {
		return false
	}
	if q.Src != "" && !sameIP(q.Src, e.Src) {
		return false
	}
	if q.Dst != "" && !sameIP(q.Dst, e.Dst) {
		return false
	}
	if q.Sport != 0 && e.Sport != q.Sport {
		return false
	}
	if q.Dport != 0 && e.Dport != q.Dport {
		return false
	}
	return true
}

func sameIP(a, b string) bool {
	x, y := net.ParseIP(a), net.ParseIP(b)
	return x != nil && y != nil && x.Equal(y)
}

// Reader 读连接跟踪表。零值可用。
type Reader struct {
	File     string                       // 默认 DefaultFile
	Run      system.Runner                // 退到命令时用,默认 system.ExecRunnerCap(8 MiB)
	LookPath func(string) (string, error) // 默认 exec.LookPath
}

// Find 返回符合条件的记录(最多 maxEntries 条,表里顺序)。读不到表返回 error。
func (r *Reader) Find(ctx context.Context, q Query) ([]Entry, error) {
	file := r.File
	if file == "" {
		file = DefaultFile
	}
	f, ferr := os.Open(file)
	if ferr == nil {
		defer f.Close()
		return scan(ctx, bufio.NewScanner(f), q)
	}
	look := r.LookPath
	if look == nil {
		look = exec.LookPath
	}
	if _, err := look("conntrack"); err != nil {
		return nil, fmt.Errorf("读不到连接跟踪表:%w(也没有 conntrack 命令)", ferr)
	}
	run := r.Run
	if run == nil {
		run = system.ExecRunnerCap(cmdOutCap)
	}
	res, err := run(ctx, "conntrack", commandArgs(q)...)
	if err != nil {
		return nil, fmt.Errorf("运行 conntrack 失败:%w", err)
	}
	if res.Code != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("退出码 %d", res.Code)
		}
		return nil, errors.New("conntrack -L 失败:" + msg)
	}
	return scan(ctx, bufio.NewScanner(strings.NewReader(res.Stdout)), q)
}

func commandArgs(q Query) []string {
	args := []string{"-L"}
	if q.Proto != "" {
		args = append(args, "-p", q.Proto)
	}
	if q.Src != "" {
		args = append(args, "-s", q.Src)
	}
	if q.Dst != "" {
		args = append(args, "-d", q.Dst)
	}
	if q.Sport != 0 {
		args = append(args, "--sport", strconv.Itoa(q.Sport))
	}
	if q.Dport != 0 {
		args = append(args, "--dport", strconv.Itoa(q.Dport))
	}
	return args
}

func scan(ctx context.Context, sc *bufio.Scanner, q Query) ([]Entry, error) {
	sc.Buffer(make([]byte, 0, 4096), maxLine)
	var out []Entry
	for n := 0; sc.Scan(); n++ {
		if n%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if n >= maxScanLines {
			break
		}
		e, ok := Parse(sc.Text())
		if !ok || !q.match(&e) {
			continue
		}
		out = append(out, e)
		if len(out) >= maxEntries {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读连接跟踪表出错:%w", err)
	}
	return out, nil
}
