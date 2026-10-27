openbox-core 交接笔记(第 5 版,只管 core;后端另见 backend/HANDOFF.md)

写给下一位接手 core 的 AI。读完这份和 core/README.md,就可以接着做,不用反复问维护者。
进度要对账:main 下的提交记录(git log -- core)是实时来源,本文第三节是 2026-10-26 的快照;两者冲突时以提交记录为准,并在补丁里顺手更新第三节。


# 零、先读这一节

- core 是什么:自己写的、开源的 Go 程序 openbox-core(目录 core/)。无状态的 stdin/stdout JSON 程序,不是服务;调用方每次拉起它一次。
  协议、方法表、目录结构以 core/README.md 为准。共 15 个方法加 hello。
- 谁调用它:自研后端(backend/,另一个独立项目,见 backend/HANDOFF.md)以子进程方式调用。core 本身不含 HTTP、存储、订阅解析、部署,这些都在后端。
- 不加激活机制。授权相关的三个方法保留为"未限制"的固定返回,新后端不调用。
- 正确性靠"金标准":同一份输入喂一个标准答案程序,得到的输入输出存成 testdata/*.json,Go 测试逐条对照;再用变异测试证明测试有效。
- core 与 backend 分工清楚:改 core 的提交只动 core/,提交说明以 "core:" 开头;后端需要 core 新增或改动方法时,在 backend/STATUS.md 里记一条"需要 core 配合",由 core 这边另做。
- 沙箱推不了仓库:每做完一块出一个 .patch(基于最新 origin/main),维护者在 Git Bash 里应用并推送(第七节)。

接手后先做:
1. 克隆 https://github.com/timeflysoon/Open-Box (公开),git checkout main(默认分支 catch-up-upstream 里没有 core/)。
2. 先对账:git fetch 后看 git log --oneline origin/main -- core,和第三节、core/README.md 的进度表对照。提交里已经做了、文档没标的,以提交为准,
   不要重复实现、重复推送。每次出补丁前再 git fetch 一次,确认 origin/main 没有新提交。
3. 沙箱里确认 go version、node -v(缺 Go 用 apt 装;沙箱访问不了 Go 模块代理,只用标准库)。
4. 按第四节的顺序做;每块做完 go vet ./... 和 go test ./... 全绿再出补丁。第三节的进度标记要**和代码放在同一个补丁里更新**,不要留到最后补
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
- CI:核心的云端编译与测试曾放在 release.yml 的 core 任务里(验证通过一次,Release #108),后来被同步覆盖了,可从历史取回:
  git show f4d044e:.github/workflows/release.yml。后端另有自己的工作流;core 要云端验证时,在 backend 的工作流或新的独立工作流里做,
  不要依赖 release.yml(它是上游版本,会被同步覆盖)。


# 三、进度标记(2026-10-26 快照)

图例:[x] 完成  [~] 代码完成、待真机或待云端确认  [ ] 未做

[x] 协议、hello、大小限制、panic 恢复
[x] routing:normalizePolicy、normalizeRouting、routingFingerprint、migrateStoredRouting、classes、forwardPlan、bypassPlan、entryPlan
[x] groups:normalizeGroups、builtinTags、emitGroups
[x] emit(节点转 sing-box 出站 / endpoint)、clientroutes、dns(含 upstream 与热切换 flip)、route
[x] 叶子模块:dnsfilter、ipx、servers、directhosts、dnsrewrite、idna、urlx
[x] config:configDetailed 装配(站点直连、链式节点核对、节点组 / 站点集、dns / route / tun 入站、入口旁路、共享网络、FakeIP 缓存)
[x] 授权三个方法(checkActivationCode、activationState、authorizationState):固定的"未限制"返回
[x] go.mod 对齐 Go 1.25.12
[~] 真机验证:真实路由器上经后端调用 core、sing-box 启动成功(由维护者做,记录在 backend/STATUS.md 的真机条目)
[ ] 第四节里的后续工作


# 四、后续工作(按顺序)

1. 补已知缺口(都影响小):
   - idna / urlx 没有 UTS46 字符映射表:含特殊字符的主机名在站点直连里可能多收或漏收一个域名;补齐需要引入 Unicode 映射表(标准库没有,自己内置一份)。
   - clientBindings:核心收下但不用,终端分流不按 IPv6 绑定扩展来源。
   - 有意保留的差异,不算缺口:BOM、__proto__ 键、非 ASCII 主机转 punycode。
2. 整理测试数据的来源(见第六节):把现有金标准逐步换成"自己写的测试向量 + 真实 sing-box check",让今后改核心不再依赖别的程序的输出。
   做法:按方法逐个来,新向量由自己设计的输入加人工核对过的期望输出组成;配置类输出再用公开发布的 sing-box 跑 check。每换一个模块,变异测试照旧做。
3. 跟着 sing-box 更新(见第五节):sing-box 发新版、配置格式变化时,更新 core 输出和对应测试。backend 的工作流里固定了一个 sing-box 版本(现为 1.14.4),升级时一起改。
4. 授权三个方法:新后端不调用,先保留,等后端稳定后再决定是否删除。
5. core/UPSTREAM_VERSION(现为 v0.1.354):原本记录"最后对齐的上游版本"。按第五节的规则不再按上游版本推进,默认不再改动,处理方式由维护者定。
6. 真机联调反馈:维护者在真机上发现的 core 输出问题,按第三节的方式补测试再修。


# 五、"跟随上游"怎么做(范围写得很窄)

维护者希望自研部分跟着上游的新功能走。可以做的:

允许的来源
- 自己的前端(panel/src):前端是需求来源,前端要什么,core 的输入输出就按什么设计。
- sing-box 官方文档和发布说明:配置格式、字段、行为以官方为准;用官方发布的 sing-box 二进制跑 check 验证。
- 上游公开发布的说明性文字(Release 说明、README、docs/ 里面向用户的功能介绍):只用来了解"上游新增了什么功能",再按自己的设计实现。
- 协议规范、OpenWrt / Debian 官方文档。

本文不提供、也请接手者不要做的
- 从上游发布包里的压缩文件(index.mjs 等)抽取代码或行为,以此对齐自研实现。
- 运行上游的官方 openbox-core 二进制,把它的输出当标准答案做差分;或为了让它肯跑而保持它要求的目录和校验。
- 让自研核心去满足上游服务端的 buildID / manifest 校验,或把自研核心换进上游的发布包里重新发布。
- 任何授权 / 激活的绕过实现。
理由见第六节。要做新功能时,从前端需要什么、sing-box 能做什么出发,自己设计,并在 core/README.md 的方法表里记录。


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
- 确认 v0.1.318 快照的许可条件。确认之前,不再从快照复制任何新代码,也不扩大对它的依赖。
- 决定现有金标准和相关生成脚本怎么处理:保留只读、还是按第四节 2 逐步换成自己写的测试向量。决定之前,不要再用官方二进制或压缩 bundle 重新生成或更新金标准。
- 决定 core/README.md 和 core/testdata/tools/ 里涉及上述做法的说明和工具要不要保留。


# 七、与维护者协作的约定

1. 用中文,简洁,先说结论。不要重复提醒他已经知道的事。
2. 补丁交付:克隆公开库 → git checkout main → 改 → git commit → git format-patch -1 → 另一份刚克隆的 main 上 git apply --check、git apply,
   跑 go vet ./... 和 go test ./... 全绿才交。出补丁前先 git fetch,main 前进了就在最新 main 上重做。
   维护者把补丁下载到 E:\Projects,在 Git Bash 里应用(git am,或 git apply 加手动提交)并推送。每次回复里给完整命令块。提交说明要和补丁内容对得上。
3. 要读他本机的文件:给 PowerShell 脚本,把内容写进剪贴板,他直接粘贴回来;不要让他在磁盘上生成文件(保护固态硬盘);
   让他贴回来的输出尽量短(只取必要的行)。他的 PowerShell 不支持 &&,也没有 grep、awk;需要这类命令放 Git Bash。
4. 交付文档给他时用纯文本:不要表格,UTF-8 带 BOM、Windows 换行。
5. 沙箱的坑:sh 不支持花括号展开,用 bash;cut -c 会切断多字节中文;输出含二进制或超长单行时终端会报编码错误,用 Python 读再打印;
   换会话后沙箱里的克隆、Go / Node 环境都会丢,要重新准备;沙箱里 git pull 遇分叉会拒绝,用 git fetch 加 git reset --hard origin/main(只影响沙箱本地)。


# 八、验证方法

1. 金标准逐条 reflect.DeepEqual;需要键序的逐字节比较(用结构体,字段顺序就是 JSON 输出顺序)。语料用固定种子随机生成,含类型错乱的脏数据,并打印覆盖统计。
2. 变异测试:先确认基线通过、测试没有被 t.Skip(go test -v 看);先放一个"空变异",它应该漏网,用来证明没有误报;
   每次测试加硬性超时(timeout 40 go test -timeout 15s);存活的变异要判断是不是等价变异,不是就补手写用例;
   用脚本做精确字符串替换并断言匹配存在,每次变异后从备份还原(sed 会被 gofmt 的对齐空格坑)。
3. 配置类输出用公开发布的 sing-box 跑 check;sing-box 的补丁字段官方版可能不认,check 失败时要区分是自己的错还是补丁字段。
4. 复刻旧 JS 行为时的坑:Number(null) 是 0 而 Number(undefined) 是 NaN,缺失键和 null 要区分(jsx 包里有 Undefined 与 nil);Math.round(-2.5) 是 -2;
   JSON.stringify(-0) 是 "0";JSON.stringify 不转义 U+2028/2029;|| 看真假而 ?? 只看 null 和 undefined;Map / Set 按插入顺序迭代;
   Go 的 map 序列化按字母序排键;切片一律非 nil,保证序列化成 [] 而不是 null。


# 九、提交记录(core,新的在最上面;完整以 git log -- core 为准)

d5fb0a0 go.mod 对齐 Go 1.25.12;3bb9345 去掉 allow_unaligned 与对齐检查;18f5684 groups;cbc9db1 UPSTREAM_VERSION=v0.1.354;235a10b 对齐 v0.1.354;
b91e295 对齐 v0.1.353;8ff1319 configDetailed 装配;b1e7fcf config 零件;61e9f57 / f9b6ed9 urlx 与叶子模块;7722ae8 route;dea84db dns;1eb1817 routing;
2561ee5 groups;ec0070b emit;90424c9 clientroutes;1651d5b 抽取工具;e3aefe9 route;693e359 dns;5be5ffc dnsfilter;18befd6 叶子模块;b39d24b emit;
587c51c groups;6caa3dd entryPlan;0e1fcdd classes / forwardPlan / bypassPlan;7750b8a routing 基础;16b0966 骨架
