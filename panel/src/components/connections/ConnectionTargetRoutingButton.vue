<template>
  <button
    v-if="target && open"
    type="button"
    class="text-base-content/65 hover:text-base-content inline-flex shrink-0 cursor-pointer items-center gap-1 text-xs whitespace-nowrap"
    @click.stop="open(target)"
    @keydown.stop
  >
    {{ $t('connectionEditTargetRouting') }}
    <PencilSquareIcon
      class="size-3"
      aria-hidden="true"
    />
  </button>
</template>

<script setup lang="ts">
import { PencilSquareIcon } from '@heroicons/vue/24/outline'
import { inject } from 'vue'
import { OPEN_TARGET_ROUTING } from './targetRoutingKey'

defineProps<{ target?: string }>()

const open = inject(OPEN_TARGET_ROUTING, null)
</script>
