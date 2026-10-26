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
- [ ] `/api/openbox/config/preview`(依赖订阅 / 节点 / 节点组,排在「配置生成」条目里一起做)
- [ ] `/api/openbox/subscriptions`:列表、新增、修改、删除、排序、刷新、预览(`login` 返回 501)
- [x] 订阅拉取与解析(`internal/sub`:URL 拉取、分享链接 / base64、sing-box 格式、用量头 `subscription-userinfo`、自定义 UA)
- [ ] Clash / Mihomo 格式解析(`internal/sub/clash.go` 现在只识别并返回「暂不支持」;没有 YAML 库可用,要自己写够用的 proxies 子集解析)
- [ ] 订阅改名规则(`renameOptions`:地区词典、模板、序号、过滤、禁用、前缀)与预览用的 `preview` / `groups` 生成
- [ ] `/api/openbox/nodes`、`/api/openbox/groups`、`/api/openbox/policies`、`/api/openbox/chain-proxies`
- [ ] `/api/openbox/rulesets`
- [ ] 配置生成 → `sing-box check` → 原子替换 → 回滚(`internal/singbox`)
- [ ] `/api/openbox/service`、`/api/openbox/kernel`
- [ ] 前端联调:添加订阅 → 生成配置 → sing-box 启动成功 **需真机**

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
- [ ] 延迟历史采集

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
