<template>
  <button
    type="button"
    class="btn btn-circle btn-ghost btn-sm"
    aria-haspopup="dialog"
    :aria-label="label"
    :title="label"
  >
    <QuestionMarkCircleIcon class="h-4 w-4" />
  </button>
</template>

<script setup lang="ts">
import { QuestionMarkCircleIcon } from '@heroicons/vue/24/outline'

// 「问号」说明按钮:点开说明弹窗。点击事件直接落在这个 button 上(组件只有一个根元素,@click 会透传),
// 弹窗关闭后调用方可以把焦点还给它。
defineProps<{ label: string }>()
</script>
