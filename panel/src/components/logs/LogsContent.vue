<template>
  <div class="flex size-full min-h-0 flex-col overflow-hidden">
    <VirtualScroller
      class="min-h-0 flex-1"
      :style="virtualScrollerStyle"
      :data="renderLogs"
      :size="isMiddleScreen ? 96 : 64"
    >
      <template v-slot="{ item }: { item: LogWithSeq }">
        <LogsCard :log="item"></LogsCard>
      </template>
    </VirtualScroller>
  </div>
</template>

<script setup lang="ts">
import VirtualScroller from '@/components/common/VirtualScroller.vue'
import LogsCard from '@/components/logs/LogsCard.vue'
import { usePaddingForViews } from '@/composables/paddingViews'
import { isMiddleScreen } from '@/helper/utils'
import { logSeverityLabel } from '@/helper/logSeverity'
import { logFilter, logLevelFilter, logs } from '@/store/logs'
import type { LogWithSeq } from '@/types'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

// 列表里每一条外面已经包了一层 8px(VirtualScroller 的 app-card-padding),这里把公共
// 逻辑给的 8px 安全边距抵掉,第一条卡片离工具栏正好 8px,和其它页面一致
const { paddingTop } = usePaddingForViews({
  offsetTop: -8,
  offsetBottom: 0,
})
const virtualScrollerStyle = computed(() => ({
  paddingTop: `${paddingTop.value}px`,
}))

const renderLogs = computed(() => {
  const keyword = logFilter.value
  const level = logLevelFilter.value

  if (!keyword && !level) {
    return logs.value
  }

  // 输入的不是合法正则时,退回成不区分大小写的普通文本匹配
  let regex: RegExp | undefined

  try {
    regex = keyword ? new RegExp(keyword, 'i') : undefined
  } catch {
    regex = undefined
  }

  const lowerKeyword = keyword.toLowerCase()
  const isMatch = (text: string) =>
    regex ? regex.test(text) : text.toLowerCase().includes(lowerKeyword)

  return logs.value.filter((log) => {
    if (level && log.type !== level) {
      return false
    }

    if (!keyword) {
      return true
    }

    return [log.rawPayload, log.payload, log.time, log.type, logSeverityLabel(log.type, t)].some(
      isMatch,
    )
  })
})
</script>
