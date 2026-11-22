<template>
  <!-- 展开的组里再分一层:不分 / 按分流 / 按另一个维度。点页签不触发外面的展开 / 收起 -->
  <div
    v-segmented="{ content: false }"
    class="tabs-box tabs tabs-xs w-max flex-nowrap"
    role="tablist"
    :aria-label="$t('connectionSubgroup')"
    @click.stop
  >
    <button
      v-for="item in items"
      :key="item"
      type="button"
      role="tab"
      class="tab whitespace-nowrap max-md:min-w-0 max-md:px-1.5"
      :class="{ 'tab-active': mode === item }"
      :aria-selected="mode === item"
      @click="emit('change', item)"
    >
      {{ $t(LABELS[item]) }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { CONNECTION_GROUP_MODE } from '@/constant'
import type { LogGroupMode, LogSubgroupMode } from '@/store/logs'
import { computed } from 'vue'

const props = defineProps<{ outerMode: LogGroupMode; mode: LogSubgroupMode }>()
const emit = defineEmits<{ change: [mode: LogSubgroupMode] }>()

const LABELS: Record<LogSubgroupMode, string> = {
  none: 'connectionSubgroupNone',
  routing: 'connectionSubgroupRouting',
  [CONNECTION_GROUP_MODE.TARGET]: 'connectionSubgroupTarget',
  [CONNECTION_GROUP_MODE.DEVICE]: 'connectionSubgroupDevice',
}
const items = computed<LogSubgroupMode[]>(() => [
  'none',
  'routing',
  props.outerMode === CONNECTION_GROUP_MODE.DEVICE
    ? CONNECTION_GROUP_MODE.TARGET
    : CONNECTION_GROUP_MODE.DEVICE,
])
</script>
