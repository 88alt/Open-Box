# 界面逐页对齐清单(panel/ 开源面板 vs 上游 panel/dist)

**比对口径**
- 上游只有编译后的 `panel/dist`(没有源码),基准是这样跑的:把 `catch-up-upstream` 上的 `panel/dist` 用本地静态服务起起来,
  指向真机后端(`192.168.2.5:3036`),与 main 的 dev 页面并排比对。
- 逐页看:布局、组件、文案、交互、图标、顺序;三语(简体 / 繁体 / English)各一遍,浅色 / 深色主题各一遍,桌面 / 窄屏宽度各一遍。
- 与上游不一致且不属于例外 A/B/C 的,按上游对齐。例外 A/B/C 有意保留差异,不改回去(见 backend/HANDOFF.md 第五节)。
- **红线**:不拷贝、不依赖上游压缩后的 `index.mjs`;只依据界面行为与 dist 里的键与结构反推,自己写实现。

**状态**:`待做` = 还没比过;`有差异待做` = 比过、发现差异还没改;`已对齐` = 比过、一致或差异已修;`有意例外` = 按 A/B/C 故意不同。

## 页面清单

| 页面 | 路由 | 上游对应块 | 状态 |
| --- | --- | --- | --- |
| 代理 | `#/proxies` | `ProxiesPage` | 已对齐 |
| 概览 | `#/overview` | `OverviewPage` | 已对齐(例外 B/C 有意不同) |
| 连接(含日志 / 规则页签) | `#/connections` | `ConnectionWorkspacePage` | 待做 |
| 设置(含订阅 / 分流 / 内核页签) | `#/settings` | `SettingsPage` | 待做 |
| 登录 | `#/login` | `LoginPage` | 待做 |
| 设置访问密码 | `#/setup` | `SetupPasswordPage` | 待做 |

## 独立页面组件(在侧栏之外,由页面内入口进入)

| 页面 / 弹窗 | 组件 | 上游对应块 | 状态 |
| --- | --- | --- | --- |
| 链式代理 | `ChainProxiesPage.vue` | `ChainProxyPage` | 待做 |
| 客户端 App | `ClientAppPage.vue` | `ClientAppsPage` | 待做 |
| 终端分流 | `ClientRoutingPage.vue` | `ClientRoutingPage` | 待做 |
| DNS | `DnsPage.vue` | `DnsPage` | 待做 |
| 内核 | `KernelPage.vue` | `KernelPage` | 待做 |
| 共享网络 | `ShareNetworkPage.vue` | `ShareNetworkPage` | 待做 |
| 订阅 | `SubscriptionsPage.vue` | `SubscriptionsPage` | 待做 |
| 分流 | `RoutingPage.vue` | `RoutingPage` | 待做 |

## 已确认与上游一致的结构(2026-11-25 核对)

- 路由结构一致:侧栏 4 个主页面(代理 / 概览 / 连接 / 设置);`logs`、`rules` 并入「连接」页的页签,
  `subscriptions`、`routing`、`kernel` 并入「设置」页的一级页签,老路由保留为重定向——与上游 dist 里的跳转结构一致。
- 三语文案键集一致(上游 2331 / main 2293,缺 38 全属例外 C),引用一致(真缺口 0)。

## 进度日志

- 2026-11-25:建立本清单,确认路由结构与上游一致。逐页比对从「代理」页开始。
- 2026-11-25 **代理页**:用 DOM 对照而不是只看截图(截图判断反复出过错,以下结论都有 DOM / 代码证据)。
  - 页签、数量、搜索框占位文字、空状态文案:两页**完全一致**(`策略 (14) / 节点 (3) / 订阅 (2)`、「搜索 | 多个关键词用空格分隔」)。
  - **修正一处真实差异**:分组卡片标题上的「域名穿透 / 策略穿透」按钮,上游用
    `hover:bg-(--app-interactive-hover)!` + `whitespace-nowrap`,main 少了 `whitespace-nowrap`,
    且暗色 hover 底色写成了 `#4b4428`(上游 `--app-interactive-hover` 的暗色是 `#264b3d`,亮色 `#f1ead6` 本来一致)。
    已补齐 `whitespace-nowrap`(`ProxyGroup.vue` 两处),并把 `#4b4428` → `#264b3d`(`ProxyGroup.vue`、`ProxyNodeCard.vue`)。
    DOM 复核:`whiteSpace` 两页都是 `nowrap`,按钮类名与上游逐项一致。
  - 分组卡片图标(直连 `misc:dart` / 拒绝 `misc:cross`)与上游**一致**——两边都是后端返回的 `icon` 字段,`core/internal/groups/group.go` 补的正是这两个值,不是前端缺图标。
  - 侧栏顶部标识、侧栏底部资源块:与上游结构一致(同一张 `logo.png`、同样的 `alt="Open-Box"`、同样的「收起侧边栏」按钮)。
  - 未覆盖:三语 × 深浅主题 × 桌面/窄屏的组合矩阵,这一页只在中简体 + 浅色 + 1440 宽下逐项验过。

- 2026-11-25 **概览页**:DOM 对照。
  - 卡片顺序与标题两边一致:`硬件负载 / 网络状态 / 流量洞察 / 节点评分`(侧栏里的系统资源块也在)。
  - 月份页签(`2026年10月 / 11月 / 12月`)、统计块(连接数 / 活跃终端 / 进站速率 / 进出站流量 / 运行时长)一致。
  - **有意差异(不改)**:
    - 上游多一张「业务优选」卡片 → 例外 C,main 整体不做;
    - 上游顶栏是「机场、VPS 安格超市 | 原生住宅 IP TOPIP」这类市场外链 → 例外 B,main 改成两个版本号。
      实测 main 显示「Open-Box v0.1.486」+「sing-box 1.14.4-openbox-60」;
      其中 `v0.1.486` 是**官方面板后端** `/api/openbox/activation` 自己给的 `defaults.left`,不是 main 写死的,所以版本号跟着后端走(当前真机是 v0.1.486 官方包)。
      按预设决策,这里应取「main 最新 release tag」;main 现在取的是构建时注入的 tag(`dev` 构建只显示「Open-Box Latest」),要改成查线上 release 只在 `internal/api/activation` 一处,已记在 STATUS 的推断点里。
