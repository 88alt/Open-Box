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

## `/api/openbox/activation`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| DELETE | `/api/openbox/activation` | `deactivate` (api/clientApp.ts:344) | OpenboxActivation | () |
| GET | `/api/openbox/activation` | `fetchActivation` (api/clientApp.ts:328) | OpenboxActivation | () |
| POST | `/api/openbox/activation` | `activate` (api/clientApp.ts:331) | OpenboxActivation | (code: string, left: string, right: string) |
| PUT | `/api/openbox/activation` | `updateActivationText` (api/clientApp.ts:338) | OpenboxActivation | (left: string, right: string) |

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

> 实现约定(M1):`link` 解析不了、缺 `upstream`、上游不存在或成环返回 400。测速用 `internal/probe`(临时 sing-box + 本机 http 入站,后端自己计时);`testUrl` 不传用档案的 `testUrl`;`via` 是按配置静态推出的上游选中节点(selector 取 default,urltest 取第一个成员),内核接入后改读控制接口。找不到 sing-box 返回 503。**未做**:`ipUrls`(IP 地区),响应里没有 `ip` 字段。

## `/api/openbox/client-app`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/client-app/egress-country` | `detectEgressCountry` (api/clientApp.ts:156) | OpenboxEgressCountry | () |
| GET | `/api/openbox/client-app/info` | `fetchClientAppInfo` (api/clientApp.ts:152) | OpenboxClientAppInfo | () |
| POST | `/api/openbox/client-app/share-codes` | `fetchShareCodeSeals` (api/clientApp.ts:215) | Record<string, OpenboxShareCodeSeal> | (links: Record<string, string>) |
| POST | `/api/openbox/client-app/share-codes/{x}/rotate` | `rotateShareCode` (api/clientApp.ts:232) | OpenboxShareCodeRotated | (id: string) |

## `/api/openbox/client-config`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/client-config` | `fetchClientConfig` (api/clientApp.ts:182) | OpenboxClientConfigIssue | () |
| PUT | `/api/openbox/client-config/enabled` | `setClientConfigEnabled` (api/clientApp.ts:189) | { enabled: boolean } | (enabled: boolean) |
| POST | `/api/openbox/client-config/file` | `fetchClientConfigFile` (api/clientApp.ts:199) | OpenboxClientBundle | (lan: string, publicBase: string) |
| POST | `/api/openbox/client-config/rotate` | `rotateClientConfig` (api/clientApp.ts:186) | OpenboxClientConfigIssue | () |

## `/api/openbox/clients`

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

## `/api/openbox/diagnostics`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/diagnostics` | `fetchDiagnostics` (api/openbox.ts:1556) | Record<string, unknown> | () |

## `/api/openbox/dns`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/dns/flush-cache` | `flushDnsCache` (api/openbox.ts:1625) | OpenboxDnsFlushResult | () |
| GET | `/api/openbox/dns/region-detect` | `fetchDnsRegionState` (api/openbox.ts:1581) | OpenboxDnsRegionState | () |
| POST | `/api/openbox/dns/region-detect` | `detectDnsRegion` (api/openbox.ts:1589) | OpenboxDnsRegionDetect | () |
| POST | `/api/openbox/dns/upstream-test` | `testDnsUpstream` (api/openbox.ts:1616) | OpenboxDnsUpstreamTestResult | (payload: OpenboxDnsUpstreamTestPayload) |
| GET | `/api/openbox/dns/wan` | `fetchWanDns` (api/openbox.ts:1572) | { servers?: string[] } | () |

## `/api/openbox/dns-filter`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/dns-filter` | `fetchDnsFilter` (api/openbox.ts:168) | DnsFilterStatus | () |
| PUT | `/api/openbox/dns-filter` | `saveDnsFilter` (api/openbox.ts:169) | — | (settings: DnsFilterSettings) |
| POST | `/api/openbox/dns-filter/apply` | `applyDnsFilter` (api/openbox.ts:170) | DnsFilterStatus | (update = false, listId = '') |
| GET | `/api/openbox/dns-filter/preview?{x})}` | `fetchDnsFilterPreview` (api/openbox.ts:173) | DnsFilterPreview | (url: string, search: string, page: number, pageSize = 20, action: DnsFilterPrev |
| GET | `/api/openbox/dns-filter/records?{x})}` | `fetchDnsFilterRecords` (api/openbox.ts:172) | { rows: DnsFilterRecord[]; total: number; page: number; pageSize: number } | (search: string, result: string, page: number, pageSize = 20) |
| GET | `/api/openbox/dns-filter/summary` | `fetchDnsFilterSummary` (api/openbox.ts:171) | DnsFilterSummary | () |

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

## `/api/openbox/node-info`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/node-info/{x}` | `fetchNodeInfo` (api/openbox.ts:1873) | OpenboxNodeInfo | (tag: string) |
| GET | `/api/openbox/node-info/{x}/entry-country` | `fetchNodeEntryCountry` (api/openbox.ts:1874) | { ip: string; country: string } | (tag: string) |
| POST | `/api/openbox/node-info/{x}/exit` | `fetchNodeExit` (api/openbox.ts:1877) | OpenboxNodeExit | (tag: string, refresh = false) |

## `/api/openbox/nodes`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/nodes/latency` | `testNodeLatency` (api/openbox.ts:666) | { results: OpenboxLatencyResult[] } | (payload: { url?: string urls?: string[] content?: string userAgent?: string log |
| POST | `/api/openbox/nodes/latency` | `testStoredLatency` (api/openbox.ts:1793) | OpenboxStoredLatencyResponse | (jobs: OpenboxStoredLatencyJob[], timeoutMs?: number) |
| POST | `/api/openbox/nodes/latency` | `testStoredLatencyStream` (api/openbox.ts:1805) | — | ( jobs: OpenboxStoredLatencyJob[], onDone: (indices: number[]) |

> 实现约定(M1):请求体有 `url` / `urls` / `content` / `loginId` 之一 = 预览(拉取、解析、改名后按 `tags` 测,不落盘,测速地址取档案 `testUrl`;`loginId` 返回 501),否则按 `jobs` 测已保存节点(`url` 缺省取档案 `testUrl`)。`results` 与 `tags` / `jobs` 一一对应。`reason`:`not-found`(是组或已不在订阅里)、`invalid`(地址不合法或 sing-box 起不来,`error` 带原因)、`timeout`、`failed`。`timeoutMs` 默认 5000,上限 30000;一次最多 500 个,同时测 16 个。`progress:true`(仅 jobs)回 NDJSON:每测完一个 `{"done":[下标]}`,最后 `{"results":[...]}`,出错 `{"error":"..."}`。找不到 sing-box 返回 503。**未做**:延迟历史(`record` 忽略,响应没有 `history`)。

## `/api/openbox/penetration`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/penetration` | `queryPenetration` (api/openbox.ts:1174) | OpenboxPenetrationResult | (target: string) |

## `/api/openbox/policies`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/policies/entries?{x}` | `fetchPolicyEntries` (api/openbox.ts:1214) | OpenboxPolicyEntries | (params: { name: string tab?: 'all' \| 'domain' \| 'ip' q?: string sort?: 'type' |

## `/api/openbox/profile`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/profile` | `fetchProfile` (api/openbox.ts:516) | { profile: OpenboxProfile } | () |
| PUT | `/api/openbox/profile` | `saveProfile` (api/openbox.ts:534) | { profile: OpenboxProfile } | (patch: Record<string, unknown>) |
| GET | `/api/openbox/profile/defaults?region=cn` | `fetchDnsRewriteDefaults` (api/openbox.ts:522) | { dnsRewriteDefaults?: OpenboxDnsRewriteRule[] } | () |
| GET | `/api/openbox/profile/defaults?region={x}` | `fetchProfileDefaults` (api/openbox.ts:528) | { defaults: OpenboxProfileDefaults } | (region: string) |

> 实现约定(M1):GET 返回「默认值 + 已保存内容」的完整档案。PUT 的请求体是**补丁**,深度合并(对象递归合并,数组和标量整体替换,`null` 删除该键,下次读取由默认值补上),合并后整体校验,失败 `400 { error }` 且不落盘;`rulesetDir` 由服务端决定,客户端传的忽略;`chainProxies[].node` 只读(link 没变沿用旧摘要)。请求体上限 1 MiB。未知键原样保留。

> 实现约定(M1):`region` 取 `cn` / `intl`(缺省 `cn`,其它 400)。响应同时带 `defaults` 和 `dnsRewriteDefaults`(两个前端函数共用同一路径)。

## `/api/openbox/route-test`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/route-test` | `testRoute` (api/openbox.ts:1295) | OpenboxRouteTest | (target: string, port?: number, method: OpenboxProbeMethod = 'GET') |

## `/api/openbox/rulesets`

实现约定(M1):`preview` / `import` 已实现,只收文本名单(Clash 经典 `.list`、YAML `payload`、一行一条的域名 / IP);条目 `type` 为 `domain` / `domain_suffix` / `domain_keyword` / `domain_regex` / `ip_cidr`,去重保序。`preview` 的 `q` 按值做不区分大小写的子串过滤,`total` 是名单总条数、`matched` 是命中数,`limit` 默认 50、最大 500;同一链接 60 秒内走内存缓存(搜索框边打边搜)。`import` 一次返回全部,超过 5 万条返回 400。二进制名单(.mrs / .srs)返回 400 并说明不支持;拉取失败 502。`entries`(按分类名)和 `refresh` 暂返回 501,等随包规则库、配置生成与 `sing-box` 就绪后再做。`/rulesets/check` 与 `/rulesets/refresh/status` 前端不调用,不实现。

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

## `/api/openbox/service`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/service/core/{x}` | `runServiceAction` (api/openbox.ts:1030) | OpenboxServiceActionResult | (action: OpenboxServiceAction) |
| GET | `/api/openbox/service/status` | `fetchServiceStatus` (api/openbox.ts:1022) | OpenboxServiceStatus | () |

## `/api/openbox/site-latency`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/site-latency` | `testSiteLatency` (api/openbox.ts:1748) | OpenboxSiteLatencyRun | (sites: OpenboxSiteInput[]) |
| GET | `/api/openbox/site-latency/history` | `fetchSiteLatencyHistory` (api/openbox.ts:1721) | Partial<OpenboxSiteLatencyHistory> | () |

## `/api/openbox/subscription-shares`

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

## `/api/openbox/system`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/system/timezone` | `fetchTimezone` (api/openbox.ts:1680) | OpenboxTimezoneState | () |
| PUT | `/api/openbox/system/timezone` | `saveTimezone` (api/openbox.ts:1683) | OpenboxTimezoneState | (zone: string) |

## `/api/openbox/terminal-test`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| POST | `/api/openbox/terminal-test` | `testTerminal` (api/openbox.ts:1398) | OpenboxTerminalTest | (target: string, port?: number, method: OpenboxProbeMethod = 'GET') |
| GET | `/api/openbox/terminal-test/capability` | `fetchTerminalCapability` (api/openbox.ts:1396) | OpenboxTerminalCapability | () |

## `/api/openbox/traffic`

| 方法 | 路径 | 前端函数(文件:行) | 响应类型 | 参数 |
|---|---|---|---|---|
| GET | `/api/openbox/traffic/month?${month ? ` | `fetchTrafficMonth` (api/openbox.ts:1487) | OpenboxTrafficMonth | (month?: string, countDirect = true) |
| GET | `/api/openbox/traffic/usage` | `fetchTrafficUsage` (api/openbox.ts:1482) | OpenboxTrafficUsage | () |

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
