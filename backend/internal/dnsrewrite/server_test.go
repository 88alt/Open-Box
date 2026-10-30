package dnsrewrite

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func query(id uint16, name string, qtype uint16) []byte {
	b := binary.BigEndian.AppendUint16(nil, id)
	b = binary.BigEndian.AppendUint16(b, 0x0100)
	b = append(b, 0, 1, 0, 0, 0, 0, 0, 0)
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	b = append(b, 0)
	b = binary.BigEndian.AppendUint16(b, qtype)
	return binary.BigEndian.AppendUint16(b, 1)
}

type answer struct {
	id    uint16
	flags uint16
	rcode int
	addrs []netip.Addr
	ttls  []uint32
	qd    int
}

func parse(t *testing.T, b []byte) answer {
	t.Helper()
	if len(b) < 12 {
		t.Fatalf("答复太短: %v", b)
	}
	a := answer{id: binary.BigEndian.Uint16(b), flags: binary.BigEndian.Uint16(b[2:])}
	a.rcode = int(a.flags & 0xF)
	a.qd = int(binary.BigEndian.Uint16(b[4:]))
	an := int(binary.BigEndian.Uint16(b[6:]))
	off := 12
	for i := 0; i < a.qd; i++ {
		for b[off] != 0 {
			off += 1 + int(b[off])
		}
		off += 5
	}
	for i := 0; i < an; i++ {
		if b[off] != 0xC0 || b[off+1] != 0x0C {
			t.Fatalf("答案名字应是指向问题的指针: %v", b[off:off+2])
		}
		typ := binary.BigEndian.Uint16(b[off+2:])
		a.ttls = append(a.ttls, binary.BigEndian.Uint32(b[off+6:]))
		rdlen := int(binary.BigEndian.Uint16(b[off+10:]))
		rd := b[off+12 : off+12+rdlen]
		off += 12 + rdlen
		switch {
		case typ == 1 && rdlen == 4:
			a.addrs = append(a.addrs, netip.AddrFrom4([4]byte(rd)))
		case typ == 28 && rdlen == 16:
			a.addrs = append(a.addrs, netip.AddrFrom16([16]byte(rd)))
		default:
			t.Fatalf("类型 %d 长度 %d 不对", typ, rdlen)
		}
	}
	if off != len(b) {
		t.Fatalf("报文尾部有多余字节: %d/%d", off, len(b))
	}
	return a
}

func newServer(rules []Rule) *Server {
	return &Server{
		Rules: func(context.Context) ([]Rule, error) { return rules, nil },
		Resolve: func(_ context.Context, host string, v6 bool) ([]netip.Addr, error) {
			switch {
			case host == "target.example" && !v6:
				return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
			case host == "target.example" && v6:
				return []netip.Addr{netip.MustParseAddr("2606:2800::1")}, nil
			}
			return nil, errors.New("解析不了")
		},
	}
}

var ctx = context.Background()

func TestFixedAddresses(t *testing.T) {
	s := newServer([]Rule{{Source: "*.lan.test", Addresses: []string{"10.0.0.2", "10.0.0.3", "fd00::2"}}})
	a := parse(t, s.Handle(ctx, query(7, "NAS.lan.test", qtypeA)))
	if a.id != 7 || a.rcode != 0 || a.flags&0x8000 == 0 || a.flags&0x0100 == 0 || a.flags&0x0080 == 0 || a.qd != 1 ||
		len(a.addrs) != 2 || a.addrs[0].String() != "10.0.0.2" || a.ttls[0] != 60 {
		t.Fatalf("%+v", a)
	}
	a = parse(t, s.Handle(ctx, query(8, "nas.lan.test", qtypeAAAA)))
	if a.rcode != 0 || len(a.addrs) != 1 || a.addrs[0].String() != "fd00::2" {
		t.Fatalf("%+v", a)
	}
	// 只有 IPv4 的规则被问 AAAA:确定的「没有这条记录」(NOERROR + 空答案),不是错误
	s = newServer([]Rule{{Source: "v4.test", Addresses: []string{"1.2.3.4"}}})
	if a = parse(t, s.Handle(ctx, query(9, "v4.test", qtypeAAAA))); a.rcode != 0 || len(a.addrs) != 0 {
		t.Fatalf("%+v", a)
	}
}

func TestDomainTarget(t *testing.T) {
	s := newServer([]Rule{{Source: "services.googleapis.cn", Domain: "target.example"}})
	a := parse(t, s.Handle(ctx, query(1, "services.googleapis.cn", qtypeA)))
	if a.rcode != 0 || len(a.addrs) != 1 || a.addrs[0].String() != "93.184.216.34" {
		t.Fatalf("%+v", a)
	}
	if a = parse(t, s.Handle(ctx, query(2, "services.googleapis.cn", qtypeAAAA))); len(a.addrs) != 1 || !a.addrs[0].Is6() {
		t.Fatalf("%+v", a)
	}
	// 目标解析失败 = SERVFAIL;没接解析 = SERVFAIL
	s = newServer([]Rule{{Source: "a.test", Domain: "bad.example"}})
	if a = parse(t, s.Handle(ctx, query(3, "a.test", qtypeA))); a.rcode != rcodeServFail {
		t.Fatalf("%+v", a)
	}
	s.Resolve = nil
	if a = parse(t, s.Handle(ctx, query(3, "a.test", qtypeA))); a.rcode != rcodeServFail {
		t.Fatalf("%+v", a)
	}
}

func TestTargetThatIsItselfASourceIsNotFollowed(t *testing.T) {
	var called atomic.Int32
	s := newServer([]Rule{{Source: "a.test", Domain: "b.test"}, {Source: "b.test", Domain: "a.test"}})
	s.Resolve = func(context.Context, string, bool) ([]netip.Addr, error) { called.Add(1); return nil, nil }
	if a := parse(t, s.Handle(ctx, query(1, "a.test", qtypeA))); a.rcode != rcodeServFail || called.Load() != 0 {
		t.Fatalf("环要断开,且不去解析: %+v %d", a, called.Load())
	}
}

func TestOtherTypesAndNoMatch(t *testing.T) {
	s := newServer([]Rule{{Source: "a.test", Addresses: []string{"1.1.1.1"}}})
	for _, qt := range []uint16{16 /*TXT*/, 65 /*HTTPS*/, 15 /*MX*/} {
		if a := parse(t, s.Handle(ctx, query(1, "a.test", qt))); a.rcode != 0 || len(a.addrs) != 0 {
			t.Errorf("类型 %d 应是 NOERROR 空答案: %+v", qt, a)
		}
	}
	if a := parse(t, s.Handle(ctx, query(1, "zzz.test", qtypeA))); a.rcode != rcodeRefused {
		t.Fatalf("没有对应规则应 REFUSED: %+v", a)
	}
	s = &Server{Rules: func(context.Context) ([]Rule, error) { return nil, errors.New("读不到档案") }}
	if a := parse(t, s.Handle(ctx, query(1, "a.test", qtypeA))); a.rcode != rcodeServFail {
		t.Fatalf("读不到规则应 SERVFAIL: %+v", a)
	}
}

func TestMalformedRequests(t *testing.T) {
	s := newServer(nil)
	if s.Handle(ctx, []byte{1, 2, 3}) != nil {
		t.Error("太短的不回")
	}
	resp := query(1, "a.test", 1)
	resp[2] |= 0x80
	if s.Handle(ctx, resp) != nil {
		t.Error("答复报文不回(免得两台 DNS 互相回)")
	}
	q := query(1, "a.test", 1)
	q[2] |= 0x28 // opcode = 5
	if a := parse(t, s.Handle(ctx, q)); a.rcode != rcodeNotImp {
		t.Errorf("%+v", a)
	}
	q = query(1, "a.test", 1)
	q[5] = 0 // QDCOUNT = 0
	if a := parse(t, s.Handle(ctx, q)); a.rcode != rcodeFormErr {
		t.Errorf("%+v", a)
	}
	q = query(1, "a.test", 1)
	q[12] = 0xC0 // 问题里的压缩指针
	if a := parse(t, s.Handle(ctx, q)); a.rcode != rcodeFormErr {
		t.Errorf("%+v", a)
	}
	if a := parse(t, s.Handle(ctx, query(1, "a.test", 1)[:16])); a.rcode != rcodeFormErr {
		t.Errorf("截断的问题: %+v", a)
	}
	q = query(1, "a.test", 1)
	binary.BigEndian.PutUint16(q[len(q)-2:], 3) // CHAOS 类
	if a := parse(t, s.Handle(ctx, q)); a.rcode != rcodeRefused {
		t.Errorf("%+v", a)
	}
}

func TestAnswersAreCapped(t *testing.T) {
	var addrs []string
	for i := 1; i <= 40; i++ {
		addrs = append(addrs, netip.AddrFrom4([4]byte{10, 0, 0, byte(i)}).String())
	}
	s := newServer([]Rule{{Source: "many.test", Addresses: addrs}})
	resp := s.Handle(ctx, query(1, "many.test", qtypeA))
	if a := parse(t, resp); len(a.addrs) != maxAnswers || len(resp) > 512 {
		t.Fatalf("%d 条,%d 字节", len(a.addrs), len(resp))
	}
}

func TestRulesAreCachedBriefly(t *testing.T) {
	var n atomic.Int32
	now := time.Unix(1000, 0)
	s := &Server{Rules: func(context.Context) ([]Rule, error) {
		n.Add(1)
		return []Rule{{Source: "a.test", Addresses: []string{"1.1.1.1"}}}, nil
	}, now: func() time.Time { return now }}
	for i := 0; i < 5; i++ {
		s.Handle(ctx, query(1, "a.test", qtypeA))
	}
	if n.Load() != 1 {
		t.Fatalf("短时间内只读一次规则: %d", n.Load())
	}
	now = now.Add(rulesTTL + time.Millisecond)
	s.Handle(ctx, query(1, "a.test", qtypeA))
	if n.Load() != 2 {
		t.Fatalf("过期后重读: %d", n.Load())
	}
	// 读失败不缓存
	s2 := &Server{Rules: func(context.Context) ([]Rule, error) { n.Add(1); return nil, errors.New("x") }, now: func() time.Time { return now }}
	n.Store(0)
	s2.Handle(ctx, query(1, "a.test", qtypeA))
	s2.Handle(ctx, query(1, "a.test", qtypeA))
	if n.Load() != 2 {
		t.Fatalf("失败不该缓存: %d", n.Load())
	}
}

func TestUDPRoundTripAndShutdown(t *testing.T) {
	s := newServer([]Rule{{Source: "a.test", Addresses: []string{"1.2.3.4"}}})
	s.Addr = "127.0.0.1:0"
	conn, err := s.Listen()
	if err != nil {
		t.Skip("监听不了本机 UDP:", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.Serve(cctx, conn); close(done) }()

	c, err := net.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(query(0x1234, "a.test", qtypeA)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 600)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if a := parse(t, buf[:n]); a.id != 0x1234 || len(a.addrs) != 1 || a.addrs[0].String() != "1.2.3.4" {
		t.Fatalf("%+v", a)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 结束后 Serve 没有退出")
	}
}

func TestRunReportsListenError(t *testing.T) {
	a, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer a.Close()
	s := &Server{Addr: a.LocalAddr().String()}
	if err := s.Run(ctx); err == nil {
		t.Fatal("端口被占用应返回 error")
	}
}
