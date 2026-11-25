<template>
  <!-- 内核小图标:用在侧栏底部资源卡上方的「内核」那一行(对齐上游的 KernelIcon)。
       尺寸与上游一致:16px 见方、object-contain、空 alt(纯装饰,名字在旁边的 span 里)。
       图形来源:
       - sing-box:SagerNet/sing-box 组织的头像(即 sing-box 的 logo);
       - Mihomo:与上游 v0.1.487 一致的 "M" 标志(96px,取自上游打包的 mihomo 图标)。
       两张都是 96px 入库,避免面板里多背大图。 -->
  <img
    :src="kernel === 'mihomo' ? mihomoLogo : singboxLogo"
    alt=""
    class="size-4 shrink-0 object-contain"
  >
</template>

<script setup lang="ts">
import mihomoLogo from '@/assets/mihomo.png'
import singboxLogo from '@/assets/sing-box.png'

defineProps<{ kernel?: string }>()
</script>
