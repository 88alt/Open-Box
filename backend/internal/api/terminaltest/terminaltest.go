// Package terminaltest 实现 /api/openbox/terminal-test:模拟 LAN 终端的真实路由测试。
// 响应形状取自前端 api/openbox.ts 的 OpenboxTerminalCapability / OpenboxTerminalTest,没有参照上游。
//
// 现在只做了「能不能做」的检测(GET /capability,只读)。真正建虚拟终端(网络命名空间 + veth + DHCP)的
// POST 部分还没做:缺前提时如实回 capable:false 和缺的项,具备前提时回 501,不去碰系统。
package terminaltest

import (
	"context"
	"errors"
	"net/http"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/routetest"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Deps 是本分组的依赖。
type Deps struct {
	// Capability 返回此刻的前提检查结果(*system.TerminalProbe 的 Capability),测试里换假的。
	Capability func(ctx context.Context) system.TerminalCapability
}

const maxBody = 4 << 10

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	h := &handlers{d: d}
	mux.HandleFunc("/api/openbox/terminal-test/capability", httpx.Methods(map[string]http.HandlerFunc{http.MethodGet: h.capability}))
	mux.HandleFunc("/api/openbox/terminal-test", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: h.run}))
}

type handlers struct{ d Deps }

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
	httpx.Error(w, http.StatusNotImplemented, "模拟终端的真实路由测试暂未实现(这台设备具备条件,但建立虚拟终端的部分还没做)")
}
