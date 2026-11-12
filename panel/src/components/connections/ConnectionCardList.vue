<template>
  <div class="@container flex min-h-0 flex-1 flex-col overflow-hidden">
    <VirtualScroller
      class="min-h-0 flex-1"
      :style="virtualScrollerStyle"
      :data="connectionGroupItems"
      :size="size"
      :get-item-key="getItemKey"
      :item-spacing="itemSpacing"
    >
      <template v-slot="{ item, index }: { item: ConnectionGroupItem; index: number }">
        <div
          v-if="item.kind !== 'connection'"
          class="card app-card-inset hover:bg-base-200 grid w-full cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-2 px-3 py-2"
          :class="{
            'rounded-b-none!': connectionGroupItems[index + 1]?.sectionKey === item.sectionKey,
            'rounded-t-none!': item.kind === 'subgroup',
            'connection-group-heading': item.kind === 'group',
          }"
          @click="toggleConnectionGroupItem(item)"
        >
          <div class="flex min-w-0 items-center gap-2">
            <component
              :is="item.expanded ? ChevronDownIcon : ChevronRightIcon"
              aria-hidden="true"
              class="size-4 shrink-0"
            />
            <div class="flex min-w-0 flex-col items-start gap-1">
              <div class="flex max-w-full min-w-0 items-center gap-2">
                <ProxyName
                  v-if="item.kind === 'subgroup' && item.mode === 'routing' && item.group.key"
                  :name="item.group.key"
                />
                <button
                  v-else
                  type="button"
                  class="min-w-0 cursor-pointer truncate text-start text-sm font-semibold"
                  :aria-expanded="item.expanded"
                  :title="groupLabel(item)"
                  @click.stop="toggleConnectionGroupItem(item)"
                >
                  {{ groupLabel(item) }}
                </button>
                <ConnectionDeviceRenameButton
                  v-if="item.group.deviceIP"
                  @rename="emit('renameDevice', item.group)"
                />
                <ConnectionTargetRoutingButton :target="item.group.target" />
              </div>
              <span class="text-base-content/65 block max-w-full truncate text-xs">
                <template v-if="item.group.sourceIP">{{ item.group.sourceIP }} · </template>
                {{ $t('connectionGroupCount', { count: item.group.connections.length }) }}
              </span>
            </div>
          </div>
          <div
            v-if="item.kind === 'group' && item.expanded"
            class="connection-group-tabs order-last col-span-2 min-w-0 overflow-x-auto text-xs"
          >
            <ConnectionSubgroupTabs
              :group-id="item.key"
              :mode="item.subgroupMode"
            />
          </div>
          <span
            class="connection-group-metrics flex shrink-0 flex-col items-end gap-1 whitespace-nowrap tabular-nums"
          >
            <span class="flex gap-2 text-xs">
              <span class="text-success flex items-center gap-1">
                <ArrowDownCircleIcon class="size-4" />
                {{ prettyBytesHelper(item.group.downloadSpeed) }}/s
              </span>
              <span class="text-info flex items-center gap-1">
                <ArrowUpCircleIcon class="size-4" />
                {{ prettyBytesHelper(item.group.uploadSpeed) }}/s
              </span>
            </span>
            <span class="text-base-content/65 flex gap-2 text-xs">
              <span>{{ prettyBytesHelper(item.group.download) }}</span>
              <span>{{ prettyBytesHelper(item.group.upload) }}</span>
            </span>
          </span>
        </div>
        <div
          v-else
          class="card overflow-hidden rounded-t-none!"
          :class="{
            'rounded-b-none!': connectionGroupItems[index + 1]?.sectionKey === item.sectionKey,
          }"
        >
          <div
            aria-hidden="true"
            class="connection-card-divider border-base-content/10 mx-[var(--app-card-space)] border-t"
          />
          <ConnectionCard :conn="item.connection" />
        </div>
      </template>
    </VirtualScroller>
  </div>
</template>

<script setup lang="ts">
import { usePaddingForViews } from '@/composables/paddingViews'
import { isMiddleScreen, prettyBytesHelper } from '@/helper/utils'
import {
  connectionGroupItems,
  toggleConnectionGroupItem,
  type ConnectionGroup,
  type ConnectionGroupItem,
} from '@/store/connectionGroups'
import { connectionCardLines } from '@/store/settings'
import {
  ArrowDownCircleIcon,
  ArrowUpCircleIcon,
  ChevronDownIcon,
  ChevronRightIcon,
} from '@heroicons/vue/24/outline'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import VirtualScroller from '../common/VirtualScroller.vue'
import ProxyName from '../proxies/ProxyName.vue'
import ConnectionCard from './ConnectionCard'
import ConnectionDeviceRenameButton from './ConnectionDeviceRenameButton.vue'
import ConnectionTargetRoutingButton from './ConnectionTargetRoutingButton.vue'
import ConnectionSubgroupTabs from './ConnectionSubgroupTabs.vue'

const emit = defineEmits<{ renameDevice: [group: ConnectionGroup] }>()

const { t } = useI18n()
const { paddingTop } = usePaddingForViews({
  offsetTop: -8,
  offsetBottom: 0,
})
const virtualScrollerStyle = computed(() => ({
  paddingTop: `${paddingTop.value}px`,
}))

// 一张连接卡片的高度;组头比它矮一点,量出真实高度之后以量出来的为准
const size = computed(() => {
  return connectionCardLines.value.length * 24 - 4 + (isMiddleScreen.value ? 24 : 32)
})

const getItemKey = (index: number) => connectionGroupItems.value[index]?.key ?? index
// 同一个组里的组头和卡片贴在一起,组与组之间才留卡片间距
const itemSpacing = (index: number) => {
  const items = connectionGroupItems.value

  return items[index]?.sectionKey === items[index + 1]?.sectionKey ? '0px' : 'var(--app-card-space)'
}
const groupLabel = (item: Exclude<ConnectionGroupItem, { kind: 'connection' }>) =>
  item.group.label || t('connectionRoutingUnmatched')
</script>
