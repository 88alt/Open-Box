<template>
  <!-- 节点质量卡里的历史柱状图:最近若干次延时 / 带宽,柱数随宽度变化,右侧显示最新值 -->
  <div
    class="flex h-10 w-full min-w-0 items-center gap-3"
    :data-quality-chart="kind"
  >
    <span
      ref="host"
      class="flex h-[34px] min-w-0 flex-1 items-end justify-start gap-[3px]"
    >
      <span
        v-for="(s, i) in slots"
        :key="i"
        v-tip="s ? tipText(s) : undefined"
        class="detection-bar relative h-full w-[6px] shrink-0"
        :class="!s && 'bg-base-content/10'"
        :data-chart-slot="s ? 'filled' : 'empty'"
      >
        <span
          v-if="s"
          class="detection-bar absolute inset-x-0 bottom-0"
          :class="barClass(s)"
          :style="{ height: barHeight(s) + '%' }"
        ></span>
      </span>
    </span>
    <span class="flex max-w-[55%] shrink-0 flex-col">
      <span class="text-base-content/60 mb-1 truncate text-xs">
        {{ $t(kind === 'latency' ? 'nodeTipLatency' : 'nodeTipBandwidth') }}
      </span>
      <span class="leading-none">
        <span
          v-if="!last"
          class="text-base-content/40 text-xl leading-none tabular-nums"
        >—</span>
        <span
          v-else-if="last.failed"
          class="text-xl leading-none font-semibold text-red-500"
        >
          <span class="text-base leading-none">{{ $t('qualityState_down') }}</span>
        </span>
        <span
          v-else
          class="text-xl leading-none font-semibold tabular-nums"
          :class="kind === 'latency' ? getColorForLatency(last.value) : getColorForBandwidth(last.value)"
        >
          {{ fmt(last.value) }}
          <span class="text-base-content/50 ml-1 text-xs leading-none font-normal">{{ unit }}</span>
        </span>
      </span>
    </span>
  </div>
</template>

<script setup lang="ts">
import type { OpenboxQualitySample } from '@/api/openbox'
import { getBgColorForBandwidth, getColorForBandwidth, getColorForLatency } from '@/helper'
import { speedtestTimeout } from '@/store/settings'
import dayjs from 'dayjs'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  kind: 'latency' | 'bandwidth'
  samples: OpenboxQualitySample[]
}>()
const { t } = useI18n()
const host = ref<HTMLElement>()
const width = ref(0)
let ro: ResizeObserver | undefined

// 每根柱 6px + 间隙 3px
const count = computed(() => (width.value > 0 ? Math.max(1, Math.floor((width.value - 1 + 3) / 9)) : 10))
const shown = computed(() => props.samples.slice(-count.value))
const slots = computed(() => Array.from({ length: count.value }, (_, i) => shown.value[i]))
const last = computed(() => props.samples[props.samples.length - 1])
const unit = computed(() => (props.kind === 'latency' ? 'ms' : 'Mbps'))

// 延时:失败按超时值算;带宽:取最大值。0 以下不参与
const ceiling = computed(() => {
  if (props.kind === 'latency') {
    const timeout = Number(speedtestTimeout.value)
    const fallback = Number.isFinite(timeout) && timeout > 0 ? timeout : 5000
    return Math.max(1, ...shown.value.map((s) => (s.failed ? 0 : s.value)).map((v) => (v === 0 ? fallback : v)).filter(Number.isFinite))
  }
  return Math.max(1, ...shown.value.filter((s) => !s.failed).map((s) => s.value).filter((v) => v > 0))
})
const fmt = (v: number) => (props.kind === 'latency' ? Math.round(v).toString() : v.toFixed(1))
const barHeight = (s: OpenboxQualitySample) =>
  s.failed ? 100 : Number.isFinite(s.value) ? Math.min(100, Math.max(0, (s.value / ceiling.value) * 100)) : 0
const textToBg = (c: string) =>
  c === 'text-low-latency' ? 'bg-low-latency' : c === 'text-medium-latency' ? 'bg-medium-latency' : c === 'text-high-latency' ? 'bg-high-latency' : 'bg-base-content/20'
const barClass = (s: OpenboxQualitySample) =>
  s.failed ? 'bg-high-latency' : props.kind === 'latency' ? textToBg(getColorForLatency(s.value)) : getBgColorForBandwidth(s.value)
const tipText = (s: OpenboxQualitySample) =>
  `${dayjs(s.at).format('MM-DD HH:mm:ss')} · ${s.failed ? t('qualityState_down') : fmt(s.value) + ' ' + unit.value}`

onMounted(() => {
  if (!host.value) return
  ro = new ResizeObserver(([e]) => {
    width.value = e?.contentRect.width || 0
  })
  ro.observe(host.value)
})
onBeforeUnmount(() => ro?.disconnect())
</script>
