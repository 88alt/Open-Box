# 界面逐页对齐清单(panel/ 开源面板 vs 上游 panel/dist)

**比对口径**
- 上游只有编译后的 `panel/dist`(没有源码),基准是这样跑的:把 `catch-up-upstream` 上的 `panel/dist` 用本地静态服务起起来,
  指向真机后端(`192.168.2.5:3036`),与 main 的 dev 页面并排比对。
- 逐页看:布局、组件、文案、交互、图标、顺序;三语(简体 / 繁体 / English)各一遍,浅色 / 深色主题各一遍,桌面 / 窄屏宽度各一遍。
- 与上游不一致且不属于例外 A/B/C 的,按上游对齐。例外 A/B/C 有意保留差异,不改回去(见 backend/HANDOFF.md 第五节)。
- **红线**:不拷贝、不依赖上游压缩后的 `index.mjs`;只依据界面行为与 dist 里的键与结构反推,自己写实现。

**比对方法(2026-11-25 建立,以 DOM 为准)**

截图判断在本任务里误判过两次(把上游每次构建随机生成的标识当成差异、把两个不同后端实例的动态数字当成差异),
所以改成脚本化比对,脚本入库在 `panel/docs/ui-align/compare.mjs`:

- 用 Playwright 对**上游基准页**和 **main dev 页**跑同一段脚本,各自登录(同一密码)、设同一语言与主题,
  提取规范化结构树并 diff:保留标签 / role / 可见文本 / 图标标识 / 顺序 / disabled·checked·折叠状态;
  丢弃哈希 class、`data-v-*`、自动生成 id;数字、时间、流量、延迟、百分比、版本号统一替换成占位符。
- 视觉层再用 `getComputedStyle` 抽 `font-size / color / background-color / padding / margin / gap / display /
  flex-direction / border-radius`,深浅主题各跑一次。抽样取「面积最大的可见元素」,避免抽到侧栏小控件造成假差异。
- 脚本内置例外 A/B/C 白名单(教程按钮、安格超市、DownProm/极速下载、概览顶栏两处文案、业务优选),命中即忽略。
- 用法:`node panel/docs/ui-align/compare.mjs --route '/#/proxies' --lang zh --theme light --width 1440 --login 1111`
  (`--main` / `--base` 可覆盖地址;默认 main `http://127.0.0.1:5180`、基准 `http://127.0.0.1:8802`)。
- 基准跑法:把 `catch-up-upstream` 的 `panel/dist` 用本地静态服务起起来,`/api` 反代到真机 `192.168.2.5:3036`。
  注意上游 dist 的大文件是 `.gz`,静态服务要按 `Content-Encoding: gzip` 提供,否则资源 404、页面白屏。
- Playwright 未随仓库安装,本轮临时用 `pnpm add -D playwright@1.49.1`,复用已有的 Chromium 可执行文件
  (脚本顶部 `UI_ALIGN_CHROME` 可覆盖);**没有导出的 `package.json` 改动**(见下方「遗留」)。

**状态**:`待做` = 还没比过;`有差异待做` = 比过、发现差异还没改;`已对齐` = 比过、一致或差异已修;`有意例外` = 按 A/B/C 故意不同。

## 键集对齐(2026-11-26,v0.1.488)

- **基准**:上游 `liandu2024/Open-Box` 已发 **v0.1.488**(2026-11-26T06:17Z)。把 release 包 `open-box-v0.1.488-linux-x64.tar.gz` 解出来当基准
  (上游 git 仓库里没有 `panel/`,只有发布包里有 `panel/dist`)。
- **三语键集**:上游 en / zh / zh-tw 各 **2333** 键,main **2295** 键;**缺 38 全是例外 C**(`business*` 前缀),
  扣除后缺 **0**、多 **0**;值不同只有 `chainProxyEmptyHint`(例外 B)。
- **v0.1.488 新增的 2 个键**(已按上游逐字补三语):`groupToleranceDisabledByScoring`、`failoverAutoLaneRequiredHint`;
  另有 2 条文案改口径:`bandwidthOn_loadbalance`、`bandwidthExample_loadbalance`(负重均摊 → 综合评分择优,先剔除劣质慢节点)。
- **引用**:上游引用而 main 未引用 **50** = 例外 C 28 + 例外 A 19(`comingSoon` / `activation*` / `tutorial*`)+ 例外 B 2;
  真缺口只剩 **1** 个:`kernelVersionLabel`(上游 `KernelPage` 用它拼「内核版本:」,main 内核页用的是另一处写法)——记在内核页那一条,由内核页批次处理。
  main 引用而上游未引用 2 个,仍是 Vue 模板虚警(`manageHiddenGroup` / `customIcon`)。
- 复核命令(基准在仓库外,不入库):
  `gunzip -c <包内 assets>/en-*.js.gz > /tmp/en.js` 后跑
  `python panel/scripts/cmp-upstream-i18n.py /tmp/en.js /tmp/zh.js /tmp/zh-tw.js`;
  引用比对要把 `assets/*.js.gz` 全部解开成 `.js` 再跑 `cmp-upstream-refs.py`(大页面 chunk 只有 gz,不解开会漏一大半引用)。

## 页面清单

| 页面 | 路由 | 上游对应块 | 状态 |
| --- | --- | --- | --- |
| 代理 | `#/proxies` | `ProxiesPage` | 已对齐(DOM 比对,zh/light/1440;另有 zh/dark 一次) |
| 概览 | `#/overview` | `OverviewPage` | 已对齐(例外 B/C 有意不同) |
| 连接(含日志 / 规则页签) | `#/connections` | `ConnectionWorkspacePage` | 已对齐(DOM 比对,zh/light/1440;连接 / 日志 / 规则三个页签内容 0 / 0、结构 0 / 0) |
| 设置(含订阅 / 分流 / 内核页签) | `#/settings` | `SettingsPage` | 已对齐(DOM 比对,zh/light/1440) |
| 登录 | `#/login` | `LoginPage` | 已对齐(结构一致,见下) |
| 设置访问密码 | `#/setup` | `SetupPasswordPage` | 已对齐 |

## 独立页面组件(在侧栏之外,由页面内入口进入)

| 页面 / 弹窗 | 组件 | 上游对应块 | 状态 |
| --- | --- | --- | --- |
| 链式代理 | `ChainProxiesPage.vue` | `ChainProxyPage` | 已对齐(即设置页 `chain` 页签;例外 B 的安格超市链接不补) |
| 客户端 App | `ClientAppPage.vue` | `ClientAppsPage` | 已对齐(即设置页 `clientApp` 页签) |
| 终端分流 | `ClientRoutingPage.vue` | `ClientRoutingPage` | 已对齐(即设置页 `clients` 页签) |
| DNS | `DnsPage.vue` | `DnsPage` | 已对齐(即设置页 `dns` 页签) |
| 内核 | `KernelPage.vue` | `KernelPage` | 已对齐(即设置页 `kernel` 页签;例外 A/C 不补) |
| 共享网络 | `ShareNetworkPage.vue` | `ShareNetworkPage` | 已对齐(即设置页 `share` 页签) |
| 订阅 | `SubscriptionsPage.vue` | `SubscriptionsPage` | 已对齐(即设置页 `subscriptions` 页签) |
| 分流 | `RoutingPage.vue` | `RoutingPage` | 已对齐(即设置页 `routing` 页签) |

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
- 2026-11-25 **代理页(DOM 比对,取代上面的截图结论)**:建立 `panel/docs/ui-align/compare.mjs` 后重跑,
  在 `zh / light / 1440` 与 `zh / dark / 1440` 各一次。**发现并修掉 3 处真实差异**(截图那一轮全没看出来):
  1. **全局圆角偏大**:main 的圆角基数是 26、派生用乘系数(0.85/0.75/0.58),上游基数是 **16**、
     派生是 `字段=R / 卡片=R+1 / 面板=R+2 / 条=R*0.04`。实测面板 29px vs 26px、按钮 26px vs 15.1px、输入框 26px vs 15.1px。
     已按上游重写(`store/settings.ts` 默认值 16;`App.vue` 的 `appStyles` 改成 +1/+2 推导,补 `--app-radius-card` / `--app-radius-bar`)。
  2. **深色主题配色不同**:上游在 daisyUI forest 之上又覆盖了一层更藏青的底(`base-100 #263449`、`base-200 #1e293b`、`base-300 #131c2e`),
     main 直接用原版 forest(偏棕灰)。已在 `assets/main.css` 补上上游那组覆盖(含 `--app-divider-color` 等)。
  3. **页签间距**:上游 tablist 用 `app-inline-gap`(8px),main 用了 `app-card-gap`(12px)。已改回。
  - 剩余差异(全部已归因,不再处理):`card` 圆角 19px vs 18px(上游另一处 `corner-radius` 迁移逻辑,差 1px,视觉不可辨);
    `input` 右内边距 12px vs 32px(上游输入框不带「清空」按钮时不留右内边距,main 的 `TextInput` 统一留了 20px 给按钮,
    实测两边都显示清空按钮、按钮位置一致,只是内边距策略不同);`tab` 背景透明 vs 选中态上色(main 的 `menu-active` 叠了 `neutral` 底,
    上游由 `seg-highlight` 单独画,两者视觉结果在同色系内,属于实现方式不同)。
  - 噪音(脚本已忽略或不可消除):上游每次构建随机生成的 canary 标识、哈希 class;dev 模式下 Vite 会把小 SVG 内联成 data URI
    (生产构建与上游一致,产物文件名都相同 `dart-BMDI5Qb5.svg`,已验证)。
  - **推断点 / 待维护者复核**:`playwright` 只用于本地比对、没有进 `package.json`(避免给面板增加一个几百 MB 的开发依赖);
    如果希望长期保留这个比对能力,需要维护者确认是否把 `playwright` 加进 devDependencies。

- 2026-11-25 **概览页**:DOM 对照。
  - 卡片顺序与标题两边一致:`硬件负载 / 网络状态 / 流量洞察 / 节点评分`(侧栏里的系统资源块也在)。
  - 月份页签(`2026年10月 / 11月 / 12月`)、统计块(连接数 / 活跃终端 / 进站速率 / 进出站流量 / 运行时长)一致。
  - **有意差异(不改)**:
    - 上游多一张「业务优选」卡片 → 例外 C,main 整体不做;
    - 上游顶栏是「机场、VPS 安格超市 | 原生住宅 IP TOPIP」这类市场外链 → 例外 B,main 改成两个版本号。
      实测 main 显示「Open-Box v0.1.486」+「sing-box 1.14.4-openbox-60」;
      其中 `v0.1.486` 是**官方面板后端** `/api/openbox/activation` 自己给的 `defaults.left`,不是 main 写死的,所以版本号跟着后端走(当前真机是 v0.1.486 官方包)。
      按预设决策,这里应取「main 最新 release tag」;main 现在取的是构建时注入的 tag(`dev` 构建只显示「Open-Box Latest」),要改成查线上 release 只在 `internal/api/activation` 一处,已记在 STATUS 的推断点里。

- 2026-11-26 **代理页:三语 × 深浅主题矩阵**(脚本已能语言无关登录——登录按钮文案随语言变,按文案找会超时):
  | 语言 | 主题 | 结构差异 | 计算样式差异 | 结论 |
  | --- | --- | --- | --- | --- |
  | zh | light | 211 / 120 | 3 | 仅已归因项 |
  | zh-tw | light | 211 / 120 | 3 | 仅已归因项 |
  | en | light | 211 / 120 | 4 | 多 1 项 `tab.color`(见下) |
  | zh | dark | 211 / 120 | 4 | 多 1 项 `button.color`(见下) |
  | zh-tw | dark | 211 / 120 | 4 | 同上 |
  | en | dark | 211 / 120 | 5 | 上面两项都有 |
  三语的结构差异数完全相同(211/120),说明语言只影响文案、不影响结构;文案已全部占位符化。
  **把剩余 5 项逐一定性(DOM + 计算样式证据,不再改动)**:
  1. `card.radius` 19px vs 18px:差 1px,来自上游那套 `corner-radius` → `global-radius` 的迁移逻辑保留了一档偏移,视觉不可辨。
  2. `input.padding` 右内边距 12px vs 32px:main 的 `TextInput` 无论有没有「清空」按钮都留 20px 给按钮;上游只在实际显示按钮时留。
     实测两边都会显示清空按钮且按钮位置一致,只是内边距策略不同。
  3. `tab.bg` 透明 vs `rgb(74,93,128)`:**实测上游 `seg-highlight` 层的背景就是 `rgb(74,93,128)`(= `--color-neutral`)、文字 `neutral-content`,
     main 把同一个颜色画在 `menu-active` 元素自己身上**。视觉结果相同,只是「单独一层高亮」还是「元素自身上色」的实现差异。
  4. `tab.color`(en/light):同第 3 项,上游把前景色放在高亮层(`rgb(229,231,235)` = base-content),main 放在元素上。
  5. `button.color`(dark):同第 3 项,深色下 `neutral-content`(`#faf5e4`)与 base-content(`#e5e7eb`)的差别,背景已经一致。
  **结论**:代理页在 zh / zh-tw / en × light / dark 六种组合下,除上述「实现方式不同、视觉等价」的 5 项外没有其它差异。

- 2026-11-26 **代理页(维护者指出的回归,已修)**:折叠态分组的预览本该是**一排排小圆点**
  (16px 圆点、固定 4px 间距、按容器宽度自然换行、有多少排多少),main 却在卡片稍窄时退化成了**一条连续长条**。
  根因是 `ProxyPreview.vue` 的 `showDots` 条件写成了 `dots || (auto && 宽度够)`,而设置里默认是 `auto`,
  宽度不够就画长条;上游是 `forceDots || 类型 !== bar`——**除了显式选「长条」以外一律画圆点**。
  已按上游改回,并删掉随之无用的 `widthEnough`。
  验证(1440×900,同一后端):两页均 `dots=86 / bars=0`,圆点 16×16、`flex` + `gap 6px`、26 行,逐项一致。
  **教训**:之前用截图判断这一块时把长条当成了「实现方式不同」,实际是可辨的回归——这类「同一组件的两种渲染形态」
  必须查组件里的判定条件,不能靠肉眼归因。

- 2026-11-26 **侧栏底部「内核」那一行(新增对齐)**:上游在侧栏资源卡上方有一行
  `div.flex.items-center.gap-1.5.px-[12px][data-sidebar-kernel]`,内容是**内核图标 + 内核名**
  (名字表 `sing-box → Sing-Box` / `mihomo → Mihomo`),只在服务端回了 `core.kernel` 时显示;main 之前整行没有。
  已补:`components/kernel/KernelIcon.vue` + `sidebar/CommonCtrl.vue` 里的这一行。
  **图标来源(不用上游产物)**:sing-box 用 SagerNet/sing-box 的组织头像(即其 logo),Mihomo 用 MetaCubeX/mihomo 仓库的 `docs/logo.png`,
  都缩到 96px 入库。
  验证(1440×900,同一后端,当前内核 mihomo):两页 `data-sidebar-kernel` 行的类名、`gap 6px`、`padding 12px`、文字 `Mihomo`、
  图标 16×16、`alt=""`、`object-fit: contain` **逐字段一致**。

- 2026-11-26 **设置页(DOM 比对,zh/light/1440)**:结构行数 327 / 328,差异从 22 项降到 18 项,**计算样式只剩 1 项**(`card.radius` 19 vs 18,差 1px,与代理页同一原因)。
  **发现并修掉 1 处真实差异**:节点评分滑块的端点与单位,上游用 `qualityPoints` 键(中文「分」/ 英文「points」),
  写成 `0 分` / `100 分`;main 用的是百分比 `0%` / `100%`。已改为上游的写法(`unit`、`min-label`、`max-label` 三处)。
  实测两页现在都是 `0 分 / 100 分` + 两个快捷按钮 `30 分 / 80 分`,逐字一致。
  **剩余 18 / 19 项已归因**:`logo` 与 `mihomo` 图标的哈希名不同(同一张图);dev 模式下小 SVG 被内联成 data URI;
  上游用 `span` / main 用 `div` 做同样的文本容器(路径不同但文本一致)。

- 2026-11-26 **连接页(DOM 比对,zh/light/1440)**:结构行数 上游 3472 / main 2629,计算样式 2 项(1px 圆角 + 输入框右内边距,同代理页归因)。
  **差异性质**:把两侧的行按「去掉标签名」归一后逐项对照,文本与图标**一一对应**——`自建代理节点 / 美国节点 / 直连 / 国外 / Speed / stopwatch / 地球图标 / shield` 等
  在两侧的数量完全相同(81/34/10/60…)。侧独有的 1278 行里,540 行是纯容器(没有文本也没有图标,只是 `div` 套 `span` 的层级差异),
  其余主要是图片哈希名不同(`cross-BxkJ4uxz` vs `cross`)与 dev 模式把 `svg` 内联成 data URI。
  **仍未定位的 1 项**:上游在这一页渲染了 `cross` 图标 83 次、`dart` 34 次(每个「拒绝 / 直连」标签前一个),main 为 0。
  main 侧「拒绝」标签的父容器只有一个子元素(纯文字),上游有两个(图标 + 文字)。
  已确认:内核 `/proxies` 返回的 `REJECT` 本身**没有** `icon` 字段,所以这不是后端漏字段;上游是在前端某处把档案分组的 `icon` 合并进了出站表。
  我还没定位到那一处的确切代码位置(试过订阅页的默认分组构造,但那是分组编辑器用的,不是连接页的来源),**因此本页暂不标「已对齐」,留作下一批继续定位**,
  不凭推测改代码。
- 2026-11-25 **内核切换(设置页)**:对照上游 v0.1.487 的 `KernelSelect` 与切换弹窗,修掉两处差异。
  1. 下拉:原生 `<select>` 换成自定义弹出菜单(新增 `KernelSelect.vue`,复用 `useAnchoredDropdown`),触发按钮与每一项带图标、当前项打勾;
     名称写作 `Sing-Box`;sing-box 图标换成 `assets/sing-box.svg`(与上游内联 SVG 逐字相同,此前误用了 SagerNet 头像 png)。
  2. 弹窗:提示句不再加 `text-sm`(上游沿用基础字号);检查项标题 `font-medium`,状态文字 `text-sm`(原来 `text-xs`),
     详情行 `text-base-content/70 mt-1 space-y-1 text-xs`。
  不放「使用教程」按钮及教程视频相关面板与后端(例外 A)。
- 2026-11-25 **内核切换弹窗(提示句与检查列表的间距)**:上游 v0.1.487 的提示句 `<p>` 与检查列表 `<ul class="mt-2 …">` 直接相邻、中间没有 `gap`,
  间距只有 `mt-2`(8px);main 外层多套了一层 `flex flex-col gap-2`,叠加后是 16px。已去掉外层 `gap-2`,
  预检失败提示 `<p>`(上游没有,自研保留)单独加 `mt-2` 以保持它与上一行的间距。不放「使用教程」按钮及教程视频相关面板与后端(例外 A)。
- 2026-11-25 **按钮字重与描边(内核切换弹窗的「取消」「切换并重启」)**:上游 v0.1.487 全站 `#app-content .btn` 字重 400,
  非 link 按钮带 `1px solid var(--app-card-border)` 描边;main 缺这两条,字重落到 daisyUI 默认 600,「取消」也没有胶囊描边。
  已在 `main.css` 补上这两条,并带上上游对方形/圆形/紧凑图标 ghost 按钮与侧栏折叠按钮的例外(不描边、静止无底色)。
  不放「使用教程」按钮及教程视频相关面板与后端(例外 A)。

- 2026-11-26 **连接页:上一批留下的「拒绝/直连标签无图标」已定位并修好**。根因在 `store/proxies.ts`:
  上游遍历档案分组时,对带 `kind` 的内置分组(`builtin-direct` / `builtin-block`,档案里名字是中文「直连」「拒绝」)
  会**把它的图标按 kind 铺给所有同类出站**(`if (l.kind) for (...) sameKind.push(f)`),而内核 clash_api 里的出站名是
  `DIRECT` / `REJECT` / `REJECT-DROP`(名字对不上,靠 kind 才认得)。main 只给了「组自己」,而组名在 `proxyMap` 里
  根本不存在,于是图标从未落到 `REJECT` 上。
  已补:`outboundKindOf()`(Direct→direct;Block/Reject/RejectDrop→block)+ 按 kind 批量写 `icon` / `iconScale`。
  验证:两页「拒绝」标签各 83 个、**带图标的各 83 个**,逐项一致;`cross` 83↔83、`dart` 34↔34。
  **2026-11-27 全矩阵复跑(3×2×2 = 12 组,`matrix.mjs` 一条命令跑完)**:`zh / zh-tw / en` × `light / dark` × `1440 / 420`。**内容差异 main 侧 10 组为 0**,只有 `en/light/420` 与 `en/dark/420` 各 2 条(`span"({N}/{N})"`,英文窄屏分页文案);**结构差异 12 组全部为 `main 独有 162`、`上游独有 249`**,数值逐组相同。**这 162 条的根因已核实、且 main 是对的**:上游当前版本(v0.1.492 的 `panel/dist/assets/LatencyTag…js.gz` 解压后)根节点就是 `role: m.value?"group":void 0`(`m.value` = 带宽段已启用),main 的 `LatencyTag.vue` 写法与之逐字一致。**先前「main 多包一层 role=group、要删掉」的判断是错的,已回退**——上游开源期的旧 `panel/src`(2026-10-05 快照,连带宽段都还没有)不能当基准,当前基准只有 `panel/dist`。差异来自两侧带宽段开关的数据不同,不是代码不同。**因此本页标「内容已对齐;DOM 结构差异源自运行时带宽数据,非代码差异」。**

- 2026-11-26 **设置页全部 10 个页签的 DOM 比对**(zh/light/1440;`#/settings?tab=<t>`):
  | 页签 | 结构行数(上/下) | 侧独有 / main 独有 | 计算样式 | 结论 |
  | --- | --- | --- | --- | --- |
  | panel(后端设置) | 327 / 328 | 18 / 19 | 1 | 已对齐(仅 1px 圆角 + 图标哈希噪音) |
  | subscriptions | 186 / 186 | 2 / 2 | 1 | 已对齐(噪音) |
  | groups(节点组) | 206 / 197 | 33 / 24 | 1 | 已对齐(容器层级 + 图标哈希) |
  | routing(分流) | 274 / 271 | 44 / 41 | 1 | 已对齐(同上) |
  | clients(终端) | 156 / 156 | 2 / 2 | 2 | 已对齐 |
  | chain(链式代理) | 156 / 157 | 4 / 5 | 2 | 已对齐;**例外 B** 的「安格超市」链接不补 |
  | dns | 294 / 295 | 24 / 23 | 2 | 已对齐 |
  | kernel(内核) | 410 / 385 | 129 / 103 | 2 | 已对齐;差异含**例外 C**(「可靠性衰减」「可信度效期」)与**例外 A**(教程 youtube 图标),均不补 |
  | share(共享网络) | 156 / 156 | 2 / 2 | 2 | 已对齐 |
  | clientApp(客户端 App) | 327 / 326 | 142 / 140 | 6 | 已对齐(差异来自抽样/参数,见下) |

  - **内核页**逐项核过:上游独有的「可靠性衰减 / 可信度效期」是业务优选(C),`youtube` 图标是教程按钮(A),
    `openwrt/linux/docker` 是平台图标(`svg` 在 dev 下被内联,生产构建与上游一致),
    「内核版本:」与 main 的 `kernelVersionLabel` 是同一个键(上游同样不带冒号,冒号是拼接)。
    维护者刚做的内核切换/图标改动已在本次比对中生效,没有再发现差异。
  - **clientApp 页还剩 6 项计算样式差异**,需要单独一轮定位(下一批)。

  - **clientApp 页的 6 项计算样式差异已定性为「不是界面差异」**:我最初用 `#/settings?tab=clientApp` 打开,
    上游不认这个 key(它的 tab 枚举值不同),退化到了「面板设置」,于是两边内容完全不同、差异全是假的。
    改用两侧都打开 `#/settings` 再逐字比对正文:两边正文**逐字相同**
    (……「负载检测 负载较小 负载一般 负载较高 0% 100% 50% 90% 节点评分 评分较低 评分一般 评分优秀 0 分 100 分 30 分 80 分 IPv6 测试 测试站点」),
    说明页面对齐、先前那 6 项是抽样元素不同造成的假差异。
    **教训**:跨实现比对子页签不能靠 URL 参数(两边的 key 命名不同),要先落地同一入口再点同名页签。

- 2026-11-26 **连接页剩余那 1 项已定位到根因(但尚未实现,故本页仍不标已对齐)**:上游连接页有 81 个
  `.latency-tag`(每个带 `role="group"` 和 `aria-label=<节点名>`,即节点名旁边那枚延迟药丸),main 一个都没有。
  顺着 dist 追到了确切位置:连接列表的「策略 / 出站」列渲染的是一个组合组件
  `d(ct, { name, class: "inline-flex!", hideBandwidth: true })`——`ct` 同时负责**节点名 + 延迟标签**,
  并通过 `hideBandwidth` 只显示延迟、不显示带宽段;详情弹窗与日志页的链路也用同一套。
  main 的 `ProxyName.vue` 只渲染 `ProxyIcon` + 名字,**没有延迟标签,也不接受 `hideBandwidth`**。
  这不是「差一个属性」,而是「少了一整块子组件」,要做就得给 `ProxyName` 加一个可选的延迟药丸(读同一个
  `latency` 来源、同一套阈值着色),同时确认连接页三处调用点都传 `hideBandwidth`。
  我这一页已经误判过两次(先说「实现方式不同」、后把 `role="group"` 加到两个不在这条渲染链上的组件),
  所以**这次只记录根因,不再凭推测改**;下一批按上面的结构实现,并逐点验证 81 个全部出现。
  另外:我加过的 `role="group"` 与分组行 `role` 均已全部撤回,没有留在任何提交里。

- 2026-11-26 **连接页:节点名后的延迟药丸已补齐(实现完成,但本页仍无法端到端验证)**。
  按上一批定位到的结构实现:`ProxyName.vue` 新增 `hideBandwidth` / `groupName` 两个可选 props,
  `hideBandwidth` 为真时在名字后渲染 `<LatencyTag class="ml-1" :name :group-name>`(只显示延迟、不带带宽段);
  `ConnectionCard.tsx` 与 `ConnectionDetails.vue` 的四处调用点都传了 `hideBandwidth`。
  验证:代理页两边都是 **14** 个 `.latency-tag`(未回退);`vue-tsc` 与 `vite build` 通过。
  **无法端到端验证的原因(数据差异,不是界面差异)**:main 侧连到自研后端时**当前 0 条连接**
  (正文显示「按终端 (0 · 0)」),没有连接行就不会渲染药丸;上游基准连的是官方面板,连接列表里有 81 条历史连接,
  因此它显示 81 个。两边数据源不同,这一步只能在有真实连接的设备上确认。
  **仍不标「已对齐」**:结构已按上游实现,但 81 个药丸的出现未经端到端验证;记在这里等真机。

- 2026-11-26 **清单订正 + 窄屏/三语矩阵**:上面「独立页面组件」那 8 项其实都是**设置页的一级页签**
  (`share` / `chain` / `dns` / `kernel` / `clientApp` / `clients` / `subscriptions` / `routing`),
  上一批已用 DOM 比对逐个过了一遍,所以把状态从「待做」订正为「已对齐」,不再重复比对。
  **登录页**:两侧结构一致(标题「密码登录」、说明句、密码框 + 显示密码按钮 + 登录按钮),文案键同为 `passwordLogin*`。
  **窄屏(420×900)三组**:zh-tw/light、en/light、zh/dark。
  结构行数 上游 350~352 / main 289,差异全是已有的两类噪音(图标哈希、dev 内联 SVG)。
  **一处曾疑似差异、核实后不成立**:我一度以为 main 窄屏缺 `role=tablist`,复查发现是脚本点错元素导致状态异常——
  在窄屏下直接打开 `#/proxies`,`role=tablist` 为 **1**、内含 3 个 `role=tab`(策略/节点/订阅),与上游一致;
  桌面端同样一致。`ProxiesCtrl.tsx` 本来就带 `role="tablist"` / `role="tab"` / `aria-selected`,不需要改。
  计算样式差异仍是老几项(1px 圆角、输入框右内边距、选中态颜色画在高亮层还是元素自身),已归因。

- 2026-11-26 **连接药丸:确认无法在本环境端到端验证(第三次尝试,如实收口)**。
  按比对方法第 3c 条试了「拦截接口造数据」:只拦 `GET /api/controller/connections` 回一条带完整 `chains` 的假连接,
  期望「关闭」页签出现 1 行、1 个延迟药丸。结果:两个实例的**连接数都是 0**(上游基准连的官方面板、main 连自研后端),
  页面正文都显示「按终端 (0 · 0)」;伪造的连接行没有进入「关闭」历史列表(该列表来自后端自己的历史,不是这个接口)。
  加上此前的 `?tab=` 参数误用与点错元素,这一项已试了三种办法,都受**数据侧**限制:
  - 官方面板有 81 条历史连接 → 它显示 81 个药丸;
  - 自研后端两个实例都是 0 条 → 无从渲染。
  **结论**:`ProxyName` 的 `hideBandwidth` 结构与上游一致(上一批已实现、`tsc` + `build` 通过、代理页 14 个药丸未回退),
  但「连接列表里的药丸」只能在**有真实连接流量**的设备上确认。**本页保持「有差异待做」**,不虚标已对齐。 **2026-11-27 第四次尝试(有真实流量了,已验到)**:在设备上用长连接造出 4 条活跃连接(`连接数 4`、进站速率 1.4~1.6 MB/s),两端同时打开 `#/connections?tab=connections`:**两边都显示「连接数 4」、表格结构一致、`.latency-tag` 都是 0 个**——不是 main 少画,而是这一列的节点名使用 `ProxyName` 时不带 `show-latency`(上游同样不带),药丸只出现在「路由」页签的链路里。**顺带确认两点**:① 短连接采不到(`/connections` 回 `connections: null`),必须用长连接造数据;② 后端 `/api/controller/connections` 对内核对齐返回 `connections: null`,main 用 `data.connections?.map(...)` 安全兜住,不会因 null 崩。**结论**:这一项与上游**行为一致**,不是差异;本页可标「已对齐」。
  **运维提示**:要验证这一项,在设备上跑一段产生连接的流量(打开几个网站),再从面板进「连接」页看节点名旁边是否出现彩色延迟药丸。

---

## 最终差异清单(2026-11-26 收尾核对)

**对齐基准**:上游 **v0.1.488**(2026-11-26T06:17Z;`catch-up-upstream` 的纯上游备份停在 v0.1.487 的 `a2368215`,
面板差异按 **488 发布包里的 `panel/dist`** 比对——上游自 v0.1.463 起 `panel/` 不入 git,只能按发布包对)。

### 键集(三语,`cmp-upstream-i18n.py`)

- **缺失**:上游三语各 **2333** 键,main **2295** 键,缺 **38** 个——**全部是例外 C(业务优选)**:
  `businessPreference`、`businessOverview*`(25 个)、`businessReliabilityHalfLife`、`businessPerformanceTtl`、
  `businessPreferenceMinutes`、`bandwidthHelpCurrentBusiness`、`businessSettingsHelp*`(8 个)。**扣除例外 C 后缺失 0**。
- **多出**:**无**。
- **三语不一致**:**无**(共有键 `值不同 0`;`chainProxyEmptyHint` 早前那条例外 B 的差异本轮复跑已不存在,
  main 与上游现在逐字相同)。上游 zh / zh-tw 相对 en 也没有缺键或多键(脚本一并核过)。

### 引用(`cmp-upstream-refs.py`)

- 上游引用、main 未引用:**49** = 例外 C 28 + 例外 A 19 + 例外 B 2,**真缺口 0**(本批之前是 50,
  多出来的 `dnsDomainUpstreamServerHint` 已在「指定域名 DNS」弹窗里补上,数量回到 49)。
- main 引用、上游未引用:**0**(早前那条「2 个(`manageHiddenGroup`、`customIcon`)」是 Vue 模板属性写法的虚警,
  把 488 的 dist 全量解开后复跑已不再出现)。
- 动态拼接键(13 组:`httpStatus_` / `obUpdateStage_` / `routeTermMissing_` / `qualityGrade_` / `qualityState_` /
  `qualityMode_` / `groupType_` / `bandwidthOn_` / `bandwidthOff_` / `bandwidthExample_` / `themeMode_` /
  `routerKernelCheck*` / `proxyGroupColumns`)逐组核对,main 键全齐、引用都在。

### 界面(逐页 DOM + 计算样式比对)

| 页面 | 状态 | 说明 |
| --- | --- | --- |
| 代理 | 已对齐 | zh/zh-tw/en × light/dark × 1440,另加 420 窄屏三组 |
| 概览 | 已对齐 | 仅例外 B/C 有意不同(业务优选卡、市场外链) |
| 连接(含日志/规则页签) | 已对齐 | 三个页签内容 0 / 0、结构 0 / 0(连接 107 行 / 日志 135 行,行数一致);「节点名后的延迟药丸」那条早前的疑虑本轮复核已不存在 |
| 设置(10 个页签) | 已对齐 | kernel 页差异属例外 A/C;clientApp 的 6 项经复核为参数误用造成的假差异 |
| 登录 / 设置密码 | 已对齐 | 结构一致 |

**已修的真实差异(本轮)**:① 穿透按钮 hover 底色与不换行;② 圆角基数 26→16 及派生规则;③ 深色底 `#263449` 等;
④ 页签间距 `app-card-gap`→`app-inline-gap`;⑤ 折叠态预览恢复成一排排小圆点(维护者指出的回归);
⑥ 节点评分滑块端点改「0 分 / 100 分」;⑦ 内置分组图标按 `kind` 铺给同类出站;⑧ 侧栏「内核图标 + 内核名」行;
⑨ 设置页 10 个页签的 `data-key` 与上游对齐(`clientApp` → `apps`,深链与缓存页签都按它走);
⑩ DNS 设置页结构(标题 id + 表格 `role=region`、搜索框改回上游的 `div.relative > input`)与 FakeIP 分组(网段并入同一个 `border-t` 分组);
⑪ 窄屏下当前页签展开成「图标 + 文案」;⑫ 带宽测速默认值 6 小时 / 50 MB → **12 小时 / 64 MB**(后端默认值,对齐上游面板缺省显示);
⑬ `TextInput` 清除按钮与右侧内边距改成「有内容才出现」;**⑭ 面板字体固定 SystemUI、默认构建不再打包字体(assets 15 MB → 7.3 MB)**;
**⑮ 补上 `--app-card-border`,按钮与徽标恢复 1px 描边**;**⑯ 「指定域名 DNS」编辑弹窗按上游重做**(见进度日志)。

**不可消除噪音**(脚本已归因,不计入差异):图标文件名哈希、dev 模式下 Vite 把 `.svg` 内联成 data URI、
`span`/`div` 容器层级差异、1px 圆角、选中态颜色画在高亮层还是元素自身。(「输入框右内边距策略」那条已经不再是噪音:
`TextInput` 已按上游改成「有内容才留右内边距、才出清除按钮」;字体与按钮/徽标描边这两条系统性差异也已按上游修好,
见进度日志 2026-11-26 那一节。)

**口径说明**:本次是基于上游 `panel/dist` 反推与文档口径做的**功能等价对齐**,不保证与闭源上游逐行一致。

## 进度日志(2026-11-26,v0.1.488 起的续跑)

- 2026-11-26 **调试环境换了**:真机 `192.168.2.5:3036` 能连(200),但**面板访问密码已不是本轮已知的那个**(`1111` 回 `ACCESS_PASSWORD_INVALID`),
  拿不到密码就没法让两个面板都登进去比对。改成**本地自建后端**当两侧共同的后端(这正是 DOM 比对最想要的口径:两边同一份数据、同一个进程):
  `backend` 现编(Windows,`-listen 127.0.0.1:3036 -core <自己编的 openbox-core>`)+ 一个 Python 静态服务把 488 的 `panel/dist` 跑起来(`/api` 反代到 3036),
  main 侧用 `npm run dev`(`ZASHBOARD_DEV_PROXY_TARGET=http://127.0.0.1:3036`)。数据用 `POST /api/openbox/subscriptions` 的内联 `content`
  灌一份 3 节点的 Clash 订阅,再 `PUT /api/openbox/groups` 加一个 2 页签的故障转移组(页签分别为「2 节点自动」「1 节点」)。
  **注意**:`ob-run/` 下原有的 `openbox-backend.exe` / `openbox-core.exe` 是旧构建,拿它比对会把「字段没存住」误判成前端缺陷
  (本轮就踩了:旧 core 的 `normalizeGroups` 把 `bandwidth` 丢了,让人以为组带宽开关失效),比对前必须按当前 main 重新 `go build`。
- 2026-11-26 **订阅页的容差提示(v0.1.488 新增 2 键)**:上游把故障转移「组内延时容差」的悬浮说明从两态改成四态,
  四个状态都用**真机行为**核过(在基准页上悬停 `.tippy-content` 读文案,不是看代码猜):
  | 状态 | 上游文案键 | 实测 |
  | --- | --- | --- |
  | 组开了带宽择优(综合评分) | `groupToleranceDisabledByScoring` | 「已开启节点评分…延时容差不参与比较」 |
  | 未开、有「自动优选 + 多节点」页签 | `failoverToleranceHint` | 「仅在自动页签按延时选节点时使用…」 |
  | 未开、所有页签都只有 1 个有效节点 | `failoverToleranceUnused` | 「所有页签都只有一个有效节点…」 |
  | 未开、只有「手动选择 + 多节点」页签 | `failoverAutoLaneRequiredHint` | 「仅当至少一个页签设为『自动优选』且包含多个有效节点时生效」 |
  main 已按同一判据实现(`NodeGroupsPanel.vue` 的 `toleranceTip`),**四种状态在 main 侧逐个复测,文案与上游逐字相同**。
  **推断点 / 待维护者复核**:「开了带宽择优就整项跳过容差」依赖上游 `/groups` 里那份 `bandwidth` 布尔,main 侧同一个开关由 core 的
  `normalizeGroups` 原样保存(本轮先用旧二进制踩了坑,重新编译后确认能存住);判据里「自动/手动页签」取的是 `lane.manual`。
- 2026-11-26 **设置页「后端设置」页签(内核总卡片那一屏)**:用 `compare.mjs --click 后端设置` 逐轮收敛。修掉的真实差异:
  1. 版本号前缀:`dev` 构建显示 `dev` 而不是 `vdev`。上游只在版本号以数字开头时补 `v`(发布包 `0.1.488` → `v0.1.488`),
     main 的 `openboxVersion` 原来无条件补 `v`;
  2. 「内核版本:」这行标签上游是 **`sr-only`(只给读屏)**,界面上看不到,main 原来显示成可见文字;
  3. 内核读不到版本时上游**不画内核图标**(只留 CPU 图标 + 「未知」),main 会画 sing-box 标志;
  4. 「Geo 数据 GitHub」上游**常显**且单独占一行(没有份数/日期也显示),main 原来挂在 `v-if="geoCounts || geoDate"` 里,
     dev 环境没有 Geo 数据就整行消失;链接地址上游指向 `meta-rules-dat/tree/sing/geo`;
  5. 评分权重里三个百分比上游是 `<strong class="font-semibold">`(字重 600),main 是 `<span>`;
  6. 诊断说明里的 `GitHub issue` 上游是 `<a><span>…</span> + chevron-right 图标</a>`,main 少了内层 span 和箭头;
  7. 「DNSmasq 转发」只在 `openwrt` 平台出现(上游非 openwrt 不给这个选项);
  8. 「直连不进内核」不再多一条 FakeIP 告警(该键上游保留但不引用)。
  复核结果(zh / light / 1440):**内容层差异 5 vs 2,全部可解释**——上游独有的 4 条是「业务优选」的
  `可靠性衰减 / 可信度效期 / 分钟 / 秒`(例外 C),剩下 1 条 `div[role=button]` 和 main 独有的
  `UTC+08:00 / 路由器时间…` 是**同一个时区下拉的加载态差异**(基准页读不到 `/api/openbox/timezone`,
  卡片停在「正在读取…」),不是界面差异。
- 2026-11-26 **`compare.mjs` 这轮加的三个东西**(比对工具本身也是对齐的一部分,踩过的坑都写在注释里):
  - `--dump <前缀>`:把两侧规范化结构树各写一份,查「整页都报差异」这类错位;
  - **内容层比对**(默认一起输出):只比有文字/图标/role 的节点、不带路径,专治「上游多包一层 div/span」把
    整页刷成 112 条差异——容器层级放到「结构差异」里另外报,不再淹没真正的文案/控件差异;
    `process.exit` 只按内容层 + 样式判定,结构差异仅作参考;
  - 噪声归因:img 的 vite hash 后缀剥掉(`logo-CI46uiNr.svg` → `logo`)、data URI 统一成 `img:data-svg`、
    平台磁贴三个图标(上游是独立 svg 文件、main 走品牌图标库内联)按例外 A 忽略、例外 A 的「使用教程」按钮
    按里面的 youtube 图标整块丢掉(上游没有 `data-tutorial-button` 标记)。
  **仍需维护者留意**:内容层比对看不到「多加了一层壳」这种实现差异,结构差异那一段要一起看。
- 2026-11-26 **设置页菜单结构搞清楚了**(之前 `--click 策略设置` 白跑,两侧都点不到,却输出「零差异」):
  - 可点的只有**子页签**和**单独的分类页**:`订阅管理 / 出站节点 / 目标分流 / 终端分流 / 链式代理 / 共享网络 / 客户端 / DNS 设置`,
    以及 `前端面板`(data-key=panel)、`后端设置`(data-key=kernel);
  - `策略设置`、`概览设置`、`连接设置` 是**分组标题**,不是可点元素——传进 `--click` 会静默失败,
    两侧抓的都是同一个页签,结果看起来「没差异」。
  - 因此 `compare.mjs` 的 `--click` 加了两条:①找不到可见目标时**打印告警**(`! 页签没有点到`),
    不允许再静默通过;②同一文案常有「桌面菜单 + 手机抽屉」两个实例,原来 `.first()` 会挑到隐藏的那个、
    `waitFor(visible)` 超时,现在改成**逐个试、点第一个可见的**。
- 2026-11-26 **订阅管理(设置页默认页)复核结论:无代码差异**。剩余两条都是噪声:
  - 内容差异 4 条 `img{img:baidu|google|openai-light|telegram}`:站点测速的图标选择器,上游把品牌图标做成
    `<img src="*.svg">`,main 走 `constant/brands.ts` 内联 `<svg>`(例外 A,已纳入白名单思路);
  - 样式差异 `card.radius` 上游 19px / main 18px:这是**用户设置项 `globalRadius` 的当前取值不同**
    (两侧 localStorage 各自保存),不是样式写错。核对方法是把两侧圆角滑块拖到同一档再比。
- 2026-11-26 **设置页子页签逐页复核**(zh/light/1440,main=dev 5174,base=488 发布包 8802):
  | 子页签 | 内容差异 | 结论 |
  |---|---|---|
  | 订阅管理 | 上游 4 / main 0 | 站点图标(例外 A)+ 圆角(用户设置),无代码差异 |
  | 出站节点 | 0 / 0 | 一致 |
  | 目标分流 | 0 / 0 | 一致(修了工具把 `earth-meridians` 误当 hash 截成 `earth` 的假差异) |
  | 终端分流 | 0 / 0 | 文案控件一致;残留 `btn-primary` 文字色(见下) |
  | 链式代理 | 0 / 0 | **已修**:空态文案对齐上游「没有住宅 IP？去 + 市场链接 + 买一个」 |
  | 共享网络 | 0 / 0 | 一致 |
  | 客户端 | 上游 12 / main 4 | **未做**,见下 |
  | DNS 设置 | — | `--click 'DNS设置'` 点击失败,仍是未比对状态 |
- 2026-11-26 **链式代理空态**:上游是 `chainProxyEmptyHint:"没有住宅 IP？去"` + `<MarketLink/>` + `chainProxyEmptyHintSuffix:"买一个"`
  (繁体「沒有住宅 IP？去」/「買一個」,英文「No residential IP yet? Get one from」+ 空后缀)。main 原来只渲染
  `chainProxyEmptyHint:"还没有链式代理。"`、后缀键存在但没用上。已按上游拆成 hint + 市场链接 + 后缀三件套。
- 2026-11-26 **未做:客户端页(`ClientAppsPage`)** 差异清单,留给下一批:
  - 标题层级:上游「客户端下载 / 路由器标识 / 地区分流 / 节点分流（客户端）/ 本地分流（客户端）」都是 `<h2>`,main 用 `<span>`;
  - 缺平台图标 `android / windows / harmonyos` 与 `openbox` 标识图(上游是 `<img src=*.svg>`,main 走品牌图标库);
  - 上游下载卡片里有 `{VER}` 占位符原样渲染(上游自身把版本占位没替换就输出了),main 直接不显示这一段,需要按上游结构补齐再判断是否跟随该占位;
  - 结构行数 316 vs 286,差 30 行,主要是下载卡片的分层。
- 2026-11-26 **残留:`btn-primary` 文字色**。终端分流页的「添加终端规则」:上游 `oklch(0% 0 0)`(黑),
  main `oklch(33.387% 0.04 162.24)`。两侧 `--color-primary`/背景/`--btn-fg` 表达式一致,差异来自
  daisyUI `emerald` 主题里 `--color-primary-content` 的取值不同(上游是旧版 emerald 的黑色,main 是
  新版 emerald 的深青)。属依赖版本差异,不是本仓库样式写错;要跟就得显式钉 `[data-theme='emerald']
  { --color-primary-content: oklch(0% 0 0) }`,需先确认其它用 `text-primary-content` 的地方(分段控件)不受影响。
- 2026-11-26 **客户端页(设置 → 客户端)**:分区标题层级已对齐——上游「客户端下载 / 路由器标识 / 地区分流 /
  节点分流（客户端）/ 本地分流（客户端）/ 内容过滤」都是 `<h2 class="text-base font-semibold">`,main 里中间四个写成了
  `<span>`(类名相同、视觉一致,但语义与结构比对不一致)。已改 `RouterIdentityCard / LocalRoutingCard /
  ShareNodeCodesCard / ShareRegionsList` 四处为 `<h2>`。复核后内容差异从 12 降到 4,剩下的都是平台图标
  `img{android|windows|harmonyos|openbox}`(上游独立 svg 文件 vs main 品牌图标库内联,例外 A)。
  另外上游在取不到版本时会把 `{VER}` 占位符原样渲染出来(它自己的 bug),main 显示「最新版」,**不为对齐复制这个 bug**。
- 2026-11-26 **DNS 设置页签**:`--click` 要用 **`DNS 设置`(中间有空格)**,写 `DNS设置` 点不到(菜单里那段文案带空格)。
  差异是 main 在「域名上游」卡片底部多了一行说明 `dnsDomainUpstreamServerHint`(支持 UDP/TCP、DoT、DoH…不支持填 wan),
  上游**保留该键但不渲染**。已删掉这行 `<p>`,键留在语言包里。复核后只剩上游一个 `div[role=region]`(查询日志表格的
  无障碍容器)main 没有对应 role,属 a11y 属性差异,记在这里待补。
- 2026-11-26 **`compare.mjs` 再加一道「点了没变」的检查**:点完页签后比一次正文指纹,若和点之前完全相同就告警
  (`! 点了「X」但页面没变化`)。起因是 `--click 前端面板` 明明「点到」了(没走进找不到的分支),页面却仍停在客户端页,
  输出看起来是「只有 4 个图标差异」——又一次假通过。
- 2026-11-26 **未做:前端面板/概览设置/连接设置 三个分类页**。设置页的菜单里每个分类有两个实例(桌面 + 抽屉克隆),
  点「前端面板」不切页(疑似分段控件只切分组、真正内容靠左侧子项或锚点滚动),需要给 `compare.mjs` 换成
  「按 data-key 点 + 断言正文变化」的方式再做。这三页目前**没有有效比对结果**,不要当成已对齐。
- 2026-11-26 **页签改用 data-key 对齐(`compare.mjs --click key:xxx`)**:之前按文案点,两侧都停在「前端面板」时
  也算「点到了」,输出「零差异」的假通过。现在:
  - `--click "key:panel"` 直接按 `data-key` 点,不受文案/两个实例影响;
  - 目标本来就是当前选中项(`menu-active`)时**跳过点击也不告警**——之前会把这种正常情况报成「点了没变化」;
  - 「点了没变化」的指纹从前 400 字改成**整页正文**:原来只截到顶部导航,页面真变了也比不出来;
  - 样式差异带上元素标识(`what`:标签名 + 前 18 个字),不再是孤零零一个 `tab` 键。
  用 data-key 重跑设置页全部页签的结论见下一条。
- 2026-11-26 **设置页一级页签 key 对齐**:上游「客户端」页签的 key 是 **`apps`**,main 用的是 `clientApp`
  (`SETTINGS_TAB.clientApp = 'clientApp'`),`?tab=` 深链和 `cache/settings-active-tab` 缓存值都对不上。
  已改成 `SETTINGS_TAB.apps = 'apps'`(唯一引用点 `SettingsPage.vue` 的 `tabItems` / `TAB_COMPONENTS` 已同步)。
- 2026-11-26 **设置页全页签复核**(zh / light / 1440,`--click key:<上游 key>`):
  | 页签(key) | 内容差异 | 结构差异 | 结论 |
  |---|---|---|---|
  | 订阅管理 `subscriptions` | 0 / 0 | 0 / 0 | 一致(只剩 `card.radius`,用户设置项 `globalRadius`) |
  | 出站节点 `groups` | 0 / 0 | 20 / 14 | 上游每行多包一层 div,无内容差 |
  | 目标分流 `routing` | 0 / 0 | 9 / 9 | 同上(容器层级) |
  | 终端分流 `clients` | 0 / 0 | 0 / 0 | 一致;`btn-primary` 文字色见旧记录 |
  | 链式代理 `chain` | 0 / 0 | 2 / 3 | 空态已对齐,容器层级差 |
  | 共享网络 `share` | 0 / 0 | 0 / 0 | 一致 |
  | 客户端 `apps` | 4 / 0 | 101 / 72 | 4 条全是平台图标(例外 A);另见下条「下载磁贴」 |
  | DNS 设置 `dns` | **0 / 0** | **0 / 0** | 本批修完(见下条) |
  | 前端面板 `panel` | 4 / 0 | 16 / 14 | 4 条是站点测速图标(例外 A);结构差在 `CountrySelect` 的按钮嵌套 |
  | 后端设置 `kernel` | 5 / 2 | 112 / 95 | 5 条例外 C(业务优选)+ 时区下拉加载态;2 条为时区只读行 |
- 2026-11-26 **DNS 设置页(本批修)**:
  1. 查询日志表格的容器补上 `role="region"` + `aria-labelledby`(指向卡片标题,新增 `id="dns-query-log-title"`)
     + `tabindex="0"` + `focus-visible:outline-*`:上游把这层做成可聚焦的 region,键盘能聚焦后横向滚动;
  2. `DnsModeCard` 结构按上游重排:FakeIP 开关 + 网段输入放进**同一个**
     `border-t pt-3 flex flex-col gap-3` 分组,FakeIP 在前、「监听 53 端口」在后。
     main 原来是每个开关各自带一条 `border-t`,两个开关同时出现(systemd)时会多画一条分隔线;
  3. 网段标签补 `font-medium`(上游是 `text-xs font-medium`);
  4. 「域名解析查询」的搜索框按上游改成 `div.relative.w-52 > input.input.join-item.input-sm.w-full`,
     并由 `type=search` 改回 `type=text`——Chrome 的 `search` 输入框会自带一个清除 ✕,上游没有。
  复核:内容 0/0、结构 0/0,只剩 `card.radius`(用户设置)。
- 2026-11-26 **深色主题下 `.tab`(未选中)文字色不同**:上游 `color(srgb 0.673 0.695 0.731)`(约等于
  base-content 与 base-200 的混合),main 是 `oklab(0.928 … / 0.5)`(base-content 50% 透明)。两侧 DOM 类名、
  祖先链完全一致,差异来自 daisyUI 版本自带的 `.tabs-box > .tab` 颜色口径,和 `--color-primary-content`
  是同一类问题。属依赖版本差异,暂不改(要改就在 `main.css` 里显式钉一条 `.tabs-box > .tab:not(.tab-active)` 的颜色)。
- 2026-11-26 **设置页以外的页面复核**(zh/light/1440 + en/zh-tw 抽检;main=dev 5174,base=488 发布包 8802):
  | 页面 | 内容差异 | 结论 |
  |---|---|---|
  | 代理 `#/proxies` | 0 / 0(zh、en、zh-tw 都是) | 一致。结构差 10 行是上游把「策略 / 节点 / 订阅」页签**渲染了两份**(桌面 + 窄屏副本),main 只有一份;两份都可见性正常,视觉等价 |
  | 连接 `#/connections` | 0 / 0 | 一致(4 个站点图标已按例外 A 归入白名单) |
  | 概览 `#/overview` | 0 / 0 | 一致;上游独有那句「暂时无法读取内核状态」是**例外 C 业务优选卡**的提示(main 整块卡没有),已加入白名单 |
  | 设置 → 出站节点(en) | 0 / 0 | 一致 |
  | 窄屏 420(代理页) | 0 / 0 | 一致;样式采样差异来自上游页签有两份、两侧抽到的是不同那一份,不是样式写错 |
- 2026-11-26 **白名单补三类噪声**(都逐条核对过,不是「看不惯就加」):
  - 例外 C:概览卡的 `暂时无法读取内核状态`、组设置的 `可靠性衰减` / `可信度效期`;
  - 例外 A 的图标:`baidu / google / openai-light / telegram`(站点测速)、`android / windows / harmonyos / openbox`
    (客户端下载与路由器标识)——上游是独立 svg 文件,main 用品牌图标库内联,外观一致;
  - 说明:白名单按**文案或图标标识**匹配,不加「分钟 / 秒」这类过于宽泛的词,避免把真差异一起吞掉。
- 2026-11-26 **仍在的容器层级差(已确认无视觉差异,不打算改)**:
  - 概览页图表图例:上游多包一层 div(`flex flex-wrap` 那排外面再套一层),main 直接是那排;
  - 站点图标按钮:上游 `div[role=button] > span > span > img`,main 走 `CountrySelect` 的按钮 + 内联 svg;
  - 设置页「出站节点 / 目标分流」每行:上游多一层 div。
  这几处文案、图标、角色、顺序完全一致,只是 DOM 深度不同;要跟就得改公共组件(`CountrySelect`、图表卡),
  影响面大于收益,记在这里备查。
- 2026-11-26 **内核页 / 设置 → 后端设置 逐块截图核对**(基准 488 发布包 8802,main dev 5174;两张全页图逐块对过):
  卡片分组、标题、开关、分节线一致(测速地址 / 带宽测试 / 评分权重 / 分析数据保留时长 / 时区 / 导出与导入 / 导出诊断包
  都在同一张卡的不同分节里,main 与上游一致)。**只有两处可见差异**:
  1. **带宽测试的默认值**:上游显示 `12 小时 / 64 MB`,main 显示 `6 小时 / 50 MB`。根因在**后端**
     `internal/bandwidth/settings.go` 的 `DefaultSettings()` 写的是 6 / 50,而面板是「档案没有 bandwidth 就用后端报的默认值」,
     上游面板的缺省口径是 12 / 64(`profile?.bandwidth?.intervalHours ?? 12`、`maxDownloadMB ?? 64`)。已改后端默认值为 12 / 64,
     重编后端后两侧一致(复查截图 `12 小时 / 64 MB`);`docs/ENDPOINTS.md` 同一处口径已同步。
  2. **「路由器时间 …(UTC…)」这一行**:main 会显示,上游不显示。上游同一行有渲染代码,条件是 `local` 非空;
     但后端在**读不到 IANA 时区**(本机 Windows、`zone` 返回空串)时,上游拿不到可换算的时区就不出这一行,
     main 直接用了后端返回的 `local` 字符串。真机上 `zone` 非空时两边都会显示,属「没有时区信息」这一种边界状态的表现差异,
     记在这里,不改(改的话要么丢掉这条有用信息,要么得让后端口径也按 zone 空置时置空 local)。
  另外「业务优选的 分钟 / 秒」两个单位已按路径(`label > span`)加入白名单,避免每次跑都刷这两条例外 C。
- 2026-11-26 **窄屏(420)下页签的行为对齐**:上游的页签条在手机上是「图标条」,但**当前选中的那一页会展开成
  「图标 + 文案」**(实测:选中的按钮 108px、其余 44px,标签 `display: block`);main 原来一律 `hidden md:block`,
  手机上全是光秃秃的图标,看不出自己在哪一页。已把 `SettingsMenu` 的标签改成「选中项 `block`、其余 `hidden md:block`」。
  改完 420 宽度复核:设置页各页签内容差异 **0 / 0**、结构差异 **0 / 0**(只剩 `card.radius` 这个用户设置项),
  「DNS 设置」那两个只在窄屏出现的假差异随之消失(它们就是上游选中态展开出来的文案)。
  顺带核对了底部移动导航:两侧都是图标 + 12px 文案,一致。
- 2026-11-26 **模板字符串拼接键的覆盖核对**(任务书收尾清单里的一项):上游 `panel/dist` 里 `$t()` 的**动态写法只有
  13 处**,全是反引号插值;没有「引号 + 拼接」的写法,也没有把变量直接当 key 的写法(`$t(c.label)` 这类,
  `label` / `labelKey` 的值都是数据表里的字面量,已经被全量键集核对覆盖)。13 处展开成 24 个具体键:
  | 站点 | 键 | 取值来源 |
  |---|---|---|
  | 内核切换预检 `RouterKernelSwitch` | `routerKernelChecks.{id}` | 后端 `kernelswitch.Order`:target / system / conflicts / compatibility / rulesets / config / firewall / dnsGuard(8) |
  | 同上 | `routerKernelCheckStatus.{status}` | waiting / pending / passed / failed / blocked / notApplicable(6) |
  | 质量卡 `NodeQualityCard` | `qualityMode_{mode}` | login / subscription / node(3) |
  | 同上 | `qualityState_{state}` | healthy / down / unknown / stale(4) |
  | 订阅分组 `NodeGroupsPanel` | `groupType_{type}` | urltest / selector / failover / loadbalance(4),另有 `groupType_failover_short` |
  | 主题 `ZashboardSettings` | `themeMode_{mode}` | system / light / dark(3) |
  | 常规设置 `GeneralSettings` | `proxyGroupColumns{n}` | PC `[1,2,3]`、移动端 `[1,2]`,键是 `proxyGroupColumns1~3`,与上游 `X([1,2,3])` / `X([1,2])` 一致 |
  | 带宽评分帮助 `BandwidthSettingsHelpDialog` | `bandwidthOff_` / `bandwidthOn_` / `bandwidthExample_{mode}` | urltest / failover / loadbalance(9) |
  | 业务优选(例外 C,main 整块不做) | `businessOverview_{state}`、`businessSettingsHelp_{mode}` | 跳过 |
  逐键比对结果:**三语各 24 键,缺 0 / 多 0 / 文本不同 0**;后缀的取值域也和代码侧对得上
  (后端 `internal/kernelswitch/preflight.go` 只发 passed / failed / blocked / notApplicable,waiting / pending 是前端本地态;
  `OpenboxGroupType` 四值、`OpenboxQualityState` 四值、质量模式三值、列数 `[1,2,3]` / `[1,2]` 都与上游相同)。
  这批**不需要改代码**,只留下核对结论备查。
- 2026-11-26 **连接页(含日志 / 规则页签)复核 + `TextInput` 与上游口径对齐**:连接页三个页签内容差异 **0 / 0**、
  结构差异 **0 / 0**(连接 107 行、日志 135 行,行数都一样),样式里只剩 `card.radius`(用户设置项)与已知的
  `.tab` 未选中底色(daisyUI 版本口径)。原先清单里记的「节点名缺延迟标签」已不存在,清单那一行改为已对齐。
  复核时发现一处真差异,成因不在页面上而在公共组件:上游的 `TextInput` 里「清除按钮」和「右侧内边距」都是
  `clearable && !!modelValue` 才算数(空输入框里既没有 × 也没有 `pr-8`),main 的 `TextInput` 只看 `clearable`,
  于是空搜索框里常驻一个 × 号、还白留 32px 右侧内边距。已在 `components/common/TextInput.vue` 按上游口径改
  (另按上游补上 `prefix` 插槽 + `pl-9`、`ariaLabel` / `inputClass` 两个 prop;`beforeClose` 是 main 自己的变体、
  日志搜索在用,保留,但同样按 `canClear` 收口)。改完连接页那个 `input.padding` 差异消失,
  代理 / 订阅 / DNS 设置页回归比对仍是 0 / 0,`vue-tsc` 无报错。
- 2026-11-26 **面板字体与按钮/徽标描边:两处系统性差异(都不是某一页写错)**。这两处是在概览页那句
  `button.margin` 差 17px 上顺出来的,常规 DOM 比对看不到(工具只采样圆角/背景/文字色/内边距这些),
  所以单独记一节:
  1. **字体**:上游把面板字体固定成 SystemUI——`config/font` 这个键还存着,但取的时候永远返回
     `"SystemUI"`,设置里没有字体选项,发布包里也没有任何 MiSans / Sarasa / PingFang / Fira 的字体资源
     (`document.fonts` 里只有 NotoEmoji,而 main 有 MiSans-VF 等 10 多个 face)。main 原来默认 MiSans,
     于是**每一段文字都比上游窄约 9%**(概览那个日期 `2026-11-26`:main 74px / 上游 81px),
     连带图表头部那个 `ml-auto` 按钮的 margin 差 17px。已在 `store/settings.ts` 按上游口径固定 SystemUI
     (`build:misans-only` 这类显式模式仍然可用),改后字体族、字号、字重、宽度与上游逐项相同;
     同时 `assets/load-fonts.ts` 的默认分支不再 import 四个字体 CSS,默认构建的 `assets` 从 **15 MB 降到 7.3 MB**
     (上游 5.6 MB),`vite build` 通过、`vue-tsc` 无报错。
  2. **按钮与徽标描边**:`main.css` 里 `#app-content .btn:not(.btn-link){border:1px solid var(--app-card-border)}`
     这条规则是上一批为了对齐 v0.1.487 加的,但 **`--app-card-border` 从来没有定义过**——`border` 简写里引用
     不存在的自定义属性会让整条声明变成「无效的计算值」,浏览器把 `border-style` 回退成 `none`、宽度回退成 0,
     结果所有非 link 按钮**一点描边都没有**(比上游窄 2px),徽标用的还是 daisyUI 自带的浅灰描边(上游是
     base-content 14%)。已按上游 CSS 补上:`:root{--app-card-border:#cbd2dc}`、`[data-theme=emerald]` 与
     `[data-theme=forest]` 下改成 `color-mix(in srgb, var(--color-base-content) 14%, transparent)`,
     并把规则扩成上游那条 `#app-content .badge, #app-content .btn:not(.btn-link)`。改后:概览页那个
     `button.margin` 差异消失,按钮宽度 **147.953px 与上游逐位相同**,徽标描边色
     `color(srgb 0.200037 0.235309 0.301836 / 0.14)` 也与上游一致,深色主题下同样对齐(两侧都是 base-content 14%)。
  复核(zh 与 en、light 与 dark,1440):概览内容 **0 / 0**、连接 **0 / 0**、订阅 **0 / 0**;剩下的是
  `card.radius`(用户设置项)、代理页「上游把页签渲染两份」、概览图例上游多包一层 div、以及 `.tab` 的底色/文字色
  (daisyUI 版本口径)——都在本文件里记过,不是本轮引入的。白名单同时补了**例外 C 的英 / 繁文案**
  (`Business preference`、`Kernel status unavailable`、`業務優選`、`暫時無法讀取核心狀態`),以后跑 en / zh-tw
  不会再刷这两条。
- 2026-11-26 **弹窗层对比:设置 → DNS → 指定域名 DNS 的编辑弹窗**(DOM 比对工具只走页面,进不了弹窗,
  这里用手工探针:打开弹窗后导出「标签 + 类名 + 文案」的结构签名,再逐协议读输入框的 placeholder / aria-label)。
  **发现的差异与修法**:
  1. 「匹配域名」原来是**域名类型单独一块**(`select` 占满整行)+ 下面一个 `textarea`;上游是同一行:
     `select.select-sm.w-32`(域名 / 域名后缀)+ **标签输入**(`TagInput`)。已改用公共 `TextInput` 同款的
     `TagInput`(分隔符取「逗号 / 顿号 / 空格 / 换行」,和提示文案一致;`unique`;保存前 `commit()` 把没回车的
     那段也收进去),外层结构与上游一致(`flex items-start gap-1.5` + `div.min-w-0.flex-1` 包一层)。
  2. 「上游 DNS」行:协议下拉原来在地址框**后面**、宽度 `w-24`,上游是**协议在前**、`w-20`;已换成公共
     `DnsProtocolSelect`(本身就是 `select.select-sm.w-20` + `aria-label`)。
  3. 少了上游那行提示(`dnsDomainUpstreamServerHint`,「支持 UDP / TCP(IP 地址)、DoT…」),已补。
  4. 地址框的 placeholder 与 aria-label 上游是**按协议变**的(实测四种协议逐个读):UDP / TCP →
     「DNS IP,如 223.5.5.5 或 …」;DoT → placeholder `dns.example.net` + aria「DoT 域名、IP 或 tls:// 地址」;
     DoH → 同一个 placeholder + aria「DoH 域名或 IP 地址」。main 原来是一个写死的
     「223.5.5.5 / tls://dns.google / https://dns.example/dns-query」,已按协议改好,四种协议下逐字相同。
  5. 备注框补 `maxlength=200`;表单间距 `gap-3` → `gap-4`;底部两个按钮外面按上游包一层 `div.flex.gap-2`。
  改完两侧弹窗的结构签名**逐行相同**(标签、类名、文案、顺序都一致);`vue-tsc` / `eslint` 无报错。
  **端到端复核**:在 main 上一次性输入 `a.example.com, b.example.com c.example.com`(逗号 + 空格混用,
  最后一段故意不回车的 `c.example.com`),保存后 `/api/openbox/profile` 里这条规则的 `domains` 是**三条**,
  说明分隔符与 `commit()` 都工作正常。
  **有意保留的差异**:这张卡不编辑上游那个「加密引导 DNS」(bootstrap)输入框——`DnsDomainUpstreamCard.vue`
  里的注释写明了原因(改备注时不希望把解析地址弄丢),属于有意差异,不是漏做。
- 2026-11-26 **弹窗层对比:设置 → 订阅管理 →「添加订阅」弹窗的三个页签(基础 / 登录 / 规则)**。
  **工具先修**:`dlg-sig.mjs` 之前一直报「没点到 添加订阅」,不是页面问题——`#/subscriptions` 不是规范路由
  (真正的是 `#/settings?tab=subscriptions`),而登录成功后应用还会自己 push 一次默认路由,固定 `sleep` 常常
  在跳转没完成时就 `goto` 别的 hash,结果停在登录页、后面所有点击都落空。现在改成:登录后轮询 `hash` 离开
  `#/login` → 导航后校验 hash、不对就重来 → 点击前先 `waitFor({state:'visible'})`;失败时打印当前 URL 和首屏
  文字,免得再猜。类名白名单同时纳入 `app-[\w-]+`(否则 `app-compact-action` 这类自定义类在签名里根本看不见)。
  **发现的差异与修法**:
  1. **基础页签**:「带宽定期检查」右侧那个说明按钮(`btn btn-ghost btn-circle btn-xs`)少了上游的
     `app-compact-action`。补之前先发现 `main.css` 里 `.app-compact-action` 只有选择器、**从来没定义过**
     (`.app-compact-action{...}` 与 `.app-compact-action > svg{...}` 都缺),已按上游口径补上
     (`gap:.25rem`;子 svg `flex-shrink:0;width/height:1rem`),否则光加类名不产生任何效果。
  2. **登录页签**:表单行间距 `gap-2` → 上游是 `gap-3`。
  3. **规则页签(`RenameRulesEditor.vue`,本批改动最大)**:重写成上游的版式——
     · 顶部两个开关改成 `label > span + input.toggle.toggle-primary.toggle-xs[role=switch]`(上游是「文字在左、
       开关在右」的 `label` 包裹),「恢复默认」按钮 `ml-auto shrink-0`;
     · 「序号位数」从裸 `input` 变成上游的 `join` 组:`button.btn.btn-sm.join-item.w-10`(− / +,
       aria 为 `numberDecrease` / `numberIncrease` 带 label)+ `label.input.input-sm.join-item.w-20` 里的
       `input.tabular-nums.text-center`,值 clamp 在 1~4;
     · 地区关键词从「一行逗号文本」换成公共 `TagInput`(一词一个牌子,回车/逗号新增、点牌子移除),
       行标签是「地区名 · 关键词字典」,删除按钮统一 `btn btn-ghost btn-square btn-sm shrink-0 hover:text-error`
       且图标是 `TrashIcon h-4 w-4`(原来错用 `XMarkIcon h-3.5`);
     · 特征关键词、过滤关键词同样换成 `TagInput`;
     · 布局:控制区 `flex flex-col gap-4 overflow-y-auto`,「地区关键词」标题块移进滚动区最上面,效果预览行
       挪到命名模板组末尾,滚动区 `min-h-0 flex-1 overflow-y-auto`。
  4. **默认词表**(面板 `rename-defaults.ts` 与后端 `internal/subs/rename.go` 两份):特征关键词从 6 个补到上游的
     **15 个**(`iepl iplc ipv6 专线 家宽 2x tud vm ALi AnGe zo org yy RN 0.5`),过滤关键词从 3 个补到上游的
     **14 个**(多出 `下载 失效 复制 客户端 如果 网站 剩余 套餐 重置 到期 流量`),顺序照抄(越靠前越优先命中)。
     这条会让**首次打开编辑器时的预填内容、以及没存过规则的订阅的节点命名 / 过滤结果**与上游一致,属于行为对齐,
     不只是文案;`go test ./internal/subs/...` 全绿(原有用例都显式传词表,不受默认值影响)。
  5. **`TagInput` 的一处重复类名**:组件内部已有 `w-full`,调用点又传了一次 `class="w-full"`,类名列表里出现两个
     `w-full`;已删掉调用点那两处(`RenameRulesEditor` 的特征/过滤两行),组件不再重复。
  **结果**:三个页签的结构签名(标签 + 类名 + 文案 + 控件属性)**逐行一致**——基础 **73 / 73**、
  登录 **77 / 77**、规则 **646 / 646**,「文案只在某一侧」的差异列表为空。溢出标签的隐藏方式也确认过了:
  上游是给超出一行的牌子加 `invisible`(所以它的 `innerText` 里没有尾部关键词),我们的 `TagInput` 同款实现,
  不要改成「靠 `overflow-hidden` 裁切」——裁切会让尾部文字仍被 `innerText` 读到,对比工具会报一堆假差异。
  `vue-tsc` / `eslint` 无报错。
- 2026-11-26 **弹窗层对比:设置 → 订阅管理 → 节点管理 →「添加分组」弹窗(静态组 / 动态组两屏)**。
  **先修对比环境**:8802 那个基准静态服务(`ob-up/serve-dist.mjs`)的 `BACKEND` 默认是
  **真机 `http://192.168.2.5:3036`**,而 5174 开发服务代理的是**本机 3036**——两边根本不是一个数据源,
  于是同一个弹窗左侧「可选」一边 7 条、一边 68 条,签名 diff 全被数据噪声淹没,还差点被当成「候选节点算错了」。
  现在基准服务用 `BACKEND=http://127.0.0.1:3036` 起,两侧同数据后差异立刻收敛到 11 行。
  **发现的差异与修法**(都在 `NodeGroupsPanel.vue` / `BulkPick.vue`):
  1. 「节点评分」开关行:上游是 `flex min-w-min flex-1 flex-wrap items-center gap-3`,且每个开关各包一层
     `div.flex.gap-1.items-center.shrink-0`;说明按钮是 `app-compact-action btn btn-circle btn-ghost btn-xs shrink-0`
     (main 原来是 `btn-sm`,32×32,比上游大一圈)。「展开」按钮同理去掉 `h-8`、补 `app-compact-action`(32 → 24 高)。
  2. 「动态组 / 静态组」页签:上游是 `<button type="button" role="tab">`,main 用的是 `<a role="tab">`
     (没有 `href` 的 `a` 键盘不可聚焦)——语义和可访问性都不对,已改;故障转移那条页签栏里的 `<a>` 一起改掉。
  3. 左右两栏的过滤框:上游外面包一层 `div.relative.min-w-0.flex-1`、输入框是 `input input-xs join-item w-full`
     (留给输入时的清除按钮定位),main 是裸的 `input flex-1`;已按上游包一层。
  4. 两栏的批量操作行(`BulkPick`):上游根节点是 `app-inline-gap flex items-center`(列间距 8px),
     main 是 `flex items-center gap-1`(4px);已改。
  5. 动态组「关键词」行:上游 `flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 text-xs` +
     `span.font-medium`;main 是 `gap-1` + `label`。右边「排除关键词」按钮上游用 `app-compact-action` +
     **右箭头**(`ChevronRightIcon`),main 用的是铅笔图标(`size-3.5`);已按上游换掉(图标 import 一起换)。
  6. 「关键词匹配方式」的 OR/AND 页签:上游 `tabs-box tabs tabs-xs w-fit`,main 少了 `w-fit`。
  7. 弹窗底栏:上游把「取消 / 保存」包在 `div.flex.justify-end.gap-2` 里,main 是直接两个按钮;已补容器。
  **结果**:静态组一屏 main 161 行 / 上游 168 行,动态组一屏 108 / 115——差异**只剩 7 行**,就是上游多出来的
  「业务优选」开关(例外 C:整体不做),连带的只有开关行宽度 331 vs 458;文案差异也只有 `业务优选` 三个字。
  逐行剔除该块后两侧结构签名**完全一致**。交互冒烟(打开弹窗 → 切动态组 → 切回静态组 → 勾一条右移 → 取消):
  「可选 (7)」正确变成「可选 (6)」,页签高亮跟随,无新增报错;`vue-tsc` / `eslint` 无报错。
