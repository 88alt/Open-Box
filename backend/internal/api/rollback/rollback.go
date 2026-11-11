// Package rollback 实现 POST /api/openbox/rollback(紧急回滚到直连;对齐上游 v0.1.423 的路由,前端没有调用,命令行 / 外部脚本用)。
//
// 做法(自己的设计,只对齐上游可读路由的外形 { ok, actions, failures }):
//  1. 和「部署」互斥:部署在进行就不动,回 409(上游是排队等锁,这里不等,和恢复出厂设置一致)。
//  2. 撤销 DNS 接管(restore-dns):dnsmasq 不能还指着一个停了的内核。这一步不成功就不停内核、也不关开机自启(沿用「撤销不成功就不动内核」),
//     failures 里写明。没有 DNS 接管的平台(Debian / systemd)DNS 为空,这一步不出现在 actions 里。
//  3. 停内核(stop-core)。
//  4. 关内核的开机自启(disable-autostart):否则重启设备后会重新拉起一个已被撤销接管的内核。停内核没成功也照样做这一步。
//
// 后端没有管「代理相关防火墙规则」的东西(上游的 remove-firewall 步骤对应的是上游自己写的 uci 规则),所以没有这一步。
// 任何一步没成 ok 就是 false,界面不能再说「已恢复直连」。命令本身失败(退出码非 0)按失败记,不是 HTTP 错误,状态码仍是 200。
package rollback

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// ErrBusy:部署正在进行(HTTP 409)。
var ErrBusy = errors.New("正在部署,请等它结束再试")

// Controller 是停内核、关自启的最小接口(*system.Services 满足)。
type Controller interface {
	Do(ctx context.Context, name, action string) (system.ActionResult, error)
}

// DNSRevert 撤销 DNS 接管(*dnssync.Coordinator 满足)。
type DNSRevert interface {
	BeforeKernelStop(ctx context.Context) error
}

// Deps 是本分组的依赖。
type Deps struct {
	Services    Controller
	DNS         DNSRevert // 为空 = 这个平台没有 DNS 接管,跳过 restore-dns
	Kind        system.InitKind
	CoreService string
	// Exclusive 在部署的锁里执行 fn;部署在进行时不执行 fn,返回 ErrBusy。为空 = 不互斥(测试用)。
	Exclusive func(fn func() error) error
}

// Failure 是一步失败的说明。
type Failure struct {
	Step    string `json:"step"`
	Message string `json:"message"`
}

// Result 是响应。
type Result struct {
	OK       bool      `json:"ok"`
	Actions  []string  `json:"actions"`
	Failures []Failure `json:"failures"`
}

// budget 是整个回滚的总时限(撤销 DNS 接管内部还有更细的超时)。
const budget = 3 * time.Minute

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("/api/openbox/rollback", httpx.Methods(map[string]http.HandlerFunc{http.MethodPost: d.run}))
}

func describe(res system.ActionResult) string {
	if m := strings.TrimSpace(res.Stderr); m != "" {
		return m
	}
	return fmt.Sprintf("code %d", res.Code)
}

// Run 按上面的顺序做一遍,返回结果(不管 HTTP)。
func (d Deps) Run(ctx context.Context) Result {
	res := Result{Actions: []string{}, Failures: []Failure{}}
	fail := func(step, msg string) { res.Failures = append(res.Failures, Failure{Step: step, Message: msg}) }
	do := func(step, action string) bool {
		r, err := d.Services.Do(ctx, d.CoreService, action)
		switch {
		case err != nil:
			fail(step, err.Error())
		case !r.OK:
			fail(step, describe(r))
		default:
			res.Actions = append(res.Actions, step)
			return true
		}
		return false
	}
	if d.DNS != nil {
		if err := d.DNS.BeforeKernelStop(ctx); err != nil {
			fail("restore-dns", err.Error())
			res.OK = false
			return res
		}
		res.Actions = append(res.Actions, "restore-dns")
	}
	do("stop-core", "stop")
	do("disable-autostart", "disable")
	res.OK = len(res.Failures) == 0
	return res
}

func (d Deps) run(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Kind == system.InitNone || d.Services == nil {
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "message": system.ErrNoInit.Error()})
		return
	}
	// 一旦开始就要做完:请求断开不能把 DNS 接管撤到一半
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), budget)
	defer cancel()
	var res Result
	exec := func() error { res = d.Run(ctx); return nil }
	var err error
	if d.Exclusive != nil {
		err = d.Exclusive(exec)
	} else {
		err = exec()
	}
	if errors.Is(err, ErrBusy) {
		httpx.JSON(w, http.StatusConflict, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	if err != nil {
		httpx.JSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
