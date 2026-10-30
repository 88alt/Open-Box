package routetest

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// 响应里的 resolve:经内核的 DNS 入站(127.0.0.1:7853)向内核真问一次这个域名。
// 内核的 DNS 入站按配置里的 DNS 规则转给对应的上游(或自己发 fake-ip 占位地址),所以这里拿到的就是
// 「此刻线路真的问出来的结果」。自己的设计,依据前端 OpenboxRouteTest.resolve 的类型和注释、
// RFC 1035 / 3596 的报文格式,没有参照上游。报文的构造和解析用标准库自己写(只认 A / AAAA 记录)。
//
// cached / fakeIpFrom 要先知道是哪台 DNS 服务器答的,所以不在这里算,由 annotateResolve(dns.go)按 DNS 判定补上。

// 内核的 fake-ip 地址池,必须与 core 的 routing.FakeIPV4 / FakeIPV6 保持一致。
var fakeIPPools = []netip.Prefix{netip.MustParsePrefix("198.19.0.0/16"), netip.MustParsePrefix("fc00::/18")}

// 别处的 fake-ip 池:代理节点那一侧的 DNS(常见是 mihomo / sing-box 的 fake-ip,默认 198.18.0.0/15)直接回的占位地址。
// 内核自己的池 198.19.0.0/16 在这个段的上半,所以这里只列 198.18.0.0/16,两边不重叠。
var foreignFakePools = []netip.Prefix{netip.MustParsePrefix("198.18.0.0/16")}

const (
	qtypeA     = 1
	qtypeAAAA  = 28
	qtypeCNAME = 5
	maxDNSPkt  = 4096
)

// Resolve 对应前端 OpenboxRouteTest.resolve。
type Resolve struct {
	OK          bool     `json:"ok"`
	Status      *int     `json:"status,omitempty"` // A 查询的 DNS 响应码(0 = 成功,3 = 域名不存在);没拿到响应时没有
	Answers     []string `json:"answers"`          // A 记录
	Answers6    []string `json:"answers6,omitempty"`
	OK6         *bool    `json:"ok6,omitempty"` // 只有查了 AAAA(档案开了 IPv6)才有下面这几项
	Status6     *int     `json:"status6,omitempty"`
	Error6      string   `json:"error6,omitempty"`
	MS          int      `json:"ms"`
	Error       string   `json:"error,omitempty"`
	FakeIP      bool     `json:"fakeIp,omitempty"`
	FakeIPLocal bool     `json:"fakeIpLocal,omitempty"`
	// FakeIPFrom 是代理侧解析时截下查询、回了占位地址的那个节点(detour 此刻落到的节点);直连解析回 fake-ip 时没有。
	FakeIPFrom string `json:"fakeIpFrom,omitempty"`
	// Cached 为真:代理侧解析几毫秒就回来了,多半是内核缓存里的答案,这次没有经线路去问。
	Cached bool `json:"cached,omitempty"`
	TTL    *int `json:"ttl,omitempty"` // A 答案里最小的剩余 TTL(秒)
}

type dnsAnswer struct {
	rcode int
	addrs []netip.Addr
	ttl   int // 没有答案时是 -1
}

// ResolveName 经内核的 DNS 入站解析一个域名,给域名穿透用(penetration.Resolver):档案开了 IPv6 才再查 AAAA。
// 返回答案里有没有占位地址(fake-ip)、所有 A / AAAA 地址;没有任何地址时 errText 是原因(A 的原因优先)。
func (s *Service) ResolveName(ctx context.Context, name string) (fakeIP bool, addrs []string, errText string) {
	r := s.resolve(ctx, name, s.IPv6 != nil && s.IPv6(ctx))
	addrs = append(append([]string{}, r.Answers...), r.Answers6...)
	if len(addrs) == 0 {
		if errText = r.Error; errText == "" {
			errText = r.Error6
		}
	}
	return r.FakeIP, addrs, errText
}

// resolve 查 A,ipv6 为真时再查 AAAA。域名查不到不算接口错误,写在 error / error6 里。
func (s *Service) resolve(ctx context.Context, name string, ipv6 bool) *Resolve {
	r := &Resolve{Answers: []string{}}
	start := time.Now()
	a, err := s.query(ctx, name, qtypeA)
	r.MS = *millis(time.Since(start))
	if err != nil {
		r.Error = describeDNSError(err, s.timeout())
	} else {
		st := a.rcode
		r.Status = &st
		for _, ad := range a.addrs {
			r.Answers = append(r.Answers, ad.String())
			r.markFake(ad)
		}
		if a.ttl >= 0 {
			t := a.ttl
			r.TTL = &t
		}
		switch {
		case a.rcode != 0:
			r.Error = rcodeText(a.rcode)
		case len(a.addrs) == 0:
			r.Error = "没有 A 记录"
		default:
			r.OK = true
		}
	}
	if ipv6 {
		a6, err := s.query(ctx, name, qtypeAAAA)
		ok6 := false
		switch {
		case err != nil:
			r.Error6 = describeDNSError(err, s.timeout())
		default:
			st := a6.rcode
			r.Status6 = &st
			for _, ad := range a6.addrs {
				r.Answers6 = append(r.Answers6, ad.String())
				r.markFake(ad)
			}
			switch {
			case a6.rcode != 0:
				r.Error6 = rcodeText(a6.rcode)
			case len(a6.addrs) == 0:
				r.Error6 = "没有 AAAA 记录"
			default:
				ok6 = true
			}
		}
		r.OK6 = &ok6
	}
	return r
}

// markFake 认出占位地址:内核自己的池 = fakeIp + fakeIpLocal;别处的池 = 只有 fakeIp(不是内核发的)。
func (r *Resolve) markFake(a netip.Addr) {
	switch {
	case inFakePool(a):
		r.FakeIP, r.FakeIPLocal = true, true
	case inPools(foreignFakePools, a):
		r.FakeIP = true
	}
}

func inPools(pools []netip.Prefix, a netip.Addr) bool {
	for _, p := range pools {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

func inFakePool(a netip.Addr) bool {
	for _, p := range fakeIPPools {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

func rcodeText(c int) string {
	switch c {
	case 1:
		return "DNS 请求格式错误(FORMERR)"
	case 2:
		return "DNS 服务器出错(SERVFAIL)"
	case 3:
		return "域名不存在(NXDOMAIN)"
	case 5:
		return "DNS 服务器拒绝了查询(REFUSED)"
	}
	return fmt.Sprintf("DNS 返回了错误码 %d", c)
}

func describeDNSError(err error, timeout time.Duration) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "解析超时(内核的 DNS 入口没有及时答复)"
	case errors.Is(err, context.Canceled):
		return "已取消"
	case strings.Contains(err.Error(), "connection refused"):
		return "连不上内核的 DNS 入口(内核没在运行?)"
	}
	return "解析失败:" + err.Error()
}

func (s *Service) dnsAddr() string {
	if s.DNSAddr != "" {
		return s.DNSAddr
	}
	return system.DefaultKernelDNSAddr
}

// query 向内核 DNS 入站发一次查询;UDP 答复被截断时改用 TCP 重问。
func (s *Service) query(ctx context.Context, name string, qtype uint16) (*dnsAnswer, error) {
	id := make([]byte, 2)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	pkt, err := buildQuery(binary.BigEndian.Uint16(id), name, qtype)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	d := &net.Dialer{}
	c, err := d.DialContext(cctx, "udp", s.dnsAddr())
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if dl, ok := cctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := c.Write(pkt); err != nil {
		return nil, err
	}
	buf := make([]byte, maxDNSPkt)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	ans, truncated, err := parseReply(buf[:n], pkt[:2])
	if err != nil {
		return nil, err
	}
	if !truncated {
		return ans, nil
	}
	tc, err := d.DialContext(cctx, "tcp", s.dnsAddr())
	if err != nil {
		return nil, err
	}
	defer tc.Close()
	if dl, ok := cctx.Deadline(); ok {
		_ = tc.SetDeadline(dl)
	}
	framed := append([]byte{byte(len(pkt) >> 8), byte(len(pkt))}, pkt...)
	if _, err := tc.Write(framed); err != nil {
		return nil, err
	}
	var lenb [2]byte
	if _, err := io.ReadFull(tc, lenb[:]); err != nil {
		return nil, err
	}
	body := make([]byte, int(binary.BigEndian.Uint16(lenb[:])))
	if _, err := io.ReadFull(tc, body); err != nil {
		return nil, err
	}
	ans, _, err = parseReply(body, pkt[:2])
	return ans, err
}

// buildQuery 造一个递归查询(RD 置位,一个问题,类别 IN)。
func buildQuery(id uint16, name string, qtype uint16) ([]byte, error) {
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > 253 {
		return nil, errors.New("域名不合法")
	}
	b := make([]byte, 12, 12+len(name)+6)
	binary.BigEndian.PutUint16(b[0:], id)
	binary.BigEndian.PutUint16(b[2:], 0x0100)
	binary.BigEndian.PutUint16(b[4:], 1)
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return nil, errors.New("域名不合法")
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)
	b = binary.BigEndian.AppendUint16(b, qtype)
	b = binary.BigEndian.AppendUint16(b, 1)
	return b, nil
}

var errBadReply = errors.New("DNS 答复格式不对")

// parseReply 解析答复:核对事务 ID,取响应码、A / AAAA 地址和最小 TTL;返回是否被截断(TC)。
func parseReply(b []byte, id []byte) (*dnsAnswer, bool, error) {
	if len(b) < 12 || b[0] != id[0] || b[1] != id[1] {
		return nil, false, errBadReply
	}
	flags := binary.BigEndian.Uint16(b[2:])
	if flags&0x8000 == 0 {
		return nil, false, errBadReply
	}
	ans := &dnsAnswer{rcode: int(flags & 0xF), ttl: -1}
	truncated := flags&0x0200 != 0
	qd, an := int(binary.BigEndian.Uint16(b[4:])), int(binary.BigEndian.Uint16(b[6:]))
	off := 12
	var err error
	for i := 0; i < qd; i++ {
		if off, err = skipName(b, off); err != nil {
			return nil, false, err
		}
		off += 4
		if off > len(b) {
			return nil, false, errBadReply
		}
	}
	for i := 0; i < an; i++ {
		if off, err = skipName(b, off); err != nil {
			return nil, false, err
		}
		if off+10 > len(b) {
			return nil, false, errBadReply
		}
		typ := binary.BigEndian.Uint16(b[off:])
		ttl := int(binary.BigEndian.Uint32(b[off+4:]))
		rdlen := int(binary.BigEndian.Uint16(b[off+8:]))
		off += 10
		if off+rdlen > len(b) {
			return nil, false, errBadReply
		}
		rd := b[off : off+rdlen]
		off += rdlen
		var ad netip.Addr
		switch {
		case typ == qtypeA && rdlen == 4:
			ad = netip.AddrFrom4([4]byte(rd))
		case typ == qtypeAAAA && rdlen == 16:
			ad = netip.AddrFrom16([16]byte(rd))
		default:
			continue // CNAME 等别的记录不要
		}
		ans.addrs = append(ans.addrs, ad)
		if ans.ttl < 0 || ttl < ans.ttl {
			ans.ttl = ttl
		}
	}
	return ans, truncated, nil
}

// skipName 跳过一个域名(普通标签或 0xC0 压缩指针),返回它后面的偏移。
func skipName(b []byte, off int) (int, error) {
	for hops := 0; hops < 128; hops++ {
		if off >= len(b) {
			return 0, errBadReply
		}
		l := int(b[off])
		switch {
		case l == 0:
			return off + 1, nil
		case l&0xC0 == 0xC0:
			if off+2 > len(b) {
				return 0, errBadReply
			}
			return off + 2, nil
		case l&0xC0 != 0:
			return 0, errBadReply
		}
		off += 1 + l
	}
	return 0, errBadReply
}
