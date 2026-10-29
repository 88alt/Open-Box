package singbox

// 「待重启」判断:配置已经改了、内核还跑着旧配置(服务状态的 pendingRestart)。
//
// 自己的设计,依据前端 OpenboxServiceStatus.pendingRestart 的类型和 composables/kernelService.ts 的用法
// (原因取 nodes / dns / route / inbounds / system 五个键,内核在运行且 pending 为真才提示),没有参照上游。
//
// 做法:每次部署成功时,把「生成这份配置用到的输入」按原因分组各算一个指纹,存进 applied-config;
// 服务状态按同样的办法现算一份,和存着的比,不一样的原因就是待重启的原因。只读已存的数据,
// 不调用 core、不联网、不碰系统,所以前端每 10 秒轮询也不贵。
//
// 约定:
//   - 没有记录(从没通过本后端部署成功过,或指纹没算出来)= 不判断,不猜「有改动」;
//   - 指纹在生成配置之前取:取完到生成之间用户又改了东西,宁可多报一次待重启(再部署一次就清掉),也不漏报;
//   - 回滚、校验失败的部署不更新记录(内核跑的还是旧配置,记录也还是旧配置的);
//   - 本机输入(/etc/resolv.conf、局域网网段)不进指纹:它们会自己变,不是用户的改动,报出来只会让提示莫名出现;
//   - 规则集链接的远端内容、DNS 过滤名单的内容也不进指纹:前者由「立即更新」自己报 needsRestart,
//     后者只在「应用」时变,应用本身就会部署;但「过滤是否已按当前设置应用」进指纹(见 FilterKey)。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

// 待重启的原因。取值就是前端 RESTART_REASON_KEYS 的键,不能改。
const (
	ReasonNodes    = "nodes"    // 节点、节点组、链式代理、测速地址
	ReasonDNS      = "dns"      // DNS 设置、地区、IPv6、域名过滤是否生效
	ReasonRoute    = "route"    // 分流规则、终端分流、直连旁路、屏蔽 QUIC
	ReasonInbounds = "inbounds" // 共享网络服务器、tun 参数
	ReasonSystem   = "system"   // 其它会进配置的档案键
)

// reasonOrder 是原因在响应里的固定顺序。
var reasonOrder = []string{ReasonNodes, ReasonDNS, ReasonRoute, ReasonInbounds, ReasonSystem}

// profileKeyReason:档案键 → 原因。依据是 core 实际读取的键(core/internal/config 里 jsx.Get(profile, …))
// 加上后端自己读的 chainProxies;前端档案里有、但 core 目前还没读的(bypassPorts 等)也按字面含义归类,
// 免得 core 以后读它们时漏报。没列出的键一律归「系统」(见 profileReason)。
var profileKeyReason = map[string]string{
	"testUrl": ReasonNodes, "directTestUrl": ReasonNodes, "testExpectedStatus": ReasonNodes, "chainProxies": ReasonNodes,
	"dns": ReasonDNS, "region": ReasonDNS, "ipv6": ReasonDNS, "ipv6Proxy": ReasonDNS,
	"routing": ReasonRoute, "clientRoutes": ReasonRoute, "directBypass": ReasonRoute, "directForNodes": ReasonRoute,
	"rejectQuic": ReasonRoute, "bypassPorts": ReasonRoute, "bypassPortsWhitelist": ReasonRoute, "bypassPortsMode": ReasonRoute,
	"servers": ReasonInbounds, "tun": ReasonInbounds,
}

// profileKeyIgnored:确定不会进 sing-box 配置的档案键。改它们不该提示重启。
// updates 是自动更新计划,traffic 是流量保留月数,shareRegions / serverInfo 是给手机 App 的,clashApiSecret 是后端自己加的。
var profileKeyIgnored = map[string]bool{
	"updates": true, "traffic": true, "shareRegions": true, "serverInfo": true, "clashApiSecret": true,
}

func profileReason(key string) (reason string, ignored bool) {
	if profileKeyIgnored[key] {
		return "", true
	}
	if r, ok := profileKeyReason[key]; ok {
		return r, false
	}
	return ReasonSystem, false
}

// Fingerprint:原因 → 这一组输入的指纹(十六进制)。
type Fingerprint map[string]string

// FingerprintInput 是生成配置用到的输入(与 Generator.BuildWith 读的一致)。
type FingerprintInput struct {
	Profile map[string]any
	Groups  []any
	Subs    []subs.Record      // 全部订阅;只有启用的参与
	Nodes   []subs.CatalogNode // 已启用订阅的节点
	// FilterKey 非空 = 域名过滤已按当前设置应用、会写进配置(启用且产物就绪);空 = 配置里没有过滤。
	FilterKey string
}

// subInput 是订阅里会影响配置的字段(id / 名字 / 是否启用 / 地址 / 节点 DNS)。
// 用量、更新时间、节点数等刷新一次就变的字段不算,否则每次刷新订阅都会提示重启。
type subInput struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	URL     string         `json:"url"`
	URLs    []string       `json:"urls,omitempty"`
	NodeDNS map[string]any `json:"nodeDns"`
}

// ComputeFingerprint 按原因分组算指纹。同样的输入永远得到同样的结果(map 键由 encoding/json 排序)。
func ComputeFingerprint(in FingerprintInput) Fingerprint {
	parts := make(map[string]map[string]any, len(reasonOrder))
	for _, r := range reasonOrder {
		parts[r] = map[string]any{}
	}
	for k, v := range in.Profile {
		r, ignored := profileReason(k)
		if ignored {
			continue
		}
		if k == "dns" {
			// core 只读 dns.filter.enabled,而且过滤是否生效要看产物是否就绪(下面的 filter),
			// 所以名单名字、自动更新计划这些 dns.filter 里的东西不参与
			v = withoutKey(v, "filter")
		}
		parts[r]["profile."+k] = v
	}
	parts[ReasonNodes]["groups"] = in.Groups
	var subList []subInput
	for _, r := range in.Subs {
		if r.Enabled {
			subList = append(subList, subInput{ID: r.ID, Name: r.Name, URL: r.URL, URLs: r.URLs, NodeDNS: r.NodeDNS})
		}
	}
	parts[ReasonNodes]["subscriptions"] = subList
	type nodeInput struct {
		Subscription string `json:"subscription"`
		Node         any    `json:"node"`
	}
	nodes := make([]nodeInput, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		nodes = append(nodes, nodeInput{Subscription: n.SubscriptionID, Node: n.Node})
	}
	parts[ReasonNodes]["nodes"] = nodes
	parts[ReasonDNS]["filter"] = in.FilterKey

	fp := make(Fingerprint, len(parts))
	for r, p := range parts {
		fp[r] = hashOf(p)
	}
	return fp
}

func withoutKey(v any, key string) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	for k, x := range m {
		if k != key {
			out[k] = x
		}
	}
	return out
}

func hashOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// 输入都来自 JSON,正常不会失败;万一失败也要给一个确定的值,不能让整个判断报废
		b = []byte(fmt.Sprintf("%#v", v))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// Fingerprint 按当前已保存的数据算一份指纹。只读,不调 core、不联网。
func (g *Generator) Fingerprint(ctx context.Context) (Fingerprint, error) {
	prof, err := g.profiles.Get(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := g.groups.Groups(ctx)
	if err != nil {
		return nil, err
	}
	recs, cnodes, err := g.cat.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	in := FingerprintInput{Profile: prof, Groups: groups, Subs: recs, Nodes: cnodes}
	// 与 BuildWith 一致:开了过滤、而且产物就绪(按当前设置应用过)才会写进配置
	if dnsFilterEnabled(prof) && g.filter != nil {
		_, key, _, ready, err := g.filter.CoreArtifact(ctx, prof)
		if err != nil {
			return nil, fmt.Errorf("读取域名过滤状态失败: %w", err)
		}
		if ready {
			in.FilterKey = key
		}
	}
	return ComputeFingerprint(in), nil
}

// ---------- 记录与比对 ----------

const fileApplied = "applied-config"

type appliedFile struct {
	Fingerprint Fingerprint `json:"fingerprint"`
	At          int64       `json:"at"` // 毫秒,仅供人看
}

// FingerprintSource 提供当前输入的指纹(*Generator 满足)。
type FingerprintSource interface {
	Fingerprint(ctx context.Context) (Fingerprint, error)
}

// PendingRestart 对应前端的 pendingRestart。Reasons 永远是数组(不是 null),按固定顺序。
type PendingRestart struct {
	Pending bool     `json:"pending"`
	Reasons []string `json:"reasons"`
}

// Applied 记「上次成功部署用的输入指纹」,并和当前的比。
type Applied struct {
	Store  *store.Store
	Source FingerprintSource
	Now    func() time.Time
}

// Snapshot 取当前输入的指纹(部署在生成配置之前调用)。
func (a *Applied) Snapshot(ctx context.Context) (Fingerprint, error) {
	return a.Source.Fingerprint(ctx)
}

// Record 把一次成功部署用的指纹存下来。fp 为空 = 这次没取到指纹,把旧记录清掉
// (旧记录描述的是更早的配置,留着会报出不存在的改动)。
func (a *Applied) Record(fp Fingerprint) error {
	if len(fp) == 0 {
		return a.Store.Delete(fileApplied)
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	return a.Store.Save(fileApplied, appliedFile{Fingerprint: fp, At: now().UnixMilli()})
}

// Pending 比较存着的记录和当前输入。没有记录就是「没有」,读不到记录或算不出当前指纹返回错误(调用方当作没有)。
func (a *Applied) Pending(ctx context.Context) (PendingRestart, error) {
	out := PendingRestart{Reasons: []string{}}
	var rec appliedFile
	ok, err := a.Store.Load(fileApplied, &rec)
	if err != nil {
		return out, err
	}
	if !ok || len(rec.Fingerprint) == 0 {
		return out, nil
	}
	cur, err := a.Source.Fingerprint(ctx)
	if err != nil {
		return out, err
	}
	for _, r := range reasonOrder {
		if rec.Fingerprint[r] != cur[r] {
			out.Reasons = append(out.Reasons, r)
		}
	}
	out.Pending = len(out.Reasons) > 0
	return out, nil
}

// Tracker 是部署用到的记录接口(*Applied 满足)。
type Tracker interface {
	Snapshot(ctx context.Context) (Fingerprint, error)
	Record(fp Fingerprint) error
}

// snapshot 在生成配置之前取指纹;取不到只写日志,这次部署照常进行(部署成功后会清掉旧记录)。
func (d *Deployer) snapshot(ctx context.Context) Fingerprint {
	if d.Track == nil {
		return nil
	}
	fp, err := d.Track.Snapshot(ctx)
	if err != nil {
		slog.Warn("取部署输入指纹失败,这次部署后不判断「待重启」", "error", err)
		return nil
	}
	return fp
}

// recordApplied 在部署成功后存指纹。存失败只写日志,不影响部署结果(系统已经变了,调用方必须拿到真实结果)。
func (d *Deployer) recordApplied(fp Fingerprint) {
	if d.Track == nil {
		return
	}
	if err := d.Track.Record(fp); err != nil {
		slog.Warn("记录部署输入指纹失败,「待重启」的判断可能不准", "error", err)
	}
}
