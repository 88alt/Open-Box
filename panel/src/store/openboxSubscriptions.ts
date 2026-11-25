import type { OpenboxSkippedNode, OpenboxSubscription, OpenboxSubscriptionSaveResult } from '@/api/openbox'
import { fetchSubscriptions, fetchSubscriptionUpdates, refreshSubscription } from '@/api/openbox'
import { refreshServiceStatus } from '@/composables/kernelService'
import { showNotification } from '@/helper/notification'
import { i18n } from '@/i18n'
import { useDocumentVisibility } from '@vueuse/core'
import { ref, watch } from 'vue'

// 保存 / 刷新订阅的返回里带着被跳过的节点(skipped)。其中 reason 为 invalid 的是字段写坏了
// 或内核不认、被直接丢掉的,要告诉用户;其余原因(类型不认识、插件不支持等)只在预览里看。
type SubscriptionApplied = OpenboxSubscriptionSaveResult['applied']

// 提示里最多列出这么多个节点名,多的折成「等,另外还有 N 个」
const DROPPED_LIST_MAX = 5
const notifyDroppedNodes = (skipped: OpenboxSkippedNode[] | undefined) => {
  const invalid = (skipped || []).filter((item) => item.reason === 'invalid')
  if (!invalid.length) return
  const t = i18n.global.t
  const list =
    invalid
      .slice(0, DROPPED_LIST_MAX)
      .map((item) => (item.detail ? `${item.name}(${item.detail})` : item.name))
      .join(t('listSeparator')) +
    (invalid.length > DROPPED_LIST_MAX ? t('subscriptionDroppedMore', { count: invalid.length - DROPPED_LIST_MAX }) : '')
  showNotification({
    content: 'subscriptionDroppedNodes',
    params: { count: String(invalid.length), list },
    type: 'alert-warning',
    timeout: 15000,
  })
}

// 订阅的动作从不自动重启内核(重启会把连接全断一次,什么时候重启由用户决定)。
// 服务端告诉我们两件事:节点池变没变(changed),以及变了之后内核用上没有(applied):
// 用不上而要重启时(applied.ok 为假且 pendingRestart.pending)提示"部分变更需重启内核后生效",
// 否则是"保存成功 / 刷新成功";没变说"节点没有变化"。skipped 是保存 / 刷新返回里被跳过的节点,
// 用来另外提示被丢掉的。有变化或有待重启时顺手刷新一次服务状态,让"待重启"的提示马上出现,不等轮询。
export const notifySubscriptionSaved = async (
  changed: boolean | undefined,
  kind: 'saved' | 'refreshed' = 'saved',
  skipped?: OpenboxSkippedNode[],
  applied?: SubscriptionApplied,
  degraded?: string,
) => {
  if (changed === undefined) return
  const needRestart = !!(applied && !applied.ok && applied.pendingRestart?.pending)
  // 降级(登录来源自动登录失败,退回用原链接刷新)优先于其它提示:这时说「刷新成功」会误导
  if (degraded) {
    showNotification({
      content: 'subscriptionRefreshDegraded',
      params: { message: degraded },
      type: 'alert-warning',
      timeout: 10000,
    })
  } else if (changed) {
    const done = kind === 'refreshed' ? 'subscriptionRefreshedDone' : 'subscriptionSavedDone'
    const restart = kind === 'refreshed' ? 'subscriptionRefreshedRestartRequired' : 'subscriptionSavedRestartRequired'
    showNotification({ content: needRestart ? restart : done, type: needRestart ? 'alert-warning' : 'alert-success' })
  } else {
    showNotification({ content: 'subscriptionNodesUnchanged', type: 'alert-info' })
  }
  notifyDroppedNodes(skipped)
  if (changed || needRestart) await refreshServiceStatus().catch(() => {})
}

// 代理页那个「订阅」标签原本读的是 Clash API 的 /providers/proxies —— 也就是 sing-box 的
// proxy provider。但 Open-Box 是把订阅解析成一条条具体的 outbound 写进配置的,从不生成
// provider,所以那个标签在 Open-Box 里结构上恒为「订阅 (0)」,内核跑没跑起来都一样。
//
// 这里给它换上 Open-Box 自己的订阅列表:代理页和订阅设置页共用同一份数据,任一处刷新
// 之后另一处也是最新的。
export const openboxSubscriptions = ref<OpenboxSubscription[]>([])
export const openboxSubscriptionsLoading = ref(false)
export const openboxSubscriptionsError = ref('')

// silent:后台同步(长轮询触发)用,不动「加载中」状态,免得列表闪一下。
export const loadOpenboxSubscriptions = async (silent = false) => {
  if (!silent) openboxSubscriptionsLoading.value = true
  openboxSubscriptionsError.value = ''
  try {
    openboxSubscriptions.value = await fetchSubscriptions()
  } catch (error) {
    openboxSubscriptionsError.value = error instanceof Error ? error.message : String(error)
  } finally {
    if (!silent) openboxSubscriptionsLoading.value = false
  }
}

// 「全部刷新」:逐个串行刷,不并发。刷新会让服务端去机场拉取,四五个订阅同时拉既容易
// 撞上机场的频率限制,失败了也分不清是哪一条的问题。任一条失败不影响其余继续。
export const refreshAllOpenboxSubscriptions = async () => {
  let changed = false
  let applied: SubscriptionApplied
  for (const sub of [...openboxSubscriptions.value]) {
    try {
      const res = await refreshSubscription(sub.id)
      if (res.changed) changed = true
      if (res.applied && !res.applied.ok && res.applied.pendingRestart?.pending) applied = res.applied
      // 降级按条提示(带订阅名),哪一条登录失败看得出来(与上游一致:最后那条汇总提示照旧)
      if (res.warning) {
        showNotification({
          content: 'subscriptionRefreshDegraded',
          params: { message: `「${sub.name}」${res.warning}` },
          type: 'alert-warning',
          timeout: 10000,
        })
      }
      notifyDroppedNodes(res.skipped)
    } catch {
      // 单条失败不中断整体;具体原因在订阅设置页逐条刷新时会显示出来
    }
  }
  // 几条里只要有一条的节点变了,就提醒一次;有一条需要重启内核才生效就按"需重启"提示
  await notifySubscriptionSaved(changed, 'refreshed', undefined, applied)
  await loadOpenboxSubscriptions()
}

// ---- 订阅变化的长轮询 ----
// 订阅 / 节点在别处变了(别的标签页、后台的自动更新、另一台设备)时,页面不用手动刷新:
// 页面可见时一直挂着问服务端 /subscriptions/updates,服务端在版本变了才回(没变挂约 20 秒后原样回)。
// revision 变了刷新订阅列表;nodesRevision 也变了再重拉节点和服务状态(待重启提示)。
// 页面刚挂上那次的数据由页面自己加载,第一次拿到版本号只记下来、不重复加载;
// 标签页从隐藏回到可见时期间可能错过变化,那一次拿到版本号后补一次完整刷新。
const POLL_FIRST_TIMEOUT_MS = 5_000 // 没带版本号的那次服务端立刻就回
const POLL_HOLD_TIMEOUT_MS = 25_000 // 比服务端挂住的时间长,到点还没回就掐掉重来
const POLL_RETRY_DELAY_MS = 3_000
const POLL_MIN_HOLD_MS = 1_000 // 「没变」的回复至少该挂这么久,更快就当服务端不对劲

const withTimeout = (ms: number, parent: AbortSignal) => {
  const controller = new AbortController()
  const abort = () => controller.abort()
  const timer = setTimeout(abort, ms)
  if (parent.aborted) abort()
  else parent.addEventListener('abort', abort, { once: true })
  return {
    signal: controller.signal,
    cleanup: () => {
      clearTimeout(timer)
      parent.removeEventListener('abort', abort)
    },
  }
}

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve) => {
    const done = () => {
      clearTimeout(timer)
      signal.removeEventListener('abort', done)
      resolve()
    }
    const timer = setTimeout(done, ms)
    signal.addEventListener('abort', done, { once: true })
  })

export interface SubscriptionUpdatesOptions {
  // 刷新订阅列表(静默,别弹通知);返回是否成功
  reloadSubscriptions: () => Promise<boolean>
  // 节点变了要重拉的东西(节点数据、归属表等)
  reloadNodes: () => Promise<unknown>
}

export const useSubscriptionUpdates = (options: SubscriptionUpdatesOptions) => {
  const visibility = useDocumentVisibility()
  let started = false

  const reload = async (nodesChanged: boolean) => {
    const [ok] = await Promise.all([options.reloadSubscriptions(), nodesChanged ? options.reloadNodes() : undefined])
    if (nodesChanged) await refreshServiceStatus().catch(() => {})
    return ok
  }

  const run = async (signal: AbortSignal, resumed: boolean) => {
    let last: { revision: string; nodesRevision: string } | undefined
    while (!signal.aborted) {
      const request = withTimeout(last ? POLL_HOLD_TIMEOUT_MS : POLL_FIRST_TIMEOUT_MS, signal)
      const asked = Date.now()
      try {
        const res = await fetchSubscriptionUpdates(last?.revision ?? '', request.signal)
        if (signal.aborted) return
        // 服务端不带版本号(老后端):没法轮询,不再问
        if (typeof res?.revision !== 'string' || typeof res?.nodesRevision !== 'string') return
        if (last && res.revision === last.revision) {
          // 正常是挂住很久才回;没变却马上回(服务端 / 代理不对劲)就歇一下,别把请求打成死循环
          if (Date.now() - asked < POLL_MIN_HOLD_MS) await sleep(POLL_RETRY_DELAY_MS, signal)
          continue
        }
        if (last || resumed) {
          if (!(await reload(res.nodesRevision !== last?.nodesRevision))) {
            await sleep(POLL_RETRY_DELAY_MS, signal)
            continue
          }
        }
        last = res
      } catch (error) {
        if (signal.aborted) return
        if ((error as { status?: number } | null)?.status === 404) return // 后端没有这个接口
        if (request.signal.aborted) continue // 挂满没回被掐掉,不是出错,直接再问
        await sleep(POLL_RETRY_DELAY_MS, signal)
      } finally {
        request.cleanup()
      }
    }
  }

  watch(
    visibility,
    (state, _old, onCleanup) => {
      if (state !== 'visible') return
      const controller = new AbortController()
      onCleanup(() => controller.abort())
      const resumed = started
      started = true
      void run(controller.signal, resumed)
    },
    { immediate: true },
  )
}
