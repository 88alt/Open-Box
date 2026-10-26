// openbox-backend 是 Open-Box 面板的自研后端。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	authapi "github.com/timeflysoon/Open-Box/backend/internal/api/auth"
	configpreviewapi "github.com/timeflysoon/Open-Box/backend/internal/api/configpreview"
	groupsapi "github.com/timeflysoon/Open-Box/backend/internal/api/groups"
	latencyapi "github.com/timeflysoon/Open-Box/backend/internal/api/latency"
	profileapi "github.com/timeflysoon/Open-Box/backend/internal/api/profile"
	rulesetsapi "github.com/timeflysoon/Open-Box/backend/internal/api/rulesets"
	storageapi "github.com/timeflysoon/Open-Box/backend/internal/api/storage"
	subsapi "github.com/timeflysoon/Open-Box/backend/internal/api/subscriptions"
	"github.com/timeflysoon/Open-Box/backend/internal/auth"
	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
	"github.com/timeflysoon/Open-Box/backend/internal/server"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
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

	checkCore(cfg.Core, log)

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

	// 核心不可用时档案接口照常工作,只是旧版分流迁移和默认节点组不可用
	var coreAPI profile.Core
	var groupsCore nodegroups.Core
	if core.Available(cfg.Core) {
		cc := core.New(cfg.Core)
		coreAPI = profile.CoreAdapter{C: cc}
		groupsCore = nodegroups.CoreAdapter{C: cc}
	}
	profileSvc := profile.New(st, coreAPI, log)
	subsSvc := subs.New(st, nil)
	profileapi.Register(srv.Mux(), profileapi.Deps{Profile: profileSvc})
	subsapi.Register(srv.Mux(), subsapi.Deps{Subs: subsSvc})
	groupsSvc := nodegroups.New(st, groupsCore, subsSvc, nodegroups.ChainsFromProfile(profileSvc))
	groupsapi.Register(srv.Mux(), groupsapi.Deps{Groups: groupsSvc})
	var genCore singbox.Caller
	if core.Available(cfg.Core) {
		genCore = core.New(cfg.Core)
	}
	gen := singbox.NewGenerator(genCore, st, profileSvc, groupsSvc, subsSvc,
		singbox.System{Resolvers: func() []string { return system.ReadResolvers("/etc/resolv.conf") }, LocalSubnets: system.LocalSubnets},
		singbox.Options{
			RulesetDir: filepath.Join(cfg.DataDir, "rules"),
			CacheFile:  filepath.Join(cfg.DataDir, "cache.db"),
			CertPath:   filepath.Join(cfg.ConfigDir, "certs", "server.crt"),
			KeyPath:    filepath.Join(cfg.ConfigDir, "certs", "server.key"),
		})
	configpreviewapi.Register(srv.Mux(), configpreviewapi.Deps{Gen: gen})

	// 节点测速:临时配置放在数据目录下的 tmp(0700),用完即删
	probeDir := filepath.Join(cfg.DataDir, "tmp")
	if err := os.MkdirAll(probeDir, 0o700); err != nil {
		log.Warn("创建测速临时目录失败,节点测速不可用", "dir", probeDir, "error", err)
	}
	latencyapi.Register(srv.Mux(), latencyapi.Deps{Gen: gen, Subs: subsSvc, Profiles: profileSvc,
		Runner: &probe.Runner{Bin: cfg.SingBox, Dir: probeDir}})

	rulesetsapi.Register(srv.Mux(), rulesetsapi.Deps{Lists: rulelist.NewService(nil)})

	// 前端静态文件与前端路由回落;最后挂,/api 下的路径由各接口分组优先处理
	srv.Mux().Handle("/", server.StaticHandler(cfg.WebDir))
	return srv, nil
}

// checkCore 启动时握手一次,只记日志:核心不可用时面板的其它功能(登录、设置)仍可用,
// 需要核心的接口(M1 起)会在调用时返回明确错误。
func checkCore(path string, log *slog.Logger) {
	if !core.Available(path) {
		log.Warn("找不到 openbox-core,生成配置等功能将不可用", "path", path)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := core.New(path).Hello(ctx)
	if err != nil {
		log.Warn("openbox-core 握手失败", "path", path, "err", err)
		return
	}
	log.Info("openbox-core 可用", "path", path, "implementation", info.Implementation, "protocol", info.Protocol, "build", info.BuildID)
}
