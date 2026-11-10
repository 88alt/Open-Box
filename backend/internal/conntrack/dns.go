package conntrack

import (
	"net"
	"strconv"
)

// DNSEvidence 对应前端 terminal-test 的 dnsEvidence:虚拟终端发往 DNS 服务器的那几次查询,在 conntrack 里
// 回复方是不是服务器本人。被内核或系统劫持 / 改写时,回复方的地址或端口与原方向的目的不一致。
type DNSEvidence struct {
	Flows      int    // 对得上的记录条数(0 = 连接跟踪表里没有这次查询)
	Hijacked   bool   // 回复方不是被问的那台服务器(地址或端口被改写)
	AnsweredBy string // 实际回复的一方:被改写时是回复方的「地址:端口」,否则是被问的服务器地址
	Line       string // 第一条对得上的记录原文(没有为空)
}

// AnalyzeDNS 从 UDP 记录里挑出终端自己发的查询:原方向源端口在 sports 里、目的是 server:port。
// entries 通常是 Reader.Find(Query{Proto: "udp", Src: 终端 IP, Dst: server, Dport: port}) 的结果。
// 靠源端口精确认出「这一次」的查询,不会把之前几次测试留在表里的旧记录算进来。
func AnalyzeDNS(entries []Entry, server string, port int, sports []int) DNSEvidence {
	want := map[int]bool{}
	for _, p := range sports {
		want[p] = true
	}
	ev := DNSEvidence{AnsweredBy: server}
	first := true
	for i := range entries {
		e := &entries[i]
		if e.Proto != "udp" || e.Dport != port || !want[e.Sport] || !sameIP(e.Dst, server) {
			continue
		}
		ev.Flows++
		if !first {
			continue
		}
		first = false
		ev.Line = e.Line
		if e.Unreplied {
			continue // 还没回复,看不出谁答的
		}
		if !sameIP(e.RSrc, e.Dst) || e.RSport != e.Dport {
			ev.Hijacked = true
			ev.AnsweredBy = e.RSrc
			if e.RSport != e.Dport {
				ev.AnsweredBy = net.JoinHostPort(e.RSrc, strconv.Itoa(e.RSport))
			}
		}
	}
	return ev
}
