<template>
  <!-- 延迟标签悬停浮层:「测速延时」页签是原来的时间线,「节点信息」页签查入口和出口。
       节点信息第一次点开才查(浮层里默认不发请求),之后来回切换不重查。 -->
  <div :class="['app-card-gap flex max-w-[calc(100vw-var(--app-card-space,12px)*4)] flex-col', widthClass]">
    <!-- 标题栏:当前页签的名字 + 刷新(测速延时:重测延迟;带宽:重测带宽;节点信息 / 风险评估:重查)+ 关闭 -->
    <div class="flex min-w-0 items-center justify-between gap-2">
      <div class="flex min-w-0 items-center gap-2">
        <span class="min-w-0 truncate text-sm font-semibold">{{ $t(currentLabel) }}</span>
        <button
          type="button"
          class="btn btn-ghost btn-circle btn-xs shrink-0"
          :disabled="refreshing"
          :aria-label="`${$t(currentLabel)} · ${$t('refresh')}`"
          :aria-busy="refreshing || undefined"
          @click.stop="refresh"
        >
          <span
            v-if="refreshing"
            class="loading loading-spinner loading-xs"
          ></span>
          <component
            :is="tab === 'latency' ? BoltIcon : tab === 'bandwidth' ? ChartBarIcon : ArrowPathIcon"
            v-else
            class="h-4 w-4"
          />
        </button>
      </div>
      <button
        type="button"
        class="btn btn-ghost btn-circle btn-xs"
        :aria-label="$t('close')"
        @click.stop="emit('close')"
      >
        <XMarkIcon class="h-4 w-4" />
      </button>
    </div>
    <div class="h-64 w-full max-w-[calc(100vw-var(--app-card-space,12px)*4)] overflow-hidden">
      <div
        v-show="tab === 'latency'"
        class="flex h-full items-start justify-center"
      >
        <span
          v-if="!hasHistory"
          class="self-center opacity-60"
        >
          {{ $t('noData') }}
        </span>
        <div
          ref="historyHost"
          class="contents"
        ></div>
      </div>
      <!-- 带宽实测时间线:样式和延时时间线一致,新的在上,最多 10 条 -->
      <div
        v-if="measurement?.enabled"
        v-show="tab === 'bandwidth'"
        class="mx-auto grid h-full w-fit max-w-full grid-cols-[auto_1rem_auto] grid-rows-[repeat(10,minmax(0,1fr))] items-stretch gap-x-2 pt-3 pb-1"
      >
        <template
          v-for="(item, i) in rows"
          :key="item?.at ?? `empty-${i}`"
        >
          <div class="flex flex-col items-end justify-center">
            <span
              class="text-xs leading-tight tabular-nums"
              :class="!item && 'text-base-content/30'"
            >{{ item ? dayjs(item.at).format('YYYY-MM-DD HH:mm:ss') : '—' }}</span>
          </div>
          <div class="relative flex min-h-6 items-center justify-center">
            <span
              class="absolute bottom-0 left-1/2 w-px -translate-x-1/2"
              :style="{ background: 'color-mix(in srgb, currentColor 45%, transparent)', top: i === 0 ? '-0.3rem' : '0' }"
            ></span>
            <span
              v-if="i === 0"
              class="border-x-transparent absolute left-1/2 -translate-x-1/2 border-x-[4px] border-b-[7px]"
              style="top: -0.45rem; border-bottom-color: color-mix(in srgb, currentColor 55%, transparent)"
            ></span>
            <span
              class="relative z-10 h-2.5 w-2.5 rounded-full"
              style="background-color: var(--color-base-100)"
            >
              <span
                class="block h-full w-full rounded-full"
                :class="dotClass(item)"
              ></span>
            </span>
          </div>
          <span
            class="flex min-w-0 items-center gap-2 text-xs tabular-nums"
            :class="item ? (item.failed ? 'text-red-500' : getColorForBandwidth(item.mbps)) : 'text-base-content/30'"
          >
            <span class="flex h-[6px] w-12 shrink-0 items-center">
              <span
                class="detection-bar h-full"
                :class="dotClass(item)"
                :style="{ width: `${item ? barPercent(item) : 100}%` }"
              ></span>
            </span>
            <span class="min-w-0 wrap-break-word">{{ item ? (item.failed ? $t('qualityState_down') : Math.round(item.mbps) + ' Mbps') : '— Mbps' }}</span>
          </span>
        </template>
      </div>
      <NodeInfoPanel
        v-if="infoOpened"
        v-show="tab === 'info' || tab === 'risk'"
        ref="infoPanel"
        class="h-full"
        :name="name"
        :udp="udp"
        :risk-only="tab === 'risk'"
      />
    </div>
    <div
      role="tablist"
      class="node-tip-tabs tabs-box tabs tabs-xs flex-nowrap"
    >
      <a
        v-for="item in tabs"
        :key="item.key"
        role="tab"
        class="tab flex-1"
        :class="tab === item.key && 'tab-active'"
        @click="select(item.key)"
      >
        {{ $t(item.label) }}
      </a>
    </div>
  </div>
</template>

<script setup lang="ts">
import NodeInfoPanel from '@/components/proxies/NodeInfoPanel.vue'
import { getBgColorForBandwidth, getColorForBandwidth } from '@/helper'
import { showNotification } from '@/helper/notification'
import { testNodeBandwidth, useBandwidth } from '@/store/bandwidth'
import { getTestUrl, proxyLatencyTest } from '@/store/proxies'
import type { OpenboxBandwidthSample } from '@/api/openbox'
import { ArrowPathIcon, BoltIcon, ChartBarIcon, XMarkIcon } from '@heroicons/vue/24/outline'
import dayjs from 'dayjs'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  name: string
  udp?: boolean
  // 原来的时间线(LatencyTag 里用 DOM 拼好的),没有历史就是 null
  history?: HTMLElement | null
  groupName?: string
  // 重测延迟以后重新拼一份时间线
  makeHistory?: () => HTMLElement
}>()
const emit = defineEmits<{ close: [] }>()
const { locale } = useI18n()

// 各页签等大:中文窄一点,英文标签长要宽一点
const widthClass = computed(() => (locale.value.startsWith('zh') ? 'w-76' : 'w-80'))
const { bandwidthMeasurements, bandwidthPendingNodes } = useBandwidth()
const measurement = computed(() => bandwidthMeasurements.value[props.name])
// 最近 10 条,新的在上;不足 10 条时空行占位放在上面(和延时时间线一致),让线对齐
const rows = computed<(OpenboxBandwidthSample | null)[]>(() => {
  const list = (measurement.value?.history || []).slice(-10).reverse()
  return [...Array(Math.max(0, 10 - list.length)).fill(null), ...list]
})
const maxMbps = computed(() => Math.max(1, ...rows.value.filter((r) => r && !r.failed && r.mbps > 0).map((r) => r!.mbps)))
// 失败的满格(红),其余按这批里最大的折算
const barPercent = (r: OpenboxBandwidthSample) => (r.failed ? 100 : Math.min(100, Math.max(0, (r.mbps / maxMbps.value) * 100)))
const dotClass = (r: OpenboxBandwidthSample | null) => (!r ? 'bg-base-content/10' : r.failed ? 'bg-red-500' : getBgColorForBandwidth(r.mbps))
const allTabs = [
  { key: 'latency', label: 'nodeTipLatency' },
  { key: 'bandwidth', label: 'nodeTipBandwidth' },
  { key: 'info', label: 'nodeInfo' },
  { key: 'risk', label: 'nodeRiskAssessment' },
] as const
const tabs = computed(() => allTabs.filter((t) => t.key !== 'bandwidth' || measurement.value?.enabled))
type TabKey = (typeof allTabs)[number]['key']
const tab = ref<TabKey>('latency')
const infoOpened = ref(false)
const historyHost = ref<HTMLElement>()
const infoPanel = ref<InstanceType<typeof NodeInfoPanel>>()
const hasHistory = ref(!!props.history)
const latencyBusy = ref(false)
const currentLabel = computed(() => allTabs.find((t) => t.key === tab.value)?.label ?? 'nodeTipLatency')
// 当前页签正在刷新就把按钮换成转圈
const refreshing = computed(() =>
  tab.value === 'latency'
    ? latencyBusy.value
    : tab.value === 'bandwidth'
      ? !!(measurement.value?.pending || bandwidthPendingNodes.value[props.name])
      : !!infoPanel.value?.loading,
)
const refresh = async () => {
  if (refreshing.value) return
  if (tab.value === 'bandwidth') return testNodeBandwidth(props.name)
  if (tab.value === 'info' || tab.value === 'risk') return infoPanel.value?.refresh()
  latencyBusy.value = true
  try {
    await proxyLatencyTest(props.name, getTestUrl(props.groupName))
    if (props.makeHistory && historyHost.value) {
      historyHost.value.replaceChildren(props.makeHistory())
      hasHistory.value = true
    }
  } catch (err) {
    showNotification({
      content: 'testRequestFailedTip',
      params: { name: props.name, message: err instanceof Error ? err.message : String(err) },
      type: 'alert-error',
    })
  } finally {
    latencyBusy.value = false
  }
}

const select = (key: TabKey) => {
  tab.value = key
  if (key === 'info' || key === 'risk') infoOpened.value = true
}

onMounted(() => {
  if (props.history) historyHost.value?.append(props.history)
})
</script>
