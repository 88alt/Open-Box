package update

import (
	"context"
	"errors"
	"strings"
)

// ErrNotReady 表示「下载并替换」这一步还没有实现:它要等 M6 定下发布包的格式和安装目录。
var ErrNotReady = errors.New("在线升级还没有实现:发布包的格式和安装目录要等打包(M6)定下来;现在可以检查有没有新版,升级请手动下载发布包替换")

// Channel 是安装通道:direct = GitHub 直连,mirror = 镜像加速(Prefix 是镜像前缀)。
type Channel struct {
	Mode   string `json:"mode"`
	Prefix string `json:"prefix"`
}

// Progress 对应前端 OpenboxUpdateProgress。没有在升级时 stage 为 "idle"、running 为 false。
type Progress struct {
	Stage   string  `json:"stage"`
	Pid     *string `json:"pid,omitempty"`
	Bytes   *int64  `json:"bytes"`
	Total   *int64  `json:"total"`
	Message string  `json:"message"`
	Running bool    `json:"running"`
}

// Status 对应前端 OpenboxUpdateStatus。geoVersion / geoDate / geoCounts 是可选字段,要等打包定下规则库清单文件的格式才有来源,
// 现在不给。
type Status struct {
	Version        string   `json:"version"`
	SingboxVersion string   `json:"singboxVersion"`
	BuiltAt        string   `json:"builtAt"`
	Channel        Channel  `json:"channel"`
	Status         Progress `json:"status"`
	LogTail        string   `json:"logTail"`
}

// Service 把版本信息、检查器和升级进度合在一起。
type Service struct {
	Checker *Checker
	// Version / BuiltAt 是本程序的版本和构建时间(编译时注入,没有就是 "dev" / "")。
	Version, BuiltAt string
	// KernelVersion 读 sing-box 内核版本,读不到返回空串;为空时 singboxVersion 是空串。
	KernelVersion func(ctx context.Context) string
}

func (s *Service) channel() Channel {
	if p := strings.TrimSpace(s.Checker.MirrorPrefix); p != "" {
		return Channel{Mode: "mirror", Prefix: p}
	}
	return Channel{Mode: "direct", Prefix: ""}
}

// Status 返回当前状态。只读本进程已知的东西,不联网。
func (s *Service) Status(ctx context.Context) Status {
	st := Status{Version: s.Version, BuiltAt: s.BuiltAt, Channel: s.channel(), Status: Progress{Stage: "idle"}}
	if s.KernelVersion != nil {
		st.SingboxVersion = s.KernelVersion(ctx)
	}
	return st
}

// Check 联网看有没有新版。
func (s *Service) Check(ctx context.Context) (*CheckResult, error) {
	return s.Checker.Check(ctx, s.Version)
}

// Notes 取更新说明(永远有结果,取不到时 Note 为 nil)。
func (s *Service) Notes(ctx context.Context, latest string) Notes {
	return s.Checker.Notes(ctx, latest)
}

// Run 开始升级。channel 取 auto / direct / mirror;目前一律返回 ErrNotReady。
func (s *Service) Run(channel string) error {
	switch channel {
	case "auto", "direct", "mirror":
		return ErrNotReady
	}
	return errors.New("通道只能是 auto、direct 或 mirror")
}

// Cancel 取消正在进行的升级。现在不会有在跑的升级,恒返回 "not-running"。
func (s *Service) Cancel() string { return "not-running" }
