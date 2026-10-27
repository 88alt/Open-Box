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
	dnsapi "github.com/timeflysoon/Open-Box/backend/internal/api/dns"
	dnsfilterapi "github.com/timeflysoon/Open-Box/backend/internal/api/dnsfilter"
	groupsapi "github.com/timeflysoon/Open-Box/backend/internal/api/groups"
	latencyapi "github.com/timeflysoon/Open-Box/backend/internal/api/latency"
	policiesapi "github.com/timeflysoon/Open-Box/backend/internal/api/policies"
	profileapi "github.com/timeflysoon/Open-Box/backend/internal/api/profile"
	rulesetsapi "github.com/timeflysoon/Open-Box/backend/internal/api/rulesets"
	serviceapi "github.com/timeflysoon/Open-Box/backend/internal/api/service"
	storageapi "github.com/timeflysoon/Open-Box/backend/internal/api/storage"
	subsapi "github.com/timeflysoon/Open-Box/backend/internal/api/subscriptions"
	"github.com/timeflysoon/Open-Box/backend/internal/auth"
	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/dnscache"
	"github.com/timeflysoon/Open-Box/backend/internal/dnsfilter"
	"github.com/timeflysoon/Open-Box/backend/internal/dnstest"
	"github.com/timeflysoon/Open-Box/backend/internal/geodata"
	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/regiondetect"
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

	// 服务控制:只通过系统自带的服务管理器(OpenWrt init.d / systemd)操作 sing-box
	initKind := system.DetectInitHere()
	svcCtl := &system.Services{Kind: initKind, Run: system.ExecRunner, ProcUptime: system.ReadProcUptime}
	serviceapi.Register(srv.Mux(), serviceapi.Deps{
		Services: svcCtl,
		Conflicts: &system.ConflictDetector{
			Svc: svcCtl, Exclude: []string{cfg.CoreService, cfg.PanelService},
			Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		},
		Kind:        initKind,
		CoreService: cfg.CoreService, PanelName: cfg.PanelService, SingBox: cfg.SingBox,
	})

	// 节点测速和 DNS 上游测试共用的临时目录:临时配置放在数据目录下的 tmp(0700),用完即删
	probeDir := filepath.Join(cfg.DataDir, "tmp")
	if err := os.MkdirAll(probeDir, 0o700); err != nil {
		log.Warn("创建测速临时目录失败,节点测速、DNS 上游测试不可用", "dir", probeDir, "error", err)
	}
	probeRunner := &probe.Runner{Bin: cfg.SingBox, Dir: probeDir}

	// 档案里的 DNS 地区;读不到按 cn(与前端一致)
	dnsRegion := func(ctx context.Context) string {
		p, err := profileSvc.Get(ctx)
		if err != nil {
			return "cn"
		}
		if d, ok := p["dns"].(map[string]any); ok {
			if r, _ := d["region"].(string); r != "" {
				return r
			}
		}
		return "cn"
	}
	wanDNS := func() []string { return system.ReadWANDNS(system.WANResolverFiles) }

	// 上游 DNS(只读):从 netifd / systemd-resolved 记录的文件里读,不碰任何系统设置
	dnsapi.Register(srv.Mux(), dnsapi.Deps{
		WAN:      wanDNS,
		Region:   dnsRegion,
		Detector: &regiondetect.Detector{},
		// 上游测试:临时 sing-box 让内核真查一次;代理侧经生成配置里的兜底出站
		Test: (&dnstest.Tester{
			Query:   probeRunner,
			WAN:     wanDNS,
			Region:  dnsRegion,
			Profile: profileSvc.Get,
			Config: func(ctx context.Context) (map[string]any, error) {
				b, err := gen.Build(ctx)
				if err != nil {
					return nil, err
				}
				return b.Config, nil
			},
		}).Test,
		// 清 DNS 缓存:内核(读正式配置里的控制接口)、dnsmasq(给进程发 SIGHUP)、systemd-resolved
		Flush: dnscache.Layers{
			Kernel: func(ctx context.Context) error {
				return singbox.KernelFlushDNS(ctx, filepath.Join(cfg.ConfigDir, "config.json"))
			},
			Dnsmasq:  func(context.Context) bool { return system.FlushDnsmasq("/proc", system.SignalHUP) },
			Resolved: func(ctx context.Context) bool { return system.FlushResolved(ctx, system.ExecRunner) },
			Log:      log,
		}.Flush,
	})

	latencyapi.Register(srv.Mux(), latencyapi.Deps{Gen: gen, Subs: subsSvc, Profiles: profileSvc, Runner: probeRunner})

	// 规则集链接服务共用一份(含 60 秒内存缓存):规则集预览和「域名穿透」都会拉同一批链接
	lists := rulelist.NewService(nil)
	// 随包规则库(只读):按分类名查条目时交给 sing-box 解码;临时文件放在上面建好的 tmp 里
	geoLib := geodata.New(cfg.GeoDir, cfg.SingBox, probeDir)
	// 规则集链接「立即更新」:只对一个链接重拉重编 .srs(目录与配置生成用的规则集目录一致),不重启、不部署
	refresher := &singbox.Refresher{
		Lists: singbox.ListFiles{SingBox: cfg.SingBox, Dir: filepath.Join(cfg.DataDir, "rules")},
		Fetch: lists, URLs: gen,
	}
	rulesetsapi.Register(srv.Mux(), rulesetsapi.Deps{Lists: lists, Geo: geoLib, Refresh: refresher})

	// 域名穿透:站点集命中哪些域名 / IP。核心不可用时接口返回 503,其它接口不受影响
	var peCore policyentries.Core
	if core.Available(cfg.Core) {
		peCore = policyentries.CoreAdapter{C: core.New(cfg.Core)}
	}
	policiesapi.Register(srv.Mux(), policiesapi.Deps{Entries: policyentries.New(profileSvc, peCore, lists).WithGeo(geoLib)})

	// DNS 域名过滤:设置读写、名单预览、下载名单并应用(应用 = 编规则集 + 走一次完整部署:校验 → 原子替换 → 重启 → 健康检查 → 失败回滚)。
	// 内核没在运行时只更新名单、不部署,不会为了应用名单把代理启动起来。
	rulesDir := filepath.Join(cfg.DataDir, "rules")
	fetchList := func(ctx context.Context, u string) ([]byte, error) { return rulelist.Fetch(ctx, nil, u) }
	deployer := &singbox.Deployer{
		Build:   singbox.ListedBuilder{Gen: gen, Lists: singbox.ListFiles{Rules: lists, SingBox: cfg.SingBox, Dir: rulesDir}},
		Files:   singbox.Files{SingBox: cfg.SingBox, GeoDir: cfg.GeoDir},
		SingBox: cfg.SingBox, ConfigDir: cfg.ConfigDir,
		Service: svcCtl, ServiceName: cfg.CoreService,
	}
	filterFiles := singbox.FilterFiles{SingBox: cfg.SingBox}
	filterSvc := &dnsfilter.Service{
		Profiles: profileSvc, Store: st,
		CacheDir: filepath.Join(cfg.DataDir, "dns-filter"), RulesDir: rulesDir,
		Fetch: fetchList,
		Write: filterFiles.Sync,
		Prune: func(dir string, groups []dnsfilter.Group) { filterFiles.Prune(dir, groups) },
		Running: func(ctx context.Context) (bool, error) {
			info, err := svcCtl.Status(ctx, cfg.CoreService)
			return info.Running, err
		},
		Deploy: func(ctx context.Context) error {
			if r := deployer.Deploy(ctx); !r.OK {
				return &dnsfilter.ApplyError{Msg: r.Message}
			}
			return nil
		},
	}
	gen.WithFilter(filterSvc)
	dnsfilterapi.Register(srv.Mux(), dnsfilterapi.Deps{
		Profiles: profileSvc,
		Filter:   filterSvc,
		Preview:  dnsfilter.NewPreviewer(fetchList).Preview,
	})

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
