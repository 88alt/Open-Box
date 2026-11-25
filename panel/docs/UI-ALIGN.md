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
| 代理 | `#/proxies` | `ProxiesPage` | 待做 |
| 概览 | `#/overview` | `OverviewPage` | 待做 |
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
