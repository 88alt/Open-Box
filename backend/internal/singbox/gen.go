// Package singbox 负责「生成 sing-box 配置并校验」(M1)。
//
// 生成本身由 openbox-core 的 configDetailed 完成(节点出站、节点组、站点集、DNS、路由、入站……),
// 这里只做装配:把订阅节点池、节点组、档案、链式代理、系统信息按 core 要的形状交给它。
// 校验是 `sing-box check -c <文件>`。本包不启停 sing-box,也不改系统(见 DESIGN「变更安全」)。
package singbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/sub"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
)

const fileRuntime = "runtime" // {"clashApiSecret": "..."}

// ErrCoreUnavailable:openbox-core 不可用。
var ErrCoreUnavailable = errors.New("openbox-core 不可用,无法生成配置")

// Caller 是用到的核心调用(*core.Client 满足)。
type Caller interface {
	CallInto(ctx context.Context, method string, args []any, out any, opts ...core.CallOption) error
}

// Profiles 读取完整档案;Groups 读取节点组;Catalog 读取订阅与节点。
type (
	Profiles interface {
		Get(ctx context.Context) (map[string]any, error)
	}
	Groups interface {
		Groups(ctx context.Context) ([]any, error)
	}
	Catalog interface {
		Catalog(ctx context.Context) ([]subs.Record, []subs.CatalogNode, error)
	}
)

// System 提供和这台机器有关的输入;测试里换成固定值。
type System struct {
	Resolvers    func() []string // 系统 DNS(给直连解析用),没有就空
	LocalSubnets func() []string // 本机局域网网段(tun 排除用)
}

// Options 是装配用的路径和平台。
type Options struct {
	Platform   string // openwrt / systemd / container,默认 openwrt
	RulesetDir string // 规则集(.srs)目录
	CacheFile  string // sing-box 的 cache.db
	CertPath   string // 共享网络服务端的证书
	KeyPath    string
}

// Generator 生成配置。
type Generator struct {
	core     Caller
	st       *store.Store
	profiles Profiles
	groups   Groups
	cat      Catalog
	sys      System
	opt      Options
}

// NewGenerator 创建生成器。core 为 nil 时 Build 返回 ErrCoreUnavailable。
func NewGenerator(c Caller, st *store.Store, p Profiles, g Groups, cat Catalog, sys System, opt Options) *Generator {
	if opt.Platform == "" {
		opt.Platform = "openwrt"
	}
	return &Generator{core: c, st: st, profiles: p, groups: g, cat: cat, sys: sys, opt: opt}
}

// Built 是一次生成的结果。
type Built struct {
	Config         map[string]any `json:"config"`
	DNSRuleOwners  any            `json:"dnsRuleOwners"`
	DirectHosts    any            `json:"directHosts"`
	Failover       any            `json:"failover"`
	NodeCount      int            `json:"-"`
	ProfileSecret  string         `json:"-"`
	rawConfigBytes []byte
}

// JSON 返回配置的紧凑 JSON(给落盘 / 校验用)。
func (b *Built) JSON() ([]byte, error) {
	if b.rawConfigBytes != nil {
		return b.rawConfigBytes, nil
	}
	return json.Marshal(b.Config)
}

// clashSecret 取(没有就生成并保存)内核控制接口的密钥:重启之后保持不变,面板才连得上。
func (g *Generator) clashSecret() (string, error) {
	var rt struct {
		ClashAPISecret string `json:"clashApiSecret"`
	}
	err := g.st.Update(fileRuntime, &rt, func() error {
		if rt.ClashAPISecret != "" {
			return nil
		}
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		rt.ClashAPISecret = hex.EncodeToString(b)
		return nil
	})
	return rt.ClashAPISecret, err
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

func nodeJSON(cn subs.CatalogNode) (map[string]any, error) {
	m, err := toMap(cn.Node)
	if err != nil {
		return nil, err
	}
	m["subscriptionId"] = cn.SubscriptionID // 节点 DNS(按订阅配置)靠它找到所属订阅
	return m, nil
}

// chainNodes 把档案里启用的链式代理解析成候选节点:tag = 名字,detour = 上游,chain = true。
// 解析不了的跳过(core 还会再核对上游是否存在 / 成环)。
func chainNodes(profile map[string]any) []any {
	list, _ := profile["chainProxies"].([]any)
	out := []any{}
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if en, has := m["enabled"].(bool); has && !en {
			continue
		}
		name, _ := m["name"].(string)
		link, _ := m["link"].(string)
		up, _ := m["upstream"].(string)
		name = strings.TrimSpace(name)
		if name == "" || strings.TrimSpace(link) == "" || up == "" {
			continue
		}
		res, err := sub.Parse([]byte(link))
		if err != nil || len(res.Nodes) == 0 {
			continue
		}
		n, err := toMap(res.Nodes[0])
		if err != nil {
			continue
		}
		n["tag"], n["detour"], n["chain"] = name, up, true
		delete(n, "originalTag")
		delete(n, "regionCode")
		out = append(out, n)
	}
	return out
}

// Override 让一次生成临时换掉一部分输入(测速用),不落盘、不影响已保存的数据。
type Override struct {
	// Nodes 非 nil:只用这些节点(带 subscriptionId),订阅换成 Subs,节点组与档案里的链式代理都不带
	// (核心补内置的直连 / 拒绝)。用于测还没保存的订阅预览。
	Nodes []any
	Subs  []any
	// ExtraChains 额外加上的链式节点(tag / detour / chain 已填好),用于测链式代理编辑框里还没保存的那一份。
	ExtraChains []any
}

// PreviewOverride 把一份预览出来的节点(还没保存的订阅)变成 Override。
func PreviewOverride(nodes []sub.Node, nodeDNS map[string]any, name string) (*Override, error) {
	const previewID = "preview"
	o := &Override{Nodes: []any{}, Subs: []any{map[string]any{"id": previewID, "name": name, "enabled": true, "nodeDns": nodeDNS}}}
	for _, n := range nodes {
		m, err := nodeJSON(subs.CatalogNode{SubscriptionID: previewID, SubscriptionName: name, Node: n})
		if err != nil {
			return nil, err
		}
		o.Nodes = append(o.Nodes, m)
	}
	return o, nil
}

// ChainOverride 把一条链式代理(分享链接 + 上游)变成只含它的 Override;链接解析不了返回错误。
func ChainOverride(tag, link, upstream string) (*Override, error) {
	res, err := sub.Parse([]byte(link))
	if err != nil || len(res.Nodes) == 0 {
		return nil, errors.New("链式代理的链接解析不了")
	}
	n, err := toMap(res.Nodes[0])
	if err != nil {
		return nil, err
	}
	n["tag"], n["detour"], n["chain"] = tag, upstream, true
	delete(n, "originalTag")
	delete(n, "regionCode")
	return &Override{ExtraChains: []any{n}}, nil
}

// Build 调用核心生成配置。
func (g *Generator) Build(ctx context.Context) (*Built, error) { return g.BuildWith(ctx, nil) }

// BuildWith 与 Build 相同,但可用 Override 临时替换节点或加链式节点。o 为 nil 就是 Build。
func (g *Generator) BuildWith(ctx context.Context, o *Override) (*Built, error) {
	if g.core == nil {
		return nil, ErrCoreUnavailable
	}
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
	secret, err := g.clashSecret()
	if err != nil {
		return nil, fmt.Errorf("读取内核控制密钥失败: %w", err)
	}

	replaced := o != nil && o.Nodes != nil
	nodes := make([]any, 0, len(cnodes))
	for _, cn := range cnodes {
		m, err := nodeJSON(cn)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, m)
	}
	subList := make([]any, 0, len(recs))
	for _, r := range recs {
		if replaced {
			break
		}
		if !r.Enabled {
			continue
		}
		m, err := toMap(r)
		if err != nil {
			return nil, err
		}
		subList = append(subList, m)
	}

	p := make(map[string]any, len(prof)+1)
	for k, v := range prof {
		p[k] = v
	}
	p["clashApiSecret"] = secret

	var chains []any
	if replaced {
		nodes, subList, groups = o.Nodes, o.Subs, []any{}
	} else {
		chains = chainNodes(prof)
	}
	if o != nil {
		chains = append(chains, o.ExtraChains...)
	}
	if chains == nil {
		chains = []any{}
	}

	var resolvers, subnets []string
	if g.sys.Resolvers != nil {
		resolvers = g.sys.Resolvers()
	}
	if g.sys.LocalSubnets != nil {
		subnets = g.sys.LocalSubnets()
	}
	args := map[string]any{
		"platform":      g.opt.Platform,
		"cacheFilePath": g.opt.CacheFile,
		"rulesetDir":    g.opt.RulesetDir,
		"tlsCert":       map[string]any{"certPath": g.opt.CertPath, "keyPath": g.opt.KeyPath},
		"selections":    map[string]any{},
		"nodes":         nodes,
		"userGroups":    groups,
		"subscriptions": subList,
		"profile":       p,
		"systemDns":     strs(resolvers),
		"localSubnets":  strs(subnets),
		"chainNodes":    chains,
	}
	var res struct {
		Config        json.RawMessage `json:"config"`
		DNSRuleOwners any             `json:"dnsRuleOwners"`
		DirectHosts   any             `json:"directHosts"`
		Failover      any             `json:"failover"`
	}
	if err := g.core.CallInto(ctx, "configDetailed", []any{args}, &res); err != nil {
		return nil, err
	}
	b := &Built{DNSRuleOwners: res.DNSRuleOwners, DirectHosts: res.DirectHosts, Failover: res.Failover,
		NodeCount: len(nodes), ProfileSecret: secret, rawConfigBytes: []byte(res.Config)}
	if err := json.Unmarshal(res.Config, &b.Config); err != nil {
		return nil, fmt.Errorf("核心返回的配置不是 JSON 对象: %w", err)
	}
	return b, nil
}

func strs(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// Redacted 返回去掉密钥的配置副本(给前端预览用,不让密钥出现在页面里)。
func Redacted(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	if exp, ok := cfg["experimental"].(map[string]any); ok {
		e := make(map[string]any, len(exp))
		for k, v := range exp {
			e[k] = v
		}
		if api, ok := e["clash_api"].(map[string]any); ok {
			a := make(map[string]any, len(api))
			for k, v := range api {
				a[k] = v
			}
			if _, has := a["secret"]; has {
				a["secret"] = "***"
			}
			e["clash_api"] = a
		}
		out["experimental"] = e
	}
	return out
}
