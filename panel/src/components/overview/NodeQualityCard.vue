<template>
  <!-- 节点质量:按订阅汇总可用率 / 延时 / 带宽 / 覆盖率,展开看每个节点,再展开看历史柱状图和出口信息。
       对齐上游 v0.1.391。数据来自 /api/openbox/node-quality,每分钟刷新一次。 -->
  <section
    class="card w-full"
    data-node-quality
  >
    <div class="flex flex-wrap items-center gap-2 app-card-header">
      <div class="flex min-w-0 flex-col gap-1">
        <div class="flex flex-wrap items-center gap-2">
          <h2 class="text-base font-semibold">{{ $t('qualityTitle') }}</h2>
          <button
            type="button"
            class="btn btn-ghost btn-circle btn-xs"
            :aria-label="$t('qualityHelp')"
            @click="helpOpen = true"
          >
            <InformationCircleIcon class="h-4 w-4" />
          </button>
          <span class="text-base-content/60 text-xs">{{ $t('qualityScope') }}</span>
        </div>
        <p
          v-if="data"
          class="text-base-content/60 text-xs"
        >
          {{ $t('qualitySummary', summary) }}
        </p>
      </div>
      <label class="input input-sm ml-auto w-56 max-sm:w-full">
        <MagnifyingGlassIcon class="h-4 w-4 shrink-0 opacity-60" />
        <input
          v-model="search"
          type="search"
          :placeholder="$t('qualitySearch')"
          :aria-label="$t('qualitySearch')"
        />
      </label>
    </div>

    <div class="card-body gap-3">
      <div
        v-if="loading && !data"
        class="flex justify-center py-8"
      >
        <span class="loading loading-spinner loading-sm"></span>
      </div>

      <div
        class="grid grid-cols-1 gap-3"
        data-quality-subscriptions
      >
        <article
          v-for="sub in data?.subscriptions || []"
          :key="sub.id"
          class="bg-base-100 border-base-300/60 rounded-box min-w-0 border"
          data-quality-subscription
        >
          <button
            type="button"
            class="quality-summary quality-toggle flex w-full cursor-pointer flex-col app-card-gap app-card-inset text-left text-sm"
            :aria-label="$t(openSub === sub.id ? 'qualityCollapse' : 'qualityExpand', { name: sub.name })"
            :aria-expanded="openSub === sub.id"
            :aria-controls="`quality-nodes-${sub.id}`"
            @click="toggleSub(sub.id)"
          >
            <div class="flex min-w-0 items-start gap-2">
              <ChevronUpIcon
                v-if="openSub === sub.id"
                class="mt-1 h-4 w-4 shrink-0"
              />
              <ChevronDownIcon
                v-else
                class="mt-1 h-4 w-4 shrink-0"
              />
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="text-sm font-normal break-all">{{ sub.name }}</span>
                  <span class="badge badge-ghost badge-sm">{{ $t(`qualityMode_${sub.mode}`) }}</span>
                </div>
                <p class="text-base-content/60 mt-1 text-xs">
                  {{ $t('qualityUsable', { usable: sub.usable, total: sub.total }) }}
                </p>
              </div>
            </div>
            <div class="quality-metrics grid w-full min-w-0 grid-cols-2 gap-3 sm:grid-cols-3">
              <div class="quality-score">
                <span class="quality-label text-base-content/60 mb-1 block text-xs">{{ $t('qualityOverall') }}</span>
                <template v-if="sub.coverage >= 0.8 && sub.score !== null">
                  <div
                    class="text-lg font-bold"
                    :class="gradeClass(sub)"
                  >
                    {{ sub.score }}<span class="ml-1 text-xs font-normal">{{ $t('qualityPoints') }}</span>
                  </div>
                  <div
                    class="mt-1 text-xs"
                    :class="gradeClass(sub)"
                  >
                    {{ $t(`qualityGrade_${sub.grade}`) }}
                    <span v-if="sub.provisional"> · {{ $t('qualityProvisional') }}</span>
                  </div>
                </template>
                <template v-else>
                  <div class="text-sm font-medium max-sm:text-xs">{{ $t('qualityGrade_pending') }}</div>
                  <div
                    v-if="sub.score !== null"
                    class="text-base-content/60 mt-1 text-xs"
                  >
                    {{ $t('qualityMeasuredScore', { score: sub.score }) }}
                  </div>
                </template>
              </div>
              <div>
                <span class="quality-label text-base-content/60 mb-1 block text-xs">{{ $t('qualityAvailability') }}</span>
                <div>
                  {{ num(sub.availability, 1) }}<span
                    v-if="sub.availability !== null"
                    class="text-base-content/60 text-xs"
                  >%</span>
                </div>
                <span class="text-base-content/60 text-xs">{{ $t('qualitySamples', { count: sub.checks || 0 }) }}</span>
              </div>
              <div>
                <span class="quality-label text-base-content/60 mb-1 block text-xs">{{ $t('qualityLatencyJitter') }}</span>
                <div>
                  <span :class="sub.latency === null ? '' : getColorForLatency(sub.latency)">{{ num(sub.latency) }}</span>
                  <span class="text-base-content/60 text-xs">ms</span>
                </div>
                <div class="text-base-content/60 text-xs">± {{ num(sub.jitter) }} ms</div>
              </div>
              <div>
                <span class="quality-label text-base-content/60 mb-1 block text-xs">{{ $t('qualityBandwidth') }}</span>
                <div>
                  <span :class="getColorForBandwidth(sub.download)">{{ num(sub.download) }}</span>
                  <span class="text-base-content/60 text-xs">Mbps</span>
                </div>
                <div class="text-base-content/60 text-xs">
                  {{ sub.upload === null ? $t('qualityUploadPending') : `↑ ${num(sub.upload)} Mbps` }}
                </div>
              </div>
              <div>
                <span class="quality-label text-base-content/60 mb-1 block text-xs">{{ $t('qualityCoverage') }}</span>
                <div :class="sub.coverage < 0.8 ? 'text-warning' : ''">{{ sub.assessed }} / {{ sub.total }}</div>
                <div class="text-base-content/60 text-xs">
                  {{ $t('qualityBandwidthSamples', { count: sub.bandwidthCount }) }}
                </div>
              </div>
              <div class="min-w-0">
                <span class="quality-label text-base-content/60 mb-1 block text-xs">{{ $t('qualityUsage') }}</span>
                <div>{{ timeText(sub.activeMs) }}</div>
                <div class="text-base-content/60 text-xs">{{ bytes(sub.up + sub.down) }}</div>
              </div>
            </div>
          </button>

          <ExpandTransition>
            <div
              v-if="openSub === sub.id"
              :id="`quality-nodes-${sub.id}`"
              class="app-card-content-margin grid grid-cols-1 app-card-gap"
              data-quality-nodes
            >
              <div
                v-if="loading && !sub.nodes"
                class="flex justify-center py-4"
              >
                <span class="loading loading-spinner loading-sm"></span>
              </div>
              <article
                v-for="(node, ni) in sub.nodes || []"
                :key="node.tag"
                class="bg-base-100 border-base-300/60 rounded-box min-w-0 border app-card-inset"
                data-quality-node
              >
                <div
                  role="button"
                  tabindex="0"
                  class="quality-node-toggle flex min-w-0 cursor-pointer flex-wrap items-center app-card-gap text-left text-sm lg:flex-nowrap"
                  :aria-expanded="openNode === node.tag"
                  :aria-label="$t(openNode === node.tag ? 'qualityCollapse' : 'qualityExpand', { name: node.tag })"
                  :aria-controls="`quality-chart-${sub.id}-${ni}`"
                  @click="toggleNode(node.tag)"
                  @keydown.enter.self.prevent="toggleNode(node.tag)"
                  @keydown.space.self.prevent="toggleNode(node.tag)"
                >
                  <div class="flex min-w-0 flex-1 items-center gap-2 max-lg:basis-full">
                    <ChevronUpIcon
                      v-if="openNode === node.tag"
                      class="h-4 w-4 shrink-0"
                    />
                    <ChevronDownIcon
                      v-else
                      class="h-4 w-4 shrink-0"
                    />
                    <CountryFlag
                      :code="node.region"
                      :size="20"
                    />
                    <div class="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1 lg:flex-nowrap">
                      <span
                        class="min-w-0 truncate"
                        :title="node.tag"
                      >{{ node.tag }}</span>
                      <div class="text-base-content/60 flex min-w-0 items-center gap-2 text-xs whitespace-nowrap">
                        <span
                          class="truncate"
                          :title="`${node.protocol} · ${timeAt(node.lastAt)}`"
                        >{{ node.protocol }} · {{ timeAt(node.lastAt) }}</span>
                        <button
                          type="button"
                          class="hover:text-base-content flex shrink-0 cursor-pointer items-center gap-0.5"
                          data-quality-node-details
                          @click.stop="openDetails(node)"
                        >
                          {{ $t('qualityNodeDetailsLink') }}
                          <ChevronRightIcon class="h-3 w-3" />
                        </button>
                      </div>
                    </div>
                  </div>
                  <div class="ml-auto flex max-w-full flex-wrap items-center gap-x-3 gap-y-1 text-sm whitespace-nowrap lg:shrink-0 lg:flex-nowrap">
                    <span :class="node.state === 'down' ? 'text-error' : ''">
                      {{ node.score === null ? $t(`qualityState_${node.state}`) : `${node.score} ${$t('qualityPoints')}` }}
                    </span>
                    <span :class="node.state === 'healthy' && node.latency !== null ? getColorForLatency(node.latency) : 'text-base-content/60'">
                      {{ node.state === 'healthy' ? num(node.latency) : '—' }} ms
                    </span>
                    <span :class="node.state === 'healthy' ? getColorForBandwidth(node.download) : 'text-base-content/60'">
                      ↓ {{ node.state === 'healthy' ? num(node.download) : '—' }} Mbps
                    </span>
                    <StatusBadge
                      v-if="node.state === 'healthy' || node.state === 'down'"
                      :on="node.state === 'healthy'"
                      :on-text="$t('qualityState_healthy')"
                      :off-text="$t('qualityState_down')"
                    />
                    <span
                      v-else
                      class="badge badge-ghost badge-sm"
                    >{{ $t(`qualityState_${node.state}`) }}</span>
                  </div>
                </div>

                <ExpandTransition>
                  <div
                    v-if="openNode === node.tag"
                    :id="`quality-chart-${sub.id}-${ni}`"
                    class="pt-[var(--app-card-space)]"
                  >
                    <div
                      v-if="loading && !node.charts"
                      class="flex justify-center py-3"
                    >
                      <span class="loading loading-spinner loading-sm"></span>
                    </div>
                    <div
                      v-else
                      class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4"
                      data-quality-detail-cards
                    >
                      <section
                        v-for="item in detailItems"
                        :key="item.key"
                        class="bg-base-100 border-base-300/60 flex min-w-0 items-center app-card-gap app-card-inset rounded-xl border"
                        :data-quality-detail="item.key"
                      >
                        <QualityHistoryChart
                          v-if="item.key === 'latency' || item.key === 'bandwidth'"
                          :kind="item.key"
                          :samples="node.charts?.[item.key] || []"
                        />
                        <div
                          v-else
                          class="flex h-10 w-full min-w-0 flex-col gap-1"
                        >
                          <div class="flex items-center justify-between gap-2 text-xs leading-4">
                            <h4 class="text-base-content/60">{{ $t(item.label) }}</h4>
                            <button
                              type="button"
                              class="text-base-content/60 hover:text-base-content flex shrink-0 cursor-pointer items-center gap-0.5"
                              :aria-label="`${$t(item.label)} · ${$t('geoEntriesLink')}`"
                              @click="openDetails(node)"
                            >
                              {{ $t('geoEntriesLink') }}
                              <ChevronRightIcon class="h-3 w-3" />
                            </button>
                          </div>
                          <div class="flex h-5 min-w-0 items-center">
                            <NodeInfoPanel
                              compact
                              class="w-full"
                              :name="node.tag"
                              :risk-only="item.key === 'risk'"
                              :exit-state="exitState"
                            />
                          </div>
                        </div>
                      </section>
                    </div>
                  </div>
                </ExpandTransition>
              </article>

              <button
                v-if="sub.total > 10"
                type="button"
                class="btn btn-ghost btn-sm w-fit"
                data-quality-show-all
                :disabled="loading"
                :aria-expanded="showAll"
                @click="toggleAll"
              >
                {{ $t(showAll ? 'qualityShowFirstNodes' : 'qualityShowAllNodes') }}
                <ChevronUpIcon
                  v-if="showAll"
                  class="h-4 w-4"
                />
                <ChevronDownIcon
                  v-else
                  class="h-4 w-4"
                />
              </button>
            </div>
          </ExpandTransition>
        </article>
      </div>

      <p
        v-if="data && !data.subscriptions.length"
        class="text-base-content/60 py-8 text-center text-sm"
      >
        {{ $t(search ? 'qualityNoResults' : 'qualityEmpty') }}
      </p>
    </div>

    <DialogWrapper
      v-model="helpOpen"
      :title="$t('qualityHelp')"
      box-class="w-full max-w-2xl"
      header-class="pr-10!"
    >
      <div class="flex flex-col gap-4 text-sm leading-relaxed">
        <p
          v-for="k in helpKeys"
          :key="k"
        >
          {{ $t(k) }}
        </p>
      </div>
      <template #footer>
        <button
          type="button"
          class="btn btn-primary btn-sm"
          @click="helpOpen = false"
        >
          {{ $t('close') }}
        </button>
      </template>
    </DialogWrapper>

    <DialogWrapper
      v-model="detailOpen"
      :title="detailNode?.tag || $t('qualityNodeDetails')"
      box-class="w-full max-w-4xl"
      header-class="pr-10! [&>span:last-of-type]:min-w-0 [&>span:last-of-type]:truncate"
    >
      <template #title-left>
        <CountryFlag
          v-if="detailNode"
          :code="detailNode.region"
          :size="20"
        />
      </template>
      <div
        v-if="detailNode"
        class="flex flex-col gap-4 text-sm"
      >
        <div
          class="grid grid-cols-1 gap-4 md:grid-cols-2"
          data-quality-metrics
        >
          <dl
            v-for="col in [0, 1]"
            :key="col"
            class="grid content-start gap-x-2 gap-y-1 text-xs leading-4"
            :style="{ gridTemplateColumns: dlColumns }"
          >
            <template
              v-for="row in detailRows.filter((_, i) => i % 2 === col)"
              :key="row.label"
            >
              <dt class="opacity-60">{{ $t(row.label) }}</dt>
              <dd class="min-w-0 wrap-break-word">{{ row.value }}</dd>
            </template>
          </dl>
        </div>
        <p
          v-if="detailNode.state === 'down'"
          class="text-error text-xs"
        >
          {{ $t('qualityFailedHelp') }}
        </p>
        <div
          role="separator"
          class="border-base-content/10 border-t"
          data-quality-details-separator
        ></div>
        <div
          class="grid grid-cols-1 gap-4 md:grid-cols-2"
          data-quality-full-details
        >
          <section
            v-for="risk in [false, true]"
            :key="`${detailNode.tag}-${risk}`"
            class="flex min-w-0 flex-col gap-2"
          >
            <h3 class="text-sm font-semibold">{{ $t(risk ? 'nodeRiskAssessment' : 'nodeInfo') }}</h3>
            <NodeInfoPanel
              class="h-auto! overflow-y-visible! pr-0!"
              :name="detailNode.tag"
              :udp="proxyMap[detailNode.tag]?.udp"
              :risk-only="risk"
              :hide-footer="!risk"
              :exit-state="detailExit"
            />
          </section>
        </div>
      </div>
    </DialogWrapper>
  </section>
</template>

<script setup lang="ts">
import { fetchNodeQuality, type OpenboxNodeQuality, type OpenboxQualityNode, type OpenboxQualitySubscription } from '@/api/openbox'
import CountryFlag from '@/components/common/CountryFlag.vue'
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import ExpandTransition from '@/components/common/ExpandTransition.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import NodeInfoPanel from '@/components/proxies/NodeInfoPanel.vue'
import { useNodeExit, type NodeExitState } from '@/composables/nodeExit'
import { getColorForBandwidth, getColorForLatency } from '@/helper'
import { showNotification } from '@/helper/notification'
import { prettyBytesHelper } from '@/helper/utils'
import { proxyMap } from '@/store/proxies'
import { ChevronDownIcon, ChevronRightIcon, ChevronUpIcon, InformationCircleIcon, MagnifyingGlassIcon } from '@heroicons/vue/24/outline'
import dayjs from 'dayjs'
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import QualityHistoryChart from './QualityHistoryChart.vue'

const { t, locale } = useI18n()
const dlColumns = computed(
  () => `minmax(${locale.value.startsWith('zh') ? '4.25rem' : '5.25rem'}, max-content) minmax(0, 1fr)`,
)

const data = ref<OpenboxNodeQuality | null>(null)
const search = ref('')
const openSub = ref('')
const openNode = ref('')
const showAll = ref(false)
const loading = ref(false)
const helpOpen = ref(false)
const detailOpen = ref(false)
const detailNode = ref<OpenboxQualityNode | null>(null)
const detailExit = shallowRef<NodeExitState>(useNodeExit(''))
const exitState = computed(() => useNodeExit(openNode.value))

const detailItems = [
  { key: 'latency', label: 'nodeTipLatency' },
  { key: 'bandwidth', label: 'nodeTipBandwidth' },
  { key: 'info', label: 'nodeInfo' },
  { key: 'risk', label: 'nodeRiskAssessment' },
] as const
const helpKeys = ['qualityHealthHelp', 'qualityModelHelp', 'qualityExampleHelp', 'qualityMeasurementHelp', 'qualityBandwidthHelp']

const num = (v: number | null | undefined, digits = 0) => (typeof v === 'number' && Number.isFinite(v) ? v.toFixed(digits) : '—')
const bytes = (v: number) => prettyBytesHelper(v, { maximumFractionDigits: 1 })
const timeText = (ms: number) => t('qualityTime', { hours: Math.floor(ms / 3_600_000), minutes: Math.floor((ms % 3_600_000) / 60_000) })
const timeAt = (v: number | string | null) => (v ? dayjs(v).format('YYYY-MM-DD HH:mm') : t('qualityNever'))
const gradeClass = (s: OpenboxQualitySubscription) =>
  s.grade === 'attention' ? 'text-error' : s.grade === 'fair' ? 'text-warning' : 'text-success'

const summary = computed(() => {
  const subs = data.value?.subscriptions || []
  return {
    subscriptions: subs.length,
    nodes: subs.reduce((n, s) => n + s.total, 0),
    failed: subs.reduce((n, s) => n + s.failed, 0),
    unknown: subs.reduce((n, s) => n + s.unknown, 0),
  }
})

const detailRows = computed(() => {
  const n = detailNode.value
  if (!n) return []
  return [
    { label: 'qualityAvailability', value: num(n.availability, 1) + '%' },
    { label: 'qualityLatencyJitter', value: `${num(n.latency)} / ${num(n.jitter)} ms` },
    { label: 'qualityBandwidth', value: `${num(n.download)} / ${num(n.upload)} Mbps` },
    { label: 'qualityActiveTime', value: timeText(n.activeMs) },
    { label: 'qualityTraffic', value: bytes(n.up + n.down) },
    { label: 'qualityLastCheck', value: timeAt(n.lastAt) },
  ]
})

let controller: AbortController | undefined
let timer: ReturnType<typeof setInterval> | undefined
let debounce: ReturnType<typeof setTimeout> | undefined
let seq = 0

const load = async () => {
  const mine = ++seq
  controller?.abort()
  controller = new AbortController()
  loading.value = true
  try {
    const res = await fetchNodeQuality(
      { allSubscriptions: true, search: search.value, details: openSub.value, nodePageSize: 10, allNodes: showAll.value, node: openNode.value },
      controller.signal,
    )
    if (mine !== seq) return
    data.value = res
    const sub = res.subscriptions.find((s) => s.id === openSub.value)
    if (!sub) openSub.value = ''
    if (!sub?.nodes?.some((n) => n.tag === openNode.value)) openNode.value = ''
  } catch (err) {
    if (mine === seq && !controller.signal.aborted) {
      showNotification({
        content: 'qualityLoadFailed',
        params: { message: err instanceof Error ? err.message : String(err) },
        type: 'alert-error',
        key: 'node-quality-load',
      })
    }
  } finally {
    if (mine === seq) loading.value = false
  }
}

const openDetails = (node: OpenboxQualityNode) => {
  detailNode.value = node
  detailExit.value = openNode.value === node.tag ? exitState.value : useNodeExit(node.tag)
  detailOpen.value = true
}
const toggleSub = (id: string) => {
  openNode.value = ''
  openSub.value = openSub.value === id ? '' : id
  showAll.value = false
  void load()
}
const toggleNode = (tag: string) => {
  openNode.value = openNode.value === tag ? '' : tag
  void load()
}
const toggleAll = () => {
  showAll.value = !showAll.value
  openNode.value = ''
  void load()
}

watch(search, () => {
  clearTimeout(debounce)
  debounce = setTimeout(() => {
    showAll.value = false
    openSub.value = ''
    openNode.value = ''
    void load()
  }, 250)
})

onMounted(() => {
  void load()
  timer = setInterval(() => {
    if (!document.hidden && !loading.value) void load()
  }, 60_000)
})
onBeforeUnmount(() => {
  seq++
  controller?.abort()
  clearInterval(timer)
  clearTimeout(debounce)
})
</script>

<style scoped>
@media (min-width: 1280px) {
  .quality-summary {
    display: grid;
    grid-template-columns: minmax(165px, 1.7fr) repeat(6, minmax(0, 1fr));
    gap: 12px;
    align-items: center;
  }
  .quality-metrics {
    display: contents;
  }
  .quality-metrics > div {
    min-width: 0;
    text-align: right;
  }
  .quality-label {
    display: none;
  }
}
</style>
