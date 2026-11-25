package profile

import (
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// 字段取值范围。
var (
	ipv6ProxyModes = []string{"node", "ipv4", "bypass"}
	portsModes     = []string{"blacklist", "whitelist"}
	tunStacks      = []string{"mixed", "gvisor", "system"}
	dnsModes       = []string{"off", "hijack", "dnsmasq"}
	dnsProtocols   = []string{"udp", "tcp", "https", "tls"}
	updateChannels = []string{"auto", "direct", "mirror"}

	statusSegment = regexp.MustCompile(`^[1-5][0-9]{2}(-[1-5][0-9]{2})?$`)
)

const (
	maxPortsLen   = 512
	maxURLLen     = 2048
	maxExtras     = 3
	minKeepMonths = 1
	maxKeepMonths = 36
)

// Check 校验合并后的完整档案。只检查「已知字段」,未知字段原样放行。
// 唯一会改动入参的是 traffic.keepMonths:超出 1~36 时夹到范围内(前端的约定),而不是报错。
func Check(p map[string]any) error {
	for _, k := range []string{"ipv6", "directForNodes", "directBypass", "rejectQuic"} {
		if err := wantBool(p, k, k); err != nil {
			return err
		}
	}
	if err := wantEnum(p, "ipv6Proxy", "ipv6Proxy", ipv6ProxyModes); err != nil {
		return err
	}
	if err := wantEnum(p, "bypassPortsMode", "bypassPortsMode", portsModes); err != nil {
		return err
	}
	if err := wantRegion(p, "region", "region"); err != nil {
		return err
	}
	for _, k := range []string{"bypassPorts", "bypassPortsWhitelist", "mihomoTlsSniffPorts"} {
		if err := checkPorts(p, k); err != nil {
			return err
		}
	}
	for _, k := range []string{"testUrl", "directTestUrl"} {
		if err := checkHTTPURL(p, k); err != nil {
			return err
		}
	}
	if err := checkExpectedStatus(p, "testExpectedStatus"); err != nil {
		return err
	}
	if err := checkTun(p); err != nil {
		return err
	}
	if err := checkDNS(p); err != nil {
		return err
	}
	if err := checkEntryDNS(p); err != nil {
		return err
	}
	if err := checkTraffic(p); err != nil {
		return err
	}
	return checkUpdates(p)
}

func wantBool(m map[string]any, key, label string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	if _, isBool := v.(bool); !isBool {
		return fmt.Errorf("%s 必须是 true 或 false", label)
	}
	return nil
}

func wantString(m map[string]any, key, label string) (string, bool, error) {
	v, ok := m[key]
	if !ok {
		return "", false, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return "", true, fmt.Errorf("%s 必须是字符串", label)
	}
	return s, true, nil
}

func wantEnum(m map[string]any, key, label string, allowed []string) error {
	s, ok, err := wantString(m, key, label)
	if err != nil || !ok {
		return err
	}
	for _, a := range allowed {
		if s == a {
			return nil
		}
	}
	return fmt.Errorf("%s 的取值只能是 %s", label, strings.Join(allowed, " / "))
}

func wantRegion(m map[string]any, key, label string) error {
	s, ok, err := wantString(m, key, label)
	if err != nil || !ok {
		return err
	}
	if !ValidRegion(s) {
		return fmt.Errorf("%s 的取值只能是 cn / intl", label)
	}
	return nil
}

// wantInt 取整数(JSON 数字解码为 float64)。present 为 false 表示没有这个键。
func wantInt(m map[string]any, key, label string) (n int, present bool, err error) {
	v, ok := m[key]
	if !ok {
		return 0, false, nil
	}
	f, isNum := v.(float64)
	if !isNum || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) > 1<<31 {
		return 0, true, fmt.Errorf("%s 必须是整数", label)
	}
	return int(f), true, nil
}

func wantObject(m map[string]any, key, label string) (map[string]any, error) {
	v, ok := m[key]
	if !ok {
		return nil, nil
	}
	o, isObj := v.(map[string]any)
	if !isObj {
		return nil, fmt.Errorf("%s 必须是对象", label)
	}
	return o, nil
}

// checkPorts 校验「21114-21119, 2233」这种写法;空串合法(不放行任何端口)。
func checkPorts(m map[string]any, key string) error {
	s, ok, err := wantString(m, key, key)
	if err != nil || !ok {
		return err
	}
	if len(s) > maxPortsLen {
		return fmt.Errorf("%s 太长(上限 %d 字符)", key, maxPortsLen)
	}
	s = strings.NewReplacer("，", ",", "–", "-", "—", "-").Replace(s)
	if strings.TrimSpace(s) == "" {
		return nil
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		lo, hi, isRange := strings.Cut(part, "-")
		a, errA := parsePort(strings.TrimSpace(lo))
		if errA != nil {
			return fmt.Errorf("%s 里的「%s」不是有效端口(写法如 21114-21119, 2233)", key, part)
		}
		if !isRange {
			continue
		}
		b, errB := parsePort(strings.TrimSpace(hi))
		if errB != nil || a > b {
			return fmt.Errorf("%s 里的「%s」不是有效端口范围", key, part)
		}
	}
	return nil
}

func parsePort(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("空")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("非数字")
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("越界")
	}
	return n, nil
}

func checkHTTPURL(m map[string]any, key string) error {
	s, ok, err := wantString(m, key, key)
	if err != nil || !ok || s == "" {
		return err
	}
	if len(s) > maxURLLen {
		return fmt.Errorf("%s 太长", key)
	}
	u, perr := url.Parse(s)
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s 必须是 http:// 或 https:// 开头的地址", key)
	}
	return nil
}

// checkExpectedStatus 校验可接受状态码:空 / * = 不限;否则是「200-399」「204」用 / 连接的多段。
func checkExpectedStatus(m map[string]any, key string) error {
	s, ok, err := wantString(m, key, key)
	if err != nil || !ok {
		return err
	}
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return nil
	}
	for _, seg := range strings.Split(s, "/") {
		seg = strings.TrimSpace(seg)
		if !statusSegment.MatchString(seg) {
			return fmt.Errorf("%s 的写法不对(例如 200-399 或 204,多段用 / 连接)", key)
		}
		if lo, hi, isRange := strings.Cut(seg, "-"); isRange && lo > hi {
			return fmt.Errorf("%s 里的范围「%s」起点大于终点", key, seg)
		}
	}
	return nil
}

func checkTun(p map[string]any) error {
	tun, err := wantObject(p, "tun", "tun")
	if err != nil || tun == nil {
		return err
	}
	if err := wantBool(tun, "autoRedirect", "tun.autoRedirect"); err != nil {
		return err
	}
	if err := wantEnum(tun, "stack", "tun.stack", tunStacks); err != nil {
		return err
	}
	// 0 = 内核默认 / 不钳制
	if n, ok, err := wantInt(tun, "mtu", "tun.mtu"); err != nil {
		return err
	} else if ok && n != 0 && (n < 576 || n > 65535) {
		return fmt.Errorf("tun.mtu 只能是 0(内核默认)或 576~65535")
	}
	if n, ok, err := wantInt(tun, "tcpMss", "tun.tcpMss"); err != nil {
		return err
	} else if ok && n != 0 && (n < 536 || n > 65535) {
		return fmt.Errorf("tun.tcpMss 只能是 0(不钳制)或 536~65535")
	}
	return nil
}

func checkDNS(p map[string]any) error {
	dns, err := wantObject(p, "dns", "dns")
	if err != nil || dns == nil {
		return err
	}
	if err := wantEnum(dns, "mode", "dns.mode", dnsModes); err != nil {
		return err
	}
	if err := wantRegion(dns, "region", "dns.region"); err != nil {
		return err
	}
	for _, k := range []string{"split", "fakeIpForProxy", "listen53"} {
		if err := wantBool(dns, k, "dns."+k); err != nil {
			return err
		}
	}
	if err := checkFakeIPRange(dns); err != nil {
		return err
	}
	if err := checkDomainUpstreams(dns); err != nil {
		return err
	}
	for _, side := range []string{"direct", "proxy"} {
		if err := wantEnum(dns, side+"Protocol", "dns."+side+"Protocol", dnsProtocols); err != nil {
			return err
		}
		if err := checkPortField(dns, side+"Port", "dns."+side+"Port"); err != nil {
			return err
		}
		if err := checkUpstream(dns, side, "dns."+side); err != nil {
			return err
		}
		if err := checkBootstrap(dns, side+"Bootstrap", "dns."+side+"Bootstrap"); err != nil {
			return err
		}
		if err := checkExtras(dns, side+"Extras", "dns."+side+"Extras"); err != nil {
			return err
		}
	}
	return nil
}

// 域名指定上游的条数上限:与面板一致(单条规则的域名数上限 64 在 checkDomainUpstreamRule 里)。
const maxDomainUpstreamRules = 200

// checkDomainUpstreams:面板 DNS 页「域名分流上游」(档案 dns.domainUpstreams = {enabled, rules})。
// 与面板同一口径:域名规整后不能为空、不能带 *;同一台上游不要 wan(内核那边的池子会退成上游 DNS,user 看不出来);
// 地址按协议校验(udp / tcp 只收 IP,tls 收 IP 或域名,https 收 https:// 地址),域名类上游要带 bootstrap。
func checkDomainUpstreams(dns map[string]any) error {
	v, ok := dns["domainUpstreams"]
	if !ok || v == nil {
		return nil
	}
	du, isObj := v.(map[string]any)
	if !isObj {
		return fmt.Errorf("dns.domainUpstreams 必须是对象")
	}
	if err := wantBool(du, "enabled", "dns.domainUpstreams.enabled"); err != nil {
		return err
	}
	list, hasList := du["rules"]
	if !hasList || list == nil {
		return nil
	}
	rules, isList := list.([]any)
	if !isList {
		return fmt.Errorf("dns.domainUpstreams.rules 必须是数组")
	}
	if len(rules) > maxDomainUpstreamRules {
		return fmt.Errorf("dns.domainUpstreams.rules 最多 %d 条", maxDomainUpstreamRules)
	}
	for i, e := range rules {
		item, isItem := e.(map[string]any)
		if !isItem {
			return fmt.Errorf("dns.domainUpstreams.rules 第 %d 条必须是对象", i+1)
		}
		if err := checkDomainUpstreamRule(item, fmt.Sprintf("dns.domainUpstreams.rules 第 %d 条", i+1)); err != nil {
			return err
		}
	}
	return nil
}

func checkDomainUpstreamRule(r map[string]any, label string) error {
	if err := wantBool(r, "enabled", label+"的 enabled"); err != nil {
		return err
	}
	if err := wantEnum(r, "matchType", label+"的 matchType", []string{"domain", "domainSuffix"}); err != nil {
		return err
	}
	matchType, _, _ := wantString(r, "matchType", label+"的 matchType")
	proto, _, err := wantString(r, "protocol", label+"的 protocol")
	if err != nil {
		return err
	}
	if proto == "" {
		proto = "udp"
	}
	if !slices.Contains(dnsProtocols, proto) {
		return fmt.Errorf("%s的 protocol 只能是 %s", label, strings.Join(dnsProtocols, " / "))
	}
	server, present, err := wantString(r, "server", label+"的 server")
	if err != nil {
		return err
	}
	if !present || strings.TrimSpace(server) == "" {
		return fmt.Errorf("%s缺少 server", label)
	}
	if strings.EqualFold(strings.TrimSpace(server), WAN) {
		return fmt.Errorf("%s的 server 不能是 wan(域名指定上游要用具体地址)", label)
	}
	if err := validUpstreamAddr(server, proto, label+"的 server"); err != nil {
		return err
	}
	if err := checkPortField(r, "port", label+"的 port"); err != nil {
		return err
	}
	if err := checkBootstrap(r, "bootstrap", label+"的 bootstrap"); err != nil {
		return err
	}
	// 域名为空时这条规则不会生效,与其让用户以为改了,不如直接告诉他。
	rawDomains, hasDomains := r["domains"]
	domains, isList := rawDomains.([]any)
	if !hasDomains || !isList || len(domains) == 0 {
		return fmt.Errorf("%s的 domains 至少要有 1 个域名", label)
	}
	if len(domains) > 64 {
		return fmt.Errorf("%s的 domains 最多 64 个域名", label)
	}
	for _, d := range domains {
		str, isStr := d.(string)
		if !isStr {
			return fmt.Errorf("%s的 domains 里只能放字符串", label)
		}
		if normalizeDomainUpstreamHost(str, matchType == "domainSuffix") == "" {
			return fmt.Errorf("%s的域名 %q 不合法(不能为空、不能带 *)", label, str)
		}
	}
	return nil
}

// normalizeDomainUpstreamHost:与 core 的 routing.normalizeDomainUpstreamHost 同一口径(去空白、转小写、
// 去掉末尾的点;后缀形态另外去掉开头的 "*." / "."),不合格返回空串。
func normalizeDomainUpstreamHost(raw any, suffix bool) string {
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimRight(s, ".")
	if suffix {
		if strings.HasPrefix(s, "*.") {
			s = s[2:]
		} else if strings.HasPrefix(s, ".") {
			s = s[1:]
		}
		s = strings.TrimRight(s, ".")
	}
	if s == "" || strings.Contains(s, "*") || len(s) > 253 {
		return ""
	}
	return s
}

// checkFakeIPRange:FakeIP 的 v4 地址池(面板「FakeIP 网段」)。空 = 内核默认;否则必须是 IPv4 CIDR、
// 掩码 /15~/24 —— 与面板校验同一口径(core 那边也按这个范围收,越界的值会退回默认池,这里先挡住,
// 免得用户以为改成功了)。
func checkFakeIPRange(m map[string]any) error {
	s, ok, err := wantString(m, "fakeIpRange", "dns.fakeIpRange")
	if err != nil || !ok {
		return err
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	p, perr := netip.ParsePrefix(s)
	if perr != nil || !p.Addr().Is4() || p.Bits() < 15 || p.Bits() > 24 {
		return fmt.Errorf("dns.fakeIpRange 只能是掩码 /15~/24 的 IPv4 网段(例如 198.20.0.0/16),留空用内核默认")
	}
	return nil
}

func checkPortField(m map[string]any, key, label string) error {
	n, ok, err := wantInt(m, key, label)
	if err != nil {
		return err
	}
	if ok && (n < 1 || n > 65535) {
		return fmt.Errorf("%s 只能是 1~65535", label)
	}
	return nil
}

// checkUpstream:上游地址按协议校验(空串 = 沿用默认):udp / tcp 只收 IP 或 wan;tls 收 IP、域名或 wan;https 收 https:// 地址或 wan。
func checkUpstream(m map[string]any, key, label string) error {
	s, ok, err := wantString(m, key, label)
	if err != nil || !ok {
		return err
	}
	proto, _, _ := wantString(m, key+"Protocol", label)
	return validUpstreamAddr(s, proto, label)
}

func validUpstreamAddr(s, proto, label string) error {
	s = strings.TrimSpace(s)
	if s == "" || s == WAN {
		return nil
	}
	return validateDNSAddr(s, proto, label)
}

// checkBootstrap:解析 DoH / 域名 DoT 主机名用的 DNS(null / 缺省 = 用上游 DNS)。
func checkBootstrap(m map[string]any, key, label string) error {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	item, isObj := v.(map[string]any)
	if !isObj {
		return fmt.Errorf("%s 必须是对象或 null", label)
	}
	server, present, err := wantString(item, "server", label+".server")
	if err != nil {
		return err
	}
	server = strings.TrimSpace(server)
	if !present || server == "" {
		return fmt.Errorf("%s 缺少 server", label)
	}
	if server != WAN {
		if _, err := netip.ParseAddr(server); err != nil {
			return fmt.Errorf("%s.server 只能是 IP 或 wan", label)
		}
	}
	if err := wantEnum(item, "protocol", label+".protocol", []string{"udp", "tcp"}); err != nil {
		return err
	}
	return checkPortField(item, "port", label+".port")
}

func checkExtras(m map[string]any, key, label string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	list, isList := v.([]any)
	if !isList {
		return fmt.Errorf("%s 必须是数组", label)
	}
	if len(list) > maxExtras {
		return fmt.Errorf("%s 最多 %d 个", label, maxExtras)
	}
	for i, e := range list {
		item, isObj := e.(map[string]any)
		if !isObj {
			return fmt.Errorf("%s 第 %d 项必须是对象", label, i+1)
		}
		l := fmt.Sprintf("%s 第 %d 项", label, i+1)
		server, present, err := wantString(item, "server", l+"的 server")
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("%s缺少 server", l)
		}
		if err := wantEnum(item, "protocol", l+"的 protocol", dnsProtocols); err != nil {
			return err
		}
		proto, _, _ := wantString(item, "protocol", l+"的 protocol")
		if err := validUpstreamAddr(server, proto, l+"的 server"); err != nil {
			return err
		}
		if err := checkPortField(item, "port", l+"的 port"); err != nil {
			return err
		}
		if err := checkBootstrap(item, "bootstrap", l+"的 bootstrap"); err != nil {
			return err
		}
	}
	return nil
}

func checkTraffic(p map[string]any) error {
	tr, err := wantObject(p, "traffic", "traffic")
	if err != nil || tr == nil {
		return err
	}
	n, ok, err := wantInt(tr, "keepMonths", "traffic.keepMonths")
	if err != nil || !ok {
		return err
	}
	tr["keepMonths"] = float64(min(max(n, minKeepMonths), maxKeepMonths))
	return nil
}

func checkUpdates(p map[string]any) error {
	up, err := wantObject(p, "updates", "updates")
	if err != nil || up == nil {
		return err
	}
	ob, err := wantObject(up, "openbox", "updates.openbox")
	if err != nil || ob == nil {
		return err
	}
	if err := wantBool(ob, "auto", "updates.openbox.auto"); err != nil {
		return err
	}
	for _, k := range []string{"channel", "checkChannel"} {
		if err := wantEnum(ob, k, "updates.openbox."+k, updateChannels); err != nil {
			return err
		}
	}
	if n, ok, err := wantInt(ob, "hour", "updates.openbox.hour"); err != nil {
		return err
	} else if ok && (n < 0 || n > 23) {
		return fmt.Errorf("updates.openbox.hour 只能是 0~23")
	}
	if n, ok, err := wantInt(ob, "days", "updates.openbox.days"); err != nil {
		return err
	} else if ok && (n < 1 || n > 365) {
		return fmt.Errorf("updates.openbox.days 只能是 1~365")
	}
	return nil
}

// checkEntryDNS 校验链式代理和终端分流条目里的自定义 DNS({ url, bootstrap? },对齐上游 v0.1.473 的面板):
// url 是 https:// tls:// tcp:// udp:// 地址;主机是域名时要有 IP 形式的 bootstrap。没有这个字段或 url 为空 = 沿用默认。
func checkEntryDNS(p map[string]any) error {
	for _, key := range []string{"chainProxies", "clientRoutes"} {
		list, ok := p[key].([]any)
		if !ok {
			continue
		}
		for i, e := range list {
			m, isObj := e.(map[string]any)
			if !isObj {
				continue
			}
			if err := checkEntryDNSValue(m, fmt.Sprintf("%s[%d].dns", key, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkEntryDNSValue(entry map[string]any, label string) error {
	raw, ok := entry["dns"]
	if !ok || raw == nil {
		return nil
	}
	m, isObj := raw.(map[string]any)
	if !isObj {
		return fmt.Errorf("%s 必须是对象", label)
	}
	addr, _, err := wantString(m, "url", label+".url")
	if err != nil {
		return err
	}
	bootstrap, _, err := wantString(m, "bootstrap", label+".bootstrap")
	if err != nil {
		return err
	}
	addr, bootstrap = strings.TrimSpace(addr), strings.TrimSpace(bootstrap)
	if addr == "" {
		return nil
	}
	if len(addr) > maxURLLen {
		return fmt.Errorf("%s.url 太长", label)
	}
	u, perr := url.Parse(addr)
	if perr != nil || u.Host == "" || u.User != nil || u.Fragment != "" || !slices.Contains(dnsProtocols, u.Scheme) {
		return fmt.Errorf("%s.url 必须是 https:// tls:// tcp:// 或 udp:// 开头的地址", label)
	}
	if port := u.Port(); port != "" {
		if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%s.url 的端口只能是 1~65535", label)
		}
	}
	if _, perr := netip.ParseAddr(u.Hostname()); perr == nil {
		return nil
	}
	if u.Scheme == "udp" || u.Scheme == "tcp" {
		return fmt.Errorf("%s.url 的 udp / tcp 地址只能是 IP", label)
	}
	if _, perr := netip.ParseAddr(bootstrap); perr != nil {
		return fmt.Errorf("%s.bootstrap 必须是 IP 地址(服务器是域名时用它来解析)", label)
	}
	return nil
}
