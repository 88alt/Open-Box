// Package diagnostics 实现 GET /api/openbox/diagnostics:把版本、固件、服务状态、脱敏后的运行配置和档案、
// 最近日志打成一份 JSON,反馈问题时一键附上。内容格式前端没有规定(浏览器只是把它存成文件),是自己设计的,
// 没有参照上游。
//
// 每一块独立收集:某一块读不到只在那一块里写明原因,不影响其余;整个过程只读,不改系统任何状态。
// 凭据和订阅链接的脱敏见 redact.go;诊断包由用户自己下载、检查后再发出去。
package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

const (
	// LogLines 每个服务最多留最近多少行日志。
	LogLines = 200
	// LogCap 读系统日志时命令输出最多留多少字节(取最后 LogLines 行前先全读进来过滤)。
	LogCap      = 1 << 20
	maxLineLen  = 400
	maxFileRead = 16 << 20
)

// Service 收集诊断包。所有依赖都可以换成假的。
type Service struct {
	Version       string
	CoreService   string // 内核服务名(如 sing-box)
	PanelService  string // 本后端的服务名
	ConfigFile    string // 正在使用的内核配置
	Init          system.InitKind
	KernelVersion func(ctx context.Context) string
	ServiceStatus func(ctx context.Context, name string) (system.ServiceInfo, error)
	Profile       func(ctx context.Context) (map[string]any, error)
	// ReadFile 读系统文件(/etc/openwrt_release、/proc/…);nil = os.ReadFile。
	ReadFile func(path string) ([]byte, error)
	// Run 运行命令读日志(输出要够长,见 LogCap);nil = system.ExecRunnerCap(LogCap)。
	Run system.Runner
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) read(p string) ([]byte, error) {
	if s.ReadFile != nil {
		return s.ReadFile(p)
	}
	return os.ReadFile(p)
}

func (s *Service) run() system.Runner {
	if s.Run != nil {
		return s.Run
	}
	return system.ExecRunnerCap(LogCap)
}

func errMap(err error) map[string]any { return map[string]any{"error": err.Error()} }

// Collect 收集一份诊断包。
func (s *Service) Collect(ctx context.Context) map[string]any {
	out := map[string]any{
		"generatedAt": s.now().UTC().Format(time.RFC3339),
		"redacted":    true,
		"note":        "凭据、节点地址、订阅链接已脱敏;发出去之前请自己再看一遍,日志里可能有你访问过的域名。",
		"backend":     map[string]any{"version": s.Version, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH},
		"firmware":    s.firmware(),
		"services":    s.services(ctx),
		"config":      s.config(),
		"profile":     s.profile(ctx),
		"logs":        s.logs(ctx),
	}
	kernel := map[string]any{}
	if s.KernelVersion != nil {
		kernel["version"] = s.KernelVersion(ctx)
	}
	out["kernel"] = kernel
	return out
}

var kvRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// parseKV 解析 KEY='value' / KEY="value" / KEY=value 形式的文件(/etc/openwrt_release、/etc/os-release)。
func parseKV(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		m := kvRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := strings.TrimSpace(m[2])
		if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[m[1]] = v
	}
	return out
}

func (s *Service) firmware() map[string]any {
	fw := map[string]any{"init": string(s.Init)}
	if b, err := s.read("/etc/openwrt_release"); err == nil {
		kv := parseKV(b)
		fw["system"] = "openwrt"
		fw["description"], fw["release"], fw["target"] = kv["DISTRIB_DESCRIPTION"], kv["DISTRIB_RELEASE"], kv["DISTRIB_TARGET"]
	} else if b, err := s.read("/etc/os-release"); err == nil {
		kv := parseKV(b)
		fw["system"] = kv["ID"]
		fw["description"], fw["release"] = kv["PRETTY_NAME"], kv["VERSION_ID"]
	}
	if b, err := s.read("/proc/version"); err == nil {
		fw["kernel"] = strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	}
	if b, err := s.read("/proc/meminfo"); err == nil {
		mem := map[string]string{}
		for _, line := range strings.Split(string(b), "\n") {
			for _, k := range []string{"MemTotal", "MemAvailable", "SwapTotal"} {
				if strings.HasPrefix(line, k+":") {
					mem[k] = strings.TrimSpace(strings.TrimPrefix(line, k+":"))
				}
			}
		}
		fw["memory"] = mem
	}
	if up, err := system.ReadProcUptime(); err == nil {
		fw["uptimeSeconds"] = int64(up)
	}
	return fw
}

func (s *Service) services(ctx context.Context) map[string]any {
	one := func(name string) any {
		if name == "" || s.ServiceStatus == nil {
			return map[string]any{"error": "没有配置"}
		}
		info, err := s.ServiceStatus(ctx, name)
		if err != nil {
			return map[string]any{"name": name, "error": err.Error()}
		}
		m := map[string]any{"name": name, "running": info.Running, "raw": ScrubString(info.Raw)}
		if info.Autostart != nil {
			m["autostart"] = *info.Autostart
		}
		if info.UptimeSeconds != nil {
			m["uptimeSeconds"] = *info.UptimeSeconds
		}
		return m
	}
	return map[string]any{"core": one(s.CoreService), "panel": one(s.PanelService)}
}

func (s *Service) config() any {
	if s.ConfigFile == "" {
		return map[string]any{"error": "没有配置内核配置文件的路径"}
	}
	info := map[string]any{"file": s.ConfigFile}
	if st, err := os.Stat(s.ConfigFile); err == nil {
		if st.Size() > maxFileRead {
			info["sizeBytes"] = st.Size()
			info["error"] = "配置文件太大,没有收集"
			return info
		}
		info["modifiedAt"] = st.ModTime().UTC().Format(time.RFC3339)
	}
	b, err := s.read(s.ConfigFile)
	if err != nil {
		info["error"] = "读不到当前配置:" + err.Error()
		return info
	}
	info["sizeBytes"] = len(b)
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		info["error"] = "当前配置不是合法 JSON(内容没有收集,以免漏掉脱敏)"
		return info
	}
	info["content"] = Redact(v)
	return info
}

func (s *Service) profile(ctx context.Context) any {
	if s.Profile == nil {
		return map[string]any{"error": "没有配置"}
	}
	p, err := s.Profile(ctx)
	if err != nil {
		return errMap(err)
	}
	return Redact(p)
}

func (s *Service) logs(ctx context.Context) map[string]any {
	return map[string]any{"core": s.serviceLog(ctx, s.CoreService), "panel": s.serviceLog(ctx, s.PanelService)}
}

// serviceLog 取服务最近的日志:OpenWrt 用 logread 按服务名过滤,systemd 用 journalctl。
// 每行都过一遍 ScrubString;读不到只回原因。
func (s *Service) serviceLog(ctx context.Context, name string) any {
	if err := system.ValidServiceName(name); err != nil {
		return map[string]any{"error": "服务名不合法,没有读日志"}
	}
	var res system.RunResult
	var err error
	switch s.Init {
	case system.InitOpenWrt:
		res, err = s.run()(ctx, "logread", "-e", regexp.QuoteMeta(name))
	case system.InitSystemd:
		res, err = s.run()(ctx, "journalctl", "-u", name, "-n", "500", "--no-pager", "-o", "short-iso")
	default:
		return map[string]any{"error": "没有识别到服务管理器,读不了日志"}
	}
	if err != nil {
		return map[string]any{"error": "运行日志命令失败:" + err.Error()}
	}
	if res.Code != 0 && strings.TrimSpace(res.Stdout) == "" {
		return map[string]any{"error": "日志命令退出码 " + strconv.Itoa(res.Code) + ":" + ScrubString(strings.TrimSpace(res.Stderr))}
	}
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if len(lines) > LogLines {
		lines = lines[len(lines)-LogLines:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = ScrubString(strings.TrimRight(l, "\r"))
		if r := []rune(l); len(r) > maxLineLen {
			l = string(r[:maxLineLen]) + "…"
		}
		out = append(out, l)
	}
	return out
}
