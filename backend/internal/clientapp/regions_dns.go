package clientapp

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// checkDNSServerAddr:DoH(https)/ DoT(tls)的地址校验,返回 server / proto / port。
// portSet 表示档案里显式给了端口;没给时 tls 取 853、https 取地址里的端口(没写则 443),
// 不能落到 udp / tcp 的 53 上,否则 core 会给 DoT / DoH 写出 server_port: 53。
func checkDNSServerAddr(server, proto string, port int, portSet bool, label string) (string, string, int, error) {
	switch proto {
	case "https":
		u, err := url.Parse(server)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return "", "", 0, fmt.Errorf("%s: server must be an https:// DoH URL", label)
		}
		if p := u.Port(); p != "" {
			n, perr := strconv.Atoi(p)
			if perr != nil || n < 1 || n > 65535 {
				return "", "", 0, fmt.Errorf("%s: DoH URL port must be 1-65535", label)
			}
			port = n // 和 core 一致:地址里的端口优先
		} else if !portSet {
			port = 443
		}
		return server, proto, port, nil
	default: // tls
		if ip, perr := netip.ParseAddr(server); perr == nil {
			if ip.Zone() != "" {
				return "", "", 0, fmt.Errorf("%s: server must be an IP, domain or wan", label)
			}
		} else if !isDNSDomainName(server) {
			return "", "", 0, fmt.Errorf("%s: server must be an IP, domain or wan", label)
		}
		if !portSet {
			port = 853
		}
		return server, proto, port, nil
	}
}

// isDNSDomainName:和 core 的 isDNSDomainHost 同口径(含点、总长 ≤253、每段 1-63 位、ASCII 字母数字和连字符、首尾不是连字符)。
func isDNSDomainName(host string) bool {
	if host == "" || len(host) > 253 || !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
