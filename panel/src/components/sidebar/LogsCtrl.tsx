import { isSingBox } from '@/api'
import { LOG_LEVEL } from '@/constant'
import { logSeverityLabel } from '@/helper/logSeverity'
import { useTooltip } from '@/helper/tooltip'
import { isMiddleScreen } from '@/helper/utils'
import {
  initLogs,
  isPaused,
  logFilter,
  logLevel,
  logLevelFilter,
  logSettingsOpen,
  logs,
} from '@/store/logs'
import { normalizeRuleTarget } from '@/store/rules'
import { logSearchHistory } from '@/store/settings'
import {
  ArrowDownTrayIcon,
  Cog6ToothIcon,
  PauseIcon,
  PlayIcon,
  SparklesIcon,
  XMarkIcon,
} from '@heroicons/vue/24/outline'
import dayjs from 'dayjs'
import { debounce } from 'lodash'
import { computed, defineComponent, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import TextInput from '../common/TextInput.vue'
import LogTabs from '../logs/LogTabs.vue'

const logLevels = computed(() => {
  if (isSingBox.value) {
    return Object.values(LOG_LEVEL)
  }
  return [LOG_LEVEL.Debug, LOG_LEVEL.Info, LOG_LEVEL.Warning, LOG_LEVEL.Error, LOG_LEVEL.Silent]
})

const downloadAllLogs = () => {
  const blob = new Blob(
    [
      logs.value
        .map((log) =>
          [
            log.seq.toString().padEnd(5, ' '),
            log.time,
            log.type.padEnd(7, ' '),
            log.rawPayload,
          ].join('\t'),
        )
        .join('\n'),
    ],
    {
      type: 'text/plain',
    },
  )
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = dayjs().format('YYYY-MM-DD HH-mm-ss') + '.log'
  a.click()
  URL.revokeObjectURL(url)
}

// 日志级别下拉:宽屏在控制栏里,窄屏挪到页签行右侧(连接页的 mobile-actions 插槽),两处共用这一个组件
export const LogLevelSelect = defineComponent({
  setup() {
    const { t } = useI18n()

    return () => (
      <select
        class={[
          'select select-sm',
          isMiddleScreen.value ? 'max-w-24 min-w-0 flex-1' : 'join-item w-24 shrink-0',
        ]}
        aria-label={t('logReceiveLevel')}
        title={logSeverityLabel(logLevel.value, t)}
        v-model={logLevel.value}
        onChange={initLogs}
      >
        {logLevels.value.map((opt) => (
          <option
            key={opt}
            value={opt}
          >
            {logSeverityLabel(opt, t)}
          </option>
        ))}
      </select>
    )
  },
})

// 下载 / 暂停 / 清空三个按钮,同样两处共用
export const LogActionButtons = defineComponent({
  setup() {
    const { t } = useI18n()
    const { showTip } = useTooltip()

    return () => {
      const pauseLabel = t(isPaused.value ? 'logResume' : 'logPause')

      return (
        <div class="flex shrink-0 items-center gap-2">
          <button
            type="button"
            class="btn btn-circle btn-sm"
            aria-label={t('logSettings')}
            aria-haspopup="dialog"
            onMouseenter={(e) => showTip(e, t('logSettings'))}
            onClick={() => (logSettingsOpen.value = true)}
          >
            <Cog6ToothIcon class="h-4 w-4" />
          </button>
          <button
            class="btn btn-circle btn-sm"
            aria-label={t('logDownload')}
            onMouseenter={(e) => showTip(e, t('logDownload'))}
            onClick={downloadAllLogs}
          >
            <ArrowDownTrayIcon class="h-4 w-4" />
          </button>
          <button
            class="btn btn-circle btn-sm"
            aria-label={pauseLabel}
            onMouseenter={(e) => showTip(e, pauseLabel)}
            onClick={() => (isPaused.value = !isPaused.value)}
          >
            {isPaused.value ? <PlayIcon class="h-4 w-4" /> : <PauseIcon class="h-4 w-4" />}
          </button>
          <button
            class="btn btn-circle btn-sm hover:text-error"
            aria-label={t('logClear')}
            onMouseenter={(e) => showTip(e, t('logClear'))}
            onClick={() => (logs.value = [])}
          >
            <XMarkIcon class="h-4 w-4" />
          </button>
        </div>
      )
    }
  },
})

export default defineComponent({
  setup() {
    const { t } = useI18n()
    const { showTip } = useTooltip()
    const insertLogSearchHistory = debounce((log: string) => {
      if (!log) {
        return
      }

      const idx = logSearchHistory.value.indexOf(log)

      if (idx !== -1) {
        logSearchHistory.value.splice(idx, 1)
      }

      logSearchHistory.value.unshift(log)
      if (logSearchHistory.value.length > 5) {
        logSearchHistory.value.pop()
      }
    }, 1500)

    watch(logFilter, insertLogSearchHistory)

    // 筛选项:内核支持的级别(不含静默) + 已收到日志里出现过的级别
    const logFilterLevels = computed(() => {
      const levels = logLevels.value.filter((level) => level !== LOG_LEVEL.Silent) as string[]

      for (const log of logs.value) {
        if (!levels.includes(log.type)) {
          levels.push(log.type)
        }
      }

      return levels
    })

    return () => {
      const searchInput = (
        <div class="flex min-w-0 flex-1 items-center gap-2">
          <TextInput
            v-model={logFilter.value}
            beforeClose={true}
            class={isMiddleScreen.value ? 'min-w-0 flex-1' : 'min-w-12 flex-1'}
            placeholder={isMiddleScreen.value ? t('search') : `${t('search')} | Regex`}
            clearable={true}
            menus={logSearchHistory.value}
            menusDeleteable={true}
            onUpdate:menus={(val) => (logSearchHistory.value = val)}
          />
          <button
            class="btn btn-circle btn-sm shrink-0 max-[360px]:hidden"
            aria-label={t('ruleFormatQuery')}
            // 和连接页一样:整条 URL 整理成主机名(不带端口)再搜
            onClick={() =>
              (logFilter.value = normalizeRuleTarget(logFilter.value).replace(/:\d+$/, ''))
            }
            onMouseenter={(e) => showTip(e, t('ruleFormatQuery'))}
          >
            <SparklesIcon class="h-4 w-4" />
          </button>
          <LogActionButtons />
        </div>
      )

      const logLevelSelect = (
        <select
          class={[
            'select select-sm w-auto shrink-0',
            isMiddleScreen.value ? 'max-w-28 ps-2 pe-6 max-[420px]:text-xs!' : 'max-w-36',
          ]}
          aria-label={t('logLevel')}
          v-model={logLevelFilter.value}
        >
          <option value="">{t('logAllLevels')}</option>
          {logFilterLevels.value.map((opt) => (
            <option
              key={opt}
              value={opt}
            >
              {logSeverityLabel(opt, t)}
            </option>
          ))}
        </select>
      )

      // 窄屏:分组页签在页签行右侧(ConnectionsPage 的 mobile-actions 插槽),这里只留类型筛选、搜索和按钮
      const content = (
        <div class="app-card-padding app-inline-gap flex min-w-0 flex-nowrap items-center">
          {!isMiddleScreen.value && <LogTabs class="shrink-0" />}
          {logLevelSelect}
          {searchInput}
        </div>
      )

      return <div class="ctrls-bar">{content}</div>
    }
  },
})
