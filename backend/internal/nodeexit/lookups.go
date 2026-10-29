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
	"strings"
)

// allowedHosts 是这两路查询(含它们的跳转)允许去的主机:rdap.org 会把请求跳转到各大洲的 RIR。
var allowedHosts = map[string]bool{
	"proxycheck.io":    true,
	"rdap.org":         true,
	"rdap.arin.net":    true,
	"rdap.db.ripe.net": true,
	"rdap.apnic.net":   true,
	"rdap.lacnic.net":  true,
	"rdap.afrinic.net": true,
}

// Lookups 是两家第三方服务的访问方式。零值 = 正式用法(withDefaults 补全)。
type Lookups struct {
	Client        *http.Client
	ProxycheckURL func(ip netip.Addr) string
	RDAPURL       func(ip netip.Addr) string
}

func (l Lookups) withDefaults() Lookups {
	if l.Client == nil {
		l.Client = &http.Client{
			Timeout:   lookupWait,
			Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: lookupWait, DisableKeepAlives: true},
			// 只跟到白名单主机、只走 https、最多 3 跳:第三方不能把我们带去别处(例如内网地址)
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 3 {
					return errors.New("跳转太多")
				}
				if req.URL.Scheme != "https" || !allowedHosts[req.URL.Hostname()] {
					return errors.New("跳转到了不允许的地址")
				}
				return nil
			},
		}
	}
	if l.ProxycheckURL == nil {
		// v3 接口:不带 key,免费每天 100 次(按路由器的出口地址算),所以结果要缓存
		l.ProxycheckURL = func(ip netip.Addr) string { return "https://proxycheck.io/v3/" + ip.String() }
	}
	if l.RDAPURL == nil {
		l.RDAPURL = func(ip netip.Addr) string { return "https://rdap.org/ip/" + ip.String() }
	}
	return l
}

func (l Lookups) get(ctx context.Context, url string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json, application/rdap+json")
	req.Header.Set("User-Agent", "openbox-backend")
	resp, err := l.Client.Do(req)
	if err != nil {
		return 0, nil, errors.New(shortErr(err))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, bodyMax))
	if err != nil {
		return 0, nil, errors.New(shortErr(err))
	}
	return resp.StatusCode, b, nil
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

// ---- proxycheck.io v3 ----

type proxycheckInfo struct {
	ASN, Owner, IPType, ProxyType string
	Risk, Devices                 *int
	City, Region, CountryName     string
}

func (p *proxycheckInfo) location() string {
	var parts []string
	for _, s := range []string{p.City, p.Region, p.CountryName} {
		if s = strings.TrimSpace(s); s != "" && (len(parts) == 0 || parts[len(parts)-1] != s) {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

func (l Lookups) proxycheck(ctx context.Context, ip netip.Addr) (*proxycheckInfo, error) {
	code, b, err := l.get(ctx, l.ProxycheckURL(ip))
	if err != nil {
		return nil, err
	}
	return parseProxycheck(code, b, ip)
}

// parseProxycheck 解析 v3 的答话:顶层 status / message,以及以查询的地址为键的结果对象
// (network / location / device_estimate / detections)。denied(次数用完、被限速)和 error 带 message。
func parseProxycheck(code int, b []byte, ip netip.Addr) (*proxycheckInfo, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		if code < 200 || code > 299 {
			return nil, fmt.Errorf("HTTP %d", code)
		}
		return nil, errors.New("返回的不是 JSON")
	}
	var status, msg string
	_ = json.Unmarshal(top["status"], &status)
	_ = json.Unmarshal(top["message"], &msg)
	if status == "denied" || status == "error" || (status == "" && (code < 200 || code > 299)) {
		if msg = strings.TrimSpace(msg); msg != "" {
			if r := []rune(msg); len(r) > 120 {
				msg = string(r[:120]) + "…"
			}
			return nil, fmt.Errorf("被拒绝:%s", msg)
		}
		return nil, fmt.Errorf("HTTP %d", code)
	}
	raw, ok := top[ip.String()]
	if !ok { // IPv6 的键写法可能和我们的不同:找唯一一个带 network / detections 的对象
		for k, v := range top {
			if k == "status" || k == "message" {
				continue
			}
			var probe struct {
				Network    json.RawMessage `json:"network"`
				Detections json.RawMessage `json:"detections"`
			}
			if json.Unmarshal(v, &probe) == nil && (probe.Network != nil || probe.Detections != nil) {
				raw, ok = v, true
				break
			}
		}
	}
	if !ok {
		return nil, errors.New("答复里没有这个 IP 的结果")
	}
	var v struct {
		Network struct {
			ASN          string `json:"asn"`
			Provider     string `json:"provider"`
			Organisation string `json:"organisation"`
			Type         string `json:"type"`
		} `json:"network"`
		Location struct {
			CountryName string `json:"country_name"`
			RegionName  string `json:"region_name"`
			CityName    string `json:"city_name"`
		} `json:"location"`
		Device struct {
			Subnet *float64 `json:"subnet"`
		} `json:"device_estimate"`
		Det struct {
			Proxy       bool     `json:"proxy"`
			VPN         bool     `json:"vpn"`
			TOR         bool     `json:"tor"`
			Scraper     bool     `json:"scraper"`
			Compromised bool     `json:"compromised"`
			Risk        *float64 `json:"risk"`
		} `json:"detections"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, errors.New("答复的格式不对")
	}
	info := &proxycheckInfo{
		ASN: strings.TrimSpace(v.Network.ASN), Owner: firstNonEmpty(v.Network.Organisation, v.Network.Provider),
		City: v.Location.CityName, Region: v.Location.RegionName, CountryName: v.Location.CountryName,
		IPType: ipTypeOf(v.Network.Type), Risk: pct(v.Det.Risk), Devices: count(v.Device.Subnet),
	}
	var kinds []string
	for _, k := range []struct {
		on   bool
		name string
	}{{v.Det.TOR, "TOR"}, {v.Det.VPN, "VPN"}, {v.Det.Proxy, "Proxy"}, {v.Det.Scraper, "Scraper"}, {v.Det.Compromised, "Compromised"}} {
		if k.on {
			kinds = append(kinds, k.name)
		}
	}
	info.ProxyType = strings.Join(kinds, " / ")
	return info, nil
}

// ipTypeOf 把 proxycheck 的 network.type 换成前端认的四个值;不认识的(含 null)给空串。
func ipTypeOf(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "residential":
		return "residential"
	case "wireless":
		return "mobile"
	case "business":
		return "business"
	case "hosting":
		return "hosting"
	}
	return ""
}

func pct(f *float64) *int {
	if f == nil || math.IsNaN(*f) || *f < 0 {
		return nil
	}
	n := int(math.Round(*f))
	if n > 100 {
		n = 100
	}
	return &n
}

func count(f *float64) *int {
	if f == nil || math.IsNaN(*f) || *f < 0 || *f > 1e9 {
		return nil
	}
	n := int(math.Round(*f))
	return &n
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// ---- RDAP ----

// rdap 返回 IP 的登记国家(两位大写)。RIPE / APNIC / LACNIC / AFRINIC 在网段对象顶层给 country;
// ARIN 不给,这种情况返回空串(不报错),「原生 IP」就是未知,不去猜。
func (l Lookups) rdap(ctx context.Context, ip netip.Addr) (string, error) {
	code, b, err := l.get(ctx, l.RDAPURL(ip))
	if err != nil {
		return "", err
	}
	if code < 200 || code > 299 {
		return "", fmt.Errorf("HTTP %d", code)
	}
	var v struct {
		Country string `json:"country"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", errors.New("返回的不是 JSON")
	}
	cc := strings.ToUpper(strings.TrimSpace(v.Country))
	if len(cc) != 2 {
		return "", nil
	}
	return cc, nil
}
