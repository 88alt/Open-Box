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
	EnvSingBox   = "OPENBOX_SINGBOX"
	EnvCore      = "OPENBOX_CORE"
)

// Paths 是后端用到的全部外部路径。M6 打包时再和安装脚本对齐默认值。
type Paths struct {
	DataDir   string // 后端自己的数据(密码哈希、订阅、设置……)
	ConfigDir string // 生成给 sing-box 的配置
	WebDir    string // 前端构建产物(panel/dist)
	SingBox   string // sing-box 可执行文件(可以只写名字,从 PATH 找)
	Core      string // openbox-core 可执行文件
}

// DefaultPaths 返回通用位置的默认值。
func DefaultPaths() Paths {
	return Paths{
		DataDir:   "/var/lib/openbox-backend",
		ConfigDir: "/etc/openbox-backend",
		WebDir:    "/usr/share/openbox-backend/panel",
		SingBox:   "sing-box",
		Core:      "openbox-core",
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
	singBox := fs.String("sing-box", env(EnvSingBox, def.SingBox), "sing-box 可执行文件(环境变量 "+EnvSingBox+")")
	core := fs.String("core", env(EnvCore, def.Core), "openbox-core 可执行文件(环境变量 "+EnvCore+")")
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
	cfg := Config{
		Listen:   strings.TrimSpace(*listen),
		LogLevel: level,
		Paths: Paths{
			DataDir:   filepath.Clean(strings.TrimSpace(*dataDir)),
			ConfigDir: filepath.Clean(strings.TrimSpace(*configDir)),
			WebDir:    filepath.Clean(strings.TrimSpace(*webDir)),
			SingBox:   strings.TrimSpace(*singBox),
			Core:      strings.TrimSpace(*core),
		},
	}
	for name, v := range map[string]string{"data-dir": *dataDir, "config-dir": *configDir, "web-dir": *webDir, "sing-box": *singBox, "core": *core} {
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
