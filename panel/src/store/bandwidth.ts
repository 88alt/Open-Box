import {
  fetchBandwidthStatus,
  queueNodeBandwidth,
  queueSubscriptionBandwidth,
  type OpenboxBandwidthMeasurement,
  type OpenboxBandwidthQueueResult,
} from '@/api/openbox'
import { latencyReasonText } from '@/helper/latencyReason'
import { showNotification } from '@/helper/notification'
import { onMounted, onUnmounted, ref } from 'vue'

// 带宽实测的共享状态:节点标签、订阅卡片、悬浮页签都读这一份。有组件挂着时每 2 秒看一眼,
// 有手动重测在等或有订阅在排队就真去取,否则最多每 10 秒取一次
const measurements = ref<Record<string, OpenboxBandwidthMeasurement>>({})
const pendingBySubscription = ref<Record<string, number>>({})
// 节点标签上的「检测中」:点了重测到出结果之前一直亮,不是只亮到入队返回
const pendingNodes = ref<Record<string, boolean>>({})
// 等结果的手动重测:节点名 → 这次入队的 requestedAt。测完(成功或失败)才弹结果、才移出
const watching = new Map<string, number | undefined>()

let users = 0
let timer: ReturnType<typeof setInterval> | undefined
let inflight: Promise<void> | undefined
let lastFetchedAt = 0

const clearWatch = (name: string) => {
  watching.delete(name)
  const next = { ...pendingNodes.value }
  delete next[name]
  pendingNodes.value = next
}

const notifyFailed = (message: string) =>
  showNotification({ content: 'bandwidthManualFailed', params: { message }, type: 'alert-error' })

// 对一遍状态:哪些手动重测已经有结果了,有就弹一条
const settleWatching = (status: Awaited<ReturnType<typeof fetchBandwidthStatus>>) => {
  for (const [name, requestedAt] of watching) {
    const m = status.measurements?.[name]
    if (!m && status.lastError && !status.running && !Object.values(status.pendingBySubscription || {}).some(Boolean)) {
      clearWatch(name)
      notifyFailed(`${name} · ${status.lastError}`)
      continue
    }
    // 还在排队 / 测着,或者测量里还是上一次的结果(requestedAt 对不上、没有 completedAt)
    if (!m || m.pending || (requestedAt !== undefined && (m.requestedAt !== requestedAt || !m.completedAt))) continue
    clearWatch(name)
    if (m.error || m.failed || m.mbps == null) {
      notifyFailed(`${name} · ${latencyReasonText(m.reason || 'error')}`)
    } else {
      showNotification({
        content: 'bandwidthManualComplete',
        params: { name, value: String(Math.round(m.mbps)) },
        type: 'alert-success',
      })
    }
  }
}

export const refreshBandwidth = () =>
  (inflight ??= fetchBandwidthStatus()
    .then((status) => {
      measurements.value = status.measurements || {}
      pendingBySubscription.value = status.pendingBySubscription || {}
      lastFetchedAt = Date.now()
      settleWatching(status)
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
        if (document.hidden) return
        if (watching.size || Object.values(pendingBySubscription.value).some(Boolean) || Date.now() - lastFetchedAt >= 10_000) {
          refreshBandwidth()
        }
      }, 2_000)
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
export const testSubscriptionBandwidth = async (id: string) => {
  if (pendingBySubscription.value[id]) return
  pendingBySubscription.value = { ...pendingBySubscription.value, [id]: 1 }
  try {
    notifyQueued(await queueSubscriptionBandwidth(id))
  } catch (e) {
    const next = { ...pendingBySubscription.value }
    delete next[id]
    pendingBySubscription.value = next
    notifyFailed(e instanceof Error ? e.message : String(e))
  } finally {
    await refreshBandwidth()
  }
}

export const testNodeBandwidth = async (name: string) => {
  if (pendingNodes.value[name] || measurements.value[name]?.pending) return
  pendingNodes.value = { ...pendingNodes.value, [name]: true }
  try {
    const result = await queueNodeBandwidth(name)
    watching.set(name, result.requestedAt)
    notifyQueued(result)
  } catch (e) {
    clearWatch(name)
    notifyFailed(e instanceof Error ? e.message : String(e))
  } finally {
    await refreshBandwidth()
  }
}
