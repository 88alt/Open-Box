// Package bandwidth 是带宽择优(重写):测速参数、排队、状态与历史。
// 只依据前端 OpenboxBandwidth* 类型与 STATUS 口径,不参照已回退的 3414adff 实现。
package bandwidth

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"
	"time"
)

// Settings 对应档案 bandwidth / OpenboxBandwidthSettings。
type Settings struct {
	IntervalHours int `json:"intervalHours"`
	// IntervalUnit 是面板上周期的显示单位("hours" / "days");IntervalHours 始终是换算后的小时数。
	IntervalUnit  string `json:"intervalUnit"`
	MaxDownloadMB int    `json:"maxDownloadMB"`
	// StartHour 是按「天」测试时每天几点开始(本地时间 0~23,默认 4);按小时测试时不起作用。
	// 对齐上游 v0.1.481 的 bandwidth.startHour。
	StartHour int `json:"startHour"`
	// Endpoint 是测速站点预设(cloudflare / apple / cachefly / custom),对齐上游 v0.1.493。
	// 不同站点的带宽样本彼此隔离;custom 时用 EndpointCustomURL。空 = cloudflare。
	Endpoint string `json:"endpoint,omitempty"`
	// EndpointCustomURL 是自定义测速地址(endpoint=custom 时必填,必须是公开 https)。
	EndpointCustomURL string `json:"endpointCustomUrl,omitempty"`
}

// 测速站点预设,取值与上游面板一致。
const (
	EndpointCloudflare = "cloudflare"
	EndpointApple      = "apple"
	EndpointCachefly   = "cachefly"
	EndpointCustom     = "custom"
)

// DefaultEndpoint 是没写 endpoint 时用的站点(上游面板把 Cloudflare 标为默认)。
const DefaultEndpoint = EndpointCloudflare

// endpointURLs 是各内置站点的下载测速地址。
// Cloudflare 是官方公开的 __down 接口(100 MiB 源,读到上限即停);Apple / CacheFly 用各自的公开测速文件。
// **推断点**:上游是闭源后端选地址,这几条是按各 CDN 公开的测速规范挑的,维护者可改。
var endpointURLs = map[string]string{
	EndpointCloudflare: "https://speed.cloudflare.com/__down?bytes=104857600",
	EndpointApple:      "https://speed.apple.com/speedtest/100MB.bin",
	EndpointCachefly:   "https://cachefly.cachefly.net/100mb.test",
}

// 周期显示单位。
const (
	UnitHours = "hours"
	UnitDays  = "days"
)

const (
	minIntervalHours = 1
	maxIntervalHours = 720
	minDownloadMB    = 1
	maxDownloadMB    = 1024
	// DefaultStartHour 按天测试时的默认开始时刻(上游面板在没写 startHour 时显示 04:00)。
	DefaultStartHour = 4
)

// DefaultSettings 默认测速参数。
// 12 小时 / 64 MB 对齐上游 v0.1.488 面板的缺省值(它读不到档案里的 bandwidth 时按 12 / 64 显示);
// 4 点对齐上游按天测试的默认开始时刻。
func DefaultSettings() Settings {
	return Settings{IntervalHours: 12, IntervalUnit: UnitHours, MaxDownloadMB: 64, StartHour: DefaultStartHour, Endpoint: DefaultEndpoint}
}

// NormalizeSettings 规整档案里的 bandwidth;非法返回错误信息(400)。
func NormalizeSettings(v any) (Settings, error) {
	def := DefaultSettings()
	if v == nil {
		return def, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Settings{}, fmt.Errorf("bandwidth must be an object")
	}
	out := def
	if x, ok := m["intervalHours"]; ok {
		n, err := asInt(x)
		if err != nil || n < minIntervalHours || n > maxIntervalHours {
			return Settings{}, fmt.Errorf("intervalHours must be an integer between %d and %d", minIntervalHours, maxIntervalHours)
		}
		out.IntervalHours = n
	}
	if x, ok := m["intervalUnit"]; ok {
		u, isStr := x.(string)
		if !isStr || (u != UnitHours && u != UnitDays) {
			return Settings{}, fmt.Errorf("intervalUnit must be %q or %q", UnitHours, UnitDays)
		}
		out.IntervalUnit = u
	}
	if x, ok := m["maxDownloadMB"]; ok {
		n, err := asInt(x)
		if err != nil || n < minDownloadMB || n > maxDownloadMB {
			return Settings{}, fmt.Errorf("maxDownloadMB must be an integer between %d and %d", minDownloadMB, maxDownloadMB)
		}
		out.MaxDownloadMB = n
	}
	if x, ok := m["startHour"]; ok {
		n, err := asInt(x)
		if err != nil || n < 0 || n > 23 {
			return Settings{}, fmt.Errorf("startHour must be an integer between 0 and 23")
		}
		out.StartHour = n
	}
	// 上游 v0.1.493 的测速站点。空 = 用默认站点(与上游面板"没写就按 Cloudflare"一致)
	if x, ok := m["endpoint"]; ok {
		e, isStr := x.(string)
		if !isStr {
			return Settings{}, fmt.Errorf("endpoint must be a string")
		}
		e = strings.TrimSpace(e)
		if e != "" && !validEndpoint(e) {
			return Settings{}, fmt.Errorf("endpoint must be one of %s, %s, %s, %s", EndpointCloudflare, EndpointApple, EndpointCachefly, EndpointCustom)
		}
		if e != "" {
			out.Endpoint = e
		}
	}
	if x, ok := m["endpointCustomUrl"]; ok {
		u, isStr := x.(string)
		if !isStr {
			return Settings{}, fmt.Errorf("endpointCustomUrl must be a string")
		}
		out.EndpointCustomURL = strings.TrimSpace(u)
	}
	// 选了自定义站点就必须给合法地址,否则测速会没有目标
	if out.Endpoint == EndpointCustom {
		if err := validCustomURL(out.EndpointCustomURL); err != nil {
			return Settings{}, err
		}
	}
	return out, nil
}

// validEndpoint 判断是不是认识的站点预设。
func validEndpoint(e string) bool {
	switch e {
	case EndpointCloudflare, EndpointApple, EndpointCachefly, EndpointCustom:
		return true
	}
	return false
}

// validCustomURL 校验自定义测速地址:必须是公开的 https,不允许本机 / 内网地址。
func validCustomURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("endpointCustomUrl is required when endpoint is %q", EndpointCustom)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("endpointCustomUrl must be a public https URL")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return fmt.Errorf("endpointCustomUrl must not point to a local or private address")
	}
	// 字面量内网 IP 直接拒绝;域名解析到内网的情况留给测速时处理(与 contentfilter 的 sourcefetch 同口径)
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return fmt.Errorf("endpointCustomUrl must not point to a local or private address")
	}
	return nil
}

// TestEndpointURL 给出这次测速该用的下载地址:内置站点取预设地址,custom 取用户填的。
// 空 / 认不出的站点按默认 Cloudflare 处理(与面板显示一致)。
func (s Settings) TestEndpointURL() string {
	switch s.Endpoint {
	case EndpointApple, EndpointCachefly:
		return endpointURLs[s.Endpoint]
	case EndpointCustom:
		if u := strings.TrimSpace(s.EndpointCustomURL); u != "" {
			return u
		}
		return endpointURLs[EndpointCloudflare]
	}
	return endpointURLs[EndpointCloudflare]
}

// Days 是按天测试时的间隔天数:IntervalHours 折成天、四舍五入、至少 1 天(与上游面板的帮助文字同口径)。
func (s Settings) Days() int {
	d := int(math.Round(float64(s.IntervalHours) / 24))
	if d < 1 {
		d = 1
	}
	return d
}

// NextDue 给出上一次测完的时刻 completed 之后,这个节点下一次该测的时刻。
// 按小时:completed + IntervalHours。按天:completed 所在日期(completed 所在时区的日历日)往后数 Days() 天、当天 StartHour 点整,
// 用日历日期推算,夏令时切换日也还是那天的那个整点。
func (s Settings) NextDue(completed time.Time) time.Time {
	if s.IntervalUnit != UnitDays {
		h := s.IntervalHours
		if h < minIntervalHours {
			h = minIntervalHours
		}
		return completed.Add(time.Duration(h) * time.Hour)
	}
	y, mo, d := completed.Date()
	return time.Date(y, mo, d+s.Days(), s.StartHour, 0, 0, 0, completed.Location())
}

func asInt(v any) (int, error) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("not int")
		}
		return int(n), nil
	case int:
		return n, nil
	case int64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("not int")
	}
}
