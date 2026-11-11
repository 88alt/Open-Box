<template>
  <!-- 节点信息的一行:放不下就截断,点一下展开全文再点收起(row 模式带左边的标签列) -->
  <div
    class="min-w-0"
    :class="[
      row ? 'col-span-2 grid grid-cols-subgrid gap-x-2' : 'flex',
      overflowing && 'focus-visible:outline-primary cursor-pointer focus-visible:outline-2',
    ]"
    :role="overflowing ? 'button' : undefined"
    :tabindex="overflowing ? 0 : undefined"
    :aria-expanded="overflowing ? expanded : undefined"
    :aria-label="overflowing ? $t(expanded ? 'nodeInfoCollapseText' : 'nodeInfoExpandText', { field: label }) : undefined"
    data-node-info-text
    @click="toggle"
    @keydown.enter.prevent="toggle"
    @keydown.space.prevent="toggle"
  >
    <span
      v-if="row"
      class="opacity-60"
    >
      {{ label }}
    </span>
    <div class="flex min-w-0 flex-1 items-start gap-1">
      <slot name="prefix" />
      <div
        ref="host"
        class="relative min-w-0 flex-1"
      >
        <!-- 量宽度用的隐形副本:单行不换行,比容器宽就说明被截断了 -->
        <span
          ref="probe"
          class="invisible absolute inset-x-0 top-0 h-0 overflow-hidden whitespace-nowrap"
          aria-hidden="true"
        >
          {{ text }}
          <span
            v-if="source"
            class="ml-2 text-[10px]"
          >
            {{ source }}
          </span>
        </span>
        <span
          class="flex min-w-0 items-baseline justify-between gap-x-2"
          :class="[overflowing && 'pr-5', expanded && 'flex-wrap']"
        >
          <span
            class="min-w-0"
            :class="[textClass, expanded ? 'break-all whitespace-normal' : 'truncate']"
            :style="textStyle"
          >
            {{ text }}
          </span>
          <span
            v-if="source"
            class="text-base-content/50 text-[10px]"
            :class="expanded ? 'break-all' : 'truncate'"
          >
            {{ source }}
          </span>
        </span>
        <span
          v-if="overflowing"
          class="pointer-events-none absolute top-0 right-0 flex h-4 w-4 items-center justify-center opacity-70"
          aria-hidden="true"
        >
          <ChevronDownIcon
            class="h-3.5 w-3.5 transition-transform duration-200"
            :class="expanded && 'rotate-180'"
          />
        </span>
      </div>
      <slot name="suffix" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ChevronDownIcon } from '@heroicons/vue/24/outline'
import { useResizeObserver } from '@vueuse/core'
import { nextTick, onMounted, ref, watch, type StyleValue } from 'vue'

const props = defineProps<{
  text: string
  label?: string
  row?: boolean
  source?: string
  textClass?: string
  textStyle?: StyleValue
}>()

const host = ref<HTMLElement>()
const probe = ref<HTMLElement>()
const expanded = ref(false)
const overflowing = ref(false)

const toggle = () => {
  if (overflowing.value) expanded.value = !expanded.value
}

const measure = () => {
  if (!probe.value?.isConnected || !probe.value.clientWidth) return
  overflowing.value = probe.value.scrollWidth > probe.value.clientWidth
  if (!overflowing.value) expanded.value = false
}

useResizeObserver(host, measure)
watch(
  () => [props.text, props.source],
  async () => {
    expanded.value = false
    await nextTick()
    measure()
  },
)
onMounted(() => {
  measure()
  void document.fonts?.ready.then(measure)
})
</script>
