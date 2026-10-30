package dnsrewrite

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// DNS 报文常量。
const (
	qtypeA    = 1
	qtypeAAAA = 28

	rcodeOK       = 0
	rcodeFormErr  = 1
	rcodeServFail = 2
	rcodeNotImp   = 4
	rcodeRefused  = 5

	maxQuery   = 512
	maxAnswers = 16 // 一次最多回这么多条地址,保证 UDP 报文不超过 512 字节
	rulesTTL   = 3 * time.Second
)

// Server 是 dns-rewrite 服务器。重写规则每次查询时现取(缓存几秒),档案里改了规则不用重启。
type Server struct {
	Addr string // 默认 DefaultAddr
	// Rules 取当前已启用的重写规则。
	Rules func(ctx context.Context) ([]Rule, error)
	// Resolve 解析域名型规则的目标域名(ipv6 为真查 AAAA,否则查 A);nil = 域名型规则一律答 SERVFAIL。
	Resolve func(ctx context.Context, host string, ipv6 bool) ([]netip.Addr, error)
	Log     *slog.Logger
	// TTL 是答案的存活秒数,默认 60(短一点,改了规则很快生效)。
	TTL uint32
	// Timeout 是一次查询的总时限,默认 4 秒。
	Timeout time.Duration

	mu       sync.Mutex
	cached   []Rule
	cachedAt time.Time
	cachedOK bool
	now      func() time.Time
}

func (s *Server) addr() string {
	if s.Addr != "" {
		return s.Addr
	}
	return DefaultAddr
}

func (s *Server) ttl() uint32 {
	if s.TTL > 0 {
		return s.TTL
	}
	return 60
}

func (s *Server) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 4 * time.Second
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Listen 打开 UDP 监听。端口被占用等错误由调用方决定怎么处理(面板不该因此起不来)。
func (s *Server) Listen() (net.PacketConn, error) { return net.ListenPacket("udp", s.addr()) }

// Serve 在 conn 上服务,直到 ctx 结束(会关掉 conn);返回前等所有在途查询处理完。
func (s *Server) Serve(ctx context.Context, conn net.PacketConn) {
	go func() { <-ctx.Done(); _ = conn.Close() }()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	buf := make([]byte, 4096)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if s.Log != nil {
				s.Log.Warn("DNS 重写服务读取失败,停止", "error", err)
			}
			break
		}
		if n > maxQuery {
			continue
		}
		req := append([]byte(nil), buf[:n]...)
		select {
		case sem <- struct{}{}:
		default:
			continue // 太忙就丢,客户端会重试
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			qctx, cancel := context.WithTimeout(ctx, s.timeout())
			defer cancel()
			if resp := s.Handle(qctx, req); resp != nil {
				_, _ = conn.WriteTo(resp, from)
			}
		}()
	}
	wg.Wait()
}

// Run 监听并服务,直到 ctx 结束。监听失败直接返回 error。
func (s *Server) Run(ctx context.Context) error {
	conn, err := s.Listen()
	if err != nil {
		return err
	}
	s.Serve(ctx, conn)
	return nil
}

func (s *Server) rules(ctx context.Context) ([]Rule, error) {
	if s.Rules == nil {
		return nil, errors.New("没有接入规则")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cachedOK && s.clock().Sub(s.cachedAt) < rulesTTL {
		return s.cached, nil
	}
	r, err := s.Rules(ctx)
	if err != nil {
		return nil, err
	}
	s.cached, s.cachedAt, s.cachedOK = r, s.clock(), true
	return r, nil
}

type question struct {
	id     uint16
	flags  uint16
	name   string
	qtype  uint16
	qclass uint16
	raw    []byte // 原样的问题段(名字 + 类型 + 类别),答复里原样带回
}

var errBadQuery = errors.New("DNS 请求格式不对")

// parseQuestion 解析请求:只收一个问题;问题里的名字不允许压缩指针。
func parseQuestion(b []byte) (*question, int, error) {
	if len(b) < 12 {
		return nil, 0, errBadQuery
	}
	q := &question{id: binary.BigEndian.Uint16(b), flags: binary.BigEndian.Uint16(b[2:])}
	if q.flags&0x8000 != 0 {
		return nil, 0, errBadQuery // 这是答复,不是请求
	}
	if op := (q.flags >> 11) & 0xF; op != 0 {
		return q, rcodeNotImp, nil
	}
	if binary.BigEndian.Uint16(b[4:]) != 1 {
		return q, rcodeFormErr, nil
	}
	off := 12
	var labels []string
	total := 0
	for {
		if off >= len(b) {
			return q, rcodeFormErr, nil
		}
		l := int(b[off])
		if l == 0 {
			off++
			break
		}
		if l&0xC0 != 0 || off+1+l > len(b) {
			return q, rcodeFormErr, nil
		}
		total += l + 1
		if total > 254 {
			return q, rcodeFormErr, nil
		}
		labels = append(labels, string(b[off+1:off+1+l]))
		off += 1 + l
	}
	if off+4 > len(b) {
		return q, rcodeFormErr, nil
	}
	q.name = strings.ToLower(strings.Join(labels, "."))
	q.qtype = binary.BigEndian.Uint16(b[off:])
	q.qclass = binary.BigEndian.Uint16(b[off+2:])
	q.raw = b[12 : off+4]
	return q, rcodeOK, nil
}

// Handle 处理一个请求报文,返回答复报文(请求本身不是合法的 DNS 请求时返回 nil,不回)。
func (s *Server) Handle(ctx context.Context, req []byte) []byte {
	q, rc, err := parseQuestion(req)
	if err != nil {
		return nil
	}
	if rc != rcodeOK {
		return reply(q, rc, nil, 0)
	}
	if q.qclass != 1 {
		return reply(q, rcodeRefused, nil, 0)
	}
	rules, err := s.rules(ctx)
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("DNS 重写服务读不到规则", "error", err)
		}
		return reply(q, rcodeServFail, nil, 0)
	}
	rule := Match(rules, q.name)
	if rule == nil {
		return reply(q, rcodeRefused, nil, 0) // 配置里有、档案里已经没有的规则:别瞎答
	}
	if q.qtype != qtypeA && q.qtype != qtypeAAAA {
		return reply(q, rcodeOK, nil, s.ttl()) // 别的类型(HTTPS 等)答「没有这条记录」,客户端会退回 A / AAAA
	}
	want6 := q.qtype == qtypeAAAA
	var addrs []netip.Addr
	if rule.Domain != "" {
		if Match(rules, rule.Domain) != nil {
			return reply(q, rcodeServFail, nil, 0) // 目标域名本身又是重写源:不跟着转,免得绕圈
		}
		if s.Resolve == nil {
			return reply(q, rcodeServFail, nil, 0)
		}
		addrs, err = s.Resolve(ctx, rule.Domain, want6)
		if err != nil {
			if s.Log != nil {
				s.Log.Debug("DNS 重写:目标域名解析失败", "source", rule.Source, "target", rule.Domain, "error", err)
			}
			return reply(q, rcodeServFail, nil, 0)
		}
	} else {
		for _, a := range rule.Addresses {
			if ip, err := netip.ParseAddr(a); err == nil {
				addrs = append(addrs, ip)
			}
		}
	}
	var picked []netip.Addr
	for _, a := range addrs {
		a = a.Unmap()
		if a.Is6() == want6 && len(picked) < maxAnswers {
			picked = append(picked, a)
		}
	}
	return reply(q, rcodeOK, picked, s.ttl())
}

// reply 拼答复:原样带回问题,答案名字用指向问题的压缩指针。
func reply(q *question, rcode int, addrs []netip.Addr, ttl uint32) []byte {
	flags := uint16(0x8000|0x0080) | (q.flags & 0x0100) | uint16(rcode)
	b := make([]byte, 12, 12+len(q.raw)+len(addrs)*28)
	binary.BigEndian.PutUint16(b, q.id)
	binary.BigEndian.PutUint16(b[2:], flags)
	if q.raw != nil {
		binary.BigEndian.PutUint16(b[4:], 1)
		b = append(b, q.raw...)
	}
	binary.BigEndian.PutUint16(b[6:], uint16(len(addrs)))
	for _, a := range addrs {
		typ, rd := uint16(qtypeA), a.AsSlice()
		if a.Is6() {
			typ = qtypeAAAA
		}
		b = append(b, 0xC0, 0x0C)
		b = binary.BigEndian.AppendUint16(b, typ)
		b = binary.BigEndian.AppendUint16(b, 1)
		b = binary.BigEndian.AppendUint32(b, ttl)
		b = binary.BigEndian.AppendUint16(b, uint16(len(rd)))
		b = append(b, rd...)
	}
	return b
}
