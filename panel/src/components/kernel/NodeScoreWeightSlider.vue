<template>
  <div
    class="w-full min-w-0 select-none"
    role="group"
    :aria-label="$t('nodeScoreWeights')"
  >
    <div
      ref="trackRef"
      class="relative mx-3 h-8 touch-none"
      @pointerdown="onTrackDown"
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
          v-for="(seg, i) in segments"
          :key="seg.key"
          class="h-full shrink-0"
          :class="[seg.color, dragging ? '' : 'transition-[width] duration-150']"
          :style="{ width: widths[i] + '%' }"
        />
      </div>
      <div
        v-for="(pos, i) in dividers"
        :key="i"
        role="slider"
        :tabindex="disabled ? -1 : 0"
        :aria-disabled="disabled || undefined"
        :aria-label="$t(i === 0 ? 'nodeScoreWeightFirstDivider' : 'nodeScoreWeightSecondDivider')"
        :aria-valuemin="i === 0 ? 0 : dividers[0]"
        :aria-valuemax="i === 0 ? dividers[1] : 100"
        :aria-valuenow="round(pos)"
        :aria-valuetext="valueText"
        class="absolute z-10 grid size-7 -translate-x-1/2 -translate-y-1/2 cursor-grab place-items-center rounded-full outline-offset-1 focus-visible:z-20 focus-visible:outline-2 active:cursor-grabbing"
        :class="dragging ? '' : 'transition-[left] duration-150'"
        :style="{ left: pos + '%', top: '50%' }"
        @pointerdown.stop="onHandleDown($event, i)"
        @keydown="onKeydown($event, i)"
      >
        <span
          class="bg-base-100 border-base-content/55 size-4 rounded-full border-2 shadow-sm"
          aria-hidden="true"
        />
      </div>
    </div>
    <ul class="mx-3 mt-1 flex flex-wrap justify-between gap-x-4 gap-y-1 text-xs tabular-nums">
      <li
        v-for="seg in segments"
        :key="seg.key"
        class="flex items-center gap-1 whitespace-nowrap"
      >
        <span
          class="size-2 shrink-0 rounded-full"
          :class="seg.color"
          aria-hidden="true"
        />
        <span>{{ $t(seg.label) }}</span>
        <strong class="font-semibold">{{ round(model[seg.key]) }}%</strong>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

export interface NodeScoreWeights {
  latency: number
  download: number
  jitter: number
}

const props = defineProps<{ disabled?: boolean }>()
const model = defineModel<NodeScoreWeights>({ required: true })
const { t } = useI18n()

const segments = [
  { key: 'latency', label: 'nodeScoreWeightLatency', color: 'bg-primary' },
  { key: 'download', label: 'nodeScoreWeightDownload', color: 'bg-blue-500' },
  { key: 'jitter', label: 'nodeScoreWeightJitter', color: 'bg-warning' },
] as const

// 避免 0.1 步进累积出 33.300000000000004 这种尾巴
const clean = (v: number) => Number(v.toFixed(10))
// 显示只留一位小数(拖到整数时不带小数点)
const round = (v: number) => Number(v.toFixed(1))

// 两个分界点:第一个 = 延时权重,第二个 = 100 − 抖动权重;带宽权重是两者之差
const dividers = computed(() => [model.value.latency, 100 - model.value.jitter])
const widths = computed(() => [model.value.latency, model.value.download, model.value.jitter])
const valueText = computed(() =>
  t('nodeScoreWeightValues', {
    latency: round(model.value.latency),
    download: round(model.value.download),
    jitter: round(model.value.jitter),
  }),
)

// 移动某个分界点:只动相邻两项,合计保持 100;分界点不能越过另一个
const moveDivider = (index: number, raw: number) => {
  const [d1, d2] = dividers.value
  if (index === 0) {
    const next = clean(Math.min(d2, Math.max(0, raw)))
    model.value = { latency: next, download: clean(d2 - next), jitter: model.value.jitter }
  } else {
    const next = clean(Math.min(100, Math.max(d1, raw)))
    model.value = {
      latency: model.value.latency,
      download: clean(next - d1),
      jitter: clean(100 - next),
    }
  }
}

const trackRef = ref<HTMLElement | null>(null)
const dragging = ref(false)
let drag: { pointer: number; index: number; offset: number } | null = null

const percentOf = (event: PointerEvent) => {
  const rect = trackRef.value?.getBoundingClientRect()
  return !rect || rect.width <= 0 ? 0 : ((event.clientX - rect.left) / rect.width) * 100
}
// 拖动时吸附到 0.1 个百分点
const snap = (v: number) => Math.round(v * 10) / 10

const begin = (event: PointerEvent, index: number, offset: number) => {
  const track = trackRef.value
  if (!track || drag || props.disabled || !event.isPrimary || event.button !== 0) return false
  event.preventDefault()
  ;(track.querySelectorAll('[role="slider"]').item(index) as HTMLElement | null)?.focus({
    preventScroll: true,
  })
  track.setPointerCapture(event.pointerId)
  drag = { pointer: event.pointerId, index, offset }
  dragging.value = true
  return true
}

// 直接按在手柄上:保持手柄与指针的相对偏移,不跳变
const onHandleDown = (event: PointerEvent, index: number) => {
  begin(event, index, dividers.value[index] - percentOf(event))
}
// 按在轨道上:最近的分界点跳到指针处(正好居中时选第一个)
const onTrackDown = (event: PointerEvent) => {
  const p = percentOf(event)
  const [d1, d2] = dividers.value
  const index = Math.abs(p - d1) <= Math.abs(p - d2) ? 0 : 1
  if (begin(event, index, 0)) moveDivider(index, snap(p))
}
const onPointerMove = (event: PointerEvent) => {
  if (!drag || drag.pointer !== event.pointerId) return
  moveDivider(drag.index, snap(percentOf(event) + drag.offset))
}
const onPointerEnd = (event: PointerEvent) => {
  if (drag?.pointer === event.pointerId) {
    drag = null
    dragging.value = false
  }
}

// 键盘:方向键 1 个百分点,Shift 0.1;Home / End 到可动范围的两端
const onKeydown = (event: KeyboardEvent, index: number) => {
  if (props.disabled) return
  const step = event.shiftKey ? 0.1 : 1
  const cur = dividers.value[index]
  let next: number
  switch (event.key) {
    case 'ArrowLeft':
    case 'ArrowDown':
      next = cur - step
      break
    case 'ArrowRight':
    case 'ArrowUp':
      next = cur + step
      break
    case 'Home':
      next = index === 0 ? 0 : dividers.value[0]
      break
    case 'End':
      next = index === 0 ? dividers.value[1] : 100
      break
    default:
      return
  }
  event.preventDefault()
  moveDivider(index, next)
}
</script>
