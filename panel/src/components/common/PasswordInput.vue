<template>
  <!-- 密码输入框:右侧一个小眼睛切换明文 / 密文(对齐上游 v0.1.413)。外观按普通 .input 来,
       class 落在外层 span 上,其余属性(placeholder / autocomplete / autofocus…)落在 input 上 -->
  <span :class="['input input-sm inline-flex min-w-0 items-center gap-2', $attrs.class]">
    <input
      v-bind="inputAttrs"
      v-model="value"
      :type="visible ? 'text' : 'password'"
      :disabled="disabled"
      class="min-w-0 grow"
    />
    <button
      type="button"
      class="text-base-content/60 hover:text-base-content flex shrink-0 cursor-pointer items-center disabled:cursor-default"
      :aria-label="$t(visible ? 'hidePassword' : 'showPassword')"
      :aria-pressed="visible"
      :disabled="disabled"
      @click.prevent.stop="visible = !visible"
      @keydown.enter.stop
    >
      <EyeSlashIcon
        v-if="visible"
        class="h-4 w-4"
      />
      <EyeIcon
        v-else
        class="h-4 w-4"
      />
    </button>
  </span>
</template>

<script setup lang="ts">
import { EyeIcon, EyeSlashIcon } from '@heroicons/vue/24/outline'
import { computed, ref, useAttrs, watch } from 'vue'

defineOptions({ inheritAttrs: false })

defineProps<{
  disabled?: boolean
}>()

const value = defineModel<string>({ default: '' })
const attrs = useAttrs()
// class 给外层 span,其余属性给 input
const inputAttrs = computed(() => Object.fromEntries(Object.entries(attrs).filter(([key]) => key !== 'class')))

// 输入框被清空后自动回到密文,下次再输入不会直接露出来
const visible = ref(false)
watch(value, (v) => {
  if (!v) visible.value = false
})
</script>
