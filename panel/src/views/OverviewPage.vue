<template>
  <div class="flex h-full min-h-0 flex-col overflow-hidden">
    <OverviewCtrl />
    <div
      class="min-h-0 flex-1 overflow-x-hidden overflow-y-auto"
      :style="padding"
    >
      <!-- 概览几张卡片:面板概览(站点延迟 + 汇总 + 四张实时图)、流量洞察和节点质量。网络信息、连接拓扑、连接统计已去掉。 -->
      <div class="flex flex-col gap-2 px-2 md:py-2">
        <PanelOverviewCard />
        <DnsFilterOverview />
        <!-- 手机屏太窄放不下 31 根柱子和明细表,每日流量只在桌面显示 -->
        <DailyTrafficCard v-if="!isMiddleScreen" />
        <NodeQualityCard />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import DnsFilterOverview from '@/components/overview/DnsFilterOverview.vue'
import DailyTrafficCard from '@/components/overview/DailyTrafficCard.vue'
import NodeQualityCard from '@/components/overview/NodeQualityCard.vue'
import PanelOverviewCard from '@/components/overview/PanelOverviewCard.vue'
import OverviewCtrl from '@/components/sidebar/OverviewCtrl.vue'
import { usePaddingForViews } from '@/composables/paddingViews'
import { isMiddleScreen } from '@/helper/utils'

const { padding } = usePaddingForViews({
  offsetTop: 0,
  offsetBottom: 0,
})
</script>
