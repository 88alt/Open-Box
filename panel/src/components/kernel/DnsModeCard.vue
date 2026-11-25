<template>
  <!-- DNS 劫持方式 + FakeIP。改动写进档案,保存后直接生效(接管 / 还原 dnsmasq 由后台自动处理)。 -->
  <div class="card bg-base-100 border-base-300/60 border">
    <div class="card-body gap-3 p-4 text-sm">
      <div class="flex items-center justify-between gap-2">
        <div class="flex flex-col gap-1">
          <h2 class="text-base font-semibold">{{ $t('dnsModeTitle') }}（{{ $t('dnsModePort', { port: listenPortText }) }}）</h2>
        </div>
        <select
          class="select select-sm w-40 shrink-0"
          :value="mode"
          :disabled="saving"
          @change="onChange"
        >
          <option
            v-if="dnsmasqAvailable"
            value="dnsmasq"
          >
            {{ $t('dnsModeDnsmasq') }}
          </option>
          <option value="hijack">{{ $t('dnsModeHijack') }}</option>
          <option value="off">{{ $t('dnsModeOff') }}</option>
        </select>
      </div>
      <p class="text-base-content/50 text-xs">{{ $t(NOTE_KEY[mode]) }}</p>
      <!-- 内核 DNS 入站三种模式都开:局域网里的 AdGuard Home / Pi-hole 把上游指到这里就能用分流解析 -->
      <p class="text-base-content/50 text-xs">{{ $t('dnsModeUpstreamHint', { addr: kernelDnsAddr }) }}</p>
      <!-- 监听 53 端口:只有 systemd 部署能选(容器一直监听,openwrt 的 53 归 dnsmasq);开了以后旁路由终端可直接把本机 IP 设为 DNS -->
      <div
        v-if="platform === 'systemd'"
        class="border-base-300/60 flex items-start gap-3 border-t pt-3"
      >
        <input
          type="checkbox"
          class="toggle toggle-sm mt-0.5 cursor-pointer"
          :aria-label="$t('dnsListen53Title')"
          :checked="listen53"
          :disabled="saving"
          @change="onListen53"
        >
        <span class="flex flex-col gap-1">
          <span>{{ $t('dnsListen53Title') }}</span>
          <span class="text-base-content/50 text-xs">{{ $t('dnsListen53Note') }}</span>
        </span>
      </div>
      <!-- FakeIP:走代理的域名不在本地解析,内核发占位地址,连接时把域名交给选中的节点 -->
      <div
        v-if="mode !== 'off'"
        class="border-base-300/60 flex items-start gap-3 border-t pt-3"
      >
        <input
          type="checkbox"
          class="toggle toggle-sm mt-0.5 cursor-pointer"
          :aria-label="$t('dnsFakeIpTitle')"
          :checked="fakeIp"
          :disabled="saving"
          @change="onFakeIp"
        >
        <span class="flex flex-col gap-1">
          <span>{{ $t('dnsFakeIpTitle') }}</span>
          <span class="text-base-content/50 text-xs">{{ $t('dnsFakeIpNote') }}</span>
        </span>
      </div>
      <!-- FakeIP 网段:留空用内核默认(sing-box 198.19.0.0/16,Mihomo 198.18.0.0/16);和本机别的 TUN / 虚拟网卡冲突时改这里 -->
      <div
        v-if="mode !== 'off'"
        class="flex flex-col gap-1.5"
        :class="!fakeIp && 'opacity-50'"
      >
        <div class="flex flex-wrap items-center gap-2">
          <label
            class="text-xs"
            for="dns-fake-ip-range"
          >{{ $t('dnsFakeIpRangeLabel') }}</label>
          <input
            id="dns-fake-ip-range"
            v-model="rangeInput"
            type="text"
            class="input input-sm w-44 font-mono"
            :placeholder="defaultRange"
            :disabled="saving || !fakeIp"
            @keyup.enter="saveRange"
          >
          <button
            type="button"
            class="btn btn-sm btn-primary"
            :disabled="saving || !fakeIp || !rangeDirty"
            @click="saveRange"
          >
            {{ $t('dnsFakeIpRangeSave') }}
          </button>
          <button
            v-if="rangeCurrent !== ''"
            type="button"
            class="btn btn-sm btn-ghost"
            :disabled="saving || !fakeIp"
            @click="resetRange"
          >
            {{ $t('dnsFakeIpRangeReset') }}
          </button>
        </div>
        <p class="text-base-content/50 text-xs">
          {{ $t('dnsFakeIpRangeNote', { defaultRange }) }}
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { fetchServiceStatus, type OpenboxDnsMode, type OpenboxProfile } from '@/api/openbox'
import { showNotification } from '@/helper/notification'
import { computed, onMounted, ref, watch } from 'vue'

const props = defineProps<{
  profile: OpenboxProfile
  patchProfile: (patch: Record<string, unknown>) => Promise<OpenboxProfile>
}>()

const NOTE_KEY: Record<OpenboxDnsMode, string> = {
  dnsmasq: 'dnsModeDnsmasqNote',
  hijack: 'dnsModeHijackNote',
  off: 'dnsModeOffNote',
}
const saving = ref(false)
// 跑在 systemd 系统上时没有 dnsmasq 可转发:隐藏这一项,档案里写着 dnsmasq 的按防火墙劫持显示
const platform = ref('openwrt')
// 当前内核决定 FakeIP 的默认网段(Mihomo 与 sing-box 的缺省池不同),切换后要重选
const kernel = ref('sing-box')
onMounted(async () => {
  try {
    const status = await fetchServiceStatus()
    platform.value = status.platform ?? 'openwrt'
    kernel.value = status.core?.kernel ?? 'sing-box'
  } catch {
    // 拿不到就按 openwrt / sing-box 处理
  }
})
const dnsmasqAvailable = computed(() => platform.value !== 'systemd')
const mode = computed<OpenboxDnsMode>(() => {
  const current = props.profile.dns?.mode ?? 'dnsmasq'
  return !dnsmasqAvailable.value && current === 'dnsmasq' ? 'hijack' : current
})
const fakeIp = computed(() => props.profile.dns?.fakeIpForProxy === true)
// 面板就在路由器上,当前打开面板的主机名就是路由器地址
const KERNEL_DNS_PORT = 7853
const kernelDnsAddr = `${location.hostname}:${KERNEL_DNS_PORT}`
// 标题里的 DNS 端口:容器一直同时监听 53 / 7853,systemd 在打开「监听 53 端口」后也是,其余只有 7853
const listen53 = computed(() => props.profile.dns?.listen53 === true)
const listenPortText = computed(() =>
  platform.value === 'container' || (platform.value === 'systemd' && listen53.value) ? `53 / ${KERNEL_DNS_PORT}` : String(KERNEL_DNS_PORT),
)

const save = async (patch: Record<string, unknown>) => {
  saving.value = true
  try {
    await props.patchProfile({ dns: patch })
    showNotification({ content: 'dnsModeSaved', type: 'alert-success' })
  } catch (err) {
    showNotification({ content: 'routingSaveFailed', params: { message: err instanceof Error ? err.message : String(err) }, type: 'alert-error' })
  } finally {
    saving.value = false
  }
}

const onFakeIp = (event: Event) => save({ fakeIpForProxy: (event.target as HTMLInputElement).checked })
const onListen53 = (event: Event) => save({ listen53: (event.target as HTMLInputElement).checked })

// FakeIP 网段:留空 = 用内核默认。档案里存的是用户改过的值(与默认值相同时存空串,和上游一样)。
const defaultRange = computed(() => (kernel.value === 'mihomo' ? '198.18.0.0/16' : '198.19.0.0/16'))
const rangeCurrent = computed(() => (props.profile.dns?.fakeIpRange ?? '').trim())
const rangeInput = ref(rangeCurrent.value)
watch([rangeCurrent, defaultRange], () => {
  rangeInput.value = rangeCurrent.value || defaultRange.value
}, { immediate: true })
const rangeDirty = computed(() => rangeInput.value.trim() !== (rangeCurrent.value || defaultRange.value))

// 与上游同一口径:IPv4 CIDR、每段 0~255、掩码 /15~/24;留空也算合法(回到默认)
const validRange = (value: string) => {
  if (value === '') return true
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(value)
  if (!m) return false
  for (let i = 1; i <= 4; i++) if (Number(m[i]) > 255) return false
  const bits = Number(m[5])
  return bits >= 15 && bits <= 24
}

const saveRange = async () => {
  const value = rangeInput.value.trim()
  if (!validRange(value)) {
    showNotification({ content: 'dnsFakeIpRangeInvalid', type: 'alert-error' })
    return
  }
  // 填的就是默认值 → 存空串,老档案与上游面板一致
  const next = value === defaultRange.value ? '' : value
  if (next === rangeCurrent.value) return
  saving.value = true
  try {
    await props.patchProfile({ dns: { fakeIpRange: next } })
    showNotification({ content: 'dnsModeSaved', type: 'alert-success' })
  } catch (err) {
    showNotification({ content: 'routingSaveFailed', params: { message: err instanceof Error ? err.message : String(err) }, type: 'alert-error' })
  } finally {
    saving.value = false
  }
}

const resetRange = () => {
  rangeInput.value = defaultRange.value
  return saveRange()
}

const onChange = (event: Event) => {
  const next = (event.target as HTMLSelectElement).value as OpenboxDnsMode
  if (next === mode.value) return
  return save({ mode: next })
}
</script>
