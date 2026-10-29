// Package dnsqlog 读 dnsmasq 的查询日志(log-queries),整理成前端「DNS 过滤 → 查询记录 / 统计」要的数据。
// 自己的设计,依据 dnsmasq 手册对 log-queries 输出的描述(query[类型] / forwarded / reply / cached / config 等)和前端
// DnsFilterRecord / DnsFilterSummary 的类型,没有参照上游。
//
// 本包只读:不改 dnsmasq 配置、不碰系统。日志从哪来(打开 log-queries、指定日志去向)是接管那一侧的事,
// 这里只认一个日志文件路径,没有路径或文件不存在时 Connected 为假。
package dnsqlog

import (
	"strings"
)

// Kind 是一行日志的种类。
type Kind int

const (
	KindIgnored   Kind = iota // 不相干的行(启动信息、forwarded、dnssec 等)
	KindQuery                 // query[A] example.com from 192.168.1.5
	KindReply                 // reply example.com is 1.2.3.4(上游应答)
	KindCached                // cached example.com is 1.2.3.4(dnsmasq 缓存应答)
	KindLocal                 // config / hosts 文件 / DHCP 名字(dnsmasq 自己答的本地记录)
	KindForwarded             // forwarded example.com to 127.0.0.1(目前只用来确认没被本地答掉,不单独记)
)

// Line 是解析出的一行。
type Line struct {
	Kind   Kind
	Domain string // 小写,不带末尾的点
	QType  string // 只有 KindQuery 有:A / AAAA / HTTPS ……
	Source string // 只有 KindQuery 有:发起查询的客户端地址
	Answer string // KindReply / KindCached / KindLocal:应答内容原文,例如 1.2.3.4、<CNAME>、NXDOMAIN、NODATA-IPv6
}

// 最长接受的一行(超长的不是 dnsmasq 写的)。
const maxLine = 2048

// ParseLine 解析一行 dnsmasq 日志。不认识的行返回 KindIgnored。
// 兼容几种前缀:syslog 文件("Oct 28 10:00:00 dnsmasq[123]: …")、logread("Wed Oct 28 10:00:00 2026 daemon.info dnsmasq[123]: …")、
// journald("dnsmasq[123]: …"),以及 log-queries=extra 在消息前多出的 "<序号> <客户端>/<端口> "。
func ParseLine(s string) Line {
	if len(s) == 0 || len(s) > maxLine {
		return Line{}
	}
	msg, ok := messageOf(s)
	if !ok {
		return Line{}
	}
	msg = stripExtra(msg)
	f := strings.Fields(msg)
	if len(f) < 2 {
		return Line{}
	}
	switch {
	case strings.HasPrefix(f[0], "query[") && strings.HasSuffix(f[0], "]"):
		// query[A] example.com from 192.168.1.5
		if len(f) != 4 || f[2] != "from" {
			return Line{}
		}
		d, ok := cleanName(f[1])
		if !ok {
			return Line{}
		}
		return Line{Kind: KindQuery, Domain: d, QType: f[0][len("query[") : len(f[0])-1], Source: f[3]}
	case f[0] == "forwarded":
		d, ok := cleanName(f[1])
		if !ok {
			return Line{}
		}
		return Line{Kind: KindForwarded, Domain: d}
	case f[0] == "reply" || f[0] == "cached":
		// reply example.com is 1.2.3.4
		if len(f) != 4 || f[2] != "is" {
			return Line{}
		}
		d, ok := cleanName(f[1])
		if !ok {
			return Line{}
		}
		k := KindReply
		if f[0] == "cached" {
			k = KindCached
		}
		return Line{Kind: k, Domain: d, Answer: f[3]}
	case f[0] == "config" || f[0] == "DHCP" || strings.HasPrefix(f[0], "/"):
		// config x is NXDOMAIN / DHCP x is 192.168.1.9 / /etc/hosts x is 10.0.0.1
		if len(f) != 4 || f[2] != "is" {
			return Line{}
		}
		d, ok := cleanName(f[1])
		if !ok {
			return Line{}
		}
		return Line{Kind: KindLocal, Domain: d, Answer: f[3]}
	}
	return Line{}
}

// messageOf 取出 "dnsmasq[pid]: " 或 "dnsmasq: " 之后的消息部分。
func messageOf(s string) (string, bool) {
	i := strings.Index(s, "dnsmasq")
	if i < 0 {
		return "", false
	}
	rest := s[i+len("dnsmasq"):]
	if strings.HasPrefix(rest, "[") {
		j := strings.Index(rest, "]")
		if j < 0 {
			return "", false
		}
		rest = rest[j+1:]
	}
	if !strings.HasPrefix(rest, ":") {
		return "", false
	}
	return strings.TrimSpace(rest[1:]), true
}

// stripExtra 去掉 log-queries=extra 多出的 "<序号> <客户端>/<端口> " 前缀。
func stripExtra(msg string) string {
	f := strings.SplitN(msg, " ", 3)
	if len(f) < 3 || !allDigits(f[0]) || !strings.Contains(f[1], "/") {
		return msg
	}
	return f[2]
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// cleanName 规整域名:小写、去末尾的点;空串或带控制字符 / 空白的不要。
func cleanName(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if s == "" || len(s) > 253 {
		return "", false
	}
	for _, c := range s {
		if c < 0x21 || c == 0x7f {
			return "", false
		}
	}
	return s, true
}
