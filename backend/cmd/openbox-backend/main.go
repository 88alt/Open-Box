// openbox-backend 是 Open-Box 面板的自研后端。
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/timeflysoon/Open-Box/backend/internal/system"
)

// version 由编译时 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 1 && (args[0] == "-version" || args[0] == "--version") {
		fmt.Println("openbox-backend", version)
		return 0
	}
	cfg, err := system.LoadConfig(args, os.Getenv, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "参数错误:", err)
		return 2
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	if err := cfg.Paths.Ensure(); err != nil {
		log.Error("初始化目录失败", "err", err)
		return 1
	}
	log.Info("openbox-backend 启动", "version", version, "listen", cfg.Listen, "data", cfg.DataDir, "web", cfg.WebDir)
	return 0
}
