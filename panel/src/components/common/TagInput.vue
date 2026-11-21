<template>
  <div
    ref="field"
    :class="[
      'input input-sm flex h-auto min-h-8 w-full items-start gap-1 p-1',
      collapsed && 'max-h-9 overflow-hidden',
    ]"
  >
    <div
      :id="contentId"
      ref="content"
      class="relative flex min-w-0 flex-1 flex-wrap items-center gap-1"
    >
      <button
        v-for="(tag, index) in tags"
        :key="`${index}:${tag}`"
        type="button"
        :class="[
          'badge hover:bg-base-200 relative h-auto max-w-full shrink-0 cursor-pointer gap-1 p-1 ps-[7px] text-sm leading-4',
          collapsed && index >= visibleCount && 'invisible',
        ]"
        :disabled="disabled"
        :aria-label="$t('tagRemove', { item: tag })"
        :data-caret="caret === index ? '' : undefined"
        @mousedown.prevent
        @click="removeAndFocus(index)"
      >
        <span :class="['min-w-0', expanded ? 'break-all' : 'truncate']">{{ tag }}</span>
        <XMarkIcon
          class="size-4 shrink-0"
          aria-hidden="true"
        />
      </button>
      <input
        :id="inputId"
        ref="textInput"
        data-tag-input
        type="text"
        :class="[
          'min-h-6 w-24 min-w-0 max-w-full shrink-0 grow px-0 only:ps-2',
          collapsed && 'invisible',
          caret !== null && 'caret-transparent',
        ]"
        :disabled="disabled"
        :aria-label="label"
        :placeholder="tags.length ? undefined : placeholder"
        :value="pending"
        @click="caret = null"
        @input="onInput"
        @paste="onPaste"
        @compositionstart="caret = null"
        @compositionend="onInput"
        @blur="onBlur"
        @keydown.enter="onEnter"
        @keydown.left="moveCaret($event, -1)"
        @keydown.right="moveCaret($event, 1)"
        @keydown.backspace="onBackspace"
      />
    </div>
    <button
      v-if="clearable && pending && !disabled && !collapsed"
      ref="clearButton"
      type="button"
      class="hover:bg-base-content/10 focus-visible:outline-base-content grid size-6 shrink-0 place-items-center rounded-full focus-visible:outline"
      :aria-label="$t('clearInput')"
      @mousedown.prevent
      @click="clearPending"
      @blur="onBlur"
    >
      <XMarkIcon
        class="size-4"
        aria-hidden="true"
      />
    </button>
    <button
      v-if="overflowing"
      ref="toggleButton"
      type="button"
      class="btn btn-ghost btn-square btn-xs shrink-0"
      :aria-controls="contentId"
      :aria-expanded="expanded"
      :aria-label="$t(expanded ? 'tagCollapse' : 'tagExpand')"
      @click="expanded = !expanded"
    >
      <ChevronUpIcon
        v-if="expanded"
        class="size-4"
        aria-hidden="true"
      />
      <ChevronDownIcon
        v-else
        class="size-4"
        aria-hidden="true"
      />
    </button>
  </div>
</template>

<script setup lang="ts">
// 标签输入(对齐上游 v0.1.472 的 TagInput):回车 / 逗号 / 换行把输入变成标签,点标签移除;
// 粘贴多行文本按分隔符拆开;输入框最左时退格 / 方向键可以选中并删除前面的标签;
// 标签放不下一行时收起成一行,右侧按钮展开。pending 是还没提交成标签的输入,
// 外部在保存前调用 commit() 把它也收进去。
import { ChevronDownIcon, ChevronUpIcon, XMarkIcon } from '@heroicons/vue/24/outline'
import { useResizeObserver } from '@vueuse/core'
import { computed, nextTick, onMounted, ref, useId, watch } from 'vue'

const props = withDefaults(
  defineProps<{
    label?: string
    placeholder?: string
    inputId?: string
    separator?: RegExp
    unique?: boolean
    clearable?: boolean
    disabled?: boolean
  }>(),
  { separator: () => /[,，、\r\n]+/ },
)
const emit = defineEmits<{ commit: [tags: string[]] }>()
const tags = defineModel<string[]>({ default: () => [] })
const pending = defineModel<string>('pending', { default: '' })

const contentId = useId()
const field = ref<HTMLElement | null>(null)
const content = ref<HTMLElement | null>(null)
const textInput = ref<HTMLInputElement | null>(null)
const toggleButton = ref<HTMLButtonElement | null>(null)
const clearButton = ref<HTMLButtonElement | null>(null)
const expanded = ref(false)
const overflowing = ref(false)
const visibleCount = ref(0)
// 选中的标签下标(方向键移动后退格删除);null = 光标在输入框里
const caret = ref<number | null>(null)
const collapsed = computed(() => overflowing.value && !expanded.value)

const measure = () => {
  const box = field.value
  const row = content.value
  const input = textInput.value
  if (!box?.clientWidth || !row || !input) return
  const style = getComputedStyle(box)
  const inner = box.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight)
  const gap = parseFloat(getComputedStyle(row).columnGap) || 0
  const badges = Array.from(row.children).filter((el): el is HTMLButtonElement => el instanceof HTMLButtonElement)
  const minInput = Math.min(inner, 6 * parseFloat(getComputedStyle(document.documentElement).fontSize))
  const over = badges.reduce((sum, el) => sum + parseFloat(getComputedStyle(el).width) + gap, minInput) > inner + 0.5
  // 输入框正在输入时不要把它收起来
  if (over && !overflowing.value && document.activeElement === input) expanded.value = true
  overflowing.value = over
  const firstTop = badges[0]?.offsetTop
  const wrapAt = badges.findIndex((el) => el.offsetTop !== firstTop)
  visibleCount.value = wrapAt < 0 ? badges.length : wrapAt
  if (!over) expanded.value = false
}
useResizeObserver(content, measure)
watch(tags, () => nextTick(() => (tags.value.length ? measure() : ((overflowing.value = false), (expanded.value = false)))), { deep: true })
onMounted(measure)

const append = (items: string[]) => {
  const cleaned = items.map((s) => s.trim()).filter(Boolean)
  if (!cleaned.length) return tags.value
  const all = [...tags.value, ...cleaned]
  const next = props.unique ? [...new Set(all)] : all
  tags.value = next
  return next
}

// 输入里出现分隔符:分隔符之前的部分提交成标签,最后一段留在输入框
const splitInto = (el: HTMLInputElement, text: string) => {
  caret.value = null
  const parts = text.split(props.separator)
  const rest = parts.pop() ?? ''
  append(parts)
  pending.value = rest
  el.value = rest
}
const onInput = (e: Event) => {
  if ((e as InputEvent).isComposing) return
  const el = e.target as HTMLInputElement
  splitInto(el, el.value)
}
const onPaste = (e: ClipboardEvent) => {
  const text = e.clipboardData?.getData('text') ?? ''
  if (!/[\r\n]/.test(text)) return
  e.preventDefault()
  const el = e.target as HTMLInputElement
  const { value, selectionStart, selectionEnd } = el
  splitInto(el, value.slice(0, selectionStart ?? 0) + text + value.slice(selectionEnd ?? value.length))
}

const commit = () => {
  const next = append([pending.value])
  pending.value = ''
  if (textInput.value) textInput.value.value = ''
  caret.value = null
  emit('commit', next)
  return next
}
const onBlur = (e: FocusEvent) => {
  const to = e.relatedTarget
  if (to && (to === clearButton.value || to === textInput.value)) return
  commit()
}
const clearPending = () => {
  pending.value = ''
  caret.value = null
  textInput.value?.focus()
}
const onEnter = (e: KeyboardEvent) => {
  if (e.isComposing || e.keyCode === 229) return
  e.preventDefault()
  commit()
}

const removeAt = (index: number) => {
  tags.value = tags.value.filter((_, i) => i !== index)
}
const removeAndFocus = async (index: number) => {
  removeAt(index)
  caret.value = null
  await nextTick()
  ;(collapsed.value ? toggleButton.value : textInput.value)?.focus()
}

const atStart = () => textInput.value?.selectionStart === 0 && textInput.value.selectionEnd === 0
const moveCaret = (e: KeyboardEvent, step: number) => {
  if (e.isComposing || e.keyCode === 229 || e.shiftKey || e.ctrlKey || e.altKey || e.metaKey) return
  const count = tags.value.length
  if (!count || (caret.value === null && (step === 1 || !atStart()))) return
  e.preventDefault()
  const next = Math.max(0, (caret.value ?? count) + step)
  caret.value = next === count ? null : next
}
const onBackspace = (e: KeyboardEvent) => {
  if (e.isComposing || e.keyCode === 229 || !tags.value.length || (caret.value === null && !atStart())) return
  e.preventDefault()
  const at = caret.value ?? tags.value.length
  if (at === 0) return
  removeAt(at - 1)
  if (caret.value !== null) caret.value = at - 1
}

defineExpose({ commit })
</script>
