// Package nodeinfo 实现 GET /api/openbox/node-info/{tag}:一个节点(或链式代理)的类型、协议层、服务器、端口、
// 链式上游,以及入口地址的解析结果。响应形状取自前端 api/openbox.ts 的 OpenboxNodeInfo,没有参照上游。
//
// 入口国家(entryCountry)要查第三方的 IP 归属:这里只带缓存里已有的(entry.go 的 EntryCountry 查并写缓存),
// 没有就是空串,前端会再调 entry-country 去问。
package nodeinfo

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

// ErrNotFound 是没有这个 tag 的节点或链式代理(404)。
var ErrNotFound = errors.New("没有这个节点")

// Addresses 是解析出来的入口地址。两个数组都不为 nil。
type Addresses struct {
	V4 []string `json:"v4"`
	V6 []string `json:"v6"`
}

// Info 对应前端的 OpenboxNodeInfo。
type Info struct {
	Tag          string    `json:"tag"`
	Type         string    `json:"type"`
	Protocol     []string  `json:"protocol"`
	Server       string    `json:"server"`
	Port         *int      `json:"port"`
	Detour       string    `json:"detour,omitempty"`
	Addresses    Addresses `json:"addresses"`
	EntryCountry string    `json:"entryCountry"`
	ResolveError string    `json:"resolveError,omitempty"`
}

// Service 查节点信息。
type Service struct {
	// Pool 返回已启用订阅的节点池。
	Pool func(ctx context.Context) ([]sub.Node, error)
	// Profile 返回完整档案(链式代理从这里取)。
	Profile func(ctx context.Context) (map[string]any, error)
	// Lookup 解析域名;nil 时用系统解析器。
	Lookup  func(ctx context.Context, host string) ([]netip.Addr, error)
	Timeout time.Duration // 解析超时;0 = 5 秒

	// Country 查一个公网 IP 的两位大写国家代码(EntryCountry 用);IsPublic 判断地址能不能拿去问。
	// 两个都要配;没配时 entry-country 不可用,node-info 的 entryCountry 恒为空串。
	Country  func(ctx context.Context, ip netip.Addr) (string, error)
	IsPublic func(ip netip.Addr) bool
	Now      func() time.Time // 缓存用的时钟;nil = 系统时钟

	countries countryCache
}

// Get 返回 tag 对应的节点信息。节点池里的优先;其次是档案里启用的链式代理(名字就是 tag)。
func (s *Service) Get(ctx context.Context, tag string) (*Info, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil, ErrNotFound
	}
	n, detour, err := s.find(ctx, tag)
	if err != nil {
		return nil, err
	}
	info := &Info{
		Tag: tag, Type: n.Type, Protocol: protocolLayers(n), Server: n.Server, Detour: detour,
		Addresses: Addresses{V4: []string{}, V6: []string{}},
	}
	if n.ServerPort > 0 {
		p := n.ServerPort
		info.Port = &p
	}
	s.resolve(ctx, info)
	s.cachedCountry(info) // 缓存里有才带;没有就留空,前端再调 entry-country
	return info, nil
}

func (s *Service) find(ctx context.Context, tag string) (sub.Node, string, error) {
	if s.Pool != nil {
		nodes, err := s.Pool(ctx)
		if err != nil {
			return sub.Node{}, "", err
		}
		for _, n := range nodes {
			if n.Tag == tag {
				return n, "", nil
			}
		}
	}
	if s.Profile != nil {
		prof, err := s.Profile(ctx)
		if err != nil {
			return sub.Node{}, "", err
		}
		list, _ := prof["chainProxies"].([]any)
		for _, e := range list {
			m, _ := e.(map[string]any)
			if m == nil {
				continue
			}
			if en, has := m["enabled"].(bool); has && !en {
				continue
			}
			name, _ := m["name"].(string)
			if strings.TrimSpace(name) != tag {
				continue
			}
			link, _ := m["link"].(string)
			up, _ := m["upstream"].(string)
			res, err := sub.Parse([]byte(link))
			if err != nil || len(res.Nodes) == 0 {
				return sub.Node{}, "", errors.New("这个链式代理的链接解析不了")
			}
			return res.Nodes[0], up, nil
		}
	}
	return sub.Node{}, "", ErrNotFound
}

// protocolLayers 把节点的协议拆成从外到内的层:类型、传输(ws / grpc 等)、tls / reality。
func protocolLayers(n sub.Node) []string {
	out := []string{}
	if n.Type != "" {
		out = append(out, n.Type)
	}
	if tr, ok := n.Fields["transport"].(map[string]any); ok {
		if t, _ := tr["type"].(string); t != "" {
			out = append(out, t)
		}
	}
	if tls, ok := n.Fields["tls"].(map[string]any); ok {
		if en, _ := tls["enabled"].(bool); en {
			out = append(out, "tls")
			if re, ok := tls["reality"].(map[string]any); ok {
				if ren, _ := re["enabled"].(bool); ren {
					out = append(out, "reality")
				}
			}
		}
	}
	return out
}

var fakeIPNet = netip.MustParsePrefix("198.18.0.0/15") // sing-box 的 fake-ip 默认段,不是节点的真实地址

func (s *Service) resolve(ctx context.Context, info *Info) {
	host := strings.TrimSpace(info.Server)
	if host == "" {
		info.ResolveError = "节点没有服务器地址"
		return
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		addrs = []netip.Addr{a}
	} else {
		to := s.Timeout
		if to <= 0 {
			to = 5 * time.Second
		}
		cctx, cancel := context.WithTimeout(ctx, to)
		defer cancel()
		lookup := s.Lookup
		if lookup == nil {
			lookup = systemLookup
		}
		got, err := lookup(cctx, host)
		if err != nil {
			info.ResolveError = describe(err)
			return
		}
		addrs = got
	}
	real := 0
	for _, a := range addrs {
		a = a.Unmap()
		if fakeIPNet.Contains(a) {
			continue
		}
		real++
		if a.Is4() {
			info.Addresses.V4 = append(info.Addresses.V4, a.String())
		} else {
			info.Addresses.V6 = append(info.Addresses.V6, a.String())
		}
	}
	sort.Strings(info.Addresses.V4)
	sort.Strings(info.Addresses.V6)
	if real == 0 {
		if len(addrs) > 0 {
			info.ResolveError = "解析到的是内核的假 IP,没有拿到真实地址"
		} else {
			info.ResolveError = "没有解析到地址"
		}
	}
}

func systemLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	return ips, err
}

func describe(err error) string {
	var de *net.DNSError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "解析超时"
	case errors.As(err, &de) && de.IsNotFound:
		return "域名不存在"
	case errors.As(err, &de) && de.IsTimeout:
		return "解析超时"
	}
	return "解析失败:" + err.Error()
}
