# core/ — 自己维护的 openbox-core(开源 Go 实现)

替换上游闭源的 `panel/server/native/openbox-core`。它是一个**无状态**的 stdin/stdout JSON 程序,
由自研后端(`backend/`,Go)以子进程方式每次调用时拉起,**不是服务**:HTTP API、订阅解析、部署回滚、服务控制都在后端。

## 协议
- stdin:`{"protocol":1,"method":"...","args":[...],"license":"...","geoTags":[...]}`
- stdout:`{"protocol":1,"ok":true,"value":...}` 或 `{"protocol":1,"ok":false,"error":"..."}`
- 退出码恒为 0;输入上限 32 MiB,输出上限 64 MiB;`license` 字段忽略(没有激活机制)
- 第一次调用先发 `hello`,返回 `implementation:"go-native"`、`protocol:1`、`buildID`;
  自研后端只要求 implementation 为 go-native、protocol 为 1,**不校验 buildID**(只是个构建标识,见 `scripts/build.sh`)

## 方法(共 20 个 + hello)
| 包 | 方法 | 状态 |
|---|---|---|
| routing | normalizePolicy normalizeRouting routingFingerprint migrateStoredRouting classes forwardPlan bypassPlan | 已实现(对照基线的金标准测试) |
| routing | entryPlan | 已实现(含 whitelist,按基线 entryModePlan 对齐) |
| groups | normalizeGroups builtinTags emitGroups | 已实现(对照基线的金标准测试,含逐字节键序);组类型 urltest / selector / failover / loadbalance,负载均衡的行为见 HANDOFF 第五节授权记录和 loadbalance_test.go |
| config | configDetailed | 已实现(返回 config / dnsRuleOwners / directHosts / failover,另多带 flipFlags、nativeBypass;对照基线 engine/config.mjs 的 buildConfigDetailed 的金标准测试 + 官方 sing-box check,见下) |
| mihomo | mihomoProxies mihomoGroups mihomoTargetRules mihomoCompatibility mihomoDnsUpstreams | 已实现(节点 → Mihomo proxies、节点组 → proxy-groups、分流 → rules(规则表分段是推断,见 HANDOFF 第三节)、切换前的兼容性清单 problems(检查范围自研定的)、两侧 DNS 上游的形状(mihomoDnsUpstreams,给后端装配 Mihomo 的 DNS 段);自写向量 + 可选真实 Mihomo `-t`,见 HANDOFF 第三节) |
| authz | checkActivationCode activationState authorizationState | 已实现(一律解锁) |

## 目录
- `cmd/openbox-core/` 入口与方法注册
- `internal/protocol/` 协议、hello、大小限制
- `internal/{routing,groups,config,authz}/` 各类方法;`internal/config/` 是 configDetailed 的装配(`build.go`)和链式代理核对(`chain.go`),以及 node-dns / tun-options / 常量
- `internal/emit/` 节点 → sing-box 出站 / endpoint;`internal/mihomo/` 节点 → Mihomo proxies(先走 emit 再翻字段)
- `internal/ipx/` Node 同款 isIPv4/isIPv6 正则 + CIDR 运算(解析、重叠、包含、挖洞)
- `internal/clientroutes/` 终端分流;`internal/servers/` 共享网络服务端入站;`internal/directhosts/` 订阅和节点站点直连;
  `internal/dnsrewrite/` DNS 重写;`internal/idna/` 域名转 punycode;`internal/urlx/` 按 WHATWG 规则取 `new URL(x).hostname`
- `internal/leafgolden/` 只有测试:上面几个叶子包统一对照基线的金标准
- `internal/jsx/` 复刻旧 Node 实现用到的 JS 语义(Number() 转换、trim、undefined 与 null 的区别)
- `testdata/` 基线生成的金标准(只读保留作回归,不再重新生成)、自写向量用的数据,以及 `tools/`(见「对照基准」)
- `scripts/` 构建脚本
- `singbox/` 预留

## 对照基准(基线)与有意差异
历史基线是 tag `engine-snapshot`(上游 v0.1.318 快照,提交 0c9d530)里 `panel/server/engine/` 的明文源码;它已不在 catch-up-upstream 也不在 main,不随同步更新,使用条件见 HANDOFF 第六节。取法:`git archive engine-snapshot panel/server/engine panel/server/system/panel-port.mjs | tar -x -C <临时目录>`,再在 `<临时目录>/panel/server/` 下补一个 `{"type":"module"}` 的 package.json。
**现在的做法**(HANDOFF 第五、六节):对齐上游靠看行为,**不再用压缩的 `index.mjs` 或官方 openbox-core 二进制来对齐实现,也不再重新生成基线金标准**。`testdata/*_golden.json` 只读保留作回归;`gen-*.mjs` 和 `testdata/tools/` 里的 `extract-from-bundle.mjs`、`diff-upstream.py`、`upgrade-golden.py` 是历史工具,保留不动、**不要再运行**(后两个依赖官方二进制,碰了红线)。新的测试向量一律自己设计、人工推算期望值,配置类输出再用公开发布的 sing-box 跑 `check`(`testdata/tools/sbcheck.py`)。

已按基线对齐的模块(都有金标准或自写向量,做过变异测试):routing(模型、迁移、指纹、plans:classes / forwardPlan / bypassPlan / entryPlan 含 whitelist)、dns(`routing/dns.go` + `dnsupstream.go` + `flip.go`)、route(`routing/route.go`)、叶子模块(ipx、servers、directhosts、dnsrewrite、clientroutes)、dnsfilter、urlx / idna(`urlx.Parse` 给 DoH 地址解析用;UTS46 轻量映射表,完整 NFKC 全表不做)、emit、groups、configDetailed(`internal/config/build.go` 按原文件顺序逐段移植)。只有 Node 进程用、不属于 core 的没有移植:dns-upstream 的 regionDnsDefaults / regionDnsError、dns 的 directResolverServers / applyProxyUpstreamRoutes / dnsPolicyClasses、flip 的 flattenFlipRule、preResolveRules、nodeDirectSources。链式代理:新版 Node 在调用前就把候选节点算好当 `chainNodes` 传进来,core 只做进内核前的核对(重名 / 上游不存在或是内置直连拒绝 / 成环,`config/chain.go`),没有移植 parseSubscription / parseHttpProxyLink。

**有意保留的差异**(不算缺口,别改回):
- BOM 和 `__proto__` 键(官方是 Go,不去 BOM、不按 JS 原型处理;自研沿用 JS 的 trim、丢弃 `__proto__`);
- 非 ASCII 主机(`ruleUrls` / 节点域名)官方丢弃,自研转 punycode 保留;
- 改版前 regions 旧数据的 `fallback.default`(官方 direct,自研 proxy);
- 前置自定义分流里网段写错的 `ipCidr` 行整理时丢掉(2026-11-08 已定,与 `ruleUrl` / `port` 同;写法按 sing-box 的 `ip_cidr`:带 `/` 按网段,否则按单个地址,前导零、带区域、逗号连写都不认),`routing/golden_test.go` 对这类用例跳过逐条比对;
- DNS 上游与节点专用解析支持 DoT(protocol 取 tls,默认端口 853),`dns_golden` 里带 tls 协议的旧用例按有意差异跳过;
- 前置自定义分流里「引用规则集、出口又不在出站表里」的行不再写 DNS 规则(HANDOFF 第四节 3);
- sing-box 补丁字段(如 urltest 的 `expected_status`)**不在核心里按内核版本降级**(已定):需要配套的 Open-Box 内核,换成原版 sing-box 遇到不认识的字段会在启动 / `check` 时直接报错。

**入参里基线没有、只能从 Node 消费处推断的**:
- `clientBindings`:按绑定表把 IPv4 来源扩展出对应 IPv6 /128(`clientroutes.ExpandSourcesWithBindings`),形状 `{ "192.168.1.10": ["fd00::10"] }` 或值单字符串;
- `platform`:只有 `container` 有行为:`dns-in` 后面多一个监听 `0.0.0.0:53` 的 `dns-in-lan` 入站,`route.rules[0]` 是 `{"action":"hijack-dns","inbound":["dns-in-lan"]}`(在 guardTerminalRules 之后插入);`openwrt` / `systemd` 和不传一样;
- `ignoreProxyClients`(bypassPlan 选项):有终端走代理时不再关旁路,reason 末尾补一句「有终端被指定走代理:这些集合只用于入口按来源旁路,代理终端本身由入口 guard 拦下」。
另:rulesetDir 里 `.srs` 的路径先去掉末尾所有的 `/`、null 当空字符串;节点直连规则集(`obnode-direct*.json`)的路径不去斜杠。官方 hello 还列了 normalizeCustom / verifyLicense / featureAccess,没有被调用,自研没有实现。

**金标准与真实 sing-box**:`testdata/tools/sbcheck.py` 用官方 sing-box 对配置跑 `sing-box check`(`config_golden` 在 1.13.14 下全过,在 1.14.4 下有 68 条不过,分类与处理见 HANDOFF 第四节 3)。官方版低于 1.14 时检查前要剥掉它不认的字段(`dns_mode` / `dns_address` / `*_mac_address` / `source_mac_address`,以及补丁字段 `expected_status`),并把语料里的假 uuid / 密钥 / 证书换成合法值——这是语料和版本差异,不是装配错误。

## 构建
    sh core/scripts/build.sh amd64 out    # 或 arm64;产出 out/openbox-core 和 out/core-build.json
    # buildID 取法:环境变量 OPENBOX_CORE_BUILD_ID > 第三个参数 / ../panel/server/index.mjs(main 上没有)> core 源码(不含测试)的 sha256;后端不校验它

## configDetailed 入参与返回
Node 传入 `{platform, cacheFilePath, rulesetDir, selections, tlsCert?, nodes, userGroups, subscriptions, profile, systemDns,
localSubnets, directHostCidrs, ruleLists, nativeBypass, dnsFilter, inlineDirectHosts, chainNodes, filterExpectedKey?}`,
取回 `{config, dnsRuleOwners, directHosts, failover}`(另多带 `flipFlags`、`nativeBypass`)。入参比旧版 buildConfig 多了 platform / rulesetDir / inlineDirectHosts / chainNodes / dnsRuleOwners,这几项没有旧代码可对照。拆解阶段 A~D(叶子模块、dns / route、config 零件、整份装配与真实 sing-box 校验)全部完成。

## 发布与 CI
- main 上有 Backend 工作流(`backend.yml`)和只能手动触发的 `release-backend.yml`,但 **`backend.yml` 只在 `backend/**` 变动时才跑,不覆盖 `core/`**;`release.yml` 只在 catch-up-upstream。所以 core 的 `go vet ./...` / `go test ./...` 要自己在本地跑(HANDOFF 第七节第 4 条讲沙箱里 Go 版本低于 go.mod 时的办法)。
- 发布包里的 core 由后端的 M6 打包:`backend/packaging/build-package.sh` 调用 `build-core.sh` 直接 `go build`(不调用 `core/scripts/build.sh`,不注入 buildID),`release-backend.yml` 检查包里有 `bin/openbox-core`。设计与落地记录见 `backend/docs/M6.md`。
- 旧方案(release.yml 的 core 任务取 main 的 core/ 编出来、build-release.sh 换进发布包、sync-upstream.yml 不覆盖 native)已被「Replace with upstream」同步覆盖掉,**不要再按它操作**;旧版 `release.yml` 可从 `git show f4d044e:.github/workflows/release.yml` 取回参考,但那是给上游 Node 面板用的。
- `core/UPSTREAM_VERSION`(v0.1.354)是旧流程留下的「最后对齐的上游版本」记录,按 HANDOFF 第四节 5 不再推进,也不随每次对齐修改;core 对齐到哪一版,看 HANDOFF 第三节和 `git log -- core`。
