// Package dnstest 实现 DNS 上游测试(/api/openbox/dns/upstream-test)的服务层。
//
// 自己的设计,依据前端 testDnsUpstream / OpenboxDnsUpstreamTestResult 的注释和 DnsUpstreamCard.vue 的用法,没有参照上游:
//   - 保存前真的让内核按所选协议向那台服务器查一次(内核层见 internal/probe 的 QueryDNS);
//   - server 可填「上游 DNS」记号 wan:固定直连、固定 UDP 53,测的是系统此刻的上游 DNS 的第一台,读不到按地区退回;
//   - direct 侧从路由器直连;proxy 侧经生成配置里的兜底出站(route.final)去连——和 core 生成 DNS 时给代理侧写的 detour 一致。
//     兜底出站是自动选择组时,实际走哪个节点由内核按延迟挑,这里只能如实写组名。
//
// 测试用的域名取自档案:代理侧 testUrl 的主机、直连侧 directTestUrl 的主机;取不到用内置的默认值。
// 没有做的:解析之后再去请求测速地址那一步(前端的 warning 字段保留给以后,现在不会出现)。
package dnstest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

const (
	// WanToken 是「上游 DNS」记号(与前端、core 一致)。
	WanToken        = "wan"
	defaultPort     = 53
	defaultProxyNS  = "www.gstatic.com"
	defaultDirNS    = "connectivitycheck.platform.hicloud.com"
	wanFallbackCN   = "223.5.5.5" // 读不到系统上游 DNS 时,与 core 的 wanFallbackUpstream 一致
	wanFallbackIntl = "1.1.1.1"
)

// InputError 表示请求本身不合法(接口回 400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// UnavailableError 表示现在测不了(接口回 503):没有 sing-box、核心不可用等。
type UnavailableError struct{ Msg string }

func (e *UnavailableError) Error() string { return e.Msg }

// Request 与前端 OpenboxDnsUpstreamTestPayload 一致。
type Request struct {
	Side     string `json:"side"`
	Server   string `json:"server"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
}

// Result 与前端 OpenboxDnsUpstreamTestResult 一致。
type Result struct {
	OK       bool     `json:"ok"`
	Ms       int      `json:"ms"`
	Server   string   `json:"server"`
	Protocol string   `json:"protocol"`
	Port     int      `json:"port"`
	Via      string   `json:"via"`
	Chain    []string `json:"chain,omitempty"`
	Policy   string   `json:"policy,omitempty"`
	Wan      bool     `json:"wan,omitempty"`
	Note     string   `json:"note,omitempty"`
	Warning  string   `json:"warning,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Querier 让内核查一次(*probe.Runner 实现它),测试里换假的。
type Querier interface {
	QueryDNS(ctx context.Context, j probe.DNSJob) (probe.DNSResult, error)
}

// Tester 是 DNS 上游测试服务。
type Tester struct {
	Query Querier
	// WAN 返回系统此刻的上游 DNS;为空或返回空都按「读不到」处理。
	WAN func() []string
	// Region 返回档案里的 DNS 地区("cn" / "intl");为空或别的值按 "cn"。
	Region func(ctx context.Context) string
	// Profile 读完整档案(取 testUrl / directTestUrl);读不到用默认域名。
	Profile func(ctx context.Context) (map[string]any, error)
	// Config 生成完整的 sing-box 配置(只有代理侧要用:取兜底出站和它的节点)。
	Config func(ctx context.Context) (map[string]any, error)
}

// Test 做一次测试。参数不合法返回 *InputError;现在测不了返回 *UnavailableError;
// 测了但没通(超时、被拒绝、上游回错误码)是正常结果:OK 为假,Error 写原因。
func (t *Tester) Test(ctx context.Context, req Request) (Result, error) {
	side := strings.TrimSpace(req.Side)
	if side != "direct" && side != "proxy" {
		return Result{}, &InputError{"side 只能是 direct 或 proxy"}
	}
	proto := strings.ToLower(strings.TrimSpace(req.Protocol))
	if proto == "" {
		proto = map[string]string{"direct": "udp", "proxy": "tcp"}[side] // 与 core 的默认一致
	}
	if proto != "udp" && proto != "tcp" {
		return Result{}, &InputError{"协议只支持 udp 和 tcp"}
	}
	port := req.Port
	if port == 0 {
		port = defaultPort
	}
	if port < 1 || port > 65535 {
		return Result{}, &InputError{"端口要在 1 到 65535 之间"}
	}

	res := Result{Protocol: proto, Port: port}
	server := strings.TrimSpace(req.Server)
	detourTag := ""
	var material *probe.Material
	switch {
	case server == WanToken:
		// 上游 DNS 固定直连、固定 UDP 53(协议和端口跟着系统的上游走,请求里的不看)
		res.Wan, res.Protocol, res.Port = true, "udp", defaultPort
		res.Server, res.Note = t.wanServer(ctx)
	default:
		ip := strings.TrimSuffix(strings.TrimPrefix(server, "["), "]")
		if p := net.ParseIP(ip); p == nil || p.IsUnspecified() {
			return Result{}, &InputError{"上游地址只能是 IPv4 / IPv6 地址,或「上游 DNS」记号 wan(不收域名)"}
		}
		res.Server = ip
		if side == "proxy" {
			var err error
			detourTag, material, err = t.proxyPath(ctx, &res)
			if err != nil {
				return Result{}, err
			}
			if res.Error != "" { // 找不到代理出口:如实告诉用户,不是请求的错
				return res, nil
			}
		}
	}

	srv := map[string]any{"type": res.Protocol, "server": res.Server}
	if res.Port != defaultPort {
		srv["server_port"] = res.Port
	}
	out, err := t.Query.QueryDNS(ctx, probe.DNSJob{
		Server: srv, Detour: detourTag, Material: material, Name: t.testName(ctx, side),
	})
	switch {
	case errors.Is(err, probe.ErrNoSingBox):
		return Result{}, &UnavailableError{"找不到 sing-box 可执行文件,无法测试"}
	case err != nil:
		return Result{}, err
	}
	res.OK, res.Ms, res.Error = out.OK, out.Ms, out.Error
	return res, nil
}

// wanServer 取「上游 DNS」此刻的第一台;读不到按地区退回(与 core 的 wanFallbackUpstream 一致),并写进 note。
func (t *Tester) wanServer(ctx context.Context) (server, note string) {
	if t.WAN != nil {
		for _, s := range t.WAN() {
			if s = strings.TrimSpace(s); s != "" {
				return s, ""
			}
		}
	}
	fb := wanFallbackCN
	if t.Region != nil && t.Region(ctx) == "intl" {
		fb = wanFallbackIntl
	}
	return fb, "读不到系统的上游 DNS,这里测的是退回用的 " + fb
}

// proxyPath 为代理侧找出口:生成配置,取 route.final(兜底出站),摘出它和它依赖的东西。
// 填好 res.Via / Chain / Policy / Note。找不到出口时在 res.Error 里写原因(返回 nil 错误)。
func (t *Tester) proxyPath(ctx context.Context, res *Result) (string, *probe.Material, error) {
	if t.Config == nil {
		return "", nil, &UnavailableError{"配置生成不可用,没法测代理侧"}
	}
	cfg, err := t.Config(ctx)
	switch {
	case errors.Is(err, singbox.ErrCoreUnavailable):
		return "", nil, &UnavailableError{err.Error()}
	case err != nil:
		return "", nil, fmt.Errorf("生成配置失败: %w", err)
	}
	route, _ := cfg["route"].(map[string]any)
	final, _ := route["final"].(string)
	mat := probe.Subset(cfg, []string{final})
	if final == "" || len(mat.Outbounds) == 0 {
		res.Error = "找不到代理出口:配置里没有兜底出站(还没有订阅或节点?),没法测代理侧"
		return "", nil, nil
	}
	res.Policy = final
	res.Via, res.Chain, res.Note = describePath(cfg, final)
	return final, mat, nil
}

// testName 取测试用的域名:代理侧用 testUrl 的主机,直连侧用 directTestUrl 的主机;取不到或是 IP 用默认值。
func (t *Tester) testName(ctx context.Context, side string) string {
	key, def := "testUrl", defaultProxyNS
	if side == "direct" {
		key, def = "directTestUrl", defaultDirNS
	}
	if t.Profile == nil {
		return def
	}
	p, err := t.Profile(ctx)
	if err != nil {
		return def
	}
	raw, _ := p[key].(string)
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || net.ParseIP(u.Hostname()) != nil {
		return def
	}
	return u.Hostname()
}

// describePath 沿着出站 tag 往下走,说明这条线路实际经哪个节点:
// selector 取它当前的默认成员(没写就取第一个);urltest 由内核按延迟挑,停在组名并说明;
// 走到节点时沿 detour 往回列出逐跳线路(链首在前);走到 direct 说明是直连。
func describePath(cfg map[string]any, tag string) (via string, chain []string, note string) {
	byTag := map[string]map[string]any{}
	if list, ok := cfg["outbounds"].([]any); ok {
		for _, o := range list {
			if m, ok := o.(map[string]any); ok {
				if tg, _ := m["tag"].(string); tg != "" {
					byTag[tg] = m
				}
			}
		}
	}
	cur := tag
	for i := 0; i < 16; i++ {
		e, ok := byTag[cur]
		if !ok {
			return cur, nil, ""
		}
		typ, _ := e["type"].(string)
		switch typ {
		case "selector":
			next, _ := e["default"].(string)
			if next == "" {
				if members, _ := e["outbounds"].([]any); len(members) > 0 {
					next, _ = members[0].(string)
				}
			}
			if next == "" || next == cur {
				return cur, nil, ""
			}
			cur = next
			continue
		case "urltest":
			return cur, nil, fmt.Sprintf("出口是自动选择组「%s」,实际走哪个节点由内核按延迟挑,这里只能写组名", cur)
		case "direct":
			return "", nil, fmt.Sprintf("兜底出口「%s」现在是直连,所以这次是从路由器直连测的", tag)
		}
		hops := []string{cur}
		for j := 0; j < 8; j++ {
			d, _ := byTag[hops[0]]["detour"].(string)
			if d == "" || byTag[d] == nil {
				break
			}
			hops = append([]string{d}, hops...)
		}
		if len(hops) > 1 {
			chain = hops
		}
		return cur, chain, ""
	}
	return cur, nil, ""
}
