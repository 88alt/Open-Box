<template>
  <!-- 重启按钮:侧边栏(icon-only)和后端设置的内核卡片共用一个。有改动要重启才生效时:
       能列出具体改动就在图标右上角挂红色数字角标、提示里逐条列出(上游 v0.1.486);
       列不出来(老服务端 / 没有对应文案)退回红色感叹号和「重启(有待生效的改动)」(上游 v0.1.470) -->
  <button
    v-tip="tip"
    type="button"
    class="btn btn-sm relative"
    :class="iconOnly && 'btn-square bg-base-100 h-9 w-9 kernel-action-btn'"
    :disabled="isRestartDisabled"
    :aria-label="label"
    @click="runKernelAction('restart')"
  >
    <span
      v-if="pendingAction === 'restart'"
      class="loading loading-spinner loading-xs"
    />
    <ArrowPathIcon
      v-else
      class="h-4 w-4"
    />
    <template v-if="!iconOnly">{{ $t('kernelActionRestart') }}</template>
    <span
      v-if="restartPendingSignature && pendingReasons.length"
      aria-hidden="true"
      class="bg-error pointer-events-none absolute -top-1 -right-1 flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-[10px] leading-none font-semibold text-white shadow-sm"
    >
      {{ pendingReasons.length }}
    </span>
    <ExclamationCircleIcon
      v-else-if="restartPendingSignature"
      aria-hidden="true"
      class="bg-base-100 text-error pointer-events-none absolute -top-1 -right-1 h-4 w-4 rounded-full"
    />
  </button>
</template>

<script setup lang="ts">
import {
  isRestartDisabled,
  pendingAction,
  restartPendingReasons,
  restartPendingSignature,
  useKernelActions,
} from '@/composables/kernelService'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import { ExclamationCircleIcon } from '@heroicons/vue/16/solid'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  iconOnly?: boolean
}>()

const { t } = useI18n()
const { runKernelAction } = useKernelActions()
const pendingReasons = computed(() => restartPendingReasons(t))
// 提示(icon-only 时的悬浮提示):有具体改动 → 「需要重启内核以生效:」加逐条列表;没有 → 老说法
const tip = computed(() => {
  if (!restartPendingSignature.value) return props.iconOnly ? t('kernelActionRestart') : undefined
  if (!pendingReasons.value.length) return t('kernelActionRestartPending')
  return `${t('kernelActionRestartPendingHeader')}\n${pendingReasons.value.map((r) => `· ${r}`).join('\n')}`
})
// 读屏用的标签:一行说完
const label = computed(() => {
  if (!restartPendingSignature.value) return t('kernelActionRestart')
  if (!pendingReasons.value.length) return t('kernelActionRestartPending')
  return `${t('kernelActionRestartPendingHeader')} ${pendingReasons.value.join(t('restartPendingReasonSeparator'))}`
})
</script>
