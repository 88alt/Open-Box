package routetest

import (
	"context"
	"net/netip"

	"github.com/timeflysoon/Open-Box/backend/internal/sitelatency"
)

// 响应里的 exit6:域名有 AAAA 答案时,按第一个 v6 地址再访问一次。
// 自己的设计,依据前端 OpenboxRouteTest.exit6 的类型和注释(「有 AAAA 记录时按第一个 v6 地址再访问一次的结果
// (只有探测结果,没有连接表信息)」),没有参照上游。
//
// 做法:隧道连的是这个 v6 地址,请求的 Host、TLS 的 SNI 和证书校验仍然用域名——和终端拿到 AAAA 答案后
// 直接连那个地址是一样的,测的是「这条 v6 路径通不通」,与 exit(按域名走内核解析)互相对照。
//
// 不做的情形(exit6 不出现,而不是编一个结果):
//   - 目标是 IP(没有解析,resolve 也不存在);
//   - 没有 AAAA 答案(档案没开 IPv6、查不到、查失败);
//   - 第一个 v6 地址落在内核的 fake-ip 池:那是内核发的占位地址,不是真实服务器,连它没有意义;
//   - 探测方式是 TCP(本来就不支持,见包注释)。
// 因为连接表按「面板入站的本地端口」认线路,而这里连的是 IP、规则命中的是 IP 规则,没有可比的线路信息,
// 所以只给探测结果,不读连接表(与前端注释一致)。

// Exit6 对应前端 OpenboxRouteTest.exit6。
type Exit6 struct {
	ConnectTo string `json:"connectTo"` // 实际连的那个 v6 地址
	OK        bool   `json:"ok"`
	Status    *int   `json:"status,omitempty"`
	MS        *int   `json:"ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// firstV6 取 resolve 里第一个能解析成 IPv6 的答案,落在 fake-ip 池里的不算(返回 false)。
func firstV6(r *Resolve) (netip.Addr, bool) {
	if r == nil {
		return netip.Addr{}, false
	}
	for _, a := range r.Answers6 {
		ad, err := netip.ParseAddr(a)
		if err != nil || !ad.Is6() || ad.Is4In6() {
			continue
		}
		if inFakePool(ad) {
			return netip.Addr{}, false // 第一个就是占位地址:整组都是内核发的,不测
		}
		return ad, true
	}
	return netip.Addr{}, false
}

// probeExit6 做 exit6;不适用时返回 nil。探测用独立的超时,不挤占主探测的时间。
func (s *Service) probeExit6(ctx context.Context, in Input, rs *Resolve) *Exit6 {
	if in.Method == MethodTCP {
		return nil
	}
	ad, ok := firstV6(rs)
	if !ok {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	var p *probed
	if in.Method == MethodTLS {
		p = s.probeTLS(pctx, in, ad.String())
	} else {
		p = s.probeHTTP(pctx, in, ad.String())
	}
	defer p.close()
	out := &Exit6{ConnectTo: ad.String()}
	if ctx.Err() != nil {
		return out // 调用方取消了:Run 会丢弃整个结果
	}
	out.OK, out.Status, out.MS = p.ok, p.status, p.ms
	if p.err != nil {
		out.Error = sitelatency.DescribeError(p.err, s.timeout())
	}
	return out
}
