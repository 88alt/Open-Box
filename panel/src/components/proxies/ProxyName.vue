<template>
  <div class="flex shrink-0 items-center select-text">
    <ProxyIcon
      v-if="icon"
      :icon="icon"
      :margin="iconMargin"
      :size="iconSize"
      :scale="node?.iconScale"
    />
    <!-- 名字用 span 包住(对齐上游 v0.1.490 的 ProxyName):上游是<span class="min-w-0 truncate">,裸文本在长名字时不会截断,DOM 比对里这一层也一直被算成容器差异 -->
    <span class="min-w-0 truncate">{{ displayName }}</span>
    <template v-if="dialerProxy"> ({{ dialerProxy }}) </template>
    <!-- 延迟药丸(对齐上游:连接页「策略 / 出站」列、详情弹窗、日志链路都会带上)。
         hideBandwidth / showLatency 两个开关等价,任一个为真就带——前者是 main 原来的写法,
         后者对齐上游 v0.1.490 的 ProxyName prop 名(路由比对出口阶段会传 show-latency)。
         这两处只要延迟、不要带宽段;其余地方(代理页等)保持原样。
         LatencyTag 自己按 name / groupName 去查延迟,这里不用再传值。 -->
    <LatencyTag
      v-if="(hideBandwidth || showLatency) && showPill"
      class="ml-1"
      :name="name"
      :group-name="groupName"
      :bandwidth-only="bandwidthOnly"
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
    // 同 hideBandwidth,是上游 v0.1.490 的 prop 名(路由比对出口阶段传 show-latency)
    showLatency?: boolean
    // 对齐上游:按带宽模式渲染延迟药丸(不等 measurements 里有条目)
    bandwidthOnly?: boolean
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
// 内置出站(DIRECT / REJECT 等)不挂延迟药丸:上游只给真节点挂。
// 真机实测 main 曾给 REJECT 画 83 枚、DIRECT 画 28 枚,而上游 81 枚全是某个真实节点名。
const NON_NODE_TYPES = new Set(['direct', 'reject', 'block', 'dns', 'pass', 'compatible', 'rejectdrop', 'selector', 'urltest', 'fallback', 'loadbalance', 'smart'])
const showPill = computed(() => {
  // 只排除「明确是内置出站 / 组」的:proxyMap 里查不到这个名字时**仍然显示**,
  // 因为规则页的节点名来自内核代理表,proxyMap 未必加载到那一条
  // (踩过:按 infoNode 那样要求必须查到,结果 main 一枚药丸都不画)。
  const node = proxyMap.value[props.name]
  if (!node) return true
  if (node.all?.length) return false
  return !NON_NODE_TYPES.has(String(node.type || '').toLowerCase())
})
const icon = computed(() => {
  return node.value?.icon
})
const dialerProxy = computed(() => {
  return node.value?.['dialer-proxy']
})
</script>
