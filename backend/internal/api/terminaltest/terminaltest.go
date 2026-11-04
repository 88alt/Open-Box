// Package terminaltest 实现 /api/openbox/terminal-test:模拟 LAN 终端的真实路由测试。
// 响应形状取自前端 api/openbox.ts 的 OpenboxTerminalCapability / OpenboxTerminalTest,没有参照上游。
//
// GET /capability 只读,检测「能不能做」。POST 现在做到「建虚拟终端并取址」:缺前提时如实回 capable:false
// 和缺的项;具备前提就建命名空间 + veth、DHCP 取址,把结果放进 source 返回;建不起来回 setupError(原因原文)。
// 取址之后的探测(GET / HEAD / TCP / TLS)、conntrack 取证、内核 DNS 追踪还没做,所以响应里还没有 exit /
// entry / dnsEvidence / kernelDns,前端不会把它当作一次完整的结果。没有注入 Terminal(没接线)时回 501,不碰系统。
// 建好的虚拟终端空闲一段时间(IdleAfter)后自动拆掉,下一次测试再建。
package terminaltest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/routetest"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Terminal 是虚拟终端的建、取址、拆(*system.VTermNet 实现了它),测试里换假的。
type Terminal interface {
	Ensure(ctx context.Context, lanDevice string) (system.VirtualTerminal, error)
	AcquireDHCP(ctx context.Context, v system.VirtualTerminal) (system.DHCPLease, error)
	Teardown(ctx context.Context) error
}

// Deps 是本分组的依赖。
type Deps struct {
	// Capability 返回此刻的前提检查结果(*system.TerminalProbe 的 Capability),测试里换假的。
	Capability func(ctx context.Context) system.TerminalCapability
	// Terminal 为空 = 没接线,POST 具备前提时回 501。
	Terminal Terminal
	// IdleAfter 是最后一次测试之后过多久自动拆掉虚拟终端,默认 5 分钟。
	IdleAfter time.Duration
	// Log 可为空。
	Log *slog.Logger
}

const (
	maxBody          = 4 << 10
	defaultIdleAfter = 5 * time.Minute
	idleTeardownTime = 60 * time.Second
)

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{d: d, sem: make(chan struct{}, 1)}
	if h.d.IdleAfter <= 0 {
		h.d.IdleAfter = defaultIdleAfter
	}
	if h.d.Log == nil {
		h.d.Log = slog.New(slog.DiscardHandler)
	}
	mux.HandleFunc("/api/openbox/terminal-test/capability", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.capability}))
	mux.HandleFunc("/api/openbox/terminal-test", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.run}))
}

type handlers struct {
	d Deps
	// sem 让同一时刻只有一次测试在建 / 取址(后面的探测也在它里面);用通道而不是互斥锁,
	// 等的时候请求被取消能马上走。timer 只在持有 sem 时读写。
	sem   chan struct{}
	timer *time.Timer
}

func (h *handlers) cap(ctx context.Context) system.TerminalCapability {
	if h.d.Capability == nil {
		return system.TerminalCapability{Missing: []string{system.MissingRoot}}
	}
	c := h.d.Capability(ctx)
	if c.Missing == nil {
		c.Missing = []string{}
	}
	return c
}

func (h *handlers) capability(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	httpx.JSON(w, http.StatusOK, h.cap(r.Context()))
}

// run 对应前端 testTerminal。请求体与 route-test 一样({ target, port?, method? }),校验也共用 routetest.Parse。
func (h *handlers) run(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	var in routetest.Input
	if !httpx.DecodeJSON(w, r, &in, maxBody) {
		return
	}
	in, err := routetest.Parse(in)
	if err != nil {
		var ie *routetest.InputError
		if errors.As(err, &ie) {
			httpx.Error(w, http.StatusBadRequest, ie.Msg)
		} else {
			httpx.Error(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	c := h.cap(r.Context())
	if !c.OK {
		// 缺前提:前端按 capable:false + missing 提示,不是错误
		httpx.JSON(w, http.StatusOK, map[string]any{"target": in.Target, "mode": "lan", "capable": false, "missing": c.Missing})
		return
	}
	if h.d.Terminal == nil {
		httpx.Error(w, http.StatusNotImplemented, "模拟终端的真实路由测试暂未实现(这台设备具备条件,但建立虚拟终端的部分还没接上)")
		return
	}
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-r.Context().Done():
		return
	}
	// 测试开始就停掉空闲拆除的计时,做完再重新计时;计时到点时拿不到 sem 就说明有测试在跑,不拆
	h.stopIdle()
	defer h.armIdle()

	start := time.Now()
	out := map[string]any{"target": in.Target, "mode": "lan", "capable": true}
	done := func() {
		out["timing"] = map[string]any{"totalMs": time.Since(start).Milliseconds()}
		httpx.JSON(w, http.StatusOK, out)
	}
	if c.LAN == nil || c.LAN.Device == "" {
		out["setupError"] = "没有找到可以接虚拟终端的 LAN 口"
		done()
		return
	}
	v, err := h.d.Terminal.Ensure(r.Context(), c.LAN.Device)
	if err != nil {
		if r.Context().Err() != nil {
			return // 请求被取消,Ensure 已自己回滚
		}
		out["setupError"] = err.Error()
		done()
		return
	}
	lease, err := h.d.Terminal.AcquireDHCP(r.Context(), v)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		out["setupError"] = err.Error()
		done()
		return
	}
	dns := lease.DNS
	if dns == nil {
		dns = []string{}
	}
	out["source"] = map[string]any{
		"kind": "virtual", "name": v.Namespace, "ip": lease.IP, "mac": v.MAC, "via": "dhcp",
		"dns": dns, "gateway": lease.Gateway, "lanDevice": v.LANDevice, "reused": v.Reused, "dhcpMs": lease.Ms,
	}
	done()
}

// stopIdle / armIdle 管「空闲后自动拆除」的计时,调用时都要持有 sem。
func (h *handlers) stopIdle() {
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}

func (h *handlers) armIdle() {
	h.timer = time.AfterFunc(h.d.IdleAfter, h.idleTeardown)
}

// idleTeardown 在空闲到点时拆掉虚拟终端。拿不到 sem(有测试正在跑)就什么都不做:那次测试结束时会重新计时。
// 拆失败只记日志,记账保留,下次测试或下次启动会再拆(见 system.VTermNet)。
func (h *handlers) idleTeardown() {
	select {
	case h.sem <- struct{}{}:
	default:
		return
	}
	defer func() { <-h.sem }()
	h.timer = nil
	ctx, cancel := context.WithTimeout(context.Background(), idleTeardownTime)
	defer cancel()
	if err := h.d.Terminal.Teardown(ctx); err != nil {
		h.d.Log.Warn("空闲后拆除虚拟终端失败,记账已保留,之后会再试", "err", err)
		return
	}
	h.d.Log.Info("虚拟终端空闲,已拆除")
}
