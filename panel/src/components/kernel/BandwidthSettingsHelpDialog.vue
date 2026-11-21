<template>
  <DialogWrapper
    v-model="open"
    :title="$t(section)"
    box-class="w-full max-w-xl"
    header-class="pr-10!"
  >
    <template #footer>
      <button
        type="button"
        class="btn btn-sm"
        @click="open = false"
      >
        {{ $t('close') }}
      </button>
    </template>
    <div class="flex flex-col gap-3 text-sm leading-relaxed">
      <div class="bg-base-200 flex flex-col gap-1 rounded-lg p-3">
        <p
          class="font-semibold tabular-nums"
          aria-live="polite"
        >
          {{ $t('bandwidthHelpCurrent') }} · {{ currentText }}
        </p>
        <p class="text-base-content/75 text-xs">
          {{ $t('bandwidthHelpEditLocation', { section: $t(section) }) }}
        </p>
      </div>

      <template v-if="section === 'bandwidthSettingsTitle'">
        <p>{{ $t('bandwidthSettingsHelpIntro') }}</p>
        <dl class="flex flex-col gap-2">
          <div>
            <dt class="font-medium">{{ $t('bandwidthPeriod') }}</dt>
            <dd class="text-base-content/75">{{ $t('bandwidthSettingsHelpInterval') }}</dd>
          </div>
          <div>
            <dt class="font-medium">{{ $t('bandwidthDownloadCap') }}</dt>
            <dd class="text-base-content/75">{{ $t('bandwidthSettingsHelpCap') }}</dd>
          </div>
        </dl>
        <div class="bg-base-200 rounded-lg p-3">
          <h3 class="mb-1 font-semibold">{{ $t('bandwidthSettingsHelpExampleHeading') }}</h3>
          <p>{{ $t('bandwidthSettingsHelpExample') }}</p>
        </div>
      </template>

      <template v-else-if="section === 'businessPreference'">
        <p>{{ $t('businessSettingsHelpIntro') }}</p>
        <section class="flex flex-col gap-1">
          <h3 class="font-semibold">{{ $t('businessReliabilityHalfLife') }}</h3>
          <p>{{ $t('businessSettingsHelpReliability') }}</p>
          <div class="bg-base-200 rounded-lg p-3">
            <h4 class="mb-1 font-semibold">{{ $t('bandwidthSettingsHelpExampleHeading') }}</h4>
            <p>{{ $t('businessSettingsHelpReliabilityExample') }}</p>
          </div>
        </section>
        <section class="flex flex-col gap-1">
          <h3 class="font-semibold">{{ $t('businessPerformanceTtl') }}</h3>
          <p>{{ $t('businessSettingsHelpPerformance') }}</p>
          <div class="bg-base-200 rounded-lg p-3">
            <h4 class="mb-1 font-semibold">{{ $t('bandwidthSettingsHelpExampleHeading') }}</h4>
            <p>{{ $t('businessSettingsHelpPerformanceExample') }}</p>
          </div>
        </section>
        <p class="text-base-content/75">{{ $t('businessSettingsHelpScope') }}</p>
      </template>

      <template v-else>
        <p>{{ $t('nodeScoreWeightsHelpIntro') }}</p>
        <p>{{ $t('nodeScoreWeightsHelpDrag') }}</p>
        <div class="bg-base-200 rounded-lg p-3">
          <h3 class="mb-1 font-semibold">{{ $t('bandwidthSettingsHelpExampleHeading') }}</h3>
          <p>{{ $t('nodeScoreWeightsHelpExample') }}</p>
          <table class="mt-2 w-full text-xs tabular-nums">
            <thead>
              <tr class="border-base-content/15 border-b">
                <th
                  scope="col"
                  class="py-1 text-start"
                >
                  {{ $t('nodeScoreWeightMetric') }}
                </th>
                <th
                  scope="col"
                  class="px-1 py-1 text-end"
                >
                  A
                </th>
                <th
                  scope="col"
                  class="px-1 py-1 text-end"
                >
                  B
                </th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in exampleRows"
                :key="row.label"
                class="border-base-content/10 border-b last:border-0"
              >
                <th
                  scope="row"
                  class="py-1 text-start font-normal"
                >
                  {{ $t(row.label) }}
                </th>
                <td
                  class="px-1 py-1 text-end whitespace-nowrap"
                  :class="row.best === 'A' && 'font-bold'"
                >
                  {{ row.a }}
                </td>
                <td
                  class="px-1 py-1 text-end whitespace-nowrap"
                  :class="row.best === 'B' && 'font-bold'"
                >
                  {{ row.b }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>
  </DialogWrapper>
</template>

<script setup lang="ts">
import type { OpenboxProfile } from '@/api/openbox'
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  // 当前展示哪一块的说明:带宽测试、评分权重,或业务优选
  section: 'bandwidthSettingsTitle' | 'nodeScoreWeights' | 'businessPreference'
  profile?: OpenboxProfile | null
  loading?: boolean
}>()
const open = defineModel<boolean>({ required: true })
const { t } = useI18n()

// 「当前设置」一行:档案里已保存的值(不是输入框里还没保存的草稿)
const currentText = computed(() => {
  if (props.loading) return t('bandwidthHelpLoading')
  const p = props.profile
  if (!p) return t('bandwidthHelpUnavailable')
  if (props.section === 'bandwidthSettingsTitle') {
    return t('bandwidthHelpCurrentTest', {
      hours: p.bandwidth?.intervalHours ?? 12,
      maxMB: p.bandwidth?.maxDownloadMB ?? 64,
    })
  }
  if (props.section === 'businessPreference') {
    return t('bandwidthHelpCurrentBusiness', {
      minutes: p.businessPreference?.reliabilityHalfLifeMinutes ?? 60,
      seconds: p.businessPreference?.performanceTtlSeconds ?? 60,
    })
  }
  const w = p.nodeScoreWeights ?? { latency: 40, download: 30, jitter: 30 }
  return t('nodeScoreWeightValues', { ...w })
})

// 示例:两个节点都健康。按默认权重 B 更高,把延时权重提到 70% 后 A 更高。
// 分数由 backend/internal/nodequality 的折线表算出(A:50 ms / 30 Mbps / 10 ms,B:100 ms / 300 Mbps / 20 ms)。
const exampleRows: { label: string; a: string; b: string; best?: 'A' | 'B' }[] = [
  { label: 'nodeScoreWeightLatency', a: '50 ms', b: '100 ms' },
  { label: 'nodeScoreWeightDownload', a: '30 Mbps', b: '300 Mbps' },
  { label: 'nodeScoreWeightJitter', a: '10 ms', b: '20 ms' },
  { label: 'nodeScoreWeightsDefaultExample', a: '84', b: '89', best: 'B' },
  { label: 'nodeScoreWeightsAdjustedExample', a: '92', b: '89', best: 'A' },
]
</script>
