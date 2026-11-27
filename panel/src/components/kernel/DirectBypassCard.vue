<template>
  <div class="card bg-base-100 border-base-300/60 border">
    <div class="card-body gap-3 p-4">
      <div>
        <div class="flex items-center gap-2">
          <h2 class="text-base font-semibold">{{ $t('directBypassTitle') }}</h2>
          <input
            type="checkbox"
            class="toggle toggle-sm shrink-0"
            :aria-label="$t('directBypassTitle')"
            :checked="profile.directBypass !== false"
            @change="onToggle"
          />
        </div>
        <p class="text-base-content/60 text-xs">{{ $t('directBypassDescription') }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { OpenboxProfile } from '@/api/openbox'
import { showNotification } from '@/helper/notification'

const props = defineProps<{
  profile: OpenboxProfile
  patchProfile: (patch: Record<string, unknown>) => Promise<OpenboxProfile>
}>()

const onToggle = async (event: Event) => {
  const on = (event.target as HTMLInputElement).checked
  try {
    await props.patchProfile({ directBypass: on })
    showNotification({ content: 'routingPolicySaved', type: 'alert-success' })
    // 上游 v0.1.493:打开直连旁路但没开 fake-ip(或 DNS 整个关掉)时,旁路拿不到站点名、
    // 只能按 IP 走,提示一句让人去 DNS 页把 fake-ip 打开
    const dns = props.profile.dns
    if (on && (dns?.fakeIpForProxy !== true || dns?.mode === 'off')) {
      showNotification({ content: 'directBypassNeedsFakeIp', key: 'direct-bypass-needs-fakeip', type: 'alert-warning', timeout: 6000 })
    }
  } catch (err) {
    showNotification({ content: 'routingSaveFailed', params: { message: err instanceof Error ? err.message : String(err) }, type: 'alert-error' })
  }
}
</script>
