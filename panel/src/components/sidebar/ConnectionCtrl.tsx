import { disconnectAllAPI, disconnectByIdAPI } from '@/api'
import { useCtrlsBar } from '@/composables/useCtrlsBar'
import { CONNECTION_COLUMN_LABEL_MAP, SORT_TYPE, SORT_TYPE_COLUMN } from '@/constant'
import { useTooltip } from '@/helper/tooltip'
import { isMiddleScreen } from '@/helper/utils'
import {
  activeSortDesc,
  activeSortType,
  connectionFilter,
  connections,
  hideDnsConnections,
  isPaused,
  quickFilterEnabled,
  renderConnections,
  sortTypeOptions,
  toggleSortDirection,
} from '@/store/connections'
import { normalizeRuleTarget } from '@/store/rules'
import { useConnectionCard, useConnectionCardMobile, useConnectionCardPC } from '@/store/settings'
import {
  ArrowDownCircleIcon,
  ArrowUpCircleIcon,
  LinkIcon,
  LinkSlashIcon,
  PauseIcon,
  PlayIcon,
  SparklesIcon,
  WrenchScrewdriverIcon,
  XMarkIcon,
} from '@heroicons/vue/24/outline'
import { defineComponent, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import DialogWrapper from '../common/DialogWrapper.vue'
import TextInput from '../common/TextInput.vue'
import ConnectionCardSettings from '../settings/ConnectionCardSettings.vue'
import SourceIPLabels from '../settings/SourceIPLabels.vue'
import TableSettings from '../settings/TableSettings.vue'
import ConnectionTabs from './ConnectionTabs.vue'
import SourceIPFilter from './SourceIPFilter.vue'

// 排序菜单的选项名和表头一致:主机叫「目标」,源 IP 叫「设备」,其余用列名(与上游 v0.1.488 一致)
const sortOptionLabel = (type: SORT_TYPE) => {
  const column = SORT_TYPE_COLUMN[type]

  return CONNECTION_COLUMN_LABEL_MAP[column] ?? column
}

const handlerClickCloseAll = () => {
  if (renderConnections.value.length === connections.value.length) {
    disconnectAllAPI()
  } else {
    renderConnections.value.forEach((conn) => {
      disconnectByIdAPI(conn.id)
    })
  }
}

export default defineComponent({
  name: 'ConnectionCtrl',
  components: {
    TextInput,
    ConnectionTabs,
    SourceIPFilter,
  },
  setup() {
    const { t } = useI18n()
    const settingsModel = ref(false)
    const settingsTab = ref<'card' | 'table'>('card')
    const { showTip, updateTip } = useTooltip()
    const { isLargeCtrlsBar } = useCtrlsBar(840)

    return () => {
      // 排序标签宽窄屏都用「排序」(connectionSort),与上游 v0.1.476 一致;sortBy 键保留但不引用
      const sortLabel = t('connectionSort')
      // 按钮点一下会切到的方向:当前升序 → 提示「降序」,反之亦然(与上游一致)
      const sortDirectionLabel = t(
        activeSortDesc.value ? 'connectionSortAscending' : 'connectionSortDescending',
      )
      // 当前视图里一个能排序的列都没有时,整个排序控件不显示
      const sortControls = sortTypeOptions.value.length > 0 && (
        <div
          class={[
            'flex min-w-0 items-center gap-1 text-sm max-[360px]:text-xs',
            !isLargeCtrlsBar.value && 'flex-1',
          ]}
        >
          <span class="shrink-0">{sortLabel}</span>
          <div class="join min-w-0 flex-1">
            <select
              class="join-item select select-sm min-w-0 flex-1 truncate max-[360px]:ps-1 max-[360px]:pe-5 max-[360px]:text-xs! max-[360px]:[--app-select-chevron-inset:0.55rem]"
              aria-label={sortLabel}
              value={activeSortType.value}
              onChange={(e) =>
                (activeSortType.value = (e.target as HTMLSelectElement).value as SORT_TYPE)
              }
            >
              {sortTypeOptions.value.map((opt) => (
                <option
                  key={opt}
                  value={opt}
                >
                  {t(sortOptionLabel(opt))}
                </option>
              ))}
            </select>
            <button
              type="button"
              class="btn join-item btn-square btn-sm shrink-0"
              aria-label={sortDirectionLabel}
              onMouseenter={(e) => showTip(e, sortDirectionLabel)}
              onClick={toggleSortDirection}
            >
              {activeSortDesc.value ? (
                <ArrowDownCircleIcon class="size-4" />
              ) : (
                <ArrowUpCircleIcon class="size-4" />
              )}
            </button>
          </div>
        </div>
      )

      const settingsModal = (
        <>
          <button
            class="btn btn-circle btn-sm"
            onClick={() => {
              settingsTab.value = useConnectionCard.value ? 'card' : 'table'
              settingsModel.value = true
            }}
          >
            <WrenchScrewdriverIcon class="h-4 w-4" />
          </button>
          <DialogWrapper
            v-model={settingsModel.value}
            title={t('connectionSettings')}
          >
            {/* 这个弹窗管显示方式、是否隐藏 DNS 劫持连接、表格列和终端名称。「隐藏连接」的开关在工具栏上(链条图标)就有,匹配用的
                正则固定用默认那条;「完整显示代理链」恒开。跳去设置页的入口也去掉了——
                这里能改的东西本来就都在眼前。 */}
            <div class="flex flex-col gap-4 text-sm">
              <div class="connection-style-selectors grid grid-cols-2 gap-4">
                {(
                  [
                    ['connectionStylePC', useConnectionCardPC],
                    ['connectionStyleMobile', useConnectionCardMobile],
                  ] as const
                ).map(([labelKey, style]) => (
                  <label
                    key={labelKey}
                    class="flex min-w-0 items-center justify-between gap-2"
                  >
                    <span class="shrink-0 text-xs whitespace-nowrap sm:text-sm">{t(labelKey)}</span>
                    <select
                      class="select select-sm w-20 max-w-28 min-w-0 flex-1"
                      aria-label={t(labelKey)}
                      value={style.value ? 'card' : 'table'}
                      onChange={(e) =>
                        (style.value = (e.target as HTMLSelectElement).value === 'card')
                      }
                    >
                      <option value="card">{t('card')}</option>
                      <option value="table">{t('table')}</option>
                    </select>
                  </label>
                ))}
              </div>
              <label class="flex cursor-pointer items-center gap-3">
                <input
                  type="checkbox"
                  class="toggle toggle-primary toggle-sm"
                  checked={hideDnsConnections.value}
                  onChange={(e) =>
                    (hideDnsConnections.value = (e.target as HTMLInputElement).checked)
                  }
                />
                <span class="flex flex-col">
                  <span>{t('hideDnsConnections')}</span>
                  <span class="text-base-content/60 text-xs">{t('hideDnsConnectionsHint')}</span>
                </span>
              </label>
              <div class="flex flex-col gap-3">
                <div
                  class="tabs tabs-box tabs-xs self-start"
                  role="tablist"
                >
                  {(['card', 'table'] as const).map((tab) => (
                    <button
                      key={tab}
                      type="button"
                      role="tab"
                      class={['tab', settingsTab.value === tab && 'tab-active']}
                      aria-selected={settingsTab.value === tab}
                      onClick={() => (settingsTab.value = tab)}
                    >
                      {t(tab === 'card' ? 'connectionCardCustomize' : 'connectionTableCustomize')}
                    </button>
                  ))}
                </div>
                <div role="tabpanel">
                  {settingsTab.value === 'card' ? (
                    <ConnectionCardSettings key={isMiddleScreen.value ? 'mobile' : 'pc'} />
                  ) : (
                    <TableSettings />
                  )}
                </div>
              </div>
              <div class="flex flex-col gap-2">
                <SourceIPLabels />
              </div>
            </div>
          </DialogWrapper>
        </>
      )

      const searchInput = (
        <div class="flex min-w-0 flex-1 items-center gap-2">
          <TextInput
            v-model={connectionFilter.value}
            placeholder={`${t('search')} | ${t('searchMultiple')}`}
            clearable={true}
            before-close={true}
            class={isLargeCtrlsBar.value ? 'w-32 max-w-80 flex-1' : 'w-full'}
          />
          <button
            class="btn btn-circle btn-sm shrink-0"
            // 连接表按主机名匹配,端口不要:board.ok1248.cn:4433 → board.ok1248.cn
            onClick={() =>
              (connectionFilter.value = normalizeRuleTarget(connectionFilter.value).replace(
                /:\d+$/,
                '',
              ))
            }
            onMouseenter={(e) => showTip(e, t('ruleFormatQuery'))}
          >
            <SparklesIcon class="h-4 w-4" />
          </button>
        </div>
      )

      const buttons = (
        <>
          <button
            class="btn btn-circle btn-sm"
            onClick={() => {
              quickFilterEnabled.value = !quickFilterEnabled.value
              updateTip(quickFilterEnabled.value ? t('showConnection') : t('hideConnection'))
            }}
            onMouseenter={(e) =>
              showTip(e, quickFilterEnabled.value ? t('showConnection') : t('hideConnection'), {
                appendTo: 'parent',
              })
            }
          >
            {quickFilterEnabled.value ? (
              <LinkSlashIcon class="h-4 w-4" />
            ) : (
              <LinkIcon class="h-4 w-4" />
            )}
          </button>
          <button
            class="btn btn-circle btn-sm"
            onClick={() => {
              isPaused.value = !isPaused.value
            }}
          >
            {isPaused.value ? <PlayIcon class="h-4 w-4" /> : <PauseIcon class="h-4 w-4" />}
          </button>
          <button
            class="btn btn-circle btn-sm"
            onClick={handlerClickCloseAll}
          >
            <XMarkIcon class="h-4 w-4" />
          </button>
        </>
      )

      // 窄屏(isMiddleScreen):分组页签已经挪到页签行右侧(ConnectionsPage 的 mobile-actions 插槽),
      // 这里不再放;布局和上游 v0.1.488 一致:分组页签 / 排序 + 按钮 / 终端筛选 + 搜索,三块各占一行
      const content = !isLargeCtrlsBar.value ? (
        <div class="app-card-padding flex flex-wrap items-center gap-x-2 gap-y-[var(--app-card-space)]">
          {!isMiddleScreen.value && <ConnectionTabs />}
          <div
            class={[
              'flex w-full min-w-0 items-center gap-x-2 gap-y-[var(--app-card-space)]',
              isMiddleScreen.value && 'max-[360px]:gap-x-1',
            ]}
          >
            {sortControls}
            <div
              class={[
                'flex shrink-0 items-center gap-2',
                isMiddleScreen.value && 'max-[360px]:gap-1',
              ]}
            >
              {settingsModal}
              {buttons}
            </div>
          </div>
          <div class="join w-full">
            <SourceIPFilter class="w-40" />
            {searchInput}
          </div>
        </div>
      ) : (
        <div class="app-card-padding flex items-center gap-2">
          {!isMiddleScreen.value && <ConnectionTabs />}
          {sortControls}
          <SourceIPFilter class="w-40" />
          <div class="flex flex-1">{searchInput}</div>
          {settingsModal}
          {buttons}
        </div>
      )

      return <div class="ctrls-bar">{content}</div>
    }
  },
})
