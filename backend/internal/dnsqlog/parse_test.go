package dnsqlog

import "testing"

func TestParseLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Line
	}{
		{"syslog 文件的 query", "Oct 28 10:00:00 dnsmasq[123]: query[A] Example.COM. from 192.168.1.5", Line{Kind: KindQuery, Domain: "example.com", QType: "A", Source: "192.168.1.5"}},
		{"logread 的前缀", "Wed Oct 28 10:00:00 2026 daemon.info dnsmasq[1234]: query[AAAA] a.b.example from fe80::1", Line{Kind: KindQuery, Domain: "a.b.example", QType: "AAAA", Source: "fe80::1"}},
		{"journald 没有时间", "dnsmasq[9]: query[HTTPS] x.org from 127.0.0.1", Line{Kind: KindQuery, Domain: "x.org", QType: "HTTPS", Source: "127.0.0.1"}},
		{"extra 的序号前缀", "Oct 28 10:00:00 dnsmasq[1]: 4711 192.168.1.5/54321 query[A] x.org from 192.168.1.5", Line{Kind: KindQuery, Domain: "x.org", QType: "A", Source: "192.168.1.5"}},
		{"上游应答", "Oct 28 10:00:00 dnsmasq[1]: reply x.org is 1.2.3.4", Line{Kind: KindReply, Domain: "x.org", Answer: "1.2.3.4"}},
		{"NXDOMAIN", "Oct 28 10:00:00 dnsmasq[1]: reply ads.x.org is NXDOMAIN", Line{Kind: KindReply, Domain: "ads.x.org", Answer: "NXDOMAIN"}},
		{"缓存", "Oct 28 10:00:00 dnsmasq[1]: cached x.org is <CNAME>", Line{Kind: KindCached, Domain: "x.org", Answer: "<CNAME>"}},
		{"config", "Oct 28 10:00:00 dnsmasq[1]: config router.lan is 192.168.1.1", Line{Kind: KindLocal, Domain: "router.lan", Answer: "192.168.1.1"}},
		{"DHCP 名字", "Oct 28 10:00:00 dnsmasq[1]: DHCP phone.lan is 192.168.1.9", Line{Kind: KindLocal, Domain: "phone.lan", Answer: "192.168.1.9"}},
		{"hosts 文件", "Oct 28 10:00:00 dnsmasq[1]: /tmp/hosts/dhcp nas.lan is 192.168.1.2", Line{Kind: KindLocal, Domain: "nas.lan", Answer: "192.168.1.2"}},
		{"forwarded", "Oct 28 10:00:00 dnsmasq[1]: forwarded x.org to 127.0.0.1", Line{Kind: KindForwarded, Domain: "x.org"}},
		{"别的进程", "Oct 28 10:00:00 sing-box[1]: query[A] x.org from 1.1.1.1", Line{}},
		{"启动信息", "Oct 28 10:00:00 dnsmasq[1]: started, version 2.90 cachesize 150", Line{}},
		{"query 缺 from", "Oct 28 10:00:00 dnsmasq[1]: query[A] x.org", Line{}},
		{"域名带空白控制字符", "Oct 28 10:00:00 dnsmasq[1]: reply x\x01.org is 1.2.3.4", Line{}},
		{"空行", "", Line{}},
	}
	for _, c := range cases {
		if got := ParseLine(c.in); got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
	long := "dnsmasq[1]: query[A] " + string(make([]byte, 3000)) + " from 1.1.1.1"
	if got := ParseLine(long); got.Kind != KindIgnored {
		t.Errorf("超长的行应忽略: %+v", got)
	}
}
