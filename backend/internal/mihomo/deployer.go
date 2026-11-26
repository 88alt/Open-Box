package mihomo

// 部署 mihomo:生成 → 写 mihomo.yaml 与伴生文件 → `mihomo -t` 校验 → 原子替换 → 重启 → 健康检查 → 失败回滚。
//
// 与 sing-box 那条流程(internal/singbox.Deployer)是**并列**的,不是替换:两条流程各自的配置文件名、
// 控制接口地址与密钥都不同,由调用方按 <ConfigDir>/router-kernel 选一条走(见 internal/api/deploy 的
// KernelDeployer)。结构照 sing-box 那套来,理由一样:先校验再应用、替换前留一份旧配置、重启后确认
// 控制接口真的答话,否则换回旧的再重启,并如实告诉调用方「回滚了」还是「回滚也失败了」。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// 阶段名与 sing-box 那条一致(前端 OpenboxDeployResult.stage 只有这几个值)。
const (
	MihomoStageRunning  = "running"
	MihomoStageConflict = "conflict"
	MihomoStageValidate = "validate"
	MihomoStageStart    = "start"
	MihomoStageVerify   = "verify"
	MihomoStageError    = "error"
)

// DeployResult 与 singbox.DeployResult 同形(前端只认这个形状)。
type DeployResult struct {
	OK         bool     `json:"ok"`
	Stage      string   `json:"stage"`
	Message    string   `json:"message"`
	BadTags    []string `json:"badTags"`
	RolledBack bool     `json:"rolledBack,omitempty"`
	Busy       bool     `json:"-"`
}

// ServiceControl 是用到的服务管理(*system.Services 满足),与 sing-box 那条同一个接口形状。
type ServiceControl interface {
	Status(ctx context.Context, name string) (system.ServiceInfo, error)
	Do(ctx context.Context, name, action string) (system.ActionResult, error)
}

// Builder 装配一份 mihomo 配置(*kernelgen.Generator 的 MihomoBuild 经适配后满足)。
type Builder interface {
	BuildMihomo(ctx context.Context) (*BuildResult, error)
}

// Deployer 做一次完整的 mihomo 部署。零值字段用默认值:HealthTimeout 20 秒,StableFor 3 秒,Poll 500 毫秒。
type Deployer struct {
	Build  Builder
	FS     FS
	Paths  ArtifactPaths
	Meta   ArtifactMeta // Kernel 留空时补 "mihomo"
	Mihomo string       // mihomo 可执行文件(-t 校验与健康检查都用它)
	Home   string       // mihomo 的工作目录(-d)
	// Platform 用来判断 OpenWrt 的 dnsmasq 伴生文件要不要写。
	Platform string

	Service     ServiceControl
	ServiceName string

	// Conflicts 与 sing-box 那条同义:内核当前没在运行、这次要从停止状态启动时才问。
	Conflicts func(ctx context.Context) ([]string, error)
	// BeforeApply / AfterRunning 与 sing-box 那条同义(DNS 接管的撤销与对齐)。
	BeforeApply  func(ctx context.Context) error
	AfterRunning func(ctx context.Context) string
	// SaveSelections 非 nil:部署成功后存下这次装配出的选中项。
	SaveSelections func(map[string]any) error
	// Track 记录「这份配置是用哪些输入生成的」(与 sing-box 那条同义)。
	// 之前 mihomo 这条完全没有它,于是在 mihomo 内核下部署成功后 pendingRestart 永远清不掉
	// ——真机 2026-11-26:改 testUrl 后 pending:true,部署成功仍 pending:true(reasons:[nodes])。
	Track singbox.Tracker
	// SavePlan 非 nil:部署成功(内核健康)后调用,把这份配置对应的故障转移计划存下来
	// (与 sing-box 那条同义)。之前 mihomo 这条也漏了它,导致在 mihomo 下配了 failover 组、
	// 部署成功后 failover.json 仍是空的、failover/status 的 groups 也一直空
	// ——真机 2026-11-26 用带 lanes 的 failover 组复现。
	SavePlan func(failover any) error

	HealthTimeout time.Duration
	StableFor     time.Duration
	Poll          time.Duration

	mu sync.Mutex
}

func (d *Deployer) fs() FS {
	if d.FS != nil {
		return d.FS
	}
	return OSFS{}
}

func (d *Deployer) healthTimeout() time.Duration {
	if d.HealthTimeout > 0 {
		return d.HealthTimeout
	}
	return 20 * time.Second
}

func (d *Deployer) stableFor() time.Duration {
	if d.StableFor > 0 {
		return d.StableFor
	}
	return 3 * time.Second
}

func (d *Deployer) poll() time.Duration {
	if d.Poll > 0 {
		return d.Poll
	}
	return 500 * time.Millisecond
}

func (d *Deployer) applyBudget() time.Duration {
	return 2*d.healthTimeout() + 3*time.Minute
}

func mihomoFail(stage, msg string, bad []string) DeployResult {
	if bad == nil {
		bad = []string{}
	}
	return DeployResult{Stage: stage, Message: msg, BadTags: bad}
}

// PrevPath 是上一份配置的备份(替换前写)。
func (d *Deployer) PrevPath() string { return d.Paths.Config + ".prev" }

// Deploy 做一次完整部署。
func (d *Deployer) Deploy(ctx context.Context) DeployResult {
	if !d.mu.TryLock() {
		r := mihomoFail(MihomoStageError, "已有一次部署正在进行,请稍后再试", nil)
		r.Busy = true
		return r
	}
	defer d.mu.Unlock()

	if d.Build == nil || d.Service == nil || d.Paths.Config == "" || d.ServiceName == "" {
		return mihomoFail(MihomoStageError, "部署没有配置完整(缺少配置生成、服务管理或路径)", nil)
	}

	// 1. 先摸清现状
	before, err := d.Service.Status(ctx, d.ServiceName)
	if err != nil {
		return mihomoFail(MihomoStageError, "读不到内核服务的状态,为安全起见没有改动:"+err.Error(), nil)
	}
	if !before.Running && d.Conflicts != nil {
		if names, err := d.Conflicts(ctx); err == nil && len(names) > 0 {
			return mihomoFail(MihomoStageConflict, "检测到其它代理服务正在运行:"+strings.Join(names, "、")+
				"。同时运行会抢占端口、DNS 和防火墙,为安全起见没有启动内核,请先停掉它们", nil)
		}
	}
	prev, err := os.ReadFile(d.Paths.Config)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return mihomoFail(MihomoStageError, "读不到当前的配置文件,为安全起见没有改动:"+err.Error(), nil)
	}
	hadPrev := err == nil

	// 2. 取指纹 → 生成 → 先在临时路径落盘 → mihomo -t 校验。到这里都没碰正式配置。
	// 指纹必须在生成之前取:生成过程中会顺带刷新规则集等输入,事后再取就和这份配置对不上了。
	fp := d.snapshot(ctx)
	res, err := d.Build.BuildMihomo(ctx)
	if err != nil {
		return mihomoFail(MihomoStageValidate, "生成配置失败:"+err.Error(), nil)
	}
	text, err := MarshalYAML(res.Config)
	if err != nil {
		return mihomoFail(MihomoStageValidate, "序列化配置失败:"+err.Error(), nil)
	}
	tmp := d.Paths.Config + ".candidate"
	if err := d.fs().WriteFile(tmp, text, 0o600); err != nil {
		return mihomoFail(MihomoStageValidate, "写临时配置文件失败:"+err.Error(), nil)
	}
	defer os.Remove(tmp)
	if err := Check(ctx, d.Mihomo, tmp, d.Home, 0); err != nil {
		return mihomoFail(MihomoStageValidate, err.Error(), nil)
	}

	// 3. 应用。从这里起不再理会请求取消
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.applyBudget())
	defer cancel()

	if d.BeforeApply != nil {
		if err := d.BeforeApply(actx); err != nil {
			return mihomoFail(MihomoStageError, "为安全起见没有改动内核和配置:"+err.Error(), nil)
		}
	}
	if hadPrev {
		if err := store.WriteFileAtomic(d.PrevPath(), prev, 0o600); err != nil {
			return mihomoFail(MihomoStageError, "备份旧配置失败,未应用:"+err.Error(), nil)
		}
	}
	meta := d.Meta
	if meta.Kernel == "" {
		meta.Kernel = "mihomo"
	}
	if meta.GeneratedAt.IsZero() {
		meta.GeneratedAt = time.Now()
	}
	if meta.DNSMasq {
		meta.DNSMasq = d.Platform == "openwrt"
	}
	if err := WriteArtifacts(d.fs(), d.Paths, res, meta); err != nil {
		// 写失败时尽量把旧配置放回去(WriteArtifacts 是逐个文件写的,可能写了一半)
		if hadPrev {
			_ = store.WriteFileAtomic(d.Paths.Config, prev, 0o600)
		}
		return mihomoFail(MihomoStageError, "写入新配置失败,未应用:"+err.Error(), nil)
	}

	action := "start"
	if before.Running {
		action = "restart"
	}
	sres, serr := d.Service.Do(actx, d.ServiceName, action)
	if serr != nil || !sres.OK {
		return d.rollback(actx, MihomoStageStart, "内核"+mihomoActionName(action)+"失败:"+describeService(sres, serr), before.Running, hadPrev, prev)
	}

	addr, secret, cerr := ControllerFromFile(d.Paths.Config)
	if cerr != nil {
		return d.rollback(actx, MihomoStageVerify, "内核已"+mihomoActionName(action)+",但读不到它的控制接口:"+cerr.Error(), before.Running, hadPrev, prev)
	}
	if err := d.waitHealthy(actx, addr, secret); err != nil {
		return d.rollback(actx, MihomoStageVerify, "内核已"+mihomoActionName(action)+",但健康检查没通过:"+err.Error(), before.Running, hadPrev, prev)
	}

	note := ""
	if d.AfterRunning != nil {
		if s := d.AfterRunning(actx); s != "" {
			note = s
		}
	}
	if d.SaveSelections != nil {
		if err := d.SaveSelections(res.SelectionsAny()); err != nil {
			msg := "选中项没保存成功:" + err.Error()
			if note == "" {
				note = msg
			} else {
				note += ";" + msg
			}
		}
	}
	// 部署成功:记下这次的输入指纹,service/status 的 pendingRestart 据此清零。
	// 之前 mihomo 这条完全没有这一步,真机上「改配置 → 部署」之后 pendingRestart 一直是真。
	d.recordApplied(fp)
	// 存故障转移计划(与 sing-box 那条同义)。漏了它,mihomo 下配了 failover 组也不会有计划。
	if d.SavePlan != nil {
		if err := d.SavePlan(res.Failover); err != nil {
			m := "故障转移计划没保存成功,故障转移状态可能不准:" + err.Error()
			if note == "" {
				note = m
			} else {
				note += ";" + m
			}
		}
	}
	msg := "已应用并正常运行"
	if note != "" {
		msg += ";" + note
	}
	return DeployResult{OK: true, Stage: MihomoStageRunning, Message: msg, BadTags: []string{}}
}

// waitHealthy 等到「服务在跑 + 控制接口答话 + 稳定一小会儿仍然如此」。
func (d *Deployer) waitHealthy(ctx context.Context, addr, secret string) error {
	probe := func() error {
		st, err := d.Service.Status(ctx, d.ServiceName)
		if err != nil {
			return fmt.Errorf("读不到服务状态:%w", err)
		}
		if !st.Running {
			return errors.New("内核没有在运行(可能启动后就退出了)")
		}
		return probeMihomoController(ctx, addr, secret)
	}
	deadline := time.Now().Add(d.healthTimeout())
	var last error
	for {
		if last = probe(); last == nil {
			break
		}
		if time.Now().After(deadline) {
			return last
		}
		if !sleepCtx(ctx, d.poll()) {
			return ctx.Err()
		}
	}
	if !sleepCtx(ctx, d.stableFor()) {
		return ctx.Err()
	}
	if err := probe(); err != nil {
		return fmt.Errorf("刚启动时正常,%s 后又出问题:%w", d.stableFor(), err)
	}
	return nil
}

func sleepCtx(ctx context.Context, dur time.Duration) bool {
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// probeMihomoController 问 mihomo 的 /version(带 secret)。200 才算控制接口真的起来了。
func probeMihomoController(ctx context.Context, addr, secret string) error {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	u := url.URL{Scheme: "http", Host: dialControllerAddr(addr), Path: "/version"}
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return errors.New("控制接口还连不上")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("控制接口返回 %d", resp.StatusCode)
	}
	return nil
}

// dialControllerAddr 把监听地址换成本机能连的:空主机 / 0.0.0.0 / :: 换成回环。
func dialControllerAddr(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return listen
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// rollback 与 sing-box 那条同义:有旧配置就写回并重启确认;没有就删掉新配置并保持停止。
func (d *Deployer) rollback(ctx context.Context, stage, reason string, wasRunning, hadPrev bool, prev []byte) DeployResult {
	var problems []string
	if hadPrev {
		if err := store.WriteFileAtomic(d.Paths.Config, prev, 0o600); err != nil {
			problems = append(problems, "写回旧配置失败:"+err.Error())
		}
	} else if err := os.Remove(d.Paths.Config); err != nil && !errors.Is(err, os.ErrNotExist) {
		problems = append(problems, "删除新配置失败:"+err.Error())
	}
	if len(problems) > 0 {
		return DeployResult{Stage: stage, BadTags: []string{}, Message: reason + ";回滚失败(" + strings.Join(problems, ";") +
			"),系统可能停在新配置上,请检查 " + d.Paths.Config}
	}
	restored := "已回滚到上一份配置"
	if !hadPrev {
		restored = "已撤销这次的配置"
	}
	tail := "内核已恢复运行"
	switch {
	case wasRunning && hadPrev:
		if res, err := d.Service.Do(ctx, d.ServiceName, "restart"); err != nil || !res.OK {
			problems = append(problems, "旧配置重启失败:"+describeService(res, err))
		} else if addr, secret, err := ControllerFromFile(d.Paths.Config); err != nil {
			problems = append(problems, "旧配置读不到控制接口:"+err.Error())
		} else if err := d.waitHealthy(ctx, addr, secret); err != nil {
			problems = append(problems, "旧配置重启后仍不正常:"+err.Error())
		}
	case wasRunning:
		problems = append(problems, "部署前内核在运行但没有配置文件,无法确定该恢复成什么,没有改动服务")
	default:
		tail = "内核保持停止(部署前就没在运行)"
		if res, err := d.Service.Do(ctx, d.ServiceName, "stop"); err != nil || !res.OK {
			problems = append(problems, "停止内核失败:"+describeService(res, err))
		}
	}
	if len(problems) > 0 {
		return DeployResult{Stage: stage, BadTags: []string{}, RolledBack: true,
			Message: reason + ";" + restored + ",但" + strings.Join(problems, ";") + ",请检查内核服务"}
	}
	msg := reason + ";" + restored + "," + tail
	if wasRunning && hadPrev && d.AfterRunning != nil {
		if s := d.AfterRunning(ctx); s != "" {
			msg += ";" + s
		}
	}
	return DeployResult{Stage: stage, BadTags: []string{}, RolledBack: true, Message: msg}
}

func describeService(res system.ActionResult, err error) string {
	switch {
	case err != nil:
		return err.Error()
	case res.Stderr != "":
		return res.Stderr
	}
	return fmt.Sprintf("退出码 %d", res.Code)
}

func mihomoActionName(a string) string {
	if a == "restart" {
		return "重启"
	}
	return "启动"
}

// snapshot 在生成配置之前取「这次部署用到的输入」的指纹;取不到只当作没有,部署照常进行
// (与 sing-box 那条同义:取不到就不判断 pendingRestart,而不是让部署失败)。
func (d *Deployer) snapshot(ctx context.Context) singbox.Fingerprint {
	if d.Track == nil {
		return nil
	}
	fp, err := d.Track.Snapshot(ctx)
	if err != nil {
		return nil
	}
	return fp
}

// recordApplied 在部署成功后存指纹。存失败不影响部署结果(系统已经变了,调用方必须拿到真实结果)。
func (d *Deployer) recordApplied(fp singbox.Fingerprint) {
	if d.Track == nil || fp == nil {
		return
	}
	_ = d.Track.Record(fp)
}
