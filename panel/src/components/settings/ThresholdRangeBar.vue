<template>
  <div
    class="min-w-0 w-full select-none"
    role="group"
    :aria-label="label"
  >
    <div class="relative mx-3 mb-0.5 h-5">
      <div
        v-for="(segment, index) in segments"
        :key="`label-${segment.key}`"
        :ref="(el) => setSegmentLabelRef(index, el)"
        class="absolute bottom-0 flex -translate-x-1/2 items-center gap-1 whitespace-nowrap text-xs z-10"
        :class="dragging ? '' : 'transition-[left] duration-150 ease-[cubic-bezier(0.16,1,0.3,1)]'"
        :style="{ left: segmentLabelLeft(index) }"
      >
        <span
          class="size-2 shrink-0 rounded-full"
          :class="segment.dotClass || segment.colorClass"
          aria-hidden="true"
        />
        <span>{{ segment.label }}</span>
      </div>
    </div>
    <div
      ref="trackRef"
      class="relative mx-3 h-7 touch-none select-none"
      @pointerdown="onPointerDown($event)"
      @pointermove="onPointerMove"
      @pointerup="onPointerEnd"
      @pointercancel="onPointerEnd"
      @lostpointercapture="onPointerEnd"
    >
      <div
        class="bg-base-300 absolute inset-x-0 top-1/2 flex h-2 -translate-y-1/2 overflow-hidden rounded-full"
        aria-hidden="true"
      >
        <span
          v-for="bar in segmentBars"
          :key="bar.key"
          class="h-full shrink-0"
          :class="[
            bar.colorClass,
            dragging ? '' : 'transition-[width] duration-150 ease-[cubic-bezier(0.16,1,0.3,1)]',
          ]"
          :style="{ width: bar.width + '%' }"
        />
      </div>
      <div
        v-for="(marker, index) in markers"
        :key="marker.key"
        role="slider"
        :tabindex="disabled ? -1 : 0"
        :aria-disabled="disabled || undefined"
        :aria-label="marker.ariaLabel || marker.label"
        :aria-valuemin="marker.min"
        :aria-valuemax="marker.max"
        :aria-valuenow="marker.value"
        :aria-valuetext="`${marker.label}: ${formatValue(marker.value)}${unitGap}${unit}`"
        class="absolute grid size-7 -translate-x-1/2 -translate-y-1/2 cursor-grab place-items-center rounded-full outline-offset-1 focus-visible:z-20 focus-visible:outline-2 active:cursor-grabbing"
        :class="[
          dragIndex === index ? 'z-20' : 'z-10',
          dragging ? '' : 'transition-[left,top] duration-150 ease-[cubic-bezier(0.16,1,0.3,1)]',
        ]"
        :style="{ left: percents[index] + '%', top: markerTop(index) }"
        @pointerdown.stop="onPointerDown($event, index)"
        @keydown="onKeydown($event, index)"
      >
        <span
          class="bg-base-100 border-base-content/55 size-4 rounded-full border-2 shadow-sm"
          aria-hidden="true"
        />
      </div>
    </div>
    <div class="relative mx-3 mt-0.5 h-5 tabular-nums">
      <span
        v-if="minLabel"
        ref="minEndpointRef"
        class="text-base-content/40 pointer-events-none absolute top-1/2 left-0 -translate-y-1/2 text-[11px] whitespace-nowrap transition-opacity duration-150"
        :class="minLabelHidden ? 'opacity-0' : 'opacity-100'"
        aria-hidden="true"
      >
        {{ minLabel }}
      </span>
      <span
        v-if="maxLabel"
        ref="maxEndpointRef"
        class="text-base-content/40 pointer-events-none absolute top-1/2 right-0 -translate-y-1/2 text-[11px] whitespace-nowrap transition-opacity duration-150"
        :class="maxLabelHidden ? 'opacity-0' : 'opacity-100'"
        aria-hidden="true"
      >
        {{ maxLabel }}
      </span>
      <div
        v-for="(marker, index) in markers"
        :key="`value-${marker.key}`"
        :ref="(el) => setValueLabelRef(index, el)"
        class="absolute top-1/2 -translate-x-1/2 -translate-y-1/2 whitespace-nowrap text-xs"
        :class="dragIndex === index || editIndex === index ? 'z-20' : 'z-10'"
        :style="{ left: valueLabelLeft(index) }"
      >
        <input
          v-if="editIndex === index"
          ref="editInputRef"
          v-model="editValue"
          type="number"
          class="input input-xs h-5 w-16 px-1 text-center font-semibold tabular-nums"
          :min="marker.min"
          :max="marker.max"
          :step="inputStepOf(marker)"
          :aria-label="marker.ariaLabel || marker.label"
          @blur="commitEdit(index)"
          @keydown.enter.prevent="commitEdit(index)"
          @keydown.esc.prevent="cancelEdit"
        />
        <button
          v-else
          type="button"
          class="hover:bg-base-200 focus-visible:outline-primary rounded px-1 py-0.5 font-semibold transition-colors focus-visible:outline-2"
          :class="dragIndex === index && 'bg-base-200/80'"
          :title="marker.ariaLabel || marker.label"
          @click="startEdit(index)"
        >
          {{ formatValue(marker.value) }}{{ unitGap }}{{ unit }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

interface ThresholdStop {
  value: number
  percent: number
  step?: number
}
interface ThresholdSegment {
  key: string
  label: string
  colorClass: string
  dotClass?: string
}
interface ThresholdMarker {
  key: string
  label: string
  ariaLabel?: string
  value: number
  min: number
  max: number
  step?: number
  inputStep?: number
  dotClass?: string
}

const props = withDefaults(
  defineProps<{
    label: string
    markers: ThresholdMarker[]
    segments: ThresholdSegment[]
    stops: ThresholdStop[]
    unit: string
    unitSpace?: boolean
    integer?: boolean
    minLabel?: string
    maxLabel?: string
    disabled?: boolean
  }>(),
  { unitSpace: true, integer: false, disabled: false },
)
const emit = defineEmits<{ update: [index: number, value: number] }>()

// 相邻标签之间至少留的间距(px)
const LABEL_GAP = 6

const unitGap = computed(() => (props.unitSpace ? ' ' : ''))

const formatValue = (value: number) =>
  Number.isFinite(value)
    ? props.integer
      ? String(Math.round(value))
      : String(Number(value.toFixed(2)))
    : '0'

const inputStepOf = (marker: ThresholdMarker) =>
  marker.inputStep !== undefined ? marker.inputStep : props.integer ? 1 : (marker.step ?? 'any')

// 数值 → 轨道百分比(按分段刻度折线插值)
const valueToPercent = (value: number) => {
  const stops = props.stops
  if (stops.length < 2) return 0
  if (value <= stops[0].value) return stops[0].percent
  const last = stops[stops.length - 1]
  if (value >= last.value) return last.percent
  for (let i = 1; i < stops.length; i++) {
    const prev = stops[i - 1]
    const next = stops[i]
    if (value <= next.value) {
      const span = next.value - prev.value
      if (span <= 0) return next.percent
      const ratio = (value - prev.value) / span
      return prev.percent + ratio * (next.percent - prev.percent)
    }
  }
  return last.percent
}

// 轨道百分比 → 数值(按所在刻度段的步长吸附)
const percentToValue = (percent: number) => {
  const stops = props.stops
  if (stops.length < 2) return 0
  const pct = Math.min(100, Math.max(0, percent))
  for (let i = 1; i < stops.length; i++) {
    const prev = stops[i - 1]
    const next = stops[i]
    if (pct <= next.percent || i === stops.length - 1) {
      const span = next.percent - prev.percent
      const ratio = span > 0 ? (pct - prev.percent) / span : 0
      const raw = prev.value + ratio * (next.value - prev.value)
      const step = next.step ?? (props.integer ? 1 : 0.1)
      const snapped = step > 0 ? Math.round(raw / step) * step : raw
      return props.integer ? Math.round(snapped) : Number(snapped.toFixed(6))
    }
  }
  return stops[stops.length - 1].value
}

const percents = computed(() => props.markers.map((marker) => valueToPercent(marker.value)))

const segmentBars = computed(() => {
  const edges = [0, ...percents.value, 100]
  return props.segments.map((segment, index) => {
    const from = edges[index] ?? 100
    const to = edges[index + 1] ?? 100
    return { key: segment.key, colorClass: segment.colorClass, width: Math.max(0, to - from) }
  })
})

const segmentCenters = computed(() => {
  const edges = [0, ...percents.value, 100]
  return props.segments.map((_, index) => {
    const from = edges[index] ?? 100
    const to = edges[index + 1] ?? 100
    return (from + to) / 2
  })
})

// 两个滑块靠得太近时,上下错开放置,避免手柄叠在一起
const markerTop = (index: number) => {
  const list = percents.value
  const nearPrev = index > 0 && list[index] - list[index - 1] < 7
  const nearNext = index < list.length - 1 && list[index + 1] - list[index] < 7
  if (!nearPrev && !nearNext) return '50%'
  return index % 2 === 0 ? '28%' : '72%'
}

const trackRef = ref<HTMLElement | null>(null)
const trackWidth = ref(0)
const segmentLabelEls = ref<(HTMLElement | null)[]>([])
const segmentLabelWidths = ref<number[]>([])
const valueLabelEls = ref<(HTMLElement | null)[]>([])
const valueLabelWidths = ref<number[]>([])
const minEndpointRef = ref<HTMLElement | null>(null)
const maxEndpointRef = ref<HTMLElement | null>(null)
const minEndpointWidth = ref(0)
const maxEndpointWidth = ref(0)

const setSegmentLabelRef = (index: number, el: unknown) => {
  segmentLabelEls.value[index] = (el as HTMLElement | null) ?? null
}
const setValueLabelRef = (index: number, el: unknown) => {
  valueLabelEls.value[index] = (el as HTMLElement | null) ?? null
}

const measure = () => {
  if (trackRef.value) trackWidth.value = trackRef.value.clientWidth
  segmentLabelWidths.value = props.segments.map(
    (_, index) => segmentLabelEls.value[index]?.offsetWidth ?? 64,
  )
  valueLabelWidths.value = props.markers.map(
    (_, index) => valueLabelEls.value[index]?.offsetWidth ?? 48,
  )
  minEndpointWidth.value = minEndpointRef.value?.offsetWidth ?? 0
  maxEndpointWidth.value = maxEndpointRef.value?.offsetWidth ?? 0
}

let resizeObserver: ResizeObserver | null = null
onMounted(() => {
  measure()
  if (typeof ResizeObserver !== 'undefined' && trackRef.value) {
    resizeObserver = new ResizeObserver(measure)
    resizeObserver.observe(trackRef.value)
  }
})
onBeforeUnmount(() => {
  resizeObserver?.disconnect()
})
watch(
  () => [
    props.segments.map((segment) => segment.label).join('|'),
    props.markers.map((marker) => `${marker.label}:${marker.value}`).join('|'),
  ],
  () => {
    nextTick(measure)
  },
)

// 把一排标签的中心位置(百分比)换算成像素,并互相推开,保证不重叠、不超出轨道
const spreadLabels = (centers: number[], widths: number[]) => {
  const total = trackWidth.value
  const count = centers.length
  if (total <= 0 || count === 0) return centers
  const result = centers.map((center, index) => {
    const half = (widths[index] ?? 56) / 2
    return Math.min(total - half, Math.max(half, (center / 100) * total))
  })
  for (let i = 1; i < count; i++) {
    const minimum = result[i - 1] + (widths[i - 1] ?? 56) / 2 + (widths[i] ?? 56) / 2 + LABEL_GAP
    if (result[i] < minimum) result[i] = minimum
  }
  const lastHalf = (widths[count - 1] ?? 56) / 2
  if (result[count - 1] > total - lastHalf) result[count - 1] = total - lastHalf
  for (let i = count - 2; i >= 0; i--) {
    const maximum = result[i + 1] - (widths[i + 1] ?? 56) / 2 - (widths[i] ?? 56) / 2 - LABEL_GAP
    if (result[i] > maximum) result[i] = Math.max((widths[i] ?? 56) / 2, maximum)
  }
  return result
}

const segmentLabelPx = computed(() => spreadLabels(segmentCenters.value, segmentLabelWidths.value))
const valueLabelPx = computed(() => spreadLabels(percents.value, valueLabelWidths.value))

const segmentLabelLeft = (index: number) =>
  trackWidth.value <= 0 ? `${segmentCenters.value[index]}%` : `${segmentLabelPx.value[index]}px`
const valueLabelLeft = (index: number) =>
  trackWidth.value <= 0 ? `${percents.value[index]}%` : `${valueLabelPx.value[index]}px`

// 数值标签挡住两端的最小 / 最大值提示时,把提示隐藏
const minLabelHidden = computed(() =>
  !props.minLabel || trackWidth.value <= 0 || valueLabelPx.value.length === 0
    ? false
    : valueLabelPx.value[0] - (valueLabelWidths.value[0] ?? 48) / 2 < minEndpointWidth.value + 8,
)
const maxLabelHidden = computed(() => {
  if (!props.maxLabel || trackWidth.value <= 0 || valueLabelPx.value.length === 0) return false
  const last = valueLabelPx.value.length - 1
  return (
    valueLabelPx.value[last] + (valueLabelWidths.value[last] ?? 48) / 2 >
    trackWidth.value - maxEndpointWidth.value - 8
  )
})

// 点数值直接输入
const editIndex = ref<number | null>(null)
const editValue = ref('')
const editInputRef = ref<HTMLInputElement | HTMLInputElement[] | null>(null)

const startEdit = (index: number) => {
  if (props.disabled) return
  const marker = props.markers[index]
  if (!marker) return
  editIndex.value = index
  editValue.value = formatValue(marker.value)
  nextTick(() => {
    const el = Array.isArray(editInputRef.value) ? editInputRef.value[0] : editInputRef.value
    el?.focus()
    el?.select()
  })
}
const cancelEdit = () => {
  editIndex.value = null
}
const setMarkerValue = (index: number, value: number) => {
  const marker = props.markers[index]
  if (!marker || !Number.isFinite(value)) return
  const clamped = Math.min(marker.max, Math.max(marker.min, value))
  const next = props.integer ? Math.round(clamped) : Number(clamped.toFixed(6))
  if (next !== marker.value) emit('update', index, next)
}
const commitEdit = (index: number) => {
  if (editIndex.value !== index) return
  const value = Number(editValue.value)
  editIndex.value = null
  if (Number.isFinite(value)) setMarkerValue(index, value)
}

// 拖动
const dragging = ref(false)
const dragIndex = ref<number | null>(null)
let dragState: { pointer: number; index: number; offsetPct: number } | null = null

const pointerPercent = (event: PointerEvent, track: HTMLElement) => {
  const rect = track.getBoundingClientRect()
  return rect.width <= 0 ? 0 : ((event.clientX - rect.left) / rect.width) * 100
}
const nearestMarker = (percent: number) => {
  const list = percents.value
  let best = 0
  let distance = Infinity
  for (let i = 0; i < list.length; i++) {
    const d = Math.abs(percent - list[i])
    if (d < distance) {
      distance = d
      best = i
    }
  }
  return best
}
const onPointerDown = (event: PointerEvent, index?: number) => {
  const track = trackRef.value
  if (props.disabled || !track || dragState || !event.isPrimary || event.button !== 0) return
  editIndex.value = null
  const percent = pointerPercent(event, track)
  const target = index ?? nearestMarker(percent)
  event.preventDefault()
  ;(track.querySelectorAll('[role="slider"]').item(target) as HTMLElement | null)?.focus({
    preventScroll: true,
  })
  track.setPointerCapture(event.pointerId)
  dragging.value = true
  dragIndex.value = target
  dragState = {
    pointer: event.pointerId,
    index: target,
    offsetPct: index === undefined ? 0 : percents.value[target] - percent,
  }
  if (index === undefined) setMarkerValue(target, percentToValue(percent))
}
const onPointerMove = (event: PointerEvent) => {
  const track = trackRef.value
  if (props.disabled || !track || dragState?.pointer !== event.pointerId) return
  const percent = pointerPercent(event, track) + dragState.offsetPct
  setMarkerValue(dragState.index, percentToValue(percent))
}
const onPointerEnd = (event: PointerEvent) => {
  if (dragState?.pointer === event.pointerId) {
    dragState = null
    dragging.value = false
    dragIndex.value = null
  }
}

// 键盘:方向键按步长,Shift 五倍,PageUp / PageDown 五步,Home / End 到端点
const keyStep = (marker: ThresholdMarker) => {
  if (marker.step !== undefined) return marker.step
  for (let i = 1; i < props.stops.length; i++) {
    if (marker.value <= props.stops[i].value && props.stops[i].step) return props.stops[i].step as number
  }
  return 1
}
const onKeydown = (event: KeyboardEvent, index: number) => {
  if (props.disabled) return
  const marker = props.markers[index]
  if (!marker) return
  const base = keyStep(marker)
  const step = event.shiftKey ? base * 5 : base
  let next = marker.value
  switch (event.key) {
    case 'ArrowLeft':
    case 'ArrowDown':
      next -= step
      break
    case 'ArrowRight':
    case 'ArrowUp':
      next += step
      break
    case 'PageDown':
      next -= base * 5
      break
    case 'PageUp':
      next += base * 5
      break
    case 'Home':
      next = marker.min
      break
    case 'End':
      next = marker.max
      break
    default:
      return
  }
  event.preventDefault()
  setMarkerValue(index, next)
}
</script>
