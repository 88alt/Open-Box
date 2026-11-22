import type { OpenboxSkippedNode, OpenboxSubscription, OpenboxSubscriptionSaveResult } from '@/api/openbox'
import { fetchSubscriptions, refreshSubscription } from '@/api/openbox'
import { refreshServiceStatus } from '@/composables/kernelService'
import { showNotification } from '@/helper/notification'
import { i18n } from '@/i18n'
import { ref } from 'vue'

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
) => {
  if (changed === undefined) return
  const needRestart = !!(applied && !applied.ok && applied.pendingRestart?.pending)
  if (changed) {
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

export const loadOpenboxSubscriptions = async () => {
  openboxSubscriptionsLoading.value = true
  openboxSubscriptionsError.value = ''
  try {
    openboxSubscriptions.value = await fetchSubscriptions()
  } catch (error) {
    openboxSubscriptionsError.value = error instanceof Error ? error.message : String(error)
  } finally {
    openboxSubscriptionsLoading.value = false
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
      notifyDroppedNodes(res.skipped)
    } catch {
      // 单条失败不中断整体;具体原因在订阅设置页逐条刷新时会显示出来
    }
  }
  // 几条里只要有一条的节点变了,就提醒一次;有一条需要重启内核才生效就按"需重启"提示
  await notifySubscriptionSaved(changed, 'refreshed', undefined, applied)
  await loadOpenboxSubscriptions()
}
