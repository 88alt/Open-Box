package terminaltest

import (
	"context"
	"net"
	"strconv"

	"github.com/timeflysoon/Open-Box/backend/internal/conntrack"
	"github.com/timeflysoon/Open-Box/backend/internal/nsprobe"
	"github.com/timeflysoon/Open-Box/backend/internal/routeget"
	"github.com/timeflysoon/Open-Box/backend/internal/routetest"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

// HoldProber 是能「出结果后先留着连接」的探测器(*nsprobe.Exec 实现)。有它、又接了 Deps.Kernel 时,
// 后端趁连接还开着去读内核连接表,才有 kernel 和 entry;否则这两个字段如实不出现。
type HoldProber interface {
	ProbeHold(ctx context.Context, ns string, req nsprobe.Request, during func(nsprobe.Result)) (nsprobe.Result, string, error)
}

// RouteGetter 问系统路由(*routeget.Getter 实现)。
type RouteGetter interface {
	Get(ctx context.Context, dst, src, iif string) (routeget.Info, error)
}

// kernelLook 是连接还开着时对内核连接表的一次查询。
type kernelLook struct {
	conn   *singbox.KernelConn
	direct []string
	err    error
}

func (h *handlers) lookupKernel(ctx context.Context, srcIP string, r nsprobe.Result) *kernelLook {
	k := &kernelLook{}
	snap, err := h.d.Kernel(ctx)
	if err != nil {
		k.err = err
		return k
	}
	k.direct = snap.DirectTags
	want := strconv.Itoa(r.LocalPort)
	src := net.ParseIP(srcIP)
	for i := range snap.Conns {
		c := &snap.Conns[i]
		if c.SourcePort == want && src != nil && src.Equal(net.ParseIP(c.Source)) {
			k.conn = c // 同一个来源地址 + 端口只会有一条;万一重复取最后一条
		}
	}
	return k
}

// fillKernelAndEntry 在探测连接还开着时取到内核连接表的结果(k)之后,补 kernel / entry(以及旁路时的 exit.forward)。
// 判法(自己的设计,依据前端 OpenboxTerminalEntry 的字段注释和文案,没有参照上游):
//
//	没连上                        → unknown(not-connected:DNS 没出地址;no-connection:连接信息没拿到)
//	读内核连接表出错              → unknown(evidence-failed)
//	内核连接表里有这条连接        → kernel;conntrack 回复方被改写成路由器自己的地址 → via redirect;
//	                                 否则路由进了 tun 设备 → via tun;否则 via connection-table
//	内核里没有:
//	  conntrack 里找不到 / 读不到 → unknown(no-conntrack,没有系统转发证据不判旁路)
//	  回复方改写成路由器自己的地址 → kernel via redirect(内核那边没看到只是辅证没取到)
//	  回复方改写成别处            → unknown(dnat-elsewhere)
//	  没被改写:路由查询失败       → unknown(no-route);路由进 tun → unknown(route-tun,证据矛盾);
//	                                 否则 bypass(设备、网关取自路由;回复方的目的地址不是终端 = 经 NAT)
//
// 不拿不到的东西凑数:nft 旁路集合(evidence.set / setHit)后端拿不到(原生旁路计划只有 core 生成配置时才有),不带;
// 入口模式(evidence.autoRedirect)同理不带。
func (h *handlers) fillKernelAndEntry(ctx context.Context, out map[string]any, ex map[string]any, env probeEnv, in routetest.Input, res nsprobe.Result, k *kernelLook) {
	ev := map[string]any{}
	entry := map[string]any{"evidence": ev}
	out["entry"] = entry
	unknown := func(reason string) { entry["kind"], entry["reason"] = "unknown", reason }

	if res.LocalPort == 0 {
		if res.DNS != nil && (!res.DNS.OK || res.ConnectTo == "") {
			unknown("not-connected")
		} else {
			unknown("no-connection")
		}
		return
	}
	if k == nil {
		unknown("no-connection")
		return
	}
	if k.err != nil {
		unknown("evidence-failed")
		entry["error"] = k.err.Error()
		ev["kernelError"] = k.err.Error()
		return
	}

	// kernel:内核连接表里这条连接的归属
	ev["kernelConn"] = k.conn != nil
	if v, ok := routetest.ViewOf(k.conn, k.direct); ok {
		kv := map[string]any{"seen": true, "rule": v.Rule, "rulePayload": v.RulePayload, "chains": v.Chains, "viaProxy": v.ViaProxy}
		if v.DestinationIP != "" {
			kv["destinationIP"] = v.DestinationIP
		}
		if v.Host != "" {
			kv["host"] = v.Host
		}
		if v.NoDomain {
			kv["noDomain"] = true
		}
		if k.conn.Inbound != "" {
			kv["inbound"] = k.conn.Inbound
		}
		out["kernel"] = kv
	} else {
		kv := map[string]any{"seen": false}
		if k.conn != nil && k.conn.Inbound != "" {
			kv["inbound"] = k.conn.Inbound
		}
		out["kernel"] = kv
	}

	// conntrack:这条连接在系统里怎么记的
	dst := res.ConnectTo
	if dst == "" {
		dst = in.Target
	}
	port := res.Port
	if port == 0 {
		port = in.Port
	}
	var ct *conntrack.Entry
	if h.d.Conntrack != nil && net.ParseIP(dst) != nil {
		entries, err := h.d.Conntrack.Find(ctx, conntrack.Query{Proto: "tcp", Src: env.srcIP, Sport: res.LocalPort, Dst: dst, Dport: port})
		switch {
		case err != nil:
			ev["conntrackError"] = err.Error()
		case len(entries) > 0:
			ct = &entries[len(entries)-1]
			ev["conntrack"] = ct.Line
			ev["mark"] = ct.Mark
		}
	}
	rewritten, rewrittenToLocal := false, false
	if ct != nil {
		rewritten = !sameIP(ct.RSrc, ct.Dst) || ct.RSport != ct.Dport
		rewrittenToLocal = rewritten && sameIP(ct.RSrc, env.lanAddr)
	}

	// 路由:包会从哪转出(只在要用时才问)
	var route *routeget.Info
	var routeErr error
	if h.d.Route != nil && net.ParseIP(dst) != nil && env.lanDevice != "" && (k.conn != nil || (ct != nil && !rewritten)) {
		info, err := h.d.Route.Get(ctx, dst, env.srcIP, env.lanDevice)
		if err != nil {
			routeErr = err
		} else {
			route = &info
			ev["route"] = info.Line
			if info.Tun {
				ev["tunDevice"] = info.Dev
			}
		}
	}

	kernelVia := func(via string) {
		entry["kind"], entry["via"] = "kernel", via
		if via == "redirect" && ct != nil {
			entry["redirectPort"] = ct.RSport
			entry["rewrittenTo"] = ct.RSrc
		}
	}
	switch {
	case k.conn != nil:
		switch {
		case rewrittenToLocal:
			kernelVia("redirect")
		case route != nil && route.Tun:
			kernelVia("tun")
		default:
			kernelVia("connection-table")
		}
	case ct == nil:
		unknown("no-conntrack")
	case rewrittenToLocal:
		kernelVia("redirect")
	case rewritten:
		unknown("dnat-elsewhere")
		entry["rewrittenTo"] = ct.RSrc
	case routeErr != nil:
		unknown("no-route")
		entry["error"] = routeErr.Error()
	case route == nil:
		unknown("no-route") // 没接路由查询:没有系统转发证据,不判旁路
	case route.Tun:
		unknown("route-tun")
	default:
		masq := !sameIP(ct.RDst, ct.Src)
		entry["kind"], entry["device"], entry["masquerade"] = "bypass", route.Dev, masq
		if route.Via != "" {
			entry["gateway"] = route.Via
		}
		fwd := map[string]any{"device": route.Dev, "gateway": route.Via, "masquerade": masq, "address": ""}
		if masq {
			fwd["address"] = ct.RDst
		}
		ex["forward"] = fwd
	}
}

func sameIP(a, b string) bool {
	x, y := net.ParseIP(a), net.ParseIP(b)
	return x != nil && y != nil && x.Equal(y)
}
