<template>
  <!-- DNS 上游(对照 v0.1.309):一行一条,哪一侧 / 协议 / 地址 / 端口;每行能测试、修改、删除。
       改完即保存:先让内核按所选协议真的查一次,查不通不保存。 -->
  <div class="card bg-base-100 border-base-300/60 border">
    <div class="card-body gap-3 p-4 text-sm">
      <div class="flex flex-col gap-1">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h2 class="text-base font-semibold">{{ $t('dnsUpstreamTitle') }}</h2>
          <div class="flex shrink-0 items-center gap-1">
            <button
              v-tip="$t('dnsUpstreamRestore')"
              type="button"
              class="btn btn-ghost btn-square btn-sm"
              :disabled="busy"
              :aria-label="$t('dnsUpstreamRestore')"
              @click="restoreOpen = true"
            >
              <ArrowUturnLeftIcon class="h-4 w-4" />
            </button>
            <button
              v-tip="$t('dnsUpstreamAdd')"
              type="button"
              class="btn btn-primary btn-sm btn-square shrink-0"
              :disabled="busy"
              :aria-label="$t('dnsUpstreamAdd')"
              @click="openEditor(null)"
            >
              <PlusIcon class="h-4 w-4" />
            </button>
          </div>
        </div>
        <p class="text-base-content/60 text-xs leading-relaxed">{{ $t('dnsUpstreamDescription') }}</p>
      </div>
      <div class="flex flex-col gap-1">
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-xs font-medium">{{ $t('dnsRegionLabel') }}</span>
          <div
            role="tablist"
            class="tabs-box tabs tabs-xs w-fit"
          >
            <a
              v-for="r in REGIONS"
              :key="r"
              role="tab"
              class="tab"
              :class="!detecting && region === r && 'tab-active'"
              @click="switchRegion(r)"
            >{{ regionName(r) }}</a>
          </div>
        </div>
        <p class="text-base-content/50 text-xs">
          {{ detecting ? $t('dnsRegionDetecting') : $t(region === 'intl' ? 'dnsRegionIntlNote' : 'dnsRegionCnNote') }}
        </p>
      </div>
      <div class="divide-base-content/10 divide-y">
        <div
          v-for="row in rows"
          :key="row.key"
          class="flex items-center gap-2 py-2"
        >
          <span class="badge badge-sm shrink-0">{{ sideName(row.side) }}</span>
          <div class="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 font-mono text-xs">
            <span
              v-if="!row.wan"
              class="text-base-content/60"
            >{{ protocolName(row.protocol) }}</span>
            <template v-if="row.wan">
              <span class="font-sans">{{ $t('dnsUpstreamWan') }}</span>
              <span class="text-base-content/50 truncate font-sans">{{ wanLabel }}</span>
              <span
                v-if="row.side === 'proxy'"
                class="text-base-content/50 font-sans"
              >{{ $t('dnsUpstreamWanDirectTag') }}</span>
            </template>
            <span
              v-else
              class="truncate"
            >{{ row.server }}<span
              v-if="row.protocol !== 'https'"
              class="text-base-content/50"
            >:{{ row.port }}</span></span>
            <span
              v-if="row.index > 0"
              class="text-base-content/50 font-sans"
            >{{ $t('dnsUpstreamBackupTag') }}</span>
          </div>
          <button
            v-tip="$t('dnsUpstreamTest')"
            type="button"
            class="btn btn-ghost btn-square btn-xs"
            :disabled="busy"
            :aria-label="$t('dnsUpstreamTest')"
            @click="runTest(row, row.key)"
          >
            <span
              v-if="testingKey === row.key"
              class="loading loading-spinner loading-xs"
            />
            <BoltIcon
              v-else
              class="h-4 w-4"
            />
          </button>
          <button
            type="button"
            class="btn btn-ghost btn-square btn-xs"
            :disabled="busy"
            :aria-label="$t('groupEdit')"
            @click="openEditor(row)"
          >
            <PencilSquareIcon class="h-4 w-4" />
          </button>
          <button
            v-tip="deleteBlocked(row) || $t('delete')"
            type="button"
            class="btn btn-ghost btn-square btn-xs hover:text-error"
            :disabled="busy || !!deleteBlocked(row)"
            :aria-label="$t('delete')"
            @click="removeRow(row)"
          >
            <TrashIcon class="h-4 w-4" />
          </button>
        </div>
      </div>
      <p class="text-base-content/50 text-xs">{{ $t('dnsUpstreamExtrasNote') }}</p>
    </div>

    <!-- 确认框:恢复默认 / 切换地区 / 删除 共用 -->
    <DialogWrapper
      v-model="confirmOpen"
      :title="confirmTitle"
    >
      <div class="flex flex-col gap-3">
        <p class="text-sm">{{ confirmMessage }}</p>
        <div class="flex justify-end gap-2">
          <button
            type="button"
            class="btn btn-sm"
            @click="answerConfirm(false)"
          >
            {{ $t('cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-sm btn-error"
            @click="answerConfirm(true)"
          >
            {{ $t('confirm') }}
          </button>
        </div>
      </div>
    </DialogWrapper>

    <DialogWrapper
      v-model="restoreOpen"
      :title="$t('dnsUpstreamRestore')"
    >
      <div class="flex flex-col gap-3">
        <p class="text-sm">{{ $t('dnsUpstreamRestoreConfirm') }}</p>
        <div class="flex justify-end gap-2">
          <button
            type="button"
            class="btn btn-sm"
            @click="restoreOpen = false"
          >
            {{ $t('cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-sm btn-error"
            :disabled="busy"
            @click="restoreDefaults"
          >
            <span
              v-if="busy"
              class="loading loading-spinner loading-xs"
            />
            {{ $t('confirm') }}
          </button>
        </div>
      </div>
    </DialogWrapper>

    <DialogWrapper
      v-model="editorOpen"
      :title="$t(editing ? 'dnsUpstreamEditTitle' : 'dnsUpstreamAddTitle')"
      box-class="w-full max-w-lg"
    >
      <div
        v-if="draft"
        class="flex flex-col gap-3 text-sm"
      >
        <div class="flex flex-wrap items-end justify-between gap-3">
          <div class="flex flex-col gap-1">
            <label class="text-xs font-medium">{{ $t('dnsUpstreamSide') }}</label>
            <div
              role="tablist"
              class="tabs-box tabs tabs-xs w-fit"
            >
              <a
                v-for="s in SIDES"
                :key="s"
                role="tab"
                class="tab"
                :class="draft.side === s && 'tab-active'"
                @click="draft.side = s"
              >{{ sideName(s) }}</a>
            </div>
          </div>
          <div
            role="tablist"
            class="tabs-box tabs tabs-xs w-fit"
          >
            <a
              role="tab"
              class="tab"
              :class="[draft.wan && 'tab-active', !wanAllowed(draft.side) && 'tab-disabled']"
              @click="wanAllowed(draft.side) && (draft.wan = true)"
            >{{ $t('dnsUpstreamSourceWan') }}</a>
            <a
              role="tab"
              class="tab"
              :class="!draft.wan && 'tab-active'"
              @click="draft.wan = false"
            >{{ $t('dnsUpstreamSourceIp') }}</a>
          </div>
        </div>
        <p class="text-base-content/50 text-xs">
          {{ $t(draft.side === 'direct' ? 'dnsUpstreamDirectNote' : 'dnsUpstreamProxyNote') }}
        </p>
        <div
          v-if="!wanAllowed(draft.side) || (draft.side === 'proxy' && draft.wan)"
          class="flex flex-col gap-1"
        >
          <template v-if="wanAllowed(draft.side)">
            <p
              v-if="draft.side === 'proxy' && draft.wan"
              class="text-base-content/50 text-xs"
            >
              {{ $t('dnsUpstreamWanProxyNote') }}
            </p>
          </template>
          <p
            v-else
            class="text-base-content/50 text-xs"
          >
            {{ $t('dnsUpstreamWanNotInCn') }}
          </p>
        </div>
        <div class="flex items-center gap-2">
          <select
            v-if="!draft.wan"
            :value="draft.protocol"
            class="select select-sm w-20 shrink-0"
            :title="$t('dnsUpstreamProtocol')"
            :aria-label="$t('dnsUpstreamProtocol')"
            @change="onProtocolChange(draft, $event)"
          >
            <option value="udp">UDP</option>
            <option value="tcp">TCP</option>
            <option value="tls">DoT</option>
            <option value="https">DoH</option>
          </select>
          <span
            v-if="draft.wan"
            class="text-base-content/60 min-w-0 flex-1 truncate text-xs"
          >{{ wanLabel }}</span>
          <template v-else>
            <input
              v-model.trim="draft.server"
              type="text"
              class="input input-sm min-w-0 flex-1 font-mono"
              :class="{ 'input-error': draft.server && !isServerValid(draft) }"
              :placeholder="draft.protocol === 'https' ? 'https://dns.example/dns-query' : draft.protocol === 'tls' ? 'dns.example.net' : draft.side === 'direct' ? '223.5.5.5' : '1.1.1.1'"
              :aria-label="$t(draft.protocol === 'https' ? 'dnsUpstreamDohUrl' : draft.protocol === 'tls' ? 'dnsUpstreamDotHost' : 'dnsUpstreamSourceIp')"
              spellcheck="false"
            >
            <input
              v-if="draft.protocol !== 'https'"
              v-model.number="draft.port"
              type="number"
              min="1"
              max="65535"
              class="input input-sm w-20 shrink-0"
              :class="{ 'input-error': !isPort(draft.port) }"
              :title="$t('dnsUpstreamPort')"
            >
          </template>
          <button
            v-tip="$t('dnsUpstreamTest')"
            type="button"
            class="btn btn-ghost btn-square btn-sm shrink-0"
            :disabled="busy || !draftValid"
            :aria-label="$t('dnsUpstreamTest')"
            @click="draft && runTest(draft, 'draft')"
          >
            <span
              v-if="testingKey === 'draft'"
              class="loading loading-spinner loading-xs"
            />
            <BoltIcon
              v-else
              class="h-4 w-4"
            />
          </button>
        </div>
        <!-- DoH、域名 DoT(地址还没填也算)要有一个 DNS 来解析它:默认用路由器的上游 DNS,也可以指定 -->
        <div
          v-if="!draft.wan && needsBootstrap(draft)"
          class="flex flex-col gap-3"
        >
          <div class="flex flex-wrap items-center justify-between gap-2">
            <label class="text-xs font-medium">{{ $t('dnsUpstreamEncryptedBootstrap') }}</label>
            <div
              role="tablist"
              class="tabs-box tabs tabs-xs w-fit"
            >
              <a
                role="tab"
                class="tab"
                :class="draft.bootstrap.server === WAN && 'tab-active'"
                @click="draft.bootstrap.server = WAN"
              >{{ $t('dnsUpstreamSourceWan') }}</a>
              <a
                role="tab"
                class="tab"
                :class="draft.bootstrap.server !== WAN && 'tab-active'"
                @click="draft.bootstrap.server === WAN && (draft.bootstrap.server = '')"
              >{{ $t('dnsUpstreamSourceIp') }}</a>
            </div>
          </div>
          <span
            v-if="draft.bootstrap.server === WAN"
            class="text-base-content/60 text-xs"
          >{{ wanLabel }}</span>
          <div
            v-else
            class="flex items-center gap-2"
          >
            <select
              v-model="draft.bootstrap.protocol"
              class="select select-sm w-20 shrink-0"
              :aria-label="$t('dnsUpstreamProtocol')"
            >
              <option value="udp">UDP</option>
              <option value="tcp">TCP</option>
            </select>
            <input
              v-model.trim="draft.bootstrap.server"
              type="text"
              class="input input-sm min-w-0 flex-1 font-mono"
              :class="{ 'input-error': draft.bootstrap.server && !isIp(draft.bootstrap.server) }"
              placeholder="223.5.5.5"
              :aria-label="$t('dnsUpstreamEncryptedBootstrap')"
              spellcheck="false"
            >
            <input
              v-model.number="draft.bootstrap.port"
              type="number"
              min="1"
              max="65535"
              class="input input-sm w-20 shrink-0"
              :class="{ 'input-error': !isPort(draft.bootstrap.port) }"
              :aria-label="$t('dnsUpstreamPort')"
            >
          </div>
        </div>
        <p
          v-if="editorError"
          class="text-error text-xs"
        >
          {{ editorError }}
        </p>
      </div>

      <!-- 取消 / 保存放底栏:内容区下面一条分割线(两端缩进到和内容边界对齐),和标题栏下面那条上下呼应 -->
      <template #footer>
        <div class="flex justify-end gap-2">
          <button
            type="button"
            class="btn btn-sm"
            :disabled="busy"
            @click="editorOpen = false"
          >
            {{ $t('cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="busy || !draftValid"
            @click="saveEditor"
          >
            <span
              v-if="busy"
              class="loading loading-spinner loading-xs"
            />
            {{ $t('subscriptionSave') }}
          </button>
        </div>
      </template>
    </DialogWrapper>
  </div>
</template>

<script setup lang="ts">
import {
  detectDnsRegion,
  fetchDnsRegionState,
  fetchWanDns,
  testDnsUpstream,
  type OpenboxDnsUpstreamTestResult,
  type OpenboxProfile,
} from '@/api/openbox'
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import { showNotification } from '@/helper/notification'
import { ArrowUturnLeftIcon, BoltIcon, PencilSquareIcon, PlusIcon, TrashIcon } from '@heroicons/vue/24/outline'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

type Region = 'cn' | 'intl'
type Side = 'direct' | 'proxy'
type Protocol = 'udp' | 'tcp' | 'https' | 'tls'
// DoH 的解析 DNS:server 为 'wan' 表示用路由器的上游 DNS
interface Bootstrap {
  server: string
  protocol: 'udp' | 'tcp'
  port: number
}
// 一条上游:wan 为真表示「上游 DNS」(路由器接口上的,固定直连、UDP 53);protocol 为 https 是 DoH,server 填 https 地址,bootstrap 才有用
interface Upstream {
  side: Side
  wan: boolean
  server: string
  protocol: Protocol
  port: number
  bootstrap: Bootstrap
}
interface Row extends Upstream {
  index: number
  key: string
}

const props = defineProps<{
  profile: OpenboxProfile
  patchProfile: (patch: Record<string, unknown>) => Promise<OpenboxProfile>
}>()
// 地区在后台判出来以后,让页面重新拉一遍档案
const emit = defineEmits<{ refresh: [] }>()
const { t } = useI18n()

// 下面这些常量和校验照抄 server/engine/dns-upstream.mjs;服务端那份才是准绳,这里只为在请求发出去之前挡掉明显的错
const WAN = 'wan'
const DEFAULT_PORT = 53
const MAX_EXTRAS = 3
const REGIONS: Region[] = ['cn', 'intl']
const SIDES: Side[] = ['direct', 'proxy']
const FALLBACK_PROTOCOL: Record<Side, Protocol> = { direct: 'udp', proxy: 'tcp' }
const IPV4_RE = /^(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)$/
const IPV6_RE = /^[0-9a-f:]+$/i

const isIp = (value: string) => {
  const host = String(value ?? '').trim()
  if (!host) return false
  if (IPV4_RE.test(host)) return host !== '0.0.0.0'
  if (!host.includes(':')) return false
  return IPV6_RE.test(host) && host !== '::' && host.split('::').length <= 2 && host.split(':').length <= 8
}
const isPort = (value: unknown) => typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= 65535
// DoH 地址:https://,有主机名,不带账号密码 / 查询 / #,端口缺省 443
const isDohUrl = (value: string) => {
  try {
    const u = new URL(value)
    const port = Number(u.port || 443)
    return (
      u.protocol === 'https:' &&
      !!u.hostname &&
      !u.username &&
      !u.password &&
      !u.hash &&
      !u.search &&
      !['0.0.0.0', '[::]'].includes(u.hostname) &&
      port >= 1 &&
      port <= 65535
    )
  } catch {
    return false
  }
}
const protocolName = (protocol: Protocol) => (protocol === 'https' ? 'DoH' : protocol === 'tls' ? 'DoT' : protocol.toUpperCase())
// 切协议时,端口还是原协议的默认值就换成新协议的默认值(53 / 443 / 853);用户改过的端口不动
const DEFAULT_PORTS: Record<Protocol, number> = { udp: 53, tcp: 53, https: 443, tls: 853 }
const onProtocolChange = (d: Upstream, e: Event) => {
  const next = (e.target as HTMLSelectElement).value as Protocol
  if (d.port === DEFAULT_PORTS[d.protocol]) d.port = DEFAULT_PORTS[next]
  d.protocol = next
}
// 要不要解析 DNS(引导 DNS):DoH,和地址不是 IP 的 DoT(地址还没填也算)
const needsBootstrap = (u: Upstream) => u.protocol === 'https' || (u.protocol === 'tls' && !isIp(u.server))
const defaultBootstrap = (b?: Partial<Bootstrap> | null): Bootstrap => ({
  server: b?.server ?? WAN,
  protocol: b?.protocol === 'tcp' ? 'tcp' : 'udp',
  port: b?.port ?? DEFAULT_PORT,
})
// 地址(和端口)填对了:DoH 看地址,DoT 看 IP 或域名加端口,UDP / TCP 看 IP 加端口
const isDotHost = (s: string) => {
  const h = s.trim()
  if (!h || isIp(h) || h.length > 253 || !h.includes('.')) return false
  return h.split('.').every((l) => l.length <= 63 && /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$/.test(l))
}
const isServerValid = (u: Upstream) => {
  if (u.protocol === 'https') return isDohUrl(u.server)
  if (u.protocol === 'tls') return (isIp(u.server) || isDotHost(u.server)) && isPort(u.port)
  return isIp(u.server) && isPort(u.port)
}
// 整条能不能保存 / 测试:上游 DNS 直接算;DoH 还要解析 DNS 填对(用上游 DNS,或 IP 加端口)
const isUpstreamValid = (u: Upstream) =>
  u.wan ||
  (isServerValid(u) &&
    (!needsBootstrap(u) || u.bootstrap.server === WAN || (isIp(u.bootstrap.server) && isPort(u.bootstrap.port))))

const sideName = (side: Side) => t(side === 'direct' ? 'dnsUpstreamDirect' : 'dnsUpstreamProxy')
const regionName = (r: Region) => t(r === 'intl' ? 'dnsRegionIntl' : 'dnsRegionCn')
const regionDefaultsText = (r: Region) => t(r === 'intl' ? 'dnsRegionDefaultsIntl' : 'dnsRegionDefaultsCn')
const regionScopeText = (r: Region) => t(r === 'intl' ? 'dnsRegionScopeIntl' : 'dnsRegionScopeCn')
// 「上游 DNS」只有直连能用;路由器在中国大陆之外时代理 DNS 也能用(那边的上游 DNS 没有污染)
const region = computed<Region>(() => (props.profile.dns?.region === 'intl' ? 'intl' : 'cn'))
const wanAllowed = (side: Side) => side === 'direct' || region.value === 'intl'

// 两个地区的默认(regionDnsDefaults):中国大陆——直连用上游 DNS,代理走 TCP 1.1.1.1;中国大陆之外——两侧都用上游 DNS
const regionDefaultsPatch = (r: Region) => ({
  direct: WAN,
  directProtocol: 'udp',
  directPort: DEFAULT_PORT,
  directExtras: [] as unknown[],
  proxy: r === 'intl' ? WAN : '1.1.1.1',
  proxyProtocol: r === 'intl' ? 'udp' : 'tcp',
  proxyPort: DEFAULT_PORT,
  proxyExtras: [] as unknown[],
})
const regionFallbackServer = (r: Region) => (r === 'intl' ? '8.8.8.8' : '223.5.5.5')

// 路由器此刻的上游 DNS(显示在「上游 DNS」那一行)
const wanServers = ref<string[]>([])
const wanLabel = computed(() =>
  wanServers.value.length ? wanServers.value[0] : t('dnsUpstreamWanUnknown', { server: regionFallbackServer(region.value) }),
)
onMounted(async () => {
  try {
    wanServers.value = await fetchWanDns()
  } catch {
    wanServers.value = []
  }
})

// ---- 启动时后台按出口公网 IP 判地区:没判完时轮询,判完刷新页面
const detecting = ref(false)
const lastRegion = ref<Region | null>(null)
let pollTimer: ReturnType<typeof setTimeout> | null = null
let pollCount = 0
const stopPoll = () => {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = null
  detecting.value = false
}
const poll = async () => {
  pollTimer = null
  let state = null as Awaited<ReturnType<typeof fetchDnsRegionState>> | null
  try {
    state = await fetchDnsRegionState()
  } catch {
    state = null
  }
  if (!state) {
    detecting.value = false
    return
  }
  if (state.pending) {
    if (++pollCount < 60) {
      detecting.value = true
      pollTimer = setTimeout(poll, 3000)
    } else {
      detecting.value = false
    }
    return
  }
  if (detecting.value || state.region !== region.value) {
    detecting.value = false
    lastRegion.value = region.value
    emit('refresh')
  }
}
watch(region, (next) => {
  if (lastRegion.value && next !== lastRegion.value) {
    showNotification({ content: 'dnsRegionDetected', params: { region: regionName(next) }, type: 'alert-info' })
  }
  lastRegion.value = null
})
onMounted(() => {
  poll()
})
onBeforeUnmount(() => {
  if (pollTimer) clearTimeout(pollTimer)
})

// ---- 档案 ⇄ 行
const busy = ref(false)
const readOne = (side: Side, server: unknown, protocol: unknown, port: unknown, bootstrap: unknown): Upstream | null => {
  const wan = server === WAN
  const host = typeof server === 'string' && !wan ? server : ''
  if (!wan && !host) return null
  return {
    side,
    wan,
    server: wan ? '' : host,
    protocol: wan
      ? 'udp'
      : protocol === 'udp' || protocol === 'tcp' || protocol === 'https' || protocol === 'tls'
        ? protocol
        : FALLBACK_PROTOCOL[side],
    port: wan || !isPort(port) ? DEFAULT_PORT : (port as number),
    bootstrap: defaultBootstrap(bootstrap as Partial<Bootstrap> | null),
  }
}
const sideRows = (side: Side): Row[] => {
  const dns = (props.profile.dns ?? {}) as Record<string, unknown>
  const main =
    side === 'direct'
      ? readOne(side, dns.direct, dns.directProtocol, dns.directPort, dns.directBootstrap)
      : readOne(side, dns.proxy, dns.proxyProtocol, dns.proxyPort, dns.proxyBootstrap)
  const extrasRaw = (side === 'direct' ? dns.directExtras : dns.proxyExtras) as Array<Record<string, unknown>> | undefined
  const extras = (Array.isArray(extrasRaw) ? extrasRaw : [])
    .slice(0, MAX_EXTRAS)
    .map((x) => readOne(side, x?.server, x?.protocol, x?.port, x?.bootstrap))
  return [main, ...extras]
    .filter((x): x is Upstream => !!x)
    .map((x, index) => ({ ...x, index, key: `${side}-${index}` }))
}
const rows = computed<Row[]>(() => [...sideRows('direct'), ...sideRows('proxy')])
const countOf = (side: Side) => rows.value.filter((r) => r.side === side).length
const rowLabel = (r: Upstream) => (r.wan ? t('dnsUpstreamWan') : r.server)

// DoH 的端口取自地址(缺省 443);DoH 和域名 DoT 才带 bootstrap
const toUpstreamOut = (u: Upstream) =>
  u.wan
    ? { server: WAN, protocol: 'udp', port: DEFAULT_PORT }
    : {
        server: u.server,
        protocol: u.protocol,
        port: u.protocol === 'https' && isDohUrl(u.server) ? Number(new URL(u.server).port || 443) : u.port,
        ...(needsBootstrap(u) ? { bootstrap: u.bootstrap } : {}),
      }
// 把一整张列表写回档案:每侧第一条是主上游,后面是备用;一侧删光了就退回「上游 DNS」
const toPatch = (list: Upstream[]) => {
  const out: Record<string, unknown> = {}
  for (const side of SIDES) {
    const own = list.filter((r) => r.side === side)
    const [main, ...extras] = own.length
      ? own
      : [{ side, wan: true, server: '', protocol: 'udp' as Protocol, port: DEFAULT_PORT, bootstrap: defaultBootstrap() }]
    const m = toUpstreamOut(main)
    const ex = extras.slice(0, MAX_EXTRAS).map(toUpstreamOut)
    const bootstrap = 'bootstrap' in m ? m.bootstrap : null
    out[side] = m.server
    out[`${side}Protocol`] = m.protocol
    out[`${side}Port`] = m.port
    out[`${side}Bootstrap`] = bootstrap
    out[`${side}Extras`] = ex
  }
  return out
}
const persist = async (list: Upstream[]) => {
  await props.patchProfile({ dns: toPatch(list) })
}
const notifyFail = (error: unknown) =>
  showNotification({
    content: 'routingSaveFailed',
    params: { message: error instanceof Error ? error.message : String(error) },
    type: 'alert-error',
  })

// ---- 确认框(恢复默认 / 切换地区 / 删除共用)
const confirmOpen = ref(false)
const confirmTitle = ref('')
const confirmMessage = ref('')
let confirmResolve: ((ok: boolean) => void) | null = null
const askConfirm = (message: string, title = '') =>
  new Promise<boolean>((resolve) => {
    confirmTitle.value = title
    confirmMessage.value = message
    confirmResolve = resolve
    confirmOpen.value = true
  })
const answerConfirm = (ok: boolean) => {
  const resolve = confirmResolve
  confirmResolve = null
  confirmOpen.value = false
  resolve?.(ok)
}
watch(confirmOpen, (open) => {
  if (!open && confirmResolve) {
    confirmResolve(false)
    confirmResolve = null
  }
})

// ---- 切换地区:两侧换成该地区的默认值,备用清空
const switchRegion = async (next: Region) => {
  if (busy.value || next === region.value) return
  const ok = await askConfirm(
    t('dnsRegionSwitchConfirm', { region: regionName(next), scope: regionScopeText(next), defaults: regionDefaultsText(next) }),
  )
  if (!ok) return
  stopPoll()
  busy.value = true
  try {
    await props.patchProfile({ dns: { region: next, ...regionDefaultsPatch(next) } })
    showNotification({ content: 'dnsRegionSwitched', params: { region: regionName(next) }, type: 'alert-success' })
  } catch (error) {
    notifyFail(error)
  } finally {
    busy.value = false
  }
}

// ---- 恢复默认:先按出口公网 IP 判一次地区,再换回那个地区的默认值
const restoreOpen = ref(false)
const restoreDefaults = async () => {
  if (busy.value) return
  stopPoll()
  busy.value = true
  try {
    let detected: { region: Region | null; ip: string } = { region: null, ip: '' }
    try {
      detected = await detectDnsRegion()
    } catch {
      // 判不出来就按当前地区恢复
    }
    const target = detected.region ?? region.value
    const defaults = regionDefaultsPatch(target)
    const dns = (props.profile.dns ?? {}) as Record<string, unknown>
    const same =
      target === region.value &&
      dns.direct === defaults.direct &&
      !((dns.directExtras as unknown[] | undefined) ?? []).length &&
      dns.proxy === defaults.proxy &&
      dns.proxyProtocol === defaults.proxyProtocol &&
      !((dns.proxyExtras as unknown[] | undefined) ?? []).length &&
      (defaults.proxy === WAN || ((dns.proxyPort as number | undefined) ?? DEFAULT_PORT) === defaults.proxyPort)
    if (!same) await props.patchProfile({ dns: { region: target, ...defaults } })
    restoreOpen.value = false
    const params = { region: regionName(target), ip: detected.ip }
    if (detected.region) {
      showNotification({
        content: same ? 'dnsUpstreamAlreadyDefault' : 'dnsUpstreamRestored',
        params,
        type: same ? 'alert-info' : 'alert-success',
      })
    } else {
      showNotification({
        content: same ? 'dnsUpstreamAlreadyDefaultNoRegion' : 'dnsUpstreamRestoredNoRegion',
        params,
        type: 'alert-warning',
      })
    }
  } catch (error) {
    notifyFail(error)
  } finally {
    busy.value = false
  }
}

// ---- 测试:让内核按所选协议真的查一次;经代理的那一侧会带上走的线路
const testingKey = ref('')
const testPayload = (u: Upstream) => ({ side: u.side, ...toUpstreamOut(u) })
const viaText = (r: OpenboxDnsUpstreamTestResult) => (r.chain?.length ? r.chain.join(' → ') : r.via)
const detailOf = (r: OpenboxDnsUpstreamTestResult, withNote: boolean) =>
  [
    ...(r.wan ? [r.server] : []),
    `${r.ms} ms`,
    ...(r.via ? [t('dnsUpstreamTestVia', { policy: r.policy ?? '', node: viaText(r) })] : []),
    ...(r.warning ? [r.warning] : []),
    ...(withNote && r.note ? [r.note] : []),
  ].join(' · ')
const failText = (r: OpenboxDnsUpstreamTestResult) =>
  [t('dnsUpstreamTestFail', { message: r.error ?? '' }), ...(r.note ? [r.note] : [])].join(' · ')
const runTest = async (u: Upstream, key: string) => {
  if (!isUpstreamValid(u)) return
  testingKey.value = key
  editorError.value = ''
  try {
    const r = await testDnsUpstream(testPayload(u))
    if (!r.ok) {
      if (key === 'draft') editorError.value = failText(r)
      showNotification({
        content: 'dnsUpstreamTestFailed',
        params: { side: sideName(u.side), message: r.error ?? '' },
        type: 'alert-error',
        timeout: 8000,
      })
      return
    }
    showNotification({
      content: 'dnsUpstreamTestOk',
      params: { side: sideName(u.side), detail: detailOf(r, true) },
      type: 'alert-success',
      timeout: 6000,
    })
  } catch (error) {
    if (key === 'draft') editorError.value = error instanceof Error ? error.message : String(error)
    notifyFail(error)
  } finally {
    testingKey.value = ''
  }
}

// ---- 删除:不能删光;路由器在中国大陆时代理 DNS 至少留一个,别的一侧删光了改用「上游 DNS」
const deleteBlocked = (r: Row) => {
  if (countOf(r.side) > 1) return ''
  if (wanAllowed(r.side)) return r.wan ? t('dnsUpstreamWanLast') : ''
  return t('dnsUpstreamLastOne')
}
const removeRow = async (r: Row) => {
  if (deleteBlocked(r)) return
  const params = { side: sideName(r.side), server: rowLabel(r) }
  const ok = await askConfirm(t(countOf(r.side) <= 1 ? 'dnsUpstreamDeleteToWanConfirm' : 'dnsUpstreamDeleteConfirm', params))
  if (!ok) return
  busy.value = true
  try {
    await persist(rows.value.filter((x) => x.key !== r.key))
    showNotification({ content: 'dnsUpstreamRemoved', params, type: 'alert-success' })
  } catch (error) {
    notifyFail(error)
  } finally {
    busy.value = false
  }
}

// ---- 添加 / 修改
const editorOpen = ref(false)
const editing = ref<Row | null>(null)
const draft = ref<Upstream | null>(null)
const editorError = ref('')
const draftValid = computed(() => {
  const d = draft.value
  return !!d && isUpstreamValid(d)
})
const openEditor = (row: Row | null) => {
  editing.value = row
  editorError.value = ''
  draft.value = row
    ? { side: row.side, wan: row.wan, server: row.server, protocol: row.protocol, port: row.port, bootstrap: { ...row.bootstrap } }
    : { side: 'proxy', wan: false, server: '', protocol: 'tcp', port: DEFAULT_PORT, bootstrap: defaultBootstrap() }
  editorOpen.value = true
}
const saveEditor = async () => {
  const d = draft.value
  if (!d || !draftValid.value) return
  if (d.wan && !wanAllowed(d.side)) {
    editorError.value = t('dnsUpstreamWanNotInCn')
    return
  }
  const current = editing.value
  // 把这一条从原来那一侧挪走以后,原来那一侧不能空(路由器在中国大陆时代理 DNS)
  if (current && current.side !== d.side && !wanAllowed(current.side) && countOf(current.side) <= 1) {
    editorError.value = t('dnsUpstreamLastOne')
    return
  }
  const duplicate = rows.value
    .filter((r) => r.key !== current?.key)
    .some((r) => r.side === d.side && (d.wan ? r.wan : !r.wan && r.server === d.server))
  if (duplicate) {
    editorError.value = t('dnsUpstreamExtraDuplicate')
    return
  }
  if (!current && countOf(d.side) > MAX_EXTRAS) {
    editorError.value = t('dnsUpstreamTooMany', { max: MAX_EXTRAS + 1 })
    return
  }
  busy.value = true
  editorError.value = ''
  try {
    // 改完即保存:先让内核真的查一次,查不通不保存
    const r = await testDnsUpstream(testPayload(d))
    if (!r.ok) {
      editorError.value = failText(r)
      return
    }
    const next: Upstream[] = current
      ? rows.value.map((x) => (x.key === current.key ? { ...d } : x))
      : [...rows.value, { ...d }]
    await persist([...next.filter((x) => x.side === 'direct'), ...next.filter((x) => x.side === 'proxy')])
    showNotification({
      content: 'dnsUpstreamSaved',
      params: { side: sideName(d.side), detail: detailOf(r, false) },
      type: 'alert-success',
      timeout: 6000,
    })
    editorOpen.value = false
  } catch (error) {
    editorError.value = error instanceof Error ? error.message : String(error)
  } finally {
    busy.value = false
  }
}
</script>
