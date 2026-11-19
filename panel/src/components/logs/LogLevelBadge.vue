<template>
  <span
    class="badge badge-sm shrink-0 whitespace-nowrap"
    :class="colors[level as keyof typeof colors]"
  >
    {{ logSeverityLabel(level, t) }}
  </span>
</template>

<script setup lang="ts">
import { LOG_LEVEL } from '@/constant'
import { logSeverityLabel } from '@/helper/logSeverity'
import { useI18n } from 'vue-i18n'

defineProps<{ level: string }>()

const { t } = useI18n()
const colors = {
  [LOG_LEVEL.Trace]: 'text-success',
  [LOG_LEVEL.Debug]: 'text-accent',
  [LOG_LEVEL.Info]: 'text-info',
  [LOG_LEVEL.Warning]: 'text-warning',
  [LOG_LEVEL.Error]: 'text-error',
  [LOG_LEVEL.Fatal]: 'text-error',
  [LOG_LEVEL.Panic]: 'text-error',
}
</script>
