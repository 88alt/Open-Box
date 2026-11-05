package profile

import (
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// 字段取值范围。
var (
	ipv6ProxyModes = []string{"node", "ipv4", "bypass"}
	portsModes     = []string{"blacklist", "whitelist"}
	tunStacks      = []string{"mixed", "gvisor", "system"}
	dnsModes       = []string{"off", "hijack", "dnsmasq"}
	dnsProtocols   = []string{"udp", "tcp"}
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
	for _, k := range []string{"bypassPorts", "bypassPortsWhitelist"} {
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
	for _, k := range []string{"split", "fakeIpForProxy"} {
		if err := wantBool(dns, k, "dns."+k); err != nil {
			return err
		}
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
		if err := checkExtras(dns, side+"Extras", "dns."+side+"Extras"); err != nil {
			return err
		}
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

// checkUpstream:上游地址只收 IP 或记号 wan(空串 = 沿用默认)。
func checkUpstream(m map[string]any, key, label string) error {
	s, ok, err := wantString(m, key, label)
	if err != nil || !ok {
		return err
	}
	return validUpstreamAddr(s, label)
}

func validUpstreamAddr(s, label string) error {
	s = strings.TrimSpace(s)
	if s == "" || s == WAN {
		return nil
	}
	if _, err := netip.ParseAddr(s); err != nil {
		return fmt.Errorf("%s 只能填 IP 地址或 wan(路由器系统的上游 DNS)", label)
	}
	return nil
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
		if err := validUpstreamAddr(server, l+"的 server"); err != nil {
			return err
		}
		if err := wantEnum(item, "protocol", l+"的 protocol", dnsProtocols); err != nil {
			return err
		}
		if err := checkPortField(item, "port", l+"的 port"); err != nil {
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
