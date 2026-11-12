// 连接页的分组:按终端或按目标把筛选、排序后的连接分成组,同一时刻只展开一个组,
// 展开的组里还能再分子组(不分 / 按分流 / 按另一个维度)。结构对齐上游 v0.1.423,自己重写。
import { CONNECTION_GROUP_MODE, SORT_TYPE, type CONNECTION_SUBGROUP_MODE } from '@/constant'
import { getDeviceLabel } from '@/helper/sourceip'
import type { Connection } from '@/types'
import { last } from 'lodash'
import { computed, reactive, shallowRef, watch } from 'vue'
import {
  activeSortDesc,
  activeSortType,
  connectionFilter,
  connectionGroupMode,
  hideDnsConnections,
  quickFilterEnabled,
  quickFilterRegex,
  renderConnections,
  sortFunctionMap,
  sourceIPFilter,
} from './connections'

export interface ConnectionGroup {
  key: string
  label: string
  // 终端分组:组名被改成了名字时,这里留着原始 IP(显示在名字下面)
  sourceIP?: string
  // 终端分组:这个组的 IP(有它才能重命名)
  deviceIP?: string
  // 目标分组:这个组的目标(域名或 IP)
  target?: string
  connections: Connection[]
  downloadSpeed: number
  uploadSpeed: number
  download: number
  upload: number
}

export type ConnectionGroupItem =
  | {
      kind: 'group'
      key: string
      sectionKey: string
      group: ConnectionGroup
      expanded: boolean
      subgroupMode: CONNECTION_SUBGROUP_MODE
    }
  | {
      kind: 'subgroup'
      mode: CONNECTION_SUBGROUP_MODE
      key: string
      sectionKey: string
      group: ConnectionGroup
      expanded: boolean
    }
  | { kind: 'connection'; key: string; sectionKey: string; connection: Connection }

// 目标分组按域名(没有域名用嗅探到的域名,再没有用目的 IP)分,不带端口
const normalizeTarget = ({ metadata }: Connection) =>
  (metadata.host || metadata.sniffHost || metadata.destinationIP || '')
    .trim()
    .toLowerCase()
    .replace(/\.+$/, '')

// 分流子分组的键:连接经过的策略组(链上最后一个,链只有一段时没有策略组)
const getRoutingPolicy = (conn: Connection) =>
  conn.chains.length > 1 ? (last(conn.chains) ?? '') : ''

const getGroupKey = (conn: Connection, mode: CONNECTION_SUBGROUP_MODE) => {
  if (mode === 'routing') return getRoutingPolicy(conn)
  if (mode === CONNECTION_GROUP_MODE.DEVICE) return conn.metadata.sourceIP || ''

  return normalizeTarget(conn)
}

const metricKeys: Partial<
  Record<SORT_TYPE, 'download' | 'downloadSpeed' | 'upload' | 'uploadSpeed'>
> = {
  [SORT_TYPE.DOWNLOAD]: 'download',
  [SORT_TYPE.DOWNLOAD_SPEED]: 'downloadSpeed',
  [SORT_TYPE.UPLOAD]: 'upload',
  [SORT_TYPE.UPLOAD_SPEED]: 'uploadSpeed',
}

const buildGroups = (conns: Connection[], mode: CONNECTION_SUBGROUP_MODE): ConnectionGroup[] => {
  const groups = new Map<string, ConnectionGroup>()
  const isDevice = mode === CONNECTION_GROUP_MODE.DEVICE

  for (const conn of conns) {
    const key = getGroupKey(conn, mode)
    let group = groups.get(key)

    if (!group) {
      const label = mode === 'routing' ? key : isDevice ? getDeviceLabel(key) : key || '—'

      group = {
        key,
        label,
        sourceIP: isDevice && label !== key ? key : undefined,
        deviceIP: isDevice && key ? key : undefined,
        target: mode === CONNECTION_GROUP_MODE.TARGET ? key : undefined,
        connections: [],
        downloadSpeed: 0,
        uploadSpeed: 0,
        download: 0,
        upload: 0,
      }
      groups.set(key, group)
    }
    group.connections.push(conn)
    group.downloadSpeed += conn.downloadSpeed
    group.uploadSpeed += conn.uploadSpeed
    group.download += conn.download
    group.upload += conn.upload
  }

  // 排序沿用连接页当前的排序项:流量 / 速度按组内合计,按目标 / 按来源 IP 排时按组名,其它按组里第一条连接
  const sortType = activeSortType.value
  const metric = metricKeys[sortType]
  const direction = activeSortDesc.value ? -1 : 1
  const byLabel =
    (sortType === SORT_TYPE.HOST && mode === CONNECTION_GROUP_MODE.TARGET) ||
    (sortType === SORT_TYPE.SOURCE_IP && isDevice)

  return [...groups.values()].sort((a, b) => {
    const diff = metric
      ? a[metric] - b[metric]
      : byLabel
        ? a.label.localeCompare(b.label)
        : sortFunctionMap[sortType](a.connections[0], b.connections[0])

    return diff * direction || a.key.localeCompare(b.key)
  })
}

const groupId = (group: ConnectionGroup) => JSON.stringify([connectionGroupMode.value, group.key])
const subgroupId = (parentId: string, mode: CONNECTION_SUBGROUP_MODE, group: ConnectionGroup) =>
  JSON.stringify([parentId, mode, group.key])

// 当前展开的组(只记它的键和名字,数据每次从最新的分组里取;组消失了也留着,让展开的内容不会一下子没掉)
const expandedGroup = shallowRef<ConnectionGroup | null>(null)
const expandedSubgroupId = shallowRef<string | null>(null)
const subgroupModes = reactive(new Map<string, CONNECTION_SUBGROUP_MODE>())

export const collapseConnectionGroup = () => {
  expandedSubgroupId.value = null
  expandedGroup.value = null
}

export const setConnectionSubgroupMode = (id: string, mode: CONNECTION_SUBGROUP_MODE) => {
  if ((subgroupModes.get(id) ?? 'none') === mode) return

  expandedSubgroupId.value = null
  subgroupModes.set(id, mode)
}

watch(
  connectionGroupMode,
  () => {
    collapseConnectionGroup()
    subgroupModes.clear()
  },
  { flush: 'sync' },
)

const allGroups = computed(() => buildGroups(renderConnections.value, connectionGroupMode.value))

// 筛选条件变了之后,展开的组如果已经没有连接了就收起来
watch(
  [connectionFilter, sourceIPFilter, quickFilterEnabled, quickFilterRegex, hideDnsConnections],
  () => {
    const expanded = expandedGroup.value

    if (expanded && !allGroups.value.some((g) => g.key === expanded.key)) {
      collapseConnectionGroup()
    }
  },
)

const liveExpanded = computed(() => {
  const expanded = expandedGroup.value

  return expanded ? (allGroups.value.find((g) => g.key === expanded.key) ?? expanded) : null
})

// 展开着的时候组的顺序固定成「看到它们的先后」:速度在变,按速度排的列表会一直跳,点开的组就跟着跑了
const seenKeys = new Set<string>()
let lastFrozenMode: CONNECTION_GROUP_MODE | null = null
const stableGroups = shallowRef<ConnectionGroup[]>([])

watch(
  [() => (expandedGroup.value ? connectionGroupMode.value : null), allGroups, liveExpanded],
  ([mode, groups, live]) => {
    if (!mode) {
      lastFrozenMode = null
      seenKeys.clear()
      stableGroups.value = []

      return
    }
    if (mode !== lastFrozenMode) {
      seenKeys.clear()
      lastFrozenMode = mode
    }
    const list = live && !groups.includes(live) ? [...groups, live] : groups
    const byKey = new Map(list.map((g) => [g.key, g]))

    for (const key of byKey.keys()) seenKeys.add(key)
    stableGroups.value = [...seenKeys].flatMap((key) => byKey.get(key) ?? [])
  },
  { flush: 'sync' },
)

export const connectionGroups = computed(() =>
  expandedGroup.value ? stableGroups.value : allGroups.value,
)

const expandedId = computed(() => (liveExpanded.value ? groupId(liveExpanded.value) : null))
const expandedSubgroupMode = computed<CONNECTION_SUBGROUP_MODE>(
  () => (expandedId.value && subgroupModes.get(expandedId.value)) || 'none',
)
const subgroups = computed(() =>
  liveExpanded.value && expandedSubgroupMode.value !== 'none'
    ? buildGroups(liveExpanded.value.connections, expandedSubgroupMode.value)
    : [],
)

export const connectionGroupItems = computed<ConnectionGroupItem[]>(() => {
  const items: ConnectionGroupItem[] = []
  const pushConnections = (conns: Connection[], sectionKey: string) => {
    for (const connection of conns) {
      items.push({ kind: 'connection', key: `connection:${connection.id}`, sectionKey, connection })
    }
  }

  for (const group of connectionGroups.value) {
    const id = groupId(group)
    const expanded = expandedId.value === id
    const mode = subgroupModes.get(id) ?? 'none'

    items.push({ kind: 'group', key: id, sectionKey: id, group, expanded, subgroupMode: mode })
    if (!expanded) continue
    if (mode === 'none') {
      pushConnections(group.connections, id)
      continue
    }
    for (const sub of subgroups.value) {
      const subId = subgroupId(id, mode, sub)
      const subExpanded = expandedSubgroupId.value === subId

      items.push({
        kind: 'subgroup',
        mode,
        key: subId,
        sectionKey: id,
        group: sub,
        expanded: subExpanded,
      })
      if (subExpanded) pushConnections(sub.connections, id)
    }
  }

  return items
})

// 点组头 / 子组头:组同一时刻只展开一个,子组同一时刻也只展开一个
export const toggleConnectionGroupItem = (item: ConnectionGroupItem) => {
  if (item.kind === 'connection') return
  if (item.kind === 'subgroup') {
    expandedSubgroupId.value = expandedSubgroupId.value === item.key ? null : item.key

    return
  }
  if (expandedGroup.value?.key === item.group.key) {
    collapseConnectionGroup()

    return
  }
  expandedSubgroupId.value = null
  expandedGroup.value = {
    ...item.group,
    connections: [],
    downloadSpeed: 0,
    uploadSpeed: 0,
    download: 0,
    upload: 0,
  }
}

export const connectionGroupCount = computed(() => connectionGroups.value.length)
