<template>
  <!-- 网络状态:一张大卡,从上到下是站点延迟小卡、六项汇总、四张实时图(连接数 / 活跃终端 / 进站速率 / 出站速率) -->
  <div class="card w-full">
    <div class="card-body gap-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="flex flex-wrap items-center gap-2">
          <h2 class="text-base font-semibold">{{ $t('overviewCardTitle') }}</h2>
          <label
            class="text-base-content/70 flex cursor-pointer items-center gap-1.5 text-xs font-normal"
            :title="$t('siteLatencyAutoTestHint')"
          >
            <span>{{ $t('siteLatencyAutoTest') }}</span>
            <input
              v-model="autoSiteLatencyTest"
              type="checkbox"
              class="toggle toggle-xs toggle-primary"
            />
          </label>
        </div>
        <button
          type="button"
          class="btn btn-ghost btn-square btn-sm shrink-0"
          :disabled="siteLatencyLoading"
          :aria-label="$t('siteLatencyTestAll')"
          v-tip="$t('siteLatencyTestAll')"
          @click="() => runSiteLatencyTest()"
        >
          <span
            v-if="siteLatencyLoading"
            class="loading loading-spinner loading-xs"
          />
          <BoltIcon class="h-4 w-4" />
        </button>
      </div>
      <SiteLatencyTiles />
      <StatisticsStats type="overview" />
      <div class="grid grid-cols-2 gap-3 xl:grid-cols-4">
        <BasicCharts
          :title="$t('statConnections')"
          tone="primary"
          :data="connectionsData"
          :label-formatter="connectionsLabel"
          :tool-tip-formatter="connectionsTip"
          :min="100"
        />
        <BasicCharts
          :title="$t('statActiveTerminals')"
          tone="primary"
          :data="devicesData"
          :label-formatter="connectionsLabel"
          :tool-tip-formatter="connectionsTip"
          :min="4"
        />
        <BasicCharts
          :title="$t('dlSpeed')"
          tone="primary"
          :data="dlData"
          :label-formatter="speedLabel"
          :tool-tip-formatter="speedTip"
          :min="60 * 1000"
        />
        <BasicCharts
          :title="$t('ulSpeed')"
          tone="info"
          :data="ulData"
          :label-formatter="speedLabel"
          :tool-tip-formatter="speedTip"
          :min="60 * 1000"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import BasicCharts from '@/components/overview/BasicCharts.vue'
import SiteLatencyTiles from '@/components/overview/SiteLatencyTiles.vue'
import StatisticsStats from '@/components/overview/StatisticsStats.vue'
import { systemResourcesHistory, useSystemResources } from '@/composables/systemResources'
import { autoSiteLatencyTest, runSiteLatencyTest, siteLatencyLoading } from '@/composables/siteLatency'
import { getToolTipForParams } from '@/helper'
import { prettyBytesHelper } from '@/helper/utils'
import {
  connectionsHistory,
  downloadSpeedHistory,
  timeSaved,
  uploadSpeedHistory,
} from '@/store/overview'
import { BoltIcon } from '@heroicons/vue/24/outline'
import dayjs from 'dayjs'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

useSystemResources()

const dlData = computed(() => [{ name: t('dlSpeed'), data: downloadSpeedHistory.value }])
const ulData = computed(() => [{ name: t('ulSpeed'), data: uploadSpeedHistory.value }])
const connectionsData = computed(() => [{ name: t('connections'), data: connectionsHistory.value }])
// 活跃终端的曲线取自系统资源快照的历史,没带这个字段的采样点跳过
const devicesData = computed(() => [
  {
    name: t('statActiveTerminals'),
    data: systemResourcesHistory.value.flatMap(({ at, snapshot }) =>
      snapshot.activeTerminals == null ? [] : [{ name: at, value: snapshot.activeTerminals }],
    ),
  },
])

const speedLabel = (value: number) =>
  `${prettyBytesHelper(value, { maximumFractionDigits: 0, binary: false })}/s`
const speedTip = (value: ToolTipParams[]) =>
  value.map((item) => getToolTipForParams(item, { binary: false, suffix: '/s' })).join('')

const connectionsLabel = (value: number) => `       ${value}`
const connectionsTip = (value: ToolTipParams[]) =>
  value
    .map((item) => {
      // fake data
      if (item.data.name < timeSaved + 1) {
        return
      }
      return `
    <div class="flex items-center my-2 gap-1">
      <div class="w-4 h-4 rounded-full" style="background-color: ${item.color}"></div>
      ${item.seriesName}
      (${dayjs(item.data.name).format('HH:mm:ss')}): ${item.data.value}
    </div>`
    })
    .join('\n')
</script>
