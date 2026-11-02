<template>
  <DialogWrapper
    v-model="open"
    :title="`${$t('bandwidthHelpTitle')} · ${$t(`groupType_${mode}`)}`"
    box-class="w-full max-w-2xl"
    header-class="pr-10!"
  >
    <template #footer>
      <button
        type="button"
        class="btn btn-primary btn-sm"
        @click="open = false"
      >
        {{ $t('close') }}
      </button>
    </template>
    <div class="flex flex-col gap-4 text-sm leading-relaxed">
      <p class="font-medium">{{ $t('bandwidthHealthFirst') }}</p>
      <div class="flex flex-col gap-2">
        <p class="font-semibold">{{ $t('bandwidthOff') }}</p>
        <p>{{ $t(`bandwidthOff_${mode}`) }}</p>
      </div>
      <div class="flex flex-col gap-2">
        <p class="font-semibold">{{ $t('bandwidthOn') }}</p>
        <p>{{ $t(`bandwidthOn_${mode}`) }}</p>
      </div>
      <div class="flex flex-col gap-2">
        <p class="font-semibold">{{ $t('bandwidthExample') }}</p>
        <p class="whitespace-pre-line">{{ $t(`bandwidthExample_${mode}`) }}</p>
      </div>
      <p class="text-base-content/70">{{ $t('bandwidthSharedHelp', { hours, maxMB }) }}</p>
      <p class="text-base-content/70">{{ $t('bandwidthClientDisabled') }}</p>
    </div>
  </DialogWrapper>
</template>

<script setup lang="ts">
import DialogWrapper from '@/components/common/DialogWrapper.vue'

// mode 是组类型(urltest / failover / loadbalance),决定显示哪一组「关闭时 / 开启时 / 例子」文案;
// 这三种类型都有对应的 bandwidthOff_* / bandwidthOn_* / bandwidthExample_* 键(动态拼出来的,代码里搜不到字面量)
defineProps<{
  mode: string
  hours: number
  maxMB: number
}>()

const open = defineModel<boolean>({ required: true })
</script>
