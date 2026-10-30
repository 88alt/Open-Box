package routetest

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// closedAddr 返回一个没人听的本机 UDP 地址。
func closedAddr(t *testing.T) string {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := c.LocalAddr().String()
	c.Close()
	return a
}

// rr 是答复里的一条记录。
type rr struct {
	typ  uint16
	ttl  uint32
	data []byte
}

func a4(ip string, ttl uint32) rr {
	a := netip.MustParseAddr(ip).As4()
	return rr{qtypeA, ttl, a[:]}
}
func a6(ip string, ttl uint32) rr {
	a := netip.MustParseAddr(ip).As16()
	return rr{qtypeAAAA, ttl, a[:]}
}

// reply 按请求造一个答复:id 照抄、问题段照抄、答案用压缩指针指回问题里的名字。
func reply(req []byte, rcode int, tc bool, rrs ...rr) []byte {
	// 问题段 = 12 字节头之后到 0 结尾标签 + 4 字节
	end := 12
	for req[end] != 0 {
		end += 1 + int(req[end])
	}
	end += 5
	b := append([]byte(nil), req[:end]...)
	flags := uint16(0x8180) | uint16(rcode)
	if tc {
		flags |= 0x0200
	}
	binary.BigEndian.PutUint16(b[2:], flags)
	binary.BigEndian.PutUint16(b[6:], uint16(len(rrs)))
	for _, r := range rrs {
		b = append(b, 0xC0, 12)
		b = binary.BigEndian.AppendUint16(b, r.typ)
		b = binary.BigEndian.AppendUint16(b, 1)
		b = binary.BigEndian.AppendUint32(b, r.ttl)
		b = binary.BigEndian.AppendUint16(b, uint16(len(r.data)))
		b = append(b, r.data...)
	}
	return b
}

// fakeDNS 是假的内核 DNS 入站:按请求的查询类型调 handler 取答复;handler 返回 nil 表示不应答。
type fakeDNS struct{ addr string }

func qtypeOf(req []byte) uint16 {
	end := 12
	for req[end] != 0 {
		end += 1 + int(req[end])
	}
	return binary.BigEndian.Uint16(req[end+1:])
}

// newFakeDNS 同一个端口上起 UDP,tcp 为真时再起 TCP(UDP 答复带 TC 时客户端会改用 TCP)。
func newFakeDNS(t *testing.T, tcp bool, h func(req []byte, viaTCP bool) []byte) *fakeDNS {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	f := &fakeDNS{addr: pc.LocalAddr().String()}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if out := h(append([]byte(nil), buf[:n]...), false); out != nil {
				_, _ = pc.WriteTo(out, from)
			}
		}
	}()
	if tcp {
		ln, err := net.Listen("tcp", f.addr)
		if err != nil {
			t.Skip("同一个端口上起不了 TCP:", err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					var l [2]byte
					if _, err := readFull(c, l[:]); err != nil {
						return
					}
					req := make([]byte, binary.BigEndian.Uint16(l[:]))
					if _, err := readFull(c, req); err != nil {
						return
					}
					out := h(req, true)
					_, _ = c.Write(append([]byte{byte(len(out) >> 8), byte(len(out))}, out...))
				}()
			}
		}()
	}
	return f
}

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func TestResolveAnswersTTLAndStatus(t *testing.T) {
	f := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		return reply(req, 0, false, a4("93.184.216.34", 300), a4("93.184.216.35", 120))
	})
	s := &Service{DNSAddr: f.addr, Timeout: 2 * time.Second}
	r := s.resolve(context.Background(), "example.com", false)
	if !r.OK || r.Error != "" || strings.Join(r.Answers, ",") != "93.184.216.34,93.184.216.35" {
		t.Fatalf("%+v", r)
	}
	if r.Status == nil || *r.Status != 0 || r.TTL == nil || *r.TTL != 120 || r.MS < 1 {
		t.Fatalf("状态 / TTL / 耗时不对:%+v", r)
	}
	if r.FakeIP || r.FakeIPLocal || r.OK6 != nil || r.Answers6 != nil {
		t.Fatalf("不该有 fake-ip 和 AAAA:%+v", r)
	}
}

func TestResolveIPv6AndFakeIP(t *testing.T) {
	f := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		if qtypeOf(req) == qtypeAAAA {
			return reply(req, 0, false, a6("fc00::1234", 1))
		}
		return reply(req, 0, false, a4("198.19.0.7", 1))
	})
	s := &Service{DNSAddr: f.addr, Timeout: 2 * time.Second}
	r := s.resolve(context.Background(), "example.com", true)
	if !r.OK || r.OK6 == nil || !*r.OK6 || len(r.Answers6) != 1 || r.Answers6[0] != "fc00::1234" {
		t.Fatalf("%+v", r)
	}
	if !r.FakeIP || !r.FakeIPLocal {
		t.Fatalf("答案在 fake-ip 池里,要标出来:%+v", r)
	}
	// 只有 AAAA 在池里也算
	f2 := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		if qtypeOf(req) == qtypeAAAA {
			return reply(req, 0, false, a6("fc00::1", 1))
		}
		return reply(req, 0, false, a4("1.2.3.4", 1))
	})
	r = (&Service{DNSAddr: f2.addr, Timeout: 2 * time.Second}).resolve(context.Background(), "example.com", true)
	if !r.FakeIP {
		t.Fatalf("%+v", r)
	}
}

func TestResolveFailures(t *testing.T) {
	cases := []struct {
		name  string
		h     func(req []byte, _ bool) []byte
		want  string
		wantS int
	}{
		{"NXDOMAIN", func(req []byte, _ bool) []byte { return reply(req, 3, false) }, "域名不存在(NXDOMAIN)", 3},
		{"SERVFAIL", func(req []byte, _ bool) []byte { return reply(req, 2, false) }, "SERVFAIL", 2},
		{"REFUSED", func(req []byte, _ bool) []byte { return reply(req, 5, false) }, "REFUSED", 5},
		{"没有 A 记录", func(req []byte, _ bool) []byte { return reply(req, 0, false) }, "没有 A 记录", 0},
		{"只有 CNAME", func(req []byte, _ bool) []byte { return reply(req, 0, false, rr{qtypeCNAME, 5, []byte{0}}) }, "没有 A 记录", 0},
	}
	for _, c := range cases {
		f := newFakeDNS(t, false, c.h)
		r := (&Service{DNSAddr: f.addr, Timeout: 2 * time.Second}).resolve(context.Background(), "x.example.com", false)
		if r.OK || !strings.Contains(r.Error, c.want) || r.Status == nil || *r.Status != c.wantS || r.Answers == nil || len(r.Answers) != 0 {
			t.Errorf("%s:%+v", c.name, r)
		}
		if r.TTL != nil {
			t.Errorf("%s:没有答案不该有 TTL", c.name)
		}
	}

	// 超时
	silent := newFakeDNS(t, false, func([]byte, bool) []byte { return nil })
	r := (&Service{DNSAddr: silent.addr, Timeout: 200 * time.Millisecond}).resolve(context.Background(), "example.com", false)
	if r.OK || !strings.Contains(r.Error, "超时") || r.Status != nil {
		t.Errorf("%+v", r)
	}
	// 入口没在听
	r = (&Service{DNSAddr: closedAddr(t), Timeout: time.Second}).resolve(context.Background(), "example.com", false)
	if r.OK || !strings.Contains(r.Error, "内核的 DNS 入口") {
		t.Errorf("%+v", r)
	}
	// 事务 ID 不对的答复不能收
	bad := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		out := reply(req, 0, false, a4("1.2.3.4", 1))
		out[0] ^= 0xFF
		return out
	})
	r = (&Service{DNSAddr: bad.addr, Timeout: time.Second}).resolve(context.Background(), "example.com", false)
	if r.OK || !strings.Contains(r.Error, "解析失败") {
		t.Errorf("%+v", r)
	}
	// AAAA 失败不影响 A 的结果
	half := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		if qtypeOf(req) == qtypeAAAA {
			return reply(req, 2, false)
		}
		return reply(req, 0, false, a4("1.2.3.4", 9))
	})
	r = (&Service{DNSAddr: half.addr, Timeout: time.Second}).resolve(context.Background(), "example.com", true)
	if !r.OK || r.OK6 == nil || *r.OK6 || r.Status6 == nil || *r.Status6 != 2 || !strings.Contains(r.Error6, "SERVFAIL") {
		t.Errorf("%+v", r)
	}
}

func TestResolveFallsBackToTCPWhenTruncated(t *testing.T) {
	f := newFakeDNS(t, true, func(req []byte, viaTCP bool) []byte {
		if !viaTCP {
			return reply(req, 0, true) // 被截断,没有答案
		}
		return reply(req, 0, false, a4("8.8.8.8", 77))
	})
	r := (&Service{DNSAddr: f.addr, Timeout: 2 * time.Second}).resolve(context.Background(), "big.example.com", false)
	if !r.OK || len(r.Answers) != 1 || r.Answers[0] != "8.8.8.8" || r.TTL == nil || *r.TTL != 77 {
		t.Fatalf("%+v", r)
	}
}

func TestResolveJSONShape(t *testing.T) {
	f := newFakeDNS(t, false, func(req []byte, _ bool) []byte { return reply(req, 3, false) })
	r := (&Service{DNSAddr: f.addr, Timeout: time.Second}).resolve(context.Background(), "nx.example.com", false)
	b, _ := json.Marshal(r)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if a, ok := m["answers"].([]any); !ok || len(a) != 0 {
		t.Errorf("answers 要是空数组,不能是 null:%s", b)
	}
	for _, k := range []string{"ok", "ms", "status", "error"} {
		if _, ok := m[k]; !ok {
			t.Errorf("缺字段 %s:%s", k, b)
		}
	}
	for _, k := range []string{"answers6", "ok6", "ttl", "fakeIp"} {
		if _, ok := m[k]; ok {
			t.Errorf("不该有字段 %s:%s", k, b)
		}
	}
}

func TestParseReplyRejectsGarbage(t *testing.T) {
	q, _ := buildQuery(0x1234, "a.example.com", qtypeA)
	good := reply(q, 0, false, a4("1.2.3.4", 5))
	if _, _, err := parseReply(good, q[:2]); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{
		"空":       nil,
		"太短":      good[:8],
		"答案被截":    good[:len(good)-3],
		"不是答复":    func() []byte { x := append([]byte(nil), good...); x[2] &^= 0x80; return x }(),
		"问题段被截":   good[:20],
		"压缩指针出界":  append(append([]byte(nil), good[:len(good)-16]...), 0xC0),
		"无效的标签长度": append(append([]byte(nil), good[:12]...), 0x80, 0, 0, 0),
	} {
		if _, _, err := parseReply(b, q[:2]); err == nil {
			t.Errorf("%s:应该报错", name)
		}
	}
	// 事务 ID 不符
	if _, _, err := parseReply(good, []byte{0, 0}); err == nil {
		t.Error("事务 ID 不符应该报错")
	}
}

func TestBuildQuery(t *testing.T) {
	b, err := buildQuery(7, "www.Example.com.", qtypeAAAA)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(b) != 7 || b[2] != 0x01 || b[5] != 1 {
		t.Fatalf("头不对:% x", b[:12])
	}
	want := "\x03www\x07Example\x03com\x00"
	if string(b[12:12+len(want)]) != want || binary.BigEndian.Uint16(b[12+len(want):]) != qtypeAAAA {
		t.Fatalf("问题段不对:%q", b[12:])
	}
	for _, bad := range []string{"", ".", "a..b", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 130) + "com"} {
		if _, err := buildQuery(1, bad, qtypeA); err == nil {
			t.Errorf("%q 应该被拒绝", bad)
		}
	}
}

// 整条:域名目标会先经 DNS 入站真问,结果放进响应的 resolve;IP 目标不问;档案开了 IPv6 才查 AAAA。
func TestRunIncludesResolveForDomains(t *testing.T) {
	queries := make(chan uint16, 8)
	f := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		queries <- qtypeOf(req)
		if qtypeOf(req) == qtypeAAAA {
			return reply(req, 0, false, a6("2001:db8::1", 60))
		}
		return reply(req, 0, false, a4("93.184.216.34", 60))
	})
	p := newFakeProxy(t, 403, "")
	ipv6 := false
	s := &Service{ProxyAddr: p.addr(), DNSAddr: f.addr, Timeout: 2 * time.Second, IPv6: func(context.Context) bool { return ipv6 }}

	res, err := s.Run(context.Background(), Input{Target: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolve == nil || !res.Resolve.OK || res.Resolve.OK6 != nil || len(queries) != 1 {
		t.Fatalf("%+v queries=%d", res.Resolve, len(queries))
	}
	<-queries
	ipv6 = true
	res, _ = s.Run(context.Background(), Input{Target: "example.com"})
	if res.Resolve == nil || res.Resolve.OK6 == nil || !*res.Resolve.OK6 || len(queries) != 2 {
		t.Fatalf("%+v queries=%d", res.Resolve, len(queries))
	}
	for len(queries) > 0 {
		<-queries
	}
	res, _ = s.Run(context.Background(), Input{Target: "93.184.216.34"})
	if res.Resolve != nil || len(queries) != 0 {
		t.Fatalf("IP 目标不该解析:%+v", res.Resolve)
	}
}

func TestResolveMarksForeignFakeIP(t *testing.T) {
	// 198.18.0.0/16 是别处(节点那一侧)的 fake-ip:只标 fakeIp,不是内核自己发的;内核自己的池 198.19.0.0/16 两项都标
	for _, c := range []struct {
		ip          string
		fake, local bool
	}{
		{"198.18.0.9", true, false},
		{"198.19.0.9", true, true},
		{"198.17.255.255", false, false},
		{"198.20.0.1", false, false},
		{"93.184.216.34", false, false},
	} {
		f := newFakeDNS(t, false, func(req []byte, _ bool) []byte { return reply(req, 0, false, a4(c.ip, 30)) })
		r := (&Service{DNSAddr: f.addr, Timeout: 2 * time.Second}).resolve(context.Background(), "example.com", false)
		if r.FakeIP != c.fake || r.FakeIPLocal != c.local {
			t.Errorf("%s: fakeIp=%v local=%v,想要 %v %v", c.ip, r.FakeIP, r.FakeIPLocal, c.fake, c.local)
		}
	}
}

func TestResolveNameForPenetration(t *testing.T) {
	// A 和 AAAA 都有:合并,档案开了 IPv6 才问 AAAA
	f := newFakeDNS(t, false, func(req []byte, _ bool) []byte {
		if qtypeOf(req) == qtypeAAAA {
			return reply(req, 0, false, a6("2001:db8::5", 60))
		}
		return reply(req, 0, false, a4("93.184.216.34", 60))
	})
	s := &Service{DNSAddr: f.addr, Timeout: 2 * time.Second, IPv6: func(context.Context) bool { return true }}
	fake, addrs, errText := s.ResolveName(context.Background(), "example.com")
	if fake || errText != "" || strings.Join(addrs, ",") != "93.184.216.34,2001:db8::5" {
		t.Fatalf("%v %v %q", fake, addrs, errText)
	}
	s.IPv6 = nil
	if _, addrs, _ := s.ResolveName(context.Background(), "example.com"); strings.Join(addrs, ",") != "93.184.216.34" {
		t.Fatalf("没开 IPv6 不该有 AAAA 地址: %v", addrs)
	}

	// 占位地址要标出来
	f = newFakeDNS(t, false, func(req []byte, _ bool) []byte { return reply(req, 0, false, a4("198.19.0.7", 1)) })
	if fake, addrs, _ := (&Service{DNSAddr: f.addr, Timeout: 2 * time.Second}).ResolveName(context.Background(), "example.com"); !fake || len(addrs) != 1 {
		t.Fatalf("%v %v", fake, addrs)
	}

	// 解析失败:没有地址,原因写进 errText;addrs 不是 nil 也不含脏数据
	f = newFakeDNS(t, false, func(req []byte, _ bool) []byte { return reply(req, 3, false) })
	fake, addrs, errText = (&Service{DNSAddr: f.addr, Timeout: 2 * time.Second}).ResolveName(context.Background(), "nx.example.com")
	if fake || len(addrs) != 0 || errText == "" {
		t.Fatalf("%v %v %q", fake, addrs, errText)
	}
}
