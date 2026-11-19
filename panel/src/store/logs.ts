import { fetchLogsAPI } from '@/api'
import { CONNECTION_GROUP_MODE, LOG_LEVEL } from '@/constant'
import { normalizeLogLevel } from '@/helper/logSeverity'
import { getDeviceName } from '@/helper/sourceip'
import { isMiddleScreen } from '@/helper/utils'
import type { Log, LogWithSeq } from '@/types'
import { useStorage } from '@vueuse/core'
import dayjs from 'dayjs'
import { throttle } from 'lodash'
import { computed, ref, watch } from 'vue'

export const logs = ref<LogWithSeq[]>([])
export const logFilter = ref('')
export const logLevelFilter = ref('')
export const isPaused = ref(false)
export const logLevel = useStorage<string>('config/log-level', LOG_LEVEL.Info)

// 日志分组方式(不分组 / 按终端 / 按目标)和每种分组方式下展开的组、子组;表格 / 卡片布局 PC 和手机各存各的
export type LogGroupMode = 'none' | CONNECTION_GROUP_MODE
export type LogSubgroupMode = 'none' | 'routing' | CONNECTION_GROUP_MODE
export interface LogGroupSelection {
  group: string
  subgroupMode: LogSubgroupMode
  subgroup: string | null
}
export const logGroupMode = useStorage<LogGroupMode>('logs/group-mode', 'none')
export const logGroupSelection = useStorage<
  Partial<Record<CONNECTION_GROUP_MODE, LogGroupSelection>>
>('logs/group-selection', {})
export const logLayoutPC = useStorage<'table' | 'card'>('config/log-layout-pc', 'table')
export const logLayoutMobile = useStorage<'table' | 'card'>('config/log-layout-mobile', 'card')
export const logLayout = computed(() =>
  isMiddleScreen.value ? logLayoutMobile.value : logLayoutPC.value,
)
export const logSettingsOpen = ref(false)
// 页面里保留多少条日志。原来是个设置项,但没人会去改它,固定成原来的默认值。
const LOG_RETENTION_LIMIT = 1000
// 节点探测的日志很频繁,单独限制条数,免得把普通日志挤掉
const PROBE_LOG_LIMIT = 200

let cancel: () => void
let logsTemp: LogWithSeq[] = []

const sliceLogs = throttle(() => {
  let probeCount = 0

  logs.value = logsTemp
    .concat(logs.value)
    .filter((log) => !log.probe || ++probeCount <= PROBE_LOG_LIMIT)
    .slice(0, LOG_RETENTION_LIMIT)
  logsTemp = []
}, 500)

// 日志里的 `IP:端口` 换成 `IP (名字) :端口`,名字走终端命名(MAC / IP 标签、邻居表主机名)
const ipPortPattern = /(\[[0-9a-fA-F:.]+\]|\b\d{1,3}(?:\.\d{1,3}){3}):(?=\d)/g
const labelLogIPs = (payload: string) =>
  payload.replace(ipPortPattern, (matched, ip: string) => {
    const name = getDeviceName(ip.startsWith('[') ? ip.slice(1, -1) : ip)

    return name ? `${ip} (${name}) :` : matched
  })

export const initLogs = () => {
  cancel?.()
  logs.value = []
  logsTemp = []

  let idx = 1
  const ws = fetchLogsAPI<Log>({
    level: logLevel.value,
  })

  const unwatch = watch(ws.data, (data) => {
    if (!data) return

    if (isPaused.value) {
      idx++
      return
    }

    logsTemp.unshift({
      ...data,
      type: normalizeLogLevel(data.type),
      rawPayload: data.payload,
      payload: labelLogIPs(data.payload),
      time: dayjs().format('HH:mm:ss'),
      seq: idx++,
    })

    sliceLogs()
  })

  cancel = () => {
    unwatch()
    ws.close()
  }
}
