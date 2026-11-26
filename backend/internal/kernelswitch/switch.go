package kernelswitch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/dnssync"
	"github.com/timeflysoon/Open-Box/backend/internal/kernelgen"
	"github.com/timeflysoon/Open-Box/backend/internal/mihomo"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// SwitchDeps 是切换内核要用的依赖(预检之外的部分)。
type SwitchDeps struct {
	Engine  *Engine
	Gen     *kernelgen.Generator
	Sbx     *singbox.Generator
	SbxBin  string
	Profile func(ctx context.Context) (map[string]any, error)

	Services    Service
	ServiceName string
	DNS         DNSSync

	KernelFile    string
	SingBoxConfig string // <ConfigDir>/config.json
	MihomoPaths   mihomo.ArtifactPaths
	FS            mihomo.FS
	Platform      string
	GeoDir        string
	// SaveSelections 在切到 Mihomo 成功之后存下这次装配出的选中项(下次内核没在跑时照它装配);可为 nil。
	SaveSelections func(map[string]any) error
	Now            func() time.Time
}

// Service 是内核服务控制(*system.Services 满足)。
type Service interface {
	Status(ctx context.Context, name string) (system.ServiceInfo, error)
	Do(ctx context.Context, name, action string) (system.ActionResult, error)
}

// DNSSync 是 DNS 接管的撤销与对齐(*dnssync.Coordinator 满足)。
type DNSSync interface {
	BeforeKernelStop(ctx context.Context) error
	AfterKernelStart(ctx context.Context) (dnssync.Result, error)
}

// Result 是 POST /api/openbox/kernel/switch 的响应(与上游 `nn` 的返回一致)。
type Result struct {
	OK      bool   `json:"ok"`
	Code    int    `json:"code"`
	Stderr  string `json:"stderr"`
	Warning string `json:"warning,omitempty"`
}

// Switch 切到目标内核:预检 → 生成目标配置 → 撤 DNS 接管 → 停旧内核 → 写 router-kernel → 起新内核 → 确认在跑。
// 任何一步失败都回到原内核与原配置,并把原因写进 stderr。
func (d SwitchDeps) Switch(ctx context.Context, kernel string) Result {
	if !system.ValidKernel(kernel) {
		return Result{Code: 1, Stderr: "不支持的路由器内核"}
	}
	if d.Engine == nil || d.Gen == nil || d.Sbx == nil || d.Services == nil || d.ServiceName == "" {
		return Result{Code: 1, Stderr: "内核切换没有配置完整"}
	}
	// 切到 sing-box 之前,先把 sing-box 那份配置引用的本地规则集写到磁盘。
	// 为什么必须在预检之前:这些产物平时只有「部署 sing-box」时才写,而设备一直在跑 mihomo,
	// 所以 data/rules 里可能一个文件都没有;预检的 rulesets / config 两项会因此稳定失败,
	// 而预检不过就永远走不到 stage() 里那段写文件的代码 —— 死锁,真机上切 sing-box 因此不可能成功
	// (2026-11-26 真机:预检报 geosite-cn / geoip-cn 不可用,而 data/rules 整个目录都不存在)。
	// 写产物这一步是幂等的,而且此时还没停旧内核、没动 router-kernel,失败就直接返回、不影响现状。
	if kernel == system.KernelSingBox && d.GeoDir != "" {
		pre, err := d.Gen.SingboxBuild(ctx, d.Sbx)
		if err != nil {
			return Result{Code: 1, Stderr: "生成 sing-box 配置失败: " + err.Error()}
		}
		rep, err := (singbox.Files{SingBox: d.SbxBin, GeoDir: d.GeoDir}).WriteLocalSetsFlip(ctx, pre.Config, pre.DirectHosts, pre.FlipFlags)
		if err != nil {
			return Result{Code: 1, Stderr: "准备规则集文件被中断: " + err.Error()}
		}
		if !rep.OK() {
			var why []string
			for _, m := range rep.Missing {
				why = append(why, m.Tag+"("+m.Reason+")")
			}
			return Result{Code: 1, Stderr: "规则集还没准备好,未切换: " + strings.Join(why, "、")}
		}
	}
	if msg := failedReason(d.Engine.Preflight(ctx, kernel, nil)); msg != "" {
		return Result{Code: 1, Stderr: msg}
	}
	prev, err := system.ReadRouterKernel(d.KernelFile)
	if err != nil {
		return Result{Code: 1, Stderr: err.Error()}
	}

	// 1. 生成目标配置并准备好「写哪些文件」;生成失败时还没有碰任何东西。
	apply, paths, onSuccess, err := d.stage(ctx, kernel)
	if err != nil {
		return Result{Code: 1, Stderr: err.Error()}
	}
	// 2. 备份要改的文件(内存里),失败时写回。
	bak := d.backup(paths)
	// 3. 撤 DNS 接管:内核停了而 dnsmasq 还指着它,局域网会没有 DNS。
	if d.DNS != nil {
		if err := d.DNS.BeforeKernelStop(ctx); err != nil {
			return Result{Code: 1, Stderr: "为安全起见没有切换内核: " + err.Error()}
		}
	}
	restore := func() []string {
		var problems []string
		if err := bak.restore(); err != nil {
			problems = append(problems, "恢复配置文件失败: "+err.Error())
		}
		if err := writeKernelFile(d.KernelFile, prev); err != nil {
			problems = append(problems, "恢复内核选择失败: "+err.Error())
		}
		if res, err := d.Services.Do(ctx, d.ServiceName, "restart"); err != nil || !res.OK {
			problems = append(problems, "重启 "+prev+" 失败: "+describe(res, err))
		}
		return problems
	}
	// 4. 写目标配置 → 停旧 → 写 router-kernel → 起新。
	if err := apply(); err != nil {
		if rerr := bak.restore(); rerr != nil {
			return Result{Code: 1, Stderr: "写入目标内核配置失败,且没能恢复原配置: " + rerr.Error()}
		}
		return Result{Code: 1, Stderr: "写入目标内核配置失败,没有改动内核: " + err.Error()}
	}
	if res, err := d.Services.Do(ctx, d.ServiceName, "stop"); err != nil || !res.OK {
		return Result{Code: 1, Stderr: joinLines("停止原内核失败,未切换: "+describe(res, err), restore())}
	}
	if err := writeKernelFile(d.KernelFile, kernel); err != nil {
		return Result{Code: 1, Stderr: joinLines("写入内核选择失败: "+err.Error(), restore())}
	}
	if res, err := d.Services.Do(ctx, d.ServiceName, "restart"); err != nil || !res.OK {
		return Result{Code: 1, Stderr: joinLines("启动目标内核失败: "+describe(res, err), restore())}
	}
	if info, err := d.Services.Status(ctx, d.ServiceName); err != nil || !info.Running {
		return Result{Code: 1, Stderr: joinLines("目标内核没有在运行: "+describeStatus(info, err), restore())}
	}
	warning := ""
	if d.DNS != nil {
		got, derr := d.DNS.AfterKernelStart(ctx)
		warning = dnssync.Note(got, derr)
	}
	// 切换成功后记下这一份配置里各组的选择,下次装配(内核没在跑时)照它来。
	if onSuccess != nil {
		if err := onSuccess(); err != nil {
			warning = joinLines(warning, []string{"保存选中项失败: " + err.Error()})
		}
	}
	return Result{OK: true, Code: 0, Warning: warning}
}

func describeStatus(info system.ServiceInfo, err error) string {
	if err != nil {
		return err.Error()
	}
	if info.Raw != "" {
		return info.Raw
	}
	return "服务状态不是 running"
}

func describe(res system.ActionResult, err error) string {
	if err != nil {
		return err.Error()
	}
	if res.Stderr != "" {
		return res.Stderr
	}
	return fmt.Sprintf("退出码 %d", res.Code)
}

func joinLines(head string, more []string) string {
	parts := []string{}
	if head != "" {
		parts = append(parts, head)
	}
	for _, m := range more {
		if m != "" {
			parts = append(parts, m)
		}
	}
	return strings.Join(parts, ";")
}

// failedReason 把预检的失败项拼成一句给人看的原因。
func failedReason(checks []Check) string {
	var bad []string
	for _, c := range checks {
		if c.Status != StatusFailed {
			continue
		}
		detail := strings.Join(c.Details, ";")
		if detail == "" {
			detail = "检查没通过"
		}
		bad = append(bad, c.ID+": "+detail)
	}
	if len(bad) == 0 {
		return ""
	}
	return "切换前预检没通过:" + strings.Join(bad, " | ")
}

// stage 生成目标内核的配置,返回「写文件」的动作、要备份的路径,以及切换成功后才做的事(可为 nil)。
// 生成失败时什么都不写。
func (d SwitchDeps) stage(ctx context.Context, kernel string) (apply func() error, paths []string, onSuccess func() error, err error) {
	if kernel == system.KernelMihomo {
		profile, err := d.Profile(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		res, err := d.Gen.MihomoBuild(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("生成 mihomo 配置失败: %w", err)
		}
		dnsMode, _ := nestedString(profile, "dns", "mode")
		meta := mihomo.ArtifactMeta{
			Kernel: "mihomo", Failover: res.Failover, NodeDirectResolution: res.NodeDirectResolution,
			DNSMasq: d.Platform == "openwrt" && dnsMode == "dnsmasq", GeneratedAt: d.now(),
		}
		paths := []string{d.MihomoPaths.Config, d.MihomoPaths.Inputs, d.MihomoPaths.EntryBypass, d.MihomoPaths.DNSMasq}
		fs := d.fs()
		var save func() error
		if d.SaveSelections != nil {
			save = func() error { return d.SaveSelections(res.SelectionsAny()) }
		}
		return func() error {
			return mihomo.WriteArtifacts(fs, d.MihomoPaths, res, meta)
		}, paths, save, nil
	}
	// 切到 sing-box:生成 → 写本地规则集 → 写 config.json。
	built, err := d.Gen.SingboxBuild(ctx, d.Sbx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("生成 sing-box 配置失败: %w", err)
	}
	if rep, err := (singbox.Files{SingBox: d.SbxBin, GeoDir: d.GeoDir}).WriteLocalSetsFlip(ctx, built.Config, built.DirectHosts, built.FlipFlags); err != nil {
		return nil, nil, nil, err
	} else if !rep.OK() {
		var why []string
		for _, m := range rep.Missing {
			why = append(why, m.Tag+"("+m.Reason+")")
		}
		return nil, nil, nil, fmt.Errorf("规则集还没准备好,未切换: %s", strings.Join(why, "、"))
	}
	data, err := built.JSON()
	if err != nil {
		return nil, nil, nil, err
	}
	return func() error { return d.fs().WriteFile(d.SingBoxConfig, data, 0o600) }, []string{d.SingBoxConfig}, nil, nil
}

func (d SwitchDeps) fs() mihomo.FS {
	if d.FS != nil {
		return d.FS
	}
	return mihomo.OSFS{}
}

func (d SwitchDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// saved 是切换前的一份文件。
type saved struct {
	path    string
	data    []byte
	existed bool
}

type backup []saved

func (d SwitchDeps) backup(paths []string) backup {
	fs := d.fs()
	out := backup{}
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		b, err := fs.ReadFile(p)
		out = append(out, saved{path: p, data: b, existed: err == nil})
	}
	return out
}

func (b backup) restore() error {
	var problems []string
	for i := len(b) - 1; i >= 0; i-- {
		f := b[i]
		if !f.existed {
			if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				problems = append(problems, f.path+": "+err.Error())
			}
			continue
		}
		if err := appendWrite(f.path, f.data); err != nil {
			problems = append(problems, f.path+": "+err.Error())
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, ";"))
	}
	return nil
}

// appendWrite 原子写(同目录临时文件 → 改名),0600。
func appendWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".kernel-switch-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func writeKernelFile(path, kernel string) error {
	if path == "" {
		return nil
	}
	return appendWrite(path, []byte(kernel+"\n"))
}

func nestedString(m map[string]any, keys ...string) (string, bool) {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur = mm[k]
	}
	s, ok := cur.(string)
	return s, ok
}
