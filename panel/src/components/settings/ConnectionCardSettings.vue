<template>
  <div class="relative flex flex-col rounded-sm">
    <div
      v-for="(row, rowIndex) in rows"
      :key="rowIndex"
      :class="`flex items-start gap-2 p-2 ${rowIndex % 2 === 0 ? 'bg-base-200' : 'bg-base-300'}`"
      data-connection-card-row
    >
      <button
        v-if="rows.length > 1"
        type="button"
        class="btn btn-circle bg-base-100 hover:bg-base-200 btn-sm mt-1 shrink-0"
        :aria-label="$t('delete')"
        @click="removeRow(rowIndex)"
      >
        <TrashIcon class="size-4" />
      </button>
      <div class="grid min-w-0 flex-1 grid-cols-3 gap-2">
        <Draggable
          v-for="(_, columnIndex) in row"
          :key="columnIndex"
          v-model="row[columnIndex]"
          class="border-base-content/10 rounded-field flex min-h-10 min-w-0 flex-col gap-2 border p-1"
          data-connection-card-column
          :animation="150"
          :force-fallback="true"
          :fallback-tolerance="5"
          group="connection-card-columns"
          ghostClass="ghost"
          :item-key="(id: string) => id"
          @end="save"
        >
          <template #item="{ element }">
            <button
              type="button"
              class="btn btn-sm bg-base-100 hover:bg-base-200 min-w-0 cursor-move px-1 sm:px-2"
              :title="$t(labelOf(element))"
            >
              <span class="truncate">{{ $t(labelOf(element)) }}</span>
            </button>
          </template>
        </Draggable>
      </div>
    </div>
    <div :class="`p-2 ${rows.length % 2 === 1 ? 'bg-base-300' : 'bg-base-200'}`">
      <button
        type="button"
        class="btn btn-circle bg-base-100 hover:bg-base-200 btn-sm"
        :aria-label="$t('connectionCardAddRow')"
        @click="addRow"
      >
        <PlusIcon class="size-4" />
      </button>
    </div>

    <Draggable
      v-model="restOfColumns"
      class="flex min-h-10 flex-wrap gap-2 p-2"
      :animation="150"
      :force-fallback="true"
      :fallback-tolerance="5"
      group="connection-card-columns"
      ghostClass="ghost"
      :item-key="(id: string) => id"
      @end="save"
    >
      <template #item="{ element }">
        <button
          type="button"
          class="btn btn-sm cursor-move"
        >
          {{ $t(labelOf(element)) }}
        </button>
      </template>
    </Draggable>
  </div>
</template>

<script setup lang="ts">
import {
  CONNECTION_COLUMN_LABEL_MAP,
  CONNECTIONS_TABLE_ACCESSOR_KEY,
  TARGET_ROUTING_COLUMN,
  type ConnectionCardColumn,
  type ConnectionCardRow,
} from '@/constant'
import { connectionCardLines } from '@/store/settings'
import { PlusIcon, TrashIcon } from '@heroicons/vue/24/outline'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Draggable from 'vuedraggable'

const { t } = useI18n()

const copyRows = (source: ConnectionCardRow[]): ConnectionCardRow[] =>
  source.map(([left, middle, right]) => [[...left], [...middle], [...right]])

// 「路由设置」是虚拟列,「关闭」按钮固定在卡片最左,不进可选列
const ALL_COLUMNS: ConnectionCardColumn[] = [
  TARGET_ROUTING_COLUMN,
  ...Object.values(CONNECTIONS_TABLE_ACCESSOR_KEY).filter(
    (key) => key !== CONNECTIONS_TABLE_ACCESSOR_KEY.Close,
  ),
]

const rows = ref<ConnectionCardRow[]>(copyRows(connectionCardLines.value))
const restOfColumns = ref<ConnectionCardColumn[]>([])

const labelOf = (key: ConnectionCardColumn) =>
  key === TARGET_ROUTING_COLUMN ? 'routingSettings' : (CONNECTION_COLUMN_LABEL_MAP[key] ?? key)

const refreshRest = () => {
  const used = new Set(rows.value.flat(2))
  restOfColumns.value = ALL_COLUMNS.filter((key) => !used.has(key))
}
refreshRest()

const save = () => {
  connectionCardLines.value = copyRows(rows.value)
}

const addRow = () => {
  rows.value.push([[], [], []])
  save()
}

const removeRow = (index: number) => {
  const row = rows.value[index]
  if (!row || rows.value.length <= 1) return
  if (
    row.some((column) => column.length > 0) &&
    !window.confirm(t('connectionCardLineDeleteConfirm'))
  ) {
    return
  }
  rows.value.splice(index, 1)
  refreshRest()
  save()
}
</script>
