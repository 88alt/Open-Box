package nodeexit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/regiondetect"
)

// 对齐上游 v0.1.423 的节点出口信息来源:全部来自 ip.net.coffee。
//  1. 经节点取 cdn-cgi/trace(出口 IP 和国家);
//  2. 按出口 IP 取 ipv2/lookup(属地、ASN、类型、信任分、风险评估);
//  3. 按 ASN 取 ipv2/radar(近 28 天真人流量占比),和 lookup 一起估出 humanTraffic。
//
// 只读参考了上游面板服务端可读部分的行为(字段取舍、校验、估算系数),代码自写。

// TraceURL 是经节点问出口 IP 的地址。
const TraceURL = "https://ip.net.coffee/cdn-cgi/trace"

const netCoffeeHost = "ip.net.coffee"

// Lookups 是 lookup / radar 两个接口的访问方式。零值 = 正式用法(withDefaults 补全)。
type Lookups struct {
	Client    *http.Client
	LookupURL func(ip string) string
	RadarURL  func(asn string) string
}

func (l Lookups) withDefaults() Lookups {
	if l.Client == nil {
		l.Client = &http.Client{
			Timeout:   lookupWait,
			Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: lookupWait, DisableKeepAlives: true},
			// 只跟到 ip.net.coffee 自己、只走 https、最多 3 跳
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 3 {
					return errors.New("跳转太多")
				}
				if req.URL.Scheme != "https" || req.URL.Hostname() != netCoffeeHost {
					return errors.New("跳转到了不允许的地址")
				}
				return nil
			},
		}
	}
	if l.LookupURL == nil {
		l.LookupURL = func(ip string) string { return "https://" + netCoffeeHost + "/api/ipv2/lookup/" + ip }
	}
	if l.RadarURL == nil {
		l.RadarURL = func(asn string) string { return "https://" + netCoffeeHost + "/api/ipv2/radar/" + asn }
	}
	return l
}

func (l Lookups) get(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "openbox-backend")
	resp, err := l.Client.Do(req)
	if err != nil {
		return nil, errors.New(shortErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, bodyMax))
	if err != nil {
		return nil, errors.New(shortErr(err))
	}
	return b, nil
}

// shortErr 只留原因,不带完整 URL(URL 里有出口 IP,错误会原样给前端显示,没必要)。
func shortErr(err error) string {
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		return "超时"
	}
	if errors.Is(err, context.Canceled) {
		return "已取消"
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

// ---- trace ----

var countryRe = regexp.MustCompile(`^[A-Z]{2}$`)

// parseTrace 解析 cdn-cgi/trace(每行 key=value):必须是 ip.net.coffee 答的、ip 是公网地址,否则 ok=false。
func parseTrace(body string) (ip netip.Addr, country string, ok bool) {
	kv := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		line = strings.TrimRight(line, "\r")
		i := strings.Index(line, "=")
		if i < 0 {
			continue
		}
		kv[line[:i]] = strings.TrimSpace(line[i+1:])
	}
	if kv["h"] != netCoffeeHost {
		return netip.Addr{}, "", false
	}
	a, err := netip.ParseAddr(kv["ip"])
	if err != nil {
		return netip.Addr{}, "", false
	}
	a = a.Unmap()
	if !regiondetect.Public(a) {
		return netip.Addr{}, "", false
	}
	if countryRe.MatchString(kv["loc"]) {
		country = kv["loc"]
	}
	return a, country, true
}

// ---- lookup ----

// Threat 是风险评估里的一条标签。
type Threat struct {
	Label    string `json:"label"`
	Severity string `json:"severity"`
}

// RiskAssessment 对应前端 OpenboxNodeRiskAssessment。
type RiskAssessment struct {
	Threats      []Threat `json:"threats"`
	AbuseLevel   string   `json:"abuseLevel"`
	AbuseScore   *float64 `json:"abuseScore"`
	HTTPBlack    *float64 `json:"httpBlacklist"`
	RPKI         string   `json:"rpki"`
	RPKIReported string   `json:"rpkiReported"`
	VPN          *bool    `json:"vpn"`
	Proxy        *bool    `json:"proxy"`
	Tor          *bool    `json:"tor"`
	Crawler      *bool    `json:"crawler"`
}

// HumanTraffic 对应前端 OpenboxNodeHumanTraffic。
type HumanTraffic struct {
	Human float64 `json:"human"`
	Bot   float64 `json:"bot"`
	Basis string  `json:"basis"`
	Stale bool    `json:"stale"`
}

// netInfo 是 lookup 解析后的结果(不含出口 IP 本身)。
type netInfo struct {
	Country           string
	Location          string
	ASN               string
	ASNOwner          string
	IPType            string
	ProxyType         string
	TrustScore        *float64
	Risk              RiskAssessment
	RegisteredCountry string
	Human             *HumanTraffic
}

// ---- JSON 取值小工具(上游是 JS,按它的真值与类型习惯取) ----

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}

func isTrue(v any) bool { b, ok := v.(bool); return ok && b }

func boolPtr(v any) *bool {
	if b, ok := v.(bool); ok {
		return &b
	}
	return nil
}

// str 对应 JS 的 String(v || ""):空值、0、false 给空串。
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == 0 || math.IsNaN(x) {
			return ""
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
	}
	return ""
}

// num 对应上游的 u():数字,或非空的数字字符串;否则 nil。
func num(v any) *float64 {
	switch x := v.(type) {
	case float64:
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			return &x
		}
	case string:
		if strings.TrimSpace(x) != "" {
			if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil && !math.IsInf(f, 0) {
				return &f
			}
		}
	}
	return nil
}

func firstString(vs ...any) string {
	for _, v := range vs {
		if s := str(v); s != "" {
			return s
		}
	}
	return ""
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// decodeLookup 解析 lookup 正文并校验:必须是这个 IP 的、不是 bogon、没有 error。
func decodeLookup(body []byte, ip string) map[string]any {
	var r map[string]any
	if json.Unmarshal(body, &r) != nil || r == nil {
		return nil
	}
	if truthy(r["error"]) || truthy(r["is_bogon"]) {
		return nil
	}
	if s, _ := r["ip"].(string); s != ip {
		return nil
	}
	return r
}

var abuseScoreRe = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)(?:\s*\([^)]*\))?\s*$`)

// parseLookup 对应上游的 CP:拿不到任何有用信息(国家、ASN、类型、信任分都没有)时返回 nil。
func parseLookup(body []byte, ip string) *netInfo {
	r := decodeLookup(body, ip)
	if r == nil {
		return nil
	}
	country := strings.ToUpper(str(r["countryCode"]))
	reg := strings.ToUpper(str(r["registered_country_code"]))
	asn := ""
	if a := str(r["asn"]); regexp.MustCompile(`^\d+$`).MatchString(a) {
		asn = "AS" + a
	} else if regexp.MustCompile(`(?i)^AS\d+$`).MatchString(a) {
		asn = strings.ToUpper(a)
	}
	kind := strings.ToLower(firstString(r["company_type"], r["asn_kind"]))
	ipType := ""
	switch {
	case isTrue(r["is_mobile"]):
		ipType = "mobile"
	case isTrue(r["isResidential"]) || kind == "residential":
		ipType = "residential"
	case isTrue(r["is_datacenter"]) || kind == "hosting" || kind == "cdn":
		ipType = "hosting"
	case kind == "business":
		ipType = "business"
	}
	var trust *float64
	if f, ok := r["trust_score"].(float64); ok && !math.IsNaN(f) && f >= 0 && f <= 100 {
		trust = &f
	}
	if !countryRe.MatchString(country) && asn == "" && ipType == "" && trust == nil {
		return nil
	}
	intel := obj(r["intelligence"])

	info := &netInfo{ASN: asn, ASNOwner: firstString(r["asOrganization"], r["isp"], r["company_name"]), IPType: ipType, TrustScore: trust}
	if countryRe.MatchString(country) {
		info.Country = country
	}
	if countryRe.MatchString(reg) {
		info.RegisteredCountry = reg
	}
	var parts []string
	for _, k := range []string{"country", "region", "city"} {
		s, _ := r[k].(string)
		if strings.TrimSpace(s) == "" {
			continue
		}
		dup := false
		for _, p := range parts {
			if p == s {
				dup = true
			}
		}
		if !dup {
			parts = append(parts, s)
		}
	}
	info.Location = strings.Join(parts, " · ")
	var kinds []string
	for _, p := range []struct{ k, n string }{{"is_vpn", "VPN"}, {"is_proxy", "Proxy"}, {"is_tor", "Tor"}} {
		if isTrue(r[p.k]) {
			kinds = append(kinds, p.n)
		}
	}
	info.ProxyType = strings.Join(kinds, " · ")

	ra := RiskAssessment{
		Threats: parseThreats(intel["threats"]),
		RPKI:    "",
		VPN:     boolPtr(r["is_vpn"]), Proxy: boolPtr(r["is_proxy"]), Tor: boolPtr(r["is_tor"]), Crawler: boolPtr(r["is_crawler"]),
	}
	if lv, ok := intel["abuser_level"].(string); ok {
		ra.AbuseLevel = strings.ToLower(clip(lv, 30))
	}
	raw := intel["abuser_score_raw"]
	if raw == nil {
		raw = r["abuser_score"]
	}
	rawS := ""
	switch x := raw.(type) {
	case string:
		rawS = x
	case float64:
		rawS = strconv.FormatFloat(x, 'f', -1, 64)
	}
	if m := abuseScoreRe.FindStringSubmatch(rawS); m != nil {
		ra.AbuseScore = num(m[1])
	}
	hb := intel["rep_threat"]
	if hb == nil {
		hb = intel["httpbl_threat"]
	}
	ra.HTTPBlack = num(hb)
	switch s := strings.ToLower(str(r["rpki_status"])); s {
	case "valid", "invalid", "notfound":
		ra.RPKIReported = s
	}
	info.Risk = ra
	return info
}

// parseThreats:数组里每一项都要有字符串 label 才认,否则整块是 null;最多 20 条,label 最长 200。
func parseThreats(v any) []Threat {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]Threat, 0, len(arr))
	for _, it := range arr {
		m := obj(it)
		l, ok := m["label"].(string)
		if m == nil || !ok {
			return nil
		}
		if len(out) >= 20 {
			continue
		}
		sev, _ := m["severity"].(string)
		if sev != "bad" && sev != "warn" && sev != "info" {
			sev = "info"
		}
		out = append(out, Threat{Label: clip(l, 200), Severity: sev})
	}
	return out
}

// humanTraffic 对应上游的 MP。radar 是 ipv2/radar 的正文(可为空)。
// 有 28 天的 ASN 统计就直接用;没有就按网络类型估;什么线索都没有返回 nil。
func humanTraffic(lookup []byte, radar string, ip string) *HumanTraffic {
	n := decodeLookup(lookup, ip)
	if n == nil {
		return nil
	}
	var o map[string]any
	_ = json.Unmarshal([]byte(radar), &o)
	s := strings.ToLower(str(n["company_type"]))
	i := strings.ToLower(str(n["asn_kind"]))
	asnDigits := regexp.MustCompile(`(?i)^AS`).ReplaceAllString(str(n["asn"]), "")
	var human float64
	hv, hok := o["human"].(float64)
	a := o != nil && isTrue(o["ok"]) && str(o["asn"]) == asnDigits && str(o["range"]) == "28d" && hok && !math.IsNaN(hv) && hv >= 0 && hv <= 100
	if !a && s == "" && i == "" && !truthy(n["is_public_service"]) && !truthy(n["is_crawler"]) && !truthy(n["is_mobile"]) && !truthy(n["is_datacenter"]) && !truthy(n["isResidential"]) {
		return nil
	}
	switch {
	case a:
		human = hv
	case truthy(n["is_public_service"]):
		human = 2
	case truthy(n["is_crawler"]):
		human = 6
	case truthy(n["is_mobile"]) || i == "mobile":
		human = 93
	case truthy(n["is_datacenter"]) || s == "hosting" || i == "hosting" || i == "cdn":
		human = 18
	case s == "business":
		human = 65
	default:
		human = 88
	}
	switch {
	case isTrue(n["is_tor"]):
		human *= .4
	case isTrue(n["is_proxy"]) || isTrue(n["is_vpn"]):
		human *= .6
	}
	if isTrue(n["is_crawler"]) {
		human = math.Min(human, human*.3+2)
	}
	if isTrue(n["is_abuser"]) {
		human -= 5
	}
	human = math.Round(math.Max(1, math.Min(99, human))*10) / 10
	basis := "network-estimate"
	if a {
		basis = "asn-28d"
	}
	return &HumanTraffic{Human: human, Bot: math.Round((100-human)*10) / 10, Basis: basis, Stale: a && isTrue(o["stale"])}
}
