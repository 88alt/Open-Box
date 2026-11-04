<template>
  <div
    class="app-card-gap grid min-w-0"
    :style="`grid-template-columns: repeat(auto-fill, minmax(min(${minProxyCardWidth}px, 100%), 1fr));`"
  >
    <slot />
  </div>
</template>

<script lang="ts" setup>
import { minProxyCardWidth } from '@/store/settings'
</script>
