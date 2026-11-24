package mihomo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// 装配整份 mihomo.yaml(双内核 3e-5;对应上游 v0.1.462 Node 的 di 本体)。
//
// 这一步把 3e-1 ~ 3e-4 的产物拼起来:组与代理(core)、规则(core)、DNS 段(3e-2)、
// 入口旁路的「直连 IP 段」与 nft(3e-4),再按上游字面量的键序写成一份配置。
// 上游是闭源服务端在跑,看不到它把这份对象落成 YAML 的细节;**键序按 di 里的字面量顺序**,
// 条件键(`...cond?{k:v}:{}`)按同样的条件展开。
//
// 与上游的两处有意省略:
//   - `resolveDirectHosts`(装配时现查节点域名)默认关:需要联网的解析器由调用方注入 directHostLookup 才有;
//     没注入时按上游「没给 resolver」的样子算(不产生 nodeDirectResolution)。
//   - 已部署配置的复用由调用方判断后传 DeployedBypass(in 里),这里不自己比指纹。

// Mihomo 配置里的固定项(上游 di / 常量)。
const (
	mihomoMixedPort         = 7891
	mihomoRoutingMark       = 8236 // Number("0x2024")
	mihomoAdmitDNSPort      = 7855 // Ia
	mihomoHealthHost        = "openbox-health.invalid"
	mihomoTrafficHealthHost = "openbox-traffic-health.invalid"
	mihomoHealthAddr        = "192.0.2.1"
	mihomoTrafficHealthAddr = "127.0.0.1"
	// Mihomo 自己的两个假 IP 池(与 sing-box 的 198.19.0.0/16 不同,别混)。
	mihomoPoolV4      = "198.18.0.0/16"
	mihomoPoolV6      = "2001:2:0:8000::/49"
	mihomoRejectV4    = "198.19.0.0/16"
	mihomoRejectV6    = "2001:2::/49"
	mihomoEntryDirect = "ob-entry-direct"
	// 共享网络入口的名字前缀与终端自定义 DNS 的 tag 前缀(上游 vu / Df)。
	shareNamePrefix  = "share-"
	routeDNSPrefix   = "dns-terminal-custom-"
	defaultBootstrap = "1.1.1.1"
)

// defaultTunDNS 是 TUN 的 DNS 地址(上游 Qa)。
var defaultTunDNS = TunDNS{V4: "198.18.0.2", V6: "fdfe:dcba:9876::2"}

// DeployedBypass 是「上一份配置里的入口旁路可以整块复用」时的快照(上游 r.deployedBypass)。
type DeployedBypass struct {
	Provider *Provider
	Entry    string
}

// BuildInput 是装配一份 Mihomo 配置要用到的全部数据(字段名对齐上游 di 的解构参数)。
type BuildInput struct {
	Platform       string           // openwrt / container / linux …
	DataDir        string           // 数据目录(Mihomo 的 -d 是 <DataDir>/mihomo)
	Profile        map[string]any   // 档案
	Nodes          []map[string]any // 节点
	Groups         []map[string]any // 用户分组
	Subscriptions  []map[string]any // 订阅(取节点 DNS 与直连主机)
	RuleLists      map[string]any   // 用户规则集链接(tag → {url, domain, ip})
	Selections     map[string]any   // 面板里选中的节点
	SystemDNS      []any            // 系统 DNS(core 的 mihomoDnsUpstreams 用)
	Secret         string           // external-controller 的密钥
	ChainNodes     []any            // 链式节点(上游 et(profile))
	ClientBindings any              // 上游 r.clientBindings(现在 core 不产出 ipv6Only 行,留位)
	LanIfaces      []string         // 局域网口(入口 nft 用;调用方读好传进来)

	// 已部署配置里的 rule-providers(tag → 条目):能复用就不再转换规则集。
	DeployedProviders     map[string]Provider
	AllowNewRuleProviders bool
	// DeployedBypass 不为空 = 入口旁路整块复用(含 nft 与直连 IP 段)。
	DeployedBypass *DeployedBypass
	// BaseCacheKey 是上游的 baseCacheKey(非空时组与规则的缓存键以它打底)。
	BaseCacheKey string
}

// BuildDeps 是装配要的外部能力。
type BuildDeps struct {
	FS         FS
	Compiler   *Compiler
	Converter  *Converter        // 规则集转 provider(3e-3)
	Deployed   *DeployedReader   // 读回已部署 provider 里的网段(3e-4d)
	EntryCache *EntryDirectCache // 可 nil

	// Bypass 是入口旁路要的入参(规则集形状 / 用户规则集 / .srs 路径)。
	Bypass EntryBypassDeps
	// RulesDir 是 provider 落盘目录(<DataDir>/mihomo/rules)。
	RulesDir string
	// MihomoDir 是 Mihomo 的 -d 目录(<DataDir>/mihomo)。
	MihomoDir string

	// ReadFile 读证书(共享网络入口的 TLS);nil 表示没有 TLS 入口,遇到 TLS 入口就报错。
	ReadFile func(path string) (string, error)
	TLSCert  string
	TLSKey   string

	// LookupHost 是「装配时现查节点域名」用的解析器(上游 r.directHostLookup);nil = 不查。
	LookupHost         func(ctx context.Context, hosts []string, servers []any) ([]string, error)
	ResolveDirectHosts bool
}

// BuildResult 是装配的产物。Config 直接写成 mihomo.yaml;Entry 写成 mihomo-entry-bypass.nft。
type BuildResult struct {
	Config               *OrderedMap
	Entry                string
	Failover             json.RawMessage
	Monitoring           json.RawMessage
	NodeDirectResolution any
	Providers            map[string]Provider
	// Selections 是装配后各组此刻的第一选择({组名: 节点名}),切换成功后存成选中项快照。
	Selections map[string]string
}

// BuildConfig 对应上游 di:装配整份配置。
func BuildConfig(ctx context.Context, in BuildInput, d BuildDeps) (*BuildResult, error) {
	platform := in.Platform
	if platform == "" {
		platform = "linux"
	}
	profile := in.Profile
	if profile == nil {
		profile = map[string]any{}
	}
	// dnsmasq 只有 OpenWrt 有;别的平台按劫持算(上游 di 开头那一句)。
	if platform != "openwrt" && nestedString(profile, "dns", "mode") == "dnsmasq" {
		profile = withDNSMode(profile, "hijack")
	}
	if strings.TrimSpace(in.Secret) == "" {
		return nil, errors.New("缺少 Open-Box 内核控制密钥")
	}
	if d.Compiler == nil {
		return nil, errors.New("mihomo: 装配需要 Compiler")
	}
	if d.FS == nil {
		d.FS = OSFS{}
	}
	rulesDir := d.RulesDir
	mihomoDir := d.MihomoDir
	if rulesDir == "" {
		rulesDir = rulesDirOf(in.DataDir)
	}
	if mihomoDir == "" {
		mihomoDir = mihomoDirOf(in.DataDir)
	}
	if err := d.FS.MkdirAll(rulesDir); err != nil {
		return nil, err
	}

	builtin, err := d.Compiler.BuiltinTags(ctx, in.Groups)
	if err != nil {
		return nil, err
	}
	routing, err := d.Compiler.NormalizeRouting(ctx, profile["routing"])
	if err != nil {
		return nil, err
	}

	// 站点集与前置自定义分流要用到的规则集(上游 p)。
	wanted, err := providerTags(routing, in.RuleLists)
	if err != nil {
		return nil, err
	}

	model, err := d.Compiler.Groups(ctx, GroupsInput{
		Profile: profile, Nodes: in.Nodes, Groups: in.Groups, ChainNodes: in.ChainNodes, Selections: in.Selections,
	}, in.BaseCacheKey)
	if err != nil {
		return nil, err
	}
	classOf := model.ClassOf()

	// 节点的「节点 DNS」:域名节点补一条 openbox-node-dns。
	proxies := withNodeDNS(model.Proxies, in.Nodes, in.Subscriptions)

	directTag := targetOf(model, "direct", "DIRECT")
	blockTag := targetOf(model, "block", "REJECT")

	dnsMode := nestedString(profile, "dns", "mode")
	if dnsMode == "" {
		dnsMode = "hijack"
	}
	autoRedirect := true
	if v, ok := nestedValue(profile, "tun", "autoRedirect").(bool); ok && !v {
		autoRedirect = false
	}
	if dnsMode == "off" {
		autoRedirect = false
	}
	ipv6Mode := IPv6Mode(profile)

	opts := RouteOpts{
		DNSMode: dnsMode, AutoRedirect: autoRedirect, PureTun: !autoRedirect,
		DirectBypass: boolPtr(profile["directBypass"]),
		SplitDNS:     isTrue(nestedValue(profile, "dns", "split")),
		DirectTag:    builtin.Direct, BlockTag: builtin.Block,
	}
	admitDNS := AdmitDNSEnabled(profile["clientRoutes"], opts)

	// 终端分流:先按内置出站名规整,再把出口换成分组名;strict = 不是「不进内核」且出口不是直连 / 拒绝。
	routes := NormalizeClientRoutes(profile["clientRoutes"], builtin.Direct)
	terminalRoutes := make([]ClientRoute, 0, len(routes))
	for _, r := range routes {
		n := r
		out, known := model.Targets[r.Outbound]
		if !known {
			out = blockTag
		} else {
			out = aliasTarget(r.Outbound, model, builtin)
		}
		n.Outbound = out
		n.Strict = !r.Bypass && out != builtin.Direct && out != builtin.Block
		terminalRoutes = append(terminalRoutes, n)
	}

	// 规则集 provider:已部署的直接复用,缺的才转换。
	newTags := make([]string, 0, len(wanted))
	for _, tag := range wanted {
		if _, ok := in.DeployedProviders[tag]; !ok {
			newTags = append(newTags, tag)
		}
	}
	if in.DeployedProviders != nil && !in.AllowNewRuleProviders && len(newTags) > 0 {
		return nil, fmt.Errorf("已部署的规则集缺少 %s", newTags[0])
	}
	converted := &Converted{Providers: map[string]Provider{}, Kinds: map[string]string{}}
	if len(newTags) > 0 {
		if d.Converter == nil {
			return nil, errors.New("mihomo: 需要转换规则集,但没给 Converter")
		}
		converted, err = d.Converter.Convert(ctx, newTags)
		if err != nil {
			return nil, err
		}
	}
	providers := map[string]Provider{}
	kinds := map[string]string{}
	for _, tag := range wanted {
		p, ok := in.DeployedProviders[tag]
		if !ok {
			p = converted.Providers[tag]
		}
		providers[tag] = p
		k := converted.Kinds[tag]
		if k == "" {
			k = providerKind(p)
		}
		kinds[tag] = k
	}

	// 节点与订阅要直连解析的主机(上游 hn / U)。
	hosts := Hosts{Domains: []string{}, CIDRs: []string{}}
	if v, ok := profile["directForNodes"].(bool); !ok || v {
		hosts = HostsOf(in.Nodes, in.Subscriptions)
	}
	directCIDRs := filterDirectCIDRs(hosts.CIDRs)
	var nodeDirect any
	if d.ResolveDirectHosts && d.LookupHost != nil && len(hosts.Domains) > 0 {
		sorted := append([]string(nil), hosts.Domains...)
		sortStrings(sorted)
		key := StableJSON([]any{sorted, upstreamServersForLookup(profile, in.SystemDNS)})
		cidrs, err := d.LookupHost(ctx, sorted, in.SystemDNS)
		if err != nil {
			return nil, err
		}
		directCIDRs = mergeCIDRs(directCIDRs, filterDirectCIDRs(cidrs))
		nodeDirect = map[string]any{"key": key, "cidrs": filterDirectCIDRs(cidrs)}
	}

	rules, err := d.Compiler.TargetRules(ctx, RulesInput{
		Routing: profile["routing"], Providers: kinds, RuleLists: in.RuleLists, Targets: model.Targets,
		DirectDomains: hosts.Domains, DirectCIDRs: directCIDRs, TerminalRoutes: terminalRoutes,
	}, in.BaseCacheKey)
	if err != nil {
		return nil, err
	}

	// 入口旁路:直连 IP 段的 provider + 入口 nft。
	bypassPorts := BypassPortsOf(profile)
	admitList := TerminalSet{}
	if autoRedirect {
		admitList = AdmitLists(profile["clientRoutes"])
	}
	// 白名单端口、或有「只决定谁进内核」的终端时,入口 nft 要按局域网口写规则。
	needLanIfaces := (bypassPorts.Mode == "whitelist" && NormalizePorts(bypassPorts.Ports) != "") ||
		len(admitList.IPs) > 0
	var entryDirect EntryDirect
	reuseBypass := in.DeployedBypass != nil
	if reuseBypass {
		entryDirect = EntryDirect{FallbackKeepIPv4: []string{}}
	} else if autoRedirect && directBypassOn(profile) {
		emitted, err := d.Compiler.PublicTags(ctx, in.Groups, in.Nodes)
		if err != nil {
			return nil, err
		}
		plan, err := d.Compiler.RoutePlan(ctx, RoutePlanInput{
			Profile: profile, Builtin: builtin, Members: membersOf(emitted.PublicTags, builtin),
			Selections: in.Selections, Model: model,
		})
		if err != nil {
			return nil, err
		}
		entryDirect, err = BuildEntryDirect(ctx, plan, EntryDirectDeps{
			FS: d.FS, RulesDir: rulesDir, MihomoDir: mihomoDir,
			Bypass: d.Bypass, Deployed: in.DeployedProviders,
		}, d.EntryCache)
		if err != nil {
			return nil, err
		}
	} else {
		entryDirect = EntryDirect{FallbackKeepIPv4: []string{}}
	}

	entry := ""
	if reuseBypass {
		entry = in.DeployedBypass.Entry
		entryDirect.Provider = in.DeployedBypass.Provider
	} else {
		if needLanIfaces && len(in.LanIfaces) == 0 {
			if bypassPorts.Mode == "whitelist" && NormalizePorts(bypassPorts.Ports) != "" {
				return nil, errors.New("找不到可应用端口白名单的局域网接口")
			}
			return nil, errors.New("找不到可应用终端白名单的局域网接口")
		}
		proxyTerminals := TerminalSet{}
		for _, r := range terminalRoutes {
			if r.Bypass {
				continue
			}
			proxyTerminals.IPs = append(proxyTerminals.IPs, r.Sources...)
			if r.IPv6Only {
				proxyTerminals.IPv6Macs = append(proxyTerminals.IPv6Macs, r.Macs...)
			} else {
				proxyTerminals.Macs = append(proxyTerminals.Macs, r.Macs...)
			}
		}
		entry = BuildEntryNft(EntryNftInput{
			Ports: bypassPorts.Ports, PortsMode: bypassPorts.Mode, ClientRoutes: profile["clientRoutes"],
			AutoRedirect: autoRedirect, PureTun: !autoRedirect, LANIfaces: in.LanIfaces,
			DirectIPCidrs: entryDirect.DirectIPCidrs, FallbackKeepIPv4: entryDirect.FallbackKeepIPv4,
			TCPMss: TunOf(profile).TCPMSS, TerminalDNS: EntryLists(terminalCandidates(routes, profile, builtin), opts), DNSV6: ipv6Mode == "node",
			FakeIPCidrs:  FakeIPCidrs{V4: []string{mihomoPoolV4, mihomoRejectV4}, V6: []string{mihomoPoolV6, mihomoRejectV6}},
			AdmitDNSPort: pickInt(admitDNS, mihomoAdmitDNSPort), ProxyTerminals: proxyTerminals,
			TunDNSAddresses: defaultTunDNS, ProtectTunDNS: true,
		})
	}

	// DNS 段。
	upstreams, err := FetchDNSUpstreams(ctx, d.Compiler.call, profile, in.SystemDNS)
	if err != nil {
		return nil, err
	}
	terminalDNS := buildTerminalDNS(terminalRoutes, classOf, builtin, dnsMode, opts, upstreams, profile, ipv6Mode)
	dnsResult, err := BuildDNSConfig(d.FS, rulesDir, DNSBuildInput{
		Profile: profile, Rules: rules.Rules, ClassOf: classOf, Kinds: kinds, Upstreams: upstreams,
		TerminalDNS: terminalDNS, EndTerminal: rules.EndTerminal, DirectTerminal: rules.DirectTerminal,
		FirstPolicy: rules.FirstPolicy, IPv6Mode: ipv6Mode, Platform: platform,
		DeployedProviders: in.DeployedProviders,
	})
	if err != nil {
		return nil, err
	}
	for tag, p := range dnsResult.Providers {
		providers[tag] = p
	}

	// rules:三条健康 / 假 IP 兜底,加共享入口的 IN-NAME,再逐条补 IPv4-only 与 QUIC 的拒绝。
	finalRules := []string{
		"DOMAIN," + mihomoHealthHost + "," + directTag,
		"IP-CIDR," + mihomoRejectV4 + "," + blockTag + ",no-resolve",
		"IP-CIDR6," + mihomoRejectV6 + "," + blockTag + ",no-resolve",
	}
	servers := enabledServers(profile["servers"])
	inName := make([]string, 0, len(servers))
	for _, s := range servers {
		out := strings.TrimSpace(strOf(s["outbound"]))
		if out == "" {
			continue
		}
		target := blockTag
		if t, ok := model.Targets[out]; ok {
			target = t
		}
		inName = append(inName, "IN-NAME,"+shareName(s)+","+ruleTargetName(target))
	}
	all := append(append([]string{}, inName...), rules.Rules...)
	for i, rule := range all {
		inTerminal := i >= len(inName)+rules.FirstTerminal && i < len(inName)+rules.EndTerminal
		match, target := splitRule(rule)
		if classOf(decodeRuleTarget(target)) == "proxy" {
			if ipv6Mode == "ipv4" {
				if match == "MATCH" {
					finalRules = append(finalRules, "IP-CIDR6,::/0,"+blockTag+",no-resolve")
				} else {
					finalRules = append(finalRules, "AND,((IP-CIDR6,::/0,no-resolve),("+match+")),"+blockTag)
				}
			}
			if !inTerminal && isTrue(profile["rejectQuic"]) && !strings.Contains(match, "DST-PORT,") {
				base := "(NETWORK,udp),(DST-PORT,443)"
				m := base
				if match != "MATCH" {
					m = base + ",(" + match + ")"
				}
				finalRules = append(finalRules, "AND,("+m+"),"+blockTag)
			}
		}
		finalRules = append(finalRules, rule)
	}

	listeners, err := buildListeners(servers, d)
	if err != nil {
		return nil, err
	}

	tun := TunOf(profile)
	cfg := NewOrdered()
	cfg.Set("mode", "rule")
	cfg.Set("ipv6", isTrue(profile["ipv6"]))
	cfg.Set("allow-lan", false)
	cfg.Set("bind-address", "127.0.0.1")
	cfg.Set("external-controller", MihomoControllerAddr)
	cfg.Set("secret", in.Secret)
	cfg.Set("log-level", "warning")
	cfg.Set("mixed-port", mihomoMixedPort)
	if len(model.Builtins) > 0 {
		builtins := NewOrdered()
		for _, k := range sortedKeysOf(model.Builtins) {
			builtins.Set(k, model.Builtins[k])
		}
		cfg.Set("openbox-builtin-names", builtins)
	}
	if !autoRedirect && tun.TCPMSS != 0 {
		cfg.Set("routing-mark", mihomoRoutingMark)
	}
	hostsMap := NewOrdered()
	hostsMap.Set(mihomoHealthHost, mihomoHealthAddr)
	hostsMap.Set(mihomoTrafficHealthHost, mihomoTrafficHealthAddr)
	cfg.Set("hosts", hostsMap)
	profileSection := NewOrdered()
	profileSection.Set("store-selected", false)
	profileSection.Set("store-fake-ip", true)
	cfg.Set("profile", profileSection)

	tunSection := NewOrdered()
	tunSection.Set("enable", true)
	tunSection.Set("device", tunDevice)
	tunSection.Set("stack", tun.Stack)
	tunSection.Set("auto-route", true)
	tunSection.Set("auto-redirect", autoRedirect)
	tunSection.Set("auto-detect-interface", true)
	if ipv6Mode == "bypass" {
		tunSection.Set("inet6-address", []any{})
	}
	if !autoRedirect && len(admitList.IPs) == 0 && len(admitList.Macs) > 0 {
		tunSection.Set("include-mac-address", toAnyList(admitList.Macs))
	}
	if dnsMode == "hijack" {
		tunSection.Set("dns-mode", "hijack")
		tunSection.Set("dns-hijack", []any{"any:53", "tcp://any:53"})
	} else {
		tunSection.Set("dns-mode", "disabled")
		strict := strictTerminalExists(terminalRoutes)
		hijack := []any{}
		if strict {
			hijack = append(hijack, defaultTunDNS.V4+":53", "tcp://"+defaultTunDNS.V4+":53")
			if ipv6Mode == "node" || ipv6Mode == "ipv4" {
				hijack = append(hijack, "["+defaultTunDNS.V6+"]:53", "tcp://["+defaultTunDNS.V6+"]:53")
			}
		}
		tunSection.Set("dns-hijack", hijack)
	}
	if entryDirect.Provider != nil {
		tunSection.Set("route-exclude-address-set", []any{mihomoEntryDirect})
	}
	if tun.MTU != 0 {
		tunSection.Set("mtu", tun.MTU)
	}
	cfg.Set("tun", tunSection)

	sniffer := NewOrdered()
	sniffer.Set("enable", true)
	sniffer.Set("parse-pure-ip", true)
	sniffer.Set("override-destination", false)
	sniff := NewOrdered()
	sniff.Set("HTTP", NewOrdered())
	ports := SniffPorts(profile)
	if len(ports) > 0 {
		tls := NewOrdered()
		list := append([]any{443}, ports...)
		tls.Set("ports", dedupeAny(list))
		sniff.Set("TLS", tls)
	} else {
		sniff.Set("TLS", NewOrdered())
	}
	sniff.Set("QUIC", NewOrdered())
	sniffer.Set("sniff", sniff)
	cfg.Set("sniffer", sniffer)

	cfg.Set("dns", dnsResult.Config)
	cfg.Set("proxies", proxies)
	if len(listeners) > 0 {
		cfg.Set("listeners", listeners)
	}
	cfg.Set("proxy-groups", toAnyList2(model.Groups))
	rp := NewOrdered()
	for _, tag := range sortedKeysOf(providers) {
		p := providers[tag]
		item := NewOrdered()
		item.Set("type", p.Type)
		item.Set("behavior", p.Behavior)
		item.Set("format", p.Format)
		item.Set("path", p.Path)
		rp.Set(tag, item)
	}
	if entryDirect.Provider != nil {
		p := entryDirect.Provider
		item := NewOrdered()
		item.Set("type", p.Type)
		item.Set("behavior", p.Behavior)
		item.Set("format", p.Format)
		item.Set("path", p.Path)
		rp.Set(mihomoEntryDirect, item)
	}
	cfg.Set("rule-providers", rp)
	cfg.Set("rules", toAnyList(finalRules))

	return &BuildResult{
		Config: cfg, Entry: entry, Failover: nonEmptyJSON(model.Failover),
		Monitoring: nil, NodeDirectResolution: nodeDirect, Providers: providers,
		Selections: cloneSelections(model.First),
	}, nil
}

// ---- 小工具 ----

func rulesDirOf(dataDir string) string  { return joinPath(mihomoDirOf(dataDir), "rules") }
func mihomoDirOf(dataDir string) string { return joinPath(dataDir, "mihomo") }

// SelectionsAny 把装配后的选中项转成存储用的 map[string]any(切换成功后写快照用)。
func (r *BuildResult) SelectionsAny() map[string]any {
	out := map[string]any{}
	if r == nil {
		return out
	}
	for k, v := range r.Selections {
		if k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}

func cloneSelections(first map[string]string) map[string]string {
	out := make(map[string]string, len(first))
	for k, v := range first {
		if k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}

func nestedValue(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func nestedString(m map[string]any, keys ...string) string {
	s, _ := nestedValue(m, keys...).(string)
	return s
}

func withDNSMode(profile map[string]any, mode string) map[string]any {
	out := make(map[string]any, len(profile))
	for k, v := range profile {
		out[k] = v
	}
	dns, _ := profile["dns"].(map[string]any)
	nd := make(map[string]any, len(dns)+1)
	for k, v := range dns {
		nd[k] = v
	}
	nd["mode"] = mode
	out["dns"] = nd
	return out
}

func boolPtr(v any) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

func directBypassOn(profile map[string]any) bool {
	if v, ok := profile["directBypass"].(bool); ok {
		return v
	}
	return true
}

func pickInt(cond bool, v int) int {
	if cond {
		return v
	}
	return 0
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

func targetOf(m *GroupsModel, name, fallback string) string {
	if t, ok := m.Targets[name]; ok && t != "" {
		return t
	}
	return fallback
}

// providerKind 是上游 `N[se].behavior==="ipcidr"?"ipcidr":"domain"`。
func providerKind(p Provider) string {
	if p.Behavior == "ipcidr" {
		return "ipcidr"
	}
	return "domain"
}

// aliasTarget 对应上游 `x`:字面量 direct / block 换成内置出站此刻的名字,别的(组名、节点名)原样。
func aliasTarget(name string, m *GroupsModel, b BuiltinNames) string {
	switch name {
	case "direct":
		return b.Direct
	case "block":
		return b.Block
	}
	return name
}

// membersOf 对应上游 zr:候选出站 = 公开成员里去掉「被停用的内置直连 / 拒绝」;一个不剩就用内置直连。
func membersOf(publicTags []string, b BuiltinNames) []string {
	out := make([]string, 0, len(publicTags))
	for _, t := range publicTags {
		switch {
		case t == b.Direct:
			if b.directOn() {
				out = append(out, t)
			}
		case t == b.Block:
			if b.blockOn() {
				out = append(out, t)
			}
		default:
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return []string{b.Direct}
	}
	return out
}

func withNodeDNS(proxies []any, nodes []map[string]any, subs []map[string]any) []any {
	dns := NodeDNSOf(subs, nil)
	bySub := map[string]NodeDNS{}
	for _, n := range dns {
		bySub[n.ID] = n
	}
	byTag := map[string]map[string]any{}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		byTag[strOf(n["tag"])] = n
	}
	out := make([]any, 0, len(proxies))
	for _, p := range proxies {
		m, _ := p.(map[string]any)
		if m == nil {
			out = append(out, p)
			continue
		}
		node := byTag[strOf(m["name"])]
		sub := NodeDNS{}
		if node != nil {
			sub = bySub[strOf(node["subscriptionId"])]
		}
		server := strOf(m["server"])
		if sub.ID == "" || server == "" || isIPText(server) {
			out = append(out, p)
			continue
		}
		cp := make(map[string]any, len(m)+1)
		for k, v := range m {
			cp[k] = v
		}
		item := NewOrdered()
		item.Set("nameserver", sub.Nameserver)
		if sub.HasBootstrap {
			item.Set("bootstrap", sub.Bootstrap)
		}
		cp["openbox-node-dns"] = item
		out = append(out, cp)
	}
	return out
}

func enabledServers(v any) []map[string]any {
	var out []map[string]any
	for _, item := range rawArray(v) {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if en, ok := m["enabled"].(bool); ok && !en {
			continue
		}
		out = append(out, m)
	}
	return out
}

// shareName 是上游 vu:共享网络入口在配置里的名字。
func shareName(s map[string]any) string {
	if id, ok := s["id"].(string); ok && id != "" {
		return shareNamePrefix + id
	}
	if n, ok := s["name"].(string); ok && n != "" {
		return shareNamePrefix + n
	}
	return shareNamePrefix + "x"
}

func splitRule(rule string) (match, target string) {
	if m := ruleWithNoResolve.FindStringSubmatch(rule); m != nil {
		return m[1] + ",no-resolve", m[2]
	}
	i := strings.LastIndex(rule, ",")
	if i < 0 {
		return rule, ""
	}
	return rule[:i], rule[i+1:]
}

var (
	ruleWithNoResolve = regexp.MustCompile(`^([^,()]+,[^,()]+),([^,()]+),no-resolve$`)
	unsafeTargetRe    = regexp.MustCompile(`[,()\x00-\x1f\x7f-\x9f]`)
	// specialNamePrefix 是上游 Lu:名字不能直接写进规则时,编成 ~openbox~<base64url>。
	specialNamePrefix = "~openbox~"
	cloudflareRanges  = []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
		"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
		"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22", "2400:cb00::/32",
		"2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
		"2a06:98c0::/29", "2c0f:f248::/32",
	}
)

// isCloudflareRange 是上游 mw:节点直连解析的结果要排除 Cloudflare 的段(它们走代理才有意义)。
func isCloudflareRange(cidr string) bool {
	for _, r := range cloudflareRanges {
		if sameCIDRRange(r, cidr) {
			return true
		}
	}
	return false
}

// sameCIDRRange 判断 cidr 是否落在 r 里(上游 fl 的网段版:比前缀)。
func sameCIDRRange(r, cidr string) bool {
	a, ok := ParseCIDR(r)
	if !ok {
		return false
	}
	b, ok := ParseCIDR(cidr)
	if !ok || a.Family != b.Family || a.Prefix > b.Prefix {
		return false
	}
	shift := uint(familyBits(a.Family) - a.Prefix)
	left := new(big.Int).Rsh(a.Net, shift)
	right := new(big.Int).Rsh(b.Net, shift)
	return left.Cmp(right) == 0
}

// safeTagPart 是上游 d0:tag 里只留字母数字、下划线与横线,最多 40 个字。
func safeTagPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 40 {
			break
		}
	}
	return b.String()
}

// validDNSHost 是上游 Mf:合法 IP 或像域名(每个标签 1~63,只认字母数字与 -)。
func validDNSHost(h string) bool {
	if isIPText(h) {
		return true
	}
	if h == "" || len(h) > 253 {
		return false
	}
	for _, part := range strings.Split(h, ".") {
		if part == "" || len(part) > 63 {
			return false
		}
		for i, r := range part {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
			if !ok || (i == 0 || i == len(part)-1) && r == '-' {
				return false
			}
		}
	}
	return true
}

// ruleTargetName 对应上游 wC:名字里有逗号 / 括号 / 控制字符,或首尾有空格时,编码成 ~openbox~<base64url>。
func ruleTargetName(s string) string {
	if strings.HasPrefix(s, specialNamePrefix) || strings.TrimSpace(s) != s || unsafeTargetRe.MatchString(s) {
		return specialNamePrefix + base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	return s
}

// terminalCandidates 对应上游 F:开了分 DNS、自带 DNS、严格出口、出口是拒绝的终端分流行。
func terminalCandidates(routes []ClientRoute, profile map[string]any, b BuiltinNames) []ClientRoute {
	split := isTrue(nestedValue(profile, "dns", "split"))
	out := make([]ClientRoute, 0, len(routes))
	for _, r := range routes {
		if split || r.DNS != nil || r.Strict || r.Outbound == b.Block {
			out = append(out, r)
		}
	}
	return out
}

func strictTerminalExists(routes []ClientRoute) bool {
	for _, r := range routes {
		if r.Strict {
			return true
		}
	}
	return false
}

func nonEmptyJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

// upstreamServersForLookup 是上游 `Bn(d,c)`:直连侧的上游 DNS(只给解析器用)。
func upstreamServersForLookup(profile map[string]any, systemDNS []any) []any {
	if v, ok := nestedValue(profile, "dns", "direct").(string); ok && v != "" {
		return []any{v}
	}
	return systemDNS
}

func filterDirectCIDRs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		if c == "" || isCloudflareRange(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func mergeCIDRs(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, c := range list {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

func isIPText(s string) bool { return ipFamily(s) != 0 }

func ipFamily(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	ip := net.ParseIP(strings.Trim(s, "[]"))
	if ip == nil {
		return 0
	}
	if ip.To4() != nil && !strings.Contains(s, ":") {
		return 4
	}
	return 6
}

func toAnyList(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func toAnyList2(in []map[string]any) []any {
	out := make([]any, len(in))
	for i, m := range in {
		out[i] = m
	}
	return out
}

func dedupeAny(in []any) []any {
	seen := map[any]bool{}
	out := make([]any, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func stringList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return in
}

func sortedKeysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}

func joinPath(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			clean = append(clean, p)
		}
	}
	return filepath.Join(clean...)
}

// ---- provider 名单 ----

// providerTags 对应上游 `f.activePolicies.flatMap($s)` 加前置自定义分流里的规则集。
func providerTags(r RoutingModel, ruleLists map[string]any) ([]string, error) {
	var tags []string
	add := func(list []string) {
		for _, tag := range list {
			tags = append(tags, expandRuleset(tag, ruleLists)...)
		}
	}
	for _, p := range r.ActivePolicies {
		add(p.Rulesets)
	}
	if r.Custom.active() {
		for _, rule := range r.Custom.Rules {
			if tag := ruleSetTagOf(rule); tag != "" {
				tags = append(tags, expandRuleset(tag, ruleLists)...)
			}
		}
	}
	// 去重保序
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	// 用户规则集链接没准备好就整份报错(上游同一句文案)。
	for _, tag := range out {
		if !isListTag(tag) {
			continue
		}
		base := strings.TrimSuffix(tag, "-ip")
		entry, _ := ruleLists[base].(map[string]any)
		// 档案里没有这条链接(或没登记 domain / ip)就算没准备好——上游这时候也直接抛错。
		if entry == nil {
			return nil, errRuleListNotReady
		}
		if strings.HasSuffix(tag, "-ip") {
			if !isTrue(entry["ip"]) {
				return nil, errRuleListNotReady
			}
		} else if !isTrue(entry["domain"]) {
			return nil, errRuleListNotReady
		}
	}
	return out, nil
}

var errRuleListNotReady = errors.New("规则集链接尚未准备好，无法生成 mihomo 配置")

// expandRuleset 对应上游 $s 的单条:list-* 按规则集链接有没有 domain / ip 拆成两条,别的原样。
func expandRuleset(tag string, ruleLists map[string]any) []string {
	if !isListTag(tag) {
		return []string{tag}
	}
	entry, _ := ruleLists[tag].(map[string]any)
	if entry == nil {
		return []string{tag}
	}
	out := []string{}
	if isTrue(entry["domain"]) {
		out = append(out, tag)
	}
	if isTrue(entry["ip"]) {
		out = append(out, tag+"-ip")
	}
	return out
}

// ruleSetTagOf 对应上游 To:把一条前置自定义分流换成它引用的规则集名。
func ruleSetTagOf(rule CustomRule) string {
	switch rule.Type {
	case "geosite", "geoip":
		return rule.Type + "-" + rule.Value
	case "ruleset":
		return rule.Value
	case "ruleUrl":
		return fnvListTag(rule.Value)
	}
	return ""
}

func isListTag(tag string) bool { return strings.HasPrefix(tag, "list-") }

// fnvListTag 对应上游 Mt:规则集链接按 URL 算 list-<fnv1a 8 位十六进制>。
func fnvListTag(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return "list-" + fmt.Sprintf("%08x", h)
}

// ---- 终端 DNS ----

// buildTerminalDNS 对应上游 di 里 G 那一段:把要自定义 DNS / 严格出口的终端分流写成 DNS 段认的行。
func buildTerminalDNS(routes []ClientRoute, classOf func(string) string, builtin BuiltinNames, dnsMode string, opts RouteOpts, upstreams DNSUpstreamSet, profile map[string]any, ipv6Mode string) []any {
	blockTag := builtin.Block
	directTag := builtin.Direct
	picked := terminalCandidates(routes, profile, builtin)
	// 不进内核的排前面(上游 `[...F.filter(se=>se.bypass),...F.filter(se=>!se.bypass)]`)。
	ordered := make([]ClientRoute, 0, len(picked))
	for _, r := range picked {
		if r.Bypass {
			ordered = append(ordered, r)
		}
	}
	for _, r := range picked {
		if !r.Bypass {
			ordered = append(ordered, r)
		}
	}

	strictServer, strictBoot := strictUpstream(upstreams)
	out := make([]any, 0, len(ordered))
	for _, r := range ordered {
		if dnsMode != "hijack" && !EntryRouteActive(r, opts) {
			continue
		}
		var custom []*OrderedMap
		if r.Outbound != blockTag {
			custom = routeDNSServers(r.DNS, r.ID)
		}
		item := NewOrdered()
		item.Set("sources", toAnyList(r.Sources))
		if len(r.Macs) > 0 {
			item.Set("macs", toAnyList(r.Macs))
		}
		if r.IPv6Only {
			item.Set("ipv6-only", true)
		}
		if ipv6Mode == "ipv4" && r.Outbound != blockTag && classOf(r.Outbound) == "proxy" {
			item.Set("ipv4-only", true)
		}
		if r.Strict {
			item.Set("outbound", r.Outbound)
		}
		switch {
		case custom != nil:
			item.Set("action", "custom")
		case r.Bypass || r.Outbound == directTag:
			item.Set("action", "direct")
		case r.Outbound == blockTag:
			item.Set("action", "reject")
		default:
			item.Set("action", "proxy")
		}
		if r.Bypass {
			item.Set("bypass", true)
		}
		if custom != nil {
			item.Set("nameserver", r.DNS.URL)
			if len(custom) > 1 {
				proto := "udp"
				if r.Strict {
					proto = "tcp"
				}
				server := strOf(custom[1].vals["server"])
				item.Set("bootstrap", ServerURL(DNSServer{Protocol: proto, Server: server, Port: 53}))
			}
		} else if r.Strict && strictServer != nil {
			item.Set("nameserver", ServerURL(*strictServer))
			if strictBoot != nil {
				item.Set("bootstrap", ServerURL(*strictBoot))
			}
		}
		out = append(out, item)
	}
	return out
}

// strictUpstream 对应上游 cw:严格出口的终端没有自定义 DNS 时,用代理侧上游;
// 代理侧是「路由器自己的 DNS」时换成 tcp://1.1.1.1:53(wan 不能直接写进 Mihomo)。
func strictUpstream(u DNSUpstreamSet) (*DNSServer, *DNSServer) {
	p := u.Proxy
	if p.Wan {
		s := DNSServer{Protocol: "tcp", Server: defaultBootstrap, Port: 53}
		return &s, nil
	}
	s := p.dnsServer()
	var boot *DNSServer
	if p.Bootstrap != nil {
		boot = &DNSServer{Protocol: "tcp", Server: p.Bootstrap.Server, Port: p.Bootstrap.Port}
	}
	return &s, boot
}

// routeDNSServers 对应上游 Ff / f0:把一条终端分流的自定义 DNS 写成 Mihomo 的 dns server 列表
// (第 0 个是它自己,第 1 个是解析它域名的 bootstrap,有才给)。
func routeDNSServers(dns *RouteDNS, id string) []*OrderedMap {
	if dns == nil {
		return nil
	}
	raw := strings.TrimSpace(dns.URL)
	if raw == "" {
		return nil
	}
	tag := routeDNSPrefix + safeTagPart(id) + ".upstream"
	mk := func(typ, name, host string, port int, path string) *OrderedMap {
		m := NewOrdered()
		m.Set("type", typ)
		m.Set("tag", name)
		m.Set("server", host)
		switch typ {
		case "udp", "tcp":
			if port != 53 {
				m.Set("server_port", port)
			}
		case "tls":
			if port != 853 {
				m.Set("server_port", port)
			}
		case "https":
			if port != 443 {
				m.Set("server_port", port)
			}
			if path != "" && path != "/dns-query" {
				m.Set("path", path)
			}
		}
		return m
	}
	if typ, host, port, ok := simpleDNSURL(raw); ok {
		return []*OrderedMap{mk(typ, tag, host, port, "")}
	}
	host, port, path, typ, ok := complexDNSURL(raw)
	if !ok {
		return nil
	}
	main := mk(typ, tag, host, port, path)
	if isIPText(host) || strings.TrimSpace(dns.Bootstrap) == "" {
		return []*OrderedMap{main}
	}
	main.Set("domain_resolver", tag+"-bootstrap")
	boot := NewOrdered()
	boot.Set("type", "udp")
	boot.Set("tag", tag+"-bootstrap")
	boot.Set("server", strings.TrimSpace(dns.Bootstrap))
	return []*OrderedMap{main, boot}
}

// simpleDNSURL 对应上游 l0:udp:// / tcp:// 且主机必须是 IP。
func simpleDNSURL(raw string) (typ, host string, port int, ok bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, false
	}
	switch u.Scheme {
	case "udp", "tcp":
	default:
		return "", "", 0, false
	}
	h := strings.Trim(u.Hostname(), "[]")
	port = 53
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", "", 0, false
		}
		port = n
	}
	if !isIPText(h) || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", 0, false
	}
	if u.Path != "" && u.Path != "/" {
		return "", "", 0, false
	}
	if port < 1 || port > 65535 {
		return "", "", 0, false
	}
	return u.Scheme, h, port, true
}

// complexDNSURL 对应上游 u0 / c0:tls://(默认 853)或 https://(默认 443)。
func complexDNSURL(raw string) (host string, port int, path, typ string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, "", "", false
	}
	switch u.Scheme {
	case "tls":
		h := strings.Trim(u.Hostname(), "[]")
		if strings.ContainsAny(raw, "?# \t") || !validDNSHost(h) || u.User != nil {
			return "", 0, "", "", false
		}
		if u.Path != "" && u.Path != "/" {
			return "", 0, "", "", false
		}
		port = 853
		if p := u.Port(); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return "", 0, "", "", false
			}
			port = n
		}
		if port < 1 || port > 65535 {
			return "", 0, "", "", false
		}
		return h, port, "", "tls", true
	case "https":
		if u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return "", 0, "", "", false
		}
		if u.Port() == "0" {
			return "", 0, "", "", false
		}
		h := strings.Trim(u.Hostname(), "[]")
		port = 443
		if p := u.Port(); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return "", 0, "", "", false
			}
			port = n
		}
		p := u.Path
		if p == "" {
			p = "/"
		}
		if u.RawQuery != "" {
			p += "?" + u.RawQuery
		}
		return h, port, p, "https", true
	}
	return "", 0, "", "", false
}

// ---- 共享网络入口(listeners) ----

// buildListeners 对应上游 RE:把档案里启用的共享网络入口写成 Mihomo 的 listeners。
// 需要 TLS 的入口(tuic / hysteria2 / vless+tls)要证书:没给读取方式就报错,免得静默降级成明文。
func buildListeners(servers []map[string]any, d BuildDeps) ([]any, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	needTLS := false
	for _, s := range servers {
		if serverNeedsTLS(s) {
			needTLS = true
			break
		}
	}
	var cert, key string
	if needTLS {
		if d.ReadFile == nil || d.TLSCert == "" || d.TLSKey == "" {
			return nil, errors.New("共享网络入口需要 TLS 证书,但没给证书路径与读取方式")
		}
		c, err := d.ReadFile(d.TLSCert)
		if err != nil {
			return nil, err
		}
		k, err := d.ReadFile(d.TLSKey)
		if err != nil {
			return nil, err
		}
		cert, key = c, k
	}
	out := make([]any, 0, len(servers))
	for _, s := range servers {
		proto := strOf(s["protocol"])
		name := shareName(s)
		item := NewOrdered()
		item.Set("name", name)
		item.Set("type", proto)
		item.Set("listen", "::")
		item.Set("port", s["port"])
		switch proto {
		case "shadowsocks":
			item.Set("cipher", s["method"])
			item.Set("password", s["password"])
			item.Set("udp", true)
		case "vless":
			user := NewOrdered()
			user.Set("uuid", strOf(s["uuid"]))
			item.Set("users", []any{user})
			if serverNeedsTLS(s) {
				item.Set("certificate", cert)
				item.Set("private-key", key)
			} else {
				item.Set("allow-insecure", true)
			}
		case "tuic":
			item.Set("certificate", cert)
			item.Set("private-key", key)
			users := NewOrdered()
			users.Set(strOf(s["uuid"]), strOf(s["password"]))
			item.Set("users", users)
			item.Set("congestion-controller", "bbr")
			item.Set("alpn", []any{"h3"})
		case "hysteria2":
			item.Set("certificate", cert)
			item.Set("private-key", key)
			users := NewOrdered()
			users.Set(name, strOf(s["password"]))
			item.Set("users", users)
			item.Set("alpn", []any{"h3"})
			if obfs := strOf(s["obfs"]); obfs != "" {
				item.Set("obfs", "salamander")
				item.Set("obfs-password", obfs)
			}
		case "mixed":
			item.Set("udp", false)
			users := []any{}
			if u := strOf(s["username"]); u != "" {
				one := NewOrdered()
				one.Set("username", u)
				one.Set("password", strOf(s["password"]))
				users = append(users, one)
			}
			item.Set("users", users)
		default:
			return nil, fmt.Errorf("共享网络入口「%s」的协议无效", name)
		}
		out = append(out, item)
	}
	return out, nil
}

// serverNeedsTLS 是上游 Gm:tuic / hysteria2,以及开了 TLS 的 vless。
func serverNeedsTLS(s map[string]any) bool {
	switch strOf(s["protocol"]) {
	case "tuic", "hysteria2":
		return true
	case "vless":
		return isTrue(s["tls"])
	}
	return false
}
