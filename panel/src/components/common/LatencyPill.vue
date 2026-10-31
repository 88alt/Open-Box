<template>
  <!-- 延迟小药丸:没测过显示一个闪电图标,测完显示毫秒数(按阈值着色),失败显示错误文字,测着显示三个点。
       传了 testLabel 就渲染成可点击的按钮(点一下发起测速),不传就是只读的一枚标签。
       和 proxies/LatencyTag.vue 同一套外观(.latency-tag),但不绑具体节点:数值由调用方传进来,
       链式代理这类不在内核节点表里的东西也能用。 -->
  <component
    :is="testLabel ? 'button' : 'span'"
    :type="testLabel ? 'button' : undefined"
    :disabled="testLabel ? disabled || loading : undefined"
    :aria-label="testLabel"
    :aria-busy="loading || undefined"
    :class="
      twMerge(
        'latency-tag bg-base-200/50 inline-flex h-5 min-w-10 shrink-0 items-center justify-center rounded-xl px-1.5 text-xs tabular-nums select-none',
        error ? 'text-error' : getColorForLatency(ms ?? 0),
        testLabel &&
          'hover:bg-base-200 focus-visible:outline-primary cursor-pointer focus-visible:outline-2 focus-visible:outline-offset-2 disabled:cursor-default [&>*]:pointer-events-none',
      )
    "
  >
    <span
      v-if="loading"
      class="loading loading-dots loading-xs text-base-content/80"
    />
    <span
      v-else-if="error"
      class="max-w-32 truncate"
    >{{ error }}</span>
    <BoltIcon
      v-else-if="ms === undefined"
      class="text-base-content h-3 w-3"
    />
    <span v-else>{{ ms }}</span>
  </component>
</template>

<script setup lang="ts">
import { getColorForLatency } from '@/helper'
import { BoltIcon } from '@heroicons/vue/24/outline'
import { twMerge } from 'tailwind-merge'

defineProps<{
  // 毫秒数;undefined = 还没测过
  ms?: number
  // 有值就显示这段错误文字(红色),优先于 ms
  error?: string
  loading?: boolean
  disabled?: boolean
  // 可点击时的无障碍名字;传了才是按钮
  testLabel?: string
}>()
</script>
