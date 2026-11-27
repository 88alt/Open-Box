<template>
  <DialogWrapper
    v-model="open"
    :title="title"
    :box-class="mode || subscription ? 'w-full max-w-2xl' : 'w-full max-w-xl'"
    :modal-class="subscription ? 'z-[1000]' : undefined"
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
        <p
          v-if="section === 'nodeScoreWeights' && mode && profile"
          class="font-medium tabular-nums"
        >
          {{ $t(currentTestKey, testParams) }}
        </p>
        <p class="text-base-content/75 text-xs">
          {{ $t('bandwidthHelpEditLocation', { section: editLocation }) }}
          <template v-if="switchEnabled !== null">
            · {{ $t(switchEnabled ? 'bandwidthHelpSwitchOn' : 'bandwidthHelpSwitchOff') }}
          </template>
        </p>
      </div>

      <template v-if="subscription">
        <p>{{ $t('subscriptionBandwidthHelp') }}</p>
        <p>{{ $t('subscriptionBandwidthDedupe') }}</p>
        <p>{{ $t('subscriptionBandwidthBudget') }}</p>
        <div class="bg-base-200 rounded-lg p-3">
          <h3 class="mb-1 font-semibold">{{ $t('bandwidthSettingsHelpExampleHeading') }}</h3>
          <p>{{ $t('bandwidthSettingsHelpExample') }}</p>
        </div>
        <p class="text-base-content/75">{{ $t('bandwidthClientDisabled') }}</p>
      </template>

      <template v-else-if="section === 'bandwidthSettingsTitle'">
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
          <!-- 上游 v0.1.493:测速站点预设(不同站点的带宽样本彼此隔离) -->
          <div>
            <dt class="font-medium">{{ $t('bandwidthEndpoint') }}</dt>
            <dd class="text-base-content/75">{{ $t('bandwidthSettingsHelpEndpoint') }}</dd>
          </div>
        </dl>
        <!-- 上游 v0.1.493:带上当前实际用的测速站点 -->
        <p class="font-medium tabular-nums">
          {{ $t('bandwidthHelpCurrentEndpoint', { endpoint: endpointLabel }) }}
        </p>
        <div class="bg-base-200 rounded-lg p-3">
          <h3 class="mb-1 font-semibold">{{ $t('bandwidthSettingsHelpExampleHeading') }}</h3>
          <p>{{ $t('bandwidthSettingsHelpExample') }}</p>
        </div>
      </template>

      <template v-else-if="section === 'nodeScoreWeights' && mode">
        <p class="font-medium">{{ $t('bandwidthHealthFirst') }}</p>
        <div>
          <h3 class="font-semibold">{{ $t('bandwidthOff') }}</h3>
          <p>{{ $t(`bandwidthOff_${mode}`) }}</p>
        </div>
        <div>
          <h3 class="font-semibold">{{ $t('bandwidthOn') }}</h3>
          <p>{{ $t(`bandwidthOn_${mode}`) }}</p>
        </div>
        <div class="bg-base-200 rounded-lg p-3">
          <h3 class="mb-1 font-semibold">{{ $t('bandwidthExample') }}</h3>
          <p>{{ $t(`bandwidthExample_${mode}`) }}</p>
        </div>
        <p class="text-base-content/75">{{ $t('bandwidthSharedHelp') }}</p>
        <p class="text-base-content/75">{{ $t('bandwidthClientDisabled') }}</p>
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

const props = withDefaults(
  defineProps<{
    // 当前展示哪一块的说明:带宽测试,或评分权重
    section: 'bandwidthSettingsTitle' | 'nodeScoreWeights'
    profile?: OpenboxProfile | null
    loading?: boolean
    // 分组类型(urltest / failover / loadbalance);分组编辑器里打开时传,决定标题和「关闭时 / 开启时 / 例子」文案
    mode?: string
    // 正在编辑的那个开关此刻开没开;null 表示不是从某个开关旁打开的,不显示这一句
    switchEnabled?: boolean | null
    // 订阅添加 / 编辑弹窗里打开:显示订阅侧的带宽检查说明
    subscription?: boolean
  }>(),
  { switchEnabled: null },
)
const open = defineModel<boolean>({ required: true })
const { t } = useI18n()

const title = computed(() => {
  if (props.subscription) return t('subscriptionBandwidthHelpTitle')
  if (props.mode) {
    const base = props.section === 'nodeScoreWeights' ? t('bandwidthHelpTitle') : t(props.section)
    return `${base} · ${t(`groupType_${props.mode}`)}`
  }
  return t(props.section)
})

// 分组编辑器里的评分说明同时讲测速,所以「修改位置」写成「节点评分 / 带宽测试」两处
const editLocation = computed(() =>
  props.mode && props.section === 'nodeScoreWeights'
    ? `${t('nodeScoreWeights')} / ${t('bandwidthSettingsTitle')}`
    : t(props.section),
)

const testParams = computed(() => {
  const b = props.profile?.bandwidth
  const hours = b?.intervalHours ?? 12
  const maxMB = b?.maxDownloadMB ?? 64
  if (b?.intervalUnit === 'days') {
    const h = b.startHour
    const hour = Number.isInteger(h) && Number(h) >= 0 && Number(h) <= 23 ? Number(h) : 4
    return {
      days: Math.max(1, Math.round(hours / 24)),
      hour: `${String(hour).padStart(2, '0')}:00`,
      maxMB,
    }
  }
  return { hours, maxMB }
})
const currentTestKey = computed(() =>
  props.profile?.bandwidth?.intervalUnit === 'days'
    ? 'bandwidthHelpCurrentTestDaily'
    : 'bandwidthHelpCurrentTest',
)

// 「当前设置」一行:档案里已保存的值(不是输入框里还没保存的草稿)
// 当前测速站点的人类可读名(与卡片下拉的四个选项同一套文案)
const ENDPOINT_KEYS: Record<string, string> = {
  cloudflare: 'bandwidthEndpointCloudflare',
  apple: 'bandwidthEndpointApple',
  cachefly: 'bandwidthEndpointCachefly',
  custom: 'bandwidthEndpointCustom',
}
const endpointLabel = computed(() => {
  const e = props.profile?.bandwidth?.endpoint ?? 'cloudflare'
  return t(ENDPOINT_KEYS[e] ?? ENDPOINT_KEYS.cloudflare)
})

const currentText = computed(() => {
  if (props.loading) return t('bandwidthHelpLoading')
  const p = props.profile
  if (!p) return t('bandwidthHelpUnavailable')
  if (props.section === 'bandwidthSettingsTitle') {
    return t(currentTestKey.value, testParams.value)
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
