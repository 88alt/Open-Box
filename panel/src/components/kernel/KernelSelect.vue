<template>
  <!-- 内核切换下拉(对齐上游 v0.1.487 的 KernelSelect):不用原生 <select>,
       触发按钮左边带当前内核图标、右边带小箭头;面板 Teleport 到 #app-content 并固定定位,
       每一项也带图标,当前项打勾。 -->
  <button
    id="router-kernel"
    ref="triggerRef"
    type="button"
    class="input input-sm inline-flex w-auto cursor-pointer gap-1.5 disabled:cursor-default"
    :disabled="disabled"
    :aria-expanded="open"
    :aria-controls="open ? 'router-kernel-options' : undefined"
    aria-labelledby="router-kernel-label router-kernel-value"
    @click="onToggle"
    @keydown.down.prevent="onOpenByKey"
    @keydown.esc="close"
  >
    <KernelIcon :kernel="modelValue" />
    <span
      id="router-kernel-value"
      class="min-w-[7ch] text-start"
    >{{ KERNEL_NAMES[modelValue] }}</span>
    <ChevronDownIcon
      class="size-3.5 shrink-0"
      aria-hidden="true"
    />
  </button>
  <Teleport to="#app-content">
    <div
      v-if="open"
      id="router-kernel-options"
      ref="panelRef"
      class="app-popover border-base-content/10 z-[1000] rounded-lg border p-1.5 text-sm"
      :style="style"
      role="group"
      aria-labelledby="router-kernel-label"
      @focusout="onFocusOut"
      @keydown.esc="onEsc"
      @keydown.down.prevent="moveFocus(1)"
      @keydown.up.prevent="moveFocus(-1)"
    >
      <button
        v-for="k in KERNELS"
        :key="k"
        type="button"
        class="hover:bg-base-200 focus-visible:bg-base-200 flex w-full cursor-pointer items-center gap-1.5 rounded-md px-2 py-1.5 text-start"
        :class="k === modelValue && 'bg-base-200'"
        :aria-pressed="k === modelValue"
        @click="choose(k)"
      >
        <KernelIcon :kernel="k" />
        <span class="flex-1 whitespace-nowrap">{{ KERNEL_NAMES[k] }}</span>
        <CheckIcon
          v-if="k === modelValue"
          class="size-3.5 shrink-0"
          aria-hidden="true"
        />
      </button>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import KernelIcon from '@/components/kernel/KernelIcon.vue'
import type { OpenboxRouterKernel } from '@/api/openbox'
import { useAnchoredDropdown } from '@/composables/anchoredDropdown'
import { CheckIcon, ChevronDownIcon } from '@heroicons/vue/24/outline'
import { nextTick } from 'vue'

defineProps<{ modelValue: OpenboxRouterKernel; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [kernel: OpenboxRouterKernel] }>()

const KERNELS: OpenboxRouterKernel[] = ['sing-box', 'mihomo']
// 显示名与上游一致:Sing-Box / Mihomo
const KERNEL_NAMES: Record<OpenboxRouterKernel, string> = { 'sing-box': 'Sing-Box', mihomo: 'Mihomo' }

const { open, triggerRef, panelRef, style, toggle, close } = useAnchoredDropdown({ minWidth: 160 })

// 打开后把焦点放到当前项(没有就放到第一项),方向键才能接着走
const focusCurrent = async () => {
  await nextTick()
  const panel = panelRef.value
  const target =
    panel?.querySelector<HTMLElement>('button[aria-pressed="true"]:enabled') ??
    panel?.querySelector<HTMLElement>('button:enabled')
  target?.focus()
}

const onToggle = async () => {
  toggle()
  if (open.value) await focusCurrent()
}
const onOpenByKey = async () => {
  if (open.value) return
  toggle()
  await focusCurrent()
}

const moveFocus = (step: number) => {
  const panel = panelRef.value
  if (!panel) return
  const buttons = Array.from(panel.querySelectorAll<HTMLElement>('button:enabled'))
  const index = buttons.findIndex((el) => el === document.activeElement)
  buttons[(index + step + buttons.length) % buttons.length]?.focus()
}

const onEsc = () => {
  close()
  triggerRef.value?.focus()
}

// 焦点移到触发按钮和面板之外就收起
const onFocusOut = (event: FocusEvent) => {
  const next = event.relatedTarget
  if (next instanceof Node && next !== triggerRef.value && !panelRef.value?.contains(next)) close()
}

const choose = (kernel: OpenboxRouterKernel) => {
  close()
  triggerRef.value?.focus()
  emit('update:modelValue', kernel)
}
</script>
