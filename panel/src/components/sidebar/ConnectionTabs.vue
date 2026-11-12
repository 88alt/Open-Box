<template>
  <!-- 连接页的分组方式:按终端 / 按目标。页签上的数字是当前分组方式下的组数 -->
  <div class="connection-tabs tabs-box tabs tabs-xs flex-nowrap">
    <a
      v-for="mode in Object.values(CONNECTION_GROUP_MODE)"
      :key="mode"
      role="tab"
      :class="
        twMerge(
          'tab whitespace-nowrap max-md:min-w-0 max-md:px-1.5',
          connectionGroupMode === mode && 'tab-active',
          !horizental && 'flex-1',
        )
      "
      @click="() => (connectionGroupMode = mode)"
      >{{ $t(mode) }}
      <template v-if="connectionGroupMode === mode">({{ connectionsCount }})</template>
    </a>
  </div>
</template>

<script setup lang="ts">
import { CONNECTION_GROUP_MODE } from '@/constant'
import { connectionGroupCount } from '@/store/connectionGroups'
import { connectionGroupMode, connections, renderConnections } from '@/store/connections'
import { twMerge } from 'tailwind-merge'
import { computed } from 'vue'

defineProps({
  horizental: {
    type: Boolean,
    default: true,
  },
})
// 组数 / 连接数:筛选掉了一部分连接时写成 筛选后 / 全部
const connectionsCount = computed(() => {
  if (renderConnections.value.length !== connections.value.length) {
    return `${connectionGroupCount.value} (${renderConnections.value.length} / ${connections.value.length})`
  }

  return `${connectionGroupCount.value} · ${connections.value.length}`
})
</script>
