<template>
  <div class="card bg-base-100 border-base-300/60 border">
    <div class="card-body gap-3 p-4">
      <div class="flex items-center gap-2">
        <h2 class="text-base font-semibold">{{ $t('bandwidthSettingsTitle') }}</h2>
        <HelpTrigger
          :label="$t('bandwidthSettingsHelpLabel', { title: $t('bandwidthSettingsTitle') })"
          @click="openHelp($event, 'bandwidthSettingsTitle')"
        />
      </div>

      <div class="flex flex-wrap items-end gap-4 text-sm">
        <div class="flex flex-col gap-1">
          <label
            for="bandwidth-test-period"
            class="text-xs font-medium"
          >
            {{ $t('bandwidthPeriod') }}
          </label>
          <NumberStepper
            id="bandwidth-test-period"
            v-model="period"
            :label="$t('bandwidthPeriod')"
            :min="unit === 'days' ? MIN_DAYS : MIN_HOURS"
            :max="unit === 'days' ? MAX_HOURS / 24 : MAX_HOURS"
            :input-step="unit === 'days' ? 'any' : 1"
            :disabled="saving"
            @change="saveBandwidth"
          >
            <template #unit>
              <select
                :value="unit"
                :aria-label="$t('bandwidthPeriodUnit')"
                class="select select-sm join-item w-auto"
                :disabled="saving"
                @change="changeUnit"
              >
                <option value="hours">{{ $t('bandwidthHours') }}</option>
                <option value="days">{{ $t('bandwidthDays') }}</option>
              </select>
            </template>
          </NumberStepper>
        </div>
        <div
          v-if="unit === 'days'"
          class="flex flex-col gap-1"
        >
          <label
            for="bandwidth-start-hour"
            class="text-xs font-medium"
          >
            {{ $t('bandwidthStartHour') }}
          </label>
          <select
            id="bandwidth-start-hour"
            v-model.number="startHour"
            :aria-label="$t('bandwidthStartHour')"
            class="select select-sm w-24 tabular-nums"
            :disabled="saving"
            @change="saveBandwidth"
          >
            <option
              v-for="h in 24"
              :key="h - 1"
              :value="h - 1"
            >
              {{ String(h - 1).padStart(2, '0') }}:00
            </option>
          </select>
        </div>
        <div class="flex flex-col gap-1">
          <label
            for="bandwidth-download-cap"
            class="text-xs font-medium"
          >
            {{ $t('bandwidthDownloadCap') }}
          </label>
          <NumberStepper
            id="bandwidth-download-cap"
            v-model="maxDownloadMB"
            :label="$t('bandwidthDownloadCap')"
            unit="MB"
            :min="MIN_MB"
            :max="MAX_MB"
            integer
            :disabled="saving"
            @change="saveBandwidth"
          />
        </div>
        <!-- 上游 v0.1.493:测速站点预设。不同站点的带宽样本彼此隔离,切换后按新站点重新累计 -->
        <div class="flex flex-col gap-1">
          <label
            for="bandwidth-endpoint"
            class="text-xs font-medium"
          >
            {{ $t('bandwidthEndpoint') }}
          </label>
          <select
            id="bandwidth-endpoint"
            v-model="endpoint"
            class="select select-sm w-auto"
            :disabled="saving"
            @change="saveBandwidth"
          >
            <option value="cloudflare">{{ $t('bandwidthEndpointCloudflare') }}</option>
            <option value="apple">{{ $t('bandwidthEndpointApple') }}</option>
            <option value="cachefly">{{ $t('bandwidthEndpointCachefly') }}</option>
            <option value="custom">{{ $t('bandwidthEndpointCustom') }}</option>
          </select>
          <input
            v-if="endpoint === 'custom'"
            v-model.trim="customUrl"
            type="url"
            class="input input-sm w-full max-w-md"
            :placeholder="$t('bandwidthCustomUrlPlaceholder')"
            :aria-label="$t('bandwidthCustomUrl')"
            :disabled="saving"
            @change="saveBandwidth"
          >
          <p class="text-base-content/60 text-xs">{{ $t('bandwidthEndpointHint') }}</p>
        </div>
      </div>

      <p class="text-base-content/70 text-xs leading-relaxed">
        {{ $t('bandwidthDaily', { count: nodeCount, times: timesPerDay, size: dailyText }) }}
      </p>

      <form
        class="border-base-300 flex flex-col gap-2 border-t pt-3"
        @submit.prevent="saveWeights"
      >
        <div class="flex items-center gap-2">
          <h2 class="text-base font-semibold">{{ $t('nodeScoreWeights') }}</h2>
          <HelpTrigger
            :label="$t('bandwidthSettingsHelpLabel', { title: $t('nodeScoreWeights') })"
            @click="openHelp($event, 'nodeScoreWeights')"
          />
        </div>
        <div class="flex w-full max-w-2xl items-center gap-2 sm:gap-4">
          <NodeScoreWeightSlider
            v-model="weights"
            :disabled="savingWeights"
          />
          <button
            type="submit"
            class="btn btn-sm shrink-0"
            :disabled="savingWeights || !weightsDirty"
          >
            {{ $t('save') }}
          </button>
        </div>
      </form>
    </div>
  </div>

  <BandwidthSettingsHelpDialog
    v-model="helpOpen"
    :section="helpSection"
    :profile="profile"
  />
</template>

<script setup lang="ts">
import {
  fetchBandwidthStatus,
  type OpenboxBandwidthEndpoint,
  type OpenboxBandwidthSettings,
  type OpenboxNodeScoreWeights,
  type OpenboxProfile,
} from '@/api/openbox'
import HelpTrigger from '@/components/common/HelpTrigger.vue'
import NumberStepper from '@/components/common/NumberStepper.vue'
import { showNotification } from '@/helper/notification'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BandwidthSettingsHelpDialog from './BandwidthSettingsHelpDialog.vue'
import NodeScoreWeightSlider from './NodeScoreWeightSlider.vue'

// 范围和后端一致:周期 1 小时 ~ 30 天(界面可以按小时或天输入,存的始终是小时),单次下载上限 1~1024 MB
const MIN_HOURS = 1
const MAX_HOURS = 720
// 1 小时折成天取三位小数是 0.042,下限按这个值放行,保存时再折回整小时
const MIN_DAYS = 0.042
const DEFAULT_HOURS = 12
const MIN_MB = 1
const MAX_MB = 1024
const DEFAULT_MB = 64
// 按天测试时的默认开始时刻(与后端 DefaultStartHour 一致)
const DEFAULT_START_HOUR = 4
const DEFAULT_WEIGHTS: OpenboxNodeScoreWeights = { latency: 40, download: 30, jitter: 30 }

const props = defineProps<{
  profile: OpenboxProfile
  patchProfile: (patch: Record<string, unknown>) => Promise<OpenboxProfile>
}>()

const { t } = useI18n()
type HelpSection = 'bandwidthSettingsTitle' | 'nodeScoreWeights'

type Unit = 'hours' | 'days'
const toDisplay = (hours: number, unit: Unit) =>
  unit === 'days' ? Number((hours / 24).toFixed(3)) : hours

// 当前档案里生效的值:档案没写过 bandwidth 时,以后端状态接口报的默认值为准,免得界面和后端各说各的
const status = ref<{ nodes: number; settings?: OpenboxBandwidthSettings } | null>(null)
const effective = computed<OpenboxBandwidthSettings>(
  () =>
    props.profile.bandwidth ??
    status.value?.settings ?? {
      intervalHours: DEFAULT_HOURS,
      intervalUnit: 'hours',
      maxDownloadMB: DEFAULT_MB,
      startHour: DEFAULT_START_HOUR,
    },
)

const unit = ref<Unit>(effective.value.intervalUnit ?? 'hours')
const period = ref<number>(toDisplay(effective.value.intervalHours, unit.value))
const maxDownloadMB = ref<number>(effective.value.maxDownloadMB)
const startHour = ref<number>(effective.value.startHour ?? DEFAULT_START_HOUR)
// 上游 v0.1.493:测速站点(cloudflare / apple / cachefly / custom)与自定义地址
const endpoint = ref<OpenboxBandwidthEndpoint>(effective.value.endpoint ?? 'cloudflare')
const customUrl = ref<string>(effective.value.endpointCustomUrl ?? '')
const saving = ref(false)
const nodeCount = computed(() => status.value?.nodes ?? 0)

watch(effective, (s) => {
  unit.value = s.intervalUnit ?? 'hours'
  period.value = toDisplay(s.intervalHours, unit.value)
  maxDownloadMB.value = s.maxDownloadMB
  startHour.value = s.startHour ?? DEFAULT_START_HOUR
  endpoint.value = s.endpoint ?? 'cloudflare'
  customUrl.value = s.endpointCustomUrl ?? ''
})

const hoursNow = () => Math.round(Number(period.value) * (unit.value === 'days' ? 24 : 1))

// 单个节点一天最多测几次:周期不到一天就是 24 / 周期 向上取整,一天以上最多 1 次;每日下载量上限 = 节点数 × 单次上限 × 次数
const timesPerDay = computed(() => {
  const hours = hoursNow()
  return Number.isFinite(hours) && hours >= 1 ? Math.max(1, Math.ceil(24 / hours)) : 0
})
const dailyText = computed(
  () =>
    `${((nodeCount.value * Math.max(MIN_MB, Number(maxDownloadMB.value) || DEFAULT_MB) * timesPerDay.value) / 1e3).toFixed(2)} GB`,
)

onMounted(() => {
  fetchBandwidthStatus()
    .then((s) => {
      status.value = s
    })
    .catch(() => {})
})

const resetBandwidthFields = () => {
  unit.value = effective.value.intervalUnit ?? 'hours'
  period.value = toDisplay(effective.value.intervalHours, unit.value)
  maxDownloadMB.value = effective.value.maxDownloadMB
  startHour.value = effective.value.startHour ?? DEFAULT_START_HOUR
}

const saveBandwidth = async () => {
  const hours = hoursNow()
  const cap = Math.round(Number(maxDownloadMB.value))
  if (!Number.isFinite(hours) || hours < MIN_HOURS) {
    showNotification({ content: t('bandwidthPeriodInvalid'), type: 'alert-error' })
    resetBandwidthFields()
    return
  }
  const intervalHours = Math.min(MAX_HOURS, hours)
  const cappedMB = Number.isFinite(cap) ? Math.max(MIN_MB, Math.min(MAX_MB, cap)) : DEFAULT_MB
  period.value = toDisplay(intervalHours, unit.value)
  maxDownloadMB.value = cappedMB
  // 选了自定义站点就必须填合法地址:前端先拦一道,免得存进去一个测不了的地址
  if (endpoint.value === 'custom') {
    const u = customUrl.value.trim()
    let ok = false
    try {
      const parsed = new URL(u)
      ok = parsed.protocol === 'https:' && !!parsed.hostname
    } catch {
      ok = false
    }
    if (!ok) {
      showNotification({ content: t('bandwidthCustomUrlInvalid'), type: 'alert-error' })
      resetBandwidthFields()
      return
    }
  }
  saving.value = true
  try {
    await props.patchProfile({
      bandwidth: {
        intervalHours,
        intervalUnit: unit.value,
        maxDownloadMB: cappedMB,
        startHour: Math.min(23, Math.max(0, Math.round(Number(startHour.value)) || 0)),
        // 自定义站点要带地址;非自定义时不带(后端按预设选地址)
        endpoint: endpoint.value,
        ...(endpoint.value === 'custom' ? { endpointCustomUrl: customUrl.value.trim() } : {}),
      },
    })
    showNotification({ content: 'bandwidthEndpointSaved', type: 'alert-success' })
  } catch (e) {
    resetBandwidthFields()
    showNotification({ content: (e as Error).message, type: 'alert-error' })
  } finally {
    saving.value = false
  }
}

// 换单位:显示值按当前周期折算,周期本身不变,并保存新的显示单位
const changeUnit = async (e: Event) => {
  const next = (e.target as HTMLSelectElement).value as Unit
  if (next === unit.value) return
  const hours = hoursNow()
  if (!Number.isFinite(hours) || hours < MIN_HOURS) {
    showNotification({ content: t('bandwidthPeriodInvalid'), type: 'alert-error' })
    ;(e.target as HTMLSelectElement).value = unit.value
    return
  }
  unit.value = next
  period.value = toDisplay(Math.min(MAX_HOURS, hours), next)
  await saveBandwidth()
}

// ---- 评分权重 ----
const savedWeights = computed<OpenboxNodeScoreWeights>(
  () => props.profile.nodeScoreWeights ?? DEFAULT_WEIGHTS,
)
const weights = ref<OpenboxNodeScoreWeights>({ ...savedWeights.value })
const savingWeights = ref(false)
const weightsDirty = computed(
  () =>
    weights.value.latency !== savedWeights.value.latency ||
    weights.value.download !== savedWeights.value.download ||
    weights.value.jitter !== savedWeights.value.jitter,
)

watch(
  () => [savedWeights.value.latency, savedWeights.value.download, savedWeights.value.jitter],
  () => {
    weights.value = { ...savedWeights.value }
  },
)

const saveWeights = async () => {
  if (savingWeights.value) return
  savingWeights.value = true
  try {
    await props.patchProfile({ nodeScoreWeights: { ...weights.value } })
  } catch (e) {
    showNotification({ content: (e as Error).message, type: 'alert-error' })
  } finally {
    savingWeights.value = false
  }
}

// ---- 说明弹窗 ----
const helpOpen = ref(false)
const helpSection = ref<HelpSection>('bandwidthSettingsTitle')
let helpTrigger: HTMLElement | null = null

const openHelp = (e: Event, section: HelpSection) => {
  helpTrigger = e.currentTarget as HTMLElement
  helpSection.value = section
  helpOpen.value = true
}
// 关弹窗后把焦点还给点开它的问号按钮
watch(helpOpen, (open) => {
  if (!open) nextTick(() => helpTrigger?.focus({ preventScroll: true }))
})
</script>
