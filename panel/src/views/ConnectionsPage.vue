<template>
  <!-- 「连接」页:顶栏左边是 路由 / 连接 / 日志 三个页签(原来的「规则」「日志」两个侧边栏入口并进来了),
       右边是当前页签自己的搜索和按钮;下面是当前页签的内容。和上游 v0.1.376 一致。 -->
  <div
    class="connection-workspace relative flex h-full min-h-0 flex-col overflow-hidden"
    :style="{
      '--connection-controls-min-width': CONTROLS_MIN_WIDTH[activeTab],
      '--connection-mobile-tools-width':
        activeTab === CONNECTION_WORKSPACE_TAB.routing ? '0px' : '11rem',
    }"
  >
    <SettingsMenu
      :menu-items="tabItems"
      :active-menu-key="activeTab"
      :track-controls="false"
      @menu-click="handleTabClick"
    >
      <!-- 窄屏:页签行右侧放当前页签的小工具(连接页是「活跃 / 已关闭」,日志页是级别和三个按钮),
           其余的搜索 / 按钮在下面单独一行;宽屏这个插槽被样式隐藏。和上游 v0.1.413 一致 -->
      <template #mobile-actions>
        <div id="connection-workspace-mobile-actions">
          <ConnectionTabs
            v-if="isMiddleScreen && activeTab === CONNECTION_WORKSPACE_TAB.connections"
          />
          <div
            v-if="isMiddleScreen && activeTab === CONNECTION_WORKSPACE_TAB.logs"
            class="workspace-log-header-tools flex min-w-0 flex-1 items-center justify-end gap-2"
          >
            <LogLevelSelect />
            <LogActionButtons />
          </div>
        </div>
      </template>
      <div class="connection-workspace-controls">
        <component :is="CONTROLS[activeTab]" />
      </div>
    </SettingsMenu>
    <div class="min-h-0 flex-1 overflow-hidden">
      <component :is="CONTENTS[activeTab]" />
    </div>
  </div>
</template>

<script setup lang="ts">
import ConnectionsContent from '@/components/connections/ConnectionsContent.vue'
import LogsContent from '@/components/logs/LogsContent.vue'
import RulesContent from '@/components/rules/RulesContent.vue'
import SettingsMenu from '@/components/settings/SettingsMenu.vue'
import ConnectionCtrl from '@/components/sidebar/ConnectionCtrl.tsx'
import ConnectionTabs from '@/components/sidebar/ConnectionTabs.vue'
import LogsCtrl, { LogActionButtons, LogLevelSelect } from '@/components/sidebar/LogsCtrl.tsx'
import RulesCtrl from '@/components/sidebar/RulesCtrl.tsx'
import { CONNECTION_WORKSPACE_TAB, ROUTE_NAME } from '@/constant'
import { isMiddleScreen } from '@/helper/utils'
import { ArrowsRightLeftIcon, DocumentTextIcon, MapIcon } from '@heroicons/vue/24/outline'
import { useStorage } from '@vueuse/core'
import type { Component } from 'vue'
import { computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

const route = useRoute()
const router = useRouter()

const tabItems: { key: CONNECTION_WORKSPACE_TAB; label: string; icon: Component }[] = [
  { key: CONNECTION_WORKSPACE_TAB.routing, label: 'connectionRoutingTab', icon: MapIcon },
  { key: CONNECTION_WORKSPACE_TAB.connections, label: 'connections', icon: ArrowsRightLeftIcon },
  { key: CONNECTION_WORKSPACE_TAB.logs, label: 'logs', icon: DocumentTextIcon },
]
// 每个页签的控制栏(搜索、按钮)和内容。控制栏放进顶栏右侧,内容占下面剩余的高度
const CONTROLS: Record<CONNECTION_WORKSPACE_TAB, Component> = {
  [CONNECTION_WORKSPACE_TAB.routing]: RulesCtrl,
  [CONNECTION_WORKSPACE_TAB.connections]: ConnectionCtrl,
  [CONNECTION_WORKSPACE_TAB.logs]: LogsCtrl,
}
const CONTENTS: Record<CONNECTION_WORKSPACE_TAB, Component> = {
  [CONNECTION_WORKSPACE_TAB.routing]: RulesContent,
  [CONNECTION_WORKSPACE_TAB.connections]: ConnectionsContent,
  [CONNECTION_WORKSPACE_TAB.logs]: LogsContent,
}
// 控制栏放不下时的最小宽度:低于它就换到页签下面一行
const CONTROLS_MIN_WIDTH: Record<CONNECTION_WORKSPACE_TAB, string> = {
  [CONNECTION_WORKSPACE_TAB.routing]: '30rem',
  [CONNECTION_WORKSPACE_TAB.connections]: '56rem',
  [CONNECTION_WORKSPACE_TAB.logs]: '46rem',
}

const isTab = (v: unknown): v is CONNECTION_WORKSPACE_TAB => tabItems.some((item) => item.key === v)

// 上次停留的页签记在本机;地址里带 ?tab= 时(老的 #/logs、#/rules 链接会被路由改写成这样)以地址为准
const storedTab = useStorage<CONNECTION_WORKSPACE_TAB>(
  'cache/connection-workspace-tab',
  CONNECTION_WORKSPACE_TAB.routing,
)
if (!isTab(storedTab.value)) {
  storedTab.value = CONNECTION_WORKSPACE_TAB.routing
}

const activeTab = computed(() => (isTab(route.query.tab) ? route.query.tab : storedTab.value))

watch(
  () => route.query.tab,
  (tab) => {
    if (route.name === ROUTE_NAME.connections && isTab(tab)) {
      storedTab.value = tab
    }
  },
  { immediate: true },
)

const handleTabClick = (key: string) => {
  if (!isTab(key) || key === activeTab.value) return

  router.push({ name: ROUTE_NAME.connections, query: { ...route.query, tab: key } })
}
</script>

<style scoped>
/* 页签靠左不拉伸,右边那块(#settings-header-actions)让给控制栏,放不下时控制栏自己横向滚动 */
.connection-workspace :deep(.settings-menu-list) {
  flex: 0 0 auto;
}
.connection-workspace :deep(#settings-header-actions) {
  flex: 1 1 0%;
  min-width: 0;
  overflow-x: auto;
  scrollbar-width: none;
}
.connection-workspace-controls {
  width: 100%;
  min-width: var(--connection-controls-min-width);
}
/* 控制栏原本是贴顶的整条栏(有自己的背景和内边距);嵌进页签栏以后跟着页签栏走 */
.connection-workspace :deep(.connection-workspace-controls > .ctrls-bar) {
  position: static;
  background: none;
  backdrop-filter: none;
  -webkit-backdrop-filter: none;
}
#app-content
  .connection-workspace
  :deep(.connection-workspace-controls > .ctrls-bar > .app-card-padding) {
  padding: 0 !important;
}

/* 窄屏插槽在宽屏下不显示 */
.connection-workspace :deep(.settings-menu-mobile-actions) {
  display: none;
}

/* 窄屏:页签只留文字,右边留一块放小工具;控制栏换到页签下面单独一行 */
@media screen and (max-width: 768px) {
  .connection-workspace :deep(.settings-menu > div) {
    flex-wrap: wrap;
  }
  .connection-workspace :deep(.settings-menu-list) {
    flex: 0 1 auto;
    max-width: calc(100% - var(--connection-mobile-tools-width) - var(--app-card-space, 12px));
  }
  .connection-workspace :deep(.settings-menu-list .settings-menu-btn) {
    padding-inline: 0.5rem;
  }
  .connection-workspace :deep(.settings-menu-list .settings-menu-btn > svg) {
    display: none;
  }
  .connection-workspace :deep(.settings-menu-list .settings-menu-btn > span) {
    display: block;
    white-space: nowrap;
  }
  .connection-workspace :deep(.settings-menu-mobile-actions) {
    display: flex;
    flex: 1 0 var(--connection-mobile-tools-width);
    min-width: 0;
    justify-content: flex-end;
  }
  #connection-workspace-mobile-actions {
    display: flex;
    min-width: 0;
    flex: 1;
    justify-content: flex-end;
  }
  .connection-workspace :deep(.workspace-log-header-tools .select) {
    padding-left: 0.5rem;
    padding-right: 1.25rem;
  }
  .connection-workspace :deep(#settings-header-actions) {
    flex: 0 0 100%;
    margin-left: 0;
    overflow: visible;
  }
  .connection-workspace-controls {
    min-width: 0;
  }
}
</style>
