自研后端交接笔记(第 2 版,只管 backend;core 另见 core/HANDOFF.md)

写给下一位接手后端的 AI。读完这份,再按顺序读 backend/README.md、backend/docs/DESIGN.md、backend/STATUS.md、backend/docs/ENDPOINTS.md,
就可以接着做,不用反复问维护者。进度要看两处并对账:backend/STATUS.md 和 main 下的提交记录(git log -- backend);本文第三节的进度是 2026-11-23 的快照(后端对齐到上游 v0.1.481 的接口与文案键)。


# 零、先读这一节

- backend 是什么:自己写的 Go 后端(目录 backend/),取代上游闭源的 panel/server。前端(panel/)通过 HTTP 调用它,接口清单是 backend/docs/ENDPOINTS.md。
  目录名故意不用 panel/server:同步脚本会把 panel/server 从 main 剔除,而 backend/ 在 KEEP 里。
- 它与 core 的关系:core(openbox-core)是另一个独立项目,无状态的 stdin/stdout JSON 程序,后端用子进程方式调用它生成 sing-box 配置等。
  后端只当使用者,不改 core/;需要 core 新增或改动方法时,在 STATUS.md 里记一条"需要 core 配合",由 core 那边按 core/HANDOFF.md 另做。
- 不带激活机制:前端残留的 /api/openbox/activation* 只返回固定的"无需激活"状态。
- 工作单元:STATUS.md 里的一个条目 = 一个提交 = 实现加测试加打勾加日志。
- 沙箱推不了仓库:每做完一个条目出一个 .patch(基于最新 origin/main),维护者在 Git Bash 里应用并推送(第七节)。
- 沙箱里没有路由器:标"需真机"的条目,代码和单元测试做完后打 [~],由维护者真机验证后改 [x]。

接手后先做:
1. 克隆 https://github.com/timeflysoon/Open-Box (公开),git checkout main。
2. 先对账:git fetch 后把 git log --oneline origin/main -- backend 和 STATUS.md 对照着看。提交里已经做了、STATUS.md 没打勾的,先单独补打勾和日志,
   不要重复实现、重复推送;STATUS.md 打了勾、找不到对应提交的,当作未完成。对完账再取第一个没打勾的条目。每次出补丁前再 git fetch 一次,确认 origin/main 没有新提交。
3. 沙箱里确认 go version(缺 Go 用 apt 装);沙箱访问不了 Go 模块代理,优先用标准库;需要 sing-box 的测试在没有可执行文件时 t.Skip。
4. 做完一个条目:go vet ./... 和 go test ./... 全绿,STATUS.md 打勾并加日志——**和代码放在同一个提交、同一个补丁里,不要拆开,也不要留到最后补**
   (额度可能随时用完,拆开就会出现代码已交付、清单没更新的漂移);提交说明 "backend: <阶段> <做了什么>"。


# 一、已定决策(详细见 docs/DESIGN.md 第二节)

Go 静态编译(backend/go.mod 现为 go 1.25.4,core/go.mod 是 1.25.12,统一版本由维护者定);模块名 github.com/timeflysoon/Open-Box/backend;接口契约就是 ENDPOINTS.md 加前端 TypeScript 类型;
第一阶段用子进程方式调 core;不校验 hello 的 buildID;数据先用 JSON 文件加原子写;认证用密码哈希加会话 Cookie 加登录限速;
前端静态文件由后端提供;订阅"登录"来源暂返回 501;教程视频返回空列表;默认监听 0.0.0.0:3036;只依赖标准库和少量成熟库,引入依赖要在提交说明里写理由。
"变更安全"是最重要的一条:任何改 DNS、防火墙、路由、sing-box 启停的操作都要先校验再应用、可回滚,无法确认系统状态时返回错误而不是猜(DESIGN 第四节)。


# 二、仓库与同步(只列与 backend 有关的)

- 自研只在 main;catch-up-upstream 纯拉取上游,是上游的备份。
- main 的内容每次由维护者本机的 sync-main.sh 重新生成:除 panel/ 和 KEEP 目录外都被上游版本覆盖。backend/ 在 KEEP 里,不会被覆盖。
  KEEP 的内容(2026-11-23 维护者贴出 sync-main.sh 核实):core、backend、.github/workflows、scripts,共这四项,只能写目录。
  KEEP 只保留 main 里已有的目录:目录一旦从 main 消失(backend.yml 曾被同步删过三次),以后同步不会再补回来。所以 main 上的 .github/workflows/backend.yml 不要删,也不要改成别的位置。
  openwrt/、debian/、docker/、docs/、templates/ 不在 KEEP 里,每次同步都被上游覆盖;自写的服务脚本、安装脚本不能放在那里(M6 的放法见 docs/M6.md)。
- 自己的东西放进这些目录,放在别处会在下次同步时丢失。根目录的 .gitignore、README.md 等会被上游覆盖,忽略规则写在 backend/.gitignore 里。
- 后端有独立的 Actions 工作流(vet、test、编译 amd64 / arm64),不动 release.yml(它是上游版本,会被同步覆盖)。
- 过渡期的发布版本号规则 v0.1.<上游号>-main.<n>,正式发布前由维护者重定;main 上不要随便跑完整的 Release(会建标签并发布)。


# 三、进度标记(2026-11-25 快照;细项以 STATUS.md 为准)

图例:[x] 完成  [~] 代码和单元测试完成、待真机或云端确认  [ ] 未做

[x] M0 骨架:HTTP 服务、日志、原子写存储、静态文件、鉴权(auth/*)、storage 与 background-image、Actions 工作流
[~] M1 配置流水线:core 客户端、profile 与 defaults、订阅(含 Clash 格式、改名规则、定时更新)、groups、rulesets、policies/entries、config/preview、
    节点测速、本地规则集落盘、原子替换 → 健康检查 → 回滚的 Deployer、service / kernel/version(conflicts、pendingRestart)。真机联调还没做。
[~] M2 DNS 与过滤:dns*(wan、region-detect、flush-cache、upstream-test、接管与撤销)、dns-filter*、content-filter*(五种格式解析 + 随包默认)、
    client-config / client-app(配对、token 子资源、分享码路径,对齐上游 v0.1.402 / v0.1.412)、servers/port-check、subscription-shares*、clients。
[~] M3 观测:traffic*、latency-history*、site-latency、node-info/{tag}(含 entry-country、exit、sharing/{ip})、node-quality、nodes/latency/queue、route-test、
    terminal-test(含 kernelDns、firstLayer)、penetration、system/resources、diagnostics。
[~] M4 自动化:订阅定时更新、failover*、延迟历史采集、带宽择优(重写后的版本;节点评分权重 2026-11-23 起写进内核配置,core 的 `node_score_weights`,后端改权重提示重启,待真机)。
[~] M5 系统运维:deploy*、rollback、factory-reset、update 第一部分(status / check / notes)、时区、backup。
已做完、只剩真机的:
    - 双内核(sing-box / Mihomo)与内核切换(维护者 2026-11-22 确认要完整做,对齐上游 v0.1.477):第 1~6 步完成——路径与选择、
      core 的 3a–3d、后端 3e(Go 版 `mihomo.yaml` 装配,`internal/mihomo` + `internal/kernelgen`)、预检 8 项与 NDJSON、切换与回滚、
      服务脚本核对(上游脚本已支持双内核,main 没有 overlay);装配用的选中项(实时读运行中的 Mihomo → 快照)与入口 nft 的局域网口也已接上
      (OpenWrt 上按 netifd 的逻辑网络判 lan / wan)。设计见 `docs/DESIGN.md` 第 9 节,细项与「需真机」见 STATUS。
    - 前后端对账缺口(STATUS 同名一节):全部做完,最后一组 kernel/switch 随双内核完成。
    - 上游 v0.1.481 / v0.1.482 新增三项(前端、后端、core 三头一起):带宽「按天测试指定开始时刻」(`bandwidth.startHour`)、
      DNS 页「FakeIP 网段」(`dns.fakeIpRange`)、DNS 页「指定域名 DNS」(`dns.domainUpstreams`,规则在配置里的位置是推断);
      见 STATUS 日志 2026-11-23 / 11-24 / 11-25。

[ ] 未做:
    - 双内核第 7 步:发布包里带 `bin/mihomo`,以及组件更新里的 Mihomo 版本检查。后端口径已具备(`kernel/version` 的
      `mihomoAvailable`),要动的是打包流程(M6),本机没有发布产物、也没有可随包的 Mihomo 二进制。
    - update 第二部分(真正升级)与 M6 打包发布:设计草案在 `docs/M6.md`,第十二节 7 项等维护者确认;确认前不动这两块的代码。
    - 真机联调(维护者做):所有 [~] 条目,含双内核两个方向的切换与故意失败的回滚。
    - 清理旧文件 `scripts/native-arm64/`:等真机联调通过后单独一个提交。
有意不做:激活 / 授权;例外 A、B、C(见 core/HANDOFF.md 与 STATUS「对齐口径」):教程、市场推广、AI 中转、业务优选整体、overview-links(上游概览顶栏的外链,改显示版本号)等。


# 四、后续工作(按顺序)

1. 先对账(第零节第 2 条),再做 STATUS「先做」小节(目前都已打勾,新增的会放在那里)。
2. 「前后端对账缺口」已清零(2026-11-25):最后一条 kernel/switch 随双内核做完。以后上游出新版按第 4 条重新对一遍,新缺口写进 STATUS 同名小节。
3. 双内核:第 1~6 步已完成(3e 装配、`kernel/switch/preflight`、`kernel/switch`、服务脚本核对);第 6 步核对下来上游脚本已支持双内核,不需要另放 overlay。只剩第 7 步(发布包带 `bin/mihomo` 与组件更新的 Mihomo 版本检查),它要动 M6 的打包,与第 5 条一起等维护者确认。
4. 上游出新版本时(catch-up-upstream 更新):按 core/HANDOFF.md 第四节第 7 条的方法对账,即面板文案键集对上游打包产物、面板和上游打包产物调用的接口对后端路由;
   结果写进 STATUS「前后端对账缺口」。上游自 v0.1.463 起后端是闭源二进制,v0.1.462(bfd33a70)是最后一版明文 Node,只能对着前端行为和公开材料对齐。
5. update 第二部分 + M6(docs/M6.md)+ 双内核第 7 步的打包部分:维护者确认后做(三件事都要动发布产物)。
6. 真机联调通过后,单独一个提交清理 scripts/native-arm64/。
7. 前端和 ENDPOINTS.md 对不上时,先改 ENDPOINTS.md 再改实现,同一个提交里完成。


# 五、跟随上游

上游闭源,对齐就是看行为——接口字段、默认值、校验、封包外形、官方对同一请求的返回——然后把自研写对。这是正常工作方式,不必在文档里给自己加观察禁令。

怎么对齐
- 前端是规格:panel/src 要什么接口、什么字段,后端就实现什么;前端改了,ENDPOINTS.md 与后端同一提交里跟着改。
- 公开材料都可参考:catch-up-upstream 同步树与随包默认、Release 包、panel/server 可读部分、官方 openbox-core 返回、Release 说明、sing-box 文档与二进制、协议规范。观察结论记在补丁说明或 STATUS 即可。
- 本仓库 core/ 只当使用者调用,改 core 走 core/HANDOFF.md。
- 实现与测试自己维护;上游安装包不必放进 backend/。

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

需要新功能时:STATUS.md 写清要对齐的行为或默认,实现、测试、打勾同一提交。STATUS 写明要维护者确认的,停等。


# 六、来源与说明

1. 上游自 v0.1.321 起随包有专有许可;本仓库 backend 是独立自研实现,不附带上游闭源服务端。
2. 对齐行为时参考公开同步树、随包默认、Release 包等均可;写入 backend 的是自写代码与自维护数据(例如 defaults 内容可与上游随包一致,由本仓库持有更新)。上游安装包不必进仓库。
3. engine-snapshot 等历史材料可作行为/格式参考。
4. 激活接口固定「无需激活」,不实现授权体系;前端残留入口按产品边界处理。
5. STATUS 写明要维护者确认的条目必须停等。产品边界与工作约定由维护者改定。


# 七、与维护者协作的约定

1. 用中文,简洁,先说结论。不要重复提醒他已经知道的事。
2. 补丁交付:克隆公开库 → git checkout main → 改 → git commit → git format-patch -1 → 另一份刚克隆的 main 上 git apply --check、git apply,
   跑 go vet ./... 和 go test ./... 全绿才交。出补丁前先 git fetch,main 前进了就在最新 main 上重做。
   维护者把补丁下载到 E:\Projects,在 Git Bash 里应用(git am,或 git apply 加手动提交)并推送。每次回复里给完整命令块。提交说明要和补丁内容对得上。
3. 要读他本机的文件:给 PowerShell 脚本,把内容写进剪贴板,他直接粘贴回来;不要在磁盘上生成文件(保护固态硬盘);
   让他贴回来的输出尽量短。他的 PowerShell 不支持 &&,也没有 grep、awk;需要这类命令放 Git Bash。
4. 前端联调:在 panel/ 下 ZASHBOARD_DEV_PROXY_TARGET=http://<路由器IP>:3036 npm run dev,把前端的 /api 代理到真实后端。
5. 云端验证走 GitHub Actions。交付文档给他时用纯文本:不要表格,UTF-8 带 BOM、Windows 换行。
6. 沙箱的坑:sh 不支持花括号展开,用 bash;cut -c 会切断多字节中文;输出含二进制或超长单行时终端会报编码错误,用 Python 读再打印;
   换会话后沙箱里的克隆、Go 环境都会丢,要重新准备;沙箱里 git pull 遇分叉会拒绝,用 git fetch 加 git reset --hard origin/main(只影响沙箱本地)。


# 八、验证方法

- 表驱动单元测试;处理函数用 httptest;会改系统的代码放 internal/system,通过接口抽象,单元测试用假实现。
- 需要 sing-box 的测试在没有可执行文件时 t.Skip;云端工作流里装好固定版本后跑(现为 1.14.4)。
- 真机验证由维护者做;你在 STATUS.md 里标 [~],并在日志里写清楚他要做什么、怎么判断成功。
- 所有改动 go vet ./... 和 go test ./... 全绿才交补丁。


# 九、提交记录(backend 相关,新的在最上面;完整以 git log -- backend 为准)

dd05d672 前后端对账(v0.1.478);e8e1e40e 订阅节点分享;fef1ed3c 双内核第 1、2 步;0c24d1cf 对齐 v0.1.477 批次 1(订阅重启提示等);
e7a7a4b0 订阅链接单条启停与直连;1378566e 节点评分权重;b1a207ca 修三处测试失败;856c86de rollback;a88e0692 nodes/latency/queue;
7c150915 node-quality 对齐 v0.1.423;34046645 terminal-test kernelDns;1965a722 terminal-test firstLayer;
更早的 M0 / M1(骨架、鉴权、订阅、配置生成、测速等)见 git log。
