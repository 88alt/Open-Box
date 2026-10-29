package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 服务控制:启动 / 停止 / 重启 / 开机自启 sing-box(和后端自己)。
// 只通过系统自带的服务管理器做事,不自己 fork 常驻进程:
//   - OpenWrt:/etc/init.d/<名字> start|stop|restart|enable|disable|running|enabled
//   - systemd:systemctl start|stop|restart|enable|disable <名字>,查询用 is-active / is-enabled
//
// 所有会改系统状态的调用都在这里;测试用假的 Runner,真机验证由维护者做。

// InitKind 是这台机器的服务管理器。
type InitKind string

const (
	InitOpenWrt InitKind = "openwrt"
	InitSystemd InitKind = "systemd"
	InitNone    InitKind = "none"
)

// 错误。
var (
	ErrNoInit    = errors.New("没有识别到受支持的服务管理器(OpenWrt procd 或 systemd)")
	ErrBadAction = errors.New("不支持的服务操作")
	ErrBadName   = errors.New("服务名只能包含字母、数字和 . _ @ -")
)

// serviceNameRe:服务名会拼进命令行,只放行最保守的字符,并且不能以 - 开头(避免被当成选项)。
var serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9_.@][A-Za-z0-9_.@-]*$`)

// ValidServiceName 检查服务名是否可以安全地传给服务管理器。
func ValidServiceName(name string) error {
	if len(name) == 0 || len(name) > 64 || !serviceNameRe.MatchString(name) {
		return ErrBadName
	}
	return nil
}

// Actions 是允许的操作。
var Actions = map[string]bool{"start": true, "stop": true, "restart": true, "enable": true, "disable": true}

// ServiceInfo 对应前端的 OpenboxServiceInfo。
type ServiceInfo struct {
	Running       bool   `json:"running"`
	Raw           string `json:"raw"`
	Autostart     *bool  `json:"autostart,omitempty"`
	UptimeSeconds *int64 `json:"uptimeSeconds,omitempty"`
}

// ActionResult 对应前端的 OpenboxServiceActionResult。
type ActionResult struct {
	OK         bool   `json:"ok"`
	Code       int    `json:"code"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// RunResult 是一次命令的结果。
type RunResult struct {
	Stdout string
	Stderr string
	Code   int // 命令自己的退出码
}

// Runner 运行命令。命令能启动但退出码非 0 不算 error(看 RunResult.Code);只有根本启动不了才返回 error。
type Runner func(ctx context.Context, name string, args ...string) (RunResult, error)

const maxOut = 4096

type capWriter struct {
	buf bytes.Buffer
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

// ExecRunner 是真正运行命令的 Runner(输出各只留前 4 KiB,够看服务操作的报错)。
func ExecRunner(ctx context.Context, name string, args ...string) (RunResult, error) {
	return execRun(ctx, maxOut, name, args...)
}

// ExecRunnerCap 返回一个标准输出最多留 max 字节的 Runner(读系统日志这类输出长的命令用)。
func ExecRunnerCap(max int) Runner {
	return func(ctx context.Context, name string, args ...string) (RunResult, error) {
		return execRun(ctx, max, name, args...)
	}
}

func execRun(ctx context.Context, max int, name string, args ...string) (RunResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, errw := &capWriter{max: max}, &capWriter{max: maxOut}
	cmd.Stdout, cmd.Stderr = out, errw
	err := cmd.Run()
	res := RunResult{Stdout: out.buf.String(), Stderr: errw.buf.String()}
	if err == nil {
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.Code = ee.ExitCode()
		return res, nil
	}
	return res, err
}

// DetectInit 判断服务管理器。exists 判断路径是否存在,lookPath 查可执行文件;测试里注入假的。
func DetectInit(exists func(string) bool, lookPath func(string) (string, error)) InitKind {
	if exists("/etc/openwrt_release") && exists("/etc/init.d") {
		return InitOpenWrt
	}
	if _, err := lookPath("systemctl"); err == nil && exists("/run/systemd/system") {
		return InitSystemd
	}
	return InitNone
}

// DetectInitHere 用真实的文件系统判断。
func DetectInitHere() InitKind {
	return DetectInit(func(p string) bool { _, err := os.Stat(p); return err == nil }, exec.LookPath)
}

// Services 操作某一种服务管理器下的服务。
type Services struct {
	Kind InitKind
	Run  Runner
	// ProcUptime 读系统已运行的秒数(/proc/uptime),算 systemd 服务的运行时长用;nil 则不给运行时长。
	ProcUptime func() (float64, error)
	// StatusTimeout / ActionTimeout 为 0 时用默认值(10 秒 / 60 秒)。
	StatusTimeout time.Duration
	ActionTimeout time.Duration
}

func (s *Services) statusTimeout() time.Duration {
	if s.StatusTimeout > 0 {
		return s.StatusTimeout
	}
	return 10 * time.Second
}

func (s *Services) actionTimeout() time.Duration {
	if s.ActionTimeout > 0 {
		return s.ActionTimeout
	}
	return 60 * time.Second
}

func (s *Services) run(ctx context.Context, timeout time.Duration, name string, args ...string) (RunResult, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := s.Run(cctx, name, args...)
	if err != nil && cctx.Err() != nil {
		return res, fmt.Errorf("命令超时(%s): %s", timeout, name)
	}
	return res, err
}

// Status 查询服务状态。服务不存在、没在跑都不是错误,返回 Running=false。
func (s *Services) Status(ctx context.Context, name string) (ServiceInfo, error) {
	if err := ValidServiceName(name); err != nil {
		return ServiceInfo{}, err
	}
	switch s.Kind {
	case InitOpenWrt:
		script := "/etc/init.d/" + name
		run, err := s.run(ctx, s.statusTimeout(), script, "running")
		if err != nil {
			return ServiceInfo{}, fmt.Errorf("查询服务状态失败: %w", err)
		}
		info := ServiceInfo{Running: run.Code == 0, Raw: "inactive"}
		if info.Running {
			info.Raw = "running"
		}
		if en, err := s.run(ctx, s.statusTimeout(), script, "enabled"); err == nil {
			v := en.Code == 0
			info.Autostart = &v
		}
		return info, nil
	case InitSystemd:
		act, err := s.run(ctx, s.statusTimeout(), "systemctl", "is-active", name)
		if err != nil {
			return ServiceInfo{}, fmt.Errorf("查询服务状态失败: %w", err)
		}
		raw := strings.TrimSpace(act.Stdout)
		if raw == "" {
			raw = "unknown"
		}
		info := ServiceInfo{Running: raw == "active", Raw: raw}
		if en, err := s.run(ctx, s.statusTimeout(), "systemctl", "is-enabled", name); err == nil {
			v := en.Code == 0
			info.Autostart = &v
		}
		if info.Running && s.ProcUptime != nil {
			if sh, err := s.run(ctx, s.statusTimeout(), "systemctl", "show", name, "--property=ActiveEnterTimestampMonotonic", "--value"); err == nil {
				if us, err := strconv.ParseInt(strings.TrimSpace(sh.Stdout), 10, 64); err == nil && us > 0 {
					if up, err := s.ProcUptime(); err == nil {
						if secs := int64(up) - us/1_000_000; secs >= 0 {
							info.UptimeSeconds = &secs
						}
					}
				}
			}
		}
		return info, nil
	}
	return ServiceInfo{}, ErrNoInit
}

// Do 对服务执行 start / stop / restart / enable / disable。命令失败不返回 error,
// 而是 OK=false 并带上退出码和 stderr,让界面直接显示。
func (s *Services) Do(ctx context.Context, name, action string) (ActionResult, error) {
	if err := ValidServiceName(name); err != nil {
		return ActionResult{}, err
	}
	if !Actions[action] {
		return ActionResult{}, ErrBadAction
	}
	var cmd string
	var args []string
	switch s.Kind {
	case InitOpenWrt:
		cmd, args = "/etc/init.d/"+name, []string{action}
	case InitSystemd:
		cmd, args = "systemctl", []string{action, name}
	default:
		return ActionResult{}, ErrNoInit
	}
	t0 := time.Now()
	res, err := s.run(ctx, s.actionTimeout(), cmd, args...)
	if err != nil {
		return ActionResult{OK: false, Code: -1, Stderr: err.Error(), DurationMs: time.Since(t0).Milliseconds()}, nil
	}
	return ActionResult{OK: res.Code == 0, Code: res.Code, Stderr: strings.TrimSpace(res.Stderr), DurationMs: time.Since(t0).Milliseconds()}, nil
}

// ReadProcUptime 读 /proc/uptime 的第一个数(系统已运行的秒数)。
func ReadProcUptime() (float64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, errors.New("/proc/uptime 为空")
	}
	return strconv.ParseFloat(f[0], 64)
}
