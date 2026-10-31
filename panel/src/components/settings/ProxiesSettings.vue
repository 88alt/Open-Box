<template>
  <div class="settings-section flex flex-col gap-2 p-4 text-sm">
    <template v-if="hasVisibleLatencyItems">
      <div class="settings-title">
        {{ $t('latency') }}
      </div>
      <div class="settings-grid">
        <div
          v-if="isVisibleSpeedtestTimeout"
          class="setting-item"
        >
          <div class="setting-item-label">
            {{ $t('speedtestTimeout') }}
          </div>
          <input
            type="number"
            class="input input-sm w-20"
            v-model="speedtestTimeout"
          />
          ms
        </div>
        <div
          v-if="isVisibleLowLatency"
          class="setting-item"
        >
          <div class="setting-item-label">
            {{ $t('lowLatencyDesc') }}
          </div>
          <input
            type="number"
            class="input input-sm w-20"
            v-model="lowLatency"
          />
          ms
        </div>
        <div
          v-if="isVisibleMediumLatency"
          class="setting-item"
        >
          <div class="setting-item-label">
            {{ $t('mediumLatencyDesc') }}
          </div>
          <input
            type="number"
            class="input input-sm w-20"
            v-model="mediumLatency"
          />
          ms
        </div>
        <div
          v-if="isVisibleIpv6Test"
          class="setting-item"
        >
          <div class="setting-item-label">
            {{ $t('ipv6Test') }}
          </div>
          <input
            class="toggle"
            type="checkbox"
            v-model="IPv6test"
          />
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
    <!-- 布局:代理组分几列(GitHub #10) -->
    <template v-if="isVisibleProxyGroupColumnsPC || isVisibleProxyGroupColumnsMobile">
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
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { useIsSettingVisible } from '@/composables/settings'
import { PROXIES_ITEM_KEYS } from '@/config/settingsItems'
import {
  hideUnavailableProxies,
  IPv6test,
  lowLatency,
  mediumLatency,
  proxyGroupColumnsMobile,
  proxyGroupColumnsPC,
  speedtestTimeout,
} from '@/store/settings'
import { computed } from 'vue'

const k = PROXIES_ITEM_KEYS
const isVisibleSpeedtestTimeout = useIsSettingVisible(k.speedtestTimeout)
const isVisibleLowLatency = useIsSettingVisible(k.lowLatencyDesc)
const isVisibleMediumLatency = useIsSettingVisible(k.mediumLatencyDesc)
const isVisibleIpv6Test = useIsSettingVisible(k.ipv6Test)
const isVisibleProxyGroupColumnsPC = useIsSettingVisible(k.proxyGroupColumnsPC)
const isVisibleProxyGroupColumnsMobile = useIsSettingVisible(k.proxyGroupColumnsMobile)

const hasVisibleLatencyItems = computed(() => {
  return (
    isVisibleSpeedtestTimeout.value ||
    isVisibleLowLatency.value ||
    isVisibleMediumLatency.value ||
    isVisibleIpv6Test.value
  )
})

</script>
