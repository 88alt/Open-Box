<template>
  <!-- 升级进度弹窗。挂在 App 根上(不在「后端设置」那一页里):升级期间面板会重启一次、
       内核再重启一次,用户待在哪一页都该看得见进度,刷新或重新登录之后也要接着显示。 -->
  <DialogWrapper
    v-model="updateDialogOpen"
    :title="$t('obUpdateDialogTitle')"
    box-class="w-full max-w-2xl"
    content-class="flex flex-col overflow-hidden!"
  >
    <div class="flex min-h-0 flex-col gap-3 text-sm">
      <div class="flex items-center gap-2">
        <span
          v-if="updateRunning"
          class="loading loading-spinner loading-xs"
        />
        <span>{{ stageText }}</span>
        <span
          v-if="updateProgress?.message"
          class="text-base-content/60 truncate text-xs"
        >{{ updateProgress.message }}</span>
      </div>
      <progress
        class="progress progress-primary w-full"
        :value="updatePercent ?? undefined"
        max="100"
      />
      <!-- 上游 v0.1.493:升级时多显示三行(当前下载源 / 下载进度 / 切源记录)。
           后端没给就不显示那一行,旧后端也能正常跑。 -->
      <div
        v-if="progressSource || progressTransfer || progressAttempts"
        class="flex flex-col gap-0.5"
      >
        <div
          v-if="progressSource"
          class="flex flex-wrap items-center justify-between gap-2"
        >
          <span class="text-base-content/60">{{ $t('obUpdateSourceLabel') }}</span>
          <span class="font-mono tabular-nums">{{ progressSource }}</span>
        </div>
        <div
          v-if="progressTransfer || progressAttempts"
          class="flex flex-wrap items-center justify-between gap-2"
        >
          <span class="text-base-content/60">{{ $t('obUpdateTransferLabel') }}</span>
          <span class="font-mono tabular-nums">
            <template v-if="progressTransfer">{{ progressTransfer }}</template>
            <template v-if="progressTransfer && updatePercent !== null"> ({{ updatePercent }}%)</template>
            <template v-if="progressTransfer && progressAttempts"> · </template>
            <template v-if="progressAttempts">{{ progressAttempts }}</template>
          </span>
        </div>
        <div
          v-if="progressAttempts"
          class="flex flex-wrap items-center justify-between gap-2"
        >
          <span class="text-base-content/60">{{ $t('obUpdateAttemptsLabel') }}</span>
          <span class="text-base-content/70 font-mono break-all">{{ progressAttempts }}</span>
        </div>
      </div>
      <p class="text-base-content/60 text-xs">{{ $t('obUpdateDialogHint') }}</p>
      <pre
        v-if="updateInfo?.logTail"
        class="bg-base-200/60 max-h-64 min-h-0 overflow-auto rounded-lg p-2 font-mono text-xs whitespace-pre-wrap"
      >{{ updateInfo.logTail }}</pre>
    </div>
    <template #footer>
      <button
        v-if="updateRunning"
        type="button"
        class="btn btn-sm"
        :disabled="!updateCancellable"
        @click="cancelRunningUpdate"
      >
        {{ $t('cancel') }}
      </button>
      <button
        type="button"
        class="btn btn-sm"
        @click="updateDialogOpen = false"
      >
        {{ $t(updateRunning ? 'obUpdateDialogHide' : 'close') }}
      </button>
    </template>
  </DialogWrapper>
</template>

<script setup lang="ts">
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import {
  cancelRunningUpdate,
  updateCancellable,
  updateDialogOpen,
  updateInfo,
  updatePercent,
  updateProgress,
  updateRunning,
} from '@/composables/openboxUpdate'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

// 上游 v0.1.493 的三行:当前下载源 / 下载进度 / 切源记录。后端没给就是空串,模板里整行不显示
const progressSource = computed(() => updateProgress.value?.source || '')
const progressTransfer = computed(() => updateProgress.value?.transfer || '')
const progressAttempts = computed(() => updateProgress.value?.attempts || '')

const stageText = computed(() => {
  const stage = updateProgress.value?.stage || ''
  const key = `obUpdateStage_${stage}`
  const text = t(key)
  return text === key ? stage : text
})
</script>
