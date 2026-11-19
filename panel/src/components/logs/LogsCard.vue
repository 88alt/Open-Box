<template>
  <div
    class="card log-list-card overflow-hidden"
    :class="{
      'rounded-t-none!': row.groupKey,
      'rounded-b-none!': joinedBelow,
      'card-title-divider': row.groupKey && followsHeading,
    }"
  >
    <div
      v-if="row.groupKey && !followsHeading"
      aria-hidden="true"
      class="border-base-content/10 mx-[var(--app-card-space)] border-t"
    />
    <div
      class="app-card-inset flex w-full cursor-pointer flex-col gap-1 text-start text-sm"
      :class="{ 'hover:bg-base-200': !row.groupKey }"
      @click="open"
    >
      <span class="flex w-full min-w-0 items-center gap-2">
        <button
          type="button"
          class="text-base-content/65 shrink-0 cursor-pointer tabular-nums"
          :aria-label="`${$t('logDetails')} · ${row.log.time}`"
          @click.stop="emit('open', row)"
        >
          {{ row.log.time }}
        </button>
        <LogLevelBadge :level="row.level" />
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
        <LogLabel
          :label="row.sourceLabel || row.sourceAddress"
          :address="row.sourceLabel ? row.sourceAddress : undefined"
        >
          <ConnectionDeviceRenameButton
            v-if="row.sourceAddress"
            @rename="emit('rename', row)"
          />
        </LogLabel>
        <div class="min-w-0 text-center max-sm:order-last max-sm:col-span-2">
          <LogOutboundChain
            v-if="row.outboundChain?.length || row.routingPolicy"
            :chains="row.outboundChain ?? []"
            :policy="row.routingPolicy"
            class="justify-center"
          />
          <span
            v-else-if="row.module"
            class="text-base-content/65 font-mono text-xs [overflow-wrap:anywhere]"
          >
            {{ row.module }}
          </span>
        </div>
        <LogLabel
          :label="row.targetAddress"
          :address="row.targetFakeIp"
          class="items-end text-end"
        >
          <ConnectionTargetRoutingButton :target="row.routingTarget" />
        </LogLabel>
      </div>

      <span class="w-full truncate">{{ row.message }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import ConnectionDeviceRenameButton from '@/components/connections/ConnectionDeviceRenameButton.vue'
import ConnectionTargetRoutingButton from '@/components/connections/ConnectionTargetRoutingButton.vue'
import LogLabel from '@/components/logs/LogLabel.vue'
import LogLevelBadge from '@/components/logs/LogLevelBadge.vue'
import LogOutboundChain from '@/components/logs/LogOutboundChain.vue'
import { useBounceOnVisible } from '@/composables/bouncein'
import type { LogRow } from '@/composables/logRows'
import { computed } from 'vue'

const props = defineProps<{
  row: LogRow & { groupKey?: string }
  // 下面紧跟着同一个组的日志(卡片之间不留缝)
  joinedBelow?: boolean
  // 上面紧挨着的是组头 / 子组头
  followsHeading?: boolean
}>()
const emit = defineEmits<{ open: [row: LogRow]; rename: [row: LogRow] }>()

const open = () => {
  if (!window.getSelection()?.toString().trim()) emit('open', props.row)
}
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

useBounceOnVisible()
</script>
