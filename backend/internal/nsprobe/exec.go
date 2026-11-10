package nsprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Exec 在指定命名空间里调起 ns-probe 子命令。
type Exec struct {
	Run  system.Runner // 默认 system.ExecRunner
	Self string        // 本程序路径,默认 os.Executable()
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
