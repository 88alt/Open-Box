<template>
  <div class="setting-item">
    <div class="setting-item-label">
      {{ $t('language') }}
    </div>
    <select
      class="select select-sm w-48"
      :value="locale"
      :disabled="loading"
      :aria-busy="loading || undefined"
      @change="handleChange"
    >
      <option
        v-for="opt in Object.values(LANG)"
        :key="opt"
        :value="opt"
      >
        {{ langLabelMap[opt] || opt }}
      </option>
    </select>
  </div>
</template>

<script setup lang="ts">
import { LANG } from '@/constant'
import { showNotification } from '@/helper/notification'
import { loadLanguage } from '@/i18n'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

const { locale } = useI18n()
const loading = ref(false)
const langLabelMap = {
  [LANG.EN_US]: 'English',
  [LANG.ZH_CN]: '简体中文',
  [LANG.ZH_TW]: '繁體中文',
}

const handleChange = async (e: Event) => {
  const select = e.currentTarget as HTMLSelectElement
  const next = select.value as LANG

  if (loading.value) return
  loading.value = true
  try {
    await loadLanguage(next)
  } catch {
    select.value = locale.value
    showNotification({ content: 'languageLoadFailed', type: 'alert-error' })
  } finally {
    loading.value = false
  }
}
</script>
