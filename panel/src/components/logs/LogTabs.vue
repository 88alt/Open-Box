<template>
  <!-- 日志的分组方式:不分组 / 按终端 / 按目标 -->
  <div
    class="connection-tabs tabs-box tabs tabs-xs flex-nowrap"
    role="tablist"
    :aria-label="$t('logGrouping')"
  >
    <button
      v-for="item in items"
      :key="item"
      type="button"
      role="tab"
      class="tab whitespace-nowrap max-md:min-w-0 max-md:px-1.5"
      :class="{ 'tab-active': logGroupMode === item }"
      :aria-selected="logGroupMode === item"
      @click="logGroupMode = item"
    >
      {{ $t(item === 'none' ? 'logUngrouped' : item) }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { CONNECTION_GROUP_MODE } from '@/constant'
import { logGroupMode, type LogGroupMode } from '@/store/logs'

const items: LogGroupMode[] = ['none', ...Object.values(CONNECTION_GROUP_MODE)]
</script>
