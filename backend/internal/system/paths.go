// Package system 集中放「和这台机器有关」的东西:路径、参数、环境变量,
// 以及之后所有会改动系统的操作(DNS 接管、防火墙、服务控制等)。
package system

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultListen 是默认监听地址(与现有联调使用的端口一致)。
const DefaultListen = "0.0.0.0:3036"

// 环境变量名。命令行参数优先于环境变量,环境变量优先于默认值。
const (
	EnvListen    = "OPENBOX_LISTEN"
	EnvLogLevel  = "OPENBOX_LOG_LEVEL"
	EnvDataDir   = "OPENBOX_DATA_DIR"
	EnvConfigDir = "OPENBOX_CONFIG_DIR"
	EnvWebDir    = "OPENBOX_WEB_DIR"
	EnvGeoDir    = "OPENBOX_GEO_DIR"
	EnvSingBox   = "OPENBOX_SINGBOX"
	EnvCore      = "OPENBOX_CORE"
	// EnvDNSQueryLog 是 dnsmasq 查询日志文件的路径,默认 DefaultDNSQueryLog;写 off 就既不让 dnsmasq 写、后端也不读
	// (DNS 过滤的「查询记录」没有数据)。
	EnvDNSQueryLog = "OPENBOX_DNS_QUERY_LOG"

	// 服务名:交给系统服务管理器(OpenWrt init.d / systemd)的名字
	EnvCoreService  = "OPENBOX_CORE_SERVICE"
	EnvPanelService = "OPENBOX_PANEL_SERVICE"
)

// DefaultDNSQueryLog 是 dnsmasq 查询日志的默认位置:/var/run 在 OpenWrt(指向 /tmp)和 Debian(指向 /run)上都是内存盘,
// 查询日志写得频繁,不能落在路由器的闪存或 SD 卡上。位置依据 OpenWrt 和 Debian 的目录约定,没有在真机上见过,需真机确认(见 STATUS.md)。
const DefaultDNSQueryLog = "/var/run/openbox-dnsmasq.log"

// 服务名默认值。M6 打包时再和安装脚本对齐。
const (
	DefaultCoreService  = "sing-box"
	DefaultPanelService = "openbox-backend"
)

// Paths 是后端用到的全部外部路径。M6 打包时再和安装脚本对齐默认值。
type Paths struct {
	DataDir   string // 后端自己的数据(密码哈希、订阅、设置……)
	ConfigDir string // 生成给 sing-box 的配置
	WebDir    string // 前端构建产物(panel/dist)
	GeoDir    string // 随包规则库(只读):<分类名>.srs,如 geosite-cn.srs、geoip-cn.srs
	SingBox   string // sing-box 可执行文件(可以只写名字,从 PATH 找)
	Core      string // openbox-core 可执行文件
	// DNSQueryLog 是 dnsmasq 查询日志(log-queries 的输出)的文件路径:DNS 接管时让 dnsmasq 写到这里(system.DNSTakeover.QueryLog),
	// 后端跟读它;空表示不开、不读。
	DNSQueryLog string
}

// DefaultPaths 返回通用位置的默认值。
func DefaultPaths() Paths {
	return Paths{
		DataDir:     "/var/lib/openbox-backend",
		ConfigDir:   "/etc/openbox-backend",
		WebDir:      "/usr/share/openbox-backend/panel",
		GeoDir:      "/usr/share/openbox-backend/geodata",
		SingBox:     "sing-box",
		Core:        "openbox-core",
		DNSQueryLog: DefaultDNSQueryLog,
	}
}

// StateDir 是持久化存储(internal/store)使用的目录。
func (p Paths) StateDir() string { return filepath.Join(p.DataDir, "state") }

// Ensure 创建需要写入的目录,权限仅 root 可访问(里面有密码哈希、订阅地址)。
func (p Paths) Ensure() error {
	for _, dir := range []string{p.DataDir, p.StateDir(), p.ConfigDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	return nil
}

// Config 是进程的完整启动配置。
type Config struct {
	Listen   string
	LogLevel slog.Level
	Paths
	CoreService  string // sing-box 的服务名
	PanelService string // 后端自己的服务名(只用来查开机自启)
}

// LoadConfig 解析命令行参数和环境变量。getenv 传入 os.Getenv;usage 输出写到 out。
func LoadConfig(args []string, getenv func(string) string, out io.Writer) (Config, error) {
	def := DefaultPaths()
	env := func(key, fallback string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return fallback
	}

	fs := flag.NewFlagSet("openbox-backend", flag.ContinueOnError)
	fs.SetOutput(out)
	listen := fs.String("listen", env(EnvListen, DefaultListen), "监听地址 host:port(环境变量 "+EnvListen+")")
	logLevel := fs.String("log-level", env(EnvLogLevel, "info"), "日志级别 debug|info|warn|error(环境变量 "+EnvLogLevel+")")
	dataDir := fs.String("data-dir", env(EnvDataDir, def.DataDir), "数据目录(环境变量 "+EnvDataDir+")")
	configDir := fs.String("config-dir", env(EnvConfigDir, def.ConfigDir), "sing-box 配置目录(环境变量 "+EnvConfigDir+")")
	webDir := fs.String("web-dir", env(EnvWebDir, def.WebDir), "前端文件目录,即 panel/dist(环境变量 "+EnvWebDir+")")
	geoDir := fs.String("geo-dir", env(EnvGeoDir, def.GeoDir), "随包规则库目录,里面是 geosite-*.srs / geoip-*.srs(环境变量 "+EnvGeoDir+")")
	singBox := fs.String("sing-box", env(EnvSingBox, def.SingBox), "sing-box 可执行文件(环境变量 "+EnvSingBox+")")
	core := fs.String("core", env(EnvCore, def.Core), "openbox-core 可执行文件(环境变量 "+EnvCore+")")
	dnsQueryLog := fs.String("dns-query-log", env(EnvDNSQueryLog, def.DNSQueryLog), "dnsmasq 查询日志文件:DNS 接管时让 dnsmasq 写到这里,后端跟读;off = 不开不读(环境变量 "+EnvDNSQueryLog+")")
	coreSvc := fs.String("core-service", env(EnvCoreService, DefaultCoreService), "sing-box 的服务名(环境变量 "+EnvCoreService+")")
	panelSvc := fs.String("panel-service", env(EnvPanelService, DefaultPanelService), "后端自己的服务名(环境变量 "+EnvPanelService+")")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("多余的参数: %s", strings.Join(fs.Args(), " "))
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.TrimSpace(*logLevel))); err != nil {
		return Config{}, fmt.Errorf("日志级别 %q 无效,可选 debug|info|warn|error", *logLevel)
	}
	if err := validateListen(*listen); err != nil {
		return Config{}, err
	}
	for name, v := range map[string]string{"core-service": *coreSvc, "panel-service": *panelSvc} {
		if err := ValidServiceName(strings.TrimSpace(v)); err != nil {
			return Config{}, fmt.Errorf("参数 %s 无效: %w", name, err)
		}
	}
	cfg := Config{
		Listen:       strings.TrimSpace(*listen),
		LogLevel:     level,
		CoreService:  strings.TrimSpace(*coreSvc),
		PanelService: strings.TrimSpace(*panelSvc),
		Paths: Paths{
			DataDir:   filepath.Clean(strings.TrimSpace(*dataDir)),
			ConfigDir: filepath.Clean(strings.TrimSpace(*configDir)),
			WebDir:    filepath.Clean(strings.TrimSpace(*webDir)),
			GeoDir:    filepath.Clean(strings.TrimSpace(*geoDir)),
			SingBox:   strings.TrimSpace(*singBox),
			Core:      strings.TrimSpace(*core),
		},
	}
	if v := strings.TrimSpace(*dnsQueryLog); v != "" && !strings.EqualFold(v, "off") {
		cfg.DNSQueryLog = filepath.Clean(v)
	}
	for name, v := range map[string]string{"data-dir": *dataDir, "config-dir": *configDir, "web-dir": *webDir, "geo-dir": *geoDir, "sing-box": *singBox, "core": *core} {
		if strings.TrimSpace(v) == "" {
			return Config{}, errors.New("参数 " + name + " 不能为空")
		}
	}
	return cfg, nil
}

func validateListen(addr string) error {
	_, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("监听地址 %q 无效,格式应为 host:port: %w", addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("监听地址 %q 的端口无效", addr)
	}
	return nil
}
