import { fetchLogsAPI } from '@/api'
import { LOG_LEVEL } from '@/constant'
import { getDeviceName } from '@/helper/sourceip'
import type { Log, LogWithSeq } from '@/types'
import { useStorage } from '@vueuse/core'
import dayjs from 'dayjs'
import { throttle } from 'lodash'
import { ref, watch } from 'vue'

export const logs = ref<LogWithSeq[]>([])
export const logFilter = ref('')
export const logTypeFilter = ref('')
export const isPaused = ref(false)
export const logLevel = useStorage<string>('config/log-level', LOG_LEVEL.Info)
// 页面里保留多少条日志。原来是个设置项,但没人会去改它,固定成原来的默认值。
const LOG_RETENTION_LIMIT = 1000

let cancel: () => void
let logsTemp: LogWithSeq[] = []

const sliceLogs = throttle(() => {
  logs.value = logsTemp.concat(logs.value).slice(0, LOG_RETENTION_LIMIT)
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

    data.payload = labelLogIPs(data.payload)

    logsTemp.unshift({
      ...data,
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
