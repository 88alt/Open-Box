import { disconnectByIdAPI, fetchConnectionsAPI } from '@/api'
import {
  CONNECTION_GROUP_MODE,
  PROXY_CHAIN_DIRECTION,
  SORT_DIRECTION,
  SORT_TYPE,
  SORT_TYPE_COLUMN,
  type CONNECTION_VIEW_MODE,
} from '@/constant'
import {
  getHostFromConnection,
  getInboundUserFromConnection,
  getNetworkTypeFromConnection,
} from '@/helper'
import { showNotification } from '@/helper/notification'
import { getDeviceLabel } from '@/helper/sourceip'
import type { Connection, ConnectionRawMessage } from '@/types'
import { useStorage, watchOnce } from '@vueuse/core'
import dayjs from 'dayjs'
import { computed, ref, watch } from 'vue'
import { initAggregatedDataMap, saveConnectionHistory } from './connHistory'
import { siteSetNames } from './openboxSiteSets'
import {
  autoDisconnectIdleUDP,
  autoDisconnectIdleUDPTime,
  connectionCardLines,
  connectionTableColumns,
  proxyChainDirection,
  useConnectionCard,
} from './settings'

// 分组方式只在本次打开页面期间有效(换方式会收起已展开的组)
export const connectionGroupMode = ref<CONNECTION_VIEW_MODE>(CONNECTION_GROUP_MODE.DEVICE)
export const connectionSortType = useStorage<SORT_TYPE>(
  'config/connection-sort-type',
  SORT_TYPE.DOWNLOAD_SPEED,
)
export const connectionSortDirection = useStorage<SORT_DIRECTION>(
  'config/connection-sort-direction',
  SORT_DIRECTION.DESC,
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

// 排序菜单的选项:当前视图里显示着的列(卡片视图按卡片布局从上到下、从左到右,表格视图按列顺序),
// 没有对应排序项的列跳过(与上游 v0.1.488 一致)
export const sortTypeOptions = computed(() => {
  const columns: string[] = useConnectionCard.value
    ? connectionCardLines.value.flat(2)
    : connectionTableColumns.value
  const shown = columns.length ? columns : [SORT_TYPE_COLUMN[SORT_TYPE.HOST]]

  return [...new Set(shown)].flatMap((column) =>
    Object.values(SORT_TYPE).filter((type) => SORT_TYPE_COLUMN[type] === column),
  )
})

// 存下来的排序项在当前视图里还能不能用:对应的列没显示就不能用;按「链路」排而只显示了「出站节点」时改按出站节点
const selectedSortType = computed(() => {
  const options = sortTypeOptions.value
  const stored = connectionSortType.value

  if (options.includes(stored)) return stored
  if (stored === SORT_TYPE.CHAINS && options.includes(SORT_TYPE.OUTBOUND)) return SORT_TYPE.OUTBOUND

  return undefined
})

// 实际生效的排序项:存的那项不能用时,退回进站速率,连它也没显示就取菜单里第一项
export const activeSortType = computed({
  get: () => {
    const options = sortTypeOptions.value

    return (
      selectedSortType.value ??
      (options.includes(SORT_TYPE.DOWNLOAD_SPEED) ? SORT_TYPE.DOWNLOAD_SPEED : options[0]) ??
      SORT_TYPE.DOWNLOAD_SPEED
    )
  },
  set: (type: SORT_TYPE) => {
    // 正处在退回状态时换排序项,方向从降序开始
    if (!selectedSortType.value) connectionSortDirection.value = SORT_DIRECTION.DESC
    connectionSortType.value = type
  },
})
export const activeSortDesc = computed(
  () => !(selectedSortType.value && connectionSortDirection.value === SORT_DIRECTION.ASC),
)
export const toggleSortDirection = () => {
  const wasDesc = activeSortDesc.value

  connectionSortType.value = activeSortType.value
  connectionSortDirection.value = wasDesc ? SORT_DIRECTION.ASC : SORT_DIRECTION.DESC
}

// 每个排序项拿什么来比:文字列按表格里显示的内容比,数值和时间按数值比
export const sortValueMap: Record<SORT_TYPE, (conn: Connection) => string | number> = {
  [SORT_TYPE.HOST]: (conn) => getHostFromConnection(conn),
  [SORT_TYPE.CHAINS]: ({ chains }) =>
    (proxyChainDirection.value === PROXY_CHAIN_DIRECTION.REVERSE
      ? chains
      : [...chains].reverse()
    ).join(' → '),
  // 出站节点:先比命中的分流策略,再比节点链
  [SORT_TYPE.OUTBOUND]: ({ chains }) => {
    const last = chains[chains.length - 1]
    const policy = last && siteSetNames.value.has(last) ? last : undefined
    const nodes = policy ? chains.slice(0, -1) : [...chains]

    if (proxyChainDirection.value !== PROXY_CHAIN_DIRECTION.REVERSE) nodes.reverse()

    return [policy, nodes.join(' → ')].filter(Boolean).join(' · ')
  },
  [SORT_TYPE.RULE]: ({ rule, rulePayload }) =>
    rulePayload ? `${rule}: ${rulePayload}` : rule || '',
  [SORT_TYPE.TYPE]: (conn) => getNetworkTypeFromConnection(conn),
  [SORT_TYPE.CONNECT_TIME]: (conn) => dayjs(conn.start).valueOf() || 0,
  [SORT_TYPE.DOWNLOAD]: (conn) => conn.download,
  [SORT_TYPE.DOWNLOAD_SPEED]: (conn) => conn.downloadSpeed,
  [SORT_TYPE.UPLOAD]: (conn) => conn.upload,
  [SORT_TYPE.UPLOAD_SPEED]: (conn) => conn.uploadSpeed,
  [SORT_TYPE.SOURCE_IP]: (conn) => getDeviceLabel(conn.metadata.sourceIP),
  [SORT_TYPE.INBOUND_USER]: (conn) => getInboundUserFromConnection(conn),
}

export const compareSortValues = (a: string | number, b: string | number) =>
  typeof a === 'number' && typeof b === 'number' ? a - b : String(a).localeCompare(String(b))

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
      const getValue = sortValueMap[activeSortType.value]
      const direction = activeSortDesc.value ? -1 : 1

      // 值相同时固定按连接 id 升序,不随排序方向翻转
      return compareSortValues(getValue(a), getValue(b)) * direction || a.id.localeCompare(b.id)
    })
})
