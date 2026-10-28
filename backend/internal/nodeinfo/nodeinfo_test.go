package nodeinfo

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/sub"
)

func pool(nodes ...sub.Node) func(context.Context) ([]sub.Node, error) {
	return func(context.Context) ([]sub.Node, error) { return nodes, nil }
}

func lookup(m map[string][]string, err error) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, h string) ([]netip.Addr, error) {
		if err != nil {
			return nil, err
		}
		var out []netip.Addr
		for _, s := range m[h] {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

var vless = sub.Node{
	Tag: "HK 01", Type: "vless", Server: "hk.example.com", ServerPort: 443,
	Fields: map[string]any{
		"transport": map[string]any{"type": "ws"},
		"tls":       map[string]any{"enabled": true, "reality": map[string]any{"enabled": true}},
	},
}

func TestGetPoolNodeResolvesAndSplitsFamilies(t *testing.T) {
	s := &Service{Pool: pool(vless), Lookup: lookup(map[string][]string{"hk.example.com": {"2001:db8::1", "203.0.113.9", "203.0.113.2"}}, nil)}
	info, err := s.Get(context.Background(), "HK 01")
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != "vless" || !reflect.DeepEqual(info.Protocol, []string{"vless", "ws", "tls", "reality"}) || info.Server != "hk.example.com" || info.Port == nil || *info.Port != 443 {
		t.Errorf("基本信息: %+v", info)
	}
	if !reflect.DeepEqual(info.Addresses.V4, []string{"203.0.113.2", "203.0.113.9"}) || !reflect.DeepEqual(info.Addresses.V6, []string{"2001:db8::1"}) || info.ResolveError != "" {
		t.Errorf("地址: %+v %q", info.Addresses, info.ResolveError)
	}
	if info.EntryCountry != "" || info.Detour != "" {
		t.Errorf("入口国家这里不查,池里的节点没有上游: %+v", info)
	}
}

func TestGetIPLiteralNeedsNoLookup(t *testing.T) {
	n := sub.Node{Tag: "ip", Type: "trojan", Server: "198.51.100.7", ServerPort: 0, Fields: map[string]any{}}
	s := &Service{Pool: pool(n), Lookup: func(context.Context, string) ([]netip.Addr, error) {
		t.Fatal("IP 字面量不该走解析")
		return nil, nil
	}}
	info, err := s.Get(context.Background(), "ip")
	if err != nil || !reflect.DeepEqual(info.Addresses.V4, []string{"198.51.100.7"}) || info.Port != nil {
		t.Fatalf("%+v %v", info, err)
	}
	if !reflect.DeepEqual(info.Protocol, []string{"trojan"}) {
		t.Errorf("没有传输和 tls 就只有类型: %v", info.Protocol)
	}
	v6 := sub.Node{Tag: "v6", Type: "ss", Server: "[2001:db8::5]", ServerPort: 1}
	s.Pool = pool(v6)
	if info, _ := s.Get(context.Background(), "v6"); !reflect.DeepEqual(info.Addresses.V6, []string{"2001:db8::5"}) {
		t.Errorf("带方括号的 IPv6: %+v", info.Addresses)
	}
}

func TestGetResolveFailuresAreReportedNotFatal(t *testing.T) {
	s := &Service{Pool: pool(vless)}
	s.Lookup = lookup(nil, errors.New("boom"))
	info, err := s.Get(context.Background(), "HK 01")
	if err != nil || !strings.Contains(info.ResolveError, "解析失败") || info.Addresses.V4 == nil || info.Addresses.V6 == nil {
		t.Fatalf("解析失败应带原因、地址数组不为 nil: %+v %v", info, err)
	}
	s.Lookup = lookup(map[string][]string{"hk.example.com": {"198.18.0.5", "198.19.255.1"}}, nil)
	info, _ = s.Get(context.Background(), "HK 01")
	if !strings.Contains(info.ResolveError, "假 IP") || len(info.Addresses.V4) != 0 {
		t.Errorf("fake-ip 段不是真实地址: %+v", info)
	}
	s.Lookup = lookup(map[string][]string{"hk.example.com": {"198.18.0.5", "203.0.113.9"}}, nil)
	if info, _ := s.Get(context.Background(), "HK 01"); !reflect.DeepEqual(info.Addresses.V4, []string{"203.0.113.9"}) || info.ResolveError != "" {
		t.Errorf("混着时只留真实地址: %+v", info)
	}
	s.Lookup = lookup(map[string][]string{}, nil)
	if info, _ := s.Get(context.Background(), "HK 01"); info.ResolveError == "" {
		t.Error("一个地址都没有应带原因")
	}
	noHost := sub.Node{Tag: "x", Type: "ss"}
	s.Pool = pool(noHost)
	if info, _ := s.Get(context.Background(), "x"); info.ResolveError == "" {
		t.Error("没有服务器地址应带原因")
	}
}

func TestGetChainProxy(t *testing.T) {
	link := "trojan://pw@chain.example.net:8443?security=tls#c"
	prof := map[string]any{"chainProxies": []any{
		map[string]any{"name": "跳板", "link": link, "upstream": "HK 01", "enabled": true},
		map[string]any{"name": "停用的", "link": link, "upstream": "HK 01", "enabled": false},
		map[string]any{"name": "坏链接", "link": "not a link", "upstream": "HK 01"},
	}}
	s := &Service{
		Pool:    pool(vless),
		Profile: func(context.Context) (map[string]any, error) { return prof, nil },
		Lookup:  lookup(map[string][]string{"chain.example.net": {"203.0.113.50"}}, nil),
	}
	info, err := s.Get(context.Background(), "跳板")
	if err != nil || info.Type != "trojan" || info.Detour != "HK 01" || info.Server != "chain.example.net" || *info.Port != 8443 || !reflect.DeepEqual(info.Addresses.V4, []string{"203.0.113.50"}) {
		t.Fatalf("链式代理: %+v %v", info, err)
	}
	if _, err := s.Get(context.Background(), "停用的"); !errors.Is(err, ErrNotFound) {
		t.Errorf("停用的链式代理应 404: %v", err)
	}
	if _, err := s.Get(context.Background(), "坏链接"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("链接解析不了应是别的错误: %v", err)
	}
}

func TestGetNotFoundAndPrecedence(t *testing.T) {
	prof := map[string]any{"chainProxies": []any{map[string]any{"name": "HK 01", "link": "trojan://p@x.example.org:1#a", "upstream": "u"}}}
	s := &Service{Pool: pool(vless), Profile: func(context.Context) (map[string]any, error) { return prof, nil }, Lookup: lookup(nil, nil)}
	if info, _ := s.Get(context.Background(), "HK 01"); info.Type != "vless" || info.Detour != "" {
		t.Errorf("同名时节点池优先: %+v", info)
	}
	for _, tag := range []string{"", "  ", "不存在"} {
		if _, err := s.Get(context.Background(), tag); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q 应 ErrNotFound: %v", tag, err)
		}
	}
	bad := &Service{Pool: func(context.Context) ([]sub.Node, error) { return nil, errors.New("读不了") }}
	if _, err := bad.Get(context.Background(), "x"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("读节点池失败应原样返回: %v", err)
	}
}
