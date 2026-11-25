package update

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/timeflysoon/Open-Box/backend/internal/release"
)

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

// Status 对应前端 OpenboxUpdateStatus。
// geoVersion / geoDate / geoCounts 读随包规则库的清单(geodata.json);没有随包规则库时不给这三个字段。
type Status struct {
	Version        string     `json:"version"`
	SingboxVersion string     `json:"singboxVersion"`
	BuiltAt        string     `json:"builtAt"`
	GeoVersion     string     `json:"geoVersion,omitempty"`
	GeoDate        string     `json:"geoDate,omitempty"`
	GeoCounts      *GeoCounts `json:"geoCounts,omitempty"`
	Channel        Channel    `json:"channel"`
	Status         Progress   `json:"status"`
	LogTail        string     `json:"logTail"`
}

// GeoCounts 是规则库的分类文件个数,与前端 OpenboxUpdateStatus.geoCounts 一致。
type GeoCounts struct {
	Geosite int `json:"geosite"`
	Geoip   int `json:"geoip"`
}

// Service 把版本信息、检查器和升级进度合在一起。
type Service struct {
	Checker *Checker
	// Version / BuiltAt 是本程序的版本和构建时间(编译时注入,没有就是 "dev" / "")。
	Version, BuiltAt string
	// KernelVersion 读 sing-box 内核版本,读不到返回空串;为空时 singboxVersion 是空串。
	KernelVersion func(ctx context.Context) string

	// DataDir 是数据目录;升级的暂存放 <DataDir>/update/<版本>(与安装目标同一个文件系统,提交时改名就是原子的)。
	DataDir string
	// GeoDir 是规则库目录,读里面的 geodata.json 填 geoVersion / geoDate / geoCounts。
	GeoDir string
	// ConfigFile 是当前已部署的 sing-box 配置,冒烟时用新内核 check 它;为空或不存在就跳过这一步。
	ConfigFile string
	// Client 是下载用的 HTTP 客户端,为空时用不设整体超时的默认客户端。
	Client *http.Client
	// Smoke 是冒烟检查,为空时用 SmokeTest(跑暂存目录里的新二进制)。
	Smoke func(ctx context.Context, root string) error
	// FreeSpace 报目录所在文件系统剩余字节数,为空时用 FreeSpace。
	FreeSpace func(dir string) (int64, error)

	// 提交(M6-6)。InstallRoot 为空时**不做提交**——开发机、容器、没接线的环境里
	// 升级跑到冒烟为止就停(「本次没有改动系统」),不会去换文件。
	InstallRoot string
	// StateDir 是状态目录(<data-dir>/state),提交前打快照、回滚时恢复。
	StateDir string
	// Restart 请服务管理器重启后端。重启后本进程会退出,由新进程接着自检。
	Restart func(ctx context.Context) error
	// RestartCommand 写进看门狗脚本,看门狗回滚后用它把旧版本拉起来。
	RestartCommand string
	// StartWatchdog 拉起看门狗,为空时用 StartWatchdog。
	StartWatchdog func(req WatchdogRequest) (string, error)

	// mu 保护 j:Run / Cancel / Status 会并发进来。
	mu sync.Mutex
	j  *job
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
	st.GeoVersion, st.GeoDate, st.GeoCounts = s.geo()
	s.mu.Lock()
	j := s.j
	s.mu.Unlock()
	if j != nil {
		st.Status, st.LogTail = j.snapshot()
	}
	return st
}

// geo 读随包规则库的清单。没有 geodata.json 时退回数目录里的 .srs 文件(版本与日期就给不出来)。
func (s *Service) geo() (version, date string, counts *GeoCounts) {
	if s.GeoDir == "" {
		return "", "", nil
	}
	if f, err := os.Open(filepath.Join(s.GeoDir, release.GeoDataName)); err == nil {
		defer f.Close()
		if g, err := release.ParseGeoData(f); err == nil {
			c := GeoCounts{Geosite: g.Counts.Geosite, Geoip: g.Counts.Geoip}
			return g.Version, g.Date, &c
		}
	}
	// 数文件兜底:规则库是 <分类名>.srs,前缀决定它算 geosite 还是 geoip
	entries, err := os.ReadDir(s.GeoDir)
	if err != nil {
		return "", "", nil
	}
	var c GeoCounts
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".srs") {
			continue
		}
		switch {
		case strings.HasPrefix(e.Name(), "geosite-"):
			c.Geosite++
		case strings.HasPrefix(e.Name(), "geoip-"):
			c.Geoip++
		}
	}
	if c.Geosite == 0 && c.Geoip == 0 {
		return "", "", nil
	}
	return "", "", &c
}

// Check 联网看有没有新版。
func (s *Service) Check(ctx context.Context) (*CheckResult, error) {
	return s.Checker.Check(ctx, s.Version)
}

// Notes 取更新说明(永远有结果,取不到时 Note 为 nil)。
func (s *Service) Notes(ctx context.Context, latest string) Notes {
	return s.Checker.Notes(ctx, latest)
}
