<template>
  <DialogWrapper
    v-model="isOpen"
    :title="$t('connectionDeviceRenameTitle')"
    box-class="w-full max-w-md"
  >
    <div class="flex flex-col gap-[var(--app-card-space)]">
      <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
        <dt class="text-base-content/65">{{ $t('connectionDeviceIP') }}</dt>
        <dd class="break-all">{{ ip }}</dd>
        <dt class="text-base-content/65">{{ $t('connectionDeviceMAC') }}</dt>
        <dd class="tabular-nums">{{ mac || '—' }}</dd>
        <dt class="text-base-content/65">{{ $t('connectionDeviceHostname') }}</dt>
        <dd class="break-all">{{ hostname || '—' }}</dd>
      </dl>
      <label class="flex flex-col gap-2 text-sm">
        <span>{{ $t('connectionDeviceAlias') }}</span>
        <input
          v-model="alias"
          class="input input-sm w-full"
          :placeholder="ip"
          @keydown.enter.prevent="save"
        />
      </label>
      <p class="text-base-content/65 text-xs">
        {{ $t(mac ? 'connectionDeviceRenameByMAC' : 'connectionDeviceRenameByIP') }}
      </p>
    </div>
    <template #footer>
      <button
        type="button"
        class="btn btn-sm"
        @click="isOpen = false"
      >
        {{ $t('cancel') }}
      </button>
      <button
        type="button"
        class="btn btn-sm btn-primary"
        :disabled="!canSave"
        @click="save"
      >
        {{ $t('save') }}
      </button>
    </template>
  </DialogWrapper>
</template>

<script setup lang="ts">
import { showNotification } from '@/helper/notification'
import {
  getDeviceName,
  getKnownClient,
  normalizeMac,
  refreshKnownClients,
  type DeviceRenameTarget,
} from '@/helper/sourceip'
import { sourceIPLabelList } from '@/store/settings'
import { v4 as uuid } from 'uuid'
import { computed, ref, watch } from 'vue'
import DialogWrapper from '../common/DialogWrapper.vue'

const target = defineModel<DeviceRenameTarget | null>({ required: true })

const ip = computed(() => target.value?.ip ?? '')
const client = computed(() => getKnownClient(ip.value))
const hostname = computed(() => client.value?.name || target.value?.hostname || '')
// 有 MAC 就按 MAC 保存(换 IP 也跟着走),没有才按 IP
const mac = computed(() => normalizeMac(client.value?.mac))
const isOpen = computed({
  get: () => !!ip.value,
  set: (open) => {
    if (!open) target.value = null
  },
})

const currentName = computed(() => getDeviceName(ip.value, hostname.value))
const draft = ref<string | null>(null)
const alias = computed({
  get: () => draft.value ?? currentName.value,
  set: (value) => {
    draft.value = value
  },
})
// 打开时还不知道 MAC,先强制刷新一次邻居表,刷新完再让保存:免得按 IP 存了一个会过期的名字
const resolving = ref(false)
const trimmed = computed(() => alias.value.trim())
const canSave = computed(
  () => !!ip.value && !!trimmed.value && trimmed.value !== currentName.value.trim() && !resolving.value,
)

watch(ip, async (value, _old, onCleanup) => {
  draft.value = null
  resolving.value = !!(value && !mac.value)
  if (!resolving.value) return

  let alive = true

  onCleanup(() => {
    alive = false
  })
  await refreshKnownClients(true)
  if (alive) resolving.value = false
})

const save = () => {
  if (!canSave.value) return

  const key = mac.value || ip.value
  const labels = sourceIPLabelList.value
  const index = labels.map((item) => normalizeMac(item.key) || item.key).lastIndexOf(key)

  if (index === -1) {
    sourceIPLabelList.value = [...labels, { id: uuid(), key, label: trimmed.value }]
  } else {
    sourceIPLabelList.value = labels.map((item, i) =>
      i === index ? { ...item, label: trimmed.value } : item,
    )
  }
  isOpen.value = false
  showNotification({ content: 'connectionDeviceRenameSaved', type: 'alert-success', timeout: 2000 })
}
</script>
