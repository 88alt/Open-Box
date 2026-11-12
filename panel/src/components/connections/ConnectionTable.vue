<template>
  <div class="app-card-inset flex min-h-0 flex-1">
    <div
      class="connection-table-shell border-base-300/60 bg-base-100 flex min-h-0 flex-1 overflow-hidden rounded-lg border"
    >
      <div
        ref="parentRef"
        class="connection-table-scroll min-h-0 flex-1 overflow-auto overscroll-contain"
      >
        <div :style="{ minHeight: `${totalSize}px` }">
          <table
            :class="sharedTableClass"
            :style="tableStyle"
          >
            <colgroup v-if="isManualTable">
              <col
                v-for="column in visibleColumns"
                :key="column.id"
                :style="getColumnStyle(column.id, column.getSize())"
              />
            </colgroup>
            <thead
              ref="headerRef"
              class="bg-base-100 sticky top-0 z-10"
            >
              <tr
                v-for="headerGroup in tanstackTable.getHeaderGroups()"
                :key="headerGroup.id"
              >
                <th
                  v-for="header in headerGroup.headers"
                  :key="header.id"
                  :colSpan="header.colSpan"
                  :aria-sort="getAriaSort(header.column.id)"
                  class="relative"
                  :class="[
                    isSortableColumn(header.column.id) ? 'cursor-pointer select-none' : '',
                    header.column.id === CONNECTIONS_TABLE_ACCESSOR_KEY.Close ? 'px-1' : '',
                    header.column.getIsPinned && header.column.getIsPinned() === 'left'
                      ? 'pinned-td bg-base-100 sticky left-0 z-20'
                      : '',
                  ]"
                  :style="getColumnStyle(header.column.id, header.getSize())"
                  @click="toggleSort(header.column.id)"
                >
                  <div class="flex items-center gap-1">
                    <FlexRender
                      v-if="!header.isPlaceholder"
                      :render="header.column.columnDef.header"
                      :props="header.getContext()"
                    >
                    </FlexRender>
                    <template v-if="header.column.id === sortColumnId">
                      <ArrowDownCircleIcon
                        v-if="activeSortDesc"
                        class="h-4 w-4"
                      />
                      <ArrowUpCircleIcon
                        v-else
                        class="h-4 w-4"
                      />
                    </template>
                    <div>
                      <button
                        v-if="
                          header.column.id === CONNECTIONS_TABLE_ACCESSOR_KEY.Host ||
                          header.column.id === CONNECTIONS_TABLE_ACCESSOR_KEY.SniffHost
                        "
                        class="btn btn-xs btn-circle btn-ghost"
                        @click.stop="() => handlePinColumn(header.column)"
                      >
                        <MapPinIcon
                          v-if="header.column.getIsPinned() !== 'left'"
                          class="h-4 w-4"
                        />
                        <XMarkIcon
                          v-else
                          class="h-4 w-4"
                        />
                      </button>
                    </div>
                  </div>
                  <div
                    v-if="isManualTable"
                    @dblclick="() => header.column.resetSize()"
                    @click.stop
                    @mousedown.stop="(e) => header.getResizeHandler()(e)"
                    @touchstart.stop="(e) => header.getResizeHandler()(e)"
                    class="resizer bg-neutral absolute top-0 right-0 h-full w-1 cursor-ew-resize"
                  />
                </th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-if="topSpacer > 0"
                class="connection-table-spacer"
                aria-hidden="true"
              >
                <td
                  :colspan="visibleColumns.length"
                  :style="{ height: `${topSpacer}px` }"
                ></td>
              </tr>
              <tr
                v-for="{ virtualRow, item } in virtualItems"
                :key="item.key"
                :style="{ height: `${virtualRow.size}px` }"
                :class="getRowClass(item)"
                @click="handlerClickRow(item)"
              >
                <!-- 展开的组下面单独一行的子分组页签(列太窄放不进组头那一行时) -->
                <td
                  v-if="item.kind === 'tabs'"
                  :colspan="visibleColumns.length"
                  class="connection-table-group-tabs"
                >
                  <div class="sticky left-0 w-max max-w-full">
                    <ConnectionSubgroupTabs
                      :group-id="item.groupId"
                      :mode="item.mode"
                    />
                  </div>
                </td>
                <!-- 组头 / 子组头:第一列放展开箭头,第一个数据列放名字和连接数,流量列放组内合计 -->
                <template v-else-if="item.kind !== 'connection'">
                  <template
                    v-for="column in visibleColumns"
                    :key="column.id"
                  >
                    <td
                      v-if="column.id === CONNECTIONS_TABLE_ACCESSOR_KEY.Close"
                      :class="getPinnedClass(column)"
                      :style="getColumnStyle(column.id, column.getSize())"
                    >
                      <component
                        :is="item.expanded ? ChevronDownIcon : ChevronRightIcon"
                        class="mx-auto size-4"
                        aria-hidden="true"
                      />
                    </td>
                    <td
                      v-else-if="column.id === labelColumn?.id"
                      :class="getPinnedClass(column)"
                      :style="getColumnStyle(column.id, column.getSize())"
                    >
                      <div
                        class="flex min-w-0 items-center justify-between gap-2"
                        :class="{ 'pl-4': item.kind === 'subgroup' }"
                      >
                        <div class="min-w-0">
                          <div class="flex min-w-0 items-center gap-2">
                            <ProxyName
                              v-if="
                                item.kind === 'subgroup' &&
                                item.mode === 'routing' &&
                                item.group.key
                              "
                              :name="item.group.key"
                            />
                            <button
                              v-else
                              type="button"
                              class="min-w-0 cursor-pointer truncate text-start"
                              :class="item.kind === 'subgroup' ? 'font-normal' : 'font-semibold'"
                              :aria-expanded="item.expanded"
                              :title="getGroupLabel(item)"
                              @click.stop="toggleConnectionGroupItem(item)"
                            >
                              {{ getGroupLabel(item) }}
                            </button>
                            <ConnectionDeviceRenameButton
                              v-if="item.group.deviceIP"
                              @rename="emit('renameDevice', item.group)"
                            />
                            <ConnectionTargetRoutingButton :target="item.group.target" />
                          </div>
                          <span class="text-base-content/65 block truncate text-xs">
                            <template v-if="item.group.sourceIP"
                              >{{ item.group.sourceIP }} ·
                            </template>
                            {{
                              $t('connectionGroupCount', { count: item.group.connections.length })
                            }}
                          </span>
                        </div>
                        <span
                          v-if="METRIC_KEY[column.id]"
                          class="shrink-0 tabular-nums"
                          :class="getMetricClass(column.id)"
                        >
                          {{ getMetricText(item.group, column.id) }}
                        </span>
                      </div>
                    </td>
                    <td
                      v-else-if="isInlineTabsStart(item, column.id)"
                      :colspan="inlineTabs?.span"
                      class="connection-table-group-tabs"
                    >
                      <div class="text-xs">
                        <ConnectionSubgroupTabs
                          :group-id="item.key"
                          :mode="item.kind === 'group' ? item.subgroupMode : 'none'"
                        />
                      </div>
                    </td>
                    <td
                      v-else-if="!isInlineTabsCovered(item, column.id)"
                      class="tabular-nums"
                      :class="[getMetricClass(column.id), getPinnedClass(column)]"
                      :style="getColumnStyle(column.id, column.getSize())"
                    >
                      {{ getMetricText(item.group, column.id) }}
                    </td>
                  </template>
                </template>
                <template v-else>
                  <td
                    v-for="cell in rowsById[item.connection.id]?.getVisibleCells() ?? []"
                    :key="cell.id"
                    :class="[
                      cell.column.id === CONNECTIONS_TABLE_ACCESSOR_KEY.Close ? 'px-1' : '',
                      isManualTable
                        ? 'cursor-pointer truncate text-sm select-text'
                        : 'cursor-pointer text-sm whitespace-nowrap select-text',
                      cell.column.getIsPinned && cell.column.getIsPinned() === 'left'
                        ? 'pinned-td sticky left-0 z-20 bg-inherit'
                        : '',
                    ]"
                    :style="getColumnStyle(cell.column.id, cell.column.getSize())"
                    @contextmenu="handleCellRightClick($event, cell)"
                  >
                    <FlexRender
                      :render="cell.column.columnDef.cell"
                      :props="cell.getContext()"
                    />
                  </td>
                </template>
              </tr>
              <tr
                v-if="bottomSpacer > 0"
                class="connection-table-spacer"
                aria-hidden="true"
              >
                <td
                  :colspan="visibleColumns.length"
                  :style="{ height: `${bottomSpacer}px` }"
                ></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { blockConnectionByIdAPI, disconnectByIdAPI } from '@/api'
import { useConnections } from '@/composables/connections'
import {
  CONNECTION_COLUMN_LABEL_MAP,
  CONNECTIONS_TABLE_ACCESSOR_KEY,
  PROXY_CHAIN_DIRECTION,
  SORT_DIRECTION,
  SORT_TYPE,
  SORT_TYPE_COLUMN,
  TABLE_SIZE,
  TABLE_WIDTH_MODE,
  type CONNECTION_SUBGROUP_MODE,
} from '@/constant'
import {
  getDestinationFromConnection,
  getDestinationTypeFromConnection,
  getHostFromConnection,
  getInboundUserFromConnection,
  getNetworkTypeFromConnection,
  getProcessFromConnection,
} from '@/helper'
import { copyText } from '@/helper/clipboard'
import { showNotification } from '@/helper/notification'
import { getDeviceLabel } from '@/helper/sourceip'
import { fromNow, prettyBytesHelper } from '@/helper/utils'
import {
  connectionGroupItems,
  toggleConnectionGroupItem,
  type ConnectionGroup,
  type ConnectionGroupItem,
} from '@/store/connectionGroups'
import {
  activeSortDesc,
  activeSortType,
  connectionSortDirection,
  connectionSortType,
  renderConnections,
} from '@/store/connections'
import {
  connectionTableColumns,
  proxyChainDirection,
  tableSize,
  tableWidthMode,
} from '@/store/settings'
import type { Connection } from '@/types'
import {
  ArrowDownCircleIcon,
  ArrowRightCircleIcon,
  ArrowUpCircleIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  MapPinIcon,
  NoSymbolIcon,
  XMarkIcon,
} from '@heroicons/vue/24/outline'
import {
  FlexRender,
  getCoreRowModel,
  isFunction,
  useVueTable,
  type Column,
  type ColumnDef,
  type ColumnPinningState,
} from '@tanstack/vue-table'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { useResizeObserver, useStorage } from '@vueuse/core'
import { computed, h, ref, type VNode } from 'vue'
import { useI18n } from 'vue-i18n'
import ProxyName from '../proxies/ProxyName.vue'
import ConnectionDeviceRenameButton from './ConnectionDeviceRenameButton.vue'
import ConnectionTargetRoutingButton from './ConnectionTargetRoutingButton.vue'
import ConnectionSubgroupTabs from './ConnectionSubgroupTabs.vue'

// 表格里的行:组头 / 子组头 / 连接(和卡片视图同一份分组数据),外加展开的组下面单独一行的子分组页签
type TableItem =
  | ConnectionGroupItem
  | { kind: 'tabs'; key: string; groupId: string; mode: CONNECTION_SUBGROUP_MODE }
type GroupHeadItem = Exclude<ConnectionGroupItem, { kind: 'connection' }>

const { handlerInfo } = useConnections()
const columnWidthMap = useStorage('config/table-column-width', {
  [CONNECTIONS_TABLE_ACCESSOR_KEY.Close]: 50,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.Host]: 320,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.Chains]: 320,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.Rule]: 200,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.Download]: 80,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed]: 80,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.Upload]: 80,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed]: 80,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.Outbound]: 80,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.Type]: 150,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.Process]: 150,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.SourceIP]: 150,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.SourcePort]: 100,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.SniffHost]: 200,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.Destination]: 150,
  [CONNECTIONS_TABLE_ACCESSOR_KEY.ConnectTime]: 100,
} as Record<CONNECTIONS_TABLE_ACCESSOR_KEY, number>)
const isManualTable = computed(() => tableWidthMode.value === TABLE_WIDTH_MODE.MANUAL)
const emit = defineEmits<{ renameDevice: [group: ConnectionGroup] }>()

const { t } = useI18n()
const columns: ColumnDef<Connection>[] = [
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.Close),
    enableSorting: false,
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.Close,
    cell: ({ row }) => {
      const closeButton = h(
        'button',
        {
          class: 'btn btn-xs btn-circle',
          onClick: (e) => {
            const connection = row.original

            e.stopPropagation()
            disconnectByIdAPI(connection.id)
          },
        },
        [
          h(XMarkIcon, {
            class: 'h-4 w-4',
          }),
        ],
      )

      if (row.original.metadata.smartBlock === 'normal') {
        const degradeButton = h(
          'button',
          {
            class: 'btn btn-xs btn-circle',
            'aria-label': t('smartBlockConnection'),
            onClick: (e) => {
              const connection = row.original

              e.stopPropagation()
              blockConnectionByIdAPI(connection.id)
            },
          },
          [
            h(NoSymbolIcon, {
              class: 'h-4 w-4',
            }),
          ],
        )

        return h('div', { class: 'flex items-center justify-center gap-0.5' }, [
          closeButton,
          degradeButton,
        ])
      }

      return closeButton
    },
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.Type),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.Type,
    accessorFn: getNetworkTypeFromConnection,
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.Process),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.Process,
    accessorFn: getProcessFromConnection,
  },
  {
    header: () => t(CONNECTION_COLUMN_LABEL_MAP[CONNECTIONS_TABLE_ACCESSOR_KEY.Host]!),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.Host,
    accessorFn: getHostFromConnection,
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.SniffHost),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.SniffHost,
    accessorFn: (original) => original.metadata.sniffHost || '-',
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.Rule),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.Rule,
    accessorFn: (original) =>
      !original.rulePayload ? original.rule : `${original.rule}: ${original.rulePayload}`,
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.Chains),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.Chains,
    accessorFn: (original) => {
      const chains = [...original.chains]

      return proxyChainDirection.value === PROXY_CHAIN_DIRECTION.REVERSE
        ? chains.join(' → ')
        : chains.reverse().join(' → ')
    },
    cell: ({ row }) => {
      const chains: VNode[] = []
      const isReverse = proxyChainDirection.value === PROXY_CHAIN_DIRECTION.REVERSE
      const originChains = row.original.chains

      // 完整显示所有代理链
      originChains.forEach((chain, index) => {
        chains.unshift(h(ProxyName, { name: chain, key: chain }))

        if (index < originChains.length - 1) {
          chains.unshift(
            h(ArrowRightCircleIcon, {
              class: 'h-4 w-4 shrink-0',
              key: `arrow-${index}`,
            }),
          )
        }
      })

      return h(
        'div',
        {
          class: `flex items-center ${isReverse && 'flex-row-reverse justify-end'} gap-1`,
        },
        chains,
      )
    },
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.Outbound),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.Outbound,
    accessorFn: (original) => original.chains[0],
    cell: ({ row }) => {
      return h(ProxyName, { name: row.original.chains[0] })
    },
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.ConnectTime),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.ConnectTime,
    accessorFn: (original) => fromNow(original.start),
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed,
    accessorFn: (original) => `${prettyBytesHelper(original.downloadSpeed)}/s`,
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed,
    accessorFn: (original) => `${prettyBytesHelper(original.uploadSpeed)}/s`,
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.Download),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.Download,
    accessorFn: (original) => prettyBytesHelper(original.download),
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.Upload),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.Upload,
    accessorFn: (original) => prettyBytesHelper(original.upload),
  },
  {
    header: () => t(CONNECTION_COLUMN_LABEL_MAP[CONNECTIONS_TABLE_ACCESSOR_KEY.SourceIP]!),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.SourceIP,
    accessorFn: (original) => {
      return getDeviceLabel(original.metadata.sourceIP)
    },
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.SourcePort),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.SourcePort,
    accessorFn: (original) => original.metadata.sourcePort,
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.Destination),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.Destination,
    accessorFn: getDestinationFromConnection,
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.DestinationType),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.DestinationType,
    accessorFn: getDestinationTypeFromConnection,
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.RemoteAddress),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.RemoteAddress,
    accessorFn: (original) => original.metadata.remoteDestination || '-',
  },
  {
    header: () => t(CONNECTIONS_TABLE_ACCESSOR_KEY.InboundUser),
    id: CONNECTIONS_TABLE_ACCESSOR_KEY.InboundUser,
    accessorFn: getInboundUserFromConnection,
  },
]

const columnPinning = useStorage<ColumnPinningState>('config/table-column-pinning', {
  left: [],
  right: [],
})

const tanstackTable = useVueTable({
  get data() {
    return renderConnections.value
  },
  columns,
  getRowId: (conn) => conn.id,
  // 排序不走 TanStack:顺序由全局排序项决定(组的顺序和组内连接的顺序都在 store 里排好)
  enableSorting: false,
  columnResizeMode: 'onChange',
  columnResizeDirection: 'ltr',
  state: {
    get columnOrder() {
      return [CONNECTIONS_TABLE_ACCESSOR_KEY.Close, ...connectionTableColumns.value]
    },
    get columnVisibility() {
      return {
        ...Object.fromEntries(
          Object.values(CONNECTIONS_TABLE_ACCESSOR_KEY).map((key) => [key, false]),
        ),
        ...Object.fromEntries(
          [
            // 「关闭」列固定在最左
            CONNECTIONS_TABLE_ACCESSOR_KEY.Close,
            ...connectionTableColumns.value,
          ].map((key) => [key, true]),
        ),
      }
    },
    get columnSizing() {
      return columnWidthMap.value
    },
    get columnPinning() {
      return columnPinning.value
    },
  },
  onColumnSizingChange: (updater) => {
    if (isFunction(updater)) {
      columnWidthMap.value = updater(columnWidthMap.value) as Record<
        CONNECTIONS_TABLE_ACCESSOR_KEY,
        number
      >
    } else {
      columnWidthMap.value = updater as Record<CONNECTIONS_TABLE_ACCESSOR_KEY, number>
    }
  },
  onColumnPinningChange: (updater) => {
    if (isFunction(updater)) {
      columnPinning.value = updater(columnPinning.value)
    } else {
      columnPinning.value = updater
    }
  },
  getCoreRowModel: getCoreRowModel(),
})

const rowsById = computed(() => tanstackTable.getCoreRowModel().rowsById)

// 实际显示的列:固定在左边的在前,和表头 / 单元格的顺序一致
const visibleColumns = computed(() => [
  ...tanstackTable.getLeftVisibleLeafColumns(),
  ...tanstackTable.getCenterVisibleLeafColumns(),
])
// 组头里放名字的那一列:第一个不是「关闭」的列
const labelColumn = computed(() =>
  visibleColumns.value.find((column) => column.id !== CONNECTIONS_TABLE_ACCESSOR_KEY.Close),
)

// 组头里每个流量列显示的是组内合计
const METRIC_KEY: Partial<Record<string, 'downloadSpeed' | 'uploadSpeed' | 'download' | 'upload'>> =
  {
    [CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed]: 'downloadSpeed',
    [CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed]: 'uploadSpeed',
    [CONNECTIONS_TABLE_ACCESSOR_KEY.Download]: 'download',
    [CONNECTIONS_TABLE_ACCESSOR_KEY.Upload]: 'upload',
  }
const getMetricText = (group: ConnectionGroup, columnId: string) => {
  const key = METRIC_KEY[columnId]

  if (!key) return ''

  return prettyBytesHelper(group[key]) + (key.endsWith('Speed') ? '/s' : '')
}
const getMetricClass = (columnId: string) => ({
  'text-success': columnId === CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed,
  'text-info': columnId === CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed,
})

// 展开的组的子分组页签:「关闭」在最左、名字列后面有一段不是固定列也不是流量列的空位时,
// 页签放进组头那一行里(合并这几个单元格);放不下(手动列宽 / 固定了列 / 空位太少)就单独占一行
const inlineTabs = computed(() => {
  if (isManualTable.value) return null

  const [first, , ...rest] = visibleColumns.value

  if (first?.id !== CONNECTIONS_TABLE_ACCESSOR_KEY.Close) return null

  const index = rest.findIndex((column) => column.getIsPinned() || METRIC_KEY[column.id])
  const span = index < 0 ? rest.length : index

  if (span < 1) return null

  return {
    start: rest[0].id,
    span,
    covered: new Set(rest.slice(1, span).map((column) => column.id)),
  }
})
const hasInlineTabs = (item: TableItem) =>
  item.kind === 'group' && item.expanded && inlineTabs.value !== null
const isInlineTabsStart = (item: TableItem, columnId: string) =>
  hasInlineTabs(item) && inlineTabs.value?.start === columnId
const isInlineTabsCovered = (item: TableItem, columnId: string) =>
  hasInlineTabs(item) && Boolean(inlineTabs.value?.covered.has(columnId))

const rowItems = computed<TableItem[]>(() => {
  if (inlineTabs.value) return connectionGroupItems.value

  return connectionGroupItems.value.flatMap<TableItem>((item) =>
    item.kind === 'group' && item.expanded
      ? [
          item,
          { kind: 'tabs', key: `tabs:${item.key}`, groupId: item.key, mode: item.subgroupMode },
        ]
      : [item],
  )
})

const parentRef = ref<HTMLElement | null>(null)
const headerRef = ref<HTMLElement | null>(null)
const headerHeight = ref(0)

useResizeObserver(headerRef, () => {
  headerHeight.value = headerRef.value?.offsetHeight ?? 0
})

// 组头比连接行高(两行字),页签行介于两者之间;表格行的 height 只是最小高度,量不准也不会截断
const GROUP_ROW_SIZE = 52
const TABS_ROW_SIZE = 44
const CONNECTION_ROW_SIZE = 36
const getItemSize = (item: TableItem | undefined) => {
  if (!item || item.kind === 'connection') return CONNECTION_ROW_SIZE

  return item.kind === 'tabs' ? TABS_ROW_SIZE : GROUP_ROW_SIZE
}
const rowVirtualizerOptions = computed(() => {
  const list = rowItems.value

  return {
    count: list.length,
    getItemKey: (index: number) => list[index]?.key ?? index,
    getScrollElement: () => parentRef.value,
    estimateSize: (index: number) => getItemSize(list[index]),
    overscan: 24,
  }
})

const rowVirtualizer = useVirtualizer(rowVirtualizerOptions)
const virtualItems = computed(() =>
  rowVirtualizer.value.getVirtualItems().flatMap((virtualRow) => {
    const item = rowItems.value[virtualRow.index]

    return item ? [{ virtualRow, item }] : []
  }),
)
// 虚拟列表只渲染可见的行,上下用两行空白撑出完整的滚动高度
const topSpacer = computed(() => virtualItems.value[0]?.virtualRow.start ?? 0)
const bottomSpacer = computed(() => {
  const last = virtualItems.value[virtualItems.value.length - 1]?.virtualRow

  return last ? Math.max(0, rowVirtualizer.value.getTotalSize() - last.end) : 0
})
const totalSize = computed(() => rowVirtualizer.value.getTotalSize() + headerHeight.value)

const sharedTableClass = computed(() => {
  return [
    'table rounded-none',
    isManualTable.value ? 'table-fixed' : 'table-auto',
    sizeOfTable.value,
  ]
})
const resolvedTableWidth = computed(() => {
  if (!isManualTable.value) {
    return null
  }

  return visibleColumns.value.reduce((total, column) => total + column.getSize(), 0)
})
const tableStyle = computed(() => {
  if (!isManualTable.value) {
    return {
      width: 'max-content',
      minWidth: '100%',
    }
  }

  return {
    width: `${resolvedTableWidth.value}px`,
  }
})
const getColumnStyle = (columnId: string, width: number) => {
  if (!isManualTable.value) {
    return undefined
  }

  return {
    width: `${width}px`,
    minWidth: `${width}px`,
    maxWidth: `${width}px`,
  }
}

const classMap = {
  [TABLE_SIZE.SMALL]: 'table-xs',
  [TABLE_SIZE.LARGE]: 'table-sm',
}
const sizeOfTable = computed(() => {
  return classMap[tableSize.value]
})

// 表头点击排序:点的是哪一列就按哪个排序项排,再点一次反向(和卡片视图的排序菜单共用同一个设置)。
// 没有对应排序项的列(进程、嗅探域名、源端口……)不能排
const sortTypeOfColumn = new Map<string, SORT_TYPE>(
  Object.entries(SORT_TYPE_COLUMN).map(([type, column]) => [column, type as SORT_TYPE]),
)
const DESC_FIRST_SORT_TYPES = new Set<SORT_TYPE>([
  SORT_TYPE.DOWNLOAD,
  SORT_TYPE.DOWNLOAD_SPEED,
  SORT_TYPE.UPLOAD,
  SORT_TYPE.UPLOAD_SPEED,
])
const sortColumnId = computed(() => SORT_TYPE_COLUMN[activeSortType.value])
const isSortableColumn = (columnId: string) => sortTypeOfColumn.has(columnId)
const getAriaSort = (columnId: string) => {
  if (columnId !== sortColumnId.value) return undefined

  return activeSortDesc.value ? 'descending' : 'ascending'
}
const toggleSort = (columnId: string) => {
  const type = sortTypeOfColumn.get(columnId)

  if (!type) return

  if (activeSortType.value === type) {
    connectionSortDirection.value = activeSortDesc.value ? SORT_DIRECTION.ASC : SORT_DIRECTION.DESC
  } else {
    connectionSortDirection.value = DESC_FIRST_SORT_TYPES.has(type)
      ? SORT_DIRECTION.DESC
      : SORT_DIRECTION.ASC
  }
  connectionSortType.value = type
}

const getRowClass = (item: TableItem) => {
  if (item.kind === 'connection') {
    return 'bg-base-100 cursor-pointer hover:bg-primary! hover:text-primary-content'
  }
  if (item.kind === 'tabs') return 'bg-base-200'

  return 'bg-base-200 cursor-pointer hover:bg-base-300!'
}
// 固定在左边的列在组头那一行里也要固定
const getPinnedClass = (column: Column<Connection, unknown>) =>
  column.getIsPinned() === 'left' ? 'pinned-td sticky left-0 z-20 bg-inherit' : ''
const getGroupLabel = (item: GroupHeadItem) => item.group.label || t('connectionRoutingUnmatched')

const hasSelectedText = () => {
  return Boolean(window.getSelection?.()?.toString().trim())
}

const handlerClickRow = (item: TableItem) => {
  if (item.kind === 'tabs') return
  if (item.kind === 'connection') {
    if (hasSelectedText()) return
    handlerInfo(item.connection)

    return
  }
  toggleConnectionGroupItem(item)
}

const handlePinColumn = (column: Column<Connection, unknown>) => {
  if (column.getIsPinned() === 'left') {
    column.pin(false)
  } else {
    const currentPinning = columnPinning.value.left || []

    currentPinning.forEach((pinnedColumnId: string) => {
      if (pinnedColumnId !== column.id) {
        const pinnedColumn = tanstackTable.getColumn(pinnedColumnId)
        if (pinnedColumn) {
          pinnedColumn.pin(false)
        }
      }
    })
    column.pin('left')
  }
}

// 复制功能:统一走 helper/clipboard(http 下 navigator.clipboard 不可用时走兜底)
const copyToClipboard = async (text: string) => {
  const ok = await copyText(text)
  showNotification(
    ok
      ? { content: 'copySuccess', type: 'alert-success', timeout: 2000 }
      : { content: 'copyFailed', type: 'alert-error' },
  )
}

const handleCellRightClick = (
  event: MouseEvent,
  cell: { column: { id: string }; getValue: () => unknown },
) => {
  event.preventDefault()

  const cellValue = cell.getValue()

  if (cellValue && cellValue !== '-') {
    copyToClipboard(String(cellValue))
  }
}
</script>

<style>
th .resizer {
  @apply opacity-0;
}
th:hover .resizer {
  @apply opacity-100;
}

.connection-table-shell th,
.connection-table-shell td {
  padding-inline: 0.5rem;
}

.connection-table-shell thead th {
  border-bottom: 1px solid color-mix(in srgb, var(--color-base-300) 60%, transparent) !important;
}

.connection-table-shell thead th:first-child,
.connection-table-shell tbody td:first-child {
  padding-left: 0.75rem !important;
}

.connection-table-shell .connection-table-spacer td {
  padding: 0 !important;
  border: 0 !important;
}

.connection-table-shell td.connection-table-group-tabs {
  padding-block: 0.25rem;
}
</style>
