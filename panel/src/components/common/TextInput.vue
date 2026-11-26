<template>
  <div class="relative">
    <button
      v-if="beforeClose && canClear"
      type="button"
      :class="clearButtonClass"
      :aria-label="$t('clearInput')"
      @click.stop="clearInput"
    >
      <XMarkIcon
        class="size-4"
        aria-hidden="true"
      />
    </button>
    <span
      v-if="hasPrefix"
      class="pointer-events-none absolute inset-y-0 left-3 z-10 flex items-center"
      aria-hidden="true"
    >
      <slot name="prefix" />
    </span>
    <input
      v-model="inputValue"
      ref="inputRef"
      type="text"
      :class="['input join-item w-full', inputClass, inputPaddingClass, hasPrefix && 'pl-9']"
      :aria-label="ariaLabel"
      :placeholder="placeholder || ''"
      :maxlength="maxlength"
      :name="name || ''"
      :autocomplete="autocomplete || ''"
      @click="handlerSearchInputClick"
      @input="(emits('input', inputValue || ''), hideTip())"
      @change="emits('change', inputValue || '')"
    />
    <div
      v-if="hasSuffix"
      class="absolute top-1/2 right-1 z-10 -translate-y-1/2"
    >
      <slot name="suffix" />
    </div>
    <button
      v-if="canClear"
      type="button"
      :class="clearButtonClass"
      :aria-label="$t('clearInput')"
      @click.stop="clearInput"
    >
      <XMarkIcon
        class="size-4"
        aria-hidden="true"
      />
    </button>
  </div>
</template>

<script lang="ts" setup>
import { useTooltip } from '@/helper/tooltip'
import { XMarkIcon } from '@heroicons/vue/24/outline'
import { computed, createApp, defineComponent, h, ref, useSlots } from 'vue'

const emits = defineEmits<{
  (e: 'input', value: string): void
  (e: 'change', value: string): void
  (e: 'clear'): void
  (e: 'update:menus', value: string[]): void
}>()

const props = withDefaults(
  defineProps<{
    placeholder?: string
    beforeClose?: boolean
    ariaLabel?: string
    inputClass?: string
    name?: string
    autocomplete?: string
    maxlength?: number | string
    clearable?: boolean
    menus?: string[]
    menusDeleteable?: boolean
  }>(),
  { inputClass: 'input-sm' },
)

const inputValue = defineModel<string>()
const slots = useSlots()
const hasPrefix = computed(() => Boolean(slots.prefix))
const hasSuffix = computed(() => Boolean(slots.suffix))
// 和上游一致:清除按钮只在「可清除」并且**已经输入了内容**时才出现,右侧内边距跟着它走。
// 之前是只看 clearable,空输入框也留出 32px 右侧内边距、还显示一个清除按钮,和上游不一样。
const canClear = computed(() => Boolean(props.clearable && inputValue.value))
const inputPaddingClass = computed(() => {
  if (canClear.value && hasSuffix.value) {
    return 'pr-14'
  }

  if (canClear.value || hasSuffix.value) {
    return 'pr-8'
  }

  return ''
})
const clearButtonClass = computed(() => {
  return [
    'hover:bg-base-content/10 focus-visible:outline-base-content absolute top-1/2 z-10 grid size-6 -translate-y-1/2 place-items-center rounded-full focus-visible:outline',
    hasSuffix.value ? 'right-8' : 'right-1',
  ]
})

const { showTip, hideTip } = useTooltip()
const inputRef = ref<HTMLInputElement>()
const clearInput = () => {
  inputValue.value = ''
  emits('input', '')
  emits('change', '')
  emits('clear')
  hideTip()
  inputRef.value?.focus()
}

const handlerSearchInputClick = (e: Event) => {
  if (!props.menus?.length) {
    return
  }
  const PopContent = defineComponent({
    props: {
      menus: {
        type: Array,
        default: () => [],
      },
      menusDeleteable: {
        type: Boolean,
        default: false,
      },
    },
    setup(props: { menus: string[]; menusDeleteable: boolean }) {
      return () =>
        h(
          'div',
          { class: 'max-h-64 overflow-y-auto overflow-x-hidden scrollbar-hidden min-w-24 py-1' },
          props.menus.map((item) =>
            h(
              'div',
              {
                class:
                  'cursor-pointer rounded-sm p-1 px-3 flex gap-2 items-center overflow-hidden hover:bg-base-300',
              },
              [
                h(
                  'span',
                  {
                    class: 'flex-1 truncate',
                    onClick: () => {
                      inputValue.value = item
                      hideTip()
                      inputRef.value?.focus()
                    },
                  },
                  item,
                ),
                props.menusDeleteable &&
                  h(XMarkIcon, {
                    class: 'h-3 w-3 transition-transform hover:scale-125',
                    onClick: (e) => {
                      const target = e.target as HTMLElement

                      emits(
                        'update:menus',
                        props.menus.filter((menu) => menu !== item),
                      )
                      target.closest('div')?.remove()
                    },
                  }),
              ],
            ),
          ),
        )
    },
  })
  const mountEl = document.createElement('div')
  const app = createApp(PopContent, {
    menus: props.menus,
    menusDeleteable: props.menusDeleteable,
  })

  app.mount(mountEl)

  showTip(e, mountEl, {
    theme: 'base',
    placement: 'bottom-start',
    trigger: 'click',
    interactive: true,
    appendTo: document.body,
    arrow: false,
  })
}
</script>
