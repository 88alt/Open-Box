package regiondetect

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

// LookupURLs 返回按某个 IP 查归属的接口地址(依次尝试)。和 DefaultURLs 是同样三家,
// 都是各家公开的「按 IP 查询」写法:api.ip.sb/geoip/<ip>、ipwho.is/<ip>、api.ipapi.is/?q=<ip>。
func LookupURLs(ip netip.Addr) []string {
	s := ip.String()
	return []string{
		"https://api.ip.sb/geoip/" + s,
		"https://ipwho.is/" + s,
		"https://api.ipapi.is/?q=" + url.QueryEscape(s),
	}
}

// Public 报告这个地址能不能拿去问第三方:只有公网单播地址可以。
// 内网、回环、链路本地、组播、未指定地址问了没有意义,也不该把它们发出去。
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		// sing-box 的 fake-ip 段与 CGNAT 段不是真实的公网地址
		!netip.MustParsePrefix("198.18.0.0/15").Contains(ip) && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

// LookupCountry 按顺序问各家接口,返回这个 IP 的两位大写国家代码。
// 返回的 IP 必须和问的是同一个地址(防止接口答了「你自己的 IP」);不是公网地址直接报错、不发请求。
// 不占用 Detect 的「判断中」状态,可并发调用。
func (d *Detector) LookupCountry(ctx context.Context, ip netip.Addr) (string, error) {
	ip = ip.Unmap()
	if !Public(ip) {
		return "", errors.New("不是公网地址,不查归属")
	}
	urls := LookupURLs(ip)
	if d.LookupURLs != nil {
		urls = d.LookupURLs(ip)
	}
	var fails []string
	for _, u := range urls {
		if ctx.Err() != nil {
			fails = append(fails, "已取消")
			break
		}
		got, cc, err := d.query(ctx, u)
		if err == nil {
			if a, perr := netip.ParseAddr(got); perr != nil || a.Unmap() != ip {
				err = errors.New("答的不是这个 IP")
			}
		}
		if err != nil {
			fails = append(fails, hostOf(u)+": "+err.Error())
			continue
		}
		return cc, nil
	}
	return "", errors.New("查归属失败:" + strings.Join(fails, ";"))
}

// ParseBody 解析三家 IP 接口之一的答话正文,取出 IP 和两位大写国家代码。
// 给「经某条线路看出口 IP」用:正文是 internal/probe 经那条线路取回来的。
func ParseBody(b []byte) (ip, countryCode string, err error) { return parse(b) }
