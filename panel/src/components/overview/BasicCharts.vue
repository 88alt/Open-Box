<template>
  <div
    :class="
      title
        ? 'relative flex w-full flex-col rounded-xl border border-base-300/60 bg-base-100 p-3'
        : 'relative h-28 w-full overflow-hidden'
    "
  >
    <!-- 带标题(概览页四张图,对齐 v0.1.337):标题在左上、暂停键在右上,不画图例。
         暂停键是 ghost 按钮,图标颜色跟着这一行的 text-base-content/60 走(比正文色淡一档) -->
    <div
      v-if="title"
      class="text-base-content/60 flex items-center justify-between text-xs"
    >
      <span>{{ title }}</span>
      <button
        type="button"
        class="btn btn-ghost btn-square btn-xs -mr-1 -my-1"
        :aria-label="$t(isPaused ? 'chartResume' : 'chartPause')"
        v-tip="$t(isPaused ? 'chartResume' : 'chartPause')"
        @click="isPaused = !isPaused"
      >
        <component
          :is="!isPaused ? PauseCircleIcon : PlayCircleIcon"
          class="h-4 w-4"
        />
      </button>
    </div>
    <div
      v-if="title"
      class="mt-[var(--app-card-space)] flex flex-1 flex-col overflow-hidden rounded-b-[var(--app-radius-panel,1.25rem)]"
    >
      <div
        ref="chart"
        class="h-[100px] w-full"
      />
    </div>
    <div
      v-else
      ref="chart"
      class="h-full w-full"
    />
    <span
      class="border-b-primary/30 border-t-primary/60 border-l-info/30 border-r-info/60 text-base-content/10 bg-base-100/70 hidden"
      ref="colorRef"
    />
    <!-- 暂停键:概览和规则页的图有,侧边栏那张小图不放(pausable=false) -->
    <button
      v-if="!title && pausable !== false"
      class="btn btn-ghost btn-xs absolute right-1 bottom-0"
      @click="isPaused = !isPaused"
    >
      <component
        :is="!isPaused ? PauseCircleIcon : PlayCircleIcon"
        class="h-4 w-4"
      />
    </button>
  </div>
</template>

<script setup lang="ts">
import { cssColorToRgb, isMiddleScreen } from '@/helper/utils'
import { isWindowResizing } from '@/helper/windowResizeState'
import { font, theme } from '@/store/settings'
import { PauseCircleIcon, PlayCircleIcon } from '@heroicons/vue/24/outline'
import { useElementSize } from '@vueuse/core'
import { LineChart } from 'echarts/charts'
import {
  GridComponent,
  LegendComponent,
  TooltipComponent,
  VisualMapComponent,
} from 'echarts/components'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { debounce } from 'lodash'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'

echarts.use([
  LineChart,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  VisualMapComponent,
  CanvasRenderer,
])

// 布尔 prop 不传时 Vue 会当成 false,所以「不传就有暂停键」得靠 withDefaults 写成 true
const props = withDefaults(defineProps<{
  // tone 单条曲线指定颜色;solidStroke 曲线用不透明的实色(默认是 60% 透明)
  data: {
    name: string
    color?: number
    tone?: 'primary' | 'info' | 'warning'
    solidStroke?: boolean
    data: { name: number; value: number }[]
  }[]
  // 两个分界值:低于第一个画绿,低于第二个画橙,更高画红(CPU 负载用),图例固定关掉时配合 legend=false
  valueThresholds?: [number, number]
  // 要不要画图例;不传就画(带标题的卡片不画)
  legend?: boolean
  // 右下角要不要放暂停键;不传就有
  pausable?: boolean
  // 带标题时画成独立卡片:标题在左上、暂停键在右上、不画图例
  title?: string
  // 单条曲线时指定颜色(进站 primary、出站 info);不传就按位置定
  tone?: 'primary' | 'info'
  // 鼠标悬停要不要弹数值浮层;不传就有(侧边栏那张小图关掉)
  tooltip?: boolean
  labelFormatter: (value: number) => string
  toolTipFormatter: (value: ToolTipParams[]) => string
  min: number
}>(), { pausable: true, tooltip: true })

const colorRef = ref()
const chart = ref()
const isPaused = ref(false)
const colorSet = {
  primary: '',
  primary30: '',
  primary60: '',
  info: '',
  info30: '',
  info60: '',
  warning: '',
  warning30: '',
  warning60: '',
  thresholdLow: '',
  thresholdMedium: '',
  thresholdHigh: '',
  baseContent10: '',
  baseContent: '',
  base70: '',
}

let fontFamily = ''

const updateColorSet = () => {
  const colorStyle = getComputedStyle(colorRef.value)

  colorSet.baseContent = cssColorToRgb(colorStyle.getPropertyValue('--color-base-content'))
  colorSet.base70 = cssColorToRgb(colorStyle.backgroundColor)
  colorSet.baseContent10 = cssColorToRgb(colorStyle.color)
  colorSet.primary30 = cssColorToRgb(colorStyle.borderTopColor)
  colorSet.primary60 = cssColorToRgb(colorStyle.borderBottomColor)
  colorSet.info30 = cssColorToRgb(colorStyle.borderLeftColor)
  colorSet.info60 = cssColorToRgb(colorStyle.borderRightColor)
  colorSet.primary = cssColorToRgb(colorStyle.getPropertyValue('--color-primary'))
  colorSet.info = cssColorToRgb(colorStyle.getPropertyValue('--color-info'))
  const warning = colorStyle.getPropertyValue('--color-warning')
  colorSet.warning = cssColorToRgb(warning)
  colorSet.warning30 = cssColorToRgb(`color-mix(in oklab, ${warning} 30%, transparent)`)
  colorSet.warning60 = cssColorToRgb(`color-mix(in oklab, ${warning} 60%, transparent)`)
  colorSet.thresholdLow = cssColorToRgb(colorStyle.getPropertyValue('--app-latency-low'))
  colorSet.thresholdMedium = cssColorToRgb(colorStyle.getPropertyValue('--app-latency-medium'))
  colorSet.thresholdHigh = cssColorToRgb(colorStyle.getPropertyValue('--app-latency-high'))
}
const updateFontFamily = () => {
  const baseColorStyle = getComputedStyle(colorRef.value)

  fontFamily = baseColorStyle.fontFamily
}

// 图太窄(< 240px)时纵轴标签缩成 10px、左边留 40px,和 v0.1.337 一样
const { width: chartWidth } = useElementSize(chart)

const options = computed(() => {
  const narrow = chartWidth.value > 0 && chartWidth.value < 240
  const thresholds = props.valueThresholds
  const showLegend = props.legend !== false && !props.title
  const bare = !showLegend
  return {
    // 按值分段着色(CPU 负载):线和面积都按分界值变色,面积淡一点
    visualMap: thresholds && {
      type: 'piecewise',
      show: false,
      pieces: [
        { lt: thresholds[0], color: colorSet.thresholdLow },
        { gte: thresholds[0], lt: thresholds[1], color: colorSet.thresholdMedium },
        { gte: thresholds[1], color: colorSet.thresholdHigh },
      ],
    },
    // 画布贴着卡片:图例不留内边距、绘图区四边不留,纵轴标签压在绘图区里面(inside),和 v0.1.413 一样
    legend: {
      show: showLegend,
      bottom: 0,
      padding: 0,
      left: narrow ? 0 : 'center',
      data: props.data.map((item) => item.name),
      textStyle: {
        color: colorSet.baseContent,
        fontFamily,
      },
    },
    grid: {
      left: 0,
      top: 8,
      right: 0,
      bottom: bare ? 0 : 20,
    },
    tooltip: {
      show: props.tooltip !== false,
      trigger: 'axis',
      backgroundColor: colorSet.base70,
      borderColor: colorSet.base70,
      confine: true,
      padding: [0, 5],
      textStyle: {
        color: colorSet.baseContent,
        fontFamily,
      },
      formatter: props.toolTipFormatter,
    },
    xAxis: {
      type: 'category',
      boundaryGap: false,
      axisLine: { show: false },
      axisLabel: { show: false },
      splitLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      z: 5,
      splitNumber: 4,
      max: (value: { max: number }) => {
        return Math.max(value.max, props.min)
      },
      axisLine: { show: false },
      splitLine: {
        show: true,
        lineStyle: {
          type: 'dashed',
          color: colorSet.baseContent10,
        },
      },
      axisLabel: {
        align: 'left',
        inside: true,
        margin: 4,
        // 不画图例(带标题或 legend=false)时不画最底下那个 0 刻度,曲线直接贴卡片底边
        showMinLabel: bare ? false : undefined,
        formatter: props.labelFormatter,
        color: colorSet.baseContent,
        fontFamily,
        ...(narrow ? { fontSize: 10 } : {}),
      },
    },
    series: props.data.map((item, index) => {
      const tone =
        item.tone ??
        (props.tone ? props.tone : index === props.data.length - 1 ? 'primary' : 'info')
      const seriesColor = colorSet[`${tone}60`]
      const areaColor = colorSet[`${tone}30`]

      return {
        name: item.name,
        symbol: 'none',
        emphasis: {
          disabled: true,
        },
        lineStyle: {
          width: 1,
        },
        data: item.data,
        areaStyle: thresholds
          ? { opacity: 0.22 }
          : {
              color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
                {
                  offset: 0,
                  color: seriesColor,
                },
                {
                  offset: 1,
                  color: areaColor,
                },
              ]),
            },
        type: 'line',
        color: item.solidStroke ? colorSet[tone] : seriesColor,
        smooth: true,
      }
    }),
  }
})

let myChart: echarts.ECharts | null = null
let touchEndHandler: ((e: TouchEvent) => void) | null = null
const resize = debounce(() => {
  myChart?.resize()
}, 220)

onMounted(() => {
  updateColorSet()
  updateFontFamily()

  watch(theme, updateColorSet)
  watch(font, updateFontFamily)

  myChart = echarts.init(chart.value)

  myChart.setOption(options.value)

  watch(options, () => {
    if (isPaused.value || isWindowResizing.value) {
      return
    }
    myChart?.setOption(options.value)
  })

  watch(chartWidth, resize)

  watch(isWindowResizing, (resizing) => {
    if (resizing || isPaused.value) {
      return
    }

    myChart?.setOption(options.value)
    resize()
  })

  // 移动端：松手后自动隐藏 tooltip
  if (isMiddleScreen.value && chart.value) {
    touchEndHandler = () => {
      if (myChart) {
        myChart.dispatchAction({ type: 'hideTip' })
      }
    }
    chart.value.addEventListener('touchend', touchEndHandler)
  }
})

onUnmounted(() => {
  resize.cancel()
  if (chart.value && touchEndHandler) {
    chart.value.removeEventListener('touchend', touchEndHandler)
  }
  if (myChart) {
    myChart.dispose()
    myChart = null
  }
})
</script>
