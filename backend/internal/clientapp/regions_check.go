package clientapp

// 地区分流(档案 shareRegions)和路由器标识(档案 serverInfo)的保存校验与规整。
//
// 依据:前端 api/clientApp.ts 里写的上限、ShareRegionsList.vue / ShareRegionEditDialog.vue 的校验(前端注释说
// 「后端才是权威」),以及 STATUS.md 记下的规则;没有参照上游。错误文案是英文(前端注释写明回 400 + 英文原因,
// 原样显示给用户),形如 `region CN is in both cn and x`。
//
// 校验通过后返回规整过的新值(不改入参):字符串去首尾空白、地区去重、地区分流里缺的 DNS 字段按地区补默认值、
// 系统 DNS(wan)固定成 UDP 53、组里不认识的键丢掉。规整是幂等的:默认三组(DefaultShareRegions)规整后与原样相同。
// 校验只在客户端写这两个键时做;档案里已经存着的坏数据不在这里处理(见 regionsOf 的降级)。

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 校验的上限。前端 SHARE_REGION_LIMITS / SERVER_INFO_NAME_MAX 是同一组数字;
// maxRegionsPerGroup、maxIconChars 是自己加的输入上限(前端没写),免得一个请求里塞进过大的东西。
const (
	MaxShareGroups   = 12
	MaxRulesPerGroup = 200

	maxGroupName       = 30
	maxGroupDesc       = 120
	maxRuleURL         = 2048
	maxRegionsPerGroup = 300
	maxDNSExtras       = 3

	MaxServerName   = 40
	maxIconChars    = 64
	MaxIconSVGBytes = 64 << 10
)

var (
	groupIDRe  = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	regionRe   = regexp.MustCompile(`^[A-Z]{2}$`)
	geoRe      = regexp.MustCompile(`^[a-z0-9!@._-]{1,80}$`)
	domainRe   = regexp.MustCompile(`^[A-Za-z0-9*_.-]{1,253}$`)
	ruleURLRe  = regexp.MustCompile(`(?i)^https?://\S+$`)
	ruleTypes  = []string{"geosite", "geoip", "domain", "domainSuffix", "domainKeyword", "ipcidr", "ruleUrl"}
	actions    = []string{"direct", "proxy"}
	dnsProtos  = []string{"udp", "tcp"}
	matchModes = []string{"inside", "outside"}
)

func oneOf(s string, list []string) bool {
	for _, x := range list {
		if s == x {
			return true
		}
	}
	return false
}

// short 截断过长的值再放进错误信息,免得把整段输入原样回显。
func short(s string) string {
	if utf8.RuneCountInString(s) <= 60 {
		return s
	}
	return string([]rune(s)[:60]) + "…"
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// optString 取可选的字符串字段:没有或为 null 返回空串;不是字符串报错。返回值已去首尾空白。
func optString(m map[string]any, key, where string) (string, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return "", nil
	}
	s, isStr := v.(string)
	if !isStr {
		return "", fmt.Errorf("%s: %s must be a string", where, key)
	}
	return strings.TrimSpace(s), nil
}

// NormalizeShareRegions 校验并规整 shareRegions。空数组合法(= 恢复默认三组)。
func NormalizeShareRegions(v any) ([]any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, errors.New("shareRegions must be an array")
	}
	if len(arr) > MaxShareGroups {
		return nil, fmt.Errorf("at most %d groups are allowed", MaxShareGroups)
	}
	out := make([]any, 0, len(arr))
	ids := map[string]bool{}
	claimed := map[string]string{} // 「在这些地区」的组已经占用的地区 → 组 id
	defaultID := ""
	for i, e := range arr {
		where := fmt.Sprintf("group #%d", i+1)
		m, isObj := e.(map[string]any)
		if !isObj {
			return nil, fmt.Errorf("%s must be an object", where)
		}
		g, err := normalizeGroup(where, m)
		if err != nil {
			return nil, err
		}
		id := g["id"].(string)
		if ids[id] {
			return nil, fmt.Errorf("duplicate group id %s", id)
		}
		ids[id] = true
		if g["default"] == true {
			if defaultID != "" {
				return nil, fmt.Errorf("only one default group is allowed (%s and %s)", defaultID, id)
			}
			defaultID = id
		}
		if g["regionMatch"] != "outside" {
			for _, r := range g["regions"].([]any) {
				code := r.(string)
				if other, taken := claimed[code]; taken {
					return nil, fmt.Errorf("region %s is in both %s and %s", code, other, id)
				}
				claimed[code] = id
			}
		}
		out = append(out, g)
	}
	return out, nil
}

func normalizeGroup(where string, m map[string]any) (map[string]any, error) {
	id, err := optString(m, "id", where)
	if err != nil {
		return nil, err
	}
	if !groupIDRe.MatchString(id) {
		return nil, fmt.Errorf("%s: id must match ^[a-z0-9_-]{1,32}$", where)
	}
	where = "group " + id

	name, err := optString(m, "name", where)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("%s: name is required", where)
	}
	if utf8.RuneCountInString(name) > maxGroupName {
		return nil, fmt.Errorf("%s: name is longer than %d characters", where, maxGroupName)
	}
	desc, err := optString(m, "description", where)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(desc) > maxGroupDesc {
		return nil, fmt.Errorf("%s: description is longer than %d characters", where, maxGroupDesc)
	}

	regions, err := normalizeRegionCodes(where, m["regions"])
	if err != nil {
		return nil, err
	}
	match, err := optString(m, "regionMatch", where)
	if err != nil {
		return nil, err
	}
	if match != "" && !oneOf(match, matchModes) {
		return nil, fmt.Errorf("%s: regionMatch must be inside or outside", where)
	}
	if match == "outside" && len(regions) == 0 {
		return nil, fmt.Errorf("%s: an outside group needs at least one region", where)
	}
	isDefault := false
	if v, ok := m["default"]; ok && v != nil {
		b, isBool := v.(bool)
		if !isBool {
			return nil, fmt.Errorf("%s: default must be true or false", where)
		}
		isDefault = b
	}

	rules, err := normalizeRules(where, m["rules"])
	if err != nil {
		return nil, err
	}
	catchAll, err := optString(m, "catchAll", where)
	if err != nil {
		return nil, err
	}
	if !oneOf(catchAll, actions) {
		return nil, fmt.Errorf("%s: catchAll must be direct or proxy", where)
	}

	inCN := contains(regions, "CN")
	if match == "outside" {
		inCN = !inCN
	}
	dns, err := normalizeGroupDNS(where, m["dns"], inCN)
	if err != nil {
		return nil, err
	}

	g := map[string]any{
		"id": id, "name": name, "regions": regions, "rules": rules, "catchAll": catchAll, "dns": dns,
	}
	if desc != "" {
		g["description"] = desc
	}
	if match == "outside" {
		g["regionMatch"] = "outside"
	}
	if isDefault {
		g["default"] = true
	}
	return g, nil
}

func contains(list []any, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// normalizeRegionCodes 要求两位大写国家码;组内重复的静默去重(保持第一次出现的顺序)。
func normalizeRegionCodes(where string, v any) ([]any, error) {
	out := []any{}
	if v == nil {
		return out, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: regions must be an array", where)
	}
	if len(arr) > maxRegionsPerGroup {
		return nil, fmt.Errorf("%s: at most %d regions are allowed", where, maxRegionsPerGroup)
	}
	seen := map[string]bool{}
	for _, e := range arr {
		s, isStr := e.(string)
		if !isStr || !regionRe.MatchString(s) {
			return nil, fmt.Errorf("%s: region %s is not a two-letter uppercase country code", where, short(fmt.Sprint(e)))
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func normalizeRules(where string, v any) ([]any, error) {
	out := []any{}
	if v == nil {
		return out, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: rules must be an array", where)
	}
	if len(arr) > MaxRulesPerGroup {
		return nil, fmt.Errorf("%s: at most %d rules are allowed", where, MaxRulesPerGroup)
	}
	for i, e := range arr {
		rw := fmt.Sprintf("%s rule #%d", where, i+1)
		m, isObj := e.(map[string]any)
		if !isObj {
			return nil, fmt.Errorf("%s must be an object", rw)
		}
		typ, err := optString(m, "type", rw)
		if err != nil {
			return nil, err
		}
		if !oneOf(typ, ruleTypes) {
			return nil, fmt.Errorf("%s: type must be one of %s", rw, strings.Join(ruleTypes, ", "))
		}
		value, err := optString(m, "value", rw)
		if err != nil {
			return nil, err
		}
		if !validRuleValue(typ, value) {
			return nil, fmt.Errorf("%s: %q is not a valid %s value", rw, short(value), typ)
		}
		action, err := optString(m, "action", rw)
		if err != nil {
			return nil, err
		}
		if !oneOf(action, actions) {
			return nil, fmt.Errorf("%s: action must be direct or proxy", rw)
		}
		out = append(out, map[string]any{"type": typ, "value": value, "action": action})
	}
	return out, nil
}

// validRuleValue 与前端 ShareRegionEditDialog 的 ruleValueOk 同一套。
func validRuleValue(typ, v string) bool {
	switch typ {
	case "geosite", "geoip":
		return geoRe.MatchString(v)
	case "ipcidr":
		return isIPOrCIDR(v)
	case "ruleUrl":
		return len(v) <= maxRuleURL && ruleURLRe.MatchString(v) && !hasControl(v)
	}
	return domainRe.MatchString(v)
}

// isIPOrCIDR 接受单个 IP 或 IP/前缀长度;不收带区域的地址(fe80::1%eth0),前缀只收纯数字。
func isIPOrCIDR(raw string) bool {
	addr, prefix, hasPrefix := strings.Cut(raw, "/")
	ip, err := netip.ParseAddr(addr)
	if err != nil || ip.Zone() != "" {
		return false
	}
	if !hasPrefix {
		return true
	}
	if prefix == "" || len(prefix) > 3 {
		return false
	}
	n := 0
	for _, r := range prefix {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
	}
	return n <= ip.BitLen()
}

// normalizeGroupDNS 校验并补全一组的 DNS。没给的字段用地区默认值(含中国大陆与否决定代理侧默认);
// 上游只收 IP 或 wan,备用最多 3 条;wan 固定 UDP 53(手机此刻所在网络的系统 DNS)。
func normalizeGroupDNS(where string, v any, inCN bool) (map[string]any, error) {
	def := regionDNS(inCN)
	var m map[string]any
	if v != nil {
		var ok bool
		if m, ok = v.(map[string]any); !ok {
			return nil, fmt.Errorf("%s: dns must be an object", where)
		}
	}
	out := map[string]any{}
	sides := []struct {
		key           string
		server, proto string
		port          int
	}{
		{"direct", def.Direct, def.DirectProtocol, def.DirectPort},
		{"proxy", def.Proxy, def.ProxyProtocol, def.ProxyPort},
	}
	for _, s := range sides {
		label := where + " dns." + s.key
		server, proto, port, err := upstreamOf(m, s.key, s.key+"Protocol", s.key+"Port", s.server, s.proto, s.port, label)
		if err != nil {
			return nil, err
		}
		out[s.key], out[s.key+"Protocol"], out[s.key+"Port"] = server, proto, float64(port)

		extras := []any{}
		if raw, ok := m[s.key+"Extras"]; ok && raw != nil {
			arr, isArr := raw.([]any)
			if !isArr {
				return nil, fmt.Errorf("%s: %sExtras must be an array", where+" dns", s.key)
			}
			if len(arr) > maxDNSExtras {
				return nil, fmt.Errorf("%s: at most %d extra upstreams are allowed", label, maxDNSExtras)
			}
			for i, e := range arr {
				item, isObj := e.(map[string]any)
				if !isObj {
					return nil, fmt.Errorf("%s extra #%d must be an object", label, i+1)
				}
				srv, pr, pt, err := upstreamOf(item, "server", "protocol", "port", "", "udp", 53, fmt.Sprintf("%s extra #%d", label, i+1))
				if err != nil {
					return nil, err
				}
				extras = append(extras, map[string]any{"server": srv, "protocol": pr, "port": float64(pt)})
			}
		}
		out[s.key+"Extras"] = extras
	}
	return out, nil
}

// upstreamOf 读一条上游(服务器、协议、端口)。defServer 为空表示服务器必填。
func upstreamOf(m map[string]any, serverKey, protoKey, portKey, defServer, defProto string, defPort int, label string) (string, string, int, error) {
	server, err := optString(m, serverKey, label)
	if err != nil {
		return "", "", 0, err
	}
	if server == "" {
		if defServer == "" {
			return "", "", 0, fmt.Errorf("%s: server is required", label)
		}
		server = defServer
	}
	proto, err := optString(m, protoKey, label)
	if err != nil {
		return "", "", 0, err
	}
	if proto == "" {
		proto = defProto
	}
	if !oneOf(proto, dnsProtos) {
		return "", "", 0, fmt.Errorf("%s: protocol must be udp or tcp", label)
	}
	port := defPort
	if raw, ok := m[portKey]; ok && raw != nil {
		f, isNum := raw.(float64)
		if !isNum || f != float64(int(f)) || f < 1 || f > 65535 {
			return "", "", 0, fmt.Errorf("%s: port must be an integer between 1 and 65535", label)
		}
		port = int(f)
	}
	if server == wanUpstream {
		return server, "udp", 53, nil
	}
	ip, perr := netip.ParseAddr(server)
	if perr != nil || ip.Zone() != "" {
		return "", "", 0, fmt.Errorf("%s: server must be an IP address or wan", label)
	}
	return server, proto, port, nil
}

// NormalizeServerInfo 校验并规整 serverInfo(整份覆盖,没给的字段就是没有)。
// 空串等同没给,会被去掉,所以 {} 表示全用默认。不认识的键原样保留(读取时忽略)。
func NormalizeServerInfo(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("serverInfo must be an object")
	}
	out := make(map[string]any, len(m))
	for k, e := range m {
		out[k] = e
	}
	for _, key := range []string{"name", "icon", "iconSvg", "region"} {
		s, err := optString(m, key, "serverInfo")
		if err != nil {
			return nil, err
		}
		if s == "" {
			delete(out, key)
			continue
		}
		out[key] = s
	}
	if s, ok := out["name"].(string); ok && utf8.RuneCountInString(s) > MaxServerName {
		return nil, fmt.Errorf("serverInfo: name is longer than %d characters", MaxServerName)
	}
	if s, ok := out["icon"].(string); ok && (utf8.RuneCountInString(s) > maxIconChars || hasControl(s)) {
		return nil, fmt.Errorf("serverInfo: icon is not a valid icon code")
	}
	if s, ok := out["iconSvg"].(string); ok {
		if len(s) > MaxIconSVGBytes {
			return nil, fmt.Errorf("serverInfo: iconSvg is larger than %d KB", MaxIconSVGBytes>>10)
		}
		if len(s) < 4 || !strings.EqualFold(s[:4], "<svg") {
			return nil, errors.New("serverInfo: iconSvg must start with <svg")
		}
	}
	if s, ok := out["region"].(string); ok && !regionRe.MatchString(s) {
		return nil, errors.New("serverInfo: region must be a two-letter uppercase country code")
	}
	return out, nil
}
