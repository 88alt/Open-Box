import { nodeGroups, policyGroups, renderGroups } from '@/composables/proxies'
import { useCtrlsBar } from '@/composables/useCtrlsBar'
import { PROXY_SORT_TYPE, PROXY_TAB_TYPE } from '@/constant'
import { getMinCardWidth, isMiddleScreen } from '@/helper/utils'
import { showNotification } from '@/helper/notification'
import { loadNodeScores } from '@/store/nodeScore'
import { fetchBandwidthStatus, queueNodeBandwidth } from '@/api/openbox'
import {
  openboxSubscriptions,
  refreshAllOpenboxSubscriptions,
} from '@/store/openboxSubscriptions'
import {
  proxyMap,
  allProxiesLatencyTest,
  fetchProxies,
  hasSmartGroup,
  proxiesFilter,
  proxiesTabShow,
} from '@/store/proxies'
import {
  collapseGroupMap,
  groupProxiesByProvider,
  minProxyCardWidth,
  providerProxyCategoryCollapseMap,
  proxyCardSize,
  proxyGroupColumnsCurrent,
  proxySortType,
  useSmartGroupSort,
} from '@/store/settings'
import {
  ArrowPathIcon,
  ArrowPathRoundedSquareIcon,
  BoltIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  RectangleStackIcon,
  RssIcon,
  ServerIcon,
  WrenchScrewdriverIcon,
} from '@heroicons/vue/24/outline'
import { isEmpty } from 'lodash'
import { computed, defineComponent, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import DialogWrapper from '../common/DialogWrapper.vue'
import TextInput from '../common/TextInput.vue'
import PolicyDisplayOrder from '../proxies/PolicyDisplayOrder.vue'

// 三个页签各自的图标(策略 / 节点 / 订阅)
const PROXY_TAB_ICONS = {
  [PROXY_TAB_TYPE.POLICY]: RectangleStackIcon,
  [PROXY_TAB_TYPE.NODE]: ServerIcon,
  [PROXY_TAB_TYPE.PROVIDER]: RssIcon,
}

export default defineComponent({
  name: 'ProxiesCtrl',
  setup() {
    type GlobalCollapseTarget =
      | {
          type: 'group'
          key: string
        }
      | {
          type: 'provider-category'
          key: string
        }

    const { t } = useI18n()
    const isUpgrading = ref(false)
    const isAllLatencyTesting = ref(false)
    const settingsModel = ref(false)
    const { isLargeCtrlsBar } = useCtrlsBar()

    // 订阅标签下的「全部刷新」现在刷的是 Open-Box 订阅,不再是 Clash provider——
    // Open-Box 从不生成 provider,原来那个按钮点了永远是空转。
    const handlerClickUpdateAllProviders = async () => {
      if (isUpgrading.value) return
      isUpgrading.value = true
      try {
        await refreshAllOpenboxSubscriptions()
        await fetchProxies()
      } finally {
        isUpgrading.value = false
      }
    }

    // 上游 v0.1.494:右上角「刷新评分」。做法 = 先测一轮延迟(评分依赖延时),
    // 再强制重拉一次评分表(绕过 30 秒 TTL),让标签上的分数立刻跟着更新。
    const isScoreRefreshing = ref(false)
    const handlerClickRefreshScore = async () => {
      if (isScoreRefreshing.value) return
      isScoreRefreshing.value = true
      const all = t('groupFilterAllNodes')
      try {
        // 第一步:重测延迟(评分的主项),同时把评分表强制重拉一次
        showNotification({ content: 'nodeScoreRefreshStarted', params: { name: all }, type: 'alert-info', timeout: 2000 })
        await allProxiesLatencyTest()

        // 第二步:给这一页渲染的节点排队一次带宽复测 —— 带宽是评分里权重最大的一项,
        // 不重测的话分数只能是「暂估」,与上游「刷新评分会排队带宽」的行为一致
        const names = (proxiesTabShow.value === PROXY_TAB_TYPE.NODE ? nodeGroups.value : renderGroups.value)
          .flatMap((g) => (g.all ?? []).map((p) => p.name))
          .filter((n, i, arr) => n && arr.indexOf(n) === i)

        let queued = 0
        let singleQueuedName = ''
        for (const n of names) {
          try {
            const r = await queueNodeBandwidth(n)
            if ((r.queued ?? 0) > 0) {
              queued += 1
              singleQueuedName = n
            }
          } catch {
            // 单个节点排队失败不影响整体,继续排下一个
          }
        }

        await loadNodeScores(true)
        if (queued > 0) {
          // 有排队:先告诉用户「延迟已刷新、带宽在排队」,分数稍后自己会更新;
          // 然后把队列跟到底,排完了再补一条「已完成」(上游的 SubDone),
          // 一直没动静就在超时后说「暂时拿不到评分」(上游的 Unavailable)。
          // 只排了一个节点时用单节点口径的说法(上游 nodeScoreRefreshQueued),
          // 多个节点才用带 {queued} 的批量口径
          showNotification(
            queued === 1
              ? { content: 'nodeScoreRefreshQueued', params: { name: singleQueuedName }, type: 'alert-success' }
              : { content: 'nodeScoreRefreshSubQueued', params: { name: all, queued: String(queued) }, type: 'alert-success' },
          )
          void watchScoreQueue(all)
        } else {
          showNotification({ content: 'nodeScoreRefreshDone', params: { name: all }, type: 'alert-success' })
        }
      } catch (err) {
        showNotification({
          content: 'nodeScoreRefreshFailed',
          params: { message: err instanceof Error ? err.message : String(err) },
          type: 'alert-error',
        })
      } finally {
        isScoreRefreshing.value = false
      }
    }

    // 排队之后盯着队列:带宽测完就补一条「已刷新评分」,超时没进展就说「暂时拿不到」。
    // 与上游 nodeScoreRefreshSubDone / nodeScoreRefreshUnavailable 两条提示对应。
    const watchScoreQueue = async (label: string) => {
      const deadline = Date.now() + 3 * 60_000 // 最多等 3 分钟(带宽测速本身可能上百 MB)
      let sawPending = false
      while (Date.now() < deadline) {
        await new Promise((r) => window.setTimeout(r, 5000))
        try {
          const st = await fetchBandwidthStatus()
          const pendingCount = Object.values(st.pendingBySubscription ?? {}).filter(Boolean).length
          const anyPending = pendingCount > 0 || st.running === true
          if (anyPending) {
            sawPending = true
            continue
          }
          // 队列空了:重拉一次评分表让标签跟着更新,再提示
          await loadNodeScores(true)
          showNotification({ content: 'nodeScoreRefreshSubDone', params: { name: label }, type: 'alert-success' })
          return
        } catch {
          // 单次查询失败不算超时,继续等
        }
      }
      // 超时:sawPending 为真说明还在测(给「稍后再看」的说法),否则就是没拿到结果
      showNotification({ content: 'nodeScoreRefreshUnavailable', params: { name: label }, type: 'alert-warning', timeout: 8000 })
    }

    const handlerClickLatencyTestAll = async () => {
      if (isAllLatencyTesting.value) return
      isAllLatencyTesting.value = true
      try {
        await allProxiesLatencyTest()
        isAllLatencyTesting.value = false
      } catch {
        isAllLatencyTesting.value = false
      }
    }

    // 全局折叠/展开的目标 = 这一页签正在渲染的那些卡片,折叠键就是卡片名(CollapseCard 的 name)。
    // 「节点」页签渲染的是 nodeGroups(每个节点组一张卡片);fork 基线里这一支指向
    // penetration:<组名>:level-1——那是策略卡片里嵌套组的键,这一页没人读它,按钮点了没反应。
    const globalCollapseTargets = computed<GlobalCollapseTarget[]>(() => {
      const names =
        proxiesTabShow.value === PROXY_TAB_TYPE.NODE ? nodeGroups.value : renderGroups.value
      return names.map((name) => ({ type: 'group', key: name }))
    })

    const hasExpandedTargets = computed(() => {
      return globalCollapseTargets.value.some((target) => {
        if (target.type === 'provider-category') {
          return !providerProxyCategoryCollapseMap.value[target.key]
        }

        return Boolean(collapseGroupMap.value[target.key])
      })
    })

    const handlerClickToggleCollapse = () => {
      const nextCollapseGroupMap = { ...collapseGroupMap.value }
      const nextProviderProxyCategoryCollapseMap = {
        ...providerProxyCategoryCollapseMap.value,
      }

      globalCollapseTargets.value.forEach((target) => {
        if (target.type === 'provider-category') {
          nextProviderProxyCategoryCollapseMap[target.key] = hasExpandedTargets.value
          return
        }

        nextCollapseGroupMap[target.key] = !hasExpandedTargets.value
      })

      collapseGroupMap.value = nextCollapseGroupMap
      providerProxyCategoryCollapseMap.value = nextProviderProxyCategoryCollapseMap
    }

    const handlerResetProxyCardWidth = () => {
      minProxyCardWidth.value = getMinCardWidth(proxyCardSize.value)
    }

    const tabsWithNumbers = computed(() => {
      return Object.values(PROXY_TAB_TYPE).map((type) => {
        return {
          type,
          // 内核没数据时策略/节点都是 0:全局模式下 getCurrentProxyGroups 会凭空给一个
          // GLOBAL,内核根本没跑也会数出"策略 (1)",让人以为有东西只是没显示出来
          count:
            type === PROXY_TAB_TYPE.PROVIDER
              ? openboxSubscriptions.value.length
              : isEmpty(proxyMap.value)
                ? 0
                : type === PROXY_TAB_TYPE.POLICY
                  ? policyGroups.value.length
                  : nodeGroups.value.length,
        }
      })
    })

    return () => {
      const isProviderTab = proxiesTabShow.value === PROXY_TAB_TYPE.PROVIDER
      const moveRefreshToSecondRow = !isLargeCtrlsBar.value && isProviderTab

      // 顶部三个页签:和设置页同一套横向菜单(icon + 文字,选中是填充圆角块);手机端窄屏只留文字
      const tabs = (
        <ul
          key="proxy-main-tabs"
          role="tablist"
          class="proxy-main-tabs menu menu-horizontal scrollbar-hidden app-inline-gap flex min-w-0 flex-nowrap items-center overflow-x-auto bg-transparent p-0"
        >
          {tabsWithNumbers.value.map(({ type, count }) => {
            const label = t(type)
            const Icon = PROXY_TAB_ICONS[type]

            return (
              <li
                key={type}
                class="settings-menu-slot min-w-fit"
              >
                <button
                  type="button"
                  role="tab"
                  aria-selected={proxiesTabShow.value === type}
                  class={['settings-menu-btn whitespace-nowrap', proxiesTabShow.value === type && 'menu-active']}
                  onClick={() => (proxiesTabShow.value = type)}
                >
                  {!isMiddleScreen.value && (
                    <Icon
                      aria-hidden="true"
                      class="h-5 w-5 shrink-0"
                    />
                  )}
                  <span>
                    {label} ({count})
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      )

      const upgradeAllIcon = proxiesTabShow.value === PROXY_TAB_TYPE.PROVIDER && (
        <button
          class="btn btn-circle btn-sm"
          onClick={handlerClickUpdateAllProviders}
        >
          <ArrowPathIcon class={['h-4 w-4', isUpgrading.value && 'animate-spin']} />
        </button>
      )

      const sort = (
        <select
          class={['select select-sm']}
          v-model={proxySortType.value}
        >
          {Object.values(PROXY_SORT_TYPE).map((type) => {
            return (
              <option
                key={type}
                value={type}
              >
                {t(type)}
              </option>
            )
          })}
        </select>
      )

      const latencyTestAll = (
        <button
          class="btn btn-circle btn-sm"
          onClick={handlerClickLatencyTestAll}
        >
          {isAllLatencyTesting.value ? (
            <span class="loading loading-spinner loading-sm"></span>
          ) : (
            <BoltIcon class="h-4 w-4" />
          )}
        </button>
      )

      // 上游 v0.1.494:右上角刷新评分(测一轮延迟 + 强制重拉评分表)
      const refreshScore = proxiesTabShow.value !== PROXY_TAB_TYPE.PROVIDER && (
        <button
          class="btn btn-circle btn-sm"
          aria-label={t('nodeScoreRefresh')}
          title={t('nodeScoreRefresh')}
          onClick={handlerClickRefreshScore}
        >
          {isScoreRefreshing.value ? (
            <span class="loading loading-spinner loading-sm"></span>
          ) : (
            <ArrowPathRoundedSquareIcon class="h-4 w-4" />
          )}
        </button>
      )

      const toggleCollapseAll = (
        <button
          class={[
            'btn btn-circle btn-sm',
            proxyGroupColumnsCurrent.value > 1 &&
              proxiesTabShow.value !== PROXY_TAB_TYPE.PROVIDER &&
              'max-sm:hidden',
          ]}
          onClick={handlerClickToggleCollapse}
        >
          {hasExpandedTargets.value ? (
            <ChevronUpIcon class="h-4 w-4" />
          ) : (
            <ChevronDownIcon class="h-4 w-4" />
          )}
        </button>
      )

      const searchInput = (
        <TextInput
          class={[
            isLargeCtrlsBar.value
              ? 'w-32 max-w-80 flex-1'
              : moveRefreshToSecondRow
                ? 'w-full'
                : 'w-32 flex-1',
          ]}
          v-model={proxiesFilter.value}
          placeholder={`${t('search')} | ${t('searchMultiple')}`}
          clearable={true}
        />
      )

      const searchSection = <div class={['flex min-w-0 flex-1 items-center']}>{searchInput}</div>

      const settingsModal = (
        <>
          <button
            class="btn btn-circle btn-sm"
            onClick={() => (settingsModel.value = true)}
          >
            <WrenchScrewdriverIcon class="h-4 w-4" />
          </button>
          <DialogWrapper
            v-model={settingsModel.value}
            title={t('proxySettings')}
            boxClass="w-full max-w-2xl"
          >
            <div class="flex flex-col gap-4 text-sm">
              <div class="flex items-center gap-2">
                {t('proxyNodeSortBy')}
                {sort}
              </div>
              {hasSmartGroup.value && (
                <div class="flex items-center gap-2">
                  {t('useSmartGroupSort')}
                  <input
                    class="toggle"
                    type="checkbox"
                    v-model={useSmartGroupSort.value}
                  />
                </div>
              )}
              <div class="flex items-center gap-2">
                {t('groupProxiesByProvider')}
                <input
                  type="checkbox"
                  class="toggle"
                  v-model={groupProxiesByProvider.value}
                />
              </div>
              <div class="flex items-center gap-2">
                {t('minProxyCardWidth')}
                <div class="join">
                  <input
                    class="input input-sm join-item w-20"
                    type="number"
                    v-model={minProxyCardWidth.value}
                  />
                  <button
                    class="btn join-item btn-sm"
                    onClick={handlerResetProxyCardWidth}
                  >
                    {t('reset')}
                  </button>
                </div>
              </div>
              {/* 策略的显示顺序(可拖)与命中顺序(只看)并排;弹窗开着时才渲染,免得每次
                  刷代理页都跟着重算 */}
              {settingsModel.value && (
                <>
                  <div class="divider my-0" />
                  <PolicyDisplayOrder />
                </>
              )}
            </div>
          </DialogWrapper>
        </>
      )

      const content = !isLargeCtrlsBar.value ? (
        <div class="app-card-padding app-card-gap flex flex-col">
          <div class="flex gap-2">
            {tabs}
            {!moveRefreshToSecondRow && upgradeAllIcon}
          </div>
          <div class="flex w-full gap-2">
            {searchSection}
            <div class="ml-auto flex shrink-0 items-center gap-2">
              {moveRefreshToSecondRow && upgradeAllIcon}
              {settingsModal}
              {toggleCollapseAll}
              {latencyTestAll}
              {refreshScore}
            </div>
          </div>
        </div>
      ) : (
        <div class="app-card-padding flex gap-2">
          {tabs}
          {searchSection}
          {upgradeAllIcon}
          {settingsModal}
          {toggleCollapseAll}
          {latencyTestAll}
          {refreshScore}
        </div>
      )

      return <div class="ctrls-bar">{content}</div>
    }
  },
})
