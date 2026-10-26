package system

import (
	"io"
	"log/slog"
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
	if cfg.Paths != func() Paths { p := DefaultPaths(); p.DataDir = filepath.Clean(p.DataDir); return p }() {
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
	if cfg.StateDir() != filepath.Join(cfg.DataDir, "state") {
		t.Fatalf("StateDir 不对: %s", cfg.StateDir())
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
