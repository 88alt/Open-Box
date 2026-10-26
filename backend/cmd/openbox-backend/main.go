// openbox-backend 是 Open-Box 面板的自研后端。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/timeflysoon/Open-Box/backend/internal/server"
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

	srv := server.New(server.Options{Logger: log, Version: version})
	// 前端静态文件与前端路由回落;必须最后挂,/api 下的路径由各接口分组优先处理
	srv.Mux().Handle("/", server.StaticHandler(cfg.WebDir))

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Error("监听失败", "addr", cfg.Listen, "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("openbox-backend 启动", "version", version, "listen", ln.Addr().String(), "data", cfg.DataDir, "web", cfg.WebDir)
	if err := srv.Serve(ctx, ln); err != nil {
		log.Error("服务异常退出", "err", err)
		return 1
	}
	log.Info("已退出")
	return 0
}
