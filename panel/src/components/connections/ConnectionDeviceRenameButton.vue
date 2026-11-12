<template>
  <button
    type="button"
    class="text-base-content/65 hover:text-base-content inline-flex shrink-0 cursor-pointer items-center gap-1 text-xs whitespace-nowrap"
    @click.stop="emit('rename')"
  >
    {{ $t('connectionDeviceRename') }}
    <PencilSquareIcon
      class="size-3"
      aria-hidden="true"
    />
  </button>
</template>

<script setup lang="ts">
import { PencilSquareIcon } from '@heroicons/vue/24/outline'

const emit = defineEmits<{ rename: [] }>()
</script>
