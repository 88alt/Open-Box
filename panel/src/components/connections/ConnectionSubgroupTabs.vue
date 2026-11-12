<template>
  <!-- 展开的组里再分一层:不分 / 按分流 / 按另一个维度。点页签不触发外面的展开 / 收起 -->
  <div
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
      @click="setConnectionSubgroupMode(groupId, item)"
    >
      {{ $t(LABELS[item]) }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { CONNECTION_GROUP_MODE, type CONNECTION_SUBGROUP_MODE } from '@/constant'
import { setConnectionSubgroupMode } from '@/store/connectionGroups'
import { connectionGroupMode } from '@/store/connections'
import { computed } from 'vue'

defineProps<{ groupId: string; mode: CONNECTION_SUBGROUP_MODE }>()

const LABELS: Record<CONNECTION_SUBGROUP_MODE, string> = {
  none: 'connectionSubgroupNone',
  routing: 'connectionSubgroupRouting',
  [CONNECTION_GROUP_MODE.TARGET]: 'connectionSubgroupTarget',
  [CONNECTION_GROUP_MODE.DEVICE]: 'connectionSubgroupDevice',
}
const items = computed<CONNECTION_SUBGROUP_MODE[]>(() => [
  'none',
  'routing',
  connectionGroupMode.value === CONNECTION_GROUP_MODE.DEVICE
    ? CONNECTION_GROUP_MODE.TARGET
    : CONNECTION_GROUP_MODE.DEVICE,
])
</script>
