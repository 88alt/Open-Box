package system

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(nil, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "0.0.0.0:3036" || cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("默认值不对: %+v", cfg)
	}
	if cfg.CoreService != DefaultCoreService || cfg.PanelService != DefaultPanelService {
		t.Fatalf("默认服务名不对: %+v", cfg)
	}
	if cfg.DNSQueryLog != filepath.Clean(DefaultDNSQueryLog) {
		t.Fatalf("查询日志默认路径不对: %q", cfg.DNSQueryLog)
	}
	if cfg.Paths != func() Paths {
		p := DefaultPaths()
		// LoadConfig 对每个路径都做 filepath.Clean(Windows 上 / 会变成 \),期望值也按平台清理
		for _, f := range []*string{&p.DataDir, &p.ConfigDir, &p.WebDir, &p.GeoDir, &p.SingBox, &p.Core, &p.DNSQueryLog} {
			*f = filepath.Clean(*f)
		}
		return p
	}() {
		t.Fatalf("默认路径不对: %+v", cfg.Paths)
	}
}

func TestLoadConfigPrecedence(t *testing.T) {
	env := map[string]string{EnvListen: "127.0.0.1:1111", EnvDataDir: "/env/data", EnvLogLevel: "debug"}
	getenv := func(k string) string { return env[k] }

	cfg, err := LoadConfig(nil, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:1111" || cfg.DataDir != filepath.Clean("/env/data") || cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("环境变量没生效: %+v", cfg)
	}

	cfg, err = LoadConfig([]string{"-listen", ":2222", "-data-dir", "/flag/data"}, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":2222" || cfg.DataDir != filepath.Clean("/flag/data") {
		t.Fatalf("命令行参数应优先于环境变量: %+v", cfg)
	}
	cfg, err = LoadConfig([]string{"-core-service", "my-singbox"}, func(k string) string {
		if k == EnvPanelService {
			return "my-panel"
		}
		return ""
	}, io.Discard)
	if err != nil || cfg.CoreService != "my-singbox" || cfg.PanelService != "my-panel" {
		t.Fatalf("服务名参数 / 环境变量没生效: %+v %v", cfg, err)
	}
	if cfg.StateDir() != filepath.Join(cfg.DataDir, "state") {
		t.Fatalf("StateDir 不对: %s", cfg.StateDir())
	}
}

func TestLoadConfigGeoDir(t *testing.T) {
	cfg, err := LoadConfig(nil, noEnv, io.Discard)
	if err != nil || cfg.GeoDir != filepath.Clean(DefaultPaths().GeoDir) {
		t.Fatalf("默认规则库目录不对: %q %v", cfg.GeoDir, err)
	}
	fromEnv := func(k string) string {
		if k == EnvGeoDir {
			return "/env/geo"
		}
		return ""
	}
	cfg, err = LoadConfig(nil, fromEnv, io.Discard)
	if err != nil || cfg.GeoDir != filepath.Clean("/env/geo") {
		t.Fatalf("环境变量没生效: %q %v", cfg.GeoDir, err)
	}
	cfg, err = LoadConfig([]string{"-geo-dir", "/flag/geo"}, fromEnv, io.Discard)
	if err != nil || cfg.GeoDir != filepath.Clean("/flag/geo") {
		t.Fatalf("命令行参数应优先于环境变量: %q %v", cfg.GeoDir, err)
	}
	if _, err := LoadConfig([]string{"-geo-dir", " "}, noEnv, io.Discard); err == nil {
		t.Fatal("规则库目录为空应报错")
	}
	// 规则库是只读目录,由安装包放入,Ensure 不创建它
	root := t.TempDir()
	p := Paths{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "conf"), GeoDir: filepath.Join(root, "geo")}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.GeoDir); !os.IsNotExist(err) {
		t.Fatalf("Ensure 不该创建规则库目录: %v", err)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	cases := map[string][]string{
		"监听地址缺端口":     {"-listen", "localhost"},
		"端口不是数字":      {"-listen", "localhost:abc"},
		"端口超范围":       {"-listen", "localhost:70000"},
		"日志级别无效":      {"-log-level", "loud"},
		"多余参数":        {"extra"},
		"路径为空":        {"-web-dir", " "},
		"未知参数":        {"-nope"},
		"sing-box 为空": {"-sing-box", ""},
		"服务名带空格":      {"-core-service", "a b"},
		"服务名以-开头":     {"-core-service=-rf"},
		"服务名为空":       {"-panel-service", ""},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(args, noEnv, io.Discard); err == nil {
				t.Fatalf("应当报错: %v", args)
			}
		})
	}
}

func TestEnsureCreatesPrivateDirs(t *testing.T) {
	root := t.TempDir()
	p := Paths{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "conf")}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p.DataDir, p.StateDir(), p.ConfigDir} {
		if !strings.HasPrefix(d, root) {
			t.Fatal(d)
		}
	}
	if err := p.Ensure(); err != nil { // 重复调用不报错
		t.Fatal(err)
	}
}

func TestLoadConfigDNSQueryLog(t *testing.T) {
	cfg, err := LoadConfig(nil, noEnv, io.Discard)
	if err != nil || cfg.DNSQueryLog != filepath.Clean(DefaultDNSQueryLog) {
		t.Fatalf("默认用内存盘上的查询日志: %q %v", cfg.DNSQueryLog, err)
	}
	cfg, err = LoadConfig(nil, func(k string) string {
		if k == EnvDNSQueryLog {
			return " /tmp/../tmp/dnsmasq.log "
		}
		return ""
	}, io.Discard)
	if err != nil || cfg.DNSQueryLog != filepath.Clean("/tmp/dnsmasq.log") {
		t.Fatalf("环境变量: %q %v", cfg.DNSQueryLog, err)
	}
	cfg, err = LoadConfig([]string{"-dns-query-log", "/var/log/dnsmasq.log"}, func(k string) string {
		if k == EnvDNSQueryLog {
			return "/ignored"
		}
		return ""
	}, io.Discard)
	if err != nil || cfg.DNSQueryLog != filepath.Clean("/var/log/dnsmasq.log") {
		t.Fatalf("命令行优先: %q %v", cfg.DNSQueryLog, err)
	}
	// off 关掉(命令行和环境变量都认,大小写不论);这时既不让 dnsmasq 写也不读
	for _, c := range []struct {
		args []string
		env  string
	}{{[]string{"-dns-query-log", "off"}, ""}, {nil, " OFF "}} {
		cfg, err = LoadConfig(c.args, func(k string) string {
			if k == EnvDNSQueryLog {
				return c.env
			}
			return ""
		}, io.Discard)
		if err != nil || cfg.DNSQueryLog != "" {
			t.Fatalf("off 没关掉: %q %v", cfg.DNSQueryLog, err)
		}
	}
}
