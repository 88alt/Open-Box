import {
  fetchBandwidthStatus,
  queueNodeBandwidth,
  queueSubscriptionBandwidth,
  type OpenboxBandwidthMeasurement,
  type OpenboxBandwidthQueueResult,
} from '@/api/openbox'
import { showNotification } from '@/helper/notification'
import { onMounted, onUnmounted, ref } from 'vue'

// 带宽实测的共享状态:节点标签、订阅卡片、悬浮页签都读这一份,有组件挂着时每 10 秒轮询一次
const measurements = ref<Record<string, OpenboxBandwidthMeasurement>>({})
const pendingBySubscription = ref<Record<string, number>>({})
const pendingNodes = ref<Record<string, boolean>>({})

let users = 0
let timer: ReturnType<typeof setInterval> | undefined
let inflight: Promise<void> | undefined

export const refreshBandwidth = () =>
  (inflight ??= fetchBandwidthStatus()
    .then((status) => {
      measurements.value = status.measurements || {}
      pendingBySubscription.value = status.pendingBySubscription || {}
    })
    .catch(() => {})
    .finally(() => {
      inflight = undefined
    }))

// 必须在组件 setup 里调用:靠挂载/卸载计数起停轮询
export const useBandwidth = () => {
  onMounted(() => {
    if (++users === 1) {
      refreshBandwidth()
      timer = setInterval(() => {
        if (!document.hidden) refreshBandwidth()
      }, 10_000)
    }
  })
  onUnmounted(() => {
    if (--users === 0) {
      clearInterval(timer)
      timer = undefined
    }
  })
  return { bandwidthMeasurements: measurements, bandwidthPending: pendingBySubscription, bandwidthPendingNodes: pendingNodes }
}

const notifyQueued = (result: OpenboxBandwidthQueueResult) => {
  pendingBySubscription.value = result.pendingBySubscription
  showNotification({
    content: 'bandwidthManualQueued',
    params: { queued: String(result.queued), joined: String(result.joined || 0) },
    type: 'alert-info',
  })
}
const notifyFailed = (e: unknown) =>
  showNotification({
    content: 'bandwidthManualFailed',
    params: { message: e instanceof Error ? e.message : String(e) },
    type: 'alert-error',
  })

export const testSubscriptionBandwidth = async (id: string) => {
  if (pendingBySubscription.value[id]) return
  pendingBySubscription.value = { ...pendingBySubscription.value, [id]: 1 }
  try {
    notifyQueued(await queueSubscriptionBandwidth(id))
  } catch (e) {
    const next = { ...pendingBySubscription.value }
    delete next[id]
    pendingBySubscription.value = next
    notifyFailed(e)
  } finally {
    await refreshBandwidth()
  }
}

export const testNodeBandwidth = async (name: string) => {
  if (pendingNodes.value[name] || measurements.value[name]?.pending) return
  pendingNodes.value = { ...pendingNodes.value, [name]: true }
  try {
    notifyQueued(await queueNodeBandwidth(name))
  } catch (e) {
    notifyFailed(e)
  } finally {
    await refreshBandwidth()
    const next = { ...pendingNodes.value }
    delete next[name]
    pendingNodes.value = next
  }
}
