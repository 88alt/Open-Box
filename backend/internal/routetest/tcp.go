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

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
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
//
// **不同内核对 0x00 的含义不一样(2026-11-26 在真机上对比过,两台内核都跑在同一台机器上)**:
//   - sing-box:出站连完才应答。成功回 0x00 且 BND.ADDR 是**出站的本地套接字地址**;目标端口关着时干脆不应答,
//     一路等到超时;黑洞地址回 0x01(一般性失败)。
//   - mihomo:**先回 0x00 再去连**,BND.ADDR 被填成入站自己的地址(与面板入站完全相同的 host:port)。
//     目标连不上时它是在回完应答之后再关连接——关着的端口约 5 秒后 EOF,黑洞地址约 6 秒后 EOF。
// 所以「读到 0x00」在 mihomo 下不构成证据。判据是应答里的 BND.ADDR:等于入口自己的地址就说明这次应答
// 不代表出站已经连上,这时再等一小段时间:连接被对方关掉(mihomo 的失败路径)按失败报;一直开着就按成功。
// 这段时间花在失败路径上,成功路径不额外等(见 established 的注释)。

const defaultTCPPort = 443

// deferredConnectWait 是「内核先应答、后连目标」时,等它在连接表里出现的时间上限。
// mihomo 实测:连上的连接约 1 秒内出现在 /connections,连不上的(关着的端口、黑洞地址)一直不出现,
// 只会在约 5 秒后把 SOCKS 连接关掉。所以窗口取 3 秒:既够成功路径出现,又比等 EOF 短一半。
const deferredConnectWait = 3 * time.Second

// deferredPollEvery 是查连接表的间隔(成功路径平均多等半格)。
const deferredPollEvery = 250 * time.Millisecond

// deferredFallbackWait 是读不到连接表时的兜底窗口:这时只能靠「内核会不会把连接关掉」判断,
// 而 mihomo 的失败路径约 5~6 秒才关,所以窗口要比它长。默认探测总超时 8 秒,这里取 6.5 秒。
const deferredFallbackWait = 6500 * time.Millisecond

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
	conn, err := socks5Connect(c, dest, port)
	if err != nil {
		// 超时(或调用方取消)的瞬间,上面的 AfterFunc 也会关连接,读到的可能是「连接已关闭」而不是超时;
		// 上下文已经结束时以它为准,别把超时误报成对方不按协议答
		if cerr := ctx.Err(); cerr != nil {
			err = cerr
		}
		p.err = err
		return p
	}
	// 内核把入站自己的地址填在 BND.ADDR 上,说明它是先应答后连(mihomo),0x00 不算证据,要再确认一次(见包注释)。
	if sameEndpoint(conn.boundHost, conn.boundPort, s.proxyAddr()) {
		if err := s.confirmEstablished(ctx, c, p.port); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				err = cerr
			}
			p.err = err
			return p
		}
	}
	_ = c.SetDeadline(time.Time{})
	p.ok, p.ms = true, millis(time.Since(start))
	return p
}

// confirmEstablished 在「内核先应答、后连目标」时确认连接真的建立了。
//
// 依据是 2026-11-26 的实机对照(mihomo):连上的目标约 1 秒内出现在内核连接表(/connections)里;
// 连不上的(关着的端口、黑洞地址)根本不出现,内核只是约 5 秒后把 SOCKS 连接关掉。所以两种结局里
// 先到的那个说了算:连接被关掉 → 失败;连接表里出现这条(按面板入站的本地端口认)→ 成功。
// 连接表读不到(没接 Snapshot、内核不是 mihomo 之类)时退回「等它被关掉」:等满窗口仍开着就算成功,
// 这种情形下给不出更强的证据,如实按「开着就是连上了」处理。
func (s *Service) confirmEstablished(ctx context.Context, c net.Conn, localPort string) error {
	// 连接表读不到(Snapshot 没接、或内核没部署过配置)时只能靠「内核会不会把失败的连接关掉」判断,
	// 而 mihomo 的失败路径约 5~6 秒才关,所以窗口取长的那个。
	wait := deferredConnectWait
	if s.Snapshot == nil || !snapshotUsable(ctx, s.Snapshot, localPort) {
		wait = deferredFallbackWait
	}
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left < wait {
			wait = left
		}
	}
	if wait <= 0 {
		return &probeError{msg: "内核先应答后连接,没来得及确认目标是否连上"}
	}
	deadline := time.Now().Add(wait)

	// 一边等连接被关(读到 EOF / 重置),一边按间隔查连接表
	closed := make(chan error, 1)
	go func() {
		_ = c.SetReadDeadline(deadline)
		var b [1]byte
		n, err := c.Read(b[:])
		if n > 0 {
			closed <- nil // 目标主动发了字节,说明连上了
			return
		}
		closed <- err
	}()
	tick := time.NewTicker(deferredPollEvery)
	defer tick.Stop()
	for {
		select {
		case err := <-closed:
			if err == nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return s.acceptIfSeen(ctx, localPort)
			}
			// 被关掉:内核连不上目标就是这个形状;但也可能它已经在连接表里出现过(连上后立刻被目标关掉)。
			if seenConn(ctx, s.Snapshot, localPort) {
				return nil
			}
			return &probeError{msg: "内核报告目标没有连上(应答之后连接被关闭)"}
		case <-tick.C:
			if seenConn(ctx, s.Snapshot, localPort) {
				return nil
			}
			if time.Now().After(deadline) {
				return s.acceptIfSeen(ctx, localPort)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// acceptIfSeen 是窗口末尾的判定:连接表里看得见这条连接就算成功。
func (s *Service) acceptIfSeen(ctx context.Context, localPort string) error {
	if s.Snapshot == nil || !snapshotUsable(ctx, s.Snapshot, localPort) {
		return nil // 读不到连接表:连接一直开着就是连上了
	}
	if seenConn(ctx, s.Snapshot, localPort) {
		return nil
	}
	return &probeError{msg: "内核报告目标没有连上(连接表里没有这条连接)"}
}

// snapshotUsable 判断连接表此刻读不读得到(判据只在读得到时才有意义)。
func snapshotUsable(ctx context.Context, snapshot func(context.Context) (singbox.KernelSnapshot, error), localPort string) bool {
	if snapshot == nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := snapshot(cctx)
	return err == nil
}

// seenConn 查一次连接快照里有没有本地端口是 localPort 的连接。读不到就当作没有(不猜)。
func seenConn(ctx context.Context, snapshot func(context.Context) (singbox.KernelSnapshot, error), localPort string) bool {
	if snapshot == nil || localPort == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snap, err := snapshot(cctx)
	if err != nil {
		return false
	}
	for i := range snap.Conns {
		if snap.Conns[i].SourcePort == localPort {
			return true
		}
	}
	return false
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

// socks5Connect 在已连上的 c 上做无认证的 SOCKS5 CONNECT 握手。成功时返回应答里带的出站地址信息:
// deferred 为真表示这次 0x00 不能当作「已经连上」的证据(内核把入站自己的地址填在了 BND.ADDR 上,
// 说明它是先应答后连),调用方要再确认一次,见包注释与 confirmEstablished。
func socks5Connect(c net.Conn, host string, port int) (socksOK, error) {
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return socksOK{}, &probeError{msg: "和内核的本地代理入口通信失败"}
	}
	var greet [2]byte
	if _, err := io.ReadFull(c, greet[:]); err != nil {
		return socksOK{}, readErr(err)
	}
	if greet[0] != 0x05 || greet[1] != 0x00 {
		return socksOK{}, errNotSocks
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
			return socksOK{}, &probeError{msg: "目标域名长度不合法"}
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return socksOK{}, &probeError{msg: "和内核的本地代理入口通信失败"}
	}
	var head [4]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return socksOK{}, readErr(err)
	}
	if head[0] != 0x05 {
		return socksOK{}, errNotSocks
	}
	if head[1] != 0x00 {
		return socksOK{}, &probeError{msg: socksReplyText(head[1])}
	}
	// 成功应答后面还跟着出站的本地地址(BND.ADDR / BND.PORT),要读完才算应答完整;顺带用它判断
	// 这次应答是不是内核「先回后连」发出来的。
	var addr []byte
	var portBytes []byte
	switch head[3] {
	case 0x01:
		addr = make([]byte, 4)
	case 0x04:
		addr = make([]byte, 16)
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return socksOK{}, readErr(err)
		}
		addr = make([]byte, int(l[0]))
	default:
		return socksOK{}, errNotSocks
	}
	if _, err := io.ReadFull(c, addr); err != nil {
		return socksOK{}, readErr(err)
	}
	portBytes = make([]byte, 2)
	if _, err := io.ReadFull(c, portBytes); err != nil {
		return socksOK{}, readErr(err)
	}
	return socksOK{boundHost: boundHostText(head[3], addr), boundPort: int(portBytes[0])<<8 | int(portBytes[1])}, nil
}

// socksOK 是成功应答里的出站地址与「这次应答是否可信」的判定依据。
type socksOK struct {
	boundHost string // BND.ADDR 的文本形式(域名地址类型时是域名)
	boundPort int    // BND.PORT
}

// sameEndpoint 判断应答里的 BND.ADDR / BND.PORT 是不是就是面板入站自己。是 → 这次 0x00 只是「请求收到了」。
// 端口必须相同;主机相同,或应答给的是回环 / 未指定地址(有的实现填 0.0.0.0)也算相同。
func sameEndpoint(host string, port int, proxyAddr string) bool {
	ph, pp, err := net.SplitHostPort(proxyAddr)
	if err != nil {
		return false
	}
	pn, err := strconv.Atoi(pp)
	if err != nil || pn != port {
		return false
	}
	if host == ph {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func boundHostText(atyp byte, b []byte) string {
	switch atyp {
	case 0x01, 0x04:
		return net.IP(b).String()
	}
	return string(b)
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
