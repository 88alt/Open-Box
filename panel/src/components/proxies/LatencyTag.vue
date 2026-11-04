<template>
  <div
    :class="
      twMerge(
        'latency-tag bg-base-100 flex h-5 w-10 cursor-pointer items-center justify-center rounded-xl text-xs select-none',
        color,
        bandwidthEnabled && 'w-auto whitespace-nowrap',
      )
    "
    :style="bandwidthEnabled ? { minWidth: 'max-content' } : undefined"
    @mouseenter="handlerHistoryTip"
  >
    <!-- 开了带宽段:延迟、竖线、带宽三块各自左右留 px-1.5,和上游 v0.1.389 的胶囊一致 -->
    <span :class="bandwidthEnabled ? 'flex h-full items-center justify-center px-1.5' : 'contents'">
      <span
        v-if="loading"
        class="loading loading-dots loading-xs text-base-content/80"
      ></span>
      <BoltIcon
        v-else-if="latency === NOT_CONNECTED || !latency"
        class="text-base-content h-3 w-3"
      />
      <div
        v-show="latency !== NOT_CONNECTED && !loading"
        ref="latencyRef"
      >
        {{ latency }}
      </div>
    </span>
    <!-- 所属订阅开了带宽定期检查:延迟后面接一段带宽实测(Mbps,点一下重测这个节点) -->
    <template v-if="bandwidthEnabled">
      <span
        class="text-base-content/40"
        aria-hidden="true"
      >|</span>
      <button
        type="button"
        class="flex h-full cursor-pointer items-center justify-center px-1.5 disabled:cursor-default"
        :class="getColorForBandwidth(bandwidthFailText ? undefined : bandwidth?.mbps) || 'text-base-content/60'"
        :title="bandwidthFailText ? bandwidth?.error : undefined"
        :disabled="bandwidthBusy"
        :aria-label="`${$t('nodeTipBandwidth')} · ${name}`"
        :aria-busy="bandwidthBusy || undefined"
        @click.stop="testNodeBandwidth(name ?? '')"
      >
        <span
          v-if="bandwidthBusy"
          class="loading loading-dots loading-xs"
        ></span>
        <ChartBarIcon
          v-else-if="bandwidthFailText || bandwidth?.mbps == null"
          class="text-base-content h-3 w-3"
        />
        <template v-else>{{ Math.round(bandwidth.mbps) }}</template>
      </button>
    </template>
  </div>
</template>

<script setup lang="ts">
import { NOT_CONNECTED } from '@/constant'
import { getColorForBandwidth, getColorForLatency } from '@/helper'
import { latencyReasonText } from '@/helper/latencyReason'
import { useTooltip } from '@/helper/tooltip'
import { isSingBox } from '@/api'
import { MAX_LATENCY_HISTORY, getRecentLatencyHistory, type LatencySample } from '@/store/latencyHistory'
import { getHistoryByName, getLatencyByName, getNowProxyNodeName, proxyMap } from '@/store/proxies'
import { testNodeBandwidth, useBandwidth } from '@/store/bandwidth'
import { independentLatencyTest, speedtestTimeout } from '@/store/settings'
import { BoltIcon, ChartBarIcon } from '@heroicons/vue/24/outline'
import { CountUp } from 'countup.js'
import dayjs from 'dayjs'
import { twMerge } from 'tailwind-merge'
import { computed, createVNode, getCurrentInstance, nextTick, onMounted, onUnmounted, ref, render, watch } from 'vue'
import NodeLatencyTip from './NodeLatencyTip.vue'

const { showTip, hideTip } = useTooltip()
// 浮层是手动挂到 tippy 里的 DOM,不在这个组件的渲染树下:把应用上下文带过去,里面的组件才拿得到 i18n
const appContext = getCurrentInstance()?.appContext ?? null
// 最近 10 次结果,新的在上:mihomo 的独立测试模式内核自己留了多条,照用;sing-box 每个节点只留
// 最新一次,用服务端攒的时间线(store/latencyHistory.ts),一条都没攒到就退回内核那一条。
// 组看组自己的时间线(每笔带当时选中的节点),节点看节点的;标签本身显示的是当前所选节点的延迟
const timelineTarget = () => {
  if (props.timelineName) return props.timelineName
  const name = props.name ?? ''
  return proxyMap.value[name]?.all?.length ? name : getNowProxyNodeName(name)
}
// 不是真节点的出站:没有入口服务器可查,不出「节点信息」页签
const NON_NODE_TYPES = new Set(['direct', 'reject', 'block', 'dns', 'pass', 'compatible', 'rejectdrop', 'selector', 'urltest', 'fallback', 'loadbalance', 'smart'])
// 标签属于一个真节点(不是组、不是内置出站、也不是看组时间线的那个)才有节点信息可看
const infoNode = () => {
  if (props.timelineName) return null
  const node = proxyMap.value[props.name ?? '']
  if (!node || node.all?.length || NON_NODE_TYPES.has(String(node.type || '').toLowerCase())) return null
  return node
}
const recentHistory = (): LatencySample[] => {
  const name = props.name ?? ''
  const fromKernel = (): LatencySample[] =>
    [...getHistoryByName(name, props.groupName)].reverse().slice(0, MAX_LATENCY_HISTORY).map((h) => ({ time: h.time, delay: h.delay }))
  if (independentLatencyTest.value && !isSingBox.value) return fromKernel()
  const own = getRecentLatencyHistory(timelineTarget())
  if (own.length) return own
  return fromKernel()
}
// 类名要写全,Tailwind 只编它在源码里见过的类;从 text-* 动态拼 bg-* 会被裁掉
const dotColor = (delay: number) => {
  if (delay === NOT_CONNECTED) return 'bg-red-500'
  switch (getColorForLatency(delay)) {
    case 'text-primary': return 'bg-primary'
    case 'text-orange-500': return 'bg-orange-500'
    case 'text-red-500': return 'bg-red-500'
    default: return 'bg-gray-400'
  }
}
// 竖着的时间线:浮层里刷新测速以后也靠它重建。固定 10 行:不足的空行放在上面(— / — ms),
// 实测的新的在上、紧接在空行下面;右边除了数值还有一根按本组最大延迟折算长度的进度条
const buildHistoryList = (history: LatencySample[]) => {
  // 组的样本在时间上面用小字标出那一笔是当时选中的哪个节点测出来的
  const withNode = history.some((item) => Boolean(item.node))
  const slots: (LatencySample | null)[] = [
    ...Array(Math.max(0, MAX_LATENCY_HISTORY - history.length)).fill(null),
    ...history.slice(0, MAX_LATENCY_HISTORY),
  ]
  const timeout = Number(speedtestTimeout.value) || 5000
  // 失败的按超时时间算满格;其余按这批里最大的那条折算
  const maxDelay = Math.max(1, ...history.map((item) => (item.delay === NOT_CONNECTED ? timeout : item.delay)).filter(Number.isFinite))
  const historyList = document.createElement('div')
  // 顶部留出箭头的位置:箭头探出第一行 0.45rem + 自身 7px,pt-3 的 12px 刚好包住它
  historyList.className = 'mx-auto grid h-full w-fit max-w-full grid-cols-[auto_1rem_auto] grid-rows-[repeat(10,minmax(0,1fr))] items-stretch gap-x-2 pt-3 pb-1'
  slots.forEach((item, i) => {
    const time = document.createElement('div')
    time.className = `flex flex-col items-end justify-center ${withNode ? 'py-0.5' : ''}`
    if (withNode && item) {
      const node = document.createElement('div')
      node.className = 'text-[10px] leading-tight opacity-70'
      node.textContent = item.node || ''
      time.append(node)
    }
    const stamp = document.createElement('div')
    stamp.className = 'text-xs leading-tight tabular-nums'
    stamp.textContent = item ? dayjs(item.time).format('YYYY-MM-DD HH:mm:ss') : '—'
    if (!item) stamp.classList.add('text-base-content/30')
    time.append(stamp)

    // 行高跟着左边那格走(组的样本是两行),线从上到下连着
    const cell = document.createElement('div')
    cell.className = 'relative flex min-h-6 items-center justify-center'
    const line = document.createElement('div')
    line.className = 'absolute bottom-0 left-1/2 w-px -translate-x-1/2'
    line.style.background = 'color-mix(in srgb, currentColor 45%, transparent)'
    line.style.top = i === 0 ? '-0.3rem' : '0'
    cell.append(line)
    if (i === 0) {
      const arrow = document.createElement('div')
      arrow.className = 'border-x-transparent absolute left-1/2 -translate-x-1/2 border-x-[4px] border-b-[7px]'
      arrow.style.top = '-0.45rem'
      arrow.style.borderBottomColor = 'color-mix(in srgb, currentColor 55%, transparent)'
      cell.append(arrow)
    }
    const dotWrap = document.createElement('div')
    dotWrap.className = 'relative z-10 h-2.5 w-2.5 rounded-full'
    dotWrap.style.backgroundColor = 'var(--color-base-100)'
    const dot = document.createElement('span')
    dot.className = `block h-full w-full rounded-full ${item ? dotColor(item.delay) : 'bg-base-content/10'}`
    dotWrap.append(dot)
    cell.append(dotWrap)

    const latency = document.createElement('div')
    const failed = item?.delay === NOT_CONNECTED
    latency.className = `flex min-w-0 items-center gap-2 text-xs tabular-nums ${
      item ? (failed ? 'text-red-500' : getColorForLatency(item.delay)) : 'text-base-content/30'
    }`
    const barWrap = document.createElement('span')
    barWrap.className = 'flex h-[6px] w-12 shrink-0 items-center'
    barWrap.setAttribute('aria-hidden', 'true')
    const bar = document.createElement('span')
    bar.className = `detection-bar h-full ${item ? dotColor(item.delay) : 'bg-base-content/10'}`
    const barValue = item ? (failed ? timeout : item.delay) : 0
    bar.style.width = `${item ? Math.min(100, Math.max(0, (barValue / maxDelay) * 100)) : 100}%`
    barWrap.append(bar)
    const text = document.createElement('span')
    text.className = 'min-w-0 wrap-break-word'
    text.textContent = item ? (failed ? latencyReasonText(item.reason) : `${item.delay}ms`) : '— ms'
    latency.append(barWrap, text)

    historyList.append(time, cell, latency)
  })
  return historyList
}
const handlerHistoryTip = (e: Event) => {
  const history = recentHistory()
  const node = infoNode()

  // 没有历史也没有节点信息可看,就没有浮层
  if (!history.length && !node) return

  const historyList = buildHistoryList(history)

  // 不是真节点:浮层就是时间线本身
  if (!node) {
    // interactive:鼠标从标签移进浮层里不关,离开浮层才关
    showTip(e, historyList, {
      delay: [1000, 150],
      trigger: 'mouseenter',
      touch: false,
      interactive: true,
      interactiveBorder: 8,
      theme: 'base node-latency',
    })
    return
  }

  // 真节点:浮层是两个页签(测速延时 / 节点信息),打开时才挂上去,关了就卸掉
  const host = document.createElement('div')
  let mounted = false
  showTip(e, host, {
    delay: [1000, 150],
    trigger: 'mouseenter',
    touch: false,
    interactive: true,
    interactiveBorder: 8,
    theme: 'base node-latency',
    onShow: () => {
      if (mounted) return
      const vnode = createVNode(NodeLatencyTip, {
        name: props.name ?? '',
        udp: !!node.udp,
        history: history.length ? historyList : null,
        groupName: props.groupName,
        makeHistory: () => buildHistoryList(recentHistory()),
        onClose: hideTip,
      })
      vnode.appContext = appContext
      render(vnode, host)
      mounted = true
    },
    onDestroy: () => {
      if (mounted) render(null, host)
      mounted = false
    },
  })
}

const props = defineProps<{
  name?: string
  loading?: boolean
  groupName?: string
  // 悬停浮层看谁的时间线:组卡片标题的标签 name 是当前所选节点,时间线却要看组自己的
  timelineName?: string
}>()
const latencyRef = ref()
const { bandwidthMeasurements, bandwidthPendingNodes } = useBandwidth()
// 带宽段只给真节点(不是组、不是内置出站)显示,且要所属订阅开了检查
const bandwidth = computed(() => (infoNode() ? bandwidthMeasurements.value[props.name ?? ''] : undefined))
const bandwidthEnabled = computed(() => !!bandwidth.value?.enabled)
const bandwidthBusy = computed(() => !!bandwidth.value?.pending || !!bandwidthPendingNodes.value[props.name ?? ''])
// 上一次测失败了(没在测的时候)就在这一段显示归类后的原因,红字;悬停是原始报错
const bandwidthFailText = computed(() =>
  !bandwidthBusy.value && (bandwidth.value?.error || bandwidth.value?.failed) ? latencyReasonText(bandwidth.value?.reason || 'error') : '',
)
const latency = computed(() => getLatencyByName(props.name ?? '', props.groupName))
let countUp: CountUp | null = null

onMounted(() => {
  watch(latency, (value, OldValue) => {
    if (!countUp) {
      nextTick(() => {
        countUp = new CountUp(latencyRef.value, latency.value, {
          duration: 1,
          separator: '',
          enableScrollSpy: false,
          startVal: OldValue,
        })
        countUp?.update(value)
      })
    } else {
      countUp?.update(value)
    }
  })
})

onUnmounted(() => {
  countUp = null
})

const color = computed(() => {
  return getColorForLatency(latency.value)
})
</script>
