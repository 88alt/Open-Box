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
- [x] `/api/openbox/policies/entries`(`internal/policyentries`、`internal/api/policies`):站点集的手写条件、规则集链接、前置自定义分流的行展开,分档 / 搜索 / 排序 / 分页;兜底返回 `fallback`;展开不了的如实列在 `missing`
- [~] `policies/entries` 的规则库分类展开(`geosite-*` / `geoip-*`,含前置自定义分流的 `geosite` / `geoip` / `ruleset` 行;`policyentries.Service.WithGeo` + `expandGeo`):用 `internal/geodata` 展开,来源写分类名;规则库没接 / 没有这个分类 / 没有 sing-box / 解码失败如实留在 `missing`。用假规则库和真实 core + 真实 sing-box 自己编的 .srs 测过,**还没用真实的公开规则库文件验证**(与 `rulesets/entries` 那条一起做),需真机
- [~] 链式代理测速的 IP 地区(`ipUrls`:正文原样回传,只认白名单主机 api.ip.sb / ipwho.is / api.ipapi.is;`internal/probe` 的 `Job.Capture` / `Fallback`、`internal/api/latency`)。用真实 sing-box 加本机假的三家接口测过,**需真机**确认真实接口的答话
- [x] `/api/openbox/rulesets/preview`、`/import`(规则集链接的文本名单;`internal/rulelist`、`internal/api/rulesets`)
- [~] `/api/openbox/rulesets/entries`(按随包规则库的分类名查条目;`internal/geodata`、`internal/api/rulesets`):对 `<规则库目录>/<分类名>.srs` 运行 `sing-box rule-set decompile` 展开,目录用 `-geo-dir` / `OPENBOX_GEO_DIR` 指定,只读、不下载,没有的分类如实 404。用假 sing-box 和真实 sing-box 1.14.4 自己编的 .srs 测过,**还没用真实的公开规则库文件验证**(沙箱下载不了),需真机 / 有真实规则库的环境确认
- [ ] `/api/openbox/rulesets/refresh`(现为 501):规则集链接「立即重拉重编」,要先有「规则集链接编成 `list-<hash>-*.srs` 落盘」(见下面本地规则集落盘第三部分)和部署;`needsRestart`(名单构成变了才要重启)、`counts` 的算法要对照 core 的规则集 tag 布局(`core/internal/routing/rulesets.go`)另行设计
- [~] 配置生成与校验(`internal/singbox`:`Generator.Build` 装配并调用 core `configDetailed`,`Check` 运行 `sing-box check`)。生成已用真实 core 测试;`Check` 只用假的 sing-box 脚本测过,**需真机**用真实内核验证
- [~] 原子替换 → 重启后健康检查 → 失败回滚(`internal/singbox/deploy.go` 的 `Deployer.Deploy`)。代码和单元测试(假服务)完成,另有真实 sing-box 的集成测试;**需真机**确认真实服务管理器(procd / systemd)下的重启与回滚。**还没接到 HTTP**:`/api/openbox/deploy`、`/deploy/state` 属于 M5 的 `deploy*` 条目,接的时候用 `Deployer.Deploy` 的结果(阶段名已对齐前端 `OpenboxDeployResult`),并把结果存成 deploy state
- [x] 本地规则集文件落盘(第一部分,`internal/singbox/rulefiles.go` 的 `Files.WriteLocalSets`):按配置里 `route.rule_set` 的本地条目分类;`obnode-direct` / `obnode-direct-ip` 每次按 `Built.DirectHosts` 改写(空名单写占位),热切换动态集 `obflip-byp-*` / `obflip-need-*` 不存在才用 `sing-box rule-set compile` 编占位 `.srs`、已存在不覆盖
- [ ] 本地规则集落盘(第二部分):热切换开关文件 `obflip-<id>.json` / `obflip-fallback.json`——内容取决于每个站点集此刻走直连还是走代理,**需要 core 配合**(见下面「需要 core 配合」)
- [ ] 本地规则集落盘(第三部分):`geoip-*` / `geosite-*` 和用户规则集链接编成的 `.srs`——规则库目录(`-geo-dir`)和 `internal/geodata` 已有,剩下:把用到的分类从规则库放进规则集目录(拷贝还是软链,待定)、用户规则集链接用 `sing-box rule-set compile` 编成 `.srs`;在这之前 `WriteLocalSets` 的 `Missing` 里会如实列出它们,部署步骤据此拒绝继续
- [~] `/api/openbox/service/status`、`POST /api/openbox/service/core/{start|stop|restart|enable|disable}`、`/api/openbox/kernel/version`(`internal/system/service.go`、`internal/api/service`、`internal/singbox/version.go`):只通过系统服务管理器操作(OpenWrt `/etc/init.d/<名字> running|enabled|start…`,systemd `systemctl is-active|is-enabled|start…`),服务名可用 `-core-service` / `-panel-service`(或环境变量)配置,默认 `sing-box` / `openbox-backend`。代码和单元测试(假 Runner)完成,**需真机**确认 OpenWrt 的 `running` 子命令、服务名和 systemd 的运行时长
- [ ] 服务状态的剩余部分:`conflicts`(与其它代理服务冲突的检测,现恒为空)、`pendingRestart`(配置已改、内核还没重启生效,现恒为「没有」,等部署流程接进来)、Docker(`container`)平台。启动 / 重启现在只是单纯的服务操作,「生成配置 → 校验 → 重启 → 确认在跑」的完整流程见上面「原子替换 → 健康检查 → 回滚」那一条
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
- 2026-10-26 M1 policies/entries 的规则库分类展开(自己的设计,依据前端 `OpenboxPolicyEntries` 类型,没有参照上游;接上 `rulesets/entries` 那条做的 `internal/geodata`):`policyentries.Service` 新增 `WithGeo(Geo)`(不改 `New` 的签名,不接则行为与以前完全一致:规则库分类都列进 `missing`)。`expandGeo(tag)`:名字过 `geodata.ValidTag` 且接了规则库就调 `Entries` 展开,条目来源记分类名(与规则集链接的来源写法一致,搜索词能匹配来源);没接 / 名字不合法 / 不在库里 / 没有 sing-box / 解码失败都记入 `missing` 而不是让整个查询失败;请求取消则把取消错误交回(接口按原有方式处理);分类被 geodata 截断时 `missing` 里加「<分类>(条目太多,只统计了前 30 万条)」。`fromPolicy` 的 `p.Rulesets`(跳过链接折出的 `list-*`)和 `fromCustom` 的 `geosite` / `geoip` / `ruleset` 三种行都走它;同一分类内重复只留一条,不同分类里的同名条目来源不同、各留一条;总量仍受 30 万上限。`list-*` 与非法名字不去问规则库。`CoreAdapter` 仍不传 `geoTags`(传了核心会把规则库里没有的分类直接丢掉,而这里要如实列进 `missing`),注释改写。`main.go`:`policyentries.New(...).WithGeo(geoLib)`,与 rulesets 共用同一个 `geodata.Library`(共用缓存和串行解码)。测试:假规则库覆盖展开 / 来源 / 重复分类 / 按来源搜索 / ip 档 / 前置自定义分流行 / 非法名字 / 各类失败只进 missing / 截断提示 / 取消 / 注入 nil;真实 core(规范化)+ 真实 sing-box 编出的 .srs + `geodata` 的整条链(站点集和前置自定义分流两处);原有测试不改一行全部通过;`go vet`、`go test -race ./...` 全绿。**没验**:同 `rulesets/entries`,没拿真实的公开规则库文件验证。真机要做:规则库放好真实的 geosite-cn.srs 后,在面板「域名穿透」里打开引用了 geosite-cn 的站点集,应列出域名且来源显示 geosite-cn、`missing` 里不再有它;切换「域名 / IP」页签、搜索、翻页的响应时间记一下(每次请求都会重新汇总一遍条目,条目几十万时路由器上可能偏慢,若明显卡顿告诉我,再考虑缓存汇总结果)。
- 2026-10-26 M1 /api/openbox/rulesets/entries(按随包规则库的分类名查条目;自己的设计,依据前端 `fetchRulesetEntries` / `OpenboxRulesetEntries` 和 DESIGN 里「交给 sing-box 解码」,没有参照上游;原第 32 行拆成 entries / refresh,本次只做 entries,它也是第 29 行 `policies/entries` 规则库分类展开的前置):新增 `internal/geodata` 和路径参数 `-geo-dir` / 环境变量 `OPENBOX_GEO_DIR`(默认 /usr/share/openbox-backend/geodata,只读,`Ensure` 不创建,由安装包放入)。规则库目录约定是自己定的:`<目录>/<分类名>.srs`。分类名白名单 `ValidTag`:`geosite-` / `geoip-` 开头,后面只含字母、数字和 `. _ ! @ -`(公开规则库的分类名里确有 `@` 和 `!`),不含 `..`,所以拼出的路径不会跳出目录。`Library.Entries` 运行 `sing-box rule-set decompile --output <临时文件> <分类>.srs`(临时文件放 `<数据目录>/tmp`,成功失败都删),读回 JSON 展开成 domain / domain_suffix / domain_keyword / domain_regex / ip_cidr(与 `rulesets/preview` 同一组类型;sing-box 的 Listable 单个字符串写法也认),同类型同内容去重、保持原顺序;逻辑规则和只含端口等别的条件的规则计入 `Skipped`(暂不进接口)不展开;单个分类最多 30 万条,超过截断并让接口多带 `truncated:true`。限制:.srs 上限 32 MiB、解码 JSON 上限 64 MiB、解码超时 60 秒;同一时刻只解一个分类(路由器内存小,也免得同一分类被并发重复解);结果在内存缓存最多 2 个分类 / 合计 20 万条,按文件大小和修改时间失效,失败不缓存,单个分类超过 20 万条不缓存。接口:`tag` 必填;不合法 400、没有这个分类 404、目录不存在或没有 sing-box 503、解码失败 500(带 sing-box 的原因,路径已替换成分类名,不含真实路径);`q` / `offset` / `limit` 与 `preview` 一致;响应多一个 `tag`。**不下载**:前端注释写「本地没有的分类会现下一份」,没有照做——规则库是打包期冻结的,路由器上不联网去取,本地没有就如实 404。测试:假 sing-box 脚本覆盖解析 / 去重 / 空分类 / 各种错误 / 超大文件 / 缓存与失效与淘汰 / 排队时取消 / 超限截断 / 临时文件清理;真实 sing-box 1.14.4 的「编译 → 放进规则库 → 解码」往返和损坏文件报错(没有 sing-box 时跳过);接口层 httptest 覆盖分页 / 搜索 / limit 夹取 / 空数组不是 null / 各错误码 / 方法限制 / `truncated`;paths 的默认值 / 环境变量 / 参数优先级;另用真实二进制加真实 sing-box 走过 设置密码 → 登录 → 查询 的 HTTP 全链路(含路径穿越被拒、未登录 401、临时目录无残留);`go vet`、`go test -race ./...` 全绿。**没验**:没有用真实的公开规则库文件(例如 geosite-cn.srs)验证——沙箱下载那个地址被代理拒绝(403),所以真实规则的形状、条数和第一次解码的耗时都还没见过。真机要做:把真实的 geosite-cn.srs 和 geoip-cn.srs 放进 `-geo-dir` 指向的目录,在面板站点集里点开该分类的详情,应列出域名 / IP 并能搜索、翻页,总条数应与你手里这份规则库的版本对得上;第一次打开的耗时记一下(路由器上),再打开一次应明显更快(走缓存)。下一步:第 29 行把它接进 `policies/entries`。
- 2026-10-26 M1 部署流程 internal/singbox/deploy.go(自己的设计,依据 DESIGN 第四节「变更安全」和前端 `OpenboxDeployResult` 的阶段名,没有参照上游):`Deployer.Deploy` 依次:读服务状态和当前配置(读不到就不动)→ `Generator.Build` → `Files.WriteLocalSets`(有缺的规则集就拒绝,`badTags` 列出 tag)→ `sing-box check`;到这里都没碰正式配置(阶段 validate)。通过后:旧配置备份到 `config.prev.json` → 原子写新配置(0600)→ 部署前在跑就 restart、没在跑就 start(失败是阶段 start)→ 等健康:服务在跑、`clash_api.external_controller` 的 `/version` 带 secret 答 200、常见 TCP 入站(mixed / http / socks / redirect / tproxy)连得上,通过后再稳定 3 秒复查一次以抓住「起来就崩」(失败是阶段 verify,超时 20 秒,返回最后一次原因)。失败即回滚:写回旧配置;部署前在跑的重启并确认旧配置又健康,没在跑的保持停止;第一次部署(没有旧配置)则删掉新配置并停止;部署前在跑却没有配置文件时不擅自停服务,只如实说明。回滚本身失败也如实写进 `message`(含配置和备份路径),不假装成功;写不回旧配置时不再动服务。同一时刻只允许一次部署(第二个请求直接返回 error 阶段);替换之后的步骤不受请求取消影响(`context.WithoutCancel` 加总时限),客户端断开不会把系统留在半截。没做:`conflict` 阶段(与其它代理服务冲突,见「服务状态的剩余部分」)、`pendingRestart`(可以用「上次成功应用的配置哈希」来判断,接 HTTP 时一起做)、启动时清理上次异常退出遗留的状态(备份文件已留,恢复逻辑未做)。测试:假服务覆盖首次部署 / 替换并备份 / 校验失败不碰任何东西 / 规则集缺失 / 生成失败 / 没有 sing-box / 状态未知不动 / 重启失败回滚 / 健康失败回滚 / 首次失败撤销 / 部署前停止则回滚后仍停止 / 回滚重启也失败 / 写不回旧配置不动服务 / 稳定期崩溃 / 并发拒绝 / 取消后仍回滚 / 未配置;默认健康检查的鉴权、控制接口关闭、入站连得上连不上;真实 sing-box 1.14.4 的集成测试:第一次部署成功 → 第二份配置能过 check 但入站端口被占,内核起不来 → verify 失败并回滚到第一份且重新健康 → 第三份成功(没有 sing-box 时跳过);`go vet`、`go test -race ./...` 全绿,amd64 与 arm64 都能编译。真机要做:先在路由器上手动放一份能用的 `config.json`(ConfigDir 下)并让服务跑着;故意让新配置跑不起来(例如占用一个入站端口),调用部署,应看到阶段 verify、`rolledBack:true`,且服务仍在用旧配置;再正常部署一次,应返回 running。
- 2026-10-26 M1 链式代理测速的 IP 地区(自己的设计,依据前端 `OpenboxChainLatencyPayload.ipUrls` / `OpenboxChainLatencyResult.ip` 与 `ChainProxyManager.vue` 的用法,没有参照上游):`internal/probe` 的 `Job` 新增 `Capture`(>0 时取回正文,只认 2xx,3xx 不跟随,空正文算失败,最多读这么多字节,非法 UTF-8 替换)和 `Fallback`(按顺序回退,第一个成功为准,全部失败时 `Error` 按「主机: 原因」逐家列出,每次尝试各有完整超时),`Result` 新增不进 JSON 的 `URL` / `Body`;原来的 `fetchOnce` 并入 `fetch`,普通测速行为不变。`chain-proxies/latency` 的 `ipUrls`:只放行 https、主机在白名单(api.ip.sb / ipwho.is / api.ipapi.is)、无账号、端口 443,主机名转小写并去重,最多 5 个,不合规返回 400 且文案带「ipUrls」(前端 `requestChainTest` 靠它降级);合规时在同一次起内核里并行加一个取正文任务(上限 16 KiB),响应多 `ip:{ok,url,body}` 或 `ip:{ok:false,error}`;线路不通时 `ip` 如实失败,不影响测速结果的返回。取正文不校验对方证书,与测速一致(路由器上常缺根证书包)。测试:probe 层用真实 sing-box 覆盖取正文 / 回退 / 429 / 3xx / 空正文 / 截断 / UTF-8 / 全部失败的原因串 / 普通测速不取正文 / 正文不进 JSON;接口层用真实 core 和真实 sing-box,经测试用前向代理把发往 443 的隧道桥到本机 TLS 服务,覆盖回退、全部失败、不带 / 空数组无 `ip`、各种不合规地址 400、线路不通、`normalizeIPURLs` 去重与上限(没有 sing-box 时跳过);`go vet`、`go test -race ./...` 全绿。真机要做:在前端链式代理编辑框填好链接和上游,点测速,应显示出口 IP、国家和运营商;若首选接口被限流 / 拒绝,应自动回退到后面的接口;全部失败时应显示原因而不是空白。
- 2026-10-26 M1 /api/openbox/policies/entries(自己的设计,依据前端 `OpenboxPolicyEntries` 类型,没有参照上游):`name` 是站点集名;读档案路由 → 核心 `normalizeRouting`(不传 geoTags,免得核心因后端没有规则库清单而丢条目)→ 按名字分三路:等于兜底名返回 `fallback:true` 与空条目;等于前置自定义分流名展开每一行(domain / domainSuffix / domainKeyword / ipCidr / port,`ruleUrl` 拉链接,`geosite` / `geoip` / `ruleset` 报缺);否则展开站点集的四类手写条件(来源 `custom`)和 `ruleUrls`(用 `rulelist.Service` 拉,来源只写「主机 + 路径」,不带 token)。核心会把每个链接折成 `list-<hash>` 追加进 `rulesets`,后端自己实现同一个 FNV-1a(UTF-16 码元)算法(`ListTagForURL`,已用核心金标准里的向量核对,真实核心集成测试也验证两边一致),用它认出已展开的 `list-*`,不当作缺失;`geosite-*` / `geoip-*` 需要随包规则库,如实列在 `missing`(见上面新拆出的未完成条目)。单个链接拉取失败只进 `missing`,不让整个请求失败;取消请求返回 context 错误。`counts` 不受 tab / q 影响,`total` 是当前档、`matched` 是搜索后;条目去重,总量上限 30 万;sort / tab / dir 非法 400,站点集不存在 404,无核心 503。`main.go` 里规则集预览与本接口共用一个 `rulelist.Service`(共享 60 秒缓存)。测试:假核心覆盖兜底 / 自定义 / 站点集 / 缺失 / 拉取失败 / 取消 / 分档 / 搜索 / 排序 / 分页 / 上限 / 凭据不外泄,接口层 httptest 覆盖响应键名、空数组不是 null、各错误码、limit 夹取、方法限制,真实 core 集成测试(没有 Go 时跳过);`go vet`、`go test -race ./...` 全绿,amd64 与 arm64 都能编译。不需要真机。
- 2026-10-26 M1 service / kernel:`system.Services`(Status / Do)按服务管理器分两路——OpenWrt 调 `/etc/init.d/<名字> running|enabled|start|stop|restart|enable|disable`,systemd 调 `systemctl is-active|is-enabled|start…`,运行时长用 `ActiveEnterTimestampMonotonic` 对 `/proc/uptime` 算;服务名只放行 `[A-Za-z0-9_.@-]` 且不以 `-` 开头、操作只放行五个固定词(校验失败不会运行任何命令);命令退出码非 0 返回 200 加 `ok:false`、`code`、`stderr`(前端按这个显示),启动不了命令返回 `code:-1`,超时(状态 10 秒、操作 60 秒)有明确文案;没有服务管理器时 status 返回 `platform:"none"` 和 `raw:"unsupported"`、操作返回 503。`GET /api/openbox/kernel/version` 运行 `sing-box version` 解析版本号,读不到返回 200 加 `ok:false`。`Config` 新增 `CoreService` / `PanelService`(参数 `-core-service` / `-panel-service`,环境变量 `OPENBOX_CORE_SERVICE` / `OPENBOX_PANEL_SERVICE`)。测试:服务名与操作的拒绝、识别服务管理器、两种服务管理器的状态与操作、超时、真实 `ExecRunner` 的退出码、接口响应(含无服务管理器和失败路径),真实 sing-box 1.14.4 的版本读取;amd64 与 arm64 都能编译。没做的见上面「服务状态的剩余部分」。
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
