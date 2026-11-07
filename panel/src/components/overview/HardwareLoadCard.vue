<template>
  <!-- 硬件负载:一张大卡,四张实时图(CPU 总负载 / 空余内存 / 已用内存 / Open-Box 常驻内存)。
       多核时 CPU 卡片整行可点,展开后下面多一排每核负载。 -->
  <section class="card w-full">
    <div class="card-body app-card-gap">
      <h2 class="text-base font-semibold">{{ $t('hardwareLoad') }}</h2>
      <div class="app-card-gap grid grid-cols-2 xl:grid-cols-4">
        <div
          v-for="(item, index) in cards"
          :key="item.label"
          class="app-overview-chart-card bg-base-100 border-base-300/60 app-card-inset flex flex-col rounded-xl border"
        >
          <button
            v-if="index === 0 && hasMultipleCores"
            type="button"
            class="rounded-field flex w-full items-center justify-between gap-1.5 text-left focus-visible:outline-2 focus-visible:outline-offset-2 enabled:cursor-pointer"
            :aria-expanded="coresOpen"
            :aria-controls="coresId"
            data-cpu-expand
            @click="coresOpen = !coresOpen"
          >
            <span class="text-base-content/60 inline-flex min-w-0 items-center gap-1 text-xs">
              {{ item.label }}
              <component
                :is="coresOpen ? ChevronUpIcon : ChevronDownIcon"
                aria-hidden="true"
                class="size-3 shrink-0"
              />
            </span>
            <span
              class="shrink-0 text-right text-lg font-bold whitespace-nowrap tabular-nums lg:text-xl"
              :class="item.currentColor"
              >{{ item.current }}</span
            >
          </button>
          <div
            v-else
            class="flex items-center justify-between gap-1.5"
          >
            <div class="text-base-content/60 text-xs">{{ item.label }}</div>
            <div
              class="shrink-0 text-right text-lg font-bold whitespace-nowrap tabular-nums lg:text-xl"
              :class="item.currentColor"
            >
              {{ item.current }}
            </div>
          </div>
          <div class="app-overview-chart-plot mt-[var(--app-card-space)]">
            <BasicCharts
              :data="item.series"
              :value-thresholds="item.thresholds"
              :min="item.scale"
              :label-formatter="item.axis"
              :tool-tip-formatter="item.tooltip"
              :legend="false"
              :pausable="false"
            />
          </div>
        </div>
      </div>
      <ExpandTransition>
        <div
          v-if="coresOpen && hasMultipleCores"
          :id="coresId"
          class="app-card-gap border-base-300/60 grid grid-cols-2 border-t pt-[var(--app-card-space)] xl:grid-cols-4"
          data-cpu-cores
        >
          <div
            v-for="core in coreCards"
            :key="core.id"
            class="app-overview-chart-card bg-base-100 border-base-300/60 app-card-inset flex flex-col rounded-xl border"
          >
            <div class="flex items-center justify-between gap-1.5">
              <div class="text-base-content/60 text-xs">{{ core.label }}</div>
              <div
                class="shrink-0 text-right text-lg font-bold whitespace-nowrap tabular-nums lg:text-xl"
                :class="core.currentColor"
              >
                {{ core.current }}
              </div>
            </div>
            <div class="app-overview-chart-plot mt-[var(--app-card-space)]">
              <BasicCharts
                :data="core.series"
                :value-thresholds="core.thresholds"
                :min="core.scale"
                :label-formatter="core.axis"
                :tool-tip-formatter="core.tooltip"
                :legend="false"
                :pausable="false"
              />
            </div>
          </div>
        </div>
      </ExpandTransition>
    </div>
  </section>
</template>

<script setup lang="ts">
import type { SystemResources } from '@/api/openbox'
import BasicCharts from '@/components/overview/BasicCharts.vue'
import ExpandTransition from '@/components/common/ExpandTransition.vue'
import {
  cpuCores,
  cpuLoadClass,
  hasMultipleCores,
  systemResources,
  systemResourcesHistory,
  useSystemResources,
} from '@/composables/systemResources'
import { prettyBytesHelper } from '@/helper/utils'
import { cpuLoadThresholds } from '@/store/settings'
import { ChevronDownIcon, ChevronUpIcon } from '@heroicons/vue/24/outline'
import dayjs from 'dayjs'
import { computed, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'

useSystemResources()

const { t } = useI18n()
const coresOpen = ref(false)
const coresId = useId()

type Tone = 'cpu' | 'warning' | 'info' | undefined

const percent = (value: number) => `${value.toFixed(1)}%`
const bytes = (value: number) => prettyBytesHelper(value, { binary: true })

// 一张图:当前值 + 历史曲线;pick 从快照里取这一项的值,取不到(没这个字段)就跳过那个点
const makeCard = (
  label: string,
  pick: (snapshot: SystemResources) => number | undefined,
  format: (value: number) => string,
  axis: (value: number) => string,
  scale: number,
  tone?: Tone,
) => {
  const snapshot = systemResources.value
  const current = snapshot ? pick(snapshot) : undefined
  const data = systemResourcesHistory.value.flatMap(({ at, snapshot: item }) => {
    const value = pick(item)
    return value == null ? [] : [{ name: at, value }]
  })
  return {
    label,
    current: current == null ? '—' : format(current),
    currentColor: tone === 'cpu' ? cpuLoadClass(current) : undefined,
    thresholds: tone === 'cpu' ? cpuLoadThresholds.value : undefined,
    scale,
    axis,
    series: [
      {
        name: label,
        data,
        tone: tone === 'cpu' ? undefined : tone,
        solidStroke: tone === 'warning' || tone === 'info',
      },
    ],
    tooltip: (params: ToolTipParams[]) =>
      params.map(({ data: item }) => `${dayjs(item.name).format('HH:mm:ss')} · ${format(item.value)}`).join('<br>'),
  }
}

const cpuCard = (label: string, pick: (snapshot: SystemResources) => number | undefined) =>
  makeCard(label, pick, percent, (value) => `${value}%`, 100, 'cpu')

const cards = computed(() => {
  const total = systemResources.value?.memory?.total ?? 0
  const memoryAxis = (value: number) => prettyBytesHelper(value, { binary: true, maximumFractionDigits: 0 })
  return [
    cpuCard(t('systemCpuTotal'), (snapshot) => snapshot.cpu.usage),
    makeCard(t('systemMemoryRemaining'), (snapshot) => snapshot.memory?.available, bytes, memoryAxis, total),
    makeCard(t('systemMemoryUsedFull'), (snapshot) => snapshot.memory?.used, bytes, memoryAxis, total, 'warning'),
    makeCard(t('systemMemoryOpenbox'), (snapshot) => snapshot.memory?.openbox, bytes, memoryAxis, total, 'info'),
  ]
})

const coreCards = computed(() =>
  cpuCores.value.map(({ id }) => ({
    id,
    ...cpuCard(
      t('systemCpuCoreLoad', { core: id.toUpperCase() }),
      (snapshot) => snapshot.cpu.cores.find((core) => core.id === id)?.usage,
    ),
  })),
)
</script>
