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

	authapi "github.com/timeflysoon/Open-Box/backend/internal/api/auth"
	storageapi "github.com/timeflysoon/Open-Box/backend/internal/api/storage"
	"github.com/timeflysoon/Open-Box/backend/internal/auth"
	"github.com/timeflysoon/Open-Box/backend/internal/server"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
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

	srv, err := newApp(cfg, log)
	if err != nil {
		log.Error("初始化失败", "err", err)
		return 1
	}

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

// newApp 组装存储、各接口分组和中间件。
func newApp(cfg system.Config, log *slog.Logger) (*server.Server, error) {
	st, err := store.Open(cfg.StateDir())
	if err != nil {
		return nil, err
	}
	authMgr, err := auth.NewManager(st)
	if err != nil {
		return nil, err
	}
	if !authMgr.PasswordSet() {
		log.Warn("尚未设置访问密码:任何能访问本端口的人都可以设置,请尽快在面板里设置")
	}

	srv := server.New(server.Options{
		Logger:     log,
		Version:    version,
		Middleware: []server.Middleware{authapi.Require(authMgr)},
	})
	authapi.Register(srv.Mux(), authapi.Deps{Auth: authMgr, Log: log})
	storageapi.Register(srv.Mux(), storageapi.Deps{Store: st})

	// 前端静态文件与前端路由回落;最后挂,/api 下的路径由各接口分组优先处理
	srv.Mux().Handle("/", server.StaticHandler(cfg.WebDir))
	return srv, nil
}
