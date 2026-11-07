<template>
  <!-- 侧边栏「硬件负载」卡片:CPU 总负载(多核时下面分两列列出每核),内存三项(空余 / 已用 / Open-Box)和一条分段进度条 -->
  <div
    class="app-card-inset app-card-gap card flex shrink-0 flex-col"
    data-system-resources
  >
    <div class="app-card-gap flex flex-col">
      <div class="flex flex-col">
        <div class="flex items-center justify-between text-xs">
          <span class="text-base-content/70">{{ $t('systemCpuTotal') }}</span>
          <span
            class="tabular-nums"
            :class="cpuLoadClass(systemResources?.cpu.usage)"
            >{{ percent(systemResources?.cpu.usage) }}</span
          >
        </div>
        <div
          v-if="hasMultipleCores"
          class="grid grid-cols-2 gap-x-[var(--app-card-space)]"
        >
          <div
            v-for="core in cpuCores"
            :key="core.id"
            class="flex min-w-0 items-center justify-between text-xs"
          >
            <span class="text-base-content/70">{{ core.id.toUpperCase() }}</span>
            <span
              class="tabular-nums"
              :class="cpuLoadClass(core.usage)"
              >{{ percent(core.usage) }}</span
            >
          </div>
        </div>
      </div>
      <div
        class="app-card-gap flex flex-col"
        :title="$t('systemMemoryHint')"
      >
        <div class="app-card-gap grid grid-cols-3">
          <div
            v-for="item in memoryItems"
            :key="item.label"
            class="min-w-0"
          >
            <div class="text-base-content/70 text-xs">{{ $t(item.label) }}</div>
            <div class="text-sm tabular-nums">{{ bytes(item.value) }}</div>
          </div>
        </div>
        <div
          class="bg-base-300 rounded-field flex h-1.5 overflow-hidden"
          role="img"
          :aria-label="memoryItems.map(({ label, value }) => `${$t(label)}: ${bytes(value)}`).join(', ')"
          data-memory-segments
        >
          <span
            class="bg-success"
            :style="{ width: `${segments.available}%` }"
          />
          <span
            class="bg-warning"
            :style="{ width: `${segments.other}%` }"
          />
          <span
            class="bg-info"
            :style="{ width: `${segments.openbox}%` }"
          />
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import {
  cpuCores,
  cpuLoadClass,
  hasMultipleCores,
  systemResources,
  useSystemResources,
} from '@/composables/systemResources'
import { prettyBytesHelper } from '@/helper/utils'
import { computed } from 'vue'

useSystemResources()

const memoryItems = computed(() => [
  { label: 'systemMemoryRemaining', value: systemResources.value?.memory?.available },
  { label: 'systemMemoryUsedFull', value: systemResources.value?.memory?.used },
  { label: 'systemMemoryOpenbox', value: systemResources.value?.memory?.openbox },
])

const percent = (value?: number | null) => (value == null ? '—' : `${value.toFixed(1)}%`)
const bytes = (value?: number | null) =>
  value == null ? '—' : prettyBytesHelper(value, { binary: true })

// 进度条三段:绿 = 空余,橙 = 其他已用(已用 − Open-Box),蓝 = Open-Box 常驻内存
const segments = computed(() => {
  const memory = systemResources.value?.memory
  if (!memory?.total) return { available: 0, other: 0, openbox: 0 }
  const openbox = Math.max(0, Math.min(memory.used, memory.openbox ?? 0))
  return {
    available: (memory.available / memory.total) * 100,
    other: ((memory.used - openbox) / memory.total) * 100,
    openbox: (openbox / memory.total) * 100,
  }
})
</script>
