<template>
  <!-- 订阅多选筛选:触发器是一颗 input 样式的胶囊,点开是带复选框的弹层。
       不用原生 <select>:原生的只能单选,也放不进「A +2」这种摘要。
       弹层 Teleport 到 #app-content、fixed 定位(见 composables/anchoredDropdown.ts),
       留在原地会被弹窗的 overflow 裁掉。
       v-model 是选中值的数组,空数组 = 全部。 -->
  <button
    ref="triggerRef"
    v-bind="$attrs"
    type="button"
    class="input input-xs flex min-w-0 cursor-pointer items-center gap-1 px-3 text-left"
    :aria-label="$t('subscriptions')"
    :aria-expanded="open"
    :title="selectedLabels.join('、') || $t('groupFilterAllSubscriptions')"
    @click="toggle"
  >
    <span class="min-w-0 flex-1 truncate">{{ summary }}</span>
    <ChevronDownIcon
      :class="['h-3.5 w-3.5 shrink-0 transition-transform duration-150', open && 'rotate-180']"
    />
  </button>
  <Teleport to="#app-content">
    <div
      v-if="open"
      ref="panelRef"
      role="group"
      :aria-label="$t('subscriptions')"
      class="app-popover border-base-content/10 z-[1000] overflow-y-auto overscroll-contain rounded-lg border p-2 shadow-lg"
      :style="style"
    >
      <label
        class="hover:bg-base-200 flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm"
      >
        <input
          type="checkbox"
          class="checkbox checkbox-xs shrink-0"
          :checked="!modelValue.length"
          @change="emit('update:modelValue', [])"
        />
        <span>{{ $t('groupFilterAllSubscriptions') }}</span>
      </label>
      <label
        v-for="option in options"
        :key="option.value"
        class="hover:bg-base-200 flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm"
      >
        <input
          type="checkbox"
          class="checkbox checkbox-xs shrink-0"
          :checked="modelValue.includes(option.value)"
          @change="toggleValue(option.value)"
        />
        <span
          class="min-w-0 truncate"
          :title="option.label"
          >{{ option.label }}</span
        >
      </label>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { useAnchoredDropdown } from '@/composables/anchoredDropdown'
import { ChevronDownIcon } from '@heroicons/vue/24/outline'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

defineOptions({ inheritAttrs: false })

const props = defineProps<{
  modelValue: string[]
  options: { value: string; label: string }[]
}>()

const emit = defineEmits<{
  'update:modelValue': [string[]]
}>()

const { t } = useI18n()
const { open, triggerRef, panelRef, style, toggle } = useAnchoredDropdown({
  minWidth: 192,
  maxHeight: 360,
})

// 选中值对应的显示名;选项里已经没有的值(订阅被删了)直接显示原值
const selectedLabels = computed(() =>
  props.modelValue.map((v) => props.options.find((o) => o.value === v)?.label || v),
)

// 摘要:没选 = 「全部」;选一个 = 它的名字;多个 = 「第一个 +N」
const summary = computed(() => {
  const labels = selectedLabels.value
  if (!labels.length) return t('groupFilterAllSubscriptions')
  return labels.length === 1 ? labels[0] : `${labels[0]} +${labels.length - 1}`
})

// 按选项原本的顺序回写,不按点击顺序
const toggleValue = (value: string) => {
  const next = new Set(props.modelValue)
  if (next.has(value)) next.delete(value)
  else next.add(value)
  emit(
    'update:modelValue',
    props.options.filter((o) => next.has(o.value)).map((o) => o.value),
  )
}
</script>
