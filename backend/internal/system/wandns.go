package system

// 「上游 DNS」:系统此刻真正用来解析域名的那几台服务器(接口上 DHCP / PPP 分配的,或手动指定的)。
// 只读;不碰 dnsmasq、resolv.conf 或任何设置。
//
// 为什么不直接读 /etc/resolv.conf:OpenWrt 上它指向本机 dnsmasq(127.0.0.1),systemd-resolved 上指向
// 127.0.0.53,后端接管 DNS 以后也会指向本机,读出来都是回环地址,不是上游。所以按下面的顺序找「没被本机顶替」的那一份。

// WANResolverFiles 是按优先级排的候选文件,第一个读得出非回环地址的就用它:
//   - /tmp/resolv.conf.d/resolv.conf.auto:OpenWrt 的 netifd 写的,各接口拿到的上游 DNS(dnsmasq 的 resolv-file 就指向它);
//   - /tmp/resolv.conf.auto:旧版 OpenWrt 的同一份;
//   - /run/systemd/resolve/resolv.conf:systemd-resolved 记录的真实上游(/etc/resolv.conf 只指向 127.0.0.53);
//   - /etc/resolv.conf:没有上面这些的普通 Debian。
//
// 这些位置依据 OpenWrt 和 systemd 的公开文档,没有在真机上见过,需真机确认(见 STATUS.md)。
var WANResolverFiles = []string{
	"/tmp/resolv.conf.d/resolv.conf.auto",
	"/tmp/resolv.conf.auto",
	"/run/systemd/resolve/resolv.conf",
	"/etc/resolv.conf",
}

// ReadWANDNS 依次读 files,返回第一份读得出非回环地址的内容(去重、保序,规则同 ReadResolvers);
// 都读不出来返回空切片(不是 nil,方便直接序列化成 [])。
func ReadWANDNS(files []string) []string {
	for _, f := range files {
		if s := ReadResolvers(f); len(s) > 0 {
			return s
		}
	}
	return []string{}
}
