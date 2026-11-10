package nsprobe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Exec 在指定命名空间里调起 ns-probe 子命令。
type Exec struct {
	Run  system.Runner // 默认 system.ExecRunner
	Self string        // 本程序路径,默认 os.Executable()
	// Start 起一个能边读输出边写输入的进程(ProbeHold 用),默认真的起进程;测试里换假的。
	Start StartFunc
	// Extra 是 Self 与 "ns-probe" 之间要插的参数(测试里用),正常为空。
	Extra []string
}

// Probe 在命名空间 ns 里做一次探测,返回结果和子命令的标准错误(最后一行,可能为空)。
// 子命令起不来或输出看不懂返回 error;探测本身失败不是 error。
func (e *Exec) Probe(ctx context.Context, ns string, req Request) (Result, string, error) {
	run := e.Run
	if run == nil {
		run = system.ExecRunner
	}
	self := e.Self
	if self == "" {
		p, err := os.Executable()
		if err != nil {
			return Result{}, "", fmt.Errorf("找不到本程序的路径:%w", err)
		}
		self = p
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, "", err
	}
	timeout := DefaultTimeout
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	// 解析最多 dnsTimeout × 服务器数,再加探测本身,最后留点启动余量
	total := timeout + time.Duration(max(len(req.DNS), 1))*dnsTimeout + 5*time.Second
	cctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	args := append([]string{"netns", "exec", ns, self}, e.Extra...)
	args = append(args, "ns-probe", string(body))
	r, err := run(cctx, "ip", args...)
	if err != nil {
		return Result{}, "", fmt.Errorf("没能在命名空间里启动探测:%w", err)
	}
	stderr := lastLine(r.Stderr)
	if r.Code != 0 {
		if stderr == "" {
			stderr = fmt.Sprintf("退出码 %d", r.Code)
		}
		return Result{}, stderr, errors.New("探测进程失败:" + stderr)
	}
	var res Result
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &res); err != nil {
		return Result{}, stderr, fmt.Errorf("探测进程的输出看不懂:%w", err)
	}
	return res, stderr, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// HoldFor 是 ProbeHold 让探测进程把连接留着的最长时间(进程在 stdin 关闭时就会退出,这只是兜底)。
const HoldFor = 6 * time.Second

// Proc 是一个已经起来的进程:从 Stdout 读结果,关 Stdin 通知它退出,Wait 收尾。
type Proc struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	// Wait 等进程退出,返回它的标准错误和退出码。
	Wait func() (stderr string, code int, err error)
	// Kill 强行结束(ctx 到期时调用,可为空)。
	Kill func()
}

// StartFunc 起进程。
type StartFunc func(ctx context.Context, name string, args ...string) (*Proc, error)

func execStart(ctx context.Context, name string, args ...string) (*Proc, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var errb limitedBuffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Proc{
		Stdin:  in,
		Stdout: out,
		Kill:   func() { _ = cmd.Process.Kill() },
		Wait: func() (string, int, error) {
			err := cmd.Wait()
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return errb.String(), ee.ExitCode(), nil
			}
			return errb.String(), 0, err
		},
	}, nil
}

// limitedBuffer 只留前 4 KiB 的标准错误。
type limitedBuffer struct{ b bytes.Buffer }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := 4096 - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
func (l *limitedBuffer) String() string { return l.b.String() }

// ProbeHold 与 Probe 一样做一次探测,但让探测进程在出结果之后先把连接留着:收到结果、(连上了的话)调用 during,
// during 返回后才关进程的 stdin 让它退出。during 里就可以趁连接还开着去读内核连接表和 conntrack。
// 没连上(结果里没有 localPort)时进程不会等,during 不会被调用。
func (e *Exec) ProbeHold(ctx context.Context, ns string, req Request, during func(Result)) (Result, string, error) {
	start := e.Start
	if start == nil {
		start = execStart
	}
	self := e.Self
	if self == "" {
		p, err := os.Executable()
		if err != nil {
			return Result{}, "", fmt.Errorf("找不到本程序的路径:%w", err)
		}
		self = p
	}
	req.HoldMs = int(HoldFor / time.Millisecond)
	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, "", err
	}
	timeout := DefaultTimeout
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	total := timeout + time.Duration(max(len(req.DNS), 1))*dnsTimeout + 5*time.Second
	cctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	args := append([]string{"netns", "exec", ns, self}, e.Extra...)
	args = append(args, "ns-probe", string(body))
	proc, err := start(cctx, "ip", args...)
	if err != nil {
		return Result{}, "", fmt.Errorf("没能在命名空间里启动探测:%w", err)
	}
	finish := func() (string, int, error) {
		_ = proc.Stdin.Close()
		return proc.Wait()
	}
	type lineMsg struct {
		line string
		err  error
	}
	ch := make(chan lineMsg, 1)
	go func() {
		l, err := bufio.NewReader(proc.Stdout).ReadString('\n')
		ch <- lineMsg{l, err}
	}()
	var m lineMsg
	select {
	case m = <-ch:
	case <-cctx.Done():
		if proc.Kill != nil {
			proc.Kill()
		}
		_, _, _ = finish()
		return Result{}, "", fmt.Errorf("探测进程超时:%w", cctx.Err())
	}
	if m.err != nil && strings.TrimSpace(m.line) == "" {
		se, code, werr := finish()
		stderr := lastLine(se)
		if werr != nil {
			return Result{}, stderr, fmt.Errorf("探测进程失败:%w", werr)
		}
		if stderr == "" {
			stderr = fmt.Sprintf("退出码 %d,没有输出", code)
		}
		return Result{}, stderr, errors.New("探测进程失败:" + stderr)
	}
	var res Result
	if err := json.Unmarshal([]byte(strings.TrimSpace(m.line)), &res); err != nil {
		_, _, _ = finish()
		return Result{}, "", fmt.Errorf("探测进程的输出看不懂:%w", err)
	}
	if during != nil && res.LocalPort != 0 {
		during(res)
	}
	se, code, werr := finish()
	stderr := lastLine(se)
	if werr != nil {
		return Result{}, stderr, fmt.Errorf("探测进程失败:%w", werr)
	}
	if code != 0 {
		if stderr == "" {
			stderr = fmt.Sprintf("退出码 %d", code)
		}
		return Result{}, stderr, errors.New("探测进程失败:" + stderr)
	}
	return res, stderr, nil
}
