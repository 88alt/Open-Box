<template>
  <!-- 出口 IP 的风险评估:情报(威胁 / 滥用 / 黑名单 / RPKI)、深度检测(VPN / 代理 / Tor / 爬虫)、真人流量占比。
       compact 只显示一行摘要:威胁结论 + 滥用等级 -->
  <div
    :class="
      compact ? 'flex min-w-0 items-center gap-2' : 'col-span-2 flex min-w-0 flex-col gap-2 text-xs leading-4'
    "
    data-node-risk
  >
    <template v-if="compact">
      <span
        class="min-w-0 flex-1 truncate text-sm"
        :class="sections[0].rows[0].color"
        :title="sections[0].rows[0].value"
      >
        {{ sections[0].rows[0].value }}
      </span>
      <span
        class="shrink-0"
        :class="sections[0].rows[1].color"
        :title="$t('nodeRiskAbuse')"
      >
        {{ sections[0].rows[1].value }}
      </span>
    </template>
    <template v-else>
      <div
        v-for="(section, index) in sections"
        :key="section.title"
        class="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-2 gap-y-1"
      >
        <hr
          v-if="index > 0"
          class="border-base-content/15 col-span-2 my-1"
        />
        <h3
          v-else
          class="col-span-2 text-xs font-semibold"
        >
          {{ $t(section.title) }}
        </h3>
        <NodeInfoText
          v-for="row in section.rows"
          :key="row.label"
          row
          :label="$t(row.label)"
          :text="row.value"
          :text-class="`${row.color} flex-1 text-right`"
          :title="
            row.label === 'nodeRiskRpki' && assessment?.rpkiReported
              ? $t('nodeRiskRpkiReported', { value: assessment.rpkiReported })
              : undefined
          "
        />
      </div>
      <div
        class="grid grid-cols-[auto_minmax(0,1fr)] items-center gap-2"
        data-node-human-traffic
      >
        <span
          class="opacity-60"
          :title="traffic ? $t(traffic.basis === 'asn-28d' ? 'nodeTrafficAsnHelp' : 'nodeTrafficEstimateHelp') : undefined"
        >
          {{ $t('nodeTrafficEstimate') }}
        </span>
        <div
          v-if="traffic"
          class="flex min-w-0 items-center gap-2 tabular-nums"
        >
          <span class="text-primary shrink-0 whitespace-nowrap">{{ $t('nodeTrafficHuman') }} {{ human }}%</span>
          <div
            class="bg-error/70 rounded-field h-1.5 min-w-0 flex-1 overflow-hidden"
            role="img"
            :aria-label="$t('nodeTrafficRatio', { human, bot: 100 - human })"
          >
            <div
              class="bg-primary h-full"
              :style="{ width: traffic.human + '%' }"
            ></div>
          </div>
          <span class="text-error shrink-0 whitespace-nowrap">{{ $t('nodeTrafficBot') }} {{ 100 - human }}%</span>
        </div>
        <span
          v-else
          class="text-right opacity-60"
        >
          {{ $t('nodeRiskUnknown') }}
        </span>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import type { OpenboxNodeHumanTraffic, OpenboxNodeRiskAssessment } from '@/api/openbox'
import NodeInfoText from '@/components/proxies/NodeInfoText.vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  assessment?: OpenboxNodeRiskAssessment
  traffic?: OpenboxNodeHumanTraffic
  compact?: boolean
}>()
const { t } = useI18n()

type Tone = 'good' | 'moderate' | 'bad'
interface Cell {
  value: string
  color: string
}

const human = computed(() => Math.round(props.traffic?.human ?? 0))
const unknown = (): Cell => ({ value: t('nodeRiskUnknown'), color: 'opacity-60' })
// 类名要写全,Tailwind 只编它在源码里见过的类
const tone = (key: string, level: Tone): Cell => ({
  value: t(key),
  color: level === 'good' ? 'text-primary' : level === 'moderate' ? 'text-orange-500' : 'text-red-500',
})

const abuse = (): Cell => {
  const a = props.assessment
  if (!a) return unknown()
  if (a.abuseScore != null && a.abuseScore >= 0) {
    if (a.abuseScore > 0.25) return tone('nodeRiskVeryHigh', 'bad')
    if (a.abuseScore > 0.05) return tone('nodeRiskHigh', 'bad')
    if (a.abuseScore > 0.025) return tone('nodeRiskModerate', 'moderate')
    if (a.abuseScore > 0.01 || a.threats?.length) return tone('nodeRiskLow', 'moderate')
    return tone(a.abuseScore > 0.003 ? 'nodeRiskClean' : 'nodeRiskVeryClean', 'good')
  }
  switch (a.abuseLevel) {
    case 'very_high':
    case 'veryhigh':
      return tone('nodeRiskVeryHigh', 'bad')
    case 'high':
      return tone('nodeRiskHigh', 'bad')
    case 'elevated':
      return tone('nodeRiskModerate', 'moderate')
    case 'low':
      return tone('nodeRiskClean', 'good')
    case 'safe':
      return tone('nodeRiskLow', 'moderate')
    default:
      return unknown()
  }
}

const blacklist = (): Cell => {
  const n = props.assessment?.httpBlacklist
  if (n == null || n < 0) return unknown()
  if (n === 0) return tone('nodeRiskClean', 'good')
  const key = n <= 2 ? 'nodeRiskVeryLow' : n <= 25 ? 'nodeRiskLow' : n <= 50 ? 'nodeRiskModerate' : n <= 100 ? 'nodeRiskHigh' : 'nodeRiskVeryHigh'
  const cell = tone(key, n > 50 ? 'bad' : 'moderate')
  return { ...cell, value: `${cell.value} · ${(Math.min(n, 255) / 25.5).toFixed(2)} / 10` }
}

const detected = (flag?: boolean | null): Cell =>
  flag == null ? unknown() : flag ? tone('nodeRiskDetected', 'bad') : tone('nodeRiskNotDetected', 'good')

const sections = computed(() => {
  const a = props.assessment
  const threats: Cell =
    a?.threats == null
      ? unknown()
      : a.threats.length
        ? {
            value: a.threats.map((v) => v.label).join(' · '),
            color: a.threats.some((v) => v.severity === 'bad') ? 'text-red-500' : 'text-orange-500',
          }
        : tone('nodeRiskNoThreats', 'good')
  const rpki: Cell =
    a?.rpki === 'valid'
      ? { value: 'Valid', color: 'text-primary' }
      : a?.rpki === 'invalid'
        ? { value: 'Invalid', color: 'text-red-500' }
        : a?.rpki === 'notfound'
          ? { value: 'NotFound', color: 'text-orange-500' }
          : a?.rpkiReported
            ? { value: t('nodeRiskUnverified'), color: 'opacity-60' }
            : unknown()
  return [
    {
      title: 'nodeRiskIntelligence',
      rows: [
        { label: 'nodeRiskThreats', ...threats },
        { label: 'nodeRiskAbuse', ...abuse() },
        { label: 'nodeRiskBlacklist', ...blacklist() },
        { label: 'nodeRiskRpki', ...rpki },
      ],
    },
    {
      title: 'nodeRiskDeep',
      rows: [
        { label: 'nodeRiskVpn', ...detected(a?.vpn) },
        { label: 'nodeRiskProxy', ...detected(a?.proxy) },
        { label: 'nodeRiskTor', ...detected(a?.tor) },
        { label: 'nodeRiskCrawler', ...detected(a?.crawler) },
      ],
    },
  ]
})
</script>
