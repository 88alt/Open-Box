// openbox-backend 是 Open-Box 面板的自研后端。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	activationapi "github.com/timeflysoon/Open-Box/backend/internal/api/activation"
	authapi "github.com/timeflysoon/Open-Box/backend/internal/api/auth"
	clientappapi "github.com/timeflysoon/Open-Box/backend/internal/api/clientapp"
	clientsapi "github.com/timeflysoon/Open-Box/backend/internal/api/clients"
	configpreviewapi "github.com/timeflysoon/Open-Box/backend/internal/api/configpreview"
	deployapi "github.com/timeflysoon/Open-Box/backend/internal/api/deploy"
	diagnosticsapi "github.com/timeflysoon/Open-Box/backend/internal/api/diagnostics"
	dnsapi "github.com/timeflysoon/Open-Box/backend/internal/api/dns"
	dnsfilterapi "github.com/timeflysoon/Open-Box/backend/internal/api/dnsfilter"
	groupsapi "github.com/timeflysoon/Open-Box/backend/internal/api/groups"
	latencyapi "github.com/timeflysoon/Open-Box/backend/internal/api/latency"
	latencyhistoryapi "github.com/timeflysoon/Open-Box/backend/internal/api/latencyhistory"
	nodeinfoapi "github.com/timeflysoon/Open-Box/backend/internal/api/nodeinfo"
	policiesapi "github.com/timeflysoon/Open-Box/backend/internal/api/policies"
	profileapi "github.com/timeflysoon/Open-Box/backend/internal/api/profile"
	routetestapi "github.com/timeflysoon/Open-Box/backend/internal/api/routetest"
	rulesetsapi "github.com/timeflysoon/Open-Box/backend/internal/api/rulesets"
	serversapi "github.com/timeflysoon/Open-Box/backend/internal/api/servers"
	serviceapi "github.com/timeflysoon/Open-Box/backend/internal/api/service"
	sitelatencyapi "github.com/timeflysoon/Open-Box/backend/internal/api/sitelatency"
	storageapi "github.com/timeflysoon/Open-Box/backend/internal/api/storage"
	subsapi "github.com/timeflysoon/Open-Box/backend/internal/api/subscriptions"
	subsharesapi "github.com/timeflysoon/Open-Box/backend/internal/api/subshares"
	timezoneapi "github.com/timeflysoon/Open-Box/backend/internal/api/timezone"
	trafficapi "github.com/timeflysoon/Open-Box/backend/internal/api/traffic"
	"github.com/timeflysoon/Open-Box/backend/internal/auth"
	"github.com/timeflysoon/Open-Box/backend/internal/clientapp"
	"github.com/timeflysoon/Open-Box/backend/internal/core"
	"github.com/timeflysoon/Open-Box/backend/internal/deploystate"
	"github.com/timeflysoon/Open-Box/backend/internal/diagnostics"
	"github.com/timeflysoon/Open-Box/backend/internal/dnscache"
	"github.com/timeflysoon/Open-Box/backend/internal/dnsfilter"
	"github.com/timeflysoon/Open-Box/backend/internal/dnsqlog"
	"github.com/timeflysoon/Open-Box/backend/internal/dnssync"
	"github.com/timeflysoon/Open-Box/backend/internal/dnstest"
	"github.com/timeflysoon/Open-Box/backend/internal/geodata"
	"github.com/timeflysoon/Open-Box/backend/internal/latencyhistory"
	"github.com/timeflysoon/Open-Box/backend/internal/nodeexit"
	"github.com/timeflysoon/Open-Box/backend/internal/nodegroups"
	"github.com/timeflysoon/Open-Box/backend/internal/nodeinfo"
	"github.com/timeflysoon/Open-Box/backend/internal/policyentries"
	"github.com/timeflysoon/Open-Box/backend/internal/portcheck"
	"github.com/timeflysoon/Open-Box/backend/internal/probe"
	"github.com/timeflysoon/Open-Box/backend/internal/profile"
	"github.com/timeflysoon/Open-Box/backend/internal/regiondetect"
	"github.com/timeflysoon/Open-Box/backend/internal/routetest"
	"github.com/timeflysoon/Open-Box/backend/internal/rulelist"
	"github.com/timeflysoon/Open-Box/backend/internal/server"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/sitelatency"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
	"github.com/timeflysoon/Open-Box/backend/internal/subs"
	"github.com/timeflysoon/Open-Box/backend/internal/subshare"
	"github.com/timeflysoon/Open-Box/backend/internal/system"
	"github.com/timeflysoon/Open-Box/backend/internal/timezone"
	"github.com/timeflysoon/Open-Box/backend/internal/traffic"
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

	srv, background, err := newApp(cfg, log)
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
	waitBackground := func() {}
	if background != nil {
		waitBackground = background(ctx)
	}
	if err := srv.Serve(ctx, ln); err != nil {
		log.Error("服务异常退出", "err", err)
		stop()
		waitBackground()
		return 1
	}
	waitBackground() // ctx 已结束:等后台任务写完盘再退出
	log.Info("已退出")
	return 0
}

// newApp 组装存储、各接口分组和中间件。返回的 background 在进程启动后调用(传入随退出信号结束的 ctx),
// 启动后台任务(每日流量的采样、启动时清 DNS 接管的遗留)并返回等它们收尾的函数。
func newApp(cfg system.Config, log *slog.Logger) (*server.Server, func(ctx context.Context) (wait func()), error) {
	st, err := store.Open(cfg.StateDir())
	if err != nil {
		return nil, nil, err
	}
	authMgr, err := auth.NewManager(st)
	if err != nil {
		return nil, nil, err
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
	// 「上次成功部署用的输入指纹」:部署成功时记,服务状态的 pendingRestart 用它判断配置改了、内核还没用上
	applied := &singbox.Applied{Store: st, Source: gen}

	// 服务控制:只通过系统自带的服务管理器(OpenWrt init.d / systemd)操作 sing-box
	initKind := system.DetectInitHere()
	svcCtl := &system.Services{Kind: initKind, Run: system.ExecRunner, ProcUptime: system.ReadProcUptime}
	conflictDetector := &system.ConflictDetector{
		Svc: svcCtl, Exclude: []string{cfg.CoreService, cfg.PanelService},
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
	}
	// DNS 接管(档案 dns.mode = dnsmasq):停 / 重启 / 替换内核之前先撤销,内核起来后按 dns.mode 对齐,进程启动时清遗留。
	// 状态文件放在状态目录;dnsmasq 的配置目录只从它自己的主配置里读,读不到就不动(见 system/dnstakeover.go)
	// 接管时顺带让 dnsmasq 把查询日志写进 cfg.DNSQueryLog(默认在内存盘;-dns-query-log off 不写),DNS 过滤页的「查询记录」读它;
	// 用户自己已经设了日志去向、或加了日志 dnsmasq 起不来时不加(见 system/dnstakeover.go),原因由 dns-filter 状态的 queryLog 告诉前端
	takeover := &system.DNSTakeover{StateFile: filepath.Join(cfg.StateDir(), "dns-takeover.json"), Services: svcCtl, QueryLog: cfg.DNSQueryLog}
	dnsSync := &dnssync.Coordinator{
		Taker: takeover,
		Mode:  dnssync.ModeFromProfile(profileSvc.Get),
		Log:   log,
	}
	serviceapi.Register(srv.Mux(), serviceapi.Deps{
		Services:    svcCtl,
		Conflicts:   conflictDetector,
		Pending:     applied,
		DNS:         dnsSync,
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

	// 节点 / 组的延迟历史:测速接口(已保存节点)和 latency-history* 共用这一份
	nodeHistory := &latencyhistory.Service{
		Store: st,
		Kernel: func(ctx context.Context) ([]singbox.KernelProxy, error) {
			return singbox.KernelProxies(ctx, filepath.Join(cfg.ConfigDir, "config.json"))
		},
	}
	latencyapi.Register(srv.Mux(), latencyapi.Deps{Gen: gen, Subs: subsSvc, Profiles: profileSvc, Runner: probeRunner, History: nodeHistory})
	// 节点信息:类型 / 协议层 / 服务器 / 链式上游 / 入口地址解析 / 入口国家 / 出口归属和风控值
	entryDetector := &regiondetect.Detector{} // 只用它的按 IP 查国家,不占用其它两处的「判断中」状态
	nodeinfoapi.Register(srv.Mux(), nodeinfoapi.Deps{Service: &nodeinfo.Service{
		Pool:     subsSvc.PoolNodes,
		Profile:  profileSvc.Get,
		Country:  entryDetector.LookupCountry,
		IsPublic: regiondetect.Public,
	}, Exit: &nodeexit.Service{Probe: nodeexit.ProbeVia(gen, probeRunner, 8*time.Second)}})

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
		Track: applied,
		// 替换配置、重启内核之前撤销 DNS 接管;内核健康之后(含回滚后旧配置又健康)按 dns.mode 对齐
		BeforeApply: dnsSync.BeforeKernelStop,
		AfterRunning: func(ctx context.Context) string {
			res, err := dnsSync.AfterKernelStart(ctx)
			return dnssync.Note(res, err)
		},
		// 内核没在运行时,别的代理服务正在跑就不启动(同时跑会抢端口、DNS 和防火墙);只认正在运行的
		Conflicts: func(ctx context.Context) ([]string, error) {
			found, err := conflictDetector.Find(ctx)
			var names []string
			for _, c := range found {
				if c.Running {
					names = append(names, c.Label)
				}
			}
			return names, err
		},
	}
	// 每次部署(面板的「部署」按钮、DNS 过滤的应用)都经这里记下最近一次的结果,GET /deploy/state 读它
	deploySvc := &deploystate.Service{Store: st, Deploy: deployer.Deploy}
	deployapi.Register(srv.Mux(), deployapi.Deps{Service: deploySvc})
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
			if r := deploySvc.Run(ctx); !r.OK {
				return &dnsfilter.ApplyError{Msg: r.Message}
			}
			return nil
		},
	}
	gen.WithFilter(filterSvc)
	// 查询记录与统计:读 dnsmasq 的查询日志(路径由 -dns-query-log / OPENBOX_DNS_QUERY_LOG 给,默认在内存盘,写 off 就不读);
	// 应答是 NXDOMAIN 的域名拿已应用的名单对一遍,对上了才标「被拦截」
	qlog := dnsqlog.New()
	qlog.Classify = filterSvc.Classify
	// 日志文件放内存盘,写得多:读完超过上限就清空,但只清我们让 dnsmasq 写的那个文件,用户自己指定的日志不碰
	qlog.TrimAbove = dnsqlog.DefaultTrimAbove
	qlog.Owned = func() bool { return takeover.LogOwned(cfg.DNSQueryLog) }
	filterSvc.Connected = qlog.Connected
	filterSvc.QueryLogReason = func() string { _, skip := takeover.QueryLogState(); return skip }
	dnsfilterapi.Register(srv.Mux(), dnsfilterapi.Deps{
		Profiles: profileSvc,
		Filter:   filterSvc,
		Preview:  dnsfilter.NewPreviewer(fetchList).Preview,
		Log:      qlog,
	})

	// 延迟历史:每个节点 / 组最近 10 次测速,后端记、各浏览器共享;sync 读一次内核的 /proxies 把成功的结果记下来
	latencyhistoryapi.Register(srv.Mux(), latencyhistoryapi.Deps{Service: nodeHistory})

	// 站点延迟(概览页):请求经内核的面板入站走一遍路由,从内核连接列表认出线路;历史单独存 site-latency-history,不和节点历史混
	sitelatencyapi.Register(srv.Mux(), sitelatencyapi.Deps{Service: &sitelatency.Service{
		Hist: &latencyhistory.Service{Store: st, File: "site-latency-history"},
		Snapshot: func(ctx context.Context) (singbox.KernelSnapshot, error) {
			return singbox.KernelConnections(ctx, filepath.Join(cfg.ConfigDir, "config.json"))
		},
	}})

	// 真实路由测试(做了 DNS 判定、经内核 DNS 入站的解析、真实访问和线路;还没做的见 STATUS.md):经内核的面板入站访问一次,从连接列表认出命中的规则和线路;
	// 探测前让内核丢掉 DNS 缓存(只是让缓存失效)
	routetestapi.Register(srv.Mux(), routetestapi.Deps{Service: &routetest.Service{
		Snapshot: func(ctx context.Context) (singbox.KernelSnapshot, error) {
			return singbox.KernelConnections(ctx, filepath.Join(cfg.ConfigDir, "config.json"))
		},
		FlushDNS: func(ctx context.Context) error {
			return singbox.KernelFlushDNS(ctx, filepath.Join(cfg.ConfigDir, "config.json"))
		},
		// 域名目标先经内核的 DNS 入站真问一次(档案开了 IPv6 才再问 AAAA)
		IPv6: func(ctx context.Context) bool {
			p, err := profileSvc.Get(ctx)
			on, _ := p["ipv6"].(bool)
			return err == nil && on
		},
		// DNS 判定:读已部署的 config.json,按它的 DNS 规则预测这个域名问哪台服务器;规则集文件读不到、
		// 二进制规则集需要 sing-box 解码,判断不了的如实记成前提;代理侧的实际线路读内核的代理列表补上
		DNS: &routetest.DNSPredictor{
			ConfigFile: filepath.Join(cfg.ConfigDir, "config.json"), SingBox: cfg.SingBox, TmpDir: probeDir,
			Proxies: func(ctx context.Context) ([]singbox.KernelProxy, error) {
				return singbox.KernelProxies(ctx, filepath.Join(cfg.ConfigDir, "config.json"))
			},
		},
	}})

	// 每日流量:后台每 10 秒读一次内核的连接快照(/connections),按天、按小时、按终端 / 节点 / 访问目标记进数据目录下的 traffic/;
	// 这里只读。终端名取 DHCP 租约;保留月数取档案的 traffic.keepMonths
	var background func(ctx context.Context) func()
	var trafficSource trafficapi.Source
	if ts, err := traffic.Open(filepath.Join(cfg.DataDir, "traffic"), nil); err != nil {
		log.Warn("创建流量统计目录失败,每日流量不可用", "error", err)
	} else {
		trafficSource = ts
		configFile := filepath.Join(cfg.ConfigDir, "config.json")
		lastErr := ""
		collector := &traffic.Collector{
			Store: ts,
			Source: func(ctx context.Context) (singbox.KernelSnapshot, error) {
				snap, err := singbox.KernelConnections(ctx, configFile)
				if err == nil {
					lastErr = ""
				}
				return snap, err
			},
			KeepMonths: func() int {
				p, err := profileSvc.Get(context.Background())
				if err != nil {
					return 0
				}
				t, _ := p["traffic"].(map[string]any)
				switch v := t["keepMonths"].(type) {
				case float64:
					return int(v)
				case int:
					return v
				}
				return 0
			},
			OnError: func(err error) { // 内核没在跑是常态:同一条错误只记一次,恢复后再出错才再记
				if msg := err.Error(); msg != lastErr {
					lastErr = msg
					log.Warn("每日流量采样失败", "error", msg)
				}
			},
		}
		background = func(ctx context.Context) func() {
			done := make(chan struct{})
			go func() { defer close(done); collector.Run(ctx, traffic.DefaultInterval) }()
			return func() { <-done }
		}
	}
	trafficapi.Register(srv.Mux(), trafficapi.Deps{
		Source: trafficSource,
		Names: func() map[string]string {
			names := map[string]string{}
			for _, c := range system.ReadDHCPLeases(system.DHCPLeaseFiles, time.Now()) {
				if c.Name != "" {
					names[c.IP] = c.Name
				}
			}
			return names
		},
	})

	// 诊断包:版本 / 固件 / 服务状态 / 脱敏的运行配置和档案 / 最近日志,只读
	diagnosticsapi.Register(srv.Mux(), diagnosticsapi.Deps{Service: &diagnostics.Service{
		Version: version, CoreService: cfg.CoreService, PanelService: cfg.PanelService,
		ConfigFile: filepath.Join(cfg.ConfigDir, "config.json"), Init: system.DetectInitHere(),
		KernelVersion: func(ctx context.Context) string { return singbox.ReadKernelVersion(ctx, cfg.SingBox).Version },
		ServiceStatus: svcCtl.Status,
		Profile:       profileSvc.Get,
	}})

	// 系统时区:OpenWrt 走 uci + system reload,systemd 走 timedatectl;时区名单和 POSIX 时区串是内置的 IANA 数据
	timezoneapi.Register(srv.Mux(), timezoneapi.Deps{Service: &timezone.Service{Init: system.DetectInitHere()}})

	// 激活和教程视频:没有对应概念,只回固定内容(DESIGN D9 和第 8 节),不保存任何东西
	activationapi.Register(srv.Mux(), activationapi.Deps{
		Version: version,
		KernelVersion: func(ctx context.Context) string {
			return singbox.ReadKernelVersion(ctx, cfg.SingBox).Version
		},
	})

	// 已知终端(只读):DHCP 租约里的设备,给「终端分流」选来源;流量里出现过的来源 IP 等 traffic* 做了再并进来
	// 共享网络:保存服务器前的端口检测(只读;自用端口 = 面板监听端口 + 内核固定端口 + 已部署配置里的非共享入站)
	serversapi.Register(srv.Mux(), serversapi.Deps{Ports: &portcheck.Checker{
		Servers: func(ctx context.Context) ([]portcheck.Server, error) {
			p, err := profileSvc.Get(ctx)
			if err != nil {
				return nil, err
			}
			return portcheck.ServersFromProfile(p), nil
		},
		Reserved:   []int{clientapp.PortOf(cfg.Listen)},
		ConfigPath: filepath.Join(cfg.ConfigDir, "config.json"),
	}})

	clientsapi.Register(srv.Mux(), clientsapi.Deps{
		List: func() []system.Client { return system.ReadDHCPLeases(system.DHCPLeaseFiles, time.Now()) },
	})

	// 客户端 App 页的信息:路由器标识、局域网地址与网段、地区分流、路由器名称。只读网卡和档案,
	// 路由器 ID 第一次读时生成并存进状态目录。本地分流的同步状态(client-config:开关、配对码、换码)也在这里;封包、App 用的公开同步接口等私有约定另列条目,还没做
	egressDetector := &regiondetect.Detector{} // 与 dns/region-detect 各用各的:互不占用「判断中」状态
	clientAppSvc := &clientapp.Service{
		Store: st, Profile: profileSvc.Get, LAN: system.LAN, PanelPort: clientapp.PortOf(cfg.Listen),
		// 出口国家:内核在运行时才判(只查询,不改任何设置),结果缓存在状态目录里
		DetectEgress: func(ctx context.Context) (string, string, error) {
			r := egressDetector.Detect(ctx)
			if r.Region == nil {
				return "", "", errors.New(r.Error)
			}
			return r.IP, r.Country, nil
		},
		Running: func(ctx context.Context) (bool, error) {
			info, err := svcCtl.Status(ctx, cfg.CoreService)
			if errors.Is(err, system.ErrNoInit) { // 面板不在路由器上(没有 procd / systemd):判断不可用,不是出错
				return false, clientapp.ErrUnavailable
			}
			return info.Running, err
		},
	}
	clientappapi.Register(srv.Mux(), clientappapi.Deps{Service: clientAppSvc, Config: clientAppSvc, Egress: clientAppSvc})

	// 订阅分享(只管记录和 token;对外的 /sub/<token> 下发内容是另一条,还没做):新选的订阅必须存在
	subsharesapi.Register(srv.Mux(), subsharesapi.Deps{Service: &subshare.Service{
		Store: st,
		SubscriptionIDs: func(ctx context.Context) ([]string, error) {
			recs, err := subsSvc.List(ctx)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(recs))
			for _, r := range recs {
				ids = append(ids, r.ID)
			}
			return ids, nil
		},
	}})

	// 前端静态文件与前端路由回落;最后挂,/api 下的路径由各接口分组优先处理
	srv.Mux().Handle("/", server.StaticHandler(cfg.WebDir))

	// 启动后台任务之外再加一项:先清上次异常退出遗留的 DNS 接管(内核正常就保留,内核不在了就撤销)。
	// 放在后台做:它要探测内核、必要时重启 dnsmasq,不拖慢面板的启动;退出时等它做完,别把 dnsmasq 留在半截
	traffic := background
	background = func(ctx context.Context) func() {
		wait := func() {}
		if traffic != nil {
			wait = traffic(ctx)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			defer cancel()
			dnsSync.Recover(rctx)
		}()
		return func() { wait(); <-done }
	}

	// 再加一项:跟读 dnsmasq 查询日志,并定期把记录和统计落盘(退出时等它写完)
	withLog := background
	background = func(ctx context.Context) func() {
		wait := func() {}
		if withLog != nil {
			wait = withLog(ctx)
		}
		done := make(chan struct{})
		go func() { defer close(done); dnsqlog.Run(ctx, qlog, cfg.DNSQueryLog, st, 0, nil) }()
		return func() { wait(); <-done }
	}
	return srv, background, nil
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
