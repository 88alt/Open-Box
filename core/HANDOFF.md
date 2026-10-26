Open-Box 自研部分交接笔记(第 5 版:core + backend 合并版)

写给下一位接手的 AI。读完这份、backend/README.md、backend/STATUS.md,就可以接着做,不用反复问维护者。
进度的"实时"来源是 git log 和 backend/STATUS.md;本文的进度是 2026-10-26 的快照,与它们冲突时以它们为准。

========================================
零、先读这一节(一分钟看懂全局)
========================================
- 目标:前端(panel/)、核心(core/,Go)、后端(backend/,Go)三部分全部自己写、自己维护、不依赖也不打包上游的闭源程序,也不带激活机制。
- 三部分的关系:前端通过 HTTP 调用后端(接口清单 backend/docs/ENDPOINTS.md);后端用子进程方式调用 core(openbox-core,无状态,stdin/stdout JSON);
  后端再管 sing-box 内核的配置、启停。sing-box 内核本身不自己做(见一、3)。
- 做法:core 靠"金标准"数据逐条对照来保证正确;backend 靠前端接口清单当规格、一个条目一个提交、带测试。
- 沙箱推不了仓库:每做完一块,出一个 .patch(基于最新的 origin/main),维护者下载到 E:\Projects,在 Git Bash 里应用并推送(第七节)。
- 云端验证走 GitHub Actions;沙箱里没有路由器,标"需真机"的条目只能由维护者验证。

接手后先做这几件事:
1. 克隆 https://github.com/timeflysoon/Open-Box(公开库),git checkout main(默认分支是 catch-up-upstream,里面没有 core/ 和 backend/)。
2. git log --oneline -- backend core 看最近做到哪;读 backend/STATUS.md,取第一个没打勾的条目。
3. 读 core/README.md(协议、方法表、目录)和 backend/docs/DESIGN.md(已定的决定、变更安全规则)。
4. 沙箱里先确认 go version、node -v(缺 Go 用 apt 装;沙箱访问不了 Go 模块代理,别依赖第三方模块,标准库优先)。
5. 做完一个条目:测试通过、STATUS.md 打勾并写日志、提交说明用 "backend: <阶段> <做了什么>" 或 "core: <模块> <做了什么>"。

========================================
一、已定决策
========================================
1. 全部自研,不带激活机制。后端没有"授权"概念:前端残留的 /api/openbox/activation* 只返回固定的"无需激活"状态。
2. 后端目录是 backend/(不是 panel/server)。panel/server 是上游闭源后端的位置,main 里不放它。
3. sing-box 内核不自己做,取公开发布的成品;core/singbox/ 只是预留目录。
4. 核心不对 sing-box 补丁字段(如 urltest 的 expected_status)按内核版本降级:需要配套内核,换成原版时内核启动或 check 会直接报错。以后要支持其他内核再单独做版本探测。
5. Go 版本 1.25.12(core/go.mod 与 backend/go.mod 一致,CGO_ENABLED=0 静态编译)。
6. 发布版本号过渡期用 v0.1.<上游号>-main.<n>,正式发布前由维护者重定。
7. 数据先用 JSON 文件加原子写,不引入数据库;默认监听 0.0.0.0:3036。

========================================
二、仓库、分支与同步机制(很重要)
========================================
仓库 timeflysoon/Open-Box(公开)。分工明确:catch-up-upstream 纯拉取上游,自研只在 main。

- catch-up-upstream:上游发布包的同步结果(sync-upstream.yml 写入)。目前 release 只在这个分支,作用只是备份上游。不要在这里放自研代码。
- main:以 catch-up-upstream 为底,再由维护者本机的 E:\Projects\sync-main.sh 生成。除 panel/(去掉 panel/server、panel/.tools、panel/.github、panel/AGENTS.md)
  和 KEEP 里的目录之外,main 的一切内容每次同步都会被上游版本覆盖。
- KEEP 的目标内容:core、backend、.github/workflows、scripts。实际以维护者本机的 sync-main.sh 为准;如果你要往这些目录之外放自己的东西,先确认 KEEP。
  KEEP 只能写目录,不能写单个文件。根目录的 .gitignore、README.md 等会被上游覆盖,自研目录的忽略规则写在目录自己的 .gitignore 里。
- 一个已发生的教训:.github/workflows/release.yml 里曾经加过 core 任务(按 core/go.mod 装 Go 1.25.12、vet、test、双架构编译,可手动只跑它),
  验证通过一次(Actions Release #108),后来被同步覆盖掉了。需要时从历史取回:git show f4d044e:.github/workflows/release.yml。
- 同步后的验证脚本由维护者在 Git Bash 里一次粘贴运行,里面的 keep / own 目录列表要和 sync-main.sh 的 KEEP 一致。
- backup 分支:开源时期的完整旧 Node 后端(最后提交 9 月 13 日),只读;tag engine-snapshot(提交 0c9d530,上游 v0.1.318 快照)保留不删。它们的使用条件见第六节。
- backend 有独立的 Actions 工作流(vet、test、编译 amd64 / arm64),不动 release.yml。

========================================
三、进度标记(2026-10-26 快照)
========================================
图例:[x] 完成  [~] 代码完成、待真机或待云端确认  [ ] 未做

core(openbox-core,Go,stdin/stdout JSON,15 个方法加 hello)
[x] 协议、hello、大小限制、panic 恢复
[x] routing:normalizePolicy、normalizeRouting、routingFingerprint、migrateStoredRouting、classes、forwardPlan、bypassPlan、entryPlan
[x] groups:normalizeGroups、builtinTags、emitGroups
[x] emit(节点转 sing-box 出站 / endpoint)、clientroutes、dns(含 upstream 与热切换 flip)、route
[x] 叶子模块:dnsfilter、ipx、servers、directhosts、dnsrewrite、idna、urlx
[x] config:configDetailed 装配(站点直连、链式节点核对、节点组 / 站点集、dns / route / tun 入站、入口旁路、共享网络、FakeIP 缓存)
[x] 授权三个方法(checkActivationCode、activationState、authorizationState)返回固定的"未限制"状态;新后端不调用它们
[x] go.mod 对齐 Go 1.25.12
[~] 真机联调:真实路由器上用后端调用 core、sing-box 能启动(见 backend 的 M1 真机条目)
[ ] 见第四节"core 后续"

backend(Go,backend/,详细条目以 backend/STATUS.md 为准)
[x] M0 骨架:HTTP 服务、日志、原子写存储、静态文件、鉴权(auth/*)、storage、Actions 首次运行全绿
[x] M1 已完成:core 子进程客户端、profile 与 defaults、订阅(拉取 / 解析 / 增删改查 / 排序 / 刷新 / 预览 / 改名规则 / Clash 格式)、groups、
    rulesets 的 preview 与 import、config/preview(配置生成)、nodes/latency 与 chain-proxies/latency(节点测速)
[~] M1 配置生成与校验:Check 只用假的 sing-box 脚本测过,需真机用真实内核验证
[ ] M1 未完成:policies/entries、rulesets entries 与 refresh、原子替换与健康检查与回滚、本地规则集文件落盘、service、kernel、真机联调
[ ] M2 DNS 与过滤;M3 观测;M4 自动化(订阅定时更新、failover);M5 系统运维(backup、deploy、factory-reset、update 等);M6 打包与发布

仓库与流水线
[x] backend 的独立 Actions 工作流
[ ] main 自己的发布流程(目前 release.yml 就是上游版本,只用于 catch-up-upstream 的备份发布)
[ ] 清理 scripts/native-arm64/(真机联调通过之后,单独一个提交)

========================================
四、后续工作(按顺序)
========================================
backend:按 backend/STATUS.md 从上到下做,见 backend/README.md 的"怎么接着做"。优先级最高的是 M1 里还没做的部署部分
(原子替换、健康检查、回滚、规则集文件落盘、service、kernel),因为没有它们生成的配置跑不起来;然后是 M1 真机联调。

core 后续:
1. 补缺口(都是已知且影响小的):
   - idna / urlx 没有 UTS46 字符映射表,含特殊字符的主机名在站点直连里可能多收或漏收一个域名;补齐需要引入 Unicode 映射表。
   - clientBindings:核心收下但不用,终端分流不按 IPv6 绑定扩展来源。
   - 有意保留的差异(不算缺口):BOM、__proto__ 键、非 ASCII 主机转 punycode。
2. 测试数据的来源整理(见第六节):把现有金标准逐步换成"自己写的测试向量 + 真实 sing-box check",让今后改核心不再依赖上游程序的输出。
3. 跟着 sing-box 更新(见第五节):sing-box 发新版本、配置格式变化时,更新 core 输出和对应测试;backend 的 Actions 里固定了一个 sing-box 版本(现为 1.14.4),升级时一起改,并跑 sing-box check。
4. 授权相关的三个方法:新后端不调用,先保留,等后端稳定后再决定是否删除。
5. core/UPSTREAM_VERSION:现在是 v0.1.354,是"最后对齐的上游版本"的记录。第五节的新规则下不再按上游版本推进它;处理方式由维护者定,默认不再改动。

========================================
五、"跟随上游"怎么做(重要:范围写得很窄)
========================================
维护者的意愿:上游会持续更新,自研部分除了补齐缺失的组件,还要跟着更新。下面是可以做的:

允许的来源
- 自己的前端(panel/src):前端是接口规格。前端改了接口或类型,后端和 backend/docs/ENDPOINTS.md 同一个提交里跟着改。
- sing-box 官方文档和发布说明:配置格式、字段、行为的变化,以官方为准;用官方发布的 sing-box 二进制跑 check 验证。
- 上游公开发布的说明性文字(Release 说明、README、docs/ 里面向用户的功能介绍):只用来了解"上游新增了什么功能",再按自己的设计实现。
- 协议规范、OpenWrt / Debian 官方文档。

本文不提供、也请接手者不要做的
- 从上游发布包里的压缩文件(index.mjs 等)抽取代码或行为,以此对齐自研实现。
- 运行上游的官方 openbox-core 二进制,把它的输出当标准答案做差分,或为了让它"肯跑"而保持它要求的目录和哈希校验。
- 让自研核心去满足上游 Node 服务端的 buildID / manifest 校验,或把自研核心换进上游的发布包里重新发布。
- 任何授权 / 激活的绕过实现。
理由见第六节。需要新功能时,从前端需要什么、sing-box 能做什么出发,自己设计。

遇到"想跟上游某个新功能"怎么办
1. 在 backend/STATUS.md 里加一个条目,写清楚用户可见的行为(依据:前端界面、公开说明)。
2. 按 backend/docs/DESIGN.md 的约定自己实现并写测试。
3. 实现细节拿不准时,选保守、可回滚的做法(见 DESIGN 的"变更安全"),并在提交说明里写明这是自己的设计。

========================================
六、来源与合规:现状与需要维护者处理的事
========================================
这一节只陈述仓库里能看到的事实,不下法律结论;处理方式由维护者决定。接手的 AI 请据此避免继续扩大这些来源的使用。

事实
1. 上游(liandu2024/Open-Box)在 v0.1.321 起随包附带 open-box.LICENSE 和 open-box.COPYRIGHT.md:Open-Box 自有程序(服务端、原生核心、前端自有部分)为专有软件,
   允许在自己的设备上安装使用,禁止再分发、制作换名或重新打包的衍生版本、反编译和逆向恢复实现、绕过授权检查;第三方部分(zashboard 前端 MIT、sing-box GPL)不受此限制。
2. tag engine-snapshot(上游 v0.1.318 同步提交)里有可读的旧版 Node 服务端。我检查过该快照的许可文件:只有 Node.js 和 sing-box 的许可,没有 Open-Box 自己的许可文本,所以它的使用条件不明确。
3. 从提交记录能看到的来源:
   - core 的大部分模块用 engine-snapshot 里的明文做基线生成金标准(多个"对齐上游 engine 基线"提交)。
   - 1651d5b 加了从压缩 bundle 抽取声明的工具 core/testdata/tools/extract-from-bundle.mjs。
   - b91e295、235a10b 的"对齐官方 v0.1.353 / v0.1.354",18f5684 的"金标准改读官方",来自对官方 openbox-core 的差分和对压缩 bundle 的增量核对。
   - 前端里订阅"登录"来源等几处,是照着上游新版编译后的前端对齐出来的。

建议维护者做的事(接手的 AI 不要自己决定)
- 确认 v0.1.318 快照的许可条件。确认之前,不再从快照复制任何新代码,也不再扩大对它的依赖。
- 决定现有金标准和相关生成脚本怎么处理:保留只读、还是逐步换成自己写的测试向量(第四节 2)。在决定之前,不要再用官方二进制或压缩 bundle 重新生成或更新金标准。
- 决定前端里按上游编译产物对齐的部分是否保留。
- 决定 core/README.md、core/testdata/tools/ 里涉及上述做法的说明和工具要不要保留。

========================================
七、与维护者协作的约定
========================================
1. 用中文,简洁,先说结论。不要重复提醒他已经知道的事。
2. 补丁交付:克隆公开库 → git checkout main → 改 → git commit → git format-patch -1 → 另一份刚克隆的 main 上 git apply --check、git apply,
   跑 go vet ./... 和 go test ./... 全绿才交。出补丁前先 git fetch,main 前进了就在最新 main 上重做。
   维护者把补丁下载到 E:\Projects,在 Git Bash 里应用(git am,或 git apply 加手动提交)并推送。每次回复里给完整命令块。
   提交说明要和补丁内容对得上。补丁里不要放 core / backend 以外会被同步覆盖的东西,除非那是 KEEP 里的目录。
3. 要读他本机的文件:给 PowerShell 脚本,把内容写进剪贴板,他直接粘贴回来;不要让他在磁盘上生成文件(保护固态硬盘);
   让他贴回来的输出尽量短(只取必要的行)。他的 PowerShell 不支持 &&,也没有 grep、awk;需要这类命令放 Git Bash。
4. 云端验证用 GitHub Actions。完整的 Release(不勾 core_only 之类的开关)会建标签并发布,在 main 上不要随便跑。
5. 交付文档给他时用纯文本:不要表格,UTF-8 带 BOM、Windows 换行。
6. 沙箱的坑:sh 不支持花括号展开,用 bash;cut -c 会切断多字节中文;输出含二进制或超长单行时终端会报编码错误,用 Python 读再打印;
   换会话后沙箱里的克隆、基线、Go / Node 环境都会丢,要重新准备;沙箱里 git pull 遇分叉会拒绝,用 git fetch 加 git reset --hard origin/main(只影响沙箱本地)。

========================================
八、验证方法(沿用)
========================================
- core:金标准逐条 reflect.DeepEqual;需要键序的逐字节比较。做变异测试证明测试有效:先确认基线通过且没有被 t.Skip,
  放一个"空变异"应该漏网;每次测试加硬性超时(timeout 40 go test -timeout 15s);存活的变异要判断是不是等价变异。
- backend:表驱动单元测试;处理函数用 httptest;依赖 sing-box 的测试在没有可执行文件时 t.Skip,云端装好后跑。
- 所有改动:go vet ./... 和 go test ./... 全绿;会改系统的代码用假实现做单元测试,真机验证由维护者做并在 STATUS.md 里改 [~] 为 [x]。
- JS 语义坑(core 里复刻旧 Node 行为时用到):Number(null) 是 0 而 Number(undefined) 是 NaN,缺失键和 null 要区分;Math.round(-2.5) 是 -2;
  JSON.stringify(-0) 是 "0";JSON.stringify 不转义 U+2028/2029;|| 看真假而 ?? 只看 null 和 undefined;Map / Set 按插入顺序迭代;Go 的 map 序列化按字母序排键。

========================================
九、提交记录(main,新的在最上面;完整以 git log 为准)
========================================
backend:M1 节点测速(5aa6e21)、配置生成与 sing-box check(244daf3)、rulesets preview / import(057fd18)、groups(05fda53)、订阅改名规则(b68287c)、
        Clash 格式解析(40dcca8)、订阅接口(9a27dff)、订阅拉取与解析(a978177)、profile 与 defaults(e325a79)、core 客户端(0684316);
        M0:Actions 首次全绿(9b32137)、工作流(1665127)、storage(46d9571)、auth(775da48)、静态文件(292f024)、存储(73c7491)、HTTP 服务(4542ff3)、骨架(8782bcc);
        设计文档、接口清单、进度清单与交接规则(3ffa443)
core:  d5fb0a0 go.mod 对齐 Go 1.25.12;3bb9345 去掉 allow_unaligned 与对齐检查;18f5684 groups;cbc9db1 UPSTREAM_VERSION=v0.1.354;235a10b 对齐 v0.1.354;
        b91e295 对齐 v0.1.353;8ff1319 configDetailed 装配;b1e7fcf config 零件;61e9f57 / f9b6ed9 urlx 与叶子模块;7722ae8 route;dea84db dns;
        1eb1817 routing;2561ee5 groups;ec0070b emit;90424c9 clientroutes;1651d5b 抽取工具;e3aefe9 route;693e359 dns;5be5ffc dnsfilter;18befd6 叶子模块;
        b39d24b emit;587c51c groups;6caa3dd entryPlan;0e1fcdd classes / forwardPlan / bypassPlan;7750b8a routing 基础;16b0966 骨架
CI:    f4d044e 在 release.yml 加 core 任务(已被同步覆盖,可从历史取回)
