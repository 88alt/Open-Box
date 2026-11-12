<template>
  <div
    ref="parentRef"
    class="flex h-full w-full flex-col overflow-y-auto"
  >
    <slot name="before" />
    <div
      :style="{
        height: `${totalSize}px`,
      }"
      class="relative w-full"
      v-if="data.length > 0"
    >
      <div
        class="app-card-inset absolute top-0 left-0 w-full"
        :style="{
          transform: `translateY(${virtualRows[0]?.start ?? 0}px)`,
        }"
      >
        <div
          v-for="row in virtualRows"
          :key="row.key.toString()"
          :data-index="row.index"
          :ref="(ref) => measureElement(ref as Element | null)"
          :style="{ marginBottom: marginBottom(row.index) }"
        >
          <slot
            :item="data[row.index]"
            :index="row.index"
          />
        </div>
      </div>
    </div>
    <div
      v-else
      class="card app-card-inset app-card-margin flex-row text-sm"
    >
      {{ $t('noContent') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { usePaddingForViews } from '@/composables/paddingViews'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { computed, nextTick, ref } from 'vue'

const { paddingBottom } = usePaddingForViews({
  offsetTop: 0,
  offsetBottom: 0,
})
const parentRef = ref<HTMLElement | null>(null)
const props = withDefaults(
  defineProps<{
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    data: any[]
    size?: number
    overscan?: number
    // 条目有增删 / 重排时,用稳定的键让已经量好的行高跟着条目走(默认用下标)
    getItemKey?: (index: number) => string | number
    // 第 index 条和下一条之间的间距(CSS 长度),默认用卡片间距
    itemSpacing?: (index: number) => string
  }>(),
  {
    data: () => [],
    size: 64,
    overscan: 24,
  },
)
const virutalOptions = computed(() => {
  return {
    count: props.data.length,
    getScrollElement: () => parentRef.value,
    estimateSize: () => props.size,
    overscan: props.overscan,
    ...(props.getItemKey ? { getItemKey: props.getItemKey } : {}),
  }
})

const rowVirtualizer = useVirtualizer(virutalOptions)
const virtualRows = computed(() => rowVirtualizer.value.getVirtualItems())
const totalSize = computed(() => rowVirtualizer.value.getTotalSize())

const marginBottom = (index: number) => {
  if (index === props.data.length - 1) return `${paddingBottom.value}px`

  return props.itemSpacing ? props.itemSpacing(index) : 'var(--app-card-space, 12px)'
}
const measureElement = (el: Element | null) => {
  if (!el) {
    return
  }

  nextTick(() => {
    rowVirtualizer.value.measureElement(el)
  })

  return undefined
}
</script>
