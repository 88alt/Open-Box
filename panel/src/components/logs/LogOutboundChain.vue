<template>
  <!-- 出站链:最前面是命中的策略组(有的话),后面是一路经过的出站 -->
  <div
    class="flex min-w-0 items-center gap-1"
    :class="singleLine ? 'w-max flex-nowrap whitespace-nowrap' : 'flex-wrap'"
  >
    <span
      v-if="policy"
      class="badge badge-ghost badge-sm"
    >
      {{ policy }}
    </span>
    <template
      v-for="(name, index) in chains"
      :key="`${name}-${index}`"
    >
      <ArrowRightCircleIcon
        v-if="index > 0 || policy"
        class="size-4 shrink-0"
      />
      <ProxyName :name="name" />
    </template>
  </div>
</template>

<script setup lang="ts">
import ProxyName from '@/components/proxies/ProxyName.vue'
import { ArrowRightCircleIcon } from '@heroicons/vue/24/outline'

defineProps<{ chains: string[]; policy?: string; singleLine?: boolean }>()
</script>
