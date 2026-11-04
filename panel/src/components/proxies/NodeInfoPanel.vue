<template>
  <!-- 节点信息:上半是节点自己(协议 / 服务器 / 入口 IP / IPv6 / UDP),下半是经这个节点出去的出口
       (出口 IPv4 / IPv6 / 属地 / ASN / IP 类型 / 信任评分 / 是否原生)。riskOnly 只显示出口的风险评估;
       compact 压成一行(质量卡用)。两半各自查、各自失败。 -->
  <div
    :class="
      compact
        ? 'flex w-full min-w-0 items-center gap-2 text-xs leading-4'
        : 'node-info-pane grid h-full content-start gap-x-2 gap-y-1 overflow-y-auto pr-1 text-xs leading-4'
    "
    :style="compact ? undefined : { gridTemplateColumns: `minmax(${labelWidth}, max-content) minmax(0, 1fr)` }"
  >
    <template v-if="compact">
      <div
        v-if="riskOnly"
        class="flex w-full min-w-0 items-center gap-2"
        :aria-busy="loading || undefined"
        :title="error || $t('nodeInfoTrust')"
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
      <template v-else>
        <div
          class="flex min-w-0 flex-1 items-center gap-1"
          data-quality-entry
        >
          <span class="shrink-0 opacity-60">{{ $t('qualityEntry') }}</span>
          <span
            class="min-w-0 truncate tabular-nums"
            :title="infoError || info?.resolveError || entryIps.join(' · ')"
          >
            {{ entryIps[0] || '—' }}
          </span>
        </div>
        <div
          class="flex min-w-0 flex-1 items-center justify-end gap-1"
          data-quality-exit
        >
          <span class="shrink-0 opacity-60">{{ $t('qualityExit') }}</span>
          <span
            class="min-w-0 truncate tabular-nums"
            :title="error || exit?.ip"
          >
            {{ exit?.ip || '—' }}
          </span>
        </div>
      </template>
    </template>
    <template v-else>
      <template v-if="!riskOnly">
        <div
          v-if="infoError"
          class="col-span-2 break-all opacity-80"
        >
          {{ infoError }}
        </div>
        <template v-else-if="info">
          <NodeInfoText
            row
            :text="info.protocol.join(' · ')"
            :label="$t('nodeInfoProtocol')"
          />
          <NodeInfoText
            row
            :text="serverText"
            :label="$t('nodeInfoServer')"
          />
          <NodeInfoText
            v-if="info.detour"
            row
            :text="info.detour"
            :label="$t('nodeInfoUpstream')"
          />
          <NodeInfoText
            row
            :text="entryV4Text"
            :label="$t('nodeInfoEntryIp')"
          >
            <template #prefix>
              <CountryFlag
                v-if="entryCountry && info.addresses.v4.length"
                class="mt-0.5 shrink-0"
                :code="entryCountry"
                :size="12"
              />
            </template>
          </NodeInfoText>
          <NodeInfoText
            row
            :text="info.addresses.v6.join(' · ') || $t('nodeInfoNoIpv6')"
            :label="$t('nodeInfoEntryIpv6')"
          />
          <NodeInfoText
            row
            :text="udp === undefined ? $t('nodeInfoUnknown') : udp ? $t('nodeInfoSupported') : $t('nodeInfoUnsupported')"
            label="UDP"
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
        <NodeInfoText
          row
          class="tabular-nums"
          :text="exitV4 || $t('nodeInfoIpv4Unverified')"
          :label="$t('nodeInfoExitIp')"
        />
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
          type="button"
          class="flex shrink-0 cursor-pointer items-center opacity-70 hover:opacity-100"
          :disabled="busy"
          :title="$t('refresh')"
          @click="refresh"
        >
          <ArrowPathIcon
            class="h-3.5 w-3.5"
            :class="busy && 'animate-spin'"
          />
        </button>
      </div>
      <template v-else-if="exit">
        <NodeRiskPanel
          v-if="riskOnly"
          :assessment="exit.source === 'ip.net.coffee' ? exit.riskAssessment : undefined"
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
          <NodeInfoText
            v-for="row in exitRows"
            :key="row.key"
            row
            :label="row.label"
            :text="row.value"
            :source="row.source"
            :title="row.title"
            :text-style="row.key === 'trust' ? { color: trustColor } : undefined"
          >
            <template #prefix>
              <progress
                v-if="row.key === 'trust' && exit.trustScore != null"
                class="progress mt-1 h-1.5 w-12 shrink-0"
                :style="{ color: trustColor }"
                :value="exit.trustScore"
                max="100"
                :aria-label="$t('nodeInfoTrust')"
              ></progress>
            </template>
          </NodeInfoText>
          <div
            v-if="partialText"
            class="col-span-2 break-all opacity-60"
          >
            {{ partialText }}
          </div>
        </template>
        <div
          v-if="riskOnly && !hideFooter"
          class="col-span-2 mt-1 flex flex-wrap items-center justify-between gap-2 opacity-60"
        >
          <span class="flex items-center gap-1 whitespace-nowrap tabular-nums">
            {{ $t('nodeInfoCheckedAt', { time: dayjs(exit.at).format('MM-DD HH:mm') }) }}
            <button
              type="button"
              class="flex cursor-pointer items-center hover:opacity-100"
              :disabled="busy"
              :title="$t('refresh')"
              @click="refresh"
            >
              <ArrowPathIcon
                class="h-3 w-3"
                :class="busy && 'animate-spin'"
              />
            </button>
          </span>
          <a
            class="flex items-center gap-0.5 whitespace-nowrap underline-offset-2 hover:underline hover:opacity-100"
            :href="`https://ip.net.coffee/ip/${encodeURIComponent(exit.ip)}`"
            target="_blank"
            rel="noopener noreferrer"
          >
            {{ $t('nodeInfoCoffeeReport') }}
            <ArrowTopRightOnSquareIcon class="h-3 w-3" />
          </a>
        </div>
      </template>
    </template>
  </div>
</template>

<script setup lang="ts">
import { fetchNodeEntryCountry, fetchNodeInfo, type OpenboxNodeInfo } from '@/api/openbox'
import CountryFlag from '@/components/common/CountryFlag.vue'
import NodeInfoText from '@/components/proxies/NodeInfoText.vue'
import NodeRiskPanel from '@/components/proxies/NodeRiskPanel.vue'
import { useNodeExit, type NodeExitState } from '@/composables/nodeExit'
import { countryName, findCountry } from '@/constant/countries'
import { showNotification } from '@/helper/notification'
import { ArrowPathIcon, ArrowTopRightOnSquareIcon } from '@heroicons/vue/24/outline'
import dayjs from 'dayjs'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  name: string
  // 只显示出口的风险评估(浮层的「风险评估」页签)
  riskOnly?: boolean
  // 压成一行(质量卡):riskOnly 时是信任评分条,否则是入口 / 出口 IP
  compact?: boolean
  // riskOnly 下不要底部的「查询时间 / 完整报告」
  hideFooter?: boolean
  udp?: boolean
  // 外面已经有这个节点的出口查询状态就传进来共用,不再自己查
  exitState?: NodeExitState
}>()
const { t, locale } = useI18n()

const info = ref<OpenboxNodeInfo | null>(null)
const infoError = ref('')
const entryCountry = ref('')
const { exit, error, loading, load } = props.exitState || useNodeExit(props.name)

const trustColor = computed(
  () => `color-mix(in oklab, var(--color-error) ${100 - (exit.value?.trustScore ?? 0)}%, var(--color-primary))`,
)

const errorText = (err: unknown) => (err instanceof Error ? err.message : String(err))

const entryIps = computed(() => (info.value ? [...info.value.addresses.v4, ...info.value.addresses.v6] : []))

const infoLoading = ref(false)
// force = true 跳过服务端缓存重新解析入口
const loadInfo = async (force = false) => {
  infoLoading.value = true
  infoError.value = ''
  try {
    try {
      info.value = await fetchNodeInfo(props.name, force)
    } catch (err) {
      infoError.value = errorText(err)
      return
    }
    if (props.compact) return
    entryCountry.value = info.value.entryCountry
    // 服务端缓存里没有入口 IP 的国家时再单独问一次(要查库,比解析慢,所以不卡着上面那几行)
    if (entryCountry.value || !entryIps.value.length) return
    try {
      entryCountry.value = (await fetchNodeEntryCountry(props.name)).country
    } catch {
      // 查不到就只是不显示国旗
    }
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

const entryV4Text = computed(() => {
  const v4 = info.value?.addresses.v4 || []
  return v4.length ? [v4.join(' · '), countryLabel(entryCountry.value)].filter(Boolean).join(' · ') : info.value?.resolveError || t('nodeInfoUnknown')
})

const exitV4 = computed(() => (exit.value?.ip && !exit.value.ip.includes(':') ? exit.value.ip : ''))
const exitV6 = computed(() => exit.value?.ipv6 || (exit.value?.ip.includes(':') ? exit.value.ip : ''))

// 标签列宽度:中文短、英文长,两种语言下值都对得齐
const labelWidth = computed(() => (locale.value.startsWith('zh') ? '4.25rem' : '5.25rem'))

const locationText = computed(() => {
  if (!exit.value) return ''
  const first = exit.value.location.split(' — ')[0].trim()
  return locale.value.startsWith('zh') && first ? first : countryLabel(exit.value.country) || first || '—'
})

const IP_TYPE_KEYS: Record<string, string> = {
  residential: 'ipTypeResidential',
  mobile: 'ipTypeMobile',
  business: 'ipTypeBusiness',
  hosting: 'ipTypeHosting',
}
const ipTypeText = computed(() => {
  const e = exit.value
  if (!e || !e.ipType) return '—'
  return [t(IP_TYPE_KEYS[e.ipType] ?? e.ipType), e.proxyType].filter(Boolean).join(' · ')
})

const nativeText = computed(() => {
  const e = exit.value
  if (!e || e.native == null) return t('nodeInfoUnknown')
  return e.native ? t('nativeYes') : t('nativeNo')
})

const NET_COFFEE = 'Net.Coffee'
// 每一行右边的小字标出这个值是谁查的
const exitRows = computed(() => {
  const e = exit.value
  if (!e) return []
  return [
    { key: 'type', label: t('nodeInfoIpType'), value: ipTypeText.value, source: NET_COFFEE, title: '' },
    {
      key: 'trust',
      label: t('nodeInfoTrust'),
      value: e.trustScore == null ? '—' : `${e.trustScore} / 100`,
      source: NET_COFFEE,
      title: '',
    },
    {
      key: 'native',
      label: t('nodeInfoNative'),
      value: nativeText.value,
      source: e.registeredCountry ? t('nativeRegistered', { country: countryLabel(e.registeredCountry) }) : t('nodeInfoSourceRegistry'),
      title: e.source || t('nodeInfoNativeHint'),
    },
  ]
})

// 查询没成功时在底部提一句,已经查到的照常显示
const partialText = computed(() => (exit.value?.errors?.netcoffee ? t('nodeInfoPartial', { what: NET_COFFEE }) : ''))
</script>
