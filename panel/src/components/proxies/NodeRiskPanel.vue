<template>
  <!-- 出口 IP 的风险评估:Net.Coffee 信任评分、Ping0.cc 风控值,情报(威胁 / 滥用 / 黑名单 / RPKI)、深度检测(VPN / 代理 / Tor / 爬虫)、真人流量占比。
       compact 只显示一行摘要:威胁结论 + 滥用等级 -->
  <div
    :class="
      compact
        ? 'flex min-w-0 items-center gap-2'
        : 'col-span-2 grid min-w-0 grid-cols-subgrid gap-x-2 gap-y-1'
    "
    data-node-risk
  >
    <template v-if="compact">
      <span
        class="min-w-0 flex-1 truncate text-sm"
        :class="sections[0].rows[0].color"
      >
        {{ sections[0].rows[0].value }}
      </span>
      <span
        v-tip="$t('nodeRiskAbuse')"
        class="shrink-0"
        :class="sections[0].rows[1].color"
      >
        {{ sections[0].rows[1].value }}
      </span>
    </template>
    <template v-else>
      <NodeInfoText
        row
        :label="$t('nodeInfoTrust')"
        :text="trustScore == null ? '—' : `${trustScore} / 100`"
        :text-style="trustScore == null ? undefined : { color: trustColor }"
        source="Net.Coffee"
        :source-href="netCoffeeHref"
      >
        <template #prefix>
          <progress
            v-if="trustScore != null"
            class="progress mt-1 h-1.5 w-12 shrink-0"
            :style="{ color: trustColor }"
            :value="trustScore"
            max="100"
            :aria-label="$t('nodeInfoTrust')"
          ></progress>
        </template>
      </NodeInfoText>
      <NodeInfoText
        row
        :label="$t('nodeRiskPing0')"
        :text="ping0Text"
        :text-class="ping0Tier?.color"
        :source="ping0Source"
        :source-href="ping0Href"
      >
        <template
          v-if="ping0Risk"
          #prefix
        >
          <div
            class="bg-base-content/10 mt-1 h-1.5 w-12 shrink-0 overflow-hidden rounded-full"
            role="progressbar"
            :aria-label="$t('nodeRiskPing0')"
            aria-valuemin="0"
            aria-valuemax="100"
            :aria-valuenow="ping0Risk.percent"
            :title="`${ping0Risk.percent}%`"
          >
            <div
              class="from-success via-warning to-error h-full bg-linear-to-r"
              :style="{ clipPath: `inset(0 ${100 - ping0Risk.percent}% 0 0 round 9999px)` }"
            ></div>
          </div>
        </template>
      </NodeInfoText>
      <template
        v-for="(section, index) in sections"
        :key="section.title"
      >
        <hr
          v-if="index > 0"
          class="border-base-content/15 col-span-2 my-1"
        />
        <NodeInfoText
          v-for="row in section.rows"
          :key="row.label"
          row
          :label="$t(row.label)"
          :text="row.value"
          :text-class="row.color"
        />
      </template>
      <div
        class="col-span-2 grid grid-cols-subgrid items-center gap-x-2"
        data-node-human-traffic
      >
        <span class="opacity-60">
          {{ $t('nodeTrafficEstimate') }}
        </span>
        <div
          v-if="traffic"
          class="flex min-w-0 items-center gap-2 tabular-nums"
        >
          <span class="text-success shrink-0 whitespace-nowrap"
            >{{ $t('nodeTrafficHuman') }} {{ human }}%</span
          >
          <div
            class="bg-error/70 rounded-field h-1.5 w-20 min-w-0 shrink overflow-hidden"
            role="img"
            :aria-label="$t('nodeTrafficRatio', { human, bot: 100 - human })"
          >
            <div
              class="bg-success h-full"
              :style="{ width: traffic.human + '%' }"
            ></div>
          </div>
          <span class="text-error shrink-0 whitespace-nowrap"
            >{{ $t('nodeTrafficBot') }} {{ 100 - human }}%</span
          >
        </div>
        <span
          v-else
          class="opacity-60"
        >
          {{ $t('nodeRiskUnknown') }}
        </span>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import type {
  OpenboxNodeHumanTraffic,
  OpenboxNodeRiskAssessment,
  OpenboxNodeSharing,
} from '@/api/openbox'
import NodeInfoText from '@/components/proxies/NodeInfoText.vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  trustScore?: number | null
  trustColor?: string
  netCoffeeHref?: string
  ping0Risk?: OpenboxNodeSharing['risk']
  ping0Status?: string
  ping0Source?: string
  ping0Href?: string
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
const PING0_TIERS: Record<string, { label: string; color: string }> = {
  veryClean: { label: 'nodeRiskVeryClean', color: 'text-success' },
  clean: { label: 'nodeRiskClean', color: 'text-success' },
  neutral: { label: 'nodeRiskPing0Neutral', color: 'text-base-content' },
  slight: { label: 'nodeRiskPing0Slight', color: 'text-warning' },
  elevated: { label: 'nodeRiskPing0Elevated', color: 'text-orange-500' },
  extreme: { label: 'nodeRiskExtreme', color: 'text-error' },
}
const ping0Tier = computed(() =>
  props.ping0Risk?.tier ? PING0_TIERS[props.ping0Risk.tier] : undefined,
)
const ping0Text = computed(() =>
  props.ping0Risk
    ? ping0Tier.value
      ? t(ping0Tier.value.label)
      : `${props.ping0Risk.percent}%`
    : (props.ping0Status ?? t('nodeRiskUnknown')),
)
const unknown = (): Cell => ({ value: t('nodeRiskUnknown'), color: 'opacity-60' })
// 类名要写全,Tailwind 只编它在源码里见过的类
const tone = (key: string, level: Tone): Cell => ({
  value: t(key),
  color:
    level === 'good' ? 'text-success' : level === 'moderate' ? 'text-orange-500' : 'text-red-500',
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
  const key =
    n <= 2
      ? 'nodeRiskVeryLow'
      : n <= 25
        ? 'nodeRiskLow'
        : n <= 50
          ? 'nodeRiskModerate'
          : n <= 100
            ? 'nodeRiskHigh'
            : 'nodeRiskVeryHigh'
  const cell = tone(key, n > 50 ? 'bad' : 'moderate')
  return { ...cell, value: `${cell.value} · ${(Math.min(n, 255) / 25.5).toFixed(2)} / 10` }
}

const detected = (flag?: boolean | null): Cell =>
  flag == null
    ? unknown()
    : flag
      ? tone('nodeRiskDetected', 'bad')
      : tone('nodeRiskNotDetected', 'good')

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
  const rpkiValue = a?.rpki || a?.rpkiReported
  const rpki: Cell =
    rpkiValue === 'valid'
      ? { value: 'Valid', color: 'text-success' }
      : rpkiValue === 'invalid'
        ? { value: 'Invalid', color: 'text-red-500' }
        : rpkiValue === 'notfound'
          ? { value: 'NotFound', color: 'opacity-60' }
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
