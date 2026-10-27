// Package service 实现 /api/openbox/service/* 和 /api/openbox/kernel/version:
// 查询、启动、停止、重启、开机自启 sing-box,以及读内核版本。
// 这里只做「服务控制」;启动 / 重启时「生成配置 → 校验 → 重启 → 确认在跑」的完整部署流程另做(STATUS.md M1)。
package service

import (
	"context"
	"errors"
	"net/http"
	"runtime"

	"github.com/timeflysoon/Open-Box/backend/internal/httpx"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// Controller 是服务控制的最小接口(*system.Services 实现它),测试里换假的。
type Controller interface {
	Status(ctx context.Context, name string) (system.ServiceInfo, error)
	Do(ctx context.Context, name, action string) (system.ActionResult, error)
}

// ConflictFinder 找出正在运行的其它代理服务(*system.ConflictDetector 实现它),测试里换假的。
type ConflictFinder interface {
	Find(ctx context.Context) ([]system.Conflict, error)
}

// Deps 是本分组的依赖。
type Deps struct {
	Services    Controller
	Conflicts   ConflictFinder // 为空 = 不检测,conflicts 恒为空数组
	Kind        system.InitKind
	CoreService string // sing-box 的服务名
	PanelName   string // 后端自己的服务名(只查开机自启)
	SingBox     string // sing-box 可执行文件
	Arch        string // 空 = runtime.GOARCH
}

// Register 把接口挂到 mux 上。
func Register(mux *http.ServeMux, d Deps) {
	if d.Arch == "" {
		d.Arch = runtime.GOARCH
	}
	mux.HandleFunc("/api/openbox/service/status", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: d.status,
	}))
	mux.HandleFunc("/api/openbox/service/core/{action}", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodPost: d.action,
	}))
	mux.HandleFunc("/api/openbox/kernel/version", httpx.Methods(map[string]http.HandlerFunc{
		http.MethodGet: func(w http.ResponseWriter, r *http.Request) {
			httpx.NoStore(w)
			httpx.JSON(w, http.StatusOK, singbox.ReadKernelVersion(r.Context(), d.SingBox))
		},
	}))
}

type statusResponse struct {
	Core      system.ServiceInfo `json:"core"`
	Panel     system.ServiceInfo `json:"panel"`
	Conflicts []system.Conflict  `json:"conflicts"`
	Platform  string             `json:"platform"`
	Arch      string             `json:"arch"`
	// 还没重启生效的改动。部署流程接进来之前恒为「没有」。
	PendingRestart pending `json:"pendingRestart"`
}

type pending struct {
	Pending bool     `json:"pending"`
	Reasons []string `json:"reasons"`
}

func (d Deps) status(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	resp := statusResponse{
		Conflicts:      []system.Conflict{},
		Platform:       string(d.Kind),
		Arch:           d.Arch,
		PendingRestart: pending{Reasons: []string{}},
		// 后端自己:能回答这个请求就是在运行
		Panel: system.ServiceInfo{Running: true, Raw: "running"},
	}
	if d.Kind == system.InitNone || d.Services == nil {
		// 没有服务管理器:不是错误,界面按「没在跑」显示,并在 raw 里说明
		resp.Core = system.ServiceInfo{Raw: "unsupported"}
		httpx.JSON(w, http.StatusOK, resp)
		return
	}
	core, err := d.Services.Status(r.Context(), d.CoreService)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "查询 sing-box 服务状态失败: "+err.Error())
		return
	}
	resp.Core = core
	// 与其它代理服务冲突的检测:尽力而为,查不到就当没有,不让它拖垮状态接口
	if d.Conflicts != nil {
		if found, _ := d.Conflicts.Find(r.Context()); len(found) > 0 {
			resp.Conflicts = found
		}
	}
	// 后端自己的开机自启:查不到就不带这个字段
	if p, err := d.Services.Status(r.Context(), d.PanelName); err == nil {
		resp.Panel.Autostart = p.Autostart
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (d Deps) action(w http.ResponseWriter, r *http.Request) {
	httpx.NoStore(w)
	if d.Kind == system.InitNone || d.Services == nil {
		httpx.Error(w, http.StatusServiceUnavailable, system.ErrNoInit.Error())
		return
	}
	res, err := d.Services.Do(r.Context(), d.CoreService, r.PathValue("action"))
	if err != nil {
		switch {
		case errors.Is(err, system.ErrBadAction):
			httpx.Error(w, http.StatusBadRequest, "不支持的操作,可选 start / stop / restart / enable / disable")
		case errors.Is(err, system.ErrNoInit):
			httpx.Error(w, http.StatusServiceUnavailable, err.Error())
		default:
			httpx.Error(w, http.StatusInternalServerError, "服务操作失败: "+err.Error())
		}
		return
	}
	// 命令本身失败(退出码非 0)仍是 200:前端按 ok / stderr 显示
	httpx.JSON(w, http.StatusOK, res)
}
