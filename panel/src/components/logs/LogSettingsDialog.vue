<template>
  <DialogWrapper
    v-model="logSettingsOpen"
    :title="$t('logSettings')"
    box-class="w-full max-w-md"
  >
    <div class="flex flex-col gap-3 text-sm">
      <label class="flex items-center justify-between gap-3">
        <span>{{ $t('connectionStylePC') }}</span>
        <select
          ref="firstRef"
          v-model="logLayoutPC"
          class="select select-sm w-24"
          :aria-label="$t('connectionStylePC')"
        >
          <option value="table">{{ $t('table') }}</option>
          <option value="card">{{ $t('card') }}</option>
        </select>
      </label>
      <label class="flex items-center justify-between gap-3">
        <span>{{ $t('connectionStyleMobile') }}</span>
        <select
          v-model="logLayoutMobile"
          class="select select-sm w-24"
          :aria-label="$t('connectionStyleMobile')"
        >
          <option value="table">{{ $t('table') }}</option>
          <option value="card">{{ $t('card') }}</option>
        </select>
      </label>
      <label class="flex items-center justify-between gap-3">
        <span>{{ $t('logReceiveLevel') }}</span>
        <LogLevelSelect />
      </label>
    </div>
  </DialogWrapper>
</template>

<script setup lang="ts">
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import { LogLevelSelect } from '@/components/sidebar/LogsCtrl'
import { logLayoutMobile, logLayoutPC, logSettingsOpen } from '@/store/logs'
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

const firstRef = ref<HTMLElement | null>(null)

watch(logSettingsOpen, async (open) => {
  if (!open) return
  await nextTick()
  firstRef.value?.focus()
})
// 离开日志页时把弹窗关掉,免得回来时它还开着
onBeforeUnmount(() => {
  logSettingsOpen.value = false
})
</script>
