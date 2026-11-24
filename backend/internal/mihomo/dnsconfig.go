package mihomo

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// 双内核 3e-2:装配 Mihomo 的 DNS 段(对应上游 v0.1.462 Node 的 `EC` / `vt` / `nw` / `Ct` / `CE`)。
//
// 上游只把这份 config 塞进 mihomo.yaml 的 `dns:`,把 providers 并进 `rule-providers`,
// 所以这里返回的也是这两样:config(键按上游顺序)和 providers(tag → file 型 provider)。
//
// 两侧的上游 DNS 不在这里取:core 的 `mihomo/mihomoDnsUpstreams` 给(DNSUpstreamSet),
// 与 sing-box 配置共用同一套取法,避免两边漂移。

// DNS 段的固定值(上游 EC / Ia)。
const (
	DNSListenPort  = 7853 // dns.listen 的端口
	DNSRewritePort = 7854 // DNS 重写的监听端口(nameserver-policy 里指向它)
	DNSAdmitPort   = 7855 // 白名单终端的单独 DNS 端口

	fakeIPRangeV4   = "198.18.0.1/16"
	fakeIPRangeV6   = "2001:2:0:8000::1/49"
	rewriteDNSValue = "udp://127.0.0.1:7854"
	// localDNSRegex 是 ob-local-dns 规则集:单标签主机名与常见内网后缀(上游 fC)。
	localDNSRegex       = `(^[^.]+$|\.(lan|local|home|internal|home\.arpa)$)`
	ruleTargetDelimiter = ","
)

// DNSUpstreamBoot 是上游 DNS 的 bootstrap 解析器(与 core 的 mihomoDnsUpstreams 同形)。
type DNSUpstreamBoot struct {
	Protocol string `json:"protocol"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
}

// DNSUpstreamServer 是一台上游 DNS(与 core 的 mihomoDnsUpstreams 同形)。
type DNSUpstreamServer struct {
	Protocol  string           `json:"protocol"`
	Server    string           `json:"server"`
	Port      int              `json:"port"`
	Path      string           `json:"path,omitempty"`
	Wan       bool             `json:"wan,omitempty"`
	Bootstrap *DNSUpstreamBoot `json:"bootstrap,omitempty"`
}

// DNSUpstreamSet 是 core 的 mihomoDnsUpstreams 的返回:直连 / 代理两侧各一台主上游,加各自的额外上游。
type DNSUpstreamSet struct {
	Direct       DNSUpstreamServer   `json:"direct"`
	Proxy        DNSUpstreamServer   `json:"proxy"`
	DirectExtras []DNSUpstreamServer `json:"directExtras"`
	ProxyExtras  []DNSUpstreamServer `json:"proxyExtras"`
}

// ParseDNSUpstreams 解析 core 的 mihomoDnsUpstreams 返回值。
func ParseDNSUpstreams(raw json.RawMessage) (DNSUpstreamSet, error) {
	var set DNSUpstreamSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return set, fmt.Errorf("core: 解析 mihomoDnsUpstreams 的返回值失败: %w", err)
	}
	return set, nil
}

// FetchDNSUpstreams 调 core 的 mihomoDnsUpstreams(入参与 sing-box 配置那一侧同源:
// profile + systemDns)。3e-5 装配 mihomo.yaml 时用它拿 DNSBuildInput.Upstreams。
func FetchDNSUpstreams(ctx context.Context, call Call, profile any, systemDNS []any) (DNSUpstreamSet, error) {
	if systemDNS == nil {
		systemDNS = []any{}
	}
	raw, err := call(ctx, "mihomoDnsUpstreams", map[string]any{"profile": profile, "systemDns": systemDNS})
	if err != nil {
		return DNSUpstreamSet{}, err
	}
	return ParseDNSUpstreams(raw)
}

func (s DNSUpstreamServer) dnsServer() DNSServer {
	out := DNSServer{Protocol: s.Protocol, Server: s.Server, Port: s.Port, Path: s.Path, WAN: s.Wan}
	if s.Bootstrap != nil {
		out.Bootstrap = &DNSServer{Protocol: s.Bootstrap.Protocol, Server: s.Bootstrap.Server, Port: s.Bootstrap.Port}
	}
	return out
}

// DNSRewriteRule 是规整后的一条 DNS 重写(上游 nw)。
type DNSRewriteRule struct {
	ID        string
	Enabled   bool
	Source    string
	Domain    string
	Addresses []string
	Note      string
}

// DNSRewrite 是 DNS 重写配置(上游 vt)。
type DNSRewrite struct {
	Enabled     bool
	Initialized bool
	Rules       []DNSRewriteRule
}

// NormalizeDNSRewrite 对应上游 vt / nw:把档案里的 dns.rewrite 规整成「能用」的规则。
// 丢掉源域名不合法、目标为空、或「泛域名目标但源不是泛域名」的条目(上游的 filter)。
// 这里只管 Mihomo 段用得到的部分;面板保存时的校验仍由面板自己做。
func NormalizeDNSRewrite(dns map[string]any) DNSRewrite {
	raw, _ := dns["rewrite"].(map[string]any)
	out := DNSRewrite{Enabled: true}
	if raw == nil {
		return out
	}
	if b, ok := raw["enabled"].(bool); ok && !b {
		out.Enabled = false
	}
	out.Initialized = truthy(raw["initialized"])
	for i, item := range rawArray(raw["rules"]) {
		rule := normalizeRewriteRule(item, i)
		if rule.Source == "" || (rule.Domain == "" && len(rule.Addresses) == 0) {
			continue
		}
		// 泛域名的目标只有源也是泛域名时才有意义(上游 !qr(domain) || qr(source))
		if isWildcardHost(rule.Domain) && !isWildcardHost(rule.Source) {
			continue
		}
		out.Rules = append(out.Rules, rule)
	}
	return out
}

func normalizeRewriteRule(item any, index int) DNSRewriteRule {
	m, _ := item.(map[string]any)
	rule := DNSRewriteRule{Enabled: true}
	if m == nil {
		rule.ID = fmt.Sprintf("rw-%d", index+1)
		return rule
	}
	if id, ok := m["id"].(string); ok && strings.TrimSpace(id) != "" {
		rule.ID = strings.TrimSpace(id)
	} else {
		rule.ID = fmt.Sprintf("rw-%d", index+1)
	}
	if b, ok := m["enabled"].(bool); ok {
		rule.Enabled = b
	}
	rule.Source = normalizeRewriteHost(m["source"], true)
	rule.Domain = normalizeRewriteHost(m["domain"], true)
	if rule.Domain == "" {
		seen := map[string]bool{}
		for _, a := range rawArray(m["addresses"]) {
			ip := normalizeRewriteAddress(a)
			if ip != "" && !seen[ip] {
				seen[ip] = true
				rule.Addresses = append(rule.Addresses, ip)
			}
		}
	}
	if s, ok := m["note"].(string); ok {
		rule.Note = truncateRunes(strings.TrimSpace(s), 200)
	}
	return rule
}

// normalizeRewriteHost 对应上游 Ct:去空白、转小写、去掉末尾的点,允许 *. 开头的泛域名。
// 标签只认字母数字和 _ -(不能以 - 开头 / 结尾),整串不超过 253,不合格返回空串。
func normalizeRewriteHost(raw any, allowWildcard bool) string {
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimRight(s, ".")
	wildcard := false
	if allowWildcard && strings.HasPrefix(s, "*.") {
		wildcard = true
		s = s[2:]
	}
	if s == "" || len(s) > 253 || strings.Contains(s, "*") {
		return ""
	}
	for _, label := range strings.Split(s, ".") {
		if !validHostLabel(label) {
			return ""
		}
	}
	if wildcard {
		return "*." + s
	}
	return s
}

func validHostLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func isWildcardHost(s string) bool { return strings.HasPrefix(s, "*.") }

// normalizeRewriteAddress 对应上游 rw:能当 IP 解析才收,统一成小写(IPv6 的写法归一)。
func normalizeRewriteAddress(raw any) string {
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	// net.ParseIP 认的地址才收;上游也是「能解析就原样收」,不重写 IPv6 的写法。
	if net.ParseIP(s) != nil {
		return s
	}
	return ""
}

// truthy 是 JS 的真值判断(档案里的 0 / 1 也算)。
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// decodeRuleTarget 对应上游 dg:规则里的出口名如果写成 ~openbox~<base64url>,还原成原名
// (名字里有逗号 / 括号 / 首尾空白时 core 会这样写)。不是这个前缀就原样返回。
func decodeRuleTarget(s string) string {
	const prefix = "~openbox~"
	if !strings.HasPrefix(s, prefix) {
		return s
	}
	if b, err := base64.RawURLEncoding.DecodeString(s[len(prefix):]); err == nil {
		return string(b)
	}
	return s
}

// DNSBuildInput 是 BuildDNSConfig 的入参。
type DNSBuildInput struct {
	Profile     map[string]any // 档案(取 ipv6 / dns.mode / dns.split / dns.fakeIpForProxy / dns.rewrite)
	Rules       []string       // mihomoTargetRules 的 rules
	ClassOf     func(name string) string
	Kinds       map[string]string // 规则集名 → domain / ipcidr(只认 domain 的进 DNS)
	Upstreams   DNSUpstreamSet    // core 的 mihomoDnsUpstreams
	TerminalDNS []any             // 终端分流那一侧的 DNS 行(3e-5 装配时给,空 = 没有)

	EndTerminal    int // rules 的段下标(见 RulesModel)
	DirectTerminal int
	FirstPolicy    int

	IPv6Mode string // node / ipv4 / bypass / off(上游 qt)
	Platform string // openwrt / container / linux …

	DeployedProviders map[string]Provider // 上次已经写过的 provider(tag → 条目),能复用就复用
}

// DNSBuildResult 是 DNS 段的产物。
type DNSBuildResult struct {
	Providers map[string]Provider
	Config    *OrderedMap
}

// BuildDNSConfig 对应上游 EC:装配 dns 段与它要用的 rule-providers。
// rulesDir 必须已存在(provider 文件写在这里);fs 一般传 OSFS。
func BuildDNSConfig(fs FS, rulesDir string, in DNSBuildInput) (*DNSBuildResult, error) {
	profile := in.Profile
	if profile == nil {
		profile = map[string]any{}
	}
	dns, _ := profile["dns"].(map[string]any)
	if dns == nil {
		dns = map[string]any{}
	}
	platform := in.Platform
	if platform == "" {
		platform = "linux"
	}
	split := isTrue(dns["split"])
	fakeIP := FakeIPForProxy(profile)

	direct := in.Upstreams.Direct.dnsServer()
	proxy := in.Upstreams.Proxy.dnsServer()

	directList := []DNSServer{direct}
	proxyList := []DNSServer{proxy}
	if split {
		for _, e := range in.Upstreams.DirectExtras {
			directList = append(directList, e.dnsServer())
		}
		for _, e := range in.Upstreams.ProxyExtras {
			proxyList = append(proxyList, e.dnsServer())
		}
	}
	directURLs := ServerURLs(directList, URLOptions{})
	proxyURLs := ServerURLs(proxyList, URLOptions{Route: true, IPv4Only: in.IPv6Mode == "ipv4"})

	// 有 openbox-* 参数(补丁内核才认的那种)时要打开 openbox-upstream-dns
	openboxParam := hasOpenboxParam(directURLs)
	if split || len(in.TerminalDNS) > 0 {
		openboxParam = openboxParam || hasOpenboxParam(proxyURLs)
	}

	providers := map[string]Provider{}
	policy := NewOrdered()
	var fakeIPFilter []string
	var rewriteMatches []string

	// DNS 重写:源域名 → 本地 7854
	rewrite := NormalizeDNSRewrite(dns)
	if rewrite.Enabled {
		for _, r := range rewrite.Rules {
			if !r.Enabled {
				continue
			}
			name := r.Source
			if strings.HasPrefix(name, "*.") {
				name = "." + name[2:]
			}
			policy.Set(name, rewriteDNSValue)
			match := "DOMAIN," + name
			if strings.HasPrefix(name, ".") {
				match = `DOMAIN-REGEX,^.+\.` + strings.ReplaceAll(name[1:], ".", `\.`) + "$"
			}
			fakeIPFilter = append(fakeIPFilter, match+",real-ip")
			rewriteMatches = append(rewriteMatches, match)
		}
	}

	// 内网主机名(单标签 / .lan 之类)走本地 DNS:container 平台没有本地 DNS,不用这条
	if platform != "container" && DNSModeOf(profile) == "hijack" {
		if p, ok := in.DeployedProviders["ob-local-dns"]; ok {
			providers["ob-local-dns"] = p
		} else {
			p, err := WriteProvider(fs, rulesDir, "ob-local-dns", NewSource(3, Rule{Strs("domain_regex", localDNSRegex)}), "domain")
			if err != nil {
				return nil, err
			}
			providers["ob-local-dns"] = p
		}
		local := "system://"
		if platform == "openwrt" {
			local = "udp://127.0.0.1:53"
		}
		policy.Set("rule-set:ob-local-dns", local)
		fakeIPFilter = append(fakeIPFilter, "RULE-SET,ob-local-dns,real-ip")
	}

	// 分流里能落到 DNS 的那几类规则:每条按它的出口分类选上游
	type dnsRule struct {
		typ   string
		value string
		class string
	}
	var parsed []dnsRule
	var exempt []string
	fallback := "direct"
	if split {
		for i, line := range in.Rules {
			if i < in.EndTerminal || (i >= in.DirectTerminal && i < in.FirstPolicy) {
				continue
			}
			parts := strings.SplitN(line, ruleTargetDelimiter, 3)
			if len(parts) < 2 {
				continue
			}
			typ, value := parts[0], parts[1]
			if typ == "MATCH" {
				// MATCH 只有两项:类型 + 出口
				if in.ClassOf != nil {
					fallback = in.ClassOf(decodeRuleTarget(value))
				}
				continue
			}
			target := ""
			if len(parts) == 3 {
				target = decodeRuleTarget(parts[2])
			}
			switch typ {
			case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD":
			case "RULE-SET":
				if in.Kinds[value] != "domain" {
					continue
				}
			default:
				continue
			}
			class := "direct"
			if in.ClassOf != nil {
				class = in.ClassOf(target)
			}
			parsed = append(parsed, dnsRule{typ: typ, value: value, class: class})
			if i < in.DirectTerminal {
				exempt = append(exempt, typ+","+value)
			}
		}
	}

	serversFor := func(class string) []string {
		switch class {
		case "proxy":
			return proxyURLs
		case "reject":
			return []string{"rcode://refused"}
		}
		return directURLs
	}

	// FakeIP:同一条规则在 fake-ip-filter 里带 fake-ip / real-ip,决定哪些域名不进假 IP 池
	if fakeIP {
		for _, r := range parsed {
			suffix := "real-ip"
			if r.class == "proxy" {
				suffix = "fake-ip"
			}
			fakeIPFilter = append(fakeIPFilter, r.typ+","+r.value+","+suffix)
		}
	}

	for _, r := range parsed {
		tag := r.value
		if r.typ != "RULE-SET" {
			tag = "ob-dns-" + ruleTagHash(r.typ, r.value)
			if p, ok := in.DeployedProviders[tag]; ok {
				providers[tag] = p
			} else if !hasProvider(providers, tag) {
				field := map[string]string{
					"DOMAIN": "domain", "DOMAIN-SUFFIX": "domain_suffix", "DOMAIN-KEYWORD": "domain_keyword",
				}[r.typ]
				p, err := WriteProvider(fs, rulesDir, tag, NewSource(3, Rule{Strs(field, r.value)}), "domain")
				if err != nil {
					return nil, err
				}
				providers[tag] = p
			}
		}
		key := "rule-set:" + tag
		if policy.Has(key) {
			continue
		}
		list := serversFor(r.class)
		if len(list) == 1 {
			policy.Set(key, list[0])
		} else {
			policy.Set(key, list)
		}
	}

	ipv6 := isTrue(profile["ipv6"])
	listen := "0.0.0.0:7853"
	listenHost := "0.0.0.0"
	if ipv6 {
		listen = "[::]:7853"
		listenHost = "[::]"
	}

	cfg := NewOrdered()
	cfg.Set("enable", true)
	cfg.Set("listen", listen)
	cfg.Set("ipv6", ipv6)
	if fakeIP {
		cfg.Set("enhanced-mode", "fake-ip")
	} else {
		cfg.Set("enhanced-mode", "redir-host")
	}
	cfg.Set("fake-ip-range", fakeIPRangeV4)
	range6 := ""
	if in.IPv6Mode == "node" {
		range6 = fakeIPRangeV6
	}
	cfg.Set("fake-ip-range6", range6)
	boot := direct
	if direct.Bootstrap != nil {
		boot = *direct.Bootstrap
	}
	cfg.Set("default-nameserver", []string{ServerURL(boot)})
	cfg.Set("use-system-hosts", false)
	cfg.Set("proxy-server-nameserver", proxyURLs)
	cfg.Set("direct-nameserver", directURLs)
	cfg.Set("nameserver", serversFor(fallback))
	cfg.Set("nameserver-policy", policy)
	cfg.SetIf(openboxParam || in.IPv6Mode == "bypass" || platform == "container", "openbox-upstream-dns", true)
	cfg.SetIf(platform == "container", "openbox-lan-dns-listen", "0.0.0.0:53")
	cfg.SetIf(in.IPv6Mode == "bypass", "openbox-real-ipv6", true)
	// 上游这里是 `...admitDns?{...}:{}`,而 admitDns 就是 vt(dns) 的返回(永远是个对象),
	// 所以这两个键在非 container 平台上一直在。照上游写,不改语义。
	cfg.Set("openbox-admit-dns-listen", fmt.Sprintf("%s:%d", listenHost, DNSAdmitPort))
	if platform == "openwrt" {
		cfg.Set("openbox-admit-dns-local", true)
	}
	policyBlock := NewOrdered()
	policyBlock.Set("rewrite", nonNilStrings(rewriteMatches))
	// 第三个条件同样是那个恒真的 admitDns,所以等价于「不是 container 就是本地」
	policyBlock.Set("local", platform != "container")
	policyBlock.Set("suppress-https", split)
	cfg.Set("openbox-dns-policy", policyBlock)

	if len(in.TerminalDNS) > 0 {
		cfg.Set("openbox-terminal-dns", in.TerminalDNS)
		cfg.Set("openbox-terminal-dns-rewrite", nonNilStrings(rewriteMatches))
		cfg.Set("openbox-terminal-dns-exempt", nonNilStrings(exempt))
		cfg.Set("openbox-terminal-proxy-nameserver", proxyURLs)
		if platform != "container" {
			localNS := "system://"
			if platform == "openwrt" {
				localNS = "udp://127.0.0.1:53"
			}
			cfg.Set("openbox-terminal-local-nameserver", []string{localNS})
		}
	}
	if fakeIP {
		tail := "real-ip"
		if fallback == "proxy" {
			tail = "fake-ip"
		}
		cfg.Set("fake-ip-filter-mode", "rule")
		cfg.Set("fake-ip-filter", append(append([]string{}, fakeIPFilter...), "MATCH,"+tail))
	}

	return &DNSBuildResult{Providers: providers, Config: cfg}, nil
}

// ruleTagHash 对应上游 `ob-dns-` + sha256("<类型>,<值>") 的前 16 位十六进制。
func ruleTagHash(typ, value string) string {
	sum := sha256.Sum256([]byte(typ + "," + value))
	return hex.EncodeToString(sum[:])[:16]
}

func hasProvider(m map[string]Provider, tag string) bool { _, ok := m[tag]; return ok }

func hasOpenboxParam(urls []string) bool {
	for _, u := range urls {
		if strings.Contains(u, "openbox-") {
			return true
		}
	}
	return false
}
