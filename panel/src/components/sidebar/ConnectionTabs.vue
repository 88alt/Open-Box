<template>
  <!-- 连接页的分组方式:不分组 / 按终端 / 按目标(和上游 v0.1.488 一致,页签上不带数字) -->
  <div
    v-segmented="{ content: false }"
    class="connection-tabs tabs-box tabs tabs-xs flex-nowrap"
    role="tablist"
  >
    <button
      v-for="mode in modes"
      :key="mode"
      type="button"
      role="tab"
      :class="
        twMerge(
          'tab whitespace-nowrap max-md:min-w-0 max-md:px-1.5',
          connectionGroupMode === mode && 'tab-active',
          !horizental && 'flex-1',
        )
      "
      :aria-selected="connectionGroupMode === mode"
      @click="() => (connectionGroupMode = mode)"
    >
      {{ $t(mode === 'none' ? 'connectionSubgroupNone' : mode) }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { CONNECTION_GROUP_MODE, type CONNECTION_VIEW_MODE } from '@/constant'
import { connectionGroupMode } from '@/store/connections'
import { twMerge } from 'tailwind-merge'

defineProps({
  horizental: {
    type: Boolean,
    default: true,
  },
})
const modes: CONNECTION_VIEW_MODE[] = ['none', ...Object.values(CONNECTION_GROUP_MODE)]
</script>
