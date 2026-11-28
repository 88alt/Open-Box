自研后端交接笔记(第 3 版,只管 backend 的**规则与口径**;core 另见 core/HANDOFF.md)

写给下一位接手后端的 AI。读完这份,再按顺序读 backend/README.md、backend/docs/DESIGN.md、backend/STATUS.md,
改接口时读 backend/docs/ENDPOINTS.md。
**进度只在 backend/STATUS.md**,本文不记进度;**具体上游版本号只在 STATUS 的交接快照里写**,本文不写。
开工前先读 STATUS.md 文首的「交接快照」与「CI 门禁」。


# 零、先读这一节

- backend 是什么:自己写的 Go 后端(目录 backend/),取代上游闭源的 panel/server。前端(panel/)通过 HTTP 调用它,
  接口清单是 backend/docs/ENDPOINTS.md。
  目录名故意不用 panel/server:同步脚本会把 panel/server 从 main 剔除,而 backend/ 在 KEEP 里。
- 它与 core 的关系:core(openbox-core)是另一个独立项目,无状态的 stdin/stdout JSON 程序,后端用子进程方式调用它生成配置。
  后端只当使用者,不改 core/;需要 core 新增或改动方法时,在 STATUS.md 记一条「需要 core 配合」,由 core 那边按 core/HANDOFF.md 另做。
- 不带激活机制:前端残留的 `/api/openbox/activation*` 只返回固定的「无需激活」状态。
- 工作单元:STATUS.md 里的一个条目 = 一个提交 = 实现 + 测试 + 打勾 + 日志。
- 推送方式:AI 跑在维护者本机时,直接在 main 提交并推送(remote 是 SSH 别名,见 README),**不 force push**。
  只有拿不到推送权限的沙箱才出 `.patch`(第七节)。每批推送前后的检查见 STATUS 文首「CI 门禁」。
- 真机:设备与面板密码都已提供。标「需真机」的条目视为已授权执行,**红线除外**。
  **真机一律用隔离安装根 + 独立端口 + 独立服务名**(`/tmp/<名字>/root`),**不碰 `/opt/open-box`**;
  线上版本保持现状,升级只在隔离环境里验能力。

接手后先做:

1. 克隆 https://github.com/timeflysoon/Open-Box (公开),`git checkout main`。
2. 对账:git fetch 后把 `git log --oneline origin/main -- backend` 和 STATUS.md 对照着看。提交里已经做了、STATUS.md 没打勾的,
   先单独补打勾和日志,不要重复实现、重复推送;STATUS.md 打了勾、找不到对应提交的,当作未完成。对完账再取第一个没打勾的条目。
3. 确认 `go version` 不低于 `backend/go.mod` 里写的那一版;优先用标准库。需要 sing-box 的测试在没有可执行文件时 `t.Skip`。
4. 做完一个条目:`gofmt -l .` 无输出、`go vet ./...` 与 `go test ./...` 全绿,STATUS.md 打勾并加日志
   —— **和代码放在同一个提交里,不要拆开,也不要留到最后补**(额度可能随时用完,拆开就会出现代码已交付、清单没更新的漂移);
   提交说明 `backend: <阶段> <做了什么>`。


# 一、已定决策(详细见 docs/DESIGN.md 第二节)

Go 静态编译;模块名 `github.com/timeflysoon/Open-Box/backend`;接口契约就是 ENDPOINTS.md 加前端 TypeScript 类型;
第一阶段用子进程方式调 core;不校验 hello 的 buildID;数据先用 JSON 文件加原子写;认证用密码哈希加会话 Cookie 加登录限速;
前端静态文件由后端提供;订阅「登录」来源暂返回 501;教程视频返回空列表;默认监听 `0.0.0.0:3036`;
只依赖标准库和少量成熟库,引入依赖要在提交说明里写理由。

**「变更安全」是最重要的一条**:任何改 DNS、防火墙、路由、sing-box 启停的操作都要先校验再应用、可回滚,
无法确认系统状态时返回错误而不是猜(DESIGN 第四节)。

Go 版本:backend 与 core 各自以自己 `go.mod` 写的为准,不强行统一;升级时一并改 CI 里固定的版本。


# 二、仓库与同步(只列与 backend 有关的)

- 自研只在 main;catch-up-upstream 纯拉取上游,是上游的备份。
- main 的内容每次由 `sync-main.sh`(在维护者本机)重新生成:除 `panel/` 和 KEEP 目录外都被上游版本覆盖。
  `backend/` 在 KEEP 里,不会被覆盖。KEEP 的内容:`core`、`backend`、`.github/workflows`、`scripts`,共这四项,只能写目录。
  KEEP 只保留 main 里**已有**的目录:目录一旦从 main 消失,以后同步不会再补回来。
  所以 `.github/workflows/backend.yml` 与 `release-backend.yml` **不要删、不要改、不要挪位置**;
  开工先确认它们还在,不在的恢复步骤见 STATUS 文首「CI 门禁」第 1 条。
- `openwrt/`、`debian/`、`docker/`、`docs/`、`templates/` 不在 KEEP 里,每次同步都被上游覆盖;
  自写的服务脚本、安装脚本不能放在那里(M6 的放法见 `backend/docs/M6.md`)。
- 自己的东西放进这些目录,放在别处会在下次同步时丢失。根目录的 `.gitignore`、`README.md` 等会被上游覆盖,
  忽略规则写在 `backend/.gitignore` 里。
- 后端有独立的 Actions 工作流 `backend.yml`(gofmt、vet、test -race、编译 amd64 / arm64;
  只在 `backend/**` 或它自己变动时跑,**不覆盖 `core/`**)和只能手动触发的 `release-backend.yml`;
  不动 `release.yml`(它是上游版本,会被同步覆盖)。
- `backend.yml` 的 concurrency 已设好:main 上的推送按顺序自动排队,不会互相取消。**不要改它。**
- 过渡期的发布版本号规则 `v0.1.<上游号>-main.<n>`;main 上不要随便跑完整的 Release(会建标签并发布)。


# 三、对齐口径

**基线固定**:`v0.1.494` 是固定基准(记在 STATUS 交接快照里),全量对齐以它为准、已完成的不再回头重做。
上游出新版时**只做增量** —— 只看新版相对基线的差异(新增键 / 接口 / 行为),按需跟进,不重新全量比对。

对账方法:面板文案键集对上游打包产物(`catch-up-upstream` 的 `panel/dist`),面板与上游打包产物调用的接口对后端路由;
结果写进 STATUS「前后端对账缺口」。上游自 v0.1.463 起后端是闭源二进制,只能对着前端行为和公开材料对齐。

- 前端是规格:`panel/src` 要什么接口、什么字段,后端就实现什么;前端改了,ENDPOINTS.md 与后端**同一提交**里跟着改。
- 公开材料都可参考:`catch-up-upstream` 同步树与随包默认、Release 包、`panel/server` 可读部分、Release 说明、
  sing-box 文档与二进制、协议规范。观察结论记在提交说明或 STATUS 即可。
- 本仓库 `core/` 只当使用者调用,改 core 走 `core/HANDOFF.md`。
- 实现与测试自己维护;上游安装包不必放进 `backend/`。

**遇到拿不准的地方不要停**:按「对齐最新上游」(例外 A/B/C 除外)定结论;反推不出就换方法继续反推
(看 dist 的键与结构、跑上游基准面板观察实际行为、真机核对),直到对齐为止。**只有触碰红线才停。**


# 四、产品边界(与对齐无关;**各区只有这一处**)

- 不加激活机制;授权三个方法固定返回「未限制」。
- 有意不与上游一致、同步时不要改回、也不要补回的;除 A / B / C 外,main 里没有别的自有键,也没有别的有意缺失的键:
  A. 键保留,main 有意不引用:
    - 激活码类、comingSoon 的键;
    - 教程类的键:后端设置页顶部的「使用教程」按钮(YouTube 图标,tutorialButton / tutorialHint / tutorialChecking / tutorialCheckFailed)。
      main 不放这个按钮,也不做教程视频列表相关的面板与后端;
    - marketPromo、aiRelayLabel、aiRelaySubscription、aiRelayOnDemand(上游已不引用,main 也不引用)。
  B. 键保留,但 main 的显示或代码有意不同:
    - 链式代理空状态:不显示「安格超市」链接。ChainProxyManager.vue 若出现 chainProxyEmptyHintSuffix 或该链接,删除;
    - 客户端下载卡:不要「极速下载用 DownMan」。ClientDownloadsCard.vue 若出现 DOWNMAN_URL 或 clientDownloadFastWith,删除;
    - 概览页顶部文案:「机场、VPS 安格超市 | 原生住宅 IP TOPIP」改为 Open-Box Latest;
      「AI 中转站 · 订阅服务 SUPERDOOR | 按需付费 OPENDOOR」改为魔改 sing-box 最新版本号。
  C. 整体不做:键、面板、后端、core 全部没有(覆盖核心原则第 1 条):
    - 「业务优选」相关的一切都不要:设置页「业务优选」卡、概览页「业务优选」统计卡、内核按连接证据选路,
      以及对应的接口、档案字段和三语键;
    - 键按前缀识别,上游以后新增同类的键也不补:businessOverview*、businessPreference*、businessSettingsHelp*、
      businessReliabilityHalfLife、businessPerformanceTtl、bandwidthHelpCurrentBusiness;
    - 注意:ipTypeBusiness(商业宽带)与业务优选无关,照常按上游保留;
    - 核对键集和引用时,把以上键从「缺失」中剔除。


# 五、红线(任何情况下都不做)

- 不用上游压缩后的 `index.mjs`、官方 `openbox-core` 二进制来对齐实现。
- 不碰授权绕过。
- 不 force push;不删分支或 tag;不改动 `catch-up-upstream` 上的上游备份 release。
- 详见 `core/HANDOFF.md` 第五、六节。


# 六、来源与说明

1. 上游随包有专有许可;本仓库 backend 是独立自研实现,不附带上游闭源服务端。
2. 对齐行为时参考公开同步树、随包默认、Release 包等均可;写入 backend 的是自写代码与自维护数据
   (例如 defaults 内容可与上游随包一致,由本仓库持有更新)。上游安装包不必进仓库。
3. engine-snapshot 等历史材料可作行为/格式参考。
4. 激活接口固定「无需激活」,不实现授权体系;前端残留入口按产品边界(第四节)处理。


# 七、与维护者协作的约定

1. 用中文,简洁,先说结论。不要重复提醒他已经知道的事。
2. 交付方式见 README:本机直推 main(SSH 别名 remote,不 force push);
   只有没有推送权限的沙箱才出 `.patch`(`git format-patch -1`,另一份干净克隆上 `git apply --check`,
   `go vet ./...` 与 `go test ./...` 全绿才交;出补丁前先 `git fetch`,main 前进了就在最新 main 上重做)。
3. 沙箱模式下要读他本机的文件:给 PowerShell 脚本把内容写进剪贴板,他直接粘贴回来;不要在磁盘上生成文件,
   输出尽量短。他的 PowerShell 不支持 `&&`,也没有 grep、awk,需要这类命令放 Git Bash。
4. 前端联调:在 `panel/` 下 `ZASHBOARD_DEV_PROXY_TARGET=http://<路由器IP>:3036 npm run dev`,把前端的 `/api` 代理到真实后端。
5. 沙箱的坑:sh 不支持花括号展开,用 bash;`cut -c` 会切断多字节中文,输出含二进制或超长单行时终端会报编码错误,
   用 Python 读再打印;换会话后沙箱里的克隆、Go 环境都会丢,要重新准备;沙箱里 `git pull` 遇分叉会拒绝,
   用 `git fetch` 加 `git reset --hard origin/main`(只影响沙箱本地);克隆后默认分支是 catch-up-upstream,要 `git checkout main`。
6. 交付文档给他时用纯文本:不要表格,UTF-8 带 BOM、Windows 换行。


# 八、验证方法

- 表驱动单元测试;处理函数用 httptest;会改系统的代码放 `internal/system`,通过接口抽象,单元测试用假实现。
- 需要 sing-box 的测试在没有可执行文件时 `t.Skip`;云端工作流里装好固定版本后跑。
- 真机验证一律用隔离安装根,不碰线上;结果与备份位置记在 STATUS.md。
- 所有改动 `gofmt -l .` 无输出、`go vet ./...` 与 `go test ./...` 全绿才推。


# 九、提交记录

以 `git log -- backend` 为准;更早的提交清单已删(都在 git 历史里)。
