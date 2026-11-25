<template>
  <div class="flex shrink-0 items-center select-text">
    <ProxyIcon
      v-if="icon"
      :icon="icon"
      :margin="iconMargin"
      :size="iconSize"
      :scale="node?.iconScale"
    />
    {{ displayName }}
    <template v-if="dialerProxy"> ({{ dialerProxy }}) </template>
    <!-- 延迟药丸(对齐上游:连接页「策略 / 出站」列、详情弹窗、日志链路都会带上)。
         只在 hideBandwidth 时出现——那几处只要延迟、不要带宽段;其余地方(代理页等)保持原样。
         LatencyTag 自己按 name / groupName 去查延迟,这里不用再传值。 -->
    <LatencyTag
      v-if="hideBandwidth"
      class="ml-1"
      :name="name"
      :group-name="groupName"
    />
  </div>
</template>

<script setup lang="ts">
import { outboundDisplayName, proxyMap } from '@/store/proxies'
import { computed } from 'vue'
import LatencyTag from './LatencyTag.vue'
import ProxyIcon from './ProxyIcon.vue'

const props = withDefaults(
  defineProps<{
    name: string
    iconSize?: number
    iconMargin?: number
    // 为真时在名字后面带一枚只显示延迟的药丸(对齐上游 connection 列的写法)
    hideBandwidth?: boolean
    // 查延迟用的组名(链路里的出站可能挂在某个组下;不传就按出站名查)
    groupName?: string
  }>(),
  {
    iconSize: 16,
    iconMargin: 4,
  },
)

const node = computed(() => proxyMap.value[props.name])
// 故障转移的内部子组 tag(__fo:组id:页签id)不是产品名称:显示成页签名,没起名就显示「主用 / 备用 N」
const displayName = computed(() => outboundDisplayName(props.name))
const icon = computed(() => {
  return node.value?.icon
})
const dialerProxy = computed(() => {
  return node.value?.['dialer-proxy']
})
</script>
