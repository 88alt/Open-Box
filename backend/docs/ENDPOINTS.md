# 前端调用的后端接口清单

由 `panel/src` 里的调用点自动提取(主要在 `api/openbox.ts`、`api/clientApp.ts`、`store/auth.ts`)。
这份清单是后端要实现的**接口契约**:路径、方法以这里为准;请求体 / 响应体的具体字段以对应的 TypeScript 类型(`api/openbox.ts` 里同名接口)为准。
提取是脚本做的,可能漏掉动态拼接的路径、把同一路径的多个方法合并,或把「URL 常量」误记成 GET(`/api/auth/*`、`/api/storage`、`/api/background-image` 已人工校正);发现遗漏时直接在这里补上,并在 `backend/STATUS.md` 对应位置记一笔。

共 103 个(方法, 路径)组合,89 个不同路径(`/api/auth/*`、`/api/storage`、`/api/background-image` 校正后组合数略有增加)。

## 鉴权与错误码约定(以 `store/auth.ts` 为准,已实现)

- 状态:首次运行(没设过密码)为 `{ enabled:false, authenticated:true, passwordSet:false }`;设置后为 `{ enabled:true, authenticated:<会话是否有效>, passwordSet:true }`。
- 没设密码时,除 `/api/auth/*`、`/api/health` 外的 `/api/*` 一律 `403 { "error":"PASSWORD_SETUP_REQUIRED" }`;已设密码但没登录:`401 { "code":"ACCESS_PASSWORD_REQUIRED" }`。
- 登录 / 修改密码时密码错误:`401 { "code":"ACCESS_PASSWORD_INVALID" }`;失败次数过多:`429`(带 `Retry-After`,`code: ACCESS_PASSWORD_LOCKED`)。
- 设置 / 修改密码的失败码放在 `error` 字段:`PASSWORD_TOO_SHORT`(400)、`PASSWORD_TOO_LONG`(400)、`PASSWORD_ALREADY_SET`(409)。
- 以上响应都带 `message`(中文,给人看)。其它接口的错误仍是 `{ "error": "<中文>" }`。

## `/api/auth/change-password`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/auth/change-password` | `changePassword` (store/auth.ts:341) | `{ ok, enabled, authenticated, passwordSet }` | `{ currentPassword, newPassword }` |

## `/api/auth/login`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/auth/login` | `loginWithAccessPassword` (store/auth.ts:241) | `{ ok, enabled, authenticated, passwordSet }` | `{ password }` |

## `/api/auth/logout`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/auth/logout` | `logoutAccessPassword` (store/auth.ts:278) | `{ ok }` | — |

## `/api/auth/setup`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/auth/setup` | `setupPassword` (store/auth.ts:305) | `{ ok, enabled, authenticated, passwordSet }` | `{ password }` |

## `/api/auth/status`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/auth/status` | `fetchServerAuthStatus` (store/auth.ts:216) | `{ enabled, authenticated, passwordSet }` | — |

## `/api/background-image`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/background-image` | `getBase64FromServer` (helper/indexeddb.ts:123) | `{ image: string }`(没有时为空串) | — |
| PUT | `/api/background-image` | `saveBase64ToServer` (helper/indexeddb.ts:109) | `{ ok }` | `{ image }`,必须是 `data:image/` 开头,上限 12 MiB |
| DELETE | `/api/background-image` | `deleteBase64FromServer` (helper/indexeddb.ts:138) | `{ ok }` | — |

## `/api/controller-ws/{x}`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/controller-ws/{x}` | `createWebSocket` (api/index.ts:257) | — | t |

实现约定(M3):前端对内核控制接口的约定共两个前缀,**接口表只收字面路径,所以 REST 那一半没列在表里**:
- `/api/controller/<路径>`:axios 的 `baseURL`(api/index.ts:13),代理、规则、连接、配置、提供者、延迟测试、重启 / 升级等几十个调用都走这里,后端原样转给内核的 Clash API(`GET /proxies`、`PUT /proxies/{组}`、`GET /proxies/{x}/delay`、`GET /connections`、`DELETE /connections/{id}`、`PATCH /configs`……),方法、路径(含转义)、查询、请求体和响应都不改;
- `/api/controller-ws/<名字>`:`connections` / `logs` / `memory` / `traffic` 的 WebSocket,握手和帧原样转发,查询参数(如 `level`)保留。
两个前缀都在登录保护之内;内核的 secret 由后端从正式配置里读出来加在发给内核的请求上,浏览器的 Cookie / Authorization / 自带的 `token` 参数不转;WebSocket 只接受同源;内核连不上返回 502 加 `{ message }`。

## `/api/openbox/activation`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| DELETE | `/api/openbox/activation` | `deactivate` (api/clientApp.ts:344) | OpenboxActivation | () |
| GET | `/api/openbox/activation` | `fetchActivation` (api/clientApp.ts:328) | OpenboxActivation | () |
| POST | `/api/openbox/activation` | `activate` (api/clientApp.ts:331) | OpenboxActivation | (code: string, left: string, right: string) |
| PUT | `/api/openbox/activation` | `updateActivationText` (api/clientApp.ts:338) | OpenboxActivation | (left: string, right: string) |

实现约定(M5,DESIGN D9):四个方法都回同一份固定状态 `{ activated: true, left: "", right: "", defaults: { left: "Open-Box <后端版本>", right: "sing-box <内核版本>" } }`,不保存任何东西(POST 的激活码随便给、PUT 的文字不存),不会出现「激活码不对」。内核版本读不到时右边只显示 `sing-box`。
另有 `GET /api/openbox/tutorial-videos`(前端没有调用,DESIGN 第 8 节规定):固定回 `{ "videos": [], "updatedAt": 0, "staleAfterMs": 86400000 }`。

## `/api/openbox/backup`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/backup/import?subscriptions={x}` | `importBackup` (api/openbox.ts:1563) | OpenboxBackupImportResult | (data: OpenboxBackup, subscriptionsMode: OpenboxBackupSubscriptionsMode = 'repla |

## `/api/openbox/backup?subscriptions={x}&chainProxies={x}&clientRoutes={x}&servers={x}`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/backup?subscriptions={x}&chainProxies={x}&clientRoutes={x}&servers={x}` | `fetchBackup` (api/openbox.ts:1560) | OpenboxBackup | (opts: OpenboxBackupOptions) |

## `/api/openbox/chain-proxies`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/chain-proxies/latency` | `testChainLatency` (api/openbox.ts:1775) | OpenboxChainLatencyResult | (payload: OpenboxChainLatencyPayload) |

> 实现约定(M1):`link` 解析不了、缺 `upstream`、上游不存在或成环返回 400。测速用 `internal/probe`(临时 sing-box + 本机 http 入站,后端自己计时);`testUrl` 不传用档案的 `testUrl`;`via` 是按配置静态推出的上游选中节点(selector 取 default,urltest 取第一个成员),内核接入后改读控制接口。找不到 sing-box 返回 503。`ipUrls`(可选,IP 地区):只认 https 的 `api.ip.sb`、`ipwho.is`、`api.ipapi.is` 三个主机(端口只能是 443、不能带账号),主机名转小写、去掉 `:443` 与片段后去重,最多 5 个;有不合规的地址返回 400,文案里带「ipUrls」(前端靠它丢掉 IP 信息后重测)。带了之后和测速同一次起内核、并行,按顺序请求到第一个答 2xx 的为止(3xx 不跟随、非 2xx、空正文都按失败),每次尝试各有一份超时(`timeoutMs`);响应多一个 `ip`:成功 `{ok:true,url,body}`(`url` 是答话的那一家,`body` 是正文原样、最多 16 KiB、已规整成合法 UTF-8),全部失败 `{ok:false,error}`(`error` 按「主机: 原因」逐家列出,用「;」隔开)。没带或带空数组则没有 `ip` 字段。线路不通时测速失败,`ip` 也如实失败,不会让整个请求报错。这一路和测速一样不校验对方证书(路由器上常常没有根证书包)。

## `/api/openbox/client-app`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/client-app/egress-country` | `detectEgressCountry` (api/clientApp.ts:156) | OpenboxEgressCountry | () |
| GET | `/api/openbox/client-app/info` | `fetchClientAppInfo` (api/clientApp.ts:152) | OpenboxClientAppInfo | () |
| POST | `/api/openbox/client-app/share-codes` | `fetchShareCodeSeals` (api/clientApp.ts:215) | Record<string, OpenboxShareCodeSeal> | (links: Record<string, string>) |
| POST | `/api/openbox/client-app/share-codes/{x}/rotate` | `rotateShareCode` (api/clientApp.ts:232) | OpenboxShareCodeRotated | (id: string) |

实现约定(M2,只列已做的 `info`):`GET /client-app/info` 返回 `OpenboxClientAppInfo` 全部字段。`routerId` 第一次读时生成(32 位小写十六进制)并保存,之后不变,不提供更换;`lanAddresses` / `lanSubnets` 只含私有地址(跳过回环 / 隧道 / 容器网桥),IPv4 在前、LAN 口在前,`lanSubnets` 只有 IPv4;`panelPort` 是本后端监听的端口;`shareRegions` 没存或存了空数组 = 默认三组且 `shareRegionsCustomized` 为 false,`defaultShareRegions` 恒为默认三组;`serverInfo` 原样,`routerName` = `serverInfo.name`(去首尾空白)或「Open-Box」;`egressCountry` 是上一次 `POST egress-country` 判出的 `{ip, country, at}`,没判过或缓存不合格时为 `null`。`share-codes*`、`client-config/file` 的封包格式前端没有规定,未做,见 STATUS.md。

`POST /client-app/egress-country`(M2):无请求体,返回 `{ip, country, at}`(`country` 两位大写国家代码,`at` 毫秒,是「最后确认的时刻」)。只在内核服务在运行时才判(复用 `dns/region-detect` 的三家 https IP 接口,顺序问、第一家给出合法 IP 和国家代码的为准,只查询不改设置);出口 IP 和上次相同就沿用上次的国家、只刷新 `at`(前端一天内不再重判,不刷新的话过一天每次打开页面都会重判),不同才换国家;结果存在 `client-app.json` 的 `egressCountry`。内核没在运行 / 没有 procd 或 systemd(面板不在路由器上)503,读不出服务状态 500,三家接口都问不到 502(带「主机: 原因」),请求被取消 / 超时 503。

## `/api/openbox/client-config`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/client-config` | `fetchClientConfig` (api/clientApp.ts:182) | OpenboxClientConfigIssue | () |
| PUT | `/api/openbox/client-config/enabled` | `setClientConfigEnabled` (api/clientApp.ts:189) | { enabled: boolean } | (enabled: boolean) |
| POST | `/api/openbox/client-config/file` | `fetchClientConfigFile` (api/clientApp.ts:199) | OpenboxClientBundle | (lan: string, publicBase: string) |
| POST | `/api/openbox/client-config/rotate` | `rotateClientConfig` (api/clientApp.ts:186) | OpenboxClientConfigIssue | () |

实现约定(M2,只列已做的三条;`file` 未做):状态存在 `client-app.json`(与路由器 ID 同一文件)。`GET` 第一次读时生成配对码(8 个字符,取自去掉 I / O / 0 / 1 的 32 字符表,大写)和 nonce(16 位小写十六进制)并保存,**同步开关默认关**;其余字段 `routerId` / `routerName` / `lanAddresses` / `panelPort` 与 `client-app/info` 同源。`PUT enabled` 请求体必须是 `{"enabled": true|false}`(其它一律 400,上限 1 KiB),返回 `{enabled}`,不动配对码。`POST rotate` 换配对码和 nonce、`lastSyncAt` 清零、开关保持,返回换完的整份。三条响应都带 `Cache-Control: no-store`,配对码不写日志、错误响应里不出现。`lastSyncAt` 要等 App 用的公开同步接口(未做)来更新,现在恒为 0。`file` 和 App 用的公开接口的封包格式前端没有规定,见 STATUS.md。

## `/api/openbox/clients`

实现约定(M2):只读。目前只有 DHCP 租约(dnsmasq 租约文件,只收 IPv4、不含已过期的)一路,按 IP 从小到大排;`name` 没有主机名时是空串,没有 MAC 时不带 `mac`;读不到租约回 `{ "clients": [] }`,不报错。前端说明里「今天流量里出现过的来源 IP」等 `traffic*` 做了再并进来。

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/clients` | `fetchKnownClients` (api/openbox.ts:1709) | { clients: Array<{ ip: string; name: string; mac?: string }> } | () |

## `/api/openbox/config`

实现约定(M1):每次现算,不落盘、不校验、不动系统;`config.experimental.clash_api.secret` 返回 `***`。核心报错(如「出站名称重复」)原文放在 `error` 里返回 400;核心不可用 503。

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/config/preview` | `fetchConfigPreview` (api/openbox.ts:949) | { config: OpenboxConfigPreview } | () |

## `/api/openbox/content-filter`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/content-filter` | `fetchContentFilter` (api/clientApp.ts:275) | OpenboxContentFilterList | () |
| POST | `/api/openbox/content-filter` | `saveContentFilter` (api/clientApp.ts:292) | OpenboxContentFilterList | (input: OpenboxContentFilterInput) |
| POST | `/api/openbox/content-filter/preview` | `previewContentFilter` (api/clientApp.ts:286) | { text: string } | (input: { dialect: OpenboxContentFilterDialect sourceUrl: string arguments?: Rec |
| POST | `/api/openbox/content-filter/restore-defaults` | `restoreContentFilterDefaults` (api/clientApp.ts:308) | OpenboxContentFilterList | () |
| DELETE | `/api/openbox/content-filter/{x}` | `deleteContentFilter` (api/clientApp.ts:304) | OpenboxContentFilterList | (id: string) |
| PATCH | `/api/openbox/content-filter/{x}` | `setContentFilterEnabled` (api/clientApp.ts:298) | OpenboxContentFilterList | (id: string, enabled: boolean) |
| GET | `/api/openbox/content-filter/{x}/source` | `fetchContentFilterSource` (api/clientApp.ts:278) | { text: string } | (id: string) |

## `/api/openbox/defaults`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/defaults/groups` | `fetchDefaultNodeGroups` (api/openbox.ts:1629) | { groups: OpenboxUserGroup[] } | () |
| GET | `/api/openbox/defaults/routing` | `fetchDefaultRouting` (api/openbox.ts:1633) | { routing: OpenboxProfileRouting } | () |

> 实现约定(M1):`defaults/groups` 由 openbox-core 的 `normalizeGroups` 补全,核心不可用时 `503`。默认值的取舍见 `docs/DESIGN.md` 第 8 节。

## `/api/openbox/deploy`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/deploy` | `deployNow` (api/openbox.ts:926) | — | () |
| GET | `/api/openbox/deploy/state` | `fetchDeployState` (api/openbox.ts:942) | { state: OpenboxDeployState } | () |

实现约定(M5):`POST /deploy` 做一次完整部署(生成配置 → 落盘本地规则集 → `sing-box check` → 备份并原子替换 → 重启 / 启动 → 健康检查 → 失败回滚,详见 `internal/singbox/deploy.go`),同一时刻只允许一次。响应体恒为 `{ok, stage, message, badTags}`(回滚过另带 `rolledBack: true`),`stage` 取 `running` / `conflict` / `validate` / `start` / `verify` / `error`;状态码:成功 200,`conflict`(内核没在运行、而别的代理服务正在运行,所以没启动)和「已有部署在进行」409,`validate` 422,`start` / `verify` 502,其它 500——前端 `deployNow` 不看状态码,只读响应体。`GET /deploy/state` 回 `{state: {stage, message, at, badTags}}`,是最近一次部署的结果(毫秒时间戳 `at`),存在数据目录的 `deploy-state.json`、重启后仍在,从没部署过是 `stage:"idle"`、`at:0`;「已有部署在进行」的拒绝不记。面板的「部署」和 DNS 过滤的应用都会记。**DNS 接管**(档案 `dns.mode = dnsmasq` 时 dnsmasq 把查询转给内核,见 `internal/system/dnstakeover.go`):校验通过后、替换配置并重启内核**之前**先撤销接管(内核重启的几秒里 dnsmasq 不能还指着它),撤销不成功就不动手(`stage:"error"`,配置和内核都没碰);内核健康之后(含回滚后旧配置又健康)按 `dns.mode` 对齐,等内核的 DNS 入站应答最多 20 秒。对齐的结果附在 `message` 后面(例如「dnsmasq 已改为把查询转发给内核」),对齐失败**不改变** `ok`——内核是好的,接管那边出错时已自动恢复原状,`message` 里说明「DNS 保持原样」。校验没过的部署不会碰接管。

## `/api/openbox/diagnostics`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/diagnostics` | `fetchDiagnostics` (api/openbox.ts:1556) | Record<string, unknown> | () |

实现约定(M5):响应是一份自由格式的 JSON(前端只是把它存成文件),顶层有 `generatedAt`、`redacted`、`note`、`backend`、`firmware`、`kernel`、`services`、`config`、`profile`、`logs`。只读;每块独立,读不到只在那一块写 `error`。`config` 是正在使用的内核配置(脱敏后的 `content`),不是合法 JSON 就不放内容;`profile` 是脱敏后的档案;`logs.core` / `logs.panel` 是两个服务最近 200 行日志(数组,或 `{error}`)。凭据、节点地址、订阅链接、SNI、传输路径都会被抹掉,脱敏规则见 `internal/diagnostics/redact.go`;日志里可能留有访问过的域名,所以包里提醒用户发出去之前自己再看一遍。

## `/api/openbox/dns`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/dns/flush-cache` | `flushDnsCache` (api/openbox.ts:1625) | OpenboxDnsFlushResult | () |
| GET | `/api/openbox/dns/region-detect` | `fetchDnsRegionState` (api/openbox.ts:1581) | OpenboxDnsRegionState | () |
| POST | `/api/openbox/dns/region-detect` | `detectDnsRegion` (api/openbox.ts:1589) | OpenboxDnsRegionDetect | () |
| POST | `/api/openbox/dns/upstream-test` | `testDnsUpstream` (api/openbox.ts:1616) | OpenboxDnsUpstreamTestResult | (payload: OpenboxDnsUpstreamTestPayload) |
| GET | `/api/openbox/dns/wan` | `fetchWanDns` (api/openbox.ts:1572) | { servers?: string[] } | () |

> 实现约定(M2):`dns/wan` 只读,回 `{ "servers": string[] }`(读不到是空数组,不报错);服务器取自 netifd / systemd-resolved 记录的上游文件,不读指向本机的 `/etc/resolv.conf`(顺序见 `internal/system/wandns.go`)。

> 实现约定(M2):`dns/region-detect` 的 GET 回 `{ pending, region }`(`pending` 只在一次判断进行中为真,`region` 取档案 `dns.region`);POST 按出口公网 IP 判一次,回 `{ region: "cn" | "intl" | null, ip, error? }`,判不出来仍是 200、`region` 为 null;只查不写档案,不在启动时自动判。

> 实现约定(M2):`dns/flush-cache` 只接受 POST,回 `{ kernel, dnsmasq, resolved }`(每层是否清成功,各层独立);至少一层成功是 200,全都没清掉是 503 加 `error`。内核层走 sing-box 的 `POST /cache/dns/flush`,dnsmasq 层发 SIGHUP,resolved 层 `resolvectl flush-caches`;只让缓存失效,不改配置、不重启。

> 实现约定(M2):`dns/upstream-test` 只接受 POST,请求 `{ side: "direct" | "proxy", server, protocol, port? }`:`server` 只收 IPv4 / IPv6 或 `wan`(不收域名),`protocol` 只有 `udp` / `tcp`(缺省:直连 udp、代理 tcp),`port` 缺省 53;不合法回 400 加 `error`,没有 sing-box 或核心不可用回 503。测了但没通是 200、`ok: false` 加 `error`(超时、上游回错误码、内核起不来都在里面)。做法:起临时 sing-box,只开 127.0.0.1 的 direct 入站并把进来的 DNS 查询劫持给内核自己的 DNS 模块,内核 DNS 里只有被测的这一台,后端发一个 A 查询并计时,所以测的是内核按所选协议、经所选线路真的去问。`wan` 固定直连、固定 UDP 53,测系统上游 DNS 的第一台(读不到按地区退回 223.5.5.5 / 1.1.1.1,并写进 `note`),`wan` 为真。代理侧经生成配置的兜底出站(`route.final`)去连,`policy` 是这个出口的名字,`via` / `chain` 是它当前默认选中的节点和逐跳线路(链首在前);出口是自动选择组时内核按延迟挑,`via` 只能写组名并在 `note` 里说明;兜底出口是直连时 `via` 为空。测试域名取档案:代理侧 `testUrl` 的主机、直连侧 `directTestUrl` 的主机。`warning` 暂不出现:解析之后再请求测速地址那一步没有做。

## `/api/openbox/dns-filter`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/dns-filter` | `fetchDnsFilter` (api/openbox.ts:168) | DnsFilterStatus | () |
| PUT | `/api/openbox/dns-filter` | `saveDnsFilter` (api/openbox.ts:169) | — | (settings: DnsFilterSettings) |
| POST | `/api/openbox/dns-filter/apply` | `applyDnsFilter` (api/openbox.ts:170) | DnsFilterStatus | (update = false, listId = '') |
| GET | `/api/openbox/dns-filter/preview?{x})}` | `fetchDnsFilterPreview` (api/openbox.ts:173) | DnsFilterPreview | (url: string, search: string, page: number, pageSize = 20, action: DnsFilterPrev |
| GET | `/api/openbox/dns-filter/records?{x})}` | `fetchDnsFilterRecords` (api/openbox.ts:172) | { rows: DnsFilterRecord[]; total: number; page: number; pageSize: number } | (search: string, result: string, page: number, pageSize = 20) |
| GET | `/api/openbox/dns-filter/summary` | `fetchDnsFilterSummary` (api/openbox.ts:171) | DnsFilterSummary | () |

实现约定(M2):`apply` 请求体可空,`{ update?: bool, listId?: string }`:`update` 为真重新下载启用的名单(带 `listId` 只重下那一份),否则只下载没有缓存的;之后编成规则集并走一次完整部署,成功返回新的状态。关闭过滤时也走 `apply`(把规则撤下来),不下载名单。错误码:409 = 已有应用在进行,或内核没在运行(名单已更新保存但没应用,不会替用户启动内核);400 = 没有这份名单 / 一份能用的名单都没有;500 = 部署失败(`error` 里写着回滚情况);503 = 没接应用服务。GET 返回的 `lists[id].error` 是上次下载 / 解析失败的原因(失败时沿用上次的缓存),`pending` 表示设置与已应用的不一致。

实现约定(M2,`records` / `summary`):数据来自 dnsmasq 的查询日志文件(启动参数 `-dns-query-log` / 环境变量 `OPENBOX_DNS_QUERY_LOG`,默认空 = 不读;后端只读这个文件,不改 dnsmasq 配置)。没有日志来源时两个接口照常回 200:`records` 是空页,`summary` 的各项数字为 0、`averageMs` 为 `null`、`hourly` 仍是 24 项、`connected` 为假;`DnsFilterStatus.connected` 同样取自日志是否在读。`records`:新的在前,`result` 为空是全部、`dnsmasq` 是 dnsmasq 自己答的(`cached` + `local`)、其余按 `blocked` / `allowed` / `error` / `unknown` / `cached` / `local` 精确匹配,`search` 在域名、来源地址、名单名里找子串(不分大小写),每页最多 100 条(默认 20),页码超出时收回到最后一页;`via` 恒为 `dnsmasq`,`elapsed` 只有上游应答的才有(毫秒,按后端读到 query 行和 reply 行的时间差算,没有就是 `null`),`list` 只有 `blocked` 的有。`blocked` 的判断:dnsmasq 日志里被拦的域名和真不存在的域名一样是 NXDOMAIN,所以应答是 NXDOMAIN 时再拿域名对一遍已应用的名单(规则与 core 的 `dnsFilter` 一致),对上才标 `blocked`;过滤没启用、没应用过、读不到名单缓存时一律不标。`summary`:最近 24 个整点小时(含当前这一小时,旧的在前),`hourly[].hour` 是该小时开始的毫秒时间戳,`elapsed` 是耗时总和(毫秒)、`timed` 是有耗时的条数,`topDomains` 取前 10。记录在内存里最近 2000 条,统计保留 48 小时,每 30 秒有变化就落盘到数据目录的 `dns-query-log.json`。

## `/api/openbox/factory-reset`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/factory-reset` | `factoryReset` (api/openbox.ts:1645) | OpenboxFactoryResetResult | () |

## `/api/openbox/failover`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/failover/recheck` | `recheckFailover` (api/openbox.ts:815) | { ok: boolean } | (id: string, signal?: AbortSignal) |
| POST | `/api/openbox/failover/refresh` | `refreshFailover` (api/openbox.ts:811) | { ok: boolean } | () |
| GET | `/api/openbox/failover/status` | `fetchFailoverStatus` (api/openbox.ts:809) | OpenboxFailoverStatus | (signal?: AbortSignal) |

实现约定(M4,第一部分):部署成功后后端把故障转移计划存成配置目录里的 `failover.json`,`GET /status` 用这份计划加内核此刻的选择(`GET /proxies`)算出每个组的 `status`(主用 `ok` / 备用 `backup` / 拒绝 `reject` / 读不到或对不上 `unknown`)、`currentLaneId`、`kernelNow` 和各页签的状态;检测管理器还没做,所以 `running` 恒为 `false`、页签 `health` 恒为 `unknown`、节点检测结果为 `null`、`lastSwitch` 等为 `null`。`POST /refresh` 与 `POST /recheck` 现在回 501(`recheck` 对不存在的组先回 404)。

## `/api/openbox/groups`

实现约定(M1):组存 `groups.json`(经 core `normalizeGroups` 补全,含内置直连 / 拒绝),没存过时 GET 返回默认节点组(不落盘)。PUT 整份覆盖:名字留空由核心补「分组-N」;组与组重名、与节点(含启用的链式代理)重名返回 400;按 id 对比旧档,改名的组在 `renamed` 里返回,其它组(含故障转移页签)对它的引用自动迁移;`dangling` 是既不是节点也不是组的成员;`dropped` 是核心 `emitGroups` 判定要丢弃的组(成环,`reason: "cycle"`)。`availableNodes` = 已启用订阅的节点(带订阅名 / id、改名识别出的地区名)+ 启用的链式代理(`chain: true`、`upstream`);`subscriptions` 含停用的订阅;`availableGroups` = 已启用的组名(含内置直连 / 拒绝)。核心不可用返回 503。

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/groups` | `fetchNodeGroups` (api/openbox.ts:844) | OpenboxGroupsPayload | () |
| PUT | `/api/openbox/groups` | `saveNodeGroups` (api/openbox.ts:896) | — | ( groups: OpenboxUserGroup[], ) |

## `/api/openbox/kernel`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/kernel/version` | `fetchKernelVersion` (api/openbox.ts:1034) | OpenboxKernelVersion | () |

## `/api/openbox/latency-history`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/latency-history` | `fetchLatencyHistory` (api/openbox.ts:1404) | { history: OpenboxLatencyHistory; updatedAt: number } | () |
| POST | `/api/openbox/latency-history/samples` | `postLatencySamples` (api/openbox.ts:1409) | { ok: boolean; history: OpenboxLatencyHistory } | (samples: Array<{ name: string; time: string; delay: number }>) |
| POST | `/api/openbox/latency-history/sync` | `syncLatencyHistory` (api/openbox.ts:1411) | { history: OpenboxLatencyHistory } | () |
| GET | `/api/openbox/latency-history/version` | `fetchLatencyHistoryVersion` (api/openbox.ts:1406) | { updatedAt: number } | () |

实现约定(M3):样本 `{time, delay, node?}` 的 `time` 是 RFC3339、`delay` 是毫秒(0 = 不通),节点组的样本带 `node`(当时选中的节点),节点自己的没有;每个名字只留最新 10 条(旧 → 新),同一时刻只留一条,最多记 5000 个名字。`samples` 一次最多 500 条,名字不能为空或超过 200 个字符、时间要合法、延迟 0 – 600000,**任何一条不合法整批 400、什么都不记**;请求体上限 256 KiB(413)。`sync` 读一次内核的 Clash API `GET /proxies`,把各代理的测速记录合进来(`delay` 为 0 的不记);内核读不到不是错误,照样回现有的历史。`updatedAt`(毫秒)只在内容真有变化时才更新,没变化也不写盘。

实现约定(M4,定时采集):后端每 30 秒自己读一次内核,把自动择优组测出的新结果合进历史(与 `POST /sync` 是同一段逻辑),浏览器没开着也不丢样本;内核没起来时不记、不报错;没有新结果时不写盘、`updatedAt` 不变。

## `/api/openbox/node-info`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/node-info/{x}` | `fetchNodeInfo` (api/openbox.ts:1873) | OpenboxNodeInfo | (tag: string) |

实现约定(M3,仅 `GET node-info/{x}`):`x` 是节点的 tag(需 URL 编码);先在已启用订阅的节点里找,再找档案里启用的链式代理(名字就是 tag),都没有 404。`protocol` 是从外到内的协议层(类型、传输类型、`tls`、`reality`),`port` 为 0 时是 null,链式代理带 `detour`(= 它的 `upstream`)。入口地址:服务器是 IP 就直接用,是域名就用系统解析器解析(超时 5 秒),v4 / v6 分开并排序,sing-box 的 fake-ip 段(198.18.0.0/15)不算真实地址;解析不了是 200 加 `resolveError`、地址为空数组。`entryCountry` 只带缓存里已有的(查过 entry-country 之后才有),没有就是空串。

实现约定(M3,`GET node-info/{x}/entry-country`):取这个节点入口地址里第一个公网 IPv4(已排序,没有再取第一个公网 IPv6;内网、回环、链路本地、CGNAT、fake-ip 段都不查,也不会发给第三方),按顺序问 api.ip.sb、ipwho.is、api.ipapi.is 三家的「按 IP 查询」接口(只认答的是同一个 IP 的),回 `{ip, country}`,`country` 是两位大写国家代码。成功缓存一天,失败缓存一分钟(调用方取消不缓存),最多 512 个 IP。节点没有 404;没有可查的公网入口地址(解析不了、全是内网 / 假 IP)或三家都查不到是 502 加中文原因(前端遇到错误只是不显示国旗)。
| GET | `/api/openbox/node-info/{x}/entry-country` | `fetchNodeEntryCountry` (api/openbox.ts:1874) | { ip: string; country: string } | (tag: string) |
| POST | `/api/openbox/node-info/{x}/exit` | `fetchNodeExit` (api/openbox.ts:1877) | OpenboxNodeExit | (tag: string, refresh = false) |

实现约定(M3,`POST node-info/{x}/exit`):请求体可以没有,`{"refresh":true}` 跳过缓存重查,但 15 秒内刚查过的仍按缓存答;缓存 6 小时(按节点 tag,最多 256 个),响应带 `cached:true` 表示来自缓存。先经这个节点(起临时 sing-box,和节点测速同一套)问 api.ip.sb / ipwho.is / api.ipapi.is,得到出口 IP 和国家(`country`);再由本进程直接(不经节点)并行问 proxycheck.io 的 v3 接口(`network.type` → `ipType`:residential / mobile(Wireless)/ business / hosting;`detections` 里的 TOR / VPN / Proxy / Scraper / Compromised → `proxyType`;`detections.risk` → `risk`;`device_estimate.subnet` → `devices`;`network.asn` / `organisation` → `asn` / `asnOwner`;`location` 的城市、地区、国家 → `location`,用「, 」连接)和 RDAP(rdap.org 跳转到各大洲的 RIR,只认顶层两位 `country` → `registeredCountry`)。`native` = 登记国家和 `country` 一致,任何一边不知道是 null。两路各自成败:没查到的那一路在 `errors`(`proxycheck` / `rdap`)里写原因,对应项为空串或 null,另一路照常给;出口不是公网地址时不问第三方,两路都记「出口不是公网地址,不查归属」。`ipv6` 只在出口本身是 IPv6 时有值(另一个协议族的出口地址不单独查)。节点在当前配置里没有可探测的出站(没启用、是节点组)404;openbox-core 或 sing-box 不可用 503;经节点连不上或三家接口都没答 502;其余 500。同时最多 2 个经节点的探测,同一个节点的并发请求合并成一次。

## `/api/openbox/nodes`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/nodes/latency` | `testNodeLatency` (api/openbox.ts:666) | { results: OpenboxLatencyResult[] } | (payload: { url?: string urls?: string[] content?: string userAgent?: string log |
| POST | `/api/openbox/nodes/latency` | `testStoredLatency` (api/openbox.ts:1793) | OpenboxStoredLatencyResponse | (jobs: OpenboxStoredLatencyJob[], timeoutMs?: number) |
| POST | `/api/openbox/nodes/latency` | `testStoredLatencyStream` (api/openbox.ts:1805) | — | ( jobs: OpenboxStoredLatencyJob[], onDone: (indices: number[]) |

实现约定(M3,延迟历史):已保存节点的测速(请求体带 `jobs`,含 `progress` 的 NDJSON)测完后按每个 job 的 `record`(不传按默认记,`false` 只测不记)写进延迟历史,响应(NDJSON 是最后一行)带回**整份** `history`;全都不记时不带 `history`。通了记耗时(至少 1),超时 / 被拒 / 失败记 0,`invalid` / `not-found`(根本没测)不记;预览(带 `url` / `urls` / `content` / `loginId`)和链式代理测速不记。记历史失败不影响测速本身。节点组经临时实例测出的样本不带 `node`。


> 实现约定(M1):请求体有 `url` / `urls` / `content` / `loginId` 之一 = 预览(拉取、解析、改名后按 `tags` 测,不落盘,测速地址取档案 `testUrl`;`loginId` 返回 501),否则按 `jobs` 测已保存节点(`url` 缺省取档案 `testUrl`)。`results` 与 `tags` / `jobs` 一一对应。`reason`:`not-found`(是组或已不在订阅里)、`invalid`(地址不合法或 sing-box 起不来,`error` 带原因)、`timeout`、`failed`。`timeoutMs` 默认 5000,上限 30000;一次最多 500 个,同时测 16 个。`progress:true`(仅 jobs)回 NDJSON:每测完一个 `{"done":[下标]}`,最后 `{"results":[...]}`,出错 `{"error":"..."}`。找不到 sing-box 返回 503。**未做**:延迟历史(`record` 忽略,响应没有 `history`)。

## `/api/openbox/penetration`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/penetration` | `queryPenetration` (api/openbox.ts:1174) | OpenboxPenetrationResult | (target: string) |

> 实现约定(M3,第一部分):请求 `{"target":"域名或 IP"}`,目标不合法 400,读不到已部署配置 500。按已部署 `config.json` 的 `route.rules` 推算(`internal/routedecide`)。已给:`matched`(`index` / `rule` / `outbound` / `action`)、`owner`(只认得出的)、`assumed`(`needs` / `leaf` / `sameOutcome`)、`preResolve`、`dns`、`chain`(永远是数组,reject 时为空)、`finalOutbound`(拿不到为 null)、`chainError`、`matchError`(规则集没能检查:`matched` 为 null、不给线路)、`routingStale`。`matched.entries`(最多 20 条,来源是规则集名或 `custom`)/ `entriesTotal` 已做。`resolved` 和 `matched.viaIp` 已做:域名目标先经内核的 DNS 入站解析(档案开了 IPv6 才再问 AAAA),响应里 `resolved` 是 `{fakeIp?, addresses, error?}`(`addresses` 永远是数组;解析失败不算接口错误,原因写在 `error`;IP 目标不解析、没有这个字段)。推算时把「第一个真实 IPv4(没有就第一个 IPv6)」当终端会连的目的 IP:终端自己解析后按真实地址连进内核、内核再嗅探出域名,连接同时带域名和目的 IP,所以 `ip_is_private`、`ip_cidr`、`ip_version` 和规则集里的 IP 条目按这个地址判;core 给「看 IP 的规则」带了「连接没有域名」的前提(`domain_regex` 取反),有域名的连接不会被它们命中,这一点推算里照样成立。靠地址命中的规则,`matched.viaIp` 是那个地址,`entries` 也把规则集里命中它的 IP 条目列出来。解析到的是占位地址(`fakeIp: true`,连接进内核后按域名找回目标)或解析失败时没有目的 IP,IP 条件仍记成 `assumed` 的前提(与原来一致)。**未做**:`preMatch`、`firstLayer`、前置自定义分流的 `ownerName`。

## `/api/openbox/policies`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/policies/entries?{x}` | `fetchPolicyEntries` (api/openbox.ts:1214) | OpenboxPolicyEntries | (params: { name: string tab?: 'all' \| 'domain' \| 'ip' q?: string sort?: 'type' |

> 实现约定(M1):`name` 是站点集名(必填,缺少 400,没有这个站点集 404)。取自档案的路由,先交给核心 `normalizeRouting` 规范化。`name` 等于兜底站点集名时返回 `fallback:true` 和空条目(兜底命中其余所有目标);等于前置自定义分流名时展开它的每一行(含端口规则,`family:"other"`);否则展开该站点集的手写条件(来源 `custom`)和规则集链接(来源是链接的「主机 + 路径」,不带账号、查询串和片段)。条目 `type` 为 `domain` / `domain_suffix` / `domain_keyword` / `domain_regex` / `ip_cidr`(和 `rulesets/preview` 一致)或 `port`;同类型、同内容、同来源去重,总量最多 30 万条。`tab` 取 `all` / `domain` / `ip`(其它 400);`counts` 是各档总条数(不受 `tab` 和 `q` 影响,`other` 族只算进 `all`),`total` 是当前档的条数,`matched` 是当前档里命中 `q` 的条数;`q` 不区分大小写,匹配内容和来源;`sort` 取 `type` / `content` / `source`(其它 400,缺省保持原顺序),`dir` 取 `asc` / `desc`;`limit` 默认 50、最大 500。规则库分类(`geosite-*` / `geoip-*`,含前置自定义分流里的 `geosite` / `geoip` / `ruleset` 行)按 `rulesets/entries` 同一套随包规则库展开,条目来源写分类名(例如 `geosite-cn`),搜索词也能匹配来源;`missing` 列出展开不了的来源:规则库里没有的分类(或名字不合法、没有 `sing-box`、解码失败)、拉取失败的规则集链接(只写「主机 + 路径」)、没有链接可展开的 `list-*`、被截断的分类(带「条目太多,只统计了前 30 万条」的提示);单个来源失败不会让整个请求失败。没有 openbox-core 时返回 503。

## `/api/openbox/profile`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/profile` | `fetchProfile` (api/openbox.ts:516) | { profile: OpenboxProfile } | () |
| PUT | `/api/openbox/profile` | `saveProfile` (api/openbox.ts:534) | { profile: OpenboxProfile } | (patch: Record<string, unknown>) |
| GET | `/api/openbox/profile/defaults?region=cn` | `fetchDnsRewriteDefaults` (api/openbox.ts:522) | { dnsRewriteDefaults?: OpenboxDnsRewriteRule[] } | () |
| GET | `/api/openbox/profile/defaults?region={x}` | `fetchProfileDefaults` (api/openbox.ts:528) | { defaults: OpenboxProfileDefaults } | (region: string) |

> 实现约定(M1):GET 返回「默认值 + 已保存内容」的完整档案。PUT 的请求体是**补丁**,深度合并(对象递归合并,数组和标量整体替换,`null` 删除该键,下次读取由默认值补上),合并后整体校验,失败 `400 { error }` 且不落盘;`rulesetDir` 由服务端决定,客户端传的忽略;`chainProxies[].node` 只读(link 没变沿用旧摘要)。请求体上限 1 MiB。未知键原样保留。
>
> **`shareRegions` / `serverInfo` 是例外**(客户端 App 页的地区分流与路由器标识,规则见 `internal/clientapp/regions_check.go`):补丁里带了才校验,校验不过 `400 { error }`,**原因是英文**(前端 `saveShareRegions` 的注释写明原样显示,例如 `region CN is in both cn and x`),整个补丁作废、不落盘;档案里已存着的旧数据不会拦住不带这两个键的保存。`shareRegions` 是整份覆盖的数组:`[]` = 恢复默认三组(存成空数组),`null` = 删键;最多 12 组、每组最多 200 条规则;`id` 形如 `^[a-z0-9_-]{1,32}$` 且不重复;名称必填且 ≤ 30 字、说明 ≤ 120 字;地区是两位大写国家码(组内重复静默去重);`regionMatch` 取 `inside`(缺省)/ `outside`,`outside` 至少列一个地区;「在这些地区」的组之间地区不可重复(`outside` 组不参与);至多一个 `default` 组;`catchAll` 与每条规则的 `action` 取 `direct` / `proxy`;规则值按类型校验(`geosite` / `geoip` 为 `^[a-z0-9!@._-]{1,80}$`,`ipcidr` 为 IP 或 CIDR,`ruleUrl` 为 http(s) 且 ≤ 2048 字符,其余为 `^[A-Za-z0-9*_.-]{1,253}$`);DNS 上游只收 IP 或 `wan`、备用每侧 ≤ 3 条,协议只收 `udp` / `tcp`,端口 1–65535,`wan` 固定 UDP 53;没给的 DNS 字段按地区补默认值(含中国大陆:代理侧 TCP 1.1.1.1,其余系统 DNS)。校验通过后**保存规整后的值**(字符串去首尾空白、组里不认识的键丢掉、缺的 `dns` 补全),所以 PUT 返回和之后 GET 读到的是规整后的样子;默认三组原样存回不变。`serverInfo` 在这里是**整份替换**而不是深度合并(没给的字段就是没有,`{}` = 全用默认,`null` = 删键):`name` ≤ 40 字,`icon` ≤ 64 字符且无控制字符,`iconSvg` 以 `<svg` 开头且 ≤ 64 KB,`region` 为两位大写国家码;空串等同没给;不认识的键保留。`iconSvg` 的内容只校验开头和大小,不做净化。

> **`servers` 也是例外**(「共享网络」的服务器,规则见 `internal/shareservers`):补丁里带了才校验(没带不碰已存的旧数据),校验不过 `400 { error }`,**原因是中文**并指出第几台(例如 `第 2 台服务器「SS」: password 不能为空`),整个补丁作废、不落盘;`servers` 是整份数组,合并时整体替换(`[]` = 清空,`null` = 删键)。规则:最多 64 台;每台 `id` 为 1–64 位字母 / 数字 / `_` / `-` 且不重复,`name` 去空白后非空且 ≤ 80 字,`protocol` 取 `shadowsocks` / `vless` / `tuic` / `hysteria2` / `mixed`,`port` 是 1–65535 的整数且各台不重复(不论启用与否,与前端 `usedPorts` 一致),`enabled` 若给了必须是布尔;`address` 可选,只收单独的域名 / IP(IPv6 可带方括号,不带端口、路径、账号、空白),`outbound` 可选且 ≤ 128 字符,空串等同没给(去掉键);各协议:`shadowsocks` 的 `method` 取 `aes-128-gcm` / `aes-192-gcm` / `aes-256-gcm` / `chacha20-ietf-poly1305` / `xchacha20-ietf-poly1305` / `2022-blake3-aes-128-gcm` / `2022-blake3-aes-256-gcm` / `2022-blake3-chacha20-poly1305`、`password` 非空,2022 系列的密码必须是对应长度(16 / 32 字节)密钥的标准 base64;`vless` 与 `tuic` 的 `uuid` 必须是 8-4-4-4-12 或 32 位十六进制,`vless` 的 `tls` 若给了必须是布尔;`tuic` 与 `hysteria2` 的 `password` 非空,`hysteria2` 的 `obfs` 可选;`mixed` 的 `username` 与 `password` 要么都填要么都不填。字符串去首尾空白后保存,不认识的键原样保留;**不**检查 `outbound` 指的节点 / 组是否存在,也**不**检查端口是否被面板、内核或别的服务占用(那是 `servers/port-check` 的事,前端保存前会先调它)。
>
> 实现约定(M1):`region` 取 `cn` / `intl`(缺省 `cn`,其它 400)。响应同时带 `defaults` 和 `dnsRewriteDefaults`(两个前端函数共用同一路径)。

## `/api/openbox/route-test`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/route-test` | `testRoute` (api/openbox.ts:1295) | OpenboxRouteTest | (target: string, port?: number, method: OpenboxProbeMethod = 'GET') |

实现约定(M3,`POST route-test` 的**真实访问部分**;`dns` 判定和还没做的字段见下面):请求体 `{target, port?, method?}`;`target` 只认字母 / 数字 / `. _ : -` 且不以 `-` 开头(与前端 `isValidPenetrationTarget` 一致,≤ 253 字符),`port` 缺省或 1–65535,`method` 缺省 `GET`,取 `GET` / `HEAD` / `TCP` / `TLS`(大小写不分);不合法回 400 + 中文原因。探测前先让内核丢掉 DNS 缓存(只是让缓存失效,清不掉也照常测),再经内核的面板入站(127.0.0.1:7891)向 `CONNECT` 建一条隧道,连接开着时读一次 `/connections`,按「来源 127.0.0.1 + 本地端口」认出自己那条。`exit.url`:没指定端口按 `https://<目标>/`,端口 80 用 `http`,其它端口一律 `https`(IPv6 字面量带方括号);`GET` / `HEAD` 不跟随跳转,拿到什么状态码报什么(`ok:true` + `status`;`cf-mitigated: challenge` 响应头标成 `challenge:"cloudflare"`,请求本身算通);`TLS` 是建隧道加握手(`ms` 含两者,没有 `status`;目标是 IP 时不校验证书,域名照常校验);失败时 `ok:false` + 中文 `error`(超时 / 证书校验失败 / 连接被中断 / 内核没在运行等,不带网址)。认出连接后给 `chains`(路由选中的站点集 / 组在前、实际出口在后,与 Clash API 的顺序相反)、`rule` / `rulePayload`(内核报的命中规则)、`destinationIP`、`viaProxy`(出口不是 direct 出站);连接表读不到只写 `connectionsError`、探测本身仍算成功,表里找不到自己那条写 `notSeen:true`。整次探测超时 8 秒。

**`dns` 判定**(`internal/dnsdecide`、`internal/routetest/dns.go`):IP 目标回 `{skipped:true}`;域名目标读**已部署的 `config.json`**,按它 `dns.rules` 的顺序预测这个域名的 A 查询命中哪条规则,没命中走 `dns.final`(没写 final 用第一台服务器)。能判断的条件:`domain` / `domain_suffix` / `domain_keyword` / `domain_regex`(同一条规则里这四个是同一组,任意一个命中即可;后缀 `example.com` 匹配它自己和子域,`.example.com` 只匹配子域)、`rule_set`(读 `route.rule_set` 登记的本地规则集文件:source 直接读、binary 靠 `sing-box rule-set decompile`,inline 直接用;remote 不联网取)、`query_type`、`ip_version`,以及 `logical`(and / or)和 `invert`;一条规则里不同条件是「且」,同一条件的多个值是「或」。响应:`ruleIndex`(没命中规则是 `null`,键一直有)、`rejected`(`reject` 动作,或 `predefined` 回非 NOERROR 且没给答案)、`server`(`tag` / `type` / `server` / `detour`)、`viaProxy`(服务器有 `detour` 且它不是 `direct` 出站)、`fakeIpRule`(命中了 fakeip 类型服务器的规则:A / AAAA 拿占位地址,`server` / `ruleIndex` 是继续往下找到的真解析器)、`runtimeChain` / `runtimeLeaf`(服务器走代理时,读内核 `/proxies` 沿「当前选中」从 `detour` 走到末尾,最多 8 层;末尾是直连出站时 `viaProxy` 改为 false;内核读不到就不带这两个字段)。**判断不了的不猜**:一条规则其余条件都满足、只差判断不了的条件时,记进 `assumed`(`needs` 写缺什么,如 `source_ip_cidr` / `clash_mode` / `rule_set:<tag>` / `action:<不认识的动作>`,按名字排序;`sourceIpCidr` / `sourceMac` 是规则里写的来源;`sameOutcome` 表示它的服务器 / 动作和最终预测相同、对这次查询没有影响),预测时按「没命中」往下走。规则集读不出来(文件没有、解码失败、没有 sing-box)、或含有只看域名判断不了的规则(带 IP / 端口等条件、逻辑规则、取反规则,例如 `geoip-*`)而又没有别的规则命中时,同样记成前提;已部署的配置读不到 / 不是 JSON / 没有 `dns` 段时回 `{error}`。

**还没做、如实标出的**:`dns.stale`(前端没写明含义)、`context`(只在带终端来源时出现,留给 `terminal-test`)、`debug` 没有。`resolve` 已做,见 STATUS 的 route-test 的 resolve 条;`exit.noDomain` 已做,见 STATUS 的 route-test 的 noDomain 条。

**`method:"TCP"`**(`internal/routetest/tcp.go`)已做:不走上面的 HTTP CONNECT,改对同一个面板入站(`mixed` 类型,两种协议共用一个端口)做无认证的 SOCKS5 CONNECT(RFC 1928)。原因是 2026-10-30 在沙箱里用真实 sing-box 1.14.4 实测:HTTP CONNECT 对关着的端口也先回 200(内核之后才在日志里报 connection refused),确认不了「连上了」;SOCKS5 则是出站连完之后才应答——开着的端口回成功码 0x00,关着的端口回 0x05。目标是域名时用域名地址类型交给内核解析并路由,有 `connectTo` 时连那个 IP(IPv4 / IPv6 各用对应的地址类型);端口缺省 443;成功只表示内核按路由把一条 TCP 连接连到了目标,不发任何数据,不保证上面跑的是什么服务。`exit.url` 是 `主机:端口`(IPv6 带方括号),不是网址;`ms` 是「连到面板入口 + 内核连上目标」;没有 `status`;成功后同样按本地端口在连接表里认线路(`chains` / `rule` / `destinationIP`)。失败时 `ok:false` + 中文 `error`:内核回的应答码按 RFC 1928 转述(`内核报告目标拒绝了连接`、`内核报告网络不可达`、`内核报告目标主机不可达`、`内核连目标超时` 等,没有对应说明的码带十六进制代码),内核一直没应答按超时报,入口连不上 / 对方不是按 SOCKS5 应答如实说。**`exit.connectTo`**(`internal/routetest/exit6.go` 的 `firstV4`、`routetest.go` 的 `Run`)已做:目标是域名、`resolve` 成功、第一个 IPv4 答案不在内核 fake-ip 池(198.18.0.0/15 的占位段)时,主探测像终端一样按这个 IP 去连(隧道 CONNECT 的是 `IP:端口`;请求的 Host、TLS 的 SNI 和证书校验仍用域名),并把这个 IP 写进 `exit.connectTo`;连接表仍按本地端口认线路,所以 `chains` / `rule` / `destinationIP` 照常给。目标本身是 IP、没有 A 答案、查失败、第一个答案是占位地址时,仍让内核按域名去连,响应里没有 `connectTo` 字段。注意这会让命中的规则变成「按 IP 连」的结果:内核靠首包嗅探(核心配置里 `sniff` 排在路由规则前)还原域名,嗅探不到(例如非 TLS / HTTP 的端口)时只有 IP 规则能管它——这正是前端 `exit.noDomain` 要标的情形(已做,见上)。**`exit6`**(`internal/routetest/exit6.go`)已做:`resolve.answers6` 有答案时,按第一个 v6 地址再访问一次,响应多一个 `exit6: {connectTo, ok, status?, ms?, error?}`;隧道连的是这个 v6 地址,请求的 Host、TLS 的 SNI 和证书校验仍用域名(像终端拿到 AAAA 后直接连那个地址),方式与主探测一致(GET / HEAD / TLS / TCP),只有探测结果、不读连接表。不适用时整个字段不出现(不是 null):目标是 IP、没有 AAAA 答案(档案没开 IPv6、查不到、查失败)、第一个 AAAA 答案落在内核 fake-ip 池(占位地址不是真实服务器);主探测失败不影响 exit6(v4 不通时 v6 的结果恰恰是要看的),exit6 失败也不影响主探测;独立计时,各用一份超时。

## `/api/openbox/rulesets`

实现约定(M1):`preview` / `import` 已实现,只收文本名单(Clash 经典 `.list`、YAML `payload`、一行一条的域名 / IP);条目 `type` 为 `domain` / `domain_suffix` / `domain_keyword` / `domain_regex` / `ip_cidr`,去重保序。`preview` 的 `q` 按值做不区分大小写的子串过滤,`total` 是名单总条数、`matched` 是命中数,`limit` 默认 50、最大 500;同一链接 60 秒内走内存缓存(搜索框边打边搜)。`import` 一次返回全部,超过 5 万条返回 400。二进制名单(.mrs / .srs)返回 400 并说明不支持;拉取失败 502。`entries`(按随包规则库的分类名查)已实现:`tag` 必填(缺少 400;不是 `geosite-` / `geoip-` 开头加字母数字和 `. _ ! @ -` 的名字 400;规则库里没有这个分类 404),`q` / `offset` / `limit` 与 `preview` 一致(`limit` 默认 50、最大 500),返回 `tag`、`total`(分类总条数)、`matched`(命中 `q` 的条数)、`offset`、`limit`、`entries`(空分类是 `[]`);分类条目超过 30 万时只列前面的并多一个 `truncated:true`。后端不自己解析 .srs,而是对 `<规则库目录>/<tag>.srs` 运行 `sing-box rule-set decompile`,所以看到的就是内核会匹配的那份;规则库目录由 `-geo-dir`(环境变量 `OPENBOX_GEO_DIR`)指定,只读、不下载,本地没有的分类如实 404;目录不存在或没有 `sing-box` 返回 503,解码失败返回 500 并带 `sing-box` 的原因。同一分类的解码结果在内存里缓存(按文件大小和修改时间失效,最多 2 个分类),同一时刻只解一个。`refresh` 已实现:POST `{ url }`(只收 http / https),对这一个链接跳过缓存重新联网拉取,拆成域名 / IP 两份编成 `<规则集目录>/list-<hash>.srs` / `list-<hash>-ip.srs`,只改规则集文件,不重启、不部署、不清理别的文件。响应 `{ url, tag, total, counts, needsRestart, changed, dropped, referenced }`:`total` 与 `preview` 同口径;`counts` 是按条目类型的条数(`domain` / `domain_suffix` / `domain_keyword` / `domain_regex` / `ip_cidr`,只含有的类型);`needsRestart` 仅当档案里正在用这个链接、且「有域名份 / 有 IP 份」的组合变了才为 `true`;`changed` 是有文件被改写或删除;`dropped` 是被丢掉的非法正则条数;`referenced` 是链接当前是否被站点集 / 前置自定义分流引用。失败时旧文件不动:链接不合格 400、二进制或空名单 400、对端拉取失败 502、没有 `sing-box` 503、编译失败 500(带 `sing-box` 的原因)。`/rulesets/check` 与 `/rulesets/refresh/status` 前端不调用,不实现。

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/rulesets/entries?{x}` | `fetchRulesetEntries` (api/openbox.ts:882) | OpenboxRulesetEntries | ( tag: string, { q = '', offset = 0, limit = 50 }: { q?: string; offset?: number |
| GET | `/api/openbox/rulesets/import?{x}` | `importRuleList` (api/openbox.ts:873) | OpenboxRulesetEntries | (url: string) |
| GET | `/api/openbox/rulesets/preview?{x}` | `fetchRuleListPreview` (api/openbox.ts:867) | OpenboxRulesetEntries | ( url: string, { q = '', offset = 0, limit = 50 }: { q?: string; offset?: number |
| POST | `/api/openbox/rulesets/refresh` | `refreshRuleList` (api/openbox.ts:1697) | OpenboxRulesetRefreshResult | (url: string) |

## `/api/openbox/servers`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/servers/port-check?port={x}&id={x}` | `checkServerPort` (api/openbox.ts:1706) | OpenboxPortCheck | (port: number, id: string) |

实现约定(M2):`port-check` 回 `{ ok, reason?, name? }`;端口不是 1–65535 的整数不是 400,而是 `{ ok: false, reason: "invalid" }`。`reason` 依次判 `invalid` → `reserved`(面板监听端口、内核固定端口 7853 / 7855 / 7891 / 9095、已部署配置里的控制接口和非 `share-*` 入站)→ `server`(档案里另一台服务器占着,`name` 是它的名字;`id` 是正在编辑的那台,自己不算)→ `listening`(读 `/proc/net`;因为不知道协议,TCP / UDP 任一被占都算)。读不到端口占用情况回 500。服务器本身存在档案的 `servers` 里,走 `/api/openbox/profile` 读写。

## `/api/openbox/service`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/service/core/{x}` | `runServiceAction` (api/openbox.ts:1030) | OpenboxServiceActionResult | (action: OpenboxServiceAction) |
| GET | `/api/openbox/service/status` | `fetchServiceStatus` (api/openbox.ts:1022) | OpenboxServiceStatus | () |

实现约定(M1):`status` 的 `conflicts` 是正在运行的其它代理服务 `[{ id, label, running: true }]`,只读检测(OpenWrt 查 `/etc/init.d/<名> running`,systemd 用 `systemctl is-active`),名单见 `internal/system/conflicts.go` 的 `KnownConflicts`,自己的 sing-box 和后端服务名不算;检测失败当作没有,接口不报错。`pendingRestart` 是 `{pending, reasons}`:**内核在运行**、且生成配置用到的输入和**上次成功部署**时不一样才为真,`reasons` 取 `nodes` / `dns` / `route` / `inbounds` / `system`(按这个顺序,永远是数组)。做法见 `internal/singbox/pending.go`:每次部署成功(`POST /deploy`、DNS 过滤的应用)在生成配置之前取一份按原因分组的输入指纹存进数据目录的 `applied-config.json`,状态接口现算一份来比;只读已存的数据,不调 core、不联网。从没通过本后端部署成功过(没有记录)时不判断,恒为「没有」;内核没在运行、算不出指纹时也是「没有」,接口不报错。不进指纹的:订阅的用量 / 更新时间 / 节点数、档案里的 `updates` / `traffic` / `shareRegions` / `serverInfo`、`dns.filter` 里的名单名字和自动更新计划(过滤是否按当前设置应用过才算)、本机的 resolv.conf 和局域网网段、规则集链接的远端内容。清掉它要再**部署**一次;`POST /service/core/restart` 只是让内核重读磁盘上的旧配置,不会清。没有 `container` / `conflictAutoStopped`(后端不自动停别的服务)。 `POST /service/core/{x}` 与 DNS 接管的配合:`stop` / `restart` 之前先撤销接管,撤销不成功回 500 且不动内核;`start` / `restart` 命令成功后按 `dns.mode` 对齐(等内核的 DNS 入站应答最多 20 秒),响应在原有的 `{ok, code, stderr, durationMs}` 之外,有话要说时多一个 `dnsNote`(例如对齐失败、DNS 保持原样),前端不认识这个字段时忽略即可;命令本身失败时不去对齐。后端进程启动时会先清上次异常退出遗留的接管(内核正常就保留,内核不在了就撤销)。

## `/api/openbox/site-latency`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/site-latency` | `testSiteLatency` (api/openbox.ts:1748) | OpenboxSiteLatencyRun | (sites: OpenboxSiteInput[]) |
| GET | `/api/openbox/site-latency/history` | `fetchSiteLatencyHistory` (api/openbox.ts:1721) | Partial<OpenboxSiteLatencyHistory> | () |

## `/api/openbox/subscription-shares`

实现约定(M2,管理部分):记录存 `subscription-shares.json`;列表 `{ shares }`,新建 / 修改 / 换 token `{ share }`,删除 `{ ok: true }`。新建带的 `token` 合格(32–128 位字母数字 `-` `_`)且没被占用才用,否则换新;PATCH 只改传了的字段,`regenerate: true` 才换 token,不收客户端指定的 token;新选的订阅必须存在,这条分享里已有的订阅 id 照旧保留。对外的 `/sub/<token>` 下发不在本分组,尚未实现(见 STATUS.md)。

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/subscription-shares` | `fetchSubscriptionShares` (api/openbox.ts:547) | { shares: OpenboxSubscriptionShare[] } | () |
| POST | `/api/openbox/subscription-shares` | `createSubscriptionShare` (api/openbox.ts:552) | { share: OpenboxSubscriptionShare } | (payload: { name: string; host: string; protocol: 'http' \| 'https'; subscriptio |
| DELETE | `/api/openbox/subscription-shares/{x}` | `deleteSubscriptionShare` (api/openbox.ts:573) | — | (id: string) |
| PATCH | `/api/openbox/subscription-shares/{x}` | `updateSubscriptionShare` (api/openbox.ts:560) | { share: OpenboxSubscriptionShare } | (id: string, payload: { name: string; host: string; protocol: 'http' \| 'https'; |
| POST | `/api/openbox/subscription-shares/{x}/regenerate` | `regenerateSubscriptionShare` (api/openbox.ts:568) | { share: OpenboxSubscriptionShare } | (id: string) |

## `/api/openbox/subscriptions`

实现约定(M1):错误码 400 = 输入有问题(含订阅内容里没有可用节点),404 = 订阅不存在,501 = `login`(账号登录来源暂不支持),502 = 订阅服务器那头出错(连不上 / 超时 / HTTP 非 2xx)。`url` 与 `urls` 同时给时 `url` 排第一;`content` 优先于地址。时间戳 `createdAt` / `updatedAt` 是毫秒。`PATCH` 只改传了的字段,`autoUpdate` / `nodeDns` 传 `null` = 清除。

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/subscriptions` | `fetchSubscriptions` (api/openbox.ts:542) | { subscriptions: OpenboxSubscription[] } | () |
| POST | `/api/openbox/subscriptions` | `createSubscription` (api/openbox.ts:624) | — | (payload: { url?: string urls?: string[] content?: string userAgent?: string log |
| POST | `/api/openbox/subscriptions/login` | `loginSubscription` (api/openbox.ts:607) | OpenboxSubscriptionLoginResult | (payload: { url: string userAgent?: string username: string password: string nam |
| PUT | `/api/openbox/subscriptions/order` | `reorderSubscriptions` (api/openbox.ts:900) | — | (ids: string[]) |
| POST | `/api/openbox/subscriptions/preview` | `previewSubscription` (api/openbox.ts:589) | OpenboxSubscriptionPreview | (payload: { url?: string urls?: string[] content?: string userAgent?: string log |
| DELETE | `/api/openbox/subscriptions/{x}` | `deleteSubscription` (api/openbox.ts:904) | — | (id: string) |
| PATCH | `/api/openbox/subscriptions/{x}` | `updateSubscription` (api/openbox.ts:637) | — | ( id: string, payload: { name?: string; url?: string; urls?: string[]; content?: |
| POST | `/api/openbox/subscriptions/{x}/refresh` | `refreshSubscription` (api/openbox.ts:915) | — | ( id: string, renameOptions?: OpenboxRenameOptions, ) |

实现约定(M4,订阅定时更新):订阅的 `autoUpdate` 由后端调度器执行(没有对应的 HTTP 接口)。`mode` 为 `hours` 时是上次更新后每隔 `hours` 小时;否则是上次更新那天起每隔 `days` 天的 `hour` 点(进程本地时区)。只更新订阅里的节点,不自动部署、不重启内核;节点变了以后 `service/status` 的 `pendingRestart`(原因 `nodes`)会提示。停用的订阅、没开定时的订阅、只有粘贴内容没有地址的订阅不更新。失败后 10 分钟 / 30 分钟 / 1 小时退避重试。

## `/api/openbox/system`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/system/timezone` | `fetchTimezone` (api/openbox.ts:1680) | OpenboxTimezoneState | () |
| PUT | `/api/openbox/system/timezone` | `saveTimezone` (api/openbox.ts:1683) | OpenboxTimezoneState | (zone: string) |

实现约定(M5,仅时区):`zones` / `countries` 来自内置的 IANA 时区表(419 个时区,国家代码取自 `zone.tab`,UTC 没有);`zone` 读不到(没有服务管理器、命令失败)是空串,名单照给,不报错;`offset`(+08:00)和 `local`(路由器本地时间)问系统的 `date`,读不到也是空串;`platform` 是 `openwrt` / `systemd` / `none`。`PUT` 请求体 `{ zone }`:别名先换成名单里的名字(如 `Asia/Chongqing` → `Asia/Shanghai`,`Etc/UTC` / `GMT` → `UTC`),不认识 400;OpenWrt 上 system 配置里有别人暂存的修改时拒绝(500,说明原因),改失败会恢复原值;没有受支持的服务管理器 501;改完读回来与请求不一致 500。

## `/api/openbox/terminal-test`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/terminal-test` | `testTerminal` (api/openbox.ts:1398) | OpenboxTerminalTest | (target: string, port?: number, method: OpenboxProbeMethod = 'GET') |
| GET | `/api/openbox/terminal-test/capability` | `fetchTerminalCapability` (api/openbox.ts:1396) | OpenboxTerminalCapability | () |

## `/api/openbox/traffic`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/traffic/month?${month ? ` | `fetchTrafficMonth` (api/openbox.ts:1487) | OpenboxTrafficMonth | (month?: string, countDirect = true) |
| GET | `/api/openbox/traffic/day?day=&limit=&hour=&direct=` | `fetchTrafficDay` (api/openbox.ts:1489) | OpenboxTrafficDay | (day: string, limit = 500, hour?: number \| null, countDirect = true) |
| GET | `/api/openbox/traffic/drill?day=&kind=&key=&by=&limit=&hour=&direct=` | `fetchTrafficDrill` (api/openbox.ts:1506) | OpenboxTrafficDrill | (day, kind, key, by, limit = 200, hour?, countDirect = true) |
| GET | `/api/openbox/traffic/usage` | `fetchTrafficUsage` (api/openbox.ts:1482) | OpenboxTrafficUsage | () |

> 实现约定(M3):四个接口都只接受 GET、响应 no-store。参数不合法回 400 加 `error`:`month` 要 `YYYY-MM`(缺省本月);`day` 必填 `YYYY-MM-DD`;`limit` 缺省 500(drill 前端传 200),范围 1 到 5000;`hour` 0 到 23(不给是整天);drill 的 `kind` / `by` 取 `client` / `node` / `host` 且不能相同,`key` 必填;`direct=0` 表示不统计直连,其余都统计。没有数据的日子 / 月份回空榜单和 0,不是 404。
> 数据来源:后台每 10 秒读一次内核的 `/connections`(sing-box 官方 Clash API),总量取累计总量的增量(含已关闭的短连接),终端 / 节点 / 访问目标来自各连接的增量,所以 `other` = 总量 − 各节点之和 = 没采样到的短连接;节点取出站链第一项(实际出口),访问目标取域名、没有则取目的 IP。启动后第一次、以及内核断开重连后的第一次只建基线,不记数。「走直连」指走 `direct` 类型出站的连接,`direct.tag` 是配置里第一个 direct 出站的名字。终端的 `name` 取 DHCP 租约主机名,没有就不带;`self`(路由器自己的地址)暂不带。
> 存储:数据目录下 `traffic/`,每天一个 `YYYY-MM-DD.json`(24 小时合计加「终端 × 节点 × 访问目标」三维表)、最近 7 天另有 `YYYY-MM-DD.hours.json`(每小时各自的三维表,只看某个小时时用)、`index.json`(每天摘要与文件大小)。超过 30 天的日子只留访问量最大的 200 个访问目标,其余并入「其他」;超过档案 `traffic.keepMonths`(缺省 12,最多 36)个月的整天删除;只看某个小时、而那天的小时明细已过期时,榜单为空、小时合计仍在。`usage` 只读索引:`rows` 是三维表条数,`perDay` / `hourPerDay` / `oldPerDay` 取最近 14 天的平均文件大小。

## `/api/openbox/update`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/update/cancel` | `cancelUpdate` (api/openbox.ts:1243) | { result: string } | () |
| GET | `/api/openbox/update/check` | `checkUpdate` (api/openbox.ts:1240) | { current: string; latest: string; via: string; hasUpdate: boolean } | () |
| GET | `/api/openbox/update/notes${latest ? ` | `fetchUpdateNotes` (api/openbox.ts:1667) | OpenboxUpdateNotes | (latest = '') |
| POST | `/api/openbox/update/run` | `runUpdate` (api/openbox.ts:1242) | { ok: boolean; output: string } | (channel: 'auto' \| 'direct' \| 'mirror') |
| GET | `/api/openbox/update/status` | `fetchUpdateStatus` (api/openbox.ts:1238) | OpenboxUpdateStatus | () |

## `/api/storage`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/storage` | `fetchRemoteSnapshot` (helper/persistentStorage.ts:85) | `{ entries: Record<string,string> }` | — |
| PUT | `/api/storage` | `saveRemoteSnapshot` (helper/persistentStorage.ts:108) | `{ ok, count }`(前端不读响应体) | `{ entries }`,整体替换;只保留 `config/` 开头的字符串条目 |

## 后端自带的接口(前端不调用)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | 健康检查,免登录,返回 `{ "ok": true, "version": "..." }`,给 procd / systemd / 监控用 |
