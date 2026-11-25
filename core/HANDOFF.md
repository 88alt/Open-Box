openbox-core 交接笔记(第 7 版,只管 core;后端另见 backend/HANDOFF.md)

写给下一位接手 core 的 AI。读完这份和 core/README.md,就可以接着做,不用反复问维护者。
进度要对账:main 下的提交记录(git log -- core)是实时来源,本文第三节已于 2026-11-25 精简并对账;第五节于 2026-11-09 改为「对齐就是看行为」,2026-11-23 把有意例外整理成 A / B / C 三类;两者冲突时以提交记录为准,并在补丁里顺手更新第三节。


# 零、先读这一节

- core 是什么:自己写的、开源的 Go 程序 openbox-core(目录 core/)。无状态的 stdin/stdout JSON 程序,不是服务;调用方每次拉起它一次。
  协议、方法表、目录结构以 core/README.md 为准。共 20 个方法加 hello(第 16-20 个 mihomoProxies / mihomoGroups / mihomoTargetRules / mihomoCompatibility / mihomoDnsUpstreams 见第三节)。
- 谁调用它:自研后端(backend/,另一个独立项目,见 backend/HANDOFF.md)以子进程方式调用。core 本身不含 HTTP、存储、订阅解析、部署,这些都在后端。
- 不加激活机制。授权相关的三个方法保留为"未限制"的固定返回,新后端不调用。
- 正确性靠"金标准":同一份输入喂一个标准答案程序,得到的输入输出存成 testdata/*.json,Go 测试逐条对照;再用变异测试证明测试有效。
- core 与 backend 分工清楚:改 core 的提交只动 core/,提交说明以 "core:" 开头;后端需要 core 新增或改动方法时,在 backend/STATUS.md 里记一条"需要 core 配合",由 core 这边另做。
- 推送方式:AI 跑在维护者本机时,直接在 main 提交并推送(SSH 别名 remote,见 backend/README.md「维护者的工作方式」),不 force push;只有拿不到推送权限的沙箱才出 .patch(第七节)。
- **`core/` 没有任何 CI 覆盖**(`backend.yml` 只在 `backend/**` 变动时跑):改 core 的批次,推送前必须在 `core/` 下本地跑 `go vet ./...` 和 `go test ./...`,推送后没有检查是正常的;同一批若也改了 `backend/`,仍要按 backend/STATUS.md 文首「CI 门禁」等 Backend 全绿。

接手后先做:
1. 克隆 https://github.com/timeflysoon/Open-Box (公开),git checkout main(默认分支 catch-up-upstream 里没有 core/)。
2. 先对账:git fetch 后看 git log --oneline origin/main -- core,和第三节、core/README.md 的进度表对照。提交里已经做了、文档没标的,以提交为准,
   不要重复实现、重复推送。每次出补丁前再 git fetch 一次,确认 origin/main 没有新提交。
3. 确认 go version(应不低于 go.mod 的 1.25.12;沙箱里缺 Go 用 apt 装、访问不了模块代理时见第七节第 4 条)。node 只有重新生成金标准才用得到,而金标准已不再重新生成(第六节),一般用不到。
4. 按第四节的顺序做;每块做完 go vet ./... 和 go test ./... 全绿再推送。第三节的进度标记要**和代码放在同一个提交里更新**,不要留到最后补
   (额度可能随时用完,拆开就会出现代码已交付、文档没更新的漂移)。


# 一、已定决策(只列与 core 有关的)

1. 全部自研,不带激活机制。
2. sing-box 内核不自己做,取公开发布的成品;core/singbox/ 只是预留目录。
3. 核心不对 sing-box 补丁字段(如 urltest 的 expected_status)按内核版本降级:需要配套内核,换成原版时内核启动或 check 会直接报错。
   以后要支持其他内核再单独做版本探测。
4. Go 版本 1.25.12(core/go.mod),CGO_ENABLED=0 静态编译。
5. hello 的 buildID:自研后端不校验它,只要求 implementation 为 go-native、protocol 为 1。


# 二、仓库与同步(只列与 core 有关的)

- 自研只在 main;catch-up-upstream 纯拉取上游,是上游的备份,不放自研代码。
- main 的内容每次由维护者本机的 sync-main.sh 重新生成:除 panel/ 和 KEEP 目录外都被上游版本覆盖。core/ 在 KEEP 里,不会被覆盖。
  core 的所有代码、脚本、测试数据都必须放在 core/ 内,放在别处会在下次同步时丢失。
- tag engine-snapshot(提交 0c9d530,上游 v0.1.318 快照)保留不删。它的使用条件见第六节。
- CI:main 上只有 Backend 工作流(`backend.yml`,只在 `backend/**` 或它自己变动时跑)和只能手动触发的 `release-backend.yml`,**都不覆盖 `core/`**。`release.yml` 是上游版本、只在 catch-up-upstream,会被同步覆盖,不要依赖它(旧的 core 云端任务在 `git show f4d044e:.github/workflows/release.yml`,仅供参考)。所以改 core 只能本地验证。
  **建议维护者**:想让 core 也有云端验证,可以把 `core/**` 加进 `backend.yml` 的 `paths`,再加一个跑 `go vet` / `go test` 的任务。这属于改工作流,AI 不要自己动,等维护者明确要求。


# 三、进度标记(2026-11-25 精简;逐项细节在代码、测试和 git log -- core 里)

图例:[x] 完成  [~] 代码完成、待真机或待云端确认  [ ] 未做

20 个方法加 hello 全部实现(routing 8、groups 3、config 1、mihomo 5、authz 3),`go vet ./...`、`go test ./...` 在 main 上全绿。

[x] 协议、hello、大小限制、panic 恢复
[x] routing:normalizePolicy、normalizeRouting、routingFingerprint、migrateStoredRouting、classes、forwardPlan、bypassPlan、entryPlan
[x] groups:normalizeGroups、builtinTags、emitGroups(含 loadbalance,与上游 v0.1.387 行为对齐)
[x] emit、clientroutes、dns(含 upstream 与热切换 flip)、route;叶子模块 dnsfilter、ipx、servers、directhosts、dnsrewrite、idna、urlx(UTS46 轻量映射表,完整 NFKC 全表不做)
[x] config:configDetailed 装配;另多带 flipFlags、nativeBypass {enabled, sets, reason}(自研多带,基线没有;后端 terminal-test 的 firstLayer 用)
[x] 后来对齐上游的功能(都是维护者授权、自写向量覆盖、基线金标准文件没动):
    - 带宽择优(core 层):组的 bandwidth 开关 → urltest / loadbalance 出站写 bandwidth:true;选项 disableBandwidth(客户端配置)不写。测速调度、校验、接口在后端。
    - 节点评分权重:档案 nodeScoreWeights {latency, download, jitter} 写成出站的 node_score_weights(只在开着带宽择优时写),对齐上游 v0.1.472。
    - groups 排除关键词 excludeKeywords / excludeKeywordMode(v0.1.472);动态组的订阅范围 subscriptionIds 与链式代理来源(v0.1.476)。
    - dns.fakeIpRange(FakeIP 的 v4 地址池可配,IPv4 CIDR、掩码 /15~/24,v0.1.481)、dns.domainUpstreams(指定域名的上游 DNS,v0.1.482)、dns.listen53(systemd 部署额外监听 0.0.0.0:53 的 dns-in-lan,和容器同一套入站与 hijack-dns,v0.1.485);DNS 上游与节点专用解析支持 DoT(v0.1.422)。
    - 前置自定义分流里网段写错的 ipCidr 行整理时丢掉(2026-11-08 已定,与基线的有意差异)。
[x] mihomo 五个方法(双内核,对齐上游 v0.1.477):mihomoProxies、mihomoGroups、mihomoTargetRules、mihomoCompatibility、mihomoDnsUpstreams。**两处是推断,待真机核对**:mihomoTargetRules 的规则表分段;mihomoCompatibility 的检查范围(自研按「能不能生成」定,与上游逐条是否一致没法核对)。
[x] 授权三个方法(checkActivationCode、activationState、authorizationState):固定的「未限制」返回,新后端不调用
[x] go.mod 对齐 Go 1.25.12;core/scripts/build.sh 读不到 panel/server/index.mjs 时按 core 源码哈希生成 buildID(后端不校验)
[x] 整理测试数据来源:所有模块都已有自写向量(期望值人工推算;配置类输出用官方 sing-box 1.13.14 / 1.14.4 跑过 check,见第四节 2、3)。config_golden 等基线金标准只读保留作回归,其去留由维护者定。
[~] 真机验证:真实路由器上经后端调用 core、sing-box 启动成功(由维护者做,记在 backend/STATUS.md 的真机条目)
[ ] 第四节第 3 ~ 6 条里还没结的事

面板文案(2026-11-23 起反复核对):main 与上游三语键集一致,差异只有例外 C 的键和 chainProxyEmptyHint(例外 B);结论见 backend/STATUS.md「最终差异清单」。


# 四、后续工作(按顺序)

1. 补已知缺口:**已全部做完**(UTS46 轻量映射表、clientBindings、前置自定义分流 ipCidr 校验)。有意保留的差异,不算缺口:BOM、`__proto__` 键、非 ASCII 主机转 punycode。
2. 整理测试数据来源(见第六节):**已全部做完**。每个模块都换成了自写向量(期望值人工推算,JSON 逐字节比),每换一块都做过变异测试。基线金标准(`testdata/*_golden.json`)只读保留作回归,**不再重新生成**。
   - 用官方 sing-box 复核自写向量的产出:`OPENBOX_DUMP_DIR=/tmp/dump go test ./internal/config/ -count=1`(`dumpConfig` 在 `build_head_vectors_test.go`,设了该环境变量才落盘),再 `python3 testdata/tools/sbcheck.py <sing-box 可执行文件> /tmp/dump`(检查前剥掉官方版不认的字段、假密钥换合法值,与金标准那份流程相同)。1.13.14 下 277 份全过,1.14.4 下 281 份全过。
3. 跟着 sing-box 更新(见第五节):sing-box 发新版、配置格式变化时,更新 core 输出和对应测试。backend 的工作流里固定了一个 sing-box 版本(现为 1.14.4),升级时一起改。
   - [x] 2026-11-10 用官方 sing-box 1.14.4 复核:`sbcheck.py` 按 `sing-box version` 判断,1.14 及以上不再剥 tun 的 dns_mode / dns_address / *_mac_address 和规则里的 source_mac_address(官方认,原样交给 check,这些字段也就被官方校验到了),上游补丁字段 expected_status 仍剥。自写向量的产出 281 份全部通过(其中带 dns_mode 的 281 份、带 MAC 名单的 8 份、带 source_mac_address 的 6 份)。
   - [x] 金标准语料与 1.14.4(2026-11-25 已按预设决策处理,**待维护者复核**):`config_golden.json` 在 1.13.14 下全过,在 1.14.4 下有 68 条不过,分三类(`sbcheck.py` 现在自动按这三类归并,「未归类」不为 0 才是新问题):① 41 条是语料输入自相矛盾(dnsFilter 的 allow / blocks 引用了 sets 里没有的名单),② 12 条是订阅 id 重复导致节点 DoH 服务器 tag 撞车,这两类 core 照输入如实处理、不是装配错误,不用改;③ 15 条是**真实路径**:前置自定义分流里 geosite 行的出口已经不存在(用户删了或停用节点组而自定义行还指着它),route 一侧整行跳过、DNS 一侧仍写 `rule_set: geosite-xxx`,sing-box 1.14 起会拒绝整份配置、内核起不来。曾试过直接在 BuildDNS 里按 `KnownOutbounds` 跳过,会让 `dns_golden` 变 81 条、`config_golden` 变 15 条,代价太大,已否掉。现在的做法:装配层只算「哪几行要跳」,行本身不删:
     - `internal/routing/customrules.go` 的 `DanglingRuleSetRows(rules, known, b) []bool`:只标记「引用了规则集(`geosite` / `ruleset` / `ruleUrl`,也就是 `customRuleTag != ""`)**且**出口不在已知出站表里」的行;域名类 / IP / 端口行不标(它们不会让配置变非法,保持基线行为)。表为 nil 或没有悬空行时回 nil。
     - `DNSOptions.SkipRuleSetRows []bool`:BuildDNS 的自定义分流循环看到标记就 `resolvers.Custom = append(nil)` 后返回,不写规则;行下标仍按档案算,所以 `dnsRuleOwners` 的 `index` 仍对得上用户看到的那一行(先删行再建的写法会把后面的行整体挪位,`index` 就错位了,这一点试过,已否掉)。
     - `internal/config/build.go` 在算出 `knownOutbounds` 之后调一次,把标记交给 DNS;`BuildDNS` 直接调用(测试)不传就完全不变,所以 `dns_golden` **一条都不用跳**。
     - 金标准:`build_golden_test.go` 新增跳过判定 `hasDanglingRuleSetRule`(入参里有「引用规则集 + 出口不在基线出站表里」的行、且 `dns.split` 开着才跳),`config_golden.json` 500 条里跳 28 条、其余 472 条逐字节一致;这 28 条正是行为真的变了的那批(不是宽口径的 61 条)。新行为由 `internal/config/build_customrules_vectors_test.go`(端到端:悬空的 geosite 行不写规则、认识的照写、归属下标仍是 1)与 `internal/routing/customrules_test.go`(标记口径、nil 表、空表)覆盖。
     - 端到端复核(真实 core + 真实后端,`config/preview`):档案里写一条 `geosite:netflix → 已删的组` 和一条 `domain:keep.example → 已删的组` 后,预览的 `dns.rules` 里没有任何「引用路由未声明的规则集」的条目,`keep.example` 那条(域名类)照旧保留。
     - 官方内核复核:这 28 条各导两份配置(基线 / 新行为)交给官方 sing-box 1.14.4 跑 `check`(经 `testdata/tools/sbcheck.py`):基线 4/28 通过、15 条报 `rule-set not found: geosite-netflix`;新行为 14/28 通过,那 15 条全部消失。剩下的是 `auto_redirect is only supported on Linux`(5 条,Windows 环境限制)、语料自相矛盾的 dnsFilter 规则集(3 条,①)、订阅 id 重复导致 DNS tag 撞车(1 条,②)——都不是本批引入的。
     - **待维护者复核**:一是这条改动本身(要回退就 revert 这一个提交,金标准跳过判定一起删掉);二是「金标准语料怎么处理」这个大问题(①②③里的 ①② 仍未动,③ 的 28 条已按上面方式处理)。
4. 授权三个方法:新后端不调用,先保留,等后端稳定后再决定是否删除。
5. core/UPSTREAM_VERSION(现为 v0.1.354):原本记录"最后对齐的上游版本"。按第五节的规则不再按上游版本推进,默认不再改动,处理方式由维护者定。
6. 真机联调反馈:维护者在真机上发现的 core 输出问题,按第三节的方式补测试再修。

7. 前后端对齐的核对方法与结论:
   - 方法(上游出新版时重做;只读,不看上游 index.mjs):取 main 的 `panel/src` 和上游 `panel/dist` 里所有 `/api/...` 字面量,按后端 `HandleFunc` 注册的路径逐个匹配(`{x}` 当通配,不含兜底路由);再对三语文案键集和引用。
   - 结论(2026-11-25):面板调用的接口后端全部有路由(`overview-links` 除外,例外 B)。原先列的缺口都已做完:`profile/target-routing`、`node-info/sharing/{ip}`、`kernel/switch/preflight` 与内核切换、`subscriptions/updates`。上游侧边栏 `OverviewCarousel` 写死的两条外链文案,已按例外 B 改成 Open-Box Latest 和魔改 sing-box 最新版本号。
   - 没覆盖到的范围:上游面板里不以 `/api/` 开头拼出来的请求(字面量法看不到拼接),以及运行时行为(真机)。


# 五、跟随上游

上游闭源,没有源码可抄。对齐就是看行为——界面、默认值、字段形状、官方 core 对同一输入的返回、压缩包里的实现痕迹、相邻版本差异——然后按观察到的行为把自研写对。这是正常工作方式。

参考什么都可以:panel 源码与产物、公开 Release 包(含 index.mjs 与官方 openbox-core)、sing-box 文档与二进制、Release 说明、协议规范。观察结论记在补丁说明里便于后人即可。自研代码与测试自己维护;上游安装包不必放进本仓库。

产品边界(与对齐无关)
- 不加激活机制;授权三个方法固定返回「未限制」。
- 有意不与上游一致、同步时不要改回、也不要补回的(2026-11-23 维护者给出的完整清单;除 A / B / C 外,main 里没有别的自有键,也没有别的有意缺失的键):
  A. 键保留,main 有意不引用:
    - 激活码类、comingSoon 的键;
    - 教程类的键:后端设置页顶部的「使用教程」按钮(YouTube 图标,tutorialButton / tutorialHint / tutorialChecking / tutorialCheckFailed)。main 不放这个按钮,也不做教程视频列表相关的面板与后端;
    - marketPromo、aiRelayLabel、aiRelaySubscription、aiRelayOnDemand(上游已不引用,main 也不引用)。
  B. 键保留,但 main 的显示或代码有意不同:
    - 链式代理空状态:不显示「安格超市」链接。ChainProxyManager.vue 若出现 chainProxyEmptyHintSuffix 或该链接,删除;
    - 客户端下载卡:不要「极速下载用 DownMan」。ClientDownloadsCard.vue 若出现 DOWNMAN_URL 或 clientDownloadFastWith,删除;
    - 概览页顶部文案:「机场、VPS 安格超市 | 原生住宅 IP TOPIP」改为 Open-Box Latest;「AI 中转站 · 订阅服务 SUPERDOOR | 按需付费 OPENDOOR」改为魔改 sing-box 最新版本号。
  C. 整体不做:键、面板、后端、core 全部没有(覆盖核心原则第 1 条):
    - 「业务优选」相关的一切都不要:设置页「业务优选」卡、概览页「业务优选」统计卡、内核按连接证据选路,以及对应的接口、档案字段和三语键;
    - 键按前缀识别,上游以后新增同类的键也不补:businessOverview*、businessPreference*、businessSettingsHelp*、businessReliabilityHalfLife、businessPerformanceTtl、bandwidthHelpCurrentBusiness;
    - 注意:ipTypeBusiness(商业宽带)与业务优选无关,照常按上游保留;
    - 核对键集和引用时,把以上键从「缺失」中剔除。

维护者记录
- (2026-11-23)有意例外整理成 A / B / C 三类(见上),其中 C「业务优选整体不做」此前只在提交说明和第三节里提到,第五节没写;跟随上游同步面板、后端、core 时一律按这份清单取舍,不用再问。
- (2026-11-08)DNS 上游(直连 / 代理 / 备用)与订阅的节点专用解析(nodeDns)支持 DoT:协议加 tls,默认端口 853,地址是域名时生成 domain_resolver 加 udp bootstrap;nodeDns 的 protocol 取 https 或 tls。对齐上游 v0.1.422 的公开能力,按 sing-box 1.12+ DNS server 文档(type:tls / type:https)自写。dns_golden 里档案带 tls 协议的 buildDns 旧用例(基线把 tls 当非法协议退回默认)按有意差异跳过,新行为由 dnsupstream_test.go、builddns_dot_test.go、nodedns_dot_test.go 覆盖。
- (2026-11-08)对齐限制尽量放开:闭源对齐靠看行为,不必在文档里给自己加禁止条款。
- (2026-11-02)loadbalance 按上游 v0.1.387 行为对齐(字段、默认 120s、成员只收节点、上限 64、空 outbounds 照写);自研实现与测试。
- (2026-11-02)前端 i18n 对齐上游 v0.1.388;有意例外见上。


# 六、来源与合规:现状与需要维护者处理的事

只陈述仓库里能看到的事实,不下法律结论;处理方式由维护者决定。接手的 AI 请据此避免继续扩大这些来源的使用。

事实
1. 上游(liandu2024/Open-Box)自 v0.1.321 起随包附带 open-box.LICENSE 和 open-box.COPYRIGHT.md:Open-Box 自有程序(服务端、原生核心、前端自有部分)为专有软件,
   允许在自己的设备上安装使用,禁止再分发、制作换名或重新打包的衍生版本、反编译和逆向恢复实现、绕过授权检查;第三方部分(zashboard 前端 MIT、sing-box GPL)不受此限制。
2. tag engine-snapshot(上游 v0.1.318 同步提交)里有可读的旧版 Node 服务端。我检查过该快照的许可文件:只有 Node.js 和 sing-box 的许可,没有 Open-Box 自己的许可文本,所以它的使用条件不明确。
3. 从提交记录能看到的 core 来源:
   - 大部分模块用 engine-snapshot 里的明文做基线生成金标准(多个"对齐上游 engine 基线"提交)。
   - 1651d5b 加了从压缩 bundle 抽取声明的工具 core/testdata/tools/extract-from-bundle.mjs。
   - b91e295、235a10b("对齐官方 v0.1.353 / v0.1.354")和 18f5684("金标准改读官方")来自对官方 openbox-core 的差分和对压缩 bundle 的增量核对。

建议维护者做的事(接手的 AI 不要自己决定)
AI 不替维护者做这些决定,也不为此停工,按下面的保守默认继续:不再从快照复制新代码、不扩大对它的依赖;基线金标准保持只读,不用官方二进制或压缩 bundle 重新生成或更新;README 与 `testdata/tools/` 里涉及上述做法的说明和工具保留不动、不要再运行(这是红线,见 backend/STATUS.md「对齐口径」)。
- 确认 v0.1.318 快照的许可条件。确认之前,不再从快照复制任何新代码,也不扩大对它的依赖。
- 决定现有金标准和相关生成脚本怎么处理:保留只读、还是按第四节 2 逐步换成自己写的测试向量。决定之前,不要再用官方二进制或压缩 bundle 重新生成或更新金标准。
- 决定 core/README.md 和 core/testdata/tools/ 里涉及上述做法的说明和工具要不要保留。


# 七、与维护者协作的约定

1. 用中文,简洁,先说结论。不要重复提醒他已经知道的事。
2. 交付方式见 backend/README.md「维护者的工作方式」:本机直推 main(SSH 别名 remote,不 force push);只有没有推送权限的沙箱才出 `.patch`。改 core 的提交说明以 `core:` 开头,只动 `core/`。
3. 沙箱模式下的约定(读他本机文件用 PowerShell 写剪贴板、不在磁盘上生成文件、他的 PowerShell 没有 `&&` / grep / awk 等)同 backend/HANDOFF.md 第七节。交付文档给他时用纯文本:不要表格,UTF-8 带 BOM、Windows 换行。
4. 沙箱的坑:沙箱里的 Go 可能低于 go.mod 要求(2026-11 是 1.24.7 对 1.25.12),又拉不到工具链:在仓库外放一份把 go 行降到 1.24 的 go.mod 副本(加空 go.sum),用 `GOTOOLCHAIN=local go test -modfile=<副本路径>` 跑,仓库里的 go.mod 不动。其余坑同 backend/HANDOFF.md 第七节第 5 条。


# 八、验证方法

1. 金标准逐条 reflect.DeepEqual;需要键序的逐字节比较(用结构体,字段顺序就是 JSON 输出顺序)。语料用固定种子随机生成,含类型错乱的脏数据,并打印覆盖统计。
2. 变异测试:先确认基线通过、测试没有被 t.Skip(go test -v 看);先放一个"空变异",它应该漏网,用来证明没有误报;
   每次测试加硬性超时(timeout 40 go test -timeout 15s);存活的变异要判断是不是等价变异,不是就补手写用例;
   用脚本做精确字符串替换并断言匹配存在,每次变异后从备份还原(sed 会被 gofmt 的对齐空格坑)。
3. 配置类输出用公开发布的 sing-box 跑 check;sing-box 的补丁字段官方版可能不认,check 失败时要区分是自己的错还是补丁字段。
4. 复刻旧 JS 行为时的坑:Number(null) 是 0 而 Number(undefined) 是 NaN,缺失键和 null 要区分(jsx 包里有 Undefined 与 nil);Math.round(-2.5) 是 -2;
   JSON.stringify(-0) 是 "0";JSON.stringify 不转义 U+2028/2029;|| 看真假而 ?? 只看 null 和 undefined;Map / Set 按插入顺序迭代;
   Go 的 map 序列化按字母序排键;切片一律非 nil,保证序列化成 [] 而不是 null。


# 九、提交记录

以 `git log -- core` 为准;更早的提交清单已删(都在 git 历史里)。
