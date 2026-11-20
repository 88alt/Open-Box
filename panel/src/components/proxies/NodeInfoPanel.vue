<template>
  <!-- 节点信息:上半是节点自己(类型 / 协议 / 服务器 / 入口 IP / IPv6),下半是经这个节点出去的出口
       (出口 IPv4 / IPv6 / 属地 / ASN / IP 类型 / 是否原生 / 共享人数)。riskOnly 只显示出口的风险评估
       (Net.Coffee 信任评分、Ping0.cc 风控值)和两家的完整报告链接;compact 压成一小块(质量卡用)。
       两半各自查、各自失败。 -->
  <div
    :class="
      compact
        ? 'w-full min-w-0 text-xs leading-4'
        : 'node-info-pane @container/node-info grid content-start gap-x-2 gap-y-1 overflow-y-auto pr-1 text-xs leading-4'
    "
    :style="compact ? undefined : { gridTemplateColumns: `${labelWidth} minmax(0, 1fr)` }"
  >
    <template v-if="compact">
      <div
        v-if="riskOnly"
        class="flex flex-col gap-1"
        :aria-busy="loading || undefined"
      >
        <div class="flex items-start justify-between gap-2">
          <h3 class="text-base-content/60 shrink-0">{{ $t('nodeRiskThreats') }}</h3>
          <span
            class="min-w-0 text-right wrap-anywhere"
            :class="threatsCell.color"
          >
            {{ threatsCell.value }}
          </span>
        </div>
        <div
          class="flex min-h-5 min-w-0 items-center gap-2"
          data-quality-risk-score
        >
          <progress
            v-if="exit?.trustScore != null"
            class="progress h-1.5 min-w-0 flex-1"
            :style="{ color: trustColor }"
            :value="exit.trustScore"
            max="100"
            :aria-label="$t('nodeInfoTrust')"
          ></progress>
          <span
            v-else
            class="bg-base-content/10 rounded-field h-1.5 min-w-0 flex-1"
            aria-hidden="true"
          ></span>
          <span
            class="shrink-0 text-sm tabular-nums"
            :style="exit?.trustScore != null ? { color: trustColor } : undefined"
          >
            {{ exit?.trustScore != null ? `${exit.trustScore} / 100` : '—' }}
          </span>
        </div>
      </div>
      <div
        v-else
        class="grid grid-cols-2 items-start gap-2"
      >
        <div
          class="flex min-w-0 flex-col gap-1"
          data-quality-entry
        >
          <h3 class="text-base-content/60">{{ connectTypeText }} · {{ $t('nodeInfoEntryIp') }}</h3>
          <div class="flex min-h-5 min-w-0 items-center gap-1">
            <CountryFlag
              v-if="info?.addresses.v4.length"
              :code="entryCountry"
              :size="12"
            />
            <span
              v-tip="
                infoError ||
                info?.resolveError ||
                (info && info.addresses.v4.length > 1 ? entryV4Text : undefined)
              "
              class="min-w-0 wrap-anywhere tabular-nums"
            >
              {{ info?.addresses.v4[0] || '—' }}
            </span>
          </div>
        </div>
        <div
          class="flex min-w-0 flex-col items-end gap-1 text-right"
          data-quality-exit
        >
          <h3 class="text-base-content/60">{{ $t('nodeInfoExitIp') }}</h3>
          <span class="flex min-h-5 max-w-full min-w-0 items-center gap-1">
            <CountryFlag
              v-if="exitV4"
              :code="exit?.country || ''"
              :size="12"
            />
            <span
              v-tip="error"
              class="min-w-0 wrap-anywhere tabular-nums"
            >
              {{ exitV4 || '—' }}
            </span>
          </span>
        </div>
      </div>
    </template>
    <template v-else>
      <template v-if="!riskOnly">
        <NodeInfoText
          row
          :text="connectTypeText"
          :label="$t('type')"
        >
          <template
            v-if="info?.detour"
            #suffix
          >
            <span class="ml-auto min-w-0 truncate text-right">
              <span class="text-base-content/60">{{ $t('nodeInfoUpstream') }}</span>
              {{ info.detour }}
            </span>
          </template>
        </NodeInfoText>
        <div
          v-if="infoError && !info"
          class="col-span-2 break-all opacity-80"
        >
          {{ infoError }}
        </div>
        <template v-else-if="info">
          <NodeInfoText
            row
            :text="info.protocol.join(' · ')"
            :label="$t('nodeInfoProtocol')"
          >
            <template
              v-if="udp"
              #suffix
            >
              <span class="badge badge-xs badge-soft badge-info h-4 shrink-0">UDP</span>
            </template>
          </NodeInfoText>
          <NodeInfoText
            row
            :text="serverText"
            :label="$t('nodeInfoServer')"
          />
          <NodeInfoText
            row
            :text="entryV4Text"
            :label="$t('nodeInfoEntryIp')"
          >
            <template
              v-if="entryCountry && info.addresses.v4.length"
              #suffix
            >
              <span class="inline-flex shrink-0 items-center gap-1">
                <span aria-hidden="true">·</span>
                <CountryFlag
                  :code="entryCountry"
                  :size="12"
                />
                {{ countryLabel(entryCountry) }}
              </span>
            </template>
          </NodeInfoText>
          <NodeInfoText
            row
            :text="info.addresses.v6.join(' · ') || $t('nodeInfoNoIpv6')"
            :label="$t('nodeInfoEntryIpv6')"
          />
        </template>
        <span
          v-else
          class="loading loading-dots loading-xs col-span-2"
        ></span>
        <div
          role="separator"
          class="border-base-content/10 col-span-2 my-1 border-t"
        ></div>
      </template>

      <template v-if="!riskOnly">
        <NodeInfoText
          row
          class="tabular-nums"
          :text="exitV4 || $t('nodeInfoIpv4Unverified')"
          :label="$t('nodeInfoExitIp')"
        >
          <template
            v-if="exitV4 && exit?.country"
            #suffix
          >
            <span class="inline-flex shrink-0 items-center gap-1">
              <span aria-hidden="true">·</span>
              <CountryFlag
                :code="exit.country"
                :size="12"
              />
              {{ countryLabel(exit.country) }}
            </span>
          </template>
        </NodeInfoText>
        <NodeInfoText
          row
          class="tabular-nums"
          :text="exitV6 || $t('nodeInfoIpv6Unverified')"
          :label="$t('nodeInfoExitIpv6')"
        />
      </template>

      <div
        v-if="loading && !exit"
        class="col-span-2 flex items-center gap-2 opacity-80"
      >
        <span class="loading loading-spinner loading-xs"></span>
        {{ $t('nodeInfoQuerying') }}
      </div>
      <div
        v-else-if="error && !exit"
        class="col-span-2 flex items-start justify-between gap-2"
      >
        <span class="break-all opacity-80">{{ error }}</span>
        <button
          v-tip="$t('refresh')"
          type="button"
          class="flex shrink-0 cursor-pointer items-center opacity-70 hover:opacity-100"
          :disabled="loading"
          :aria-label="$t('refresh')"
          @click="load(true)"
        >
          <ArrowPathIcon
            class="h-3.5 w-3.5"
            :class="loading && 'animate-spin'"
          />
        </button>
      </div>
      <template v-else-if="exit">
        <NodeRiskPanel
          v-if="riskOnly"
          :trust-score="exit.trustScore"
          :trust-color="trustColor"
          :net-coffee-href="netCoffeeHref"
          :ping0-risk="ping0Risk"
          :ping0-status="ping0Status"
          :ping0-source="ping0Source"
          :ping0-href="ping0Href"
          :assessment="riskAssessment"
          :traffic="exit.source === 'ip.net.coffee' ? exit.humanTraffic : undefined"
        />
        <template v-else>
          <NodeInfoText
            row
            :text="locationText"
            :label="$t('nodeInfoLocation')"
          >
            <template #prefix>
              <CountryFlag
                v-if="exit.country"
                class="mt-0.5 shrink-0"
                :code="exit.country"
                :size="12"
              />
            </template>
          </NodeInfoText>
          <NodeInfoText
            row
            :text="[exit.asn, exit.asnOwner].filter(Boolean).join(' · ') || '—'"
            :label="$t('nodeInfoAsn')"
          />
          <div
            class="col-span-2 grid grid-cols-subgrid items-center gap-x-2"
            data-node-ip-type
          >
            <span class="opacity-60">{{ $t('nodeInfoIpType') }}</span>
            <div class="flex min-w-0 items-center gap-1 @max-[17.5rem]/node-info:contents">
              <NodeInfoText
                class="flex-1"
                :label="$t('nodeInfoIpType')"
                :text="ipTypeText"
              />
              <div
                class="node-ip-type-tabs tabs-box tabs tabs-xs shrink-0 flex-nowrap p-0.5 @max-[17.5rem]/node-info:col-span-2 @max-[17.5rem]/node-info:justify-self-end"
                role="group"
                :aria-label="$t('nodeInfoIpTypeSource')"
              >
                <button
                  v-for="source in IP_TYPE_SOURCES"
                  :key="source"
                  type="button"
                  class="tab h-5! min-w-0! cursor-pointer px-1.5! text-[10px]! whitespace-nowrap"
                  :class="{ 'tab-active': ipTypeSource === source }"
                  :aria-pressed="ipTypeSource === source"
                  @click="ipTypeSource = source"
                >
                  {{ source }}
                </button>
              </div>
            </div>
          </div>
          <NodeInfoText
            row
            :label="$t('nodeInfoNative')"
            :text="nativeText"
            :source="nativeSource"
          />
          <div
            v-if="partialText"
            class="col-span-2 break-all opacity-60"
          >
            {{ partialText }}
          </div>
          <NodeInfoText
            row
            :label="$t('nodeInfoSharing')"
            :text="sharingText"
            :text-class="sharingGrade?.color"
            :source="ping0Source"
            :source-href="ping0Href"
          />
        </template>
        <div
          v-if="riskOnly"
          class="col-span-2 mt-1 flex flex-wrap items-center justify-between gap-x-2 gap-y-1"
        >
          <a
            class="text-base-content/60 hover:text-base-content focus-visible:text-base-content inline-flex items-center gap-0.5 whitespace-nowrap underline-offset-2 hover:underline"
            :href="ping0Href"
            target="_blank"
            rel="noopener noreferrer"
          >
            {{ $t('nodeInfoReportLink', { provider: 'Ping0.cc' }) }}
            <ArrowTopRightOnSquareIcon
              class="size-3 shrink-0"
              aria-hidden="true"
            />
          </a>
          <a
            class="text-base-content/60 hover:text-base-content focus-visible:text-base-content inline-flex items-center gap-0.5 whitespace-nowrap underline-offset-2 hover:underline"
            :href="netCoffeeHref"
            target="_blank"
            rel="noopener noreferrer"
          >
            {{ $t('nodeInfoReportLink', { provider: 'Net.Coffee' }) }}
            <ArrowTopRightOnSquareIcon
              class="size-3 shrink-0"
              aria-hidden="true"
            />
          </a>
        </div>
      </template>
    </template>
  </div>
</template>

<script setup lang="ts">
import {
  fetchNodeEntryCountry,
  fetchNodeInfo,
  fetchNodeSharing,
  type OpenboxNodeInfo,
  type OpenboxNodeSharing,
} from '@/api/openbox'
import CountryFlag from '@/components/common/CountryFlag.vue'
import NodeInfoText from '@/components/proxies/NodeInfoText.vue'
import NodeRiskPanel from '@/components/proxies/NodeRiskPanel.vue'
import { useNodeExit, type NodeExitState } from '@/composables/nodeExit'
import { countryName, findCountry } from '@/constant/countries'
import { showNotification } from '@/helper/notification'
import { ArrowPathIcon, ArrowTopRightOnSquareIcon } from '@heroicons/vue/24/outline'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  name: string
  // 只显示出口的风险评估(浮层的「风险评估」页签)和两家的完整报告链接
  riskOnly?: boolean
  // 压成一小块(质量卡):riskOnly 时是威胁结论 + 信任评分条,否则是入口 / 出口 IP
  compact?: boolean
  udp?: boolean
  // 外面已经有这个节点的出口查询状态就传进来共用,不再自己查
  exitState?: NodeExitState
}>()
const { t, locale } = useI18n()

const info = ref<OpenboxNodeInfo | null>(null)
const infoError = ref('')
const entryCountry = ref('')
const { exit, error, loading, load } = props.exitState || useNodeExit(props.name)

// 信任评分条:满分是绿色,分数越低越往红色靠
const trustColor = computed(() =>
  exit.value?.trustScore === 100
    ? 'var(--color-success)'
    : `color-mix(in oklab, var(--color-error) ${100 - (exit.value?.trustScore ?? 0)}%, var(--color-success))`,
)

const riskAssessment = computed(() =>
  exit.value?.source === 'ip.net.coffee' ? exit.value.riskAssessment : undefined,
)
// 压缩版风险评估只有一行威胁结论
const threatsCell = computed(() => {
  const threats = riskAssessment.value?.threats
  if (!threats) return { value: t('nodeRiskUnknown'), color: 'opacity-60' }
  if (!threats.length) return { value: t('nodeRiskNoThreats'), color: 'text-success' }
  return {
    value: threats.map((v) => v.label).join(' · '),
    color: threats.some((v) => v.severity === 'bad') ? 'text-red-500' : 'text-orange-500',
  }
})

const errorText = (err: unknown) => (err instanceof Error ? err.message : String(err))

const entryIps = computed(() =>
  info.value ? [...info.value.addresses.v4, ...info.value.addresses.v6] : [],
)

const infoLoading = ref(false)
// force = true 跳过服务端缓存重新解析入口
const loadInfo = async (force = false) => {
  if (infoLoading.value) return
  infoLoading.value = true
  infoError.value = ''
  try {
    info.value = await fetchNodeInfo(props.name, force)
    entryCountry.value = info.value.entryCountry
    // 服务端缓存里没有入口 IP 的国家时再单独问一次(要查库,比解析慢,所以不卡着上面那几行)
    if (entryCountry.value || !entryIps.value.length) return
    const snapshot = info.value
    void fetchNodeEntryCountry(props.name)
      .then(({ country }) => {
        if (info.value === snapshot) entryCountry.value = country
      })
      .catch(() => {
        // 查不到就只是不显示国旗
      })
  } catch (err) {
    infoError.value = errorText(err)
  } finally {
    infoLoading.value = false
  }
}
// 入口信息和出口(风险评估)只要有一个在查就算忙
const busy = computed(() => infoLoading.value || loading.value)
// 手动刷新:入口信息(风险评估页签不查)和出口都跳过缓存重查,查完提示一次;失败时保留上一次的结果
const refresh = async () => {
  if (busy.value) return
  await Promise.all([props.riskOnly ? Promise.resolve() : loadInfo(true), load(true)])
  const message = (!props.riskOnly && infoError.value) || error.value
  showNotification(
    message
      ? { content: 'nodeInfoRefreshFailed', params: { message }, type: 'alert-error' }
      : { content: 'nodeInfoRefreshComplete', type: 'alert-success' },
  )
}
defineExpose({ refresh, loading: busy })

onMounted(() => {
  if (!props.riskOnly) void loadInfo()
  if (!exit.value) void load()
})
watch(
  () => [props.riskOnly, props.compact],
  ([riskOnly]) => {
    if (!riskOnly && !info.value) void loadInfo()
  },
)

// Ping0.cc 的共享人数 / 风控值 / IP 类型:出口 IP 查到后再问(出口刷新过就跳过缓存重查),压缩版不显示所以不查
const ping0 = ref<OpenboxNodeSharing | null>(null)
const ping0Loading = ref(false)
watch(
  () => (props.compact ? null : exit.value),
  (current, previous, onCleanup) => {
    if (current?.ip !== previous?.ip) ping0.value = null
    ping0Loading.value = !!current?.ip
    if (!current?.ip) return
    const controller = new AbortController()
    onCleanup(() => controller.abort())
    const refreshed = current.ip === previous?.ip && current.at !== previous.at
    fetchNodeSharing(current.ip, refreshed, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) ping0.value = result
      })
      .catch(() => {
        // 查不到(含后端没有这个接口)就显示「未知」
      })
      .finally(() => {
        if (!controller.signal.aborted) ping0Loading.value = false
      })
  },
  { immediate: true },
)

// 国家代码 → 当前语言的名字:表里有就用表里的,没有再让浏览器按语言翻
const countryLabel = (code: string) => {
  if (!code) return ''
  const known = findCountry(code)
  if (known) return countryName(known, locale.value)
  try {
    return new Intl.DisplayNames([locale.value], { type: 'region' }).of(code.toUpperCase()) || code
  } catch {
    return code
  }
}

const serverText = computed(() => {
  if (!info.value) return ''
  const host = info.value.server.includes(':') ? `[${info.value.server}]` : info.value.server
  return info.value.port ? `${host}:${info.value.port}` : host
})

const entryV4Text = computed(
  () => info.value?.addresses.v4.join(' · ') || info.value?.resolveError || t('nodeInfoUnknown'),
)

const exitV4 = computed(() => (exit.value?.ip && !exit.value.ip.includes(':') ? exit.value.ip : ''))
// 类型:出口 IPv4 就是入口 IPv4 → 直连,否则是中转;两边有一边还没查到就显示未知
const connectTypeText = computed(() => {
  const v4 = info.value?.addresses.v4
  if (!v4?.length || !exitV4.value) return t('nodeInfoUnknown')
  return t(v4.includes(exitV4.value) ? 'direct' : 'nodeInfoRelay')
})
const exitV6 = computed(
  () => exit.value?.ipv6 || (exit.value?.ip.includes(':') ? exit.value.ip : ''),
)

// 标签列宽度:中文短、英文长,两种语言下值都对得齐
const labelWidth = computed(() => (locale.value.startsWith('zh') ? '6.5rem' : '7.75rem'))

const locationText = computed(() => {
  if (!exit.value) return ''
  const first = exit.value.location.split(' — ')[0].trim()
  return locale.value.startsWith('zh') && first
    ? first
    : countryLabel(exit.value.country) || first || '—'
})

const IP_TYPE_KEYS: Record<string, string> = {
  residential: 'ipTypeResidential',
  mobile: 'ipTypeMobile',
  business: 'ipTypeBusiness',
  hosting: 'ipTypeHosting',
}

// ---- Ping0.cc ----
const ping0Href = computed(() =>
  exit.value?.ip ? `https://ping0.cc/ip/${encodeURIComponent(exit.value.ip)}` : undefined,
)
const netCoffeeHref = computed(() =>
  exit.value?.ip ? `https://ip.net.coffee/ip/${encodeURIComponent(exit.value.ip)}` : undefined,
)
const ping0Range = computed(() => ping0.value?.range ?? '')
const ping0Risk = computed(() => ping0.value?.risk ?? null)
// Ping0 要人工验证时,来源处变成「去验证」链接
const ping0Challenge = computed(() => !ping0Loading.value && ping0.value?.error === 'challenge')
const ping0Status = computed(() =>
  ping0Loading.value
    ? t('nodeInfoChecking')
    : t(ping0Challenge.value ? 'nodePing0Challenge' : 'nodeRiskUnknown'),
)
const ping0Source = computed(() => (ping0Challenge.value ? t('nodePing0VerifyLink') : 'Ping0.cc'))

const SHARING_GRADES: Record<string, { grade: string; color: string }> = {
  '1-10': { grade: 'nodeInfoSharingExcellent', color: 'text-success' },
  '10-100': { grade: 'nodeInfoSharingTypical', color: 'text-base-content' },
  '100-1000': { grade: 'nodeInfoSharingRisk', color: 'text-warning' },
  '1000-10000': { grade: 'nodeInfoSharingHigh', color: 'text-error' },
  '10000+': { grade: 'nodeRiskExtreme', color: 'text-error' },
}
const sharingGrade = computed(() => SHARING_GRADES[ping0Range.value])
const sharingText = computed(() =>
  sharingGrade.value && !ping0Challenge.value
    ? t('nodeInfoSharingValue', {
        range: ping0Range.value.replace('-', '–'),
        grade: t(sharingGrade.value.grade),
      })
    : ping0Status.value,
)

// IP 类型可以在两家之间切换看:默认 Ping0.cc,没查到就显示它的状态
const IP_TYPE_SOURCES = ['Ping0.cc', 'Net.Coffee']
const ipTypeSource = ref('Ping0.cc')
const ipTypeText = computed(() => {
  if (ipTypeSource.value === 'Ping0.cc')
    return ping0.value?.ipTypes?.join(' · ') || ping0Status.value
  const e = exit.value
  if (!e || !e.ipType) return '—'
  return [t(IP_TYPE_KEYS[e.ipType] ?? e.ipType), e.proxyType].filter(Boolean).join(' · ')
})

const nativeText = computed(() => {
  const e = exit.value
  if (!e || e.native == null) return t('nodeInfoUnknown')
  return e.native ? t('nativeYes') : t('nativeNo')
})
const nativeSource = computed(() => {
  const country = exit.value?.registeredCountry
  return country
    ? t('nativeRegistered', { country: countryLabel(country) })
    : t('nodeInfoSourceRegistry')
})

// 查询没成功时在底部提一句,已经查到的照常显示
const partialText = computed(() =>
  exit.value?.errors?.netcoffee ? t('nodeInfoPartial', { what: 'Net.Coffee' }) : '',
)
</script>
