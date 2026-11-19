<template>
  <DialogWrapper
    v-model="isOpen"
    :title="$t('logDetails')"
    box-class="w-full max-w-2xl"
  >
    <template v-if="row">
      <div class="mb-3 flex flex-wrap items-center gap-2 text-sm">
        <LogLevelBadge :level="row.level" />
        <span
          v-if="row.log.probe"
          class="badge badge-xs"
        >
          {{ $t('logNodeProbe') }}
        </span>
        <span class="tabular-nums">{{ row.log.time }}</span>
        <span
          v-if="row.module"
          class="text-base-content/65 font-mono text-xs"
        >
          {{ row.module }}
        </span>
        <span class="text-base-content/65">#{{ row.log.seq }}</span>
      </div>
      <LogOutboundChain
        v-if="row.outboundChain?.length || row.routingPolicy"
        :chains="row.outboundChain ?? []"
        :policy="row.routingPolicy"
        class="mb-3 text-sm"
      />
      <p
        v-if="row.message !== row.log.rawPayload"
        class="mb-3 text-sm break-all"
      >
        {{ row.message }}
      </p>
      <pre
        ref="rawRef"
        tabindex="-1"
        :aria-label="$t('logOriginal')"
        class="bg-base-200 rounded-lg p-3 text-xs break-all whitespace-pre-wrap"
        >{{ row.log.rawPayload }}</pre
      >
    </template>
    <template #footer>
      <button
        type="button"
        class="btn btn-sm btn-primary"
        @click="copy"
      >
        {{ $t('logCopy') }}
      </button>
    </template>
  </DialogWrapper>
</template>

<script setup lang="ts">
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import LogLevelBadge from '@/components/logs/LogLevelBadge.vue'
import LogOutboundChain from '@/components/logs/LogOutboundChain.vue'
import type { LogRow } from '@/composables/logRows'
import { copyText } from '@/helper/clipboard'
import { showNotification } from '@/helper/notification'
import { computed, nextTick, ref, watch } from 'vue'

const row = defineModel<LogRow | null>({ required: true })

const isOpen = computed({
  get: () => row.value !== null,
  set: (open: boolean) => {
    if (!open) row.value = null
  },
})
const rawRef = ref<HTMLElement | null>(null)

// 打开后把焦点放到日志原文上,方向键能直接滚动、Ctrl+A 只选原文
watch(isOpen, async (open) => {
  if (!open) return
  await nextTick()
  rawRef.value?.focus()
})

const copy = async () => {
  if (!row.value) return

  const { seq, time, type, rawPayload } = row.value.log
  const ok = await copyText([seq, time, type, rawPayload].join('\t'))

  showNotification({
    content: ok ? 'copySuccess' : 'copyFailed',
    type: ok ? 'alert-success' : 'alert-error',
  })
}
</script>
