# 后端设计与计划

## 1. 目标与非目标
**目标**:实现 `docs/ENDPOINTS.md` 列出的全部接口,让 `panel/` 前端在真实路由器上完整可用;单个静态二进制,能在 OpenWrt(arm64/amd64)和 Debian 上运行,内存占用小。
**非目标**:与上游后端逐接口、逐字段一致;激活码、会员、授权相关的一切;上游的闭源行为。前端是规格,上游后端不是。

## 2. 已定的决定(接手者不用再问)
| # | 决定 | 理由 |
|---|---|---|
| D1 | 语言:**Go**,`go.mod` 写 `go 1.25.12`(与 `core/go.mod` 一致),`CGO_ENABLED=0` 静态编译 | 与 `core/` 同语言;路由器上不需要 Node 运行时;单文件易部署 |
| D2 | 模块:`backend/` 自带 `go.mod`,模块名 `github.com/timeflysoon/Open-Box/backend` | 与 `core/` 分开发布 |
| D3 | 接口契约 = `docs/ENDPOINTS.md` + `panel/src/api/*.ts` 里的 TypeScript 类型;JSON,同源,不开 CORS | 前端就是规格 |
| D4 | 与 `openbox-core` 的集成:**第一阶段用子进程**,走 `core/README.md` 描述的 stdin/stdout JSON 协议(`protocol:1`,15 个方法 + `hello`) | `core/` 的金标准测试保持有效、零改动;`core/internal` 不能被别的模块直接 import |
| D5 | 不校验 `hello` 返回的 `buildID`(那是为上游 Node 服务端写死的期望值);只要求 `implementation:"go-native"`、`protocol:1` | 自己的后端自己的核心 |
| D6 | 持久化:**JSON 文件 + 原子写**(写临时文件 → fsync → rename),进程内用互斥锁串行化同一文件的读改写。流量 / 延迟历史等时序数据到 M5 再决定是否引入嵌入式数据库(优先纯 Go、无 CGO) | 路由器上依赖越少越好;配置数据量小 |
| D7 | 认证:首次设置密码(`/api/auth/setup`)→ 登录得到会话 Cookie(`HttpOnly`、`SameSite=Strict`);密码存哈希;登录失败做限速。**实现(M0)**:哈希用标准库 `crypto/pbkdf2`(PBKDF2-HMAC-SHA256,60 万次,哈希串带算法名和迭代次数,可平滑升级),不用 scrypt / argon2id;会话是无状态签名 Cookie(30 天),改密换密钥使其它会话失效 | 面板在局域网,但仍要防暴力破解。沙盒拉不到 `golang.org/x/crypto`,无法验证带依赖的 `go.sum`;PBKDF2 零依赖,也比 scrypt / argon2id 省内存,适合路由器。以后要换算法,新增一种哈希前缀即可,不影响旧密码 |
| D8 | 前端静态文件由后端提供(`panel/dist`,目录可配置);`/api` 以外的路径回落到 `index.html` | 与前端开发代理一致 |
| D9 | 激活相关接口(`/api/openbox/activation*`)只返回固定的「无需激活」状态,不保存任何东西 | 后端没有授权概念;前端残留调用不能报错 |
| D10 | 只依赖标准库和少量成熟的第三方库(例如 `golang.org/x/crypto`);每引入一个依赖要在提交说明里写理由 | 体积和供应链风险 |

## 3. 运行环境与路径
- 目标系统:OpenWrt(procd)、Debian(systemd);仓库里的 `openwrt/`、`debian/`、`docker/` 是上游留下的安装与镜像脚本,**目前不是本后端的规格**。
- 所有路径集中在 `internal/system/paths.go`(数据目录、配置目录、sing-box 可执行文件、`openbox-core`、前端 `dist`),全部可用命令行参数或环境变量覆盖;默认值先取通用位置,**M6 打包时**再与安装脚本对齐。
- 配置文件、数据文件的权限:仅 root 可读写(含密码哈希、订阅地址、订阅账号)。

## 4. 变更安全(最重要)
路由器是用户的网关,弄断了就要重新刷机或接线排查。因此:
1. **先校验再应用**:生成 sing-box 配置后先写到临时文件,执行 `sing-box check -c <file>`,通过才替换正式配置。
2. **可回滚的应用**:替换前保留上一份配置;重启 sing-box 后做健康检查(进程在、监听端口通、控制接口响应);失败自动回滚并重启旧配置,接口返回明确错误。
3. **DNS 接管 / 防火墙规则 / 路由**:每一项操作都要有对应的撤销函数;进程收到退出信号、或应用失败时执行撤销;启动时先检测并清理上次异常退出遗留的规则。
4. **不确定就不动**:无法确认当前系统状态(例如读不到防火墙规则)时,返回错误,不要「猜一个」去改。
5. 所有会改系统的代码放在 `internal/system/`,通过接口抽象,单元测试用假实现;**真机验证由维护者做**,在 `STATUS.md` 里单独标注「需真机」。

## 5. 分阶段计划
每个阶段结束都应该能被前端联调验证。条目的细分和勾选在 `STATUS.md`。

**M0 骨架**:`cmd/openbox-backend`、配置/路径、HTTP 服务、静态文件、日志、优雅退出;`/api/auth/*`(setup / login / logout / status / change-password);原子写存储;`/api/storage`;健康检查。
*验收*:前端能打开、设置密码、登录、退出;`go vet` 和 `go test ./...` 通过;云端 Actions 能编译 amd64 / arm64。

**M1 配置流水线(核心价值)**:`/api/openbox/profile`、`defaults`、`subscriptions*`(增删改查、排序、预览、刷新、`login` 暂返回「未实现」错误)、`groups`、`policies`、`rulesets`、`chain-proxies`、`nodes`;`core` 子进程客户端;`config`(调用 `configDetailed`)→ 落盘 → `sing-box check` → 应用;`service`(启动/停止/重启/状态)、`kernel`。
*验收*:在真机上添加订阅、生成配置、sing-box 能启动,前端能看到节点和策略组。**需真机**。

**M2 DNS 与过滤**:`dns*`、`dns-filter*`、`content-filter*`、`client-config*`、`client-app*`、`clients`、`servers`、`subscription-shares*`、`background-image`。
*验收*:DNS 设置、内容过滤、终端分流、共享网络页面能读写并生效。**需真机**。

**M3 观测**:`traffic*`、`latency-history*`、`node-info*`、`site-latency*`、`route-test`、`terminal-test`、`penetration`、`controller-ws/{x}`(到 sing-box 控制接口的 WebSocket 转发)、`diagnostics`。

**M4 自动化**:订阅定时更新(对应 `subscriptions` 的 auto-update 字段)、`failover*`、延迟历史采集调度器。

**M5 系统运维**:`backup`(导出/导入,含 query 参数)、`deploy*`、`factory-reset`、`system*`、`update*`(组件更新:自己的二进制、sing-box 内核、规则集)、时区、`tutorial-videos`(默认返回空列表)。

**M6 打包与发布**:GitHub Actions 里编译 amd64 / arm64 的 `openbox-backend`,打包 `panel/dist`、`openbox-core`、sing-box 内核、安装与卸载脚本;与 `main` 的发布流程对接(见 `.github/workflows/`);版本号规则 `v0.1.<上游号>-main.<n>` 仅用于过渡,正式发布前由维护者重定。

## 6. 工程约定
- 每个接口分组一个包:`internal/api/<分组>`,导出 `Register(mux, deps)`;处理函数只做参数解析、校验、调用服务层、写响应。业务逻辑放服务层,系统副作用放 `internal/system`。
- 响应格式、错误格式以前端 `requestJson` 的预期为准:成功返回 JSON;失败返回非 2xx,并带 `{ "error": "<给人看的中文信息>" }`(前端的 `extractErrorMessage` 会读)。写新接口前先读 `panel/src/api/openbox.ts` 对应函数。
  例外是 `/api/auth/*`:机器码放在 `code` 或 `error` 字段,详见 `docs/ENDPOINTS.md` 的「鉴权与错误码约定」。公共小工具在 `internal/httpx`(`Error` / `CodeError` / `ErrorCode` / `Methods` / `DecodeJSON`)。
- 测试:表驱动单元测试;处理函数用 `httptest`;需要 sing-box 的测试在没有可执行文件时 `t.Skip`,云端 Actions 里装好后跑。
- 日志:标准库 `log/slog`,默认 info;不要在日志里打印密码、订阅 URL 里的 token。
- 并发:对同一个数据文件的写入必须经过 `store` 的锁;长耗时操作(拉订阅、测速)用 `context` 带超时,可被取消。
- 内存:避免一次性把大文件读进内存(规则集、订阅内容);所有外部输入设上限。

## 7. 来源与合规
- 允许参考:`panel/src`、`core/`(含 `core/README.md` 描述的基线说明)、sing-box 官方文档、OpenWrt / Debian 文档、各协议规范。
- 禁止:读取、复制或反编译上游闭源后端和核心(见 `README.md` 硬性规则);实现任何授权 / 激活 / 校验绕过。
- v0.1.318 快照(tag `engine-snapshot`)里有可读的旧版 Node 服务端。**我检查过该快照的许可文件:里面只有 Node.js 和 sing-box 的许可,没有 Open-Box 自己的许可文本**,所以它的使用条件不明确。
  在维护者明确确认之前,把它当作**不可用**,不要复制其中任何代码,也不要照着它逐行改写。
- 前端里有几处是按上游新版的编译产物对齐出来的(订阅「登录」来源、教程视频等)。它们对应的接口 `subscriptions/login`、`tutorial-videos` 在后端按下面的默认处理,不要为它们去研究上游行为。

## 8. 待维护者确认(接手者直接按默认做,不用停下来问)
| 事项 | 默认做法 |
|---|---|
| 订阅「登录」来源(`/api/openbox/subscriptions/login`) | M1 返回 501 + 中文错误「暂不支持」;以后需要再设计 |
| 教程视频(`tutorial-videos`) | 返回 `{ "videos": [], "updatedAt": 0, "staleAfterMs": 86400000 }` |
| 激活接口 | 固定的「无需激活」状态(D9) |
| 默认监听地址与端口 | `0.0.0.0:3036`(与现有联调用的端口一致),可由参数覆盖 |
| v0.1.318 快照的许可 | 不可用,见第 7 节 |
| 版本号 | 过渡期 `v0.1.<上游号>-main.<n>` |
| 默认分流(`defaults/routing`) | 一个「国内直连」站点集(`geosite-cn` + `geoip-cn`,默认直连),兜底「其他」走第一个节点组;自研定的起点,没有参照上游。规则集名在随包规则库里没有时会被 core 过滤 |
| 默认节点组(`defaults/groups`) | 内置直连 / 拒绝 + 一个动态 urltest 组「自动选择」(全部节点),由 core `normalizeGroups` 补全字段 |
| DNS 重写默认项(`dnsRewriteDefaults`) | 空数组:没有可靠依据的规则不预置,「恢复默认」= 清空;以后有依据再补 |
| 客户端 App 的默认地区分流(`client-app/info` 的 `defaultShareRegions`,「恢复默认」= 存空数组) | 三组:「中国大陆」(`CN`;geosite cn、geoip cn 本地直连,其余经路由器;DNS 直连系统 DNS、代理 TCP 1.1.1.1)、「港澳」(`HK`、`MO`)、「其他地区」(默认组,`CN` / `HK` / `MO` 之外);后两组本地直连、只把 geosite cn、geoip cn 送回路由器,DNS 两侧都是系统 DNS(与前端 `regionDnsDefaults` 一致)。自研定的起点,没有参照上游;用户可随时改 |
| 档案默认值 | `region` 默认 `cn`;DNS 上游按前端 `regionDefaultsPatch`;`testUrl` 用 gstatic 204,`directTestUrl` 用华为 connectivitycheck 204;流量保留 12 个月 |
| DNS 查询日志位置(`-dns-query-log`) | 默认 `/var/run/openbox-dnsmasq.log`(内存盘,不写闪存 / SD 卡),DNS 接管时让 dnsmasq 写到这里;`off` = 不开不读;用户自己设了日志去向就不覆盖 |
