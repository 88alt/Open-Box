<template>
  <!-- 卡片:硬件负载、网络状态(连接 / 活跃终端 / 上下行那几个数)。运行时长和启动 / 停止 / 重启不进卡片,
       直接摆在侧边栏这一层——以前是卡片套卡片,看着像两层壳。 -->
  <div class="app-card-gap flex flex-col text-sm">
    <!-- 硬件负载和网络状态两张卡片,显示哪张由设置里的「左侧栏显示」决定;内边距 12px,和上面的图表卡片一样 -->
    <SystemResourcesCard v-if="sidebarDisplay === 'hardware' || sidebarDisplay === 'both'" />
    <div
      v-if="sidebarDisplay === 'network' || sidebarDisplay === 'both'"
      class="card app-card-inset"
    >
      <StatisticsStats type="ctrl" />
    </div>
    <!-- 左:内核运行时长,贴着卡片左边;右:启动/停止/重启,贴着卡片右边——和卡片的外边对齐,不是内容边 -->
    <div class="flex items-center justify-between gap-2">
      <div class="flex min-w-0 flex-col leading-tight">
        <span class="text-base-content/60 text-xs">{{ $t('kernelUptimeLabel') }}</span>
        <span class="truncate text-sm font-medium tabular-nums">{{ uptimeText }}</span>
      </div>
      <KernelActionButtons class="shrink-0" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { kernelUptimeText } from '@/composables/kernelService'
import { sidebarDisplay } from '@/store/settings'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import StatisticsStats from '../overview/StatisticsStats.vue'
import KernelActionButtons from './KernelActionButtons.vue'
import SystemResourcesCard from './SystemResourcesCard.vue'

const { t } = useI18n()
const uptimeText = computed(() => kernelUptimeText(t))
</script>
