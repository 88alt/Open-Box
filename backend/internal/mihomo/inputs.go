package mihomo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// IPv6Mode 是档案里 ipv6 / ipv6Proxy 合成的模式:off(没开 ipv6)、ipv4(IPv6 流量不走代理、只走 IPv4)、
// bypass(IPv6 直连)、node(IPv6 也交给节点)。
func IPv6Mode(profile map[string]any) string {
	if on, _ := profile["ipv6"].(bool); !on {
		return "off"
	}
	switch p, _ := profile["ipv6Proxy"].(string); p {
	case "ipv4":
		return "ipv4"
	case "bypass":
		return "bypass"
	}
	return "node"
}

// Tun 是档案 tun 里 Mihomo 用得到的三项(取值不合格一律退回默认)。
type Tun struct {
	Stack  string // mixed / gvisor / system,默认 mixed
	MTU    int    // 0 = 内核默认,否则 1280~65535
	TCPMSS int    // 0 = 不钳制,否则 536~65495
}

// TunOf 读档案的 tun。
func TunOf(profile map[string]any) Tun {
	t := Tun{Stack: "mixed"}
	m, _ := profile["tun"].(map[string]any)
	if m == nil {
		return t
	}
	switch s, _ := m["stack"].(string); s {
	case "mixed", "gvisor", "system":
		t.Stack = s
	}
	if n, ok := intOf(m["mtu"]); ok && (n == 0 || (n >= 1280 && n <= 65535)) {
		t.MTU = n
	}
	if n, ok := intOf(m["tcpMss"]); ok && (n == 0 || (n >= 536 && n <= 65495)) {
		t.TCPMSS = n
	}
	return t
}

// intOf 只认整数(JSON 解出来是 float64,也可能直接是 int)。
func intOf(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

var (
	looseIPv4 = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)
	looseIPv6 = regexp.MustCompile(`(?i)^[0-9a-f:.]+$`)
	hostChars = regexp.MustCompile(`^[a-z0-9.-]+$`)
)

// Hosts 是节点和订阅要「直连解析」的主机:域名和 IP 段(IPv4 写 /32,IPv6 写 /128)。
type Hosts struct {
	Domains []string
	CIDRs   []string
}

// HostsOf 收集节点服务器(跳过链式节点)和订阅(各个启用的地址、账号地址)里的主机。
// 去掉首尾空白、小写、去掉方括号和末尾的点;localhost、空、不像域名(没有点)的丢掉。
func HostsOf(nodes, subs []map[string]any) Hosts {
	domains, cidrs := map[string]bool{}, map[string]bool{}
	var dOrder, cOrder []string
	add := func(raw string) {
		h := strings.ToLower(strings.TrimSpace(raw))
		h = strings.TrimPrefix(h, "[")
		h = strings.TrimSuffix(h, "]")
		h = strings.TrimSuffix(h, ".")
		if h == "" || h == "localhost" {
			return
		}
		switch {
		case looseIPv4.MatchString(h):
			if c := h + "/32"; !cidrs[c] {
				cidrs[c] = true
				cOrder = append(cOrder, c)
			}
		case strings.Contains(h, ":") && looseIPv6.MatchString(h):
			if c := h + "/128"; !cidrs[c] {
				cidrs[c] = true
				cOrder = append(cOrder, c)
			}
		case hostChars.MatchString(h) && strings.Contains(h, "."):
			if !domains[h] {
				domains[h] = true
				dOrder = append(dOrder, h)
			}
		}
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if chain, _ := n["chain"].(bool); chain {
			continue
		}
		if s, ok := n["server"].(string); ok {
			add(s)
		}
	}
	for _, s := range subs {
		if s == nil {
			continue
		}
		var urls []any
		if entries, ok := s["urlEntries"].([]any); ok {
			for _, e := range entries {
				if m, _ := e.(map[string]any); m != nil && m["enabled"] == true {
					urls = append(urls, m["url"])
				}
			}
		} else if list, ok := s["urls"].([]any); ok && len(list) > 0 {
			urls = list
		} else {
			urls = []any{s["url"]}
		}
		if acc, _ := s["account"].(map[string]any); acc != nil {
			urls = append(urls, acc["url"], acc["apiOrigin"])
		} else {
			urls = append(urls, nil, nil)
		}
		for _, u := range urls {
			raw, _ := u.(string)
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if pu, err := url.Parse(raw); err == nil && (pu.Scheme == "http" || pu.Scheme == "https") && pu.Host != "" {
				add(pu.Hostname())
			}
		}
	}
	return Hosts{Domains: dOrder, CIDRs: cOrder}
}

// 参与输入指纹的档案键:改了其中任何一个,Mihomo 的配置就得重新生成。
// 注意没有 businessPreference(业务优选整体不做,见 HANDOFF 第五节 C)。
var fingerprintKeys = []string{
	"ipv6", "ipv6Proxy", "directBypass", "directForNodes", "bypassPorts", "bypassPortsMode", "bypassPortsWhitelist",
	"tun", "dns", "routing", "rejectQuic", "clientRoutes", "servers", "testUrl", "testExpectedStatus",
	"chainProxies", "nodeScoreWeights",
}

// Fingerprint 是 Mihomo 一侧的「输入指纹」:档案里影响配置的那些键、节点、用户分组、订阅主机、各订阅的节点 DNS 的摘要。
// 启动 Mihomo 成功时写进 mihomo.inputs,之后当前指纹和它不一样就是「配置改了、内核还没用上」(待重启)。
// 键序、空白不影响结果;档案里没写的键和写成 null 的键是两回事。
func Fingerprint(profile map[string]any, nodes, groups any, subs []map[string]any, valid func(map[string]any) error) string {
	x := map[string]any{}
	for _, k := range fingerprintKeys {
		if v, ok := profile[k]; ok {
			x[k] = v
		}
	}
	if ports := SniffPorts(profile); len(ports) > 0 {
		x["mihomoTlsSniffPorts"] = ports
	}
	hosts := HostsOf(nil, subs)
	nd := NodeDNSOf(subs, valid)
	pairs := make([]any, 0, len(nd))
	for _, n := range nd {
		item := map[string]any{"nameserver": n.Nameserver}
		if n.HasBootstrap {
			item["bootstrap"] = n.Bootstrap
		}
		pairs = append(pairs, []any{n.ID, item})
	}
	b, _ := json.Marshal([]any{x, nodes, groups, map[string]any{"domains": nonNil(hosts.Domains), "cidrs": nonNil(hosts.CIDRs)}, pairs})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	sort.Strings(s)
	return s
}
