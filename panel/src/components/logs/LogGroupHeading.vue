<template>
  <div
    class="grid w-full min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-2 text-start text-sm"
    :class="{ 'sm:grid-cols-[minmax(0,16rem)_minmax(0,1fr)_auto]': row.subgroupMode }"
  >
    <div class="flex min-w-0 items-center gap-2">
      <button
        type="button"
        class="shrink-0 cursor-pointer"
        :aria-expanded="row.expanded"
        :aria-label="
          [row.label, row.address, $t('logCount', { count: row.count })].filter(Boolean).join(' ')
        "
        @click.stop="emit('toggle', row)"
      >
        <component
          :is="row.expanded ? ChevronDownIcon : ChevronRightIcon"
          class="size-4"
          aria-hidden="true"
        />
      </button>
      <ProxyName
        v-if="row.kind === 'subgroup' && row.routingPolicy"
        :name="row.routingPolicy"
        class="shrink-0"
      />
      <LogLabel
        v-else
        :label="row.label"
        :address="row.address"
        :class="row.kind === 'subgroup' ? 'font-normal' : 'font-semibold'"
      >
        <ConnectionDeviceRenameButton
          v-if="row.sourceAddress"
          class="font-normal"
          @rename="emit('rename', { sourceAddress: row.sourceAddress, sourceLabel: row.label })"
        />
        <ConnectionTargetRoutingButton
          v-else
          :target="row.routingTarget"
          class="font-normal"
        />
      </LogLabel>
    </div>
    <div
      v-if="row.subgroupMode"
      class="order-last col-span-2 min-w-0 overflow-x-auto text-xs sm:order-none sm:col-span-1"
    >
      <LogSubgroupTabs
        :outer-mode="logGroupMode"
        :mode="row.subgroupMode"
        @change="(mode) => emit('subgroup', row.key, mode)"
      />
    </div>
    <span class="text-base-content/65 text-xs whitespace-nowrap tabular-nums">
      {{ $t('logCount', { count: row.count }) }}
    </span>
  </div>
</template>

<script setup lang="ts">
import ConnectionDeviceRenameButton from '@/components/connections/ConnectionDeviceRenameButton.vue'
import ConnectionTargetRoutingButton from '@/components/connections/ConnectionTargetRoutingButton.vue'
import LogLabel from '@/components/logs/LogLabel.vue'
import LogSubgroupTabs from '@/components/logs/LogSubgroupTabs.vue'
import ProxyName from '@/components/proxies/ProxyName.vue'
import type { LogItem } from '@/composables/logGroups'
import { logGroupMode, type LogSubgroupMode } from '@/store/logs'
import { ChevronDownIcon, ChevronRightIcon } from '@heroicons/vue/24/outline'

type HeadingRow = Extract<LogItem, { kind: 'group' | 'subgroup' }>

defineProps<{ row: HeadingRow }>()
const emit = defineEmits<{
  toggle: [row: HeadingRow]
  subgroup: [key: string, mode: LogSubgroupMode]
  rename: [target: { sourceAddress?: string; sourceLabel?: string }]
}>()
</script>
