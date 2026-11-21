<template>
  <div class="join number-stepper">
    <button
      type="button"
      class="btn btn-sm join-item w-10 shrink-0 px-0"
      :aria-label="$t('numberDecrease', { label })"
      :disabled="disabled || current <= min"
      @click="stepBy(-1)"
    >
      −
    </button>
    <label
      class="input input-sm join-item flex min-w-0 justify-center gap-0"
      :class="$slots.unit ? 'w-auto' : 'w-20'"
    >
      <input
        :id="id"
        class="number-stepper-value text-center tabular-nums"
        :class="$slots.unit && 'w-auto'"
        type="number"
        :aria-label="label"
        :aria-description="description"
        :value="current"
        :min="Number.isFinite(min) ? min : undefined"
        :max="Number.isFinite(max) ? max : undefined"
        :step="inputStep ?? step"
        :disabled="disabled"
        @change="commitInput"
        @keydown.enter="commitInput"
      />
      <span v-if="unit">{{ unit }}</span>
    </label>
    <slot name="unit" />
    <button
      type="button"
      class="btn btn-sm join-item w-10 shrink-0 px-0"
      :aria-label="$t('numberIncrease', { label })"
      :disabled="disabled || current >= max"
      @click="stepBy(1)"
    >
      +
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    modelValue: number
    label: string
    id?: string
    description?: string
    min?: number
    max?: number
    step?: number
    inputStep?: number | string
    unit?: string
    integer?: boolean
    disabled?: boolean
  }>(),
  { min: -Infinity, max: Infinity, step: 1, integer: false, disabled: false },
)
const emit = defineEmits<{
  (e: 'update:modelValue', value: number): void
  (e: 'change', value: number): void
}>()

const current = computed(() => Number(props.modelValue))

const clamp = (value: number) => {
  const base = Number.isFinite(value) ? value : current.value
  const next = Math.min(props.max, Math.max(props.min, props.integer ? Math.round(base) : base))
  if (next !== current.value) {
    emit('update:modelValue', next)
    emit('change', next)
  }
  return next
}

const stepBy = (direction: 1 | -1) => {
  clamp(Number((current.value + direction * props.step).toPrecision(15)))
}

const commitInput = (e: Event) => {
  const input = e.target as HTMLInputElement
  input.value = String(clamp(input.valueAsNumber))
}
</script>

<style scoped>
.number-stepper-value {
  min-width: 0;
  width: 100%;
  background: transparent;
  outline: none;
  appearance: textfield;
  -moz-appearance: textfield;
}
.number-stepper-value::-webkit-outer-spin-button,
.number-stepper-value::-webkit-inner-spin-button {
  -webkit-appearance: none;
  margin: 0;
}
</style>
