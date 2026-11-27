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
	"os"
	"strings"
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

// 待重启的明细码(对齐上游 v0.1.486 面板的 pendingRestart.details,取值就是前端 RESTART_DETAIL_KEYS 的键,不能改)。
// 明细比原因细:同一个原因(例如 dns)下,改了 DNS 劫持方式、改了 FakeIP 设置是两条。
// **推断点 / 待维护者复核**:上游服务端闭源,明细码与档案键的对应关系是按各码的中文文案自己定的,见 detailParts。
const (
	DetailDNSMode            = "dns_mode"
	DetailDNSListen53        = "dns_listen_53"
	DetailDNSFakeIP          = "dns_fakeip"
	DetailDNSUpstreams       = "dns_upstreams"
	DetailDNSDomainUpstreams = "dns_domain_upstreams"
	DetailDNSRewrite         = "dns_rewrite"
	DetailDNSFilter          = "dns_filter"
	DetailDNSNodeResolver    = "dns_node_resolver"
	DetailRoutingPolicies    = "routing_policies"
	DetailRoutingRuleLists   = "routing_rule_lists" // 目前不产出:规则集链接的远端内容不进指纹(见文件头),留给「立即更新」的 needsRestart
	DetailDirectBypass       = "direct_bypass"
	DetailRejectQUIC         = "reject_quic"
	DetailClientRoutes       = "client_routes"
	DetailTunSettings        = "tun_settings"
	DetailIPv6Settings       = "ipv6_settings"
	DetailBypassPorts        = "bypass_ports"
	DetailMihomoSniffPorts   = "mihomo_sniff_ports" // 目前不产出:mihomoTlsSniffPorts 仍在 profileKeyIgnored(指纹不知道在跑哪个内核)
	DetailSharedServers      = "shared_servers"
	DetailChainProxies       = "chain_proxies"
	DetailNodesAndGroups     = "nodes_and_groups"
	DetailLegacyDeploy       = "legacy_deploy" // 上次部署记录里没有明细指纹(旧版本写的),只知道原因、不知道改了什么
)

// detailOrder 是明细在响应里的固定顺序(与前端 RESTART_DETAIL_KEYS 的顺序一致)。
var detailOrder = []string{
	DetailDNSMode, DetailDNSListen53, DetailDNSFakeIP, DetailDNSUpstreams, DetailDNSDomainUpstreams,
	DetailDNSRewrite, DetailDNSFilter, DetailDNSNodeResolver, DetailRoutingPolicies, DetailRoutingRuleLists,
	DetailDirectBypass, DetailRejectQUIC, DetailClientRoutes, DetailTunSettings, DetailIPv6Settings,
	DetailBypassPorts, DetailMihomoSniffPorts, DetailSharedServers, DetailChainProxies, DetailNodesAndGroups,
}

// detailKeyPrefix 是明细指纹在 Fingerprint 里的键前缀(和原因的键放在同一张表里,落盘格式不变、旧记录照常读)。
const detailKeyPrefix = "d:"

// profileKeyReason:档案键 → 原因。依据是 core 实际读取的键(core/internal/config 里 jsx.Get(profile, …))
// 加上后端自己读的 chainProxies。没列出的键一律归「系统」(见 profileReason)。
// 进内核前放行的端口(bypassPorts / bypassPortsWhitelist / bypassPortsMode)故意不列:上游的提示里它们
// 显示为「系统设置」,这里保持一致,走默认的「系统」归类。
var profileKeyReason = map[string]string{
	"testUrl": ReasonNodes, "directTestUrl": ReasonNodes, "testExpectedStatus": ReasonNodes, "chainProxies": ReasonNodes,
	"nodeScoreWeights": ReasonNodes, // core 把它写进开着带宽择优的 urltest / loadbalance 的 node_score_weights
	"dns":              ReasonDNS, "region": ReasonDNS, "ipv6": ReasonDNS, "ipv6Proxy": ReasonDNS,
	"routing": ReasonRoute, "clientRoutes": ReasonRoute, "directBypass": ReasonRoute, "directForNodes": ReasonRoute,
	"rejectQuic": ReasonRoute,
	"servers":    ReasonInbounds, "tun": ReasonInbounds,
}

// profileKeyIgnored:确定不会进 sing-box 配置的档案键。改它们不该提示重启。
// updates 是自动更新计划,traffic 是流量保留月数,shareRegions / serverInfo 是给手机 App 的,clashApiSecret 是后端自己加的,bandwidth 是测速参数(只在后端调度,不进配置;节点组自己的 bandwidth 开关在 userGroups 里,归「节点」),mihomoTlsSniffPorts 只有 Mihomo 的配置会用(sing-box 配置不读它;core 生成 Mihomo 配置那一步做完后改成归「系统」,见 STATUS.md 双内核第 3 步)。
var profileKeyIgnored = map[string]bool{
	"updates": true, "traffic": true, "shareRegions": true, "serverInfo": true, "clashApiSecret": true, "bandwidth": true, "mihomoTlsSniffPorts": true,
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
	// 下面三个影响「订阅和节点站点直连」的主机集合
	URLEntries         []subs.URLEntry `json:"urlEntries,omitempty"`
	DirectSubscription *bool           `json:"directSubscription,omitempty"`
	DirectNodes        *bool           `json:"directNodes,omitempty"`
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
			subList = append(subList, subInput{ID: r.ID, Name: r.Name, URL: r.URL, URLs: r.URLs, NodeDNS: r.NodeDNS, URLEntries: r.URLEntries, DirectSubscription: r.DirectSubscription, DirectNodes: r.DirectNodes})
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

// detailParts 把同样的输入再按明细码分一遍组。只是「怎么分」不同,输入的范围与 ComputeFingerprint 完全一样
// (被忽略的档案键、不进指纹的本机输入这里也都不看),所以「有明细变了」必然伴随「有原因变了」。
//
// 对应关系(推断,上游闭源):
//   - dns.mode → dns_mode;dns.listen53 → dns_listen_53;dns.fakeIpForProxy / fakeIpRange → dns_fakeip;
//     dns.domainUpstreams → dns_domain_upstreams;dns.rewrite → dns_rewrite;dns 里其余的键
//     (direct* / proxy* / split / region 以及将来新增的)和顶层 region → dns_upstreams;
//   - 域名过滤「是否已按当前设置应用」→ dns_filter;各订阅的 nodeDns → dns_node_resolver;
//   - routing → routing_policies;clientRoutes → client_routes;directBypass / directForNodes → direct_bypass;rejectQuic → reject_quic;
//   - tun → tun_settings;ipv6 / ipv6Proxy → ipv6_settings;bypassPorts / bypassPortsWhitelist / bypassPortsMode → bypass_ports;
//   - servers → shared_servers;chainProxies → chain_proxies;
//   - 节点组、订阅(不含 nodeDns)、节点、测速地址与期望状态码、节点评分权重 → nodes_and_groups;
//   - 其余落在「系统」的键没有对应的明细码:只有原因,前端按原因显示。
func detailParts(in FingerprintInput) map[string]map[string]any {
	parts := make(map[string]map[string]any, len(detailOrder))
	for _, c := range detailOrder {
		parts[c] = map[string]any{}
	}
	put := func(code, key string, v any) { parts[code][key] = v }
	for k, v := range in.Profile {
		if _, ignored := profileReason(k); ignored {
			continue
		}
		switch k {
		case "dns":
			m, ok := v.(map[string]any)
			if !ok {
				put(DetailDNSUpstreams, "dns", v)
				continue
			}
			for dk, dv := range m {
				switch dk {
				case "filter":
					// 与 ComputeFingerprint 一致:只看 FilterKey(见下)
				case "mode":
					put(DetailDNSMode, dk, dv)
				case "listen53":
					put(DetailDNSListen53, dk, dv)
				case "fakeIpForProxy", "fakeIpRange":
					put(DetailDNSFakeIP, dk, dv)
				case "domainUpstreams":
					put(DetailDNSDomainUpstreams, dk, dv)
				case "rewrite":
					put(DetailDNSRewrite, dk, dv)
				default:
					put(DetailDNSUpstreams, dk, dv)
				}
			}
		case "region":
			put(DetailDNSUpstreams, "profile.region", v)
		case "routing":
			put(DetailRoutingPolicies, k, v)
		case "clientRoutes":
			put(DetailClientRoutes, k, v)
		case "directBypass", "directForNodes":
			put(DetailDirectBypass, k, v)
		case "rejectQuic":
			put(DetailRejectQUIC, k, v)
		case "tun":
			put(DetailTunSettings, k, v)
		case "ipv6", "ipv6Proxy":
			put(DetailIPv6Settings, k, v)
		case "bypassPorts", "bypassPortsWhitelist", "bypassPortsMode":
			put(DetailBypassPorts, k, v)
		case "servers":
			put(DetailSharedServers, k, v)
		case "chainProxies":
			put(DetailChainProxies, k, v)
		case "testUrl", "directTestUrl", "testExpectedStatus", "nodeScoreWeights":
			put(DetailNodesAndGroups, "profile."+k, v)
		}
	}
	put(DetailDNSFilter, "filter", in.FilterKey)
	put(DetailNodesAndGroups, "groups", in.Groups)
	type resolver struct {
		ID      string         `json:"id"`
		NodeDNS map[string]any `json:"nodeDns"`
	}
	var resolvers []resolver
	var subList []subInput
	for _, r := range in.Subs {
		if !r.Enabled {
			continue
		}
		resolvers = append(resolvers, resolver{ID: r.ID, NodeDNS: r.NodeDNS})
		subList = append(subList, subInput{ID: r.ID, Name: r.Name, URL: r.URL, URLs: r.URLs, URLEntries: r.URLEntries, DirectSubscription: r.DirectSubscription, DirectNodes: r.DirectNodes})
	}
	put(DetailDNSNodeResolver, "resolvers", resolvers)
	put(DetailNodesAndGroups, "subscriptions", subList)
	type nodeInput struct {
		Subscription string `json:"subscription"`
		Node         any    `json:"node"`
	}
	nodes := make([]nodeInput, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		nodes = append(nodes, nodeInput{Subscription: n.SubscriptionID, Node: n.Node})
	}
	put(DetailNodesAndGroups, "nodes", nodes)
	return parts
}

// ComputeDetailFingerprint 按明细码分组算指纹,键带 detailKeyPrefix,可以直接并进 ComputeFingerprint 的结果。
func ComputeDetailFingerprint(in FingerprintInput) Fingerprint {
	parts := detailParts(in)
	fp := make(Fingerprint, len(parts))
	for c, p := range parts {
		fp[detailKeyPrefix+c] = hashOf(p)
	}
	return fp
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
	fp := ComputeFingerprint(in)
	for k, v := range ComputeDetailFingerprint(in) {
		fp[k] = v
	}
	return fp, nil
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

// PendingRestart 对应前端的 pendingRestart。Reasons / Details 永远是数组(不是 null),按固定顺序。
// Details 是更细的明细码(见 detailOrder):空数组 = 判断不出具体改了什么,前端回退到按 Reasons 显示。
type PendingRestart struct {
	Pending bool     `json:"pending"`
	Reasons []string `json:"reasons"`
	Details []string `json:"details"`
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

// AdoptBaseline 在进程启动时把当前输入当作「已应用」的基线。
//
// 为什么需要它:自研后端与官方面板(以及手动改配置)都会重启内核并把新配置写进
// mihomo.yaml / config.json,但**只有自研后端的部署器会写 applied-config.json**。
// 于是「官方面板部署过、之后换成自研后端」这种场景下,记录停留在很久以前,
// 一接管就报一堆「有改动要重启内核」,而其实内核跑的已经是最新配置(2026-11-27 真机现象:
// 官方面板时角标 0,换自研后端立刻变 2,对比发现 applied 记录停在 11-26、mihomo.yaml 是 16:15 的)。
//
// 判据:已部署配置文件的修改时间比记录时间**新** => 说明中间有别人部署过,接管现状为基线;
// 记录不存在、或配置文件比记录旧(说明是自研后端自己部署的)时什么都不做。
// deployedFile 为空或文件读不到时也不做(推不出结论就不猜)。
func (a *Applied) AdoptBaseline(ctx context.Context, deployedFile string) (bool, error) {
	if deployedFile == "" {
		return false, nil
	}
	st, err := os.Stat(deployedFile)
	if err != nil {
		return false, nil
	}
	var rec appliedFile
	has, err := a.Store.Load(fileApplied, &rec)
	if err != nil {
		return false, err
	}
	// 没有记录:第一次接管,直接把现状当基线(否则一开始就会报全部改动)
	if !has {
		fp, ferr := a.Source.Fingerprint(ctx)
		if ferr != nil || len(fp) == 0 {
			return false, ferr
		}
		return true, a.Record(fp)
	}
	// 配置文件比记录新 = 中间被外部部署过
	if st.ModTime().UnixMilli() <= rec.At {
		return false, nil
	}
	fp, ferr := a.Source.Fingerprint(ctx)
	if ferr != nil || len(fp) == 0 {
		return false, ferr
	}
	return true, a.Record(fp)
}

// Pending 比较存着的记录和当前输入。没有记录就是「没有」,读不到记录或算不出当前指纹返回错误(调用方当作没有)。
func (a *Applied) Pending(ctx context.Context) (PendingRestart, error) {
	out := PendingRestart{Reasons: []string{}, Details: []string{}}
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
	if out.Pending {
		out.Details = pendingDetails(rec.Fingerprint, cur)
	}
	return out, nil
}

// pendingDetails 比较两份指纹里的明细。当前指纹带明细、而上次部署的记录里一个明细都没有(旧版本写的),
// 说明只知道原因、不知道具体改了什么:给 legacy_deploy,提示重新部署一次让记录带上明细。
// 当前指纹不带明细(别的 FingerprintSource)就不产出明细,前端回退到原因。
func pendingDetails(rec, cur Fingerprint) []string {
	out := []string{}
	hasDetail := func(fp Fingerprint) bool {
		for k := range fp {
			if strings.HasPrefix(k, detailKeyPrefix) {
				return true
			}
		}
		return false
	}
	if !hasDetail(cur) {
		return out
	}
	if !hasDetail(rec) {
		return append(out, DetailLegacyDeploy)
	}
	for _, c := range detailOrder {
		if rec[detailKeyPrefix+c] != cur[detailKeyPrefix+c] {
			out = append(out, c)
		}
	}
	return out
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
