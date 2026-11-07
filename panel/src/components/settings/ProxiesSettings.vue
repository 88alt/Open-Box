<template>
  <div class="settings-section flex flex-col app-card-gap app-card-inset text-sm">
    <template v-if="hasVisibleLatencyItems">
      <div class="settings-title">
        {{ $t('latencyThresholds') }}
      </div>
      <div class="settings-grid">
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
      </div>
    </template>
    <!-- 带宽着色阈值:不足(红)/一般(橙)可改,高于两者为绿 -->
    <div class="settings-title">
      {{ $t('bandwidthThresholds') }}
    </div>
    <div class="settings-grid">
      <div class="setting-item">
        <div class="setting-item-label">
          {{ $t('bandwidthPoorDesc') }}
        </div>
        <input
          type="number"
          class="input input-sm w-20"
          min="0"
          :max="bandwidthAdequateThreshold"
          step="0.1"
          v-model.number="bandwidthPoorThreshold"
          :aria-label="$t('bandwidthPoorDesc')"
        />
        Mbps
      </div>
      <div class="setting-item">
        <div class="setting-item-label">
          {{ $t('bandwidthAdequateDesc') }}
        </div>
        <input
          type="number"
          class="input input-sm w-20"
          :min="bandwidthPoorThreshold"
          step="0.1"
          v-model.number="bandwidthAdequateThreshold"
          :aria-label="$t('bandwidthAdequateDesc')"
        />
        Mbps
      </div>
    </div>
    <!-- CPU 负载着色阈值(硬件负载的曲线和数字按它变色):低于第一个为绿,低于第二个为橙,更高为红 -->
    <template v-if="isVisibleCpuLoadLow || isVisibleCpuLoadMedium">
      <div class="settings-title">
        {{ $t('cpuLoadThresholds') }}
      </div>
      <div class="settings-grid">
        <div
          v-if="isVisibleCpuLoadLow"
          class="setting-item"
        >
          <div class="setting-item-label">
            {{ $t('cpuLoadLowDesc') }}
          </div>
          <input
            type="number"
            class="input input-sm w-20"
            min="0"
            :max="cpuLoadThresholds[1] - 1"
            :value="cpuLoadThresholds[0]"
            :aria-label="$t('cpuLoadLowDesc')"
            @change="onCpuThresholdChange(0, $event)"
          />
          %
        </div>
        <div
          v-if="isVisibleCpuLoadMedium"
          class="setting-item"
        >
          <div class="setting-item-label">
            {{ $t('cpuLoadMediumDesc') }}
          </div>
          <input
            type="number"
            class="input input-sm w-20"
            :min="cpuLoadThresholds[0] + 1"
            max="100"
            :value="cpuLoadThresholds[1]"
            :aria-label="$t('cpuLoadMediumDesc')"
            @change="onCpuThresholdChange(1, $event)"
          />
          %
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { useIsSettingVisible } from '@/composables/settings'
import { PROXIES_ITEM_KEYS } from '@/config/settingsItems'
import {
  bandwidthAdequateThreshold,
  cpuLoadThresholds,
  bandwidthPoorThreshold,
  IPv6test,
  lowLatency,
  mediumLatency,
  setCpuLoadThreshold,
  speedtestTimeout,
} from '@/store/settings'
import { computed } from 'vue'

const k = PROXIES_ITEM_KEYS
const isVisibleSpeedtestTimeout = useIsSettingVisible(k.speedtestTimeout)
const isVisibleLowLatency = useIsSettingVisible(k.lowLatencyDesc)
const isVisibleMediumLatency = useIsSettingVisible(k.mediumLatencyDesc)
const isVisibleIpv6Test = useIsSettingVisible(k.ipv6Test)
const isVisibleCpuLoadLow = useIsSettingVisible(k.cpuLoadLowDesc)
const isVisibleCpuLoadMedium = useIsSettingVisible(k.cpuLoadMediumDesc)

// 输入框改完后按合法范围写回(0 ≤ 低 < 高 ≤ 100),再把框里的值同步成实际存下的值
const onCpuThresholdChange = (index: 0 | 1, event: Event) => {
  const input = event.target as HTMLInputElement
  setCpuLoadThreshold(index, input.valueAsNumber)
  input.value = String(cpuLoadThresholds.value[index])
}

const hasVisibleLatencyItems = computed(() => {
  return (
    isVisibleSpeedtestTimeout.value ||
    isVisibleLowLatency.value ||
    isVisibleMediumLatency.value ||
    isVisibleIpv6Test.value
  )
})

</script>
