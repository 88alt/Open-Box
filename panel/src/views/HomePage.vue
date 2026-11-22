<template>
  <div
    class="bg-base-200/50 home-page flex size-full"
    :class="isSidebarCollapsed ? 'sidebar-collapsed' : 'sidebar-expanded'"
  >
    <SideBar v-if="!isMiddleScreen" />
    <RouterView v-slot="{ Component, route }">
      <div
        class="relative flex-1 overflow-hidden"
        ref="swiperRef"
      >
        <!-- 手机端不做页面切换过场:直接换内容。之前的左右滑入动画在低端机上会掉帧,
             而且和顶部工具栏 / 底部导航的固定位置打架。 -->
        <div :class="['absolute flex h-full w-full flex-col overflow-y-auto', SCROLLABLE_PARENT_CLASS]">
          <Component :is="Component" />
        </div>

        <template v-if="isMiddleScreen">
          <nav
            v-segmented="{ active: '.dock-active', variant: 'dock' }"
            class="dock mobile-bottom-nav"
            ref="dockRef"
          >
            <button
              v-for="r in renderRoutes"
              :key="r"
              type="button"
              @click="router.push({ name: r })"
              :class="r === route.name && 'dock-active'"
              :aria-current="r === route.name ? 'page' : undefined"
            >
              <component
                :is="ROUTE_ICON_MAP[r]"
                class="mobile-bottom-nav-icon"
              />
              <span class="dock-label">
                {{ $t(r) }}
              </span>
            </button>
          </nav>
        </template>
      </div>
    </RouterView>
  </div>
</template>

<script setup lang="ts">
import { fetchBackendVersion } from '@/api'
import SideBar from '@/components/sidebar/SideBar.vue'
import { useKernelConflictToast, useRestartPendingToast } from '@/composables/kernelService'
import { dockTop } from '@/composables/paddingViews'
import { useSwipeRouter } from '@/composables/swipe'
import { ROUTE_ICON_MAP } from '@/constant'
import { renderRoutes } from '@/helper'
import { isMiddleScreen, SCROLLABLE_PARENT_CLASS } from '@/helper/utils'
import { fetchConfigs } from '@/store/config'
import { initConnections } from '@/store/connections'
import { initLogs } from '@/store/logs'
import { initSatistic } from '@/store/overview'
import { fetchProxies } from '@/store/proxies'
import { fetchRules } from '@/store/rules'
import { isSidebarCollapsed } from '@/store/settings'
import { useDocumentVisibility, useElementBounding } from '@vueuse/core'
import { ref, watch } from 'vue'
import { RouterView, useRouter } from 'vue-router'

useRestartPendingToast()
useKernelConflictToast()

const router = useRouter()
const { swiperRef } = useSwipeRouter()

const dockRef = ref<HTMLElement>()
const { top: dockRefTop } = useElementBounding(dockRef)

watch(
  dockRefTop,
  () => {
    dockTop.value = window.innerHeight - dockRefTop.value
  },
  { immediate: true },
)

fetchBackendVersion()
fetchConfigs()
fetchProxies()
fetchRules()
initConnections()
initLogs()
initSatistic()

const documentVisible = useDocumentVisibility()

watch(documentVisible, () => {
  if (documentVisible.value !== 'visible') return
  fetchProxies()
})


</script>
