<template>
  <div class="card bg-base-100 border-base-300/60 border">
    <div class="card-body gap-3 p-4">
      <h2 class="text-base font-semibold">{{ $t('bandwidthSettingsTitle') }}</h2>

      <div class="flex flex-wrap items-end gap-4 text-sm">
        <label class="flex flex-col gap-1">
          <span class="text-xs font-medium">{{ $t('bandwidthPeriod') }}</span>
          <span class="flex items-center gap-2">
            <input
              v-model.number="intervalHours"
              type="number"
              :min="MIN_HOURS"
              :max="MAX_HOURS"
              class="input input-sm w-24"
              @change="save"
            />
            {{ $t('bandwidthHours') }}
          </span>
        </label>
        <label class="flex flex-col gap-1">
          <span class="text-xs font-medium">{{ $t('bandwidthDownloadCap') }}</span>
          <span class="flex items-center gap-2">
            <input
              v-model.number="maxDownloadMB"
              type="number"
              :min="MIN_MB"
              :max="MAX_MB"
              class="input input-sm w-24"
              @change="save"
            />
            MB
          </span>
        </label>
      </div>

      <p class="text-base-content/70 text-xs leading-relaxed">
        {{ $t('bandwidthDaily', { count: nodeCount, times: timesPerDay, size: dailyText }) }}
      </p>
      <p class="text-base-content/60 text-xs leading-relaxed">{{ $t('bandwidthBudgetHint') }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { fetchBandwidthStatus, type OpenboxBandwidthSettings, type OpenboxProfile } from '@/api/openbox'
import { showNotification } from '@/helper/notification'
import { computed, onMounted, ref, watch } from 'vue'

// 范围和后端 / 上游一致:周期 1~24 小时,单次下载上限 1~1024 MB
const MIN_HOURS = 1
const MAX_HOURS = 24
const DEFAULT_HOURS = 2
const MIN_MB = 1
const MAX_MB = 1024
const DEFAULT_MB = 64

const props = defineProps<{
  profile: OpenboxProfile
  patchProfile: (patch: Record<string, unknown>) => Promise<OpenboxProfile>
}>()

const hoursOf = (b?: Partial<OpenboxBandwidthSettings>) => b?.intervalHours ?? DEFAULT_HOURS
const mbOf = (b?: Partial<OpenboxBandwidthSettings>) => b?.maxDownloadMB ?? DEFAULT_MB

const intervalHours = ref<number>(hoursOf(props.profile.bandwidth))
const maxDownloadMB = ref<number>(mbOf(props.profile.bandwidth))
// 当前要测的有效节点数,由后端按已启用带宽择优的组算出来
const nodeCount = ref(0)

// 每天测几轮 = 24 小时 / 周期,向上取整;每日下载量上限 = 节点数 × 单次上限 × 轮数
const timesPerDay = computed(() => Math.ceil(24 / Math.max(1, Number(intervalHours.value) || DEFAULT_HOURS)))
const dailyText = computed(
  () => `${((nodeCount.value * Math.max(1, Number(maxDownloadMB.value) || DEFAULT_MB) * timesPerDay.value) / 1e3).toFixed(2)} GB`,
)

watch(
  () => props.profile.bandwidth,
  (b) => {
    intervalHours.value = hoursOf(b)
    maxDownloadMB.value = mbOf(b)
  },
)

onMounted(() => {
  fetchBandwidthStatus()
    .then((status) => {
      nodeCount.value = status.nodes
    })
    .catch(() => {})
})

const save = async () => {
  intervalHours.value = Math.max(MIN_HOURS, Math.min(MAX_HOURS, Math.round(Number(intervalHours.value) || DEFAULT_HOURS)))
  maxDownloadMB.value = Math.max(MIN_MB, Math.min(MAX_MB, Math.round(Number(maxDownloadMB.value) || DEFAULT_MB)))
  try {
    await props.patchProfile({
      bandwidth: { intervalHours: intervalHours.value, maxDownloadMB: maxDownloadMB.value },
    })
  } catch (e) {
    intervalHours.value = hoursOf(props.profile.bandwidth)
    maxDownloadMB.value = mbOf(props.profile.bandwidth)
    showNotification({ content: (e as Error).message, type: 'alert-error' })
  }
}
</script>
