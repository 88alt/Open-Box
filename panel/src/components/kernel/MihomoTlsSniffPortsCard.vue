<template>
  <div class="card bg-base-100 border-base-300/60 border">
    <div class="card-body gap-3 p-4">
      <div class="flex flex-col gap-1">
        <h2 class="text-base font-semibold">{{ $t('mihomoTlsSniffPortsTitle') }}</h2>
        <p class="text-base-content/60 text-xs">{{ $t('mihomoTlsSniffPortsDescription') }}</p>
      </div>
      <TagInput
        v-model="tags"
        :label="$t('mihomoTlsSniffPortsTitle')"
        placeholder="8443, 9443-9450"
        :separator="ADDRESS_SEPARATOR"
        @commit="save"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
// Mihomo 额外 TLS 嗅探端口(对齐上游 v0.1.477):内核是 Mihomo 时,默认只嗅探 TCP 443,
// 这里加的端口或范围也一起嗅探。标签提交时整份存进档案 mihomoTlsSniffPorts。
import type { OpenboxProfile } from '@/api/openbox'
import TagInput from '@/components/common/TagInput.vue'
import { ADDRESS_SEPARATOR } from '@/helper/tagInput'
import { showNotification } from '@/helper/notification'
import { ref, watch } from 'vue'

const props = defineProps<{
  profile: OpenboxProfile
  patchProfile: (patch: Record<string, unknown>) => Promise<OpenboxProfile>
}>()

// 「8443, 9443-9450」:单个端口或范围,逗号隔开(服务端是最终裁判)
const PORT_SPEC = /^\d{1,5}(-\d{1,5})?(\s*,\s*\d{1,5}(-\d{1,5})?)*$/

const split = (text: string | undefined) =>
  (text || '')
    .split(ADDRESS_SEPARATOR)
    .map((item) => item.trim())
    .filter(Boolean)

const tags = ref<string[]>(split(props.profile.mihomoTlsSniffPorts))
watch(
  () => props.profile.mihomoTlsSniffPorts,
  (next, prev) => {
    // 本地改动还没落盘时不被旧档案覆盖:只有和上一份服务端值一致时才跟着变
    if (tags.value.join(',') === split(prev).join(',')) tags.value = split(next)
  },
)

// 连续提交排队,保证按顺序写
let queue: Promise<void> = Promise.resolve()
const save = (next: string[]) => {
  const text = next.join(',')
  if (text && !PORT_SPEC.test(text)) {
    showNotification({ content: 'bypassPortsInvalid', type: 'alert-error' })
    return
  }
  queue = queue.then(async () => {
    if (text === split(props.profile.mihomoTlsSniffPorts).join(',')) return
    try {
      await props.patchProfile({ mihomoTlsSniffPorts: text })
    } catch (error) {
      showNotification({
        content: 'routingSaveFailed',
        params: { message: error instanceof Error ? error.message : String(error) },
        type: 'alert-error',
      })
    }
  })
}
</script>
