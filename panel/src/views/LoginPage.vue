<template>
  <div
    class="bg-base-200/50 flex h-full w-full items-center justify-center overflow-auto px-4"
    @keydown.enter="handleLogin"
  >
    <div class="card bg-base-100 w-full max-w-md app-card-gap app-card-inset shadow-xl">
      <div class="space-y-2">
        <h1 class="text-2xl font-semibold">{{ $t('passwordLoginTitle') }}</h1>
        <p class="text-base-content/70 text-sm">
          {{ $t('passwordLoginDescription') }}
        </p>
      </div>

      <div class="flex flex-col gap-2">
        <label class="text-sm">{{ $t('accessPassword') }}</label>
        <PasswordInput
          v-model="password"
          class="w-full"
          autofocus
        />
      </div>


      <button
        class="btn btn-primary btn-sm w-full"
        @click="handleLogin"
      >
        {{ $t('login') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import PasswordInput from '@/components/common/PasswordInput.vue'
import { showNotification } from '@/helper/notification'
import { ROUTE_NAME } from '@/constant'
import router from '@/router'
import { loginWithAccessPassword } from '@/store/auth'
import { ref } from 'vue'

const password = ref('')
const loading = ref(false)

const handleLogin = async () => {
  if (loading.value) return

  loading.value = true

  try {
    const result = await loginWithAccessPassword(password.value)

    if (!result.ok) {
      showNotification({ content: 'wrongAccessPassword', type: 'alert-error' })
      return
    }


    const redirect = router.currentRoute.value.query.redirect
    const target =
      typeof redirect === 'string' && redirect
        ? redirect
        : router.resolve({
            name: ROUTE_NAME.proxies,
          }).fullPath

    window.location.hash = target
    window.location.reload()
  } finally {
    loading.value = false
  }
}
</script>
