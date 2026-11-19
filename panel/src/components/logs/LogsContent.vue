<template>
  <div class="flex size-full min-h-0 flex-col overflow-hidden">
    <VirtualScroller
      class="min-h-0 flex-1"
      :style="virtualScrollerStyle"
      :data="rows"
      :size="isMiddleScreen ? 120 : 96"
      :get-item-key="getItemKey"
    >
      <template v-slot="{ item }: { item: LogRow }">
        <LogsCard
          :row="item"
          @rename="renameDevice"
        ></LogsCard>
      </template>
    </VirtualScroller>
    <ConnectionDeviceRenameDialog v-model="renameTarget" />
    <ConnectionTargetRoutingDialog v-model="routingTarget" />
  </div>
</template>

<script setup lang="ts">
import VirtualScroller from '@/components/common/VirtualScroller.vue'
import ConnectionDeviceRenameDialog from '@/components/connections/ConnectionDeviceRenameDialog.vue'
import ConnectionTargetRoutingDialog from '@/components/connections/ConnectionTargetRoutingDialog.vue'
import { OPEN_TARGET_ROUTING } from '@/components/connections/targetRoutingKey'
import LogsCard from '@/components/logs/LogsCard.vue'
import { useLogRows, type LogRow } from '@/composables/logRows'
import { usePaddingForViews } from '@/composables/paddingViews'
import type { DeviceRenameTarget } from '@/helper/sourceip'
import { isMiddleScreen } from '@/helper/utils'
import { computed, provide, ref } from 'vue'

// 列表里每一条外面已经包了一层 8px(VirtualScroller 的 app-card-padding),这里把公共
// 逻辑给的 8px 安全边距抵掉,第一条卡片离工具栏正好 8px,和其它页面一致
const { paddingTop } = usePaddingForViews({
  offsetTop: -8,
  offsetBottom: 0,
})
const virtualScrollerStyle = computed(() => ({
  paddingTop: `${paddingTop.value}px`,
}))

const { rows } = useLogRows()
// 新日志从最前面插入,用稳定的键让已经量好的行高跟着日志走
const getItemKey = (index: number) => rows.value[index]?.key ?? index

const renameTarget = ref<DeviceRenameTarget | null>(null)
const routingTarget = ref('')
provide(OPEN_TARGET_ROUTING, (target: string) => {
  routingTarget.value = target
})
const renameDevice = (row: LogRow) => {
  if (row.sourceAddress) {
    renameTarget.value = { ip: row.sourceAddress, hostname: row.sourceLabel }
  }
}
</script>
