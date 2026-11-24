// Package kernelswitch 是双内核切换的流程编排:切换前预检(第 4 步)、切换本身(第 5 步)。
//
// 预检的 8 项、固定顺序、四种状态都与上游 v0.1.482 的 `hh` 一致:
// target → system → conflicts → compatibility → rulesets → config → firewall → dnsGuard;
// 前面有一项 failed,rulesets / config / firewall / dnsGuard 回 blocked。
//
// 差异(都在 STATUS 里记了「推断点」):
//   - firewall:sing-box 一侧没有入口 nft,回 notApplicable;Mihomo 一侧有入口 nft 时才用 `nft -c -f` 校验。
//   - dnsGuard:上游是临时写一份 nft 防泄露规则再校验;这里交给 dnssync(切换时先撤销 dnsmasq 接管,
//     新内核起来后再对齐),没有接管时回 notApplicable。
package kernelswitch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/timeflysoon/Open-Box/backend/internal/kernelgen"
	"github.com/timeflysoon/Open-Box/backend/internal/mihomo"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// 状态只有这四种(与上游一致)。
const (
	StatusPassed        = "passed"
	StatusFailed        = "failed"
	StatusBlocked       = "blocked"
	StatusNotApplicable = "notApplicable"
)

// Order 是固定的 8 项顺序。
var Order = []string{"target", "system", "conflicts", "compatibility", "rulesets", "config", "firewall", "dnsGuard"}

// Check 是一项检查的结果。
type Check struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Details []string `json:"details,omitempty"`
}

// Deps 是预检要用的依赖。
type Deps struct {
	KernelFile string
	SingBox    string
	Mihomo     string
	InitdCore  string // 内核服务脚本(OpenWrt 的 /etc/init.d/openbox,Debian 的 systemd unit)
	DataDir    string
	Platform   string
	TunPath    string // 默认 /dev/net/tun

	Gen *kernelgen.Generator // 目标配置的生成与校验
	Sbx *singbox.Generator   // sing-box 配置生成

	// Conflicts 返回正在运行的其它代理工具的名字;为空 = 不检查。
	Conflicts func(ctx context.Context) ([]string, error)
	// Compat 返回 Mihomo 表达不了的问题(含 profile 里缺的规则集);为空 = 没有兼容性问题。
	Compat func(ctx context.Context) ([]string, error)
	// DNSGuard 告诉预检这次切换要不要处理 DNS 接管;applicable=false 回 notApplicable。
	DNSGuard func(ctx context.Context) (applicable bool, details []string, err error)
	// Version 读目标内核的版本行(如「Mihomo Meta v1.19.0 …」/「sing-box version 1.14.4」),
	// 第二个返回值是「跑得起来吗」。为空 = 真去跑内核的 -v / version。
	Version func(ctx context.Context, kernel string) (string, bool)
}

// Engine 跑预检。
type Engine struct{ d Deps }

// New 创建引擎。
func New(d Deps) *Engine {
	if d.TunPath == "" {
		d.TunPath = "/dev/net/tun"
	}
	return &Engine{d: d}
}

// Preflight 按固定顺序跑 8 项检查。onCheck 每完成一项调用一次(可为 nil)。
// 返回的切片永远是 8 项、按 Order 排列(没跑到的回 blocked)。
func (e *Engine) Preflight(ctx context.Context, kernel string, onCheck func(Check)) []Check {
	if !system.ValidKernel(kernel) {
		kernel = system.KernelSingBox
	}
	results := map[string]Check{}
	emit := func(c Check) {
		results[c.ID] = c
		if onCheck != nil {
			onCheck(c)
		}
	}
	// step 跑一项:返回 true 表示通过或不适用的「可以继续」;失败吞掉错误继续下一项(与上游一致)。
	step := func(id string, fn func() ([]string, string, error)) bool {
		details, status, err := safeRun(ctx, fn)
		if err != nil {
			if status == "" {
				status = StatusFailed
			}
			if len(details) == 0 {
				details = []string{err.Error()}
			}
			emit(Check{ID: id, Status: status, Details: details})
			return false
		}
		if status == "" {
			status = StatusPassed
		}
		emit(Check{ID: id, Status: status, Details: details})
		return true
	}

	// 1. target:目标内核与当前不同,且可执行、版本对得上。
	step("target", func() ([]string, string, error) {
		cur, err := system.ReadRouterKernel(e.d.KernelFile)
		if err != nil {
			return nil, "", err
		}
		if cur == kernel {
			return nil, "", fmt.Errorf("目标内核已经是当前内核")
		}
		if kernel == system.KernelMihomo {
			if !fileExists(e.d.Mihomo) {
				return nil, "", fmt.Errorf("当前安装包没有 mihomo 内核")
			}
			v, ok := e.version(ctx, kernel)
			if !ok || !mihomoVersionRe.MatchString(v) {
				return []string{v}, "", fmt.Errorf("mihomo 内核无法运行")
			}
			return []string{v}, "", nil
		}
		if !fileExists(e.d.SingBox) {
			return nil, "", fmt.Errorf("当前安装包没有 sing-box 内核")
		}
		v, ok := e.version(ctx, kernel)
		if !ok || !singboxVersionRe.MatchString(v) {
			return []string{v}, "", fmt.Errorf("sing-box 内核无法运行")
		}
		return []string{v}, "", nil
	})

	// 2. system:TUN 设备与服务脚本。
	step("system", func() ([]string, string, error) {
		if _, err := os.Stat(e.d.TunPath); err != nil {
			return nil, "", fmt.Errorf("系统没有 TUN 设备(%s)", e.d.TunPath)
		}
		if e.d.InitdCore != "" && !fileExists(e.d.InitdCore) {
			return nil, "", fmt.Errorf("缺少 Open-Box 内核服务脚本")
		}
		return []string{e.d.Platform}, "", nil
	})

	// 3. conflicts:别的代理工具在跑就不切。
	step("conflicts", func() ([]string, string, error) {
		if e.d.Conflicts == nil {
			return nil, "", nil
		}
		names, err := e.d.Conflicts(ctx)
		if err != nil {
			return nil, "", err
		}
		if len(names) > 0 {
			return nil, "", conflictError(names)
		}
		return nil, "", nil
	})

	// 4. compatibility:切到 Mihomo 时查 core 的兼容性(切到 sing-box 不适用)。
	step("compatibility", func() ([]string, string, error) {
		if kernel != system.KernelMihomo {
			return nil, StatusNotApplicable, nil
		}
		if e.d.Compat == nil {
			return nil, "", nil
		}
		problems, err := e.d.Compat(ctx)
		if err != nil {
			return nil, "", err
		}
		if len(problems) > 0 {
			return problems, "", fmt.Errorf("Mihomo 兼容性检查没通过")
		}
		return nil, "", nil
	})

	// 5~7. rulesets → config → firewall:只有 target / conflicts 通过、compatibility 没失败时才有意义。
	targetOK := results["target"].Status == StatusPassed
	conflictsOK := results["conflicts"].Status == StatusPassed
	compatOK := results["compatibility"].Status != StatusFailed
	var built *mihomo.BuildResult
	if targetOK && conflictsOK && compatOK {
		if kernel == system.KernelMihomo {
			step("rulesets", func() ([]string, string, error) {
				if e.d.Gen == nil {
					return nil, "", fmt.Errorf("没有 Mihomo 配置生成器")
				}
				d, err := e.d.Gen.MihomoRulesets(ctx)
				return d, "", err
			})
			step("config", func() ([]string, string, error) {
				if e.d.Gen == nil {
					return nil, "", fmt.Errorf("没有 Mihomo 配置生成器")
				}
				res, err := e.d.Gen.MihomoBuild(ctx)
				if err != nil {
					return nil, "", err
				}
				built = res
				d, err := e.d.Gen.MihomoValidate(ctx, res)
				return d, "", err
			})
			step("firewall", func() ([]string, string, error) {
				details, ok, err := e.d.Gen.MihomoFirewall(ctx, built)
				if err != nil {
					return details, "", err
				}
				if !ok {
					return nil, StatusNotApplicable, nil
				}
				return details, "", nil
			})
		} else {
			var cfg *singbox.Built
			step("rulesets", func() ([]string, string, error) {
				if e.d.Gen == nil || e.d.Sbx == nil {
					return nil, "", fmt.Errorf("没有 sing-box 配置生成器")
				}
				b, err := e.d.Gen.SingboxBuild(ctx, e.d.Sbx)
				if err != nil {
					return nil, "", err
				}
				cfg = b
				d, err := e.d.Gen.SingboxRulesets(b)
				return d, "", err
			})
			step("config", func() ([]string, string, error) {
				if e.d.Gen == nil || e.d.Sbx == nil {
					return nil, "", fmt.Errorf("没有 sing-box 配置生成器")
				}
				if cfg == nil {
					b, err := e.d.Gen.SingboxBuild(ctx, e.d.Sbx)
					if err != nil {
						return nil, "", err
					}
					cfg = b
				}
				d, err := e.d.Gen.SingboxValidate(ctx, cfg)
				return d, "", err
			})
			// sing-box 这一侧不生成入口 nft(见包注释)。
			emit(Check{ID: "firewall", Status: StatusNotApplicable})
		}
	}

	// 没跑到的 rulesets / config / firewall 补成 blocked(与上游一样,流式也会写出去)。
	for _, id := range []string{"rulesets", "config", "firewall"} {
		if _, ok := results[id]; !ok {
			emit(Check{ID: id, Status: StatusBlocked})
		}
	}

	// 8. dnsGuard:切换期间的 DNS 防泄露。
	blocked := results["target"].Status != StatusPassed || results["conflicts"].Status != StatusPassed ||
		results["system"].Status != StatusPassed || results["compatibility"].Status == StatusFailed
	if blocked {
		emit(Check{ID: "dnsGuard", Status: StatusBlocked})
	} else {
		step("dnsGuard", func() ([]string, string, error) {
			if e.d.DNSGuard == nil {
				return nil, StatusNotApplicable, nil
			}
			ok, details, err := e.d.DNSGuard(ctx)
			if err != nil {
				return details, "", err
			}
			if !ok {
				return nil, StatusNotApplicable, nil
			}
			return details, "", nil
		})
	}

	out := make([]Check, 0, len(Order))
	for _, id := range Order {
		if c, ok := results[id]; ok {
			out = append(out, c)
		} else {
			out = append(out, Check{ID: id, Status: StatusBlocked})
		}
	}
	return out
}

var (
	mihomoVersionRe  = regexp.MustCompile(`(?i)^Mihomo\s`)
	singboxVersionRe = regexp.MustCompile(`^sing-box\s+version\s`)
)

// version 读目标内核的版本行;测试里用 Deps.Version 换掉。
func (e *Engine) version(ctx context.Context, kernel string) (string, bool) {
	if e.d.Version != nil {
		return e.d.Version(ctx, kernel)
	}
	if kernel == system.KernelMihomo {
		v := singbox.ReadMihomoVersion(ctx, e.d.Mihomo)
		return v.Version, v.OK
	}
	v := singbox.ReadKernelVersion(ctx, e.d.SingBox)
	if v.Version == "" {
		return describeVersion(v), false
	}
	return v.Version, v.OK
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func describeVersion(v singbox.KernelVersion) string {
	if v.Version != "" {
		return v.Version
	}
	if v.Raw != "" {
		return v.Raw
	}
	return "读不到版本"
}

// conflictError 与上游 `ri` 的文案一致。
func conflictError(names []string) error {
	joined := joinCN(names)
	return fmt.Errorf("检测到 %s 正在运行。同一台路由器上不能同时运行两个代理工具,会互相冲突,Open-Box 不启动内核。请先停用 %s(关掉它的主开关或卸载)再启动。", joined, joined)
}

func joinCN(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += "、"
		}
		out += n
	}
	return out
}

func safeRun(ctx context.Context, fn func() ([]string, string, error)) (details []string, status string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, StatusFailed, err
	}
	defer func() {
		if r := recover(); r != nil {
			details, status, err = nil, StatusFailed, fmt.Errorf("%v", r)
		}
	}()
	return fn()
}

// ServiceScriptPath 给出默认的内核服务脚本路径(测试与接线共用)。
func ServiceScriptPath(kind system.InitKind, platform string) string {
	if kind == system.InitOpenWrt {
		return "/etc/init.d/openbox"
	}
	if platform == "debian" {
		return filepath.Join("/etc/systemd/system", "openbox.service")
	}
	return ""
}
