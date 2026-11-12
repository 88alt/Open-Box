import { disconnectByIdAPI, fetchConnectionsAPI } from '@/api'
import { CONNECTION_GROUP_MODE, SORT_DIRECTION, SORT_TYPE, SORT_TYPE_COLUMN } from '@/constant'
import { getChainsStringFromConnection, getInboundUserFromConnection } from '@/helper'
import { showNotification } from '@/helper/notification'
import type { Connection, ConnectionRawMessage } from '@/types'
import { useStorage, watchOnce } from '@vueuse/core'
import dayjs from 'dayjs'
import { computed, ref, watch } from 'vue'
import { initAggregatedDataMap, saveConnectionHistory } from './connHistory'
import {
  autoDisconnectIdleUDP,
  autoDisconnectIdleUDPTime,
  connectionTableColumns,
  useConnectionCard,
} from './settings'

// 分组方式只在本次打开页面期间有效(换方式会收起已展开的组)
export const connectionGroupMode = ref<CONNECTION_GROUP_MODE>(CONNECTION_GROUP_MODE.DEVICE)
export const connectionSortType = useStorage<SORT_TYPE>(
  'config/connection-sort-type',
  SORT_TYPE.HOST,
)
export const connectionSortDirection = useStorage<SORT_DIRECTION>(
  'config/connection-sort-direction',
  SORT_DIRECTION.ASC,
)

export const quickFilterRegex = useStorage<string>('config/quick-filter-regex', 'direct|dns-out')
export const quickFilterEnabled = useStorage<boolean>('config/quick-filter-enabled', false)
// 终端的 DNS 查询被劫持进内核后交给 dnsmasq / dns-out 的那些 127.0.0.1:53 行,默认不显示
export const hideDnsConnections = useStorage<boolean>('config/hide-dns-connections', true)
const isDnsHijackConnection = (conn: {
  metadata: { destinationPort: string }
  chains: string[]
  rule: string
}) =>
  conn.metadata.destinationPort === '53' &&
  (conn.chains.includes('dnsmasq') ||
    conn.chains.includes('dns-out') ||
    (conn.rule || '').includes('route(dnsmasq'))
export const connectionFilter = ref('')
export const sourceIPFilter = ref<string[] | null>(null)

export const activeConnections = ref<Connection[]>([])
export const closedConnections = ref<Connection[]>([])
export const isPaused = ref(false)

export const downloadTotal = ref(0)
export const uploadTotal = ref(0)

let cancel: () => void
let previousConnectionsMap = new Map<string, Connection>()

export const initConnections = () => {
  cancel?.()
  activeConnections.value = []
  closedConnections.value = []
  downloadTotal.value = 0
  uploadTotal.value = 0
  previousConnectionsMap.clear()
  initAggregatedDataMap()
  const ws = fetchConnectionsAPI<{
    connections: ConnectionRawMessage[]
    downloadTotal: number
    uploadTotal: number
    memory: number
  }>()
  const unwatch = watch(ws.data, (data) => {
    if (!data) return

    downloadTotal.value = data.downloadTotal
    uploadTotal.value = data.uploadTotal

    if (isPaused.value) {
      return
    }

    const currentConnectionsMap = new Map<string, Connection>()

    activeConnections.value =
      data.connections?.map((conn) => {
        const connection = conn as Connection
        const preConnection = previousConnectionsMap.get(connection.id)

        if (
          (connection.metadata.destinationPort === '443' || connection.metadata.sniffHost) &&
          connection.metadata.network === 'udp'
        ) {
          connection.metadata.network = 'quic'
        }

        if (!preConnection) {
          connection.downloadSpeed = 0
          connection.uploadSpeed = 0
        } else {
          connection.downloadSpeed = connection.download - preConnection.download
          connection.uploadSpeed = connection.upload - preConnection.upload
        }

        previousConnectionsMap.delete(connection.id)
        currentConnectionsMap.set(connection.id, connection)
        return connection
      }) ?? []

    const newlyClosedConnections = Array.from(previousConnectionsMap.values())
    closedConnections.value = closedConnections.value.concat(newlyClosedConnections).slice(-500)

    if (newlyClosedConnections.length > 0) {
      saveConnectionHistory(newlyClosedConnections)
    }

    previousConnectionsMap = currentConnectionsMap
  })

  if (autoDisconnectIdleUDP.value) {
    watchOnce(activeConnections, () => {
      activeConnections.value
        .filter((conn) => conn.metadata.network !== 'tcp')
        .forEach((conn) => {
          const now = dayjs()
          const start = dayjs(conn.start)

          if (now.diff(start, 'minute') > autoDisconnectIdleUDPTime.value) {
            disconnectByIdAPI(conn.id)
          }
        })
    })
  }

  cancel = () => {
    unwatch()
    ws.close()
  }
}

export const isDesc = computed(() => {
  return connectionSortDirection.value === SORT_DIRECTION.DESC
})

// 实际生效的排序项:卡片视图用排序菜单选的;表格视图用表头点的,但那一列被隐藏了就退回按目标升序
// (不然列表在按一个看不见的列排,表头上也没有箭头可以点回来)
export const activeSortType = computed(() =>
  useConnectionCard.value ||
  connectionTableColumns.value.includes(SORT_TYPE_COLUMN[connectionSortType.value])
    ? connectionSortType.value
    : SORT_TYPE.HOST,
)
export const activeSortDesc = computed(
  () => activeSortType.value === connectionSortType.value && isDesc.value,
)

export const sortFunctionMap: Record<SORT_TYPE, (a: Connection, b: Connection) => number> = {
  [SORT_TYPE.HOST]: (a: Connection, b: Connection) => {
    return (a.metadata.host || a.metadata.destinationIP).localeCompare(
      b.metadata.host || b.metadata.destinationIP,
    )
  },
  [SORT_TYPE.RULE]: (a: Connection, b: Connection) => {
    return a.rule.localeCompare(b.rule)
  },
  [SORT_TYPE.CHAINS]: (a: Connection, b: Connection) => {
    return getChainsStringFromConnection(a).localeCompare(getChainsStringFromConnection(b))
  },
  [SORT_TYPE.DOWNLOAD]: (a: Connection, b: Connection) => {
    return a.download - b.download
  },
  [SORT_TYPE.DOWNLOAD_SPEED]: (a: Connection, b: Connection) => {
    return a.downloadSpeed - b.downloadSpeed
  },
  [SORT_TYPE.UPLOAD]: (a: Connection, b: Connection) => {
    return a.upload - b.upload
  },
  [SORT_TYPE.UPLOAD_SPEED]: (a: Connection, b: Connection) => {
    return a.uploadSpeed - b.uploadSpeed
  },
  [SORT_TYPE.SOURCE_IP]: (a: Connection, b: Connection) => {
    return a.metadata.sourceIP.localeCompare(b.metadata.sourceIP)
  },
  [SORT_TYPE.TYPE]: (a: Connection, b: Connection) => {
    return (a.metadata.type + a.metadata.network).localeCompare(
      b.metadata.type + b.metadata.network,
    )
  },
  [SORT_TYPE.CONNECT_TIME]: (a: Connection, b: Connection) => {
    return dayjs(a.start).valueOf() - dayjs(b.start).valueOf()
  },
  [SORT_TYPE.INBOUND_USER]: (a: Connection, b: Connection) => {
    return getInboundUserFromConnection(a).localeCompare(getInboundUserFromConnection(b))
  },
}

// 上游 v0.1.423 起连接页只看活跃连接(已关闭的列表不再单独展示,closedConnections 仍留着给负载均衡用量统计)
export const connections = computed(() => activeConnections.value)

let lastQuickFilterError = ''

export const renderConnections = computed(() => {
  const lowerCaseFilter = connectionFilter.value.split(' ').map((f) => f.toLowerCase().trim())

  let regex: RegExp | null = null

  if (quickFilterEnabled.value && quickFilterRegex.value) {
    try {
      regex = new RegExp(quickFilterRegex.value, 'i')
      lastQuickFilterError = ''
    } catch (error) {
      // 正则写错不能让整张连接表崩掉:先不按它过滤,同一条错误只提示一次
      if (lastQuickFilterError !== quickFilterRegex.value) {
        lastQuickFilterError = quickFilterRegex.value
        showNotification({
          content: 'quickFilterRegexInvalid',
          params: { message: error instanceof Error ? error.message : String(error) },
          type: 'alert-warning',
        })
      }
    }
  }

  return connections.value
    .filter((conn) => {
      const metadatas = [
        conn.metadata.host,
        conn.metadata.destinationIP,
        conn.metadata.destinationPort,
        conn.metadata.sourceIP,
        conn.metadata.sourcePort,
        conn.metadata.sniffHost,
        conn.metadata.processPath,
        conn.metadata.type,
        conn.metadata.network,
        conn.chains.join(''),
        conn.rule,
        conn.rulePayload,
      ]

      if (
        sourceIPFilter.value !== null &&
        sourceIPFilter.value.every((i) => i !== conn.metadata.sourceIP)
      ) {
        return false
      }

      if (hideDnsConnections.value && isDnsHijackConnection(conn)) {
        return false
      }

      if (regex) {
        const quickFilterMatch = metadatas.some((i) => regex.test(i))

        if (quickFilterMatch) {
          return false
        }
      }

      if (connectionFilter.value) {
        return lowerCaseFilter.every((i) => metadatas.some((j) => j?.toLowerCase().includes(i)))
      }

      return true
    })
    .sort((a, b) => {
      if (activeSortDesc.value) {
        ;[a, b] = [b, a]
      }
      const sortResult = sortFunctionMap[activeSortType.value](a, b)

      if (sortResult === 0) {
        return a.id.localeCompare(b.id)
      }

      return sortResult
    })
})
