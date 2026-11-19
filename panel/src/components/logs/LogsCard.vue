<template>
  <div class="card log-list-card app-card-inset flex w-full flex-col gap-1 text-start text-sm">
    <span class="flex w-full min-w-0 items-center gap-2">
      <span class="text-base-content/65 shrink-0 tabular-nums">{{ row.log.time }}</span>
      <span
        class="badge badge-sm shrink-0 whitespace-nowrap"
        :class="textColorMapForType[row.level as keyof typeof textColorMapForType]"
      >
        {{ severityLabel }}
      </span>
      <span
        v-if="row.log.probe"
        class="badge badge-xs shrink-0"
      >
        {{ $t('logNodeProbe') }}
      </span>
    </span>

    <div
      v-if="hasMeta"
      class="grid w-full grid-cols-2 items-start gap-x-2 gap-y-1 text-xs sm:grid-cols-[minmax(0,1.25fr)_minmax(0,2fr)_minmax(0,1.75fr)] sm:text-sm"
    >
      <div class="flex min-w-0 flex-col">
        <span class="truncate">{{ row.sourceLabel || row.sourceAddress }}</span>
        <span
          v-if="row.sourceLabel && row.sourceAddress"
          class="text-base-content/65 truncate text-xs"
        >
          {{ row.sourceAddress }}
        </span>
        <ConnectionDeviceRenameButton
          v-if="row.sourceAddress"
          @rename="emit('rename', row)"
        />
      </div>
      <div class="min-w-0 text-center max-sm:order-last max-sm:col-span-2">
        <div
          v-if="row.outboundChain?.length || row.routingPolicy"
          class="flex min-w-0 flex-wrap items-center justify-center gap-1"
        >
          <span
            v-if="row.routingPolicy"
            class="badge badge-ghost badge-sm"
          >
            {{ row.routingPolicy }}
          </span>
          <template
            v-for="(name, index) in row.outboundChain"
            :key="`${name}-${index}`"
          >
            <ArrowRightCircleIcon
              v-if="index > 0 || row.routingPolicy"
              class="size-4 shrink-0"
            />
            <ProxyName :name="name" />
          </template>
        </div>
        <span
          v-else-if="row.module"
          class="text-base-content/65 font-mono text-xs [overflow-wrap:anywhere]"
        >
          {{ row.module }}
        </span>
      </div>
      <div class="flex min-w-0 flex-col items-end text-end">
        <span class="max-w-full truncate">{{ row.targetAddress }}</span>
        <span
          v-if="row.targetFakeIp"
          class="text-base-content/65 max-w-full truncate text-xs"
        >
          {{ row.targetFakeIp }}
        </span>
        <ConnectionTargetRoutingButton :target="row.routingTarget" />
      </div>
    </div>

    <span class="w-full truncate">{{ row.message }}</span>
  </div>
</template>

<script setup lang="ts">
import ConnectionDeviceRenameButton from '@/components/connections/ConnectionDeviceRenameButton.vue'
import ConnectionTargetRoutingButton from '@/components/connections/ConnectionTargetRoutingButton.vue'
import ProxyName from '@/components/proxies/ProxyName.vue'
import { useBounceOnVisible } from '@/composables/bouncein'
import type { LogRow } from '@/composables/logRows'
import { LOG_LEVEL } from '@/constant'
import { logSeverityLabel } from '@/helper/logSeverity'
import { ArrowRightCircleIcon } from '@heroicons/vue/24/outline'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  row: LogRow
}>()
const emit = defineEmits<{ rename: [row: LogRow] }>()

const { t } = useI18n()
const severityLabel = computed(() => logSeverityLabel(props.row.level, t))
const hasMeta = computed(
  () =>
    !!(
      props.row.sourceLabel ||
      props.row.sourceAddress ||
      props.row.outboundChain?.length ||
      props.row.routingPolicy ||
      props.row.module ||
      props.row.targetAddress
    ),
)

const textColorMapForType = {
  [LOG_LEVEL.Trace]: 'text-success',
  [LOG_LEVEL.Debug]: 'text-accent',
  [LOG_LEVEL.Info]: 'text-info',
  [LOG_LEVEL.Warning]: 'text-warning',
  [LOG_LEVEL.Error]: 'text-error',
  [LOG_LEVEL.Fatal]: 'text-error',
  [LOG_LEVEL.Panic]: 'text-error',
}

useBounceOnVisible()
</script>
