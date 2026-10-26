# 进度

**怎么用**:从上到下,做第一个没打勾的条目。完成 = 实现 + 测试通过 + 在这里打勾 + 在「日志」加一行 + 提交(`backend: <阶段> <做了什么>`)。
标注「需真机」的条目,代码和单元测试做完后打 `[~]`(代码完成,待真机验证),由维护者真机验证后改成 `[x]`。
接口分组名对应 `docs/ENDPOINTS.md` 里的二级标题。

## 状态:M0 进行中

## M0 骨架
- [x] `go.mod`、`cmd/openbox-backend`、参数 / 环境变量 / 路径(`internal/system/paths.go`)
- [x] HTTP 服务、日志(slog)、优雅退出、统一错误响应 `{ "error": "..." }`
- [x] 原子写存储 `internal/store`(含并发测试)
- [ ] 静态文件服务 + 前端路由回落
- [ ] `/api/auth/*`:setup、login、logout、status、change-password(密码哈希、会话、登录限速)
- [ ] `/api/storage`、`/api/background-image`
- [ ] Actions:`go vet`、`go test`、编译 amd64 / arm64(`backend` 的独立工作流,不动 `release.yml`)

## M1 配置流水线
- [ ] `core` 子进程客户端(`hello` + 15 个方法,超时、输入输出上限、错误传递)
- [ ] `/api/openbox/profile`、`/api/openbox/defaults`、`/api/openbox/config`
- [ ] `/api/openbox/subscriptions`:列表、新增、修改、删除、排序、刷新、预览(`login` 返回 501)
- [ ] 订阅拉取与解析(`internal/sub`:URL、base64、Clash/sing-box 格式、用量头 `subscription-userinfo`、自定义 UA)
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
- 2026-10-25 M0 原子写存储 internal/store(临时文件 → fsync → rename;同文件读改写串行;损坏文件报错不覆盖;含并发测试,-race 通过)。
- 2026-10-25 M0 HTTP 服务(internal/server)、slog、优雅退出、统一错误响应(internal/httpx)、/api/health。
- 2026-10-25 M0 go.mod、入口、参数/环境变量/路径(internal/system/paths.go):命令行 > 环境变量 > 默认值。
