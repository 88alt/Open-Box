<template>
  <div>
    <div class="flex gap-4 rounded-sm">
      <Draggable
        class="bg-base-200 flex flex-1 flex-col gap-2 p-2"
        v-model="connectionTableColumns"
        :move="keepOneColumn"
        group="connection-table-columns"
        :animation="150"
        :force-fallback="true"
        :fallback-tolerance="5"
        :item-key="(id: string) => id"
      >
        <template #item="{ element }">
          <button
            type="button"
            class="btn btn-sm bg-base-100 cursor-move"
          >
            {{
              $t(CONNECTION_COLUMN_LABEL_MAP[element as CONNECTIONS_TABLE_ACCESSOR_KEY] ?? element)
            }}
          </button>
        </template>
      </Draggable>
      <Draggable
        class="flex flex-1 flex-col gap-2 p-2"
        v-model="restOfColumns"
        group="connection-table-columns"
        :animation="150"
        :force-fallback="true"
        :fallback-tolerance="5"
        :item-key="(id: string) => id"
      >
        <template #item="{ element }">
          <button
            type="button"
            class="btn btn-sm cursor-move"
          >
            {{
              $t(CONNECTION_COLUMN_LABEL_MAP[element as CONNECTIONS_TABLE_ACCESSOR_KEY] ?? element)
            }}
          </button>
        </template>
      </Draggable>
    </div>
  </div>
</template>

<script setup lang="ts">
import { CONNECTION_COLUMN_LABEL_MAP, CONNECTIONS_TABLE_ACCESSOR_KEY } from '@/constant'
import { connectionTableColumns } from '@/store/settings'
import { ref } from 'vue'
import Draggable from 'vuedraggable'

// 「关闭」列固定在表格最左,不进列设置
const restOfColumns = ref(
  Object.values(CONNECTIONS_TABLE_ACCESSOR_KEY).filter(
    (key) =>
      key !== CONNECTIONS_TABLE_ACCESSOR_KEY.Close && !connectionTableColumns.value.includes(key),
  ),
)

// 至少留一列:已选列表里只剩一列时不允许把它拖走
const keepOneColumn = (event: { from: Element; to: Element }) =>
  event.from === event.to || connectionTableColumns.value.length > 1
</script>
