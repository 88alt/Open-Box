// Package regiondetect 按路由器出口公网 IP 判断它在哪(mainland China 还是境外),
// 给 DNS 上游的默认值选 cn / intl。只做一次只读的查询,不改任何设置。
package regiondetect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultURLs 是依次尝试的 IP 信息接口,和链式代理测速允许的三家一致(internal/api/latency 的白名单)。
// 都只接受 https,返回 JSON,带出口 IP 和国家代码。
var DefaultURLs = []string{
	"https://api.ip.sb/geoip",
	"https://ipwho.is/",
	"https://api.ipapi.is/",
}

const (
	bodyMax    = 16 << 10
	perTimeout = 6 * time.Second
)

// Result 对应前端 OpenboxDnsRegionDetect。Region 为 nil 表示判不出来(序列化成 null)。
type Result struct {
	Region *string `json:"region"`
	IP     string  `json:"ip"`
	Error  string  `json:"error,omitempty"`
}

// Detector 按顺序问各家接口,第一家给出合法 IP 和国家代码的为准。
type Detector struct {
	URLs   []string     // 空 = DefaultURLs
	Client *http.Client // 空 = 默认客户端:不走环境里的代理变量,每家 6 秒超时

	mu       sync.Mutex // 同一时刻只做一次;后来的请求排队,拿到的是自己那次的结果
	inflight atomic.Int32
}

// Pending 在有判断正在进行时为真(给前端轮询用)。
func (d *Detector) Pending() bool { return d.inflight.Load() > 0 }

func (d *Detector) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{
		Timeout:   perTimeout,
		Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: perTimeout, DisableKeepAlives: true},
		// 不跟随跳转:白名单主机不该把我们带去别处
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Detect 判一次。全部失败时 Region 为 nil、Error 按「主机: 原因」逐家列出;ctx 取消则带着取消原因返回。
func (d *Detector) Detect(ctx context.Context) Result {
	d.inflight.Add(1)
	defer d.inflight.Add(-1)
	d.mu.Lock()
	defer d.mu.Unlock()

	urls := d.URLs
	if len(urls) == 0 {
		urls = DefaultURLs
	}
	var fails []string
	for _, u := range urls {
		if ctx.Err() != nil {
			fails = append(fails, "已取消")
			break
		}
		ip, cc, err := d.query(ctx, u)
		if err != nil {
			fails = append(fails, hostOf(u)+": "+err.Error())
			continue
		}
		r := RegionOf(cc)
		return Result{Region: &r, IP: ip}
	}
	return Result{IP: "", Error: "判断地区失败:" + strings.Join(fails, ";")}
}

// RegionOf 把国家代码映射成 cn / intl。只有中国大陆(CN)算 cn;香港、澳门、台湾按境外处理,
// 因为它们的网络环境(能直连的 DNS、不需要国内加速)和境外一致。
func RegionOf(countryCode string) string {
	if strings.EqualFold(strings.TrimSpace(countryCode), "CN") {
		return "cn"
	}
	return "intl"
}

func (d *Detector) query(ctx context.Context, url string) (ip, cc string, err error) {
	ctx, cancel := context.WithTimeout(ctx, perTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "openbox-backend")
	resp, err := d.client().Do(req)
	if err != nil {
		return "", "", errors.New(shortErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, bodyMax))
	if err != nil {
		return "", "", errors.New(shortErr(err))
	}
	return parse(b)
}

// parse 兼容三家的字段:
//   - api.ip.sb:{"ip":"…","country_code":"CN"}
//   - ipwho.is:{"ip":"…","success":true,"country_code":"CN"},失败时 success 为 false
//   - api.ipapi.is:{"ip":"…","location":{"country_code":"CN"}}
func parse(b []byte) (ip, cc string, err error) {
	var v struct {
		IP          string `json:"ip"`
		CountryCode string `json:"country_code"`
		Success     *bool  `json:"success"`
		Message     string `json:"message"`
		Location    struct {
			CountryCode string `json:"country_code"`
		} `json:"location"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", "", errors.New("返回的不是 JSON")
	}
	if v.Success != nil && !*v.Success {
		return "", "", errors.New("接口报告失败")
	}
	a, perr := netip.ParseAddr(strings.TrimSpace(v.IP))
	if perr != nil {
		return "", "", errors.New("没有返回有效的 IP")
	}
	cc = v.CountryCode
	if cc == "" {
		cc = v.Location.CountryCode
	}
	cc = strings.TrimSpace(cc)
	if len(cc) != 2 {
		return "", "", errors.New("没有返回国家代码")
	}
	return a.String(), strings.ToUpper(cc), nil
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "/?"); i >= 0 {
		u = u[:i]
	}
	return u
}

// shortErr 只留原因,不带完整 URL。
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
