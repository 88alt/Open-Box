<template>
  <div
    class="flex flex-col gap-4"
    @keydown.enter="handleSubmit"
  >
    <div class="space-y-2">
      <h1 class="text-2xl font-semibold">{{ $t('setupPasswordTitle') }}</h1>
      <p class="text-base-content/70 text-sm">
        {{ $t('setupPasswordDescription') }}
      </p>
    </div>

    <div class="flex flex-col gap-2">
      <label class="text-sm">{{ $t('newPassword') }}</label>
      <PasswordInput
        v-model="newPassword"
        class="w-full"
        autocomplete="new-password"
        autofocus
      />
      <p class="text-base-content/60 text-xs">{{ $t('passwordMinLengthHint') }}</p>
    </div>

    <div class="flex flex-col gap-2">
      <label class="text-sm">{{ $t('confirmNewPassword') }}</label>
      <PasswordInput
        v-model="confirmPassword"
        class="w-full"
        autocomplete="new-password"
      />
    </div>


    <button
      class="btn btn-primary btn-sm w-full"
      :disabled="loading"
      @click="handleSubmit"
    >
      {{ $t('setupPasswordButton') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { showNotification } from '@/helper/notification'
import { PASSWORD_ALREADY_SET_CODE, PASSWORD_TOO_SHORT_CODE, setupPassword } from '@/store/auth'
import PasswordInput from '@/components/common/PasswordInput.vue'
import { ref } from 'vue'

// The standalone /setup route's form (SetupPasswordPage.vue, forced by the router guard
// while no password exists). This component only knows how to collect+submit a new
// password and emit `success` — the caller decides where to go next.
const emit = defineEmits<{
  success: []
}>()

const MIN_PASSWORD_LENGTH = 4


const newPassword = ref('')
const confirmPassword = ref('')
const loading = ref(false)

const handleSubmit = async () => {
  if (loading.value) return

  if (newPassword.value.length < MIN_PASSWORD_LENGTH) {
    showNotification({ content: 'passwordMinLengthHint', type: 'alert-error' })
    return
  }

  if (newPassword.value !== confirmPassword.value) {
    showNotification({ content: 'passwordsDoNotMatch', type: 'alert-error' })
    return
  }

  loading.value = true

  try {
    const result = await setupPassword(newPassword.value)

    if (!result.ok) {
      if (result.code === PASSWORD_TOO_SHORT_CODE) {
        showNotification({ content: 'passwordMinLengthHint', type: 'alert-error' })
      } else if (result.code === PASSWORD_ALREADY_SET_CODE) {
        // Another tab/session already finished setup — the store has resynced
        // itself; reload so the router picks up the now-current state.
        showNotification({ content: 'passwordAlreadySetError', type: 'alert-error' })
        window.setTimeout(() => window.location.reload(), 1500)
      } else {
        showNotification({ content: 'setupPasswordFailed', type: 'alert-error' })
      }
      return
    }

    emit('success')
  } catch {
    showNotification({ content: 'setupPasswordFailed', type: 'alert-error' })
  } finally {
    loading.value = false
  }
}
</script>
