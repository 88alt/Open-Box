<template>
  <!-- 名字 + 名字下面的原始地址;默认插槽放名字旁边的操作(重命名 / 改分流) -->
  <div class="flex min-w-0 flex-col">
    <span
      class="truncate"
      :title="label"
    >
      {{ label }}
    </span>
    <span
      v-if="address"
      class="text-base-content/65 truncate text-xs"
      :title="address"
    >
      {{ address }}
    </span>
    <slot />
  </div>
</template>

<script setup lang="ts">
defineProps<{ label?: string; address?: string }>()
</script>
