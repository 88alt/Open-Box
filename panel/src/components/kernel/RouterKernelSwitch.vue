<template>
  <div
    v-if="current"
    class="flex flex-wrap items-center gap-2"
  >
    <label
      id="router-kernel-label"
      for="router-kernel"
      class="text-base-content/70"
    >{{ $t('routerKernelLabel') }}</label>
    <!-- 自定义下拉:触发按钮和每一项都带内核图标(对齐上游 v0.1.487) -->
    <KernelSelect
      :model-value="selected ?? current"
      :disabled="switching"
      @update:model-value="onSelect"
    />
    <span
      v-if="!kernelVersion?.mihomoAvailable"
      class="text-base-content/60 text-xs"
    >{{ $t('routerKernelUnavailable') }}</span>

    <!-- 切换前先逐项检查,全部通过才能点「切换并重启」 -->
    <DialogWrapper
      :model-value="selected !== null && !switching"
      :title="$t('routerKernelSwitch')"
      footer-class="flex-wrap"
      @update:model-value="onDialogToggle"
    >
      <div class="flex flex-col">
        <p v-if="selected">
          {{ $t('routerKernelConfirm', { kernel: KERNEL_NAMES[selected] }) }}
        </p>
        <p
          v-if="failed"
          class="text-error mt-2 text-sm"
        >
          {{ $t('routerKernelPreflightError') }}
          <span
            v-if="failedMessage"
            class="text-base-content/60 block text-xs break-words"
          >{{ failedMessage }}</span>
        </p>
        <ul
          class="divide-base-content/10 mt-2 divide-y"
          :aria-label="$t('routerKernelPreflightLabel')"
          :aria-busy="!!controller"
        >
          <li
            v-for="item in items"
            :key="item.id"
            class="flex items-start gap-2 py-2"
          >
            <CheckCircleIcon
              v-if="item.status === 'passed'"
              class="text-success mt-0.5 size-5 shrink-0"
              aria-hidden="true"
            />
            <XCircleIcon
              v-else-if="item.status === 'failed'"
              class="text-error mt-0.5 size-5 shrink-0"
              aria-hidden="true"
            />
            <ClockIcon
              v-else-if="item.status === 'pending'"
              class="text-base-content/60 mt-0.5 size-5 shrink-0"
              aria-hidden="true"
            />
            <MinusCircleIcon
              v-else
              class="text-base-content/60 mt-0.5 size-5 shrink-0"
              aria-hidden="true"
            />
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 text-sm">
                <span class="font-medium">{{ $t(`routerKernelChecks.${item.id}`) }}</span>
                <span
                  :class="{
                    'text-success': item.status === 'passed',
                    'text-error': item.status === 'failed',
                    'text-base-content/60': item.status !== 'passed' && item.status !== 'failed',
                  }"
                >{{ $t(`routerKernelCheckStatus.${item.status}`) }}</span>
              </div>
              <ul
                v-if="item.details?.length"
                class="text-base-content/70 mt-1 space-y-1 text-xs"
              >
                <li
                  v-for="(line, index) in item.details"
                  :key="index"
                  class="break-words whitespace-pre-wrap"
                >
                  {{ line }}
                </li>
              </ul>
            </div>
          </li>
        </ul>
      </div>
      <template #footer>
        <button
          v-if="!controller && !canSwitch"
          type="button"
          class="btn btn-ghost btn-sm mr-auto"
          @click="runPreflight"
        >
          {{ $t('routerKernelCheckAgain') }}
        </button>
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          @click="closeDialog"
        >
          {{ $t('cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-error btn-sm"
          :disabled="!canSwitch"
          @click="runSwitch"
        >
          {{ $t('routerKernelSwitch') }}
        </button>
      </template>
    </DialogWrapper>
  </div>
</template>

<script setup lang="ts">
// 路由器内核切换(对齐上游 v0.1.477):下拉选另一个内核 → 弹窗里按固定顺序逐项预检(目标内核、系统条件、
// 代理工具冲突、内核专用设置兼容性、规则集、目标内核配置、入口防火墙、切换期间 DNS 防泄露)→ 全部通过
// (或不适用)才允许「切换并重启」。预检是流式的,每项之间至少隔 120ms,让用户看得清进度。
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import KernelSelect from '@/components/kernel/KernelSelect.vue'
import type { OpenboxKernelSwitchCheck, OpenboxKernelVersion, OpenboxRouterKernel } from '@/api/openbox'
import { fetchKernelSwitchPreflight, KERNEL_SWITCH_CHECK_IDS, switchRouterKernel } from '@/api/openbox'
import { showNotification } from '@/helper/notification'
import { CheckCircleIcon, ClockIcon, MinusCircleIcon, XCircleIcon } from '@heroicons/vue/24/outline'
import { computed, onBeforeUnmount, ref } from 'vue'

type ItemStatus = OpenboxKernelSwitchCheck['status'] | 'waiting' | 'pending'
interface CheckItem {
  id: OpenboxKernelSwitchCheck['id']
  status: ItemStatus
  details?: string[]
}

const props = defineProps<{ kernelVersion: OpenboxKernelVersion | null }>()
const emit = defineEmits<{ switched: [] }>()

const KERNEL_NAMES: Record<OpenboxRouterKernel, string> = { 'sing-box': 'Sing-Box', mihomo: 'Mihomo' }
const NOTIFY_KEY = 'router-kernel-switch'
const STEP_INTERVAL_MS = 120

const current = computed(() => props.kernelVersion?.kernel ?? null)
const selected = ref<OpenboxRouterKernel | null>(null)
const checks = ref<OpenboxKernelSwitchCheck[]>([])
const controller = ref<AbortController | null>(null)
const failed = ref(false)
const failedMessage = ref('')
const switching = ref(false)

const items = computed<CheckItem[]>(() =>
  KERNEL_SWITCH_CHECK_IDS.map((id, index) =>
    checks.value[index] ?? {
      id,
      status: controller.value ? (index === checks.value.length ? 'pending' : 'waiting') : 'blocked',
    },
  ),
)

const canSwitch = computed(
  () =>
    !switching.value &&
    !controller.value &&
    !failed.value &&
    !!selected.value &&
    !!current.value &&
    selected.value !== current.value &&
    checks.value.length === KERNEL_SWITCH_CHECK_IDS.length &&
    checks.value.every((check) => check.status === 'passed' || check.status === 'notApplicable'),
)

const runPreflight = async () => {
  const kernel = selected.value
  if (!kernel) return
  controller.value?.abort()
  const mine = new AbortController()
  controller.value = mine
  checks.value = []
  failed.value = false
  failedMessage.value = ''
  let last = -Infinity
  try {
    await fetchKernelSwitchPreflight(kernel, mine.signal, async (check) => {
      const wait = last + STEP_INTERVAL_MS - performance.now()
      if (wait > 0) await new Promise((resolve) => setTimeout(resolve, wait))
      if (controller.value === mine && !mine.signal.aborted) {
        checks.value = [...checks.value, check]
        last = performance.now()
      }
    })
  } catch (error) {
    if (controller.value !== mine || mine.signal.aborted) return
    failed.value = true
    failedMessage.value = error instanceof Error ? error.message : String(error)
  } finally {
    if (controller.value === mine) controller.value = null
  }
}

const reset = () => {
  controller.value?.abort()
  controller.value = null
  selected.value = null
  checks.value = []
  failed.value = false
  failedMessage.value = ''
}

const onSelect = (kernel: OpenboxRouterKernel) => {
  if (kernel === current.value || switching.value) return
  selected.value = kernel
  void runPreflight()
}

const closeDialog = () => reset()
const onDialogToggle = (open: boolean | undefined) => {
  if (!open) reset()
}

const runSwitch = async () => {
  const target = selected.value
  const from = current.value
  if (!target || !from || !canSwitch.value) return
  switching.value = true
  showNotification({
    content: 'routerKernelSwitching',
    params: { from: KERNEL_NAMES[from], kernel: KERNEL_NAMES[target] },
    key: NOTIFY_KEY,
    type: 'alert-info',
    timeout: 0,
  })
  try {
    const result = await switchRouterKernel(target)
    if (!result.ok) throw new Error(result.stderr)
    if (result.warning) {
      showNotification({ content: result.warning, key: NOTIFY_KEY, type: 'alert-warning' })
    } else {
      showNotification({
        content: 'routerKernelSwitched',
        params: { kernel: KERNEL_NAMES[target] },
        key: NOTIFY_KEY,
        type: 'alert-success',
      })
    }
  } catch (error) {
    showNotification({
      content: 'routerKernelFailed',
      params: { message: error instanceof Error ? error.message : String(error) },
      key: NOTIFY_KEY,
      type: 'alert-error',
    })
  } finally {
    // 不管成败都重新读版本和服务状态,再收起弹窗
    emit('switched')
    reset()
    switching.value = false
  }
}

onBeforeUnmount(reset)
</script>
