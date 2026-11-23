<template>
  <DialogWrapper
    v-model="open"
    modal-class="z-[1000]"
    :title="nodeName || $t('subscriptionNodeShare')"
    box-class="w-full max-w-md"
  >
    <div
      ref="contentRef"
      class="flex flex-col gap-3 text-sm"
    >
      <div
        v-if="loading"
        class="flex justify-center py-6"
        role="status"
      >
        <span class="loading loading-spinner loading-sm" />
      </div>

      <p
        v-else-if="error"
        class="border-error/30 bg-error/10 text-error rounded-lg border px-3 py-2"
      >
        {{ error }}
      </p>

      <template v-else-if="link">
        <div class="join w-full">
          <input
            :value="link"
            type="text"
            readonly
            class="input input-sm join-item w-full font-mono text-xs"
            @focus="($event.target as HTMLInputElement).select()"
          />
          <button
            type="button"
            class="btn btn-sm join-item"
            :aria-label="$t('copyLink')"
            v-tip="$t('copyLink')"
            @click="copy"
          >
            <ClipboardDocumentIcon class="h-4 w-4" />
          </button>
        </div>

        <img
          v-if="qrDataUrl"
          :src="qrDataUrl"
          :style="{ width: `${qrWidth}px` }"
          class="max-w-full self-center rounded-lg bg-white p-1 [image-rendering:pixelated]"
          alt="QR"
        />
        <p
          v-else-if="qrTooLong"
          class="text-base-content/60 text-xs"
        >
          {{ $t('subscriptionNodeShareQrTooLong') }}
        </p>
      </template>
    </div>
  </DialogWrapper>
</template>

<script setup lang="ts">
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import { copyText } from '@/helper/clipboard'
import { showNotification } from '@/helper/notification'
import { ClipboardDocumentIcon } from '@heroicons/vue/24/outline'
import { useElementSize } from '@vueuse/core'
import QRCode from 'qrcode'
import { computed, ref, watch } from 'vue'

const props = defineProps<{
  // 弹窗标题(节点名);空时用「分享」
  nodeName: string
  link: string
  loading: boolean
  error: string
}>()

const open = defineModel<boolean>({ required: true })

// 二维码一边的格子数(含四周各 4 格留白)。显示成整数倍像素,边缘才清晰;链接太长画不出来时给一句提示
const QR_MARGIN = 4
const QR_PADDING = 8 // 图片的 p-1:左右各 4px
const qrDataUrl = ref('')
const qrCells = ref(0)
const qrTooLong = ref(false)
const contentRef = ref<HTMLElement>()
const { width: contentWidth } = useElementSize(contentRef)
const qrWidth = computed(() => {
  const cells = qrCells.value
  if (!cells) return 256
  const wanted = Math.max(3, Math.ceil(248 / cells))
  const fit = contentWidth.value > 0 ? Math.max(1, Math.floor((contentWidth.value - QR_PADDING) / cells)) : wanted
  return cells * Math.min(wanted, fit) + QR_PADDING
})

watch(
  () => props.link,
  async (value) => {
    qrTooLong.value = false
    if (!value) {
      qrDataUrl.value = ''
      qrCells.value = 0
      return
    }
    try {
      qrCells.value = QRCode.create(value).modules.size + QR_MARGIN * 2
      qrDataUrl.value = await QRCode.toDataURL(value, { margin: QR_MARGIN, scale: 6 })
    } catch {
      qrDataUrl.value = ''
      qrCells.value = 0
      qrTooLong.value = true
    }
  },
  { immediate: true },
)

const copy = async () => {
  if (!props.link) return
  const ok = await copyText(props.link)
  showNotification({ content: ok ? 'copySuccess' : 'copyFailed', type: ok ? 'alert-success' : 'alert-error' })
}
</script>
