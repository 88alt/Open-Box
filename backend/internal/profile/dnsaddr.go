package profile

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// validateDNSAddr:按协议校验 DNS 上游地址(已去空白、非空、非 wan)。口径和 core 的 parseDoHURL / isDNSDomainHost 保持一致,
// 免得后端放行、core 生成时又静默退回「上游 DNS」。
func validateDNSAddr(s, proto, label string) error {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "https":
		u, err := url.Parse(s)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("%s 在 DoH(https) 下必须是 https:// 地址", label)
		}
		if p := u.Port(); p != "" {
			if n, perr := strconv.Atoi(p); perr != nil || n < 1 || n > 65535 {
				return fmt.Errorf("%s 在 DoH(https) 下的端口必须是 1-65535", label)
			}
		}
		return nil
	case "tls":
		if _, err := netip.ParseAddr(s); err == nil {
			return nil
		}
		if !validDNSDomainHost(s) {
			return fmt.Errorf("%s 在 DoT(tls) 下只能填 IP、域名或 wan", label)
		}
		return nil
	default: // udp / tcp
		if _, err := netip.ParseAddr(s); err != nil {
			return fmt.Errorf("%s 只能填 IP 地址或 wan(路由器系统的上游 DNS)", label)
		}
		return nil
	}
}

// validDNSDomainHost:域名主机(含点、总长 ≤253、每段 1-63 位、只含 ASCII 字母数字和连字符、首尾不是连字符)。
func validDNSDomainHost(host string) bool {
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
