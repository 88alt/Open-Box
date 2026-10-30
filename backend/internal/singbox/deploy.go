package singbox

// 部署:生成配置 → 落盘本地规则集 → sing-box check → 原子替换 → 重启 → 健康检查 → 失败回滚。
// 对应 DESIGN 第四节「变更安全」:先校验再应用;替换前留一份旧配置;重启后确认进程、控制接口和入站端口都正常,
// 否则换回旧配置再重启,并如实告诉调用方「回滚了」还是「回滚也失败了」。无法确认系统状态时不动手。
//
// 自己的设计。阶段名沿用前端 OpenboxDeployResult.stage:
//   validate = 生成 / 规则集 / 校验没过(还没碰正式配置); start = 重启命令失败; verify = 重启了但健康检查没过;
//   running = 成功; error = 其它(已有部署在进行、读不到服务状态等); conflict = 内核没在运行、而别的代理服务正在运行(见 Conflicts)。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// 阶段。
const (
	StageRunning  = "running"
	StageConflict = "conflict"
	StageValidate = "validate"
	StageStart    = "start"
	StageVerify   = "verify"
	StageError    = "error"
)

// DeployResult 对应前端的 OpenboxDeployResult。
type DeployResult struct {
	OK      bool     `json:"ok"`
	Stage   string   `json:"stage"`
	Message string   `json:"message"`
	BadTags []string `json:"badTags"`
	// RolledBack:新配置没能跑起来,已经换回旧配置。Message 里写着回滚后的状态。
	RolledBack bool `json:"rolledBack,omitempty"`
	// Busy:已有一次部署在进行,这次什么都没做(不进响应;HTTP 层据此回 409,也不记成「最近一次部署」)。
	Busy bool `json:"-"`
}

// Builder 生成配置(*Generator 满足)。
type Builder interface {
	Build(ctx context.Context) (*Built, error)
}

// ServiceControl 是用到的服务管理(*system.Services 满足)。
type ServiceControl interface {
	Status(ctx context.Context, name string) (system.ServiceInfo, error)
	Do(ctx context.Context, name, action string) (system.ActionResult, error)
}

// Health 判断刚启动的内核是否正常;cfg 是刚应用的配置。返回 nil 表示正常。
type Health func(ctx context.Context, cfg map[string]any) error

// Deployer 做一次完整的部署。零值字段用默认值:HealthTimeout 20 秒,StableFor 3 秒,Poll 500 毫秒,Health = DefaultHealth。
type Deployer struct {
	Build       Builder
	Files       Files
	SingBox     string // sing-box 可执行文件
	ConfigDir   string // 放正式配置和临时校验文件的目录
	ConfigFile  string // 正式配置路径,空 = ConfigDir/config.json
	Service     ServiceControl
	ServiceName string
	Health      Health
	// Conflicts 返回此刻正在运行的其它代理服务的名字(OpenClash、PassWall 等)。只在内核当前没在运行、
	// 这次部署要把它从停止状态启动时才问:有冲突就不动手(同时跑会抢端口、DNS 和防火墙,可能弄断联网),
	// 返回 conflict 阶段。内核已经在运行时的重启不拦(本来就并存着,重启不会更糟)。nil 或返回 error = 当作没有
	// (与服务状态里的 conflicts 一样,检测失败不拦路)。
	Conflicts func(ctx context.Context) ([]string, error)
	// Track 非 nil:生成配置之前取一份输入指纹,部署成功后存下来,服务状态的 pendingRestart 靠它判断
	// 「配置改了、内核还没用上」(见 pending.go)。回滚和校验失败不更新记录。
	Track Tracker
	// BeforeApply 在校验通过、替换正式配置(也就是要重启内核)之前调用,用来撤销 DNS 接管:内核重启的这几秒里
	// dnsmasq 不能还指着它。返回 error = 没撤销成功,为安全起见不动手(StageError,正式配置和服务都没碰)。
	// 校验没过的部署到不了这一步,所以不会白白撤销。nil = 不处理 DNS。
	BeforeApply func(ctx context.Context) error
	// AfterRunning 在内核起来并通过健康检查之后调用(部署成功;或失败回滚后旧配置又健康了),用来按 dns.mode
	// 重新对齐 DNS 接管。返回的一句话附在 Message 后面(空串不附)。它的失败不改变 OK:内核是好的,
	// 接管那边出错时已自动恢复到接管之前的状态,并在这句话里说明。nil = 不处理 DNS。
	AfterRunning func(ctx context.Context) string
	// SavePlan 非 nil:部署成功(内核健康)后调用,把这份配置对应的故障转移计划(Built.Failover)存下来,
	// 状态接口按已部署的那份计划说话。回滚和校验失败不调(旧配置对应的旧计划还在)。它的失败不改变 OK
	// (内核是好的),错误作为一句话附在 Message 后面。
	SavePlan func(failover any) error

	HealthTimeout time.Duration
	StableFor     time.Duration
	Poll          time.Duration

	mu sync.Mutex
}

func (d *Deployer) configPath() string {
	if d.ConfigFile != "" {
		return d.ConfigFile
	}
	return filepath.Join(d.ConfigDir, "config.json")
}

// PrevPath 是上一份配置的备份路径(每次替换前写,回滚用;进程在替换后、回滚前被杀时,人也能照它手动恢复)。
func (d *Deployer) PrevPath() string {
	p := d.configPath()
	return strings.TrimSuffix(p, filepath.Ext(p)) + ".prev" + filepath.Ext(p)
}

func fail(stage, msg string, bad []string) DeployResult {
	if bad == nil {
		bad = []string{}
	}
	return DeployResult{Stage: stage, Message: msg, BadTags: bad}
}

// Deploy 执行一次部署。同一时刻只允许一次。
// 替换正式配置之后的所有步骤(重启、检查、回滚)不受 ctx 取消影响:请求断开不能把系统留在半截状态。
func (d *Deployer) Deploy(ctx context.Context) DeployResult {
	if !d.mu.TryLock() {
		r := fail(StageError, "已有一次部署正在进行,请稍后再试", nil)
		r.Busy = true
		return r
	}
	defer d.mu.Unlock()

	if d.Build == nil || d.Service == nil || d.ConfigDir == "" || d.ServiceName == "" {
		return fail(StageError, "部署没有配置完整(缺少配置生成、服务管理或目录)", nil)
	}

	// 1. 先摸清现状:读不到服务状态就不动
	before, err := d.Service.Status(ctx, d.ServiceName)
	if err != nil {
		return fail(StageError, "读不到内核服务的状态,为安全起见没有改动:"+err.Error(), nil)
	}
	if !before.Running && d.Conflicts != nil {
		if names, err := d.Conflicts(ctx); err == nil && len(names) > 0 {
			return fail(StageConflict, "检测到其它代理服务正在运行:"+strings.Join(names, "、")+"。同时运行会抢占端口、DNS 和防火墙,为安全起见没有启动内核,请先停掉它们", nil)
		}
	}
	prev, err := os.ReadFile(d.configPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(StageError, "读不到当前的配置文件,为安全起见没有改动:"+err.Error(), nil)
	}
	hadPrev := err == nil

	// 2. 生成、落盘规则集、校验。到这里为止都没碰正式配置
	// 输入指纹必须在生成之前取:取完之后用户又改了东西,记录就比实际部署的旧,最多多报一次「待重启」,不会漏报
	fp := d.snapshot(ctx)
	built, err := d.Build.Build(ctx)
	if err != nil {
		return fail(StageValidate, "生成配置失败:"+err.Error(), nil)
	}
	rep, err := d.Files.WriteLocalSets(ctx, built.Config, built.DirectHosts)
	if err != nil {
		return fail(StageError, "准备规则集文件被中断:"+err.Error(), nil)
	}
	if !rep.OK() {
		var bad []string
		var why []string
		for _, m := range rep.Missing {
			reason := m.Reason
			if r, ok := built.ListIssues[m.Tag]; ok {
				reason = r
			}
			bad = append(bad, m.Tag)
			why = append(why, m.Tag+"("+reason+")")
		}
		return fail(StageValidate, "配置引用的规则集还没准备好,未应用:"+strings.Join(why, "、"), bad)
	}
	data, err := built.JSON()
	if err != nil {
		return fail(StageValidate, "序列化配置失败:"+err.Error(), nil)
	}
	if err := Check(ctx, d.SingBox, d.ConfigDir, data); err != nil {
		var ce *CheckError
		switch {
		case errors.As(err, &ce):
			return fail(StageValidate, ce.Error(), nil)
		case errors.Is(err, ErrNoSingBox):
			return fail(StageValidate, err.Error(), nil)
		}
		return fail(StageValidate, "校验配置失败:"+err.Error(), nil)
	}

	// 3. 应用。从这里起不再理会请求取消
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.applyBudget())
	defer cancel()

	if d.BeforeApply != nil {
		if err := d.BeforeApply(actx); err != nil {
			return fail(StageError, "为安全起见没有改动内核和配置:"+err.Error(), nil)
		}
	}
	if hadPrev {
		if err := store.WriteFileAtomic(d.PrevPath(), prev, 0o600); err != nil {
			return fail(StageError, "备份旧配置失败,未应用:"+err.Error(), nil)
		}
	}
	if err := store.WriteFileAtomic(d.configPath(), data, 0o600); err != nil {
		return fail(StageError, "写入新配置失败,未应用:"+err.Error(), nil)
	}

	action := "start"
	if before.Running {
		action = "restart"
	}
	res, err := d.Service.Do(actx, d.ServiceName, action)
	if err != nil || !res.OK {
		return d.rollback(actx, StageStart, "内核"+actionName(action)+"失败:"+describe(res, err), before.Running, hadPrev, prev)
	}
	if err := d.waitHealthy(actx, built.Config); err != nil {
		return d.rollback(actx, StageVerify, "内核已"+actionName(action)+",但健康检查没通过:"+err.Error(), before.Running, hadPrev, prev)
	}
	d.recordApplied(fp)
	note := d.afterRunning(actx)
	if d.SavePlan != nil {
		if err := d.SavePlan(built.Failover); err != nil {
			msg := "故障转移计划没保存成功,故障转移状态可能不准:" + err.Error()
			if note == "" {
				note = msg
			} else {
				note += ";" + msg
			}
		}
	}
	return DeployResult{OK: true, Stage: StageRunning, Message: joinNote("已应用并正常运行", note), BadTags: []string{}}
}

func (d *Deployer) afterRunning(ctx context.Context) string {
	if d.AfterRunning == nil {
		return ""
	}
	return d.AfterRunning(ctx)
}

func joinNote(msg, note string) string {
	if note == "" {
		return msg
	}
	return msg + ";" + note
}

func actionName(a string) string {
	if a == "restart" {
		return "重启"
	}
	return "启动"
}

func describe(res system.ActionResult, err error) string {
	switch {
	case err != nil:
		return err.Error()
	case res.Stderr != "":
		return res.Stderr
	}
	return "退出码 " + strconv.Itoa(res.Code)
}

// applyBudget 是应用阶段(重启 + 检查 + 可能的回滚)的总时限,免得卡死时一直占着部署锁。
func (d *Deployer) applyBudget() time.Duration {
	return 2*d.healthTimeout() + 3*time.Minute
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

// waitHealthy 等到内核「在跑、健康检查通过、并且稳定了一小会儿还是在跑」。
// 稳定期是为了抓住「启动了几秒就崩」的情况。超时时返回最后一次的原因。
func (d *Deployer) waitHealthy(ctx context.Context, cfg map[string]any) error {
	health := d.Health
	if health == nil {
		health = DefaultHealth
	}
	probe := func() error {
		st, err := d.Service.Status(ctx, d.ServiceName)
		if err != nil {
			return fmt.Errorf("读不到服务状态:%w", err)
		}
		if !st.Running {
			return errors.New("内核没有在运行(可能启动后就退出了)")
		}
		return health(ctx, cfg)
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
		if !sleep(ctx, d.poll()) {
			return ctx.Err()
		}
	}
	if !sleep(ctx, d.stableFor()) {
		return ctx.Err()
	}
	if err := probe(); err != nil {
		return fmt.Errorf("刚启动时正常,%s 后又出问题:%w", d.stableFor(), err)
	}
	return nil
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

// rollback 换回旧配置并让服务回到部署前的状态:
//   - 有旧配置:写回旧配置;部署前在跑就重启并确认它又健康了,没在跑就保持停止;
//   - 没有旧配置(第一次部署):删掉新配置,保持停止(部署前就没在跑)。
//
// 返回的结果带着失败原因和回滚结果;回滚本身失败要明说,因为这时系统状态不确定,需要人看一眼。
func (d *Deployer) rollback(ctx context.Context, stage, reason string, wasRunning, hadPrev bool, prev []byte) DeployResult {
	var problems []string
	if hadPrev {
		if err := store.WriteFileAtomic(d.configPath(), prev, 0o600); err != nil {
			problems = append(problems, "写回旧配置失败:"+err.Error())
		}
	} else if err := os.Remove(d.configPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		problems = append(problems, "删除新配置失败:"+err.Error())
	}
	if len(problems) > 0 {
		// 配置都没换回去,再重启只会重复刚才的失败;不动服务,把现状说清楚
		return DeployResult{Stage: stage, BadTags: []string{}, Message: reason + ";回滚失败(" + strings.Join(problems, ";") +
			"),系统可能停在新配置上,请检查 " + d.configPath() + ",旧配置备份在 " + d.PrevPath()}
	}
	restored := "已回滚到上一份配置"
	if !hadPrev {
		restored = "已撤销这次的配置"
	}
	tail := "内核已恢复运行"
	switch {
	case wasRunning && hadPrev:
		if res, err := d.Service.Do(ctx, d.ServiceName, "restart"); err != nil || !res.OK {
			problems = append(problems, "旧配置重启失败:"+describe(res, err))
		} else if err := d.waitHealthyPrev(ctx, prev); err != nil {
			problems = append(problems, "旧配置重启后仍不正常:"+err.Error())
		}
	case wasRunning:
		// 部署前内核在跑、却没有我们的配置文件:不知道它用的是什么,不擅自停它
		problems = append(problems, "部署前内核在运行但没有配置文件,无法确定该恢复成什么,没有改动服务")
	default:
		tail = "内核保持停止(部署前就没在运行)"
		if res, err := d.Service.Do(ctx, d.ServiceName, "stop"); err != nil || !res.OK {
			problems = append(problems, "停止内核失败:"+describe(res, err))
		}
	}
	if len(problems) > 0 {
		return DeployResult{Stage: stage, BadTags: []string{}, RolledBack: true,
			Message: reason + ";" + restored + ",但" + strings.Join(problems, ";") + ",请检查内核服务"}
	}
	msg := reason + ";" + restored + "," + tail
	if wasRunning && hadPrev {
		// 旧配置的内核又健康了:BeforeApply 撤销过的 DNS 接管要接回去(没接回去也只是说明,不改回滚结果)
		msg = joinNote(msg, d.afterRunning(ctx))
	}
	return DeployResult{Stage: stage, BadTags: []string{}, RolledBack: true, Message: msg}
}

// waitHealthyPrev 用旧配置的内容做健康检查(旧配置是原样的 JSON 字节,解析一下取控制接口和入站)。
func (d *Deployer) waitHealthyPrev(ctx context.Context, prev []byte) error {
	var cfg map[string]any
	if err := json.Unmarshal(prev, &cfg); err != nil {
		// 旧配置读不懂就只看进程是否在跑
		cfg = map[string]any{}
	}
	return d.waitHealthy(ctx, cfg)
}

// ---------- 默认健康检查 ----------

// DefaultHealth 检查配置里声明的东西是否真的起来了:
//   - clash_api.external_controller 的 /version 要答 200(带上 secret),说明内核主循环在转;
//   - 常见的 TCP 入站(mixed / http / socks / redirect / tproxy)要能连上。
//
// 配置里没有这些就跳过对应项;进程是否在运行由调用方另外确认。
func DefaultHealth(ctx context.Context, cfg map[string]any) error {
	if addr, secret := controllerOf(cfg); addr != "" {
		if err := probeController(ctx, addr, secret); err != nil {
			return err
		}
	}
	for _, a := range tcpInbounds(cfg) {
		d := net.Dialer{Timeout: 2 * time.Second}
		c, err := d.DialContext(ctx, "tcp", a.addr)
		if err != nil {
			return fmt.Errorf("入站 %s(%s)连不上", a.tag, a.addr)
		}
		_ = c.Close()
	}
	return nil
}

func controllerOf(cfg map[string]any) (addr, secret string) {
	exp, _ := cfg["experimental"].(map[string]any)
	api, _ := exp["clash_api"].(map[string]any)
	ec, _ := api["external_controller"].(string)
	secret, _ = api["secret"].(string)
	return dialAddr(strings.TrimSpace(ec)), secret
}

// dialAddr 把监听地址换成本机能连的地址:空主机 / 0.0.0.0 / :: 都换成回环。
func dialAddr(listen string) string {
	if listen == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

type inboundAddr struct{ tag, addr string }

func tcpInbounds(cfg map[string]any) []inboundAddr {
	list, _ := cfg["inbounds"].([]any)
	var out []inboundAddr
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		switch t, _ := m["type"].(string); t {
		case "mixed", "http", "socks", "redirect", "tproxy":
		default:
			continue
		}
		port, ok := m["listen_port"].(float64)
		if !ok || port <= 0 {
			continue
		}
		host, _ := m["listen"].(string)
		tag, _ := m["tag"].(string)
		if addr := dialAddr(net.JoinHostPort(host, strconv.Itoa(int(port)))); addr != "" {
			out = append(out, inboundAddr{tag: tag, addr: addr})
		}
	}
	return out
}

func probeController(ctx context.Context, addr, secret string) error {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	u := url.URL{Scheme: "http", Host: addr, Path: "/version"}
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
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
