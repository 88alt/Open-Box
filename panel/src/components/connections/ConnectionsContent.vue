<template>
  <div
    class="relative flex size-full min-h-0 flex-col overflow-hidden"
    :style="!useConnectionCard && padding"
  >
    <template v-if="useConnectionCard">
      <ConnectionCardList @rename-device="renameDevice" />
    </template>
    <template v-else>
      <ConnectionTable @rename-device="renameDevice" />
    </template>
    <ConnectionDetails />
    <ConnectionDeviceRenameDialog v-model="renameTarget" />
    <ConnectionTargetRoutingDialog v-model="routingTarget" />
  </div>
</template>

<script setup lang="ts">
import ConnectionCardList from '@/components/connections/ConnectionCardList.vue'
import ConnectionDetails from '@/components/connections/ConnectionDetails.vue'
import ConnectionDeviceRenameDialog from '@/components/connections/ConnectionDeviceRenameDialog.vue'
import ConnectionTable from '@/components/connections/ConnectionTable.vue'
import ConnectionTargetRoutingDialog from '@/components/connections/ConnectionTargetRoutingDialog.vue'
import { OPEN_TARGET_ROUTING } from '@/components/connections/targetRoutingKey'
import { usePaddingForViews } from '@/composables/paddingViews'
import type { DeviceRenameTarget } from '@/helper/sourceip'
import type { ConnectionGroup } from '@/store/connectionGroups'
import { useConnectionCard } from '@/store/settings'
import { provide, ref } from 'vue'

const renameTarget = ref<DeviceRenameTarget | null>(null)
const routingTarget = ref('')
provide(OPEN_TARGET_ROUTING, (target: string) => {
  routingTarget.value = target
})
const renameDevice = (group: ConnectionGroup) => {
  if (group.deviceIP) renameTarget.value = { ip: group.deviceIP }
}

// 表格外面已经包了一层 8px(ConnectionTable 的 app-card-padding),把公共逻辑给的
// 8px 安全边距抵掉,表格离工具栏 / 底部导航正好 8px,和其它页面一致
const { padding } = usePaddingForViews({
  offsetTop: -8,
  offsetBottom: -8,
})
</script>

<style>
.vjs-tree {
  font-family:
    NotoEmoji,
    Monaco,
    Menlo,
    Consolas,
    Bitstream Vera Sans Mono,
    monospace;
}
.vjs-tree-node.is-highlight,
.vjs-tree-node:hover {
  background-color: var(--color-base-200);
}
</style>
