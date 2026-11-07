<template>
  <ChangePasswordDialog v-model="isChangePasswordDialogOpen" />
  <!-- 「通用」这一块只有一个网格:语言/圆角/背景/主题在 ZashboardSettings 里,
       下面这几行通过插槽排进同一个网格,行距才一致 -->
  <ZashboardSettings>
    <div
      v-if="isVisibleChangePassword"
      class="setting-item"
    >
      <div class="setting-item-label">
        {{ $t('changePassword') }}
      </div>
      <button
        type="button"
        class="btn btn-sm"
        @click="isChangePasswordDialogOpen = true"
      >
        {{ $t('changePassword') }}
      </button>
    </div>
    <div
      v-if="isVisibleIPInfoAPI"
      class="setting-item"
    >
      <div class="setting-item-label">
        {{ $t('IPInfoAPI') }}
        <QuestionMarkCircleIcon
          class="h-4 w-4 cursor-pointer"
          @mouseenter="showTip($event, $t('IPInfoAPITip'))"
        />
      </div>
      <select
        class="select select-sm min-w-24"
        v-model="IPInfoAPI"
      >
        <option
          v-for="opt in Object.values(IP_INFO_API)"
          :key="opt"
          :value="opt"
        >
          {{ opt }}
        </option>
      </select>
    </div>

    <div
      v-if="isVisibleScrollAnimationEffect"
      class="setting-item md:hidden!"
    >
      <div class="setting-item-label">
        {{ $t('scrollAnimationEffect') }}
      </div>
      <input
        type="checkbox"
        v-model="scrollAnimationEffect"
        class="toggle"
      />
    </div>
    <div
      v-if="isVisibleSwipeInPages"
      class="setting-item md:hidden!"
    >
      <div class="setting-item-label">
        {{ $t('swipeInPages') }}
      </div>
      <input
        type="checkbox"
        v-model="swipeInPages"
        class="toggle"
      />
    </div>
    <div
      v-if="swipeInPages && isVisibleSwipeInTabs"
      class="setting-item md:hidden!"
    >
      <div class="setting-item-label">
        {{ $t('swipeInTabs') }}
      </div>
      <input
        type="checkbox"
        v-model="swipeInTabs"
        class="toggle"
      />
    </div>
    <div
      v-if="isVisibleDisablePullToRefresh"
      class="setting-item md:hidden!"
    >
      <div class="setting-item-label">
        {{ $t('disablePullToRefresh') }}
        <QuestionMarkCircleIcon
          class="h-4 w-4 cursor-pointer"
          @mouseenter="showTip($event, $t('disablePullToRefreshTip'))"
        />
      </div>
      <input
        type="checkbox"
        v-model="disablePullToRefresh"
        class="toggle"
      />
    </div>
    <!-- 布局:代理组分几列(GitHub #10)、左侧栏显示、隐藏不可用节点。v0.1.413 把它们放在「通用」下面、同一张卡片里 -->
    <template #layout>
      <div class="settings-title">
        {{ $t('proxyGroupLayout') }}
      </div>
      <div class="settings-grid">
        <div
          v-if="isVisibleProxyGroupColumnsPC"
          class="setting-item"
        >
          <div class="setting-item-label">
            {{ $t('proxyGroupColumnsPC') }}
          </div>
          <select
            class="select select-sm w-28"
            :aria-label="$t('proxyGroupColumnsPC')"
            v-model.number="proxyGroupColumnsPC"
          >
            <option
              v-for="n in [1, 2, 3]"
              :key="n"
              :value="n"
            >
              {{ $t(`proxyGroupColumns${n}`) }}
            </option>
          </select>
        </div>
        <div
          v-if="isVisibleProxyGroupColumnsMobile"
          class="setting-item"
        >
          <div class="setting-item-label">
            {{ $t('proxyGroupColumnsMobile') }}
          </div>
          <select
            class="select select-sm w-28"
            :aria-label="$t('proxyGroupColumnsMobile')"
            v-model.number="proxyGroupColumnsMobile"
          >
            <option
              v-for="n in [1, 2]"
              :key="n"
              :value="n"
            >
              {{ $t(`proxyGroupColumns${n}`) }}
            </option>
          </select>
        </div>
        <div
          v-if="isVisibleSidebarDisplay"
          class="setting-item"
        >
          <div class="setting-item-label">
            {{ $t('sidebarDisplay') }}
          </div>
          <select
            class="select select-sm w-48 min-w-0"
            v-model="sidebarDisplay"
            :aria-label="$t('sidebarDisplay')"
          >
            <option value="off">{{ $t('sidebarDisplayOff') }}</option>
            <option value="hardware">{{ $t('hardwareLoad') }}</option>
            <option value="network">{{ $t('overviewCardTitle') }}</option>
            <option value="both">{{ $t('hardwareLoad') }} + {{ $t('overviewCardTitle') }}</option>
          </select>
        </div>
        <div class="setting-item">
          <div class="setting-item-label">
            {{ $t('hideUnavailableProxies') }}
          </div>
          <input
            class="toggle"
            type="checkbox"
            v-model="hideUnavailableProxies"
          />
        </div>
      </div>
    </template>
  </ZashboardSettings>
</template>

<script setup lang="ts">
import { useIsSettingVisible } from '@/composables/settings'
import { GENERAL_ITEM_KEYS, PROXIES_ITEM_KEYS } from '@/config/settingsItems'
import { IP_INFO_API } from '@/constant'
import { useTooltip } from '@/helper/tooltip'
import {
  disablePullToRefresh,
  hideUnavailableProxies,
  IPInfoAPI,
  proxyGroupColumnsMobile,
  proxyGroupColumnsPC,
  scrollAnimationEffect,
  sidebarDisplay,
  swipeInPages,
  swipeInTabs,
} from '@/store/settings'
import { QuestionMarkCircleIcon } from '@heroicons/vue/24/outline'
import { ref } from 'vue'
import ChangePasswordDialog from './ChangePasswordDialog.vue'
import ZashboardSettings from './ZashboardSettings.vue'

const { showTip } = useTooltip()

const k = GENERAL_ITEM_KEYS
const isVisibleChangePassword = useIsSettingVisible(k.changePassword)
const isVisibleIPInfoAPI = useIsSettingVisible(k.IPInfoAPI)
const isVisibleScrollAnimationEffect = useIsSettingVisible(k.scrollAnimationEffect)
const isVisibleSwipeInPages = useIsSettingVisible(k.swipeInPages)
const isVisibleSwipeInTabs = useIsSettingVisible(k.swipeInTabs)
const isVisibleDisablePullToRefresh = useIsSettingVisible(k.disablePullToRefresh)
const isChangePasswordDialogOpen = ref(false)

// 布局小节:代理组列数 / 左侧栏显示有可见性开关,「隐藏不可用节点」一直显示
const p = PROXIES_ITEM_KEYS
const isVisibleProxyGroupColumnsPC = useIsSettingVisible(p.proxyGroupColumnsPC)
const isVisibleProxyGroupColumnsMobile = useIsSettingVisible(p.proxyGroupColumnsMobile)
const isVisibleSidebarDisplay = useIsSettingVisible(p.sidebarDisplay)
</script>
