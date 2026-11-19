<template>
  <div
    class="flex min-h-0 min-w-0 flex-1"
    :style="{ paddingTop: `${paddingTop}px` }"
  >
    <div class="app-card-inset flex min-h-0 flex-1">
      <div
        class="connection-table-shell log-table-shell border-base-300/60 bg-base-100 flex min-h-0 flex-1 overflow-hidden rounded-lg border"
      >
        <div
          ref="scrollRef"
          class="connection-table-scroll min-h-0 flex-1 overflow-auto overscroll-contain"
        >
          <table class="table-pin-rows table w-full table-fixed text-sm">
            <colgroup>
              <col class="w-24" />
              <col class="w-24" />
              <col class="w-[1%]" />
              <col class="w-[1%]" />
              <col class="w-[clamp(12rem,21cqw,24rem)]" />
              <col />
            </colgroup>
            <thead class="bg-base-100 sticky top-0 z-10">
              <tr>
                <th>{{ $t('logTime') }} ↓</th>
                <th>{{ $t('logLevel') }}</th>
                <th>{{ $t('connectionDevice') }}</th>
                <th>{{ $t('logOutboundOrModule') }}</th>
                <th>{{ $t('connectionTarget') }}</th>
                <th>{{ $t('logMessage') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-if="topSpacer > 0"
                class="connection-table-spacer"
                aria-hidden="true"
              >
                <td
                  colspan="6"
                  :style="{ height: `${topSpacer}px` }"
                ></td>
              </tr>
              <tr
                v-for="{ virtualRow, row, classes } in virtualItems"
                :key="row.key"
                :ref="(el) => measure(el as Element | null)"
                :data-index="virtualRow.index"
                class="bg-base-100 h-[52px] cursor-pointer"
                :class="classes"
                @click="row.kind === 'log' ? open(row) : emit('toggle', row)"
              >
                <td
                  v-if="row.kind !== 'log'"
                  colspan="6"
                >
                  <LogGroupHeading
                    :row="row"
                    @toggle="(item) => emit('toggle', item)"
                    @subgroup="(key, mode) => emit('subgroup', key, mode)"
                    @rename="(target) => emit('rename', target)"
                  />
                </td>
                <template v-else>
                  <td class="whitespace-nowrap tabular-nums">
                    <button
                      type="button"
                      class="cursor-pointer"
                      :aria-label="`${$t('logDetails')} · ${row.log.time}`"
                      @click.stop="emit('open', row)"
                    >
                      {{ row.log.time }}
                    </button>
                  </td>
                  <td><LogLevelBadge :level="row.level" /></td>
                  <td class="overflow-hidden">
                    <LogLabel
                      :label="row.sourceLabel || row.sourceAddress"
                      :address="row.sourceLabel ? row.sourceAddress : undefined"
                      class="w-max"
                    >
                      <ConnectionDeviceRenameButton
                        v-if="row.sourceAddress"
                        @rename="emit('rename', row)"
                      />
                    </LogLabel>
                  </td>
                  <td class="overflow-hidden">
                    <LogOutboundChain
                      v-if="row.outboundChain?.length || row.routingPolicy"
                      :chains="row.outboundChain ?? []"
                      :policy="row.routingPolicy"
                      single-line
                    />
                    <span
                      v-else
                      class="text-base-content/65 font-mono text-xs whitespace-nowrap"
                    >
                      {{ row.module || '—' }}
                    </span>
                  </td>
                  <td class="overflow-hidden">
                    <LogLabel
                      :label="row.targetAddress"
                      :address="row.targetFakeIp"
                      class="w-0 min-w-full"
                    >
                      <ConnectionTargetRoutingButton :target="row.routingTarget" />
                    </LogLabel>
                  </td>
                  <td>
                    <div class="truncate">
                      <span
                        v-if="row.log.probe"
                        class="badge badge-xs me-1"
                      >
                        {{ $t('logNodeProbe') }}
                      </span>
                      {{ row.message }}
                    </div>
                  </td>
                </template>
              </tr>
              <tr
                v-if="bottomSpacer > 0"
                class="connection-table-spacer"
                aria-hidden="true"
              >
                <td
                  colspan="6"
                  :style="{ height: `${bottomSpacer}px` }"
                ></td>
              </tr>
              <tr v-if="!rows.length">
                <td
                  colspan="6"
                  class="text-center"
                >
                  {{ $t('noContent') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import ConnectionDeviceRenameButton from '@/components/connections/ConnectionDeviceRenameButton.vue'
import ConnectionTargetRoutingButton from '@/components/connections/ConnectionTargetRoutingButton.vue'
import LogGroupHeading from '@/components/logs/LogGroupHeading.vue'
import LogLabel from '@/components/logs/LogLabel.vue'
import LogLevelBadge from '@/components/logs/LogLevelBadge.vue'
import LogOutboundChain from '@/components/logs/LogOutboundChain.vue'
import type { LogItem } from '@/composables/logGroups'
import type { LogRow } from '@/composables/logRows'
import { usePaddingForViews } from '@/composables/paddingViews'
import type { LogSubgroupMode } from '@/store/logs'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { computed, nextTick, ref } from 'vue'

type HeadingRow = Extract<LogItem, { kind: 'group' | 'subgroup' }>

const props = defineProps<{ rows: LogItem[] }>()
const emit = defineEmits<{
  toggle: [row: HeadingRow]
  subgroup: [key: string, mode: LogSubgroupMode]
  open: [row: LogRow]
  rename: [target: { sourceAddress?: string; sourceLabel?: string }]
}>()

// 表格外面已经包了一层 8px(app-card-inset),把公共逻辑给的 8px 安全边距抵掉
const { paddingTop } = usePaddingForViews({ offsetTop: -8, offsetBottom: 0 })
const scrollRef = ref<HTMLElement | null>(null)
const virtualizer = useVirtualizer(
  computed(() => {
    const list = props.rows

    return {
      count: list.length,
      getScrollElement: () => scrollRef.value,
      getItemKey: (index: number) => list[index]?.key ?? index,
      estimateSize: () => 52,
      overscan: 24,
    }
  }),
)
const measure = (el: Element | null) => {
  if (el) nextTick(() => virtualizer.value.measureElement(el))
}
const sectionOf = (row: LogItem | undefined) =>
  row?.kind === 'log' ? row.groupKey : row?.sectionKey
const virtualItems = computed(() =>
  virtualizer.value.getVirtualItems().flatMap((virtualRow) => {
    const row = props.rows[virtualRow.index]

    if (!row) return []

    const section = sectionOf(row)
    const expanded = row.kind !== 'log' && row.expanded
    const start = row.kind === 'group' && expanded
    const framed = start || (section !== undefined && row.key !== section)

    return [
      {
        virtualRow,
        row,
        classes: {
          'table-group-expanded': expanded,
          'table-group-frame': framed,
          'table-group-start': start,
          'table-group-end': framed && section !== sectionOf(props.rows[virtualRow.index + 1]),
        },
      },
    ]
  }),
)
// 虚拟列表只渲染可见的行,上下用两行空白撑出完整的滚动高度
const topSpacer = computed(() => virtualItems.value[0]?.virtualRow.start ?? 0)
const bottomSpacer = computed(() => {
  const last = virtualItems.value[virtualItems.value.length - 1]?.virtualRow.end ?? 0

  return Math.max(0, virtualizer.value.getTotalSize() - last)
})

// 选中文字时不当成点击,免得复制日志的时候弹出详情
const open = (row: LogRow) => {
  if (!window.getSelection()?.toString().trim()) emit('open', row)
}
</script>

<style>
.log-table-shell th,
.log-table-shell td {
  padding-inline: 0.5rem;
}
.log-table-shell thead th {
  border-bottom: 1px solid color-mix(in srgb, var(--color-base-300) 60%, transparent) !important;
}
.log-table-shell thead th:first-child,
.log-table-shell tbody td:first-child {
  padding-left: 0.75rem !important;
}
.log-table-shell .connection-table-spacer td {
  padding: 0 !important;
  border: 0 !important;
}
/* 展开的组和它的日志用一圈边框框起来 */
.log-table-shell tbody tr:not(.connection-table-spacer) > td {
  box-shadow:
    inset 0 var(--table-frame-top, 0px) var(--color-neutral),
    inset var(--table-frame-left, 0px) 0 var(--color-neutral),
    inset var(--table-frame-right, 0px) 0 var(--color-neutral),
    inset 0 var(--table-frame-bottom, -1px) var(--table-bottom-color, var(--color-base-300));
  border-bottom: 0;
}
.log-table-shell .table-group-frame > td:first-child {
  --table-frame-left: 3px;
}
.log-table-shell .table-group-frame > td:last-child {
  --table-frame-right: -3px;
}
.log-table-shell .table-group-start > td {
  --table-frame-top: 1px;
}
.log-table-shell .table-group-end > td {
  --table-frame-bottom: -1px;
  --table-bottom-color: var(--color-neutral);
}
#app-content .log-table-shell .table-group-expanded {
  background-color: var(--color-base-200);
}
@media (hover: hover) {
  #app-content .log-table-shell tbody tr.cursor-pointer:not(.table-group-expanded):hover {
    background-color: var(--color-base-200) !important;
  }
}
</style>
