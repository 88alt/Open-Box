<template>
  <div :class="className.list">
    <div
      v-for="stat in order"
      :key="stat"
      :class="className.item"
    >
      <div
        :class="className.label"
        v-tip="stat === STATISTICS_TYPE.ACTIVE_TERMINALS ? $t('statActiveTerminalsHint') : ''"
      >
        {{ $t(labelKey(stat)) }}
      </div>
      <div :class="className.value">{{ statisticsMap[stat] }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { STATISTICS_TYPE, statisticsMap } from '@/composables/statistics'
import { useSystemResources } from '@/composables/systemResources'
import { computed } from 'vue'

const props = defineProps<{
  type: 'overview' | 'ctrl' | 'settings'
}>()

const classMap = {
  overview: {
    list: 'grid grid-flow-col grid-cols-3 grid-rows-2 app-card-gap rounded-xl border border-base-300/60 bg-base-100 app-card-inset lg:grid-flow-row lg:grid-cols-6 lg:grid-rows-1',
    item: 'flex h-12 flex-col items-center justify-center lg:gap-0.5',
    label: 'text-xs text-base-content/70',
    value: 'text-lg lg:text-xl font-bold',
  },
  settings: {
    list: 'app-card-gap grid w-full grid-cols-3 rounded-lg bg-base-200/50 app-card-inset',
    item: 'flex flex-col items-start',
    label: 'text-xs text-base-content/70',
    value: 'text-sm',
  },
  ctrl: {
    // 侧边栏那张卡片自己就是容器(CommonCtrl.vue,card app-card-inset = 12px 内边距),这里不再套底色也不再加内边距
    list: 'app-card-gap grid w-full grid-cols-2',
    item: 'flex items-start flex-col',
    label: 'text-xs text-base-content/70',
    value: 'text-sm',
  },
}

const orderMap = {
  overview: [
    STATISTICS_TYPE.CONNECTIONS,
    STATISTICS_TYPE.ACTIVE_TERMINALS,
    STATISTICS_TYPE.DL_SPEED,
    STATISTICS_TYPE.DOWNLOAD,
    STATISTICS_TYPE.UL_SPEED,
    STATISTICS_TYPE.UPLOAD,
  ],
  settings: [
    STATISTICS_TYPE.CONNECTIONS,
    STATISTICS_TYPE.DOWNLOAD,
    STATISTICS_TYPE.DL_SPEED,
    STATISTICS_TYPE.MEMORY_USAGE,
    STATISTICS_TYPE.UPLOAD,
    STATISTICS_TYPE.UL_SPEED,
  ],
  ctrl: [
    STATISTICS_TYPE.CONNECTIONS,
    STATISTICS_TYPE.ACTIVE_TERMINALS,
    STATISTICS_TYPE.DL_SPEED,
    STATISTICS_TYPE.DOWNLOAD,
    STATISTICS_TYPE.UL_SPEED,
    STATISTICS_TYPE.UPLOAD,
  ],
}

// 「活跃终端」来自系统资源快照;设置页那一份不显示它,不用开轮询
if (props.type !== 'settings') useSystemResources()

const className = computed(() => classMap[props.type])
const order = computed(() => orderMap[props.type])

// 概览和侧边栏用「连接数 / 进站流量 / 出站流量」这几个标签;设置页保持原样
const LABEL_KEY: Record<string, string> = {
  [STATISTICS_TYPE.CONNECTIONS]: 'statConnections',
  [STATISTICS_TYPE.DOWNLOAD]: 'statDownload',
  [STATISTICS_TYPE.UPLOAD]: 'statUpload',
}
const labelKey = (stat: string) => (props.type === 'settings' ? stat : (LABEL_KEY[stat] ?? stat))
</script>
