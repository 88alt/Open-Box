# 进度

**怎么用**:从上到下,做第一个没打勾的条目。完成 = 实现 + 测试通过 + 在这里打勾 + 在「日志」加一行 + 提交(`backend: <阶段> <做了什么>`)。
标注「需真机」的条目,代码和单元测试做完后打 `[~]`(代码完成,待真机验证),由维护者真机验证后改成 `[x]`。沙盒里无法验证的云端项(例如 Actions 首次运行)同样先打 `[~]`,由维护者确认后改成 `[x]`。
接口分组名对应 `docs/ENDPOINTS.md` 里的二级标题。

## 状态:M1 进行中

## M0 骨架
- [x] `go.mod`、`cmd/openbox-backend`、参数 / 环境变量 / 路径(`internal/system/paths.go`)
- [x] HTTP 服务、日志(slog)、优雅退出、统一错误响应 `{ "error": "..." }`
- [x] 原子写存储 `internal/store`(含并发测试)
- [x] 静态文件服务 + 前端路由回落
- [x] `/api/auth/*`:setup、login、logout、status、change-password(密码哈希、会话、登录限速)
- [x] `/api/storage`、`/api/background-image`
- [x] Actions:`go vet`、`go test`、编译 amd64 / arm64(`backend` 的独立工作流,不动 `release.yml`)

## M1 配置流水线
- [x] `core` 子进程客户端(`hello` + 15 个方法,超时、输入输出上限、错误传递)
- [x] `/api/openbox/profile`(GET / PUT 补丁合并)、`/api/openbox/profile/defaults`、`/api/openbox/defaults/{routing,groups}`
- [x] `/api/openbox/config/preview`(`internal/api/configpreview`;现算、不落盘,密钥遮成 `***`)
- [x] `/api/openbox/subscriptions`:列表、新增、修改、删除、排序、刷新、预览(`login` 返回 501)
- [x] 订阅拉取与解析(`internal/sub`:URL 拉取、分享链接 / base64、sing-box 格式、用量头 `subscription-userinfo`、自定义 UA)
- [x] Clash / Mihomo 格式解析(`internal/sub/clash.go` + 自带的 YAML 子集解析器 `miniyaml.go`)
- [x] 订阅改名规则(`renameOptions`:地区词典、特征词、模板、序号、过滤、禁用、前缀;`internal/subs/rename.go`)。预览用的 `groups` 生成并入下面「节点组」条目
- [x] `/api/openbox/groups`(GET / PUT;`internal/nodegroups`、`internal/api/groups`)
- [x] `/api/openbox/nodes/latency`、`/api/openbox/chain-proxies/latency`(`internal/probe` + `internal/api/latency`;已保存节点 / 预览 / NDJSON 进度;链式测速暂无 `ipUrls`)
- [ ] `/api/openbox/policies/entries`(依赖规则集 entries)
- [ ] 链式代理测速的 IP 地区(`ipUrls`:正文原样回传,只认白名单主机 api.ip.sb / ipwho.is / api.ipapi.is)
- [x] `/api/openbox/rulesets/preview`、`/import`(规则集链接的文本名单;`internal/rulelist`、`internal/api/rulesets`)
- [ ] `/api/openbox/rulesets/entries`(按分类名)、`/refresh`(现为 501,依赖随包规则库、`sing-box` 与配置生成)
- [~] 配置生成与校验(`internal/singbox`:`Generator.Build` 装配并调用 core `configDetailed`,`Check` 运行 `sing-box check`)。生成已用真实 core 测试;`Check` 只用假的 sing-box 脚本测过,**需真机**用真实内核验证
- [ ] 原子替换 → 重启后健康检查 → 失败回滚(`internal/singbox` 的部署部分,依赖 `service`)
- [x] 本地规则集文件落盘(第一部分,`internal/singbox/rulefiles.go` 的 `Files.WriteLocalSets`):按配置里 `route.rule_set` 的本地条目分类;`obnode-direct` / `obnode-direct-ip` 每次按 `Built.DirectHosts` 改写(空名单写占位),热切换动态集 `obflip-byp-*` / `obflip-need-*` 不存在才用 `sing-box rule-set compile` 编占位 `.srs`、已存在不覆盖
- [ ] 本地规则集落盘(第二部分):热切换开关文件 `obflip-<id>.json` / `obflip-fallback.json`——内容取决于每个站点集此刻走直连还是走代理,**需要 core 配合**(见下面「需要 core 配合」)
- [ ] 本地规则集落盘(第三部分):`geoip-*` / `geosite-*` 和用户规则集链接编成的 `.srs`——依赖随包规则库和 `rulesets/entries`;在这之前 `WriteLocalSets` 的 `Missing` 里会如实列出它们,部署步骤据此拒绝继续
- [ ] `/api/openbox/service`、`/api/openbox/kernel`
- [ ] 前端联调:添加订阅 → 生成配置 → sing-box 启动成功 **需真机**

## 需要 core 配合(由 core 那边按 core/HANDOFF.md 另做;做完后回来勾掉)
- [ ] 热切换开关状态:core 目前有 `FlipFlagMap` / `FlipFlagStates`(`core/internal/routing/flip.go`)但没有对外的方法,后端拿不到「开关 tag → 开 / 关」。
  建议让 `configDetailed` 的返回多带一个 `flipFlags`({tag: bool}),或新增一个只算这个的方法;后端拿到后在 `WriteLocalSets` 里补 `flip-flag` 一类即可。注意:改返回值会影响 core 的金标准,需要同步处理。

## M2 DNS 与过滤
- [ ] `/api/openbox/dns*`
- [ ] `/api/openbox/dns-filter*`
- [ ] `/api/openbox/content-filter*`
- [ ] `/api/openbox/client-config*`、`/api/openbox/client-app*`、`/api/openbox/clients`
- [ ] `/api/openbox/servers`、`/api/openbox/subscription-shares*`
- [ ] DNS 接管与撤销(`internal/system`,可回滚)**需真机**

## M3 观测
- [ ] `/api/openbox/traffic*`
- [ ] `/api/openbox/latency-history*`、`/api/openbox/site-latency*`
- [ ] `/api/openbox/node-info*`、`/api/openbox/route-test`、`/api/openbox/terminal-test`、`/api/openbox/penetration`
- [ ] `/api/controller-ws/{x}`(WebSocket 转发)
- [ ] `/api/openbox/diagnostics`

## M4 自动化
- [ ] 订阅定时更新(调度器)
- [ ] `/api/openbox/failover*`
- [ ] 延迟历史采集(含 `nodes/latency` 的 `record` 与响应 `history`,现在被忽略)

## M5 系统运维
- [ ] `/api/openbox/backup`(含 query 参数的导出 / 导入)
- [ ] `/api/openbox/deploy*`、`/api/openbox/factory-reset`、`/api/openbox/system*`
- [ ] `/api/openbox/update*`(组件更新)
- [ ] `/api/openbox/activation*`(固定「无需激活」)、`tutorial-videos`(空列表)
- [ ] 时区

## M6 打包与发布
- [ ] 编译 amd64 / arm64、打包 `panel/dist` + `openbox-core` + sing-box 内核 + 安装脚本
- [ ] 接入 `main` 的发布流程;真机整体联调 **需真机**
- [ ] 清理旧文件 `scripts/native-arm64/`(联调通过之后)

## 日志(新的在最上面)
- 2026-10-26 M1 本地规则集落盘(第一部分)internal/singbox/rulefiles.go:`LocalSets` 从配置的 route.rule_set 取本地条目(同 tag 只留第一条),`Classify` 按 tag 分四类(node-direct / flip-flag / flip-dynamic / ruleset)。`Files.WriteLocalSets(ctx, cfg, directHosts)`:node-direct 两份每次改写(源码 JSON `{"version":3,"rules":[{"domain":[...]}]}` / `{"ip_cidr":[...]}`,空名单用文档保留的占位,0600 原子写);flip-dynamic 不存在才用已安装的 sing-box 编占位 .srs(192.0.2.255/32 与 2001:db8:ffff::ffff/128),已存在的留给热切换,不覆盖;其余(开关文件、规则库 / 用户规则集)报在 `Missing`(带 tag、类别、路径、原因),单个文件失败不中断其它,只有 ctx 取消才返回 error。测试覆盖分类、顺序与去重、两种名单与占位、格式不符、取消、没有 sing-box、权限与临时文件不残留;有真实 sing-box 时再跑占位编译(含「已存在不覆盖」)和「真实 core 生成配置 → 落盘」(缺的只能是开关和规则库两类)。没做:开关文件(见「需要 core 配合」)、规则库 .srs(见 M1 第三部分)、把它接进部署流程(部署步骤依赖 service)。node-direct 的 JSON 形状是后端自己的设计,sing-box 1.14.4 的 compile 和 check 认得;需真机用真实内核验证。
- 2026-10-26 M1 节点测速 internal/probe + internal/api/latency:从完整配置摘出被测出站(含 detour、组成员、被引用的 DNS 服务器),起临时 sing-box(仅 127.0.0.1 的 http 入站,每任务一个用户名,路由按 auth_user),后端经 CONNECT 隧道请求并自己计时(sing-box 1.14 的 clash 延迟接口不认传入的地址,tools fetch 的耗时含进程启动,故不用;一律 CONNECT 是为了把「线路不通」和「目标回 502」分开)。nodes/latency 支持 jobs、预览(新增 Generator.BuildWith / PreviewOverride、subs.PreviewNodes,不落盘)和 NDJSON 进度;chain-proxies/latency 用 ChainOverride 加一条链式节点,`via` 静态推出。测试用真实 core 和真实 sing-box(没有则跳过);backend.yml 增加固定版本 1.14.4 的 sing-box 安装步骤。没做:ipUrls、延迟历史。需真机用真实订阅验证。
- 2026-10-26 M1 配置生成与校验 internal/singbox + /api/openbox/config/preview:Generator.Build 把已启用订阅的节点(带 subscriptionId,节点 DNS 靠它找订阅)、节点组(nodegroups.Groups)、档案、启用的链式代理(sub.Parse 解析链接,tag = 名字,detour = 上游,chain = true,解析不了的跳过,上游是否存在 / 成环由 core 再核对)、系统 DNS(读 resolv.conf,去掉回环)、本机局域网网段(私有地址,跳过 tun 类接口)按 core configDetailed 的入参拼好调用;selections 暂传空,ruleLists / nativeBypass / dnsFilter 不传(core 自己算旁路计划;内容过滤启用时的名单属于 M2)。内核控制接口密钥(clash_api.secret)首次生成后存 runtime.json,之后不变;config/preview 把它遮成 `***`。Check:配置写到 ConfigDir 下的 0600 临时文件,运行 `<sing-box> check -c`,输出截断到 8 KiB 并把临时路径替换成 config.json,30 秒超时,结束删除临时文件;找不到可执行文件返回 ErrNoSingBox。平台默认 openwrt(core 只有 container 有差异)。没做:原子替换 / 回滚、启停(依赖 service),以及配置里本地规则集文件的落盘(见上面未勾选条目)。
- 2026-10-26 M1 /api/openbox/rulesets/preview、/import:新增 internal/rulelist(拉取 + 解析)和 internal/api/rulesets。认 Clash 经典名单(DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD / DOMAIN-REGEX / IP-CIDR / IP-CIDR6,no-resolve 等参数忽略)、YAML payload、裸写法(+.x / .x / *.x = 后缀,x = 完整域名,IP 与 CIDR,单个地址补 /32 /128);进程名、GEOIP 等认不得的行计入 skipped 但不报错;IP 段按掩码规整,域名转小写,去重保序。二进制名单(.mrs / .srs)明确返回 400:解码要引入 zstd 等依赖,沙盒拉不到模块、无法验证 go.sum,先不引入(DESIGN D10)。拉取只收 http / https,8 MiB 上限,30 秒超时,错误信息不带完整地址。preview 的结果在内存缓存 60 秒(最多 8 个链接,失败不缓存)。entries / refresh 返回 501。测试覆盖解析表、BOM / CRLF、重复、非法写法、超限、缓存过期与淘汰、分页过滤、错误码。
- 2026-10-26 M1 /api/openbox/groups:新增 internal/nodegroups(服务层)和 internal/api/groups。GET 返回 groups / types / availableNodes / subscriptions / availableGroups,PUT 整份覆盖。规范化、重名补名、环检测都交给 core(normalizeGroups / emitGroups),后端只做:组名唯一且不与节点重名(400)、按 id 识别改名并迁移其它组(含故障转移页签)的引用、报告悬空成员(dangling)和成环被丢的组(dropped)、落盘 groups.json。节点清单取已启用订阅的节点加启用的链式代理;为让动态组关键词能匹配地区名,sub.Node 新增 regionName(改名命中时写入,core 不读),subs.Service 新增 Catalog。测试编译仓库里真实的 core 运行(没有 Go 时跳过),覆盖默认组、节点清单、保存回读、名字校验、改名迁移、故障转移页签迁移、悬空与环、核心不可用 503。已知没做:站点集(routing)里按组名引用出站,改名时是否要同步迁移要等 policies 条目对照前端后决定;`changed`(提示重启内核)等配置生成条目一起做。
- 2026-10-26 M1 订阅改名规则 internal/subs/rename.go:按前端 RenameRulesEditor 的语义实现。顺序:过滤关键词 → 逐条禁用 → 识别地区 / 特征 → 手工改名(不占序号)或按模板命名 → 订阅名前缀 → 全局去重。地区:regionDict 按顺序取第一个命中的条目,顺序即优先级,命中后节点带 regionCode(预览 preview[].regionCode 给界面显示国旗),重命名总开关关掉时也识别;特征:featureKeywords 里第一个命中的词转大写(旧档案 featureDict 扁平化,显式空数组 = 不加特征);模板 {region} {feature} {seq},没特征时连同相邻「-」去掉,未识别地区用 unknownLabel(默认「其他」),序号在同一(地区, 特征)内从 1 数、按 seqPad 补零(默认 2,最大 6)。缺失的项用与前端 rename-defaults.ts 一致的默认值(地区词典、特征词、过滤词「官网 / 工单 / 客服」);显式给空数组才表示「没有」。关键词不区分大小写;不超过 3 个字符的纯 ASCII 词(hk、us、2x)要求两侧不是字母,避免 us 命中 Russia、de 命中 Sweden(这是本实现的决定,前端没有规定)。Node 新增 regionCode(core 不读,不影响配置生成)。已有的订阅接口测试默认带 enabled:false,免得被默认模板改名干扰。
- 2026-10-26 M1 Clash / Mihomo 解析:只切出顶层 `proxies` 段再解析(配置其它部分的写法不影响);自带 YAML 子集解析器 miniyaml.go(块映射 / 序列、流式 {} [] 可跨行、引号、注释、锚点 / 别名 / `<<`;多行文本、tab 缩进、多文档报错;标量保持字符串避免丢前导零)。节点映射:ss(obfs / v2ray-plugin,其它插件进 skipped)、vmess、vless(含 reality、flow;encryption 非 none 不支持)、trojan(必走 TLS)、hysteria2(ports 跳跃、obfs)、tuic(v5;v4 token 版不支持)、anytls、socks5、http;传输层 ws(early data、v2ray-http-upgrade)/ grpc / h2 / http,其它(xhttp、kcp 等)进 skipped;wireguard 等进 skipped;direct / reject / dns 忽略;整份 Clash 再 base64 也认。skip-cert-verify 不写(core 的 emit 对非 reality 恒开 insecure)。语法错误返回 `ErrClashInvalid`(接口 400)。测试里核对了与分享链接解析出的节点形状完全一致。
- 2026-10-26 M1 /api/openbox/subscriptions*:列表 / 新增 / 修改(PATCH 只改传了的字段;只改名、开关、计划不联网,来源 / UA / 改名规则变了才重新拉取)/ 删除 / 排序(必须是全部 id 的排列)/ 刷新 / 预览(不落盘)/ login 返回 501;支持多条订阅地址合并和粘贴节点内容(content 模式刷新 = 重新解析,不联网);数据存 subscriptions.json(元数据)和 sub-nodes.json(节点池,先写节点再写元数据);节点名全局唯一(同订阅内和跨订阅重名都加序号,内核出站不能重名);改名目前只做过滤关键词、逐条禁用、手工改名、订阅名前缀,regionDict / 特征词 / 模板等键原样保存但还不参与(见下一条未完成项);changed 表示节点池变了(前端提示重启内核);拉取失败返回 502、输入有问题 400、不支持 501。新增 subs.Service.PoolNodes 供配置生成取已启用订阅的节点。
- 2026-10-26 M1 订阅拉取与解析 internal/sub:Fetch(只收 http/https,16 MiB 上限,默认 UA v2rayN/7.0 可改,错误信息不带 token 的 URL,解析 subscription-userinfo 用量,expire 为 0 记 null);Parse 自动识别 sing-box JSON / Clash / 分享链接 / base64(标准与 URL 编码、带不带填充、含换行都收);链接支持 ss(SIP002 与旧写法、obfs-local / v2ray-plugin)、vmess(v2rayN)、vless(含 reality)、trojan、hysteria2 / hy2(mport、多端口写法)、tuic、anytls、socks、http(s);不支持的协议 / 插件 / 传输(kcp、xhttp 等)进 skipped 并带 reason;输出节点形状与 core 一致,已用真实 openbox-core 的 configDetailed 验证能生成出站。Clash 另列条目。
- 2026-10-25 M1 /api/openbox/profile*、/api/openbox/defaults/*:档案用 map 存(未知键如 shareRegions / serverInfo 原样保留),默认值 + 已保存内容合成完整档案;PUT 是深度合并补丁(对象递归、数组整体替换、null 删键),合并后校验(端口写法、DNS 上游只收 IP/wan、tun、测速地址、状态码、更新计划等),失败 400 不落盘;rulesetDir 忽略客户端值;chainProxies[].node 只沿用服务端旧摘要;读到旧版分流(regions 等)时经 core migrateStoredRouting 迁移并写回(新版分流不起子进程);默认节点组由 core normalizeGroups 补全。config/preview 拆出,和配置生成一起做。
- 2026-10-25 M1 core 子进程客户端 internal/core:hello + 15 个方法的通用调用,超时杀进程、并发上限 2、输入 32 MiB / 输出 64 MiB 上限、stderr 只留末尾、不继承环境变量;ok:false 变成 *CoreError;集成测试编译真实 core 验证 hello 和 15 个方法均已注册。
- 2026-10-25 M0 Actions 工作流 backend.yml(gofmt、go vet、go test -race、交叉编译 amd64/arm64);本地已验证同样的命令全部通过,云端首次运行已全绿(Backend #1,3 个任务)。
- 2026-10-25 M0 /api/storage(整体替换 config/ 设置快照)、/api/background-image(GET / PUT / DELETE,data URL 校验与体积上限);ENDPOINTS 补全方法。
- 2026-10-25 M0 /api/auth/*:setup / login / logout / status / change-password;PBKDF2 哈希、签名会话 Cookie、按 IP 限速;鉴权中间件;ENDPOINTS 校正了 auth 的方法并补充错误码约定;DESIGN D7 记录偏差。
- 2026-10-25 M0 静态文件服务 + 前端路由回落(缺失的带扩展名资源返回 404,不回落成 HTML;assets/ 长缓存,index.html 不缓存)。
- 2026-10-25 M0 原子写存储 internal/store(临时文件 → fsync → rename;同文件读改写串行;损坏文件报错不覆盖;含并发测试,-race 通过)。
- 2026-10-25 M0 HTTP 服务(internal/server)、slog、优雅退出、统一错误响应(internal/httpx)、/api/health。
- 2026-10-25 M0 go.mod、入口、参数/环境变量/路径(internal/system/paths.go):命令行 > 环境变量 > 默认值。
