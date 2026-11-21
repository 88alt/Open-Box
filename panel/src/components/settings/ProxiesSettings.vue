<template>
  <div class="settings-section flex flex-col app-card-gap app-card-inset text-sm">
    <div class="settings-title">
      {{ $t('detectionThresholds') }}
    </div>
    <div class="settings-grid !gap-y-5">
      <div
        v-if="hasVisibleLatencyItems"
        class="flex min-w-0 flex-col gap-2"
      >
        <div class="text-sm font-normal">
          {{ $t('latencyThresholds') }}
        </div>
        <ThresholdRangeBar
          :label="$t('latencyThresholds')"
          :markers="latencyMarkers"
          :segments="latencySegments"
          :stops="latencyStops"
          unit="ms"
          integer
          min-label="0 ms"
          :max-label="`${latencyAxisMax} ms`"
          @update="onLatencyUpdate"
        />
      </div>
      <!-- 带宽着色阈值:不足(红)/一般(橙)可改,高于两者为绿 -->
      <div class="flex min-w-0 flex-col gap-2">
        <div class="text-sm font-normal">
          {{ $t('bandwidthThresholds') }}
        </div>
        <ThresholdRangeBar
          :label="$t('bandwidthThresholds')"
          :markers="bandwidthMarkers"
          :segments="bandwidthSegments"
          :stops="bandwidthStops"
          unit="Mbps"
          min-label="0 Mbps"
          :max-label="`${bandwidthAxisMax} Mbps`"
          @update="onBandwidthUpdate"
        />
      </div>
      <!-- CPU 负载着色阈值(硬件负载的曲线和数字按它变色):低于第一个为绿,低于第二个为橙,更高为红 -->
      <div
        v-if="isVisibleCpuLoadLow || isVisibleCpuLoadMedium"
        class="flex min-w-0 flex-col gap-2"
      >
        <div class="text-sm font-normal">
          {{ $t('cpuLoadThresholds') }}
        </div>
        <ThresholdRangeBar
          :label="$t('cpuLoadThresholds')"
          :markers="cpuMarkers"
          :segments="cpuSegments"
          :stops="cpuStops"
          unit="%"
          :unit-space="false"
          integer
          min-label="0%"
          max-label="100%"
          @update="onCpuUpdate"
        />
      </div>
      <div
        v-if="isVisibleIpv6Test"
        class="flex min-w-0 flex-col justify-center"
      >
        <div class="setting-item">
          <div class="setting-item-label">
            {{ $t('ipv6Test') }}
          </div>
          <input
            class="toggle"
            type="checkbox"
            v-model="IPv6test"
          />
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useIsSettingVisible } from '@/composables/settings'
import { PROXIES_ITEM_KEYS } from '@/config/settingsItems'
import {
  bandwidthAdequateThreshold,
  bandwidthPoorThreshold,
  cpuLoadThresholds,
  IPv6test,
  lowLatency,
  mediumLatency,
  setCpuLoadThreshold,
  speedtestTimeout,
} from '@/store/settings'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ThresholdRangeBar from './ThresholdRangeBar.vue'

const { t } = useI18n()
const k = PROXIES_ITEM_KEYS
const isVisibleSpeedtestTimeout = useIsSettingVisible(k.speedtestTimeout)
const isVisibleLowLatency = useIsSettingVisible(k.lowLatencyDesc)
const isVisibleMediumLatency = useIsSettingVisible(k.mediumLatencyDesc)
const isVisibleIpv6Test = useIsSettingVisible(k.ipv6Test)
const isVisibleCpuLoadLow = useIsSettingVisible(k.cpuLoadLowDesc)
const isVisibleCpuLoadMedium = useIsSettingVisible(k.cpuLoadMediumDesc)

const hasVisibleLatencyItems = computed(
  () => isVisibleLowLatency.value || isVisibleMediumLatency.value || isVisibleSpeedtestTimeout.value,
)

// ===== 延时:低 / 中 / 超时三个滑块,相邻至少差 50ms,超时 1000–30000ms =====
const LATENCY_GAP = 50
const TIMEOUT_MAX = 30000
const BANDWIDTH_MAX = 10000

const toInt = (value: unknown, fallback: number) =>
  Number.isFinite(Number(value)) ? Math.round(Number(value)) : fallback

const latency = computed(() => {
  const timeout = Math.min(TIMEOUT_MAX, Math.max(1000, toInt(speedtestTimeout.value, 5000)))
  const medium = Math.min(
    timeout - LATENCY_GAP,
    Math.max(LATENCY_GAP * 2, toInt(mediumLatency.value, 1000)),
  )
  const low = Math.min(medium - LATENCY_GAP, Math.max(LATENCY_GAP, toInt(lowLatency.value, 500)))
  return { low, medium, timeout }
})

const latencyAxisMax = computed(() => Math.max(10000, latency.value.timeout))
const latencyStops = computed(() => [
  { value: 0, percent: 0 },
  { value: 1000, percent: 50, step: 50 },
  { value: 3000, percent: 75, step: 100 },
  { value: latencyAxisMax.value, percent: 100, step: 500 },
])
const latencySegments = computed(() => [
  { key: 'good', label: t('latencyLowShort'), colorClass: 'bg-low-latency' },
  { key: 'moderate', label: t('latencyModerateShort'), colorClass: 'bg-medium-latency' },
  { key: 'severe', label: t('latencySevereShort'), colorClass: 'bg-high-latency' },
  {
    key: 'timeout',
    label: t('latencyTimeoutShort'),
    colorClass: 'bg-base-content/20',
    dotClass: 'bg-base-content/45',
  },
])
const latencyMarkers = computed(() => {
  const { low, medium, timeout } = latency.value
  return [
    {
      key: 'lowLatency',
      label: t('latencyModerateShort'),
      ariaLabel: t('lowLatencyDesc'),
      value: low,
      min: LATENCY_GAP,
      max: medium - LATENCY_GAP,
      step: 50,
      inputStep: 10,
      dotClass: 'bg-medium-latency',
    },
    {
      key: 'mediumLatency',
      label: t('latencySevereShort'),
      ariaLabel: t('mediumLatencyDesc'),
      value: medium,
      min: low + LATENCY_GAP,
      max: timeout - LATENCY_GAP,
      step: 50,
      inputStep: 10,
      dotClass: 'bg-high-latency',
    },
    {
      key: 'speedtestTimeout',
      label: t('latencyTimeoutShort'),
      ariaLabel: t('speedtestTimeout'),
      value: timeout,
      min: Math.max(1000, medium + LATENCY_GAP),
      max: TIMEOUT_MAX,
      step: 500,
      inputStep: 100,
      dotClass: 'bg-base-content/45',
    },
  ]
})
const onLatencyUpdate = (index: number, value: number) => {
  const { low, medium, timeout } = latency.value
  if (index === 0) {
    lowLatency.value = Math.min(medium - LATENCY_GAP, Math.max(LATENCY_GAP, Math.round(value)))
  } else if (index === 1) {
    mediumLatency.value = Math.min(timeout - LATENCY_GAP, Math.max(low + LATENCY_GAP, Math.round(value)))
  } else if (index === 2) {
    speedtestTimeout.value = Math.min(
      TIMEOUT_MAX,
      Math.max(Math.max(1000, medium + LATENCY_GAP), Math.round(value)),
    )
  }
}

// ===== 带宽:不足 / 一般两个滑块,高于一般为充足 =====
const bandwidth = computed(() => {
  const poor = Number.isFinite(Number(bandwidthPoorThreshold.value))
    ? Number(bandwidthPoorThreshold.value)
    : 5
  const adequateRaw = Number.isFinite(Number(bandwidthAdequateThreshold.value))
    ? Number(bandwidthAdequateThreshold.value)
    : 20
  const adequate = Math.min(BANDWIDTH_MAX, Math.max(0.5, adequateRaw))
  return { poor: Math.min(adequate, Math.max(0, poor)), adequate }
})
const bandwidthAxisMax = computed(() => Math.max(100, Math.ceil(bandwidth.value.adequate / 10) * 10))
const bandwidthStops = computed(() => [
  { value: 0, percent: 0 },
  { value: 10, percent: 45, step: 1 },
  { value: 30, percent: 75, step: 1 },
  { value: bandwidthAxisMax.value, percent: 100, step: 5 },
])
const bandwidthSegments = computed(() => [
  { key: 'poor', label: t('bandwidthPoorShort'), colorClass: 'bg-high-latency' },
  { key: 'adequate', label: t('bandwidthAdequateShort'), colorClass: 'bg-medium-latency' },
  { key: 'good', label: t('bandwidthGoodShort'), colorClass: 'bg-low-latency' },
])
const bandwidthMarkers = computed(() => {
  const { poor, adequate } = bandwidth.value
  return [
    {
      key: 'bandwidthPoor',
      label: t('bandwidthPoorShort'),
      ariaLabel: t('bandwidthPoorDesc'),
      value: poor,
      min: 0,
      max: adequate,
      step: 1,
      inputStep: 0.1,
      dotClass: 'bg-high-latency',
    },
    {
      key: 'bandwidthAdequate',
      label: t('bandwidthAdequateShort'),
      ariaLabel: t('bandwidthAdequateDesc'),
      value: adequate,
      min: poor,
      max: BANDWIDTH_MAX,
      step: 1,
      inputStep: 0.1,
      dotClass: 'bg-medium-latency',
    },
  ]
})
const onBandwidthUpdate = (index: number, value: number) => {
  const { poor, adequate } = bandwidth.value
  if (index === 0) {
    bandwidthPoorThreshold.value = Number(Math.min(adequate, Math.max(0, value)).toFixed(2))
  } else if (index === 1) {
    bandwidthAdequateThreshold.value = Number(
      Math.min(BANDWIDTH_MAX, Math.max(poor, value)).toFixed(2),
    )
  }
}

// ===== CPU 负载:低 / 中两个滑块(0 ≤ 低 < 中 ≤ 100) =====
const cpuStops = [
  { value: 0, percent: 0 },
  { value: 100, percent: 100, step: 1 },
]
const cpuSegments = computed(() => [
  { key: 'low', label: t('cpuLoadLowShort'), colorClass: 'bg-low-latency' },
  { key: 'medium', label: t('cpuLoadMediumShort'), colorClass: 'bg-medium-latency' },
  { key: 'high', label: t('cpuLoadHighShort'), colorClass: 'bg-high-latency' },
])
const cpuMarkers = computed(() => [
  {
    key: 'cpuLoadLow',
    label: t('cpuLoadLowShort'),
    ariaLabel: t('cpuLoadLowDesc'),
    value: cpuLoadThresholds.value[0],
    min: 0,
    max: cpuLoadThresholds.value[1] - 1,
    step: 1,
    dotClass: 'bg-low-latency',
  },
  {
    key: 'cpuLoadMedium',
    label: t('cpuLoadMediumShort'),
    ariaLabel: t('cpuLoadMediumDesc'),
    value: cpuLoadThresholds.value[1],
    min: cpuLoadThresholds.value[0] + 1,
    max: 100,
    step: 1,
    dotClass: 'bg-medium-latency',
  },
])
const onCpuUpdate = (index: number, value: number) => {
  if (index === 0 || index === 1) setCpuLoadThreshold(index, value)
}
</script>
