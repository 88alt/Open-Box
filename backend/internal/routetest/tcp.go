package routetest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/sitelatency"
)

// TCP 方式:经内核的面板入站做一次 SOCKS5 CONNECT,看内核能不能连上目标。
//
// 为什么不用 HTTP CONNECT(GET / HEAD / TLS 用的那条):2026-10-30 在沙箱里用真实 sing-box 1.14.4 实测,
// mixed 入站对 HTTP CONNECT 永远先回 200,哪怕目标端口是关着的(内核之后才在日志里报 connection refused),
// 所以 200 只说明「内核接了这个请求」,不说明「连上了」。同一个入站对 SOCKS5 CONNECT 则是出站连完之后才应答:
// 开着的端口回成功码 0x00(带着出站本地套接字的地址),关着的端口回 0x05(连接被拒绝)。
// 面板入站(core 生成的配置里的 panel-in)是 mixed 类型,同一个端口两种协议都收,所以 TCP 方式走 SOCKS5。
// 协议见 RFC 1928。
//
// 成功只表示「内核按它的路由把一条 TCP 连接连到了目标」,不发任何数据,不保证目标上跑的是什么服务。

const defaultTCPPort = 443

// probeError 是我们自己判断出的、已经是给人看的中文的失败原因。
type probeError struct{ msg string }

func (e *probeError) Error() string { return e.msg }

// describeErr 把探测错误翻成给人看的中文(自己的 probeError 原样,其它交给 sitelatency)。
func (s *Service) describeErr(err error) string {
	var pe *probeError
	if errors.As(err, &pe) {
		return pe.msg
	}
	return sitelatency.DescribeError(err, s.timeout())
}

// tcpAddr 是 TCP 方式在 exit.url 里显示的地址:主机:端口(没指定端口按 443,IPv6 字面量带方括号)。
func tcpAddr(in Input) string {
	port := in.Port
	if port == 0 {
		port = defaultTCPPort
	}
	return net.JoinHostPort(in.Target, strconv.Itoa(port))
}

// probeTCP 做一次 TCP 探测。connectTo 非空时连这个 IP(含义同 probeHTTP:像终端拿到 DNS 答案后那样),
// 否则连 in.Target(域名由内核自己解析并路由)。耗时是「连到面板入站 + 内核连上目标」。
func (s *Service) probeTCP(ctx context.Context, in Input, connectTo string) *probed {
	p := &probed{}
	port := in.Port
	if port == 0 {
		port = defaultTCPPort
	}
	dest := in.Target
	if connectTo != "" {
		dest = connectTo
	}
	start := time.Now()
	d := &net.Dialer{Timeout: s.timeout()}
	c, err := d.DialContext(ctx, "tcp", s.proxyAddr())
	if err != nil {
		if ctx.Err() != nil {
			p.err = err
		} else {
			p.err = &probeError{msg: "连不上内核的本地代理入口(内核没在运行?)"}
		}
		return p
	}
	p.closers = append(p.closers, func() { _ = c.Close() })
	// 调用方取消时立刻关连接,不让握手一直卡到超时
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	p.closers = append(p.closers, func() { stop() })
	if ta, ok := c.LocalAddr().(*net.TCPAddr); ok {
		p.port = strconv.Itoa(ta.Port)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if err := socks5Connect(c, dest, port); err != nil {
		// 超时(或调用方取消)的瞬间,上面的 AfterFunc 也会关连接,读到的可能是「连接已关闭」而不是超时;
		// 上下文已经结束时以它为准,别把超时误报成对方不按协议答
		if cerr := ctx.Err(); cerr != nil {
			err = cerr
		}
		p.err = err
		return p
	}
	_ = c.SetDeadline(time.Time{})
	p.ok, p.ms = true, millis(time.Since(start))
	return p
}

var errNotSocks = &probeError{msg: "内核的本地代理入口没有按 SOCKS5 应答"}

// readErr 区分「超时 / 取消」(原样交给 DescribeError)和「对方不是按协议答的」。
func readErr(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return err
	}
	return errNotSocks
}

// socks5Connect 在已连上的 c 上做无认证的 SOCKS5 CONNECT 握手,成功返回 nil。
func socks5Connect(c net.Conn, host string, port int) error {
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return &probeError{msg: "和内核的本地代理入口通信失败"}
	}
	var greet [2]byte
	if _, err := io.ReadFull(c, greet[:]); err != nil {
		return readErr(err)
	}
	if greet[0] != 0x05 || greet[1] != 0x00 {
		return errNotSocks
	}
	req := []byte{0x05, 0x01, 0x00}
	if ad, err := netip.ParseAddr(host); err == nil {
		ad = ad.Unmap()
		if ad.Is4() {
			b := ad.As4()
			req = append(req, 0x01)
			req = append(req, b[:]...)
		} else {
			b := ad.As16()
			req = append(req, 0x04)
			req = append(req, b[:]...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return &probeError{msg: "目标域名长度不合法"}
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return &probeError{msg: "和内核的本地代理入口通信失败"}
	}
	var head [4]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return readErr(err)
	}
	if head[0] != 0x05 {
		return errNotSocks
	}
	if head[1] != 0x00 {
		return &probeError{msg: socksReplyText(head[1])}
	}
	// 成功应答后面还跟着出站的本地地址,读掉(不用,但要确认应答是完整的)
	var rest int
	switch head[3] {
	case 0x01:
		rest = 4 + 2
	case 0x04:
		rest = 16 + 2
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return readErr(err)
		}
		rest = int(l[0]) + 2
	default:
		return errNotSocks
	}
	if _, err := io.ReadFull(c, make([]byte, rest)); err != nil {
		return readErr(err)
	}
	return nil
}

// socksReplyText 把 RFC 1928 的应答码翻成中文。内核在出站没连上时按原因给码,这里只如实转述,不多猜。
func socksReplyText(rep byte) string {
	switch rep {
	case 0x01:
		return "内核没能连上目标(一般性失败)"
	case 0x02:
		return "内核的规则不允许连这个目标"
	case 0x03:
		return "内核报告网络不可达"
	case 0x04:
		return "内核报告目标主机不可达"
	case 0x05:
		return "内核报告目标拒绝了连接"
	case 0x06:
		return "内核连目标超时"
	case 0x07:
		return "内核不支持这个操作"
	case 0x08:
		return "内核不支持这个地址类型"
	}
	return fmt.Sprintf("内核报告连接失败(代码 0x%02x)", rep)
}
