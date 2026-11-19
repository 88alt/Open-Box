<template>
  <div
    class="flex size-full min-h-0 flex-col overflow-hidden"
    :style="logLayout === 'table' ? { paddingBottom: `${paddingBottom}px` } : undefined"
  >
    <LogTable
      v-if="logLayout === 'table'"
      :rows="items"
      @toggle="toggle"
      @subgroup="onSubgroup"
      @open="(row) => (detailRow = row)"
      @rename="renameDevice"
    />
    <VirtualScroller
      v-else
      class="min-h-0 flex-1"
      :style="virtualScrollerStyle"
      :data="items"
      :size="62"
      :get-item-key="getItemKey"
      :item-spacing="itemSpacing"
    >
      <template v-slot="{ item, index }: { item: LogItem; index: number }">
        <div
          v-if="item.kind !== 'log'"
          class="card log-list-card cursor-pointer overflow-hidden"
          :class="{
            'rounded-t-none!': item.kind === 'subgroup',
            'rounded-b-none!': isJoined(index),
            'card-title-divider': item.kind === 'subgroup',
            'card-group-expanded': item.expanded,
            'hover:bg-base-200': !item.expanded,
          }"
          @click="toggle(item)"
        >
          <LogGroupHeading
            :row="item"
            class="app-card-inset"
            @toggle="toggle"
            @subgroup="onSubgroup"
            @rename="renameDevice"
          />
        </div>
        <LogsCard
          v-else
          :row="item"
          :joined-below="isJoined(index)"
          :follows-heading="items[index - 1]?.kind !== 'log'"
          @open="(row) => (detailRow = row)"
          @rename="renameDevice"
        />
      </template>
    </VirtualScroller>
    <ConnectionDeviceRenameDialog v-model="renameTarget" />
    <ConnectionTargetRoutingDialog v-model="routingTarget" />
    <LogSettingsDialog />
    <LogDetailDialog v-model="detailRow" />
  </div>
</template>

<script setup lang="ts">
import VirtualScroller from '@/components/common/VirtualScroller.vue'
import ConnectionDeviceRenameDialog from '@/components/connections/ConnectionDeviceRenameDialog.vue'
import ConnectionTargetRoutingDialog from '@/components/connections/ConnectionTargetRoutingDialog.vue'
import { OPEN_TARGET_ROUTING } from '@/components/connections/targetRoutingKey'
import LogDetailDialog from '@/components/logs/LogDetailDialog.vue'
import LogGroupHeading from '@/components/logs/LogGroupHeading.vue'
import LogSettingsDialog from '@/components/logs/LogSettingsDialog.vue'
import LogTable from '@/components/logs/LogTable.vue'
import LogsCard from '@/components/logs/LogsCard.vue'
import { useLogGroups, type LogItem } from '@/composables/logGroups'
import { useLogRows, type LogRow } from '@/composables/logRows'
import { usePaddingForViews } from '@/composables/paddingViews'
import type { DeviceRenameTarget } from '@/helper/sourceip'
import { logLayout, type LogSubgroupMode } from '@/store/logs'
import { computed, provide, ref } from 'vue'

// 列表里每一条外面已经包了一层 8px(VirtualScroller 的 app-card-padding),这里把公共
// 逻辑给的 8px 安全边距抵掉,第一条卡片离工具栏正好 8px,和其它页面一致
const { paddingTop, paddingBottom } = usePaddingForViews({
  offsetTop: -8,
  offsetBottom: -8,
})
const virtualScrollerStyle = computed(() => ({
  paddingTop: `${paddingTop.value}px`,
}))

const { rows } = useLogRows()
const { items, toggle, setSubgroupMode } = useLogGroups(rows)
// 新日志从最前面插入,用稳定的键让已经量好的行高跟着日志走
const getItemKey = (index: number) => items.value[index]?.key ?? index

const sectionOf = (item: LogItem | undefined) =>
  item?.kind === 'log' ? item.groupKey : item?.sectionKey
// 同一个展开的组里,上下相邻的两项之间不留缝
const isJoined = (index: number) => {
  const section = sectionOf(items.value[index])

  return section !== undefined && section === sectionOf(items.value[index + 1])
}
const itemSpacing = (index: number) => (isJoined(index) ? '0px' : 'var(--app-card-space)')
const onSubgroup = (key: string, mode: LogSubgroupMode) => setSubgroupMode(key, mode)

const detailRow = ref<LogRow | null>(null)
const renameTarget = ref<DeviceRenameTarget | null>(null)
const routingTarget = ref('')
provide(OPEN_TARGET_ROUTING, (target: string) => {
  routingTarget.value = target
})
const renameDevice = ({
  sourceAddress,
  sourceLabel,
}: {
  sourceAddress?: string
  sourceLabel?: string
}) => {
  if (sourceAddress) {
    renameTarget.value = { ip: sourceAddress, hostname: sourceLabel }
  }
}
</script>

<style>
/* 展开的组:组头、子组头和它们下面的日志卡片连成一块 */
#app-content .card.card-group-expanded {
  background-color: var(--color-base-200);
}
#app-content .log-list-card[class~='rounded-t-none!']:not(.card-title-divider) {
  border-top-width: 0;
}
</style>
