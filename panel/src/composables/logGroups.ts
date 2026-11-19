// 日志页的分组:按终端或按目标把日志分成组,同一时刻只展开一个组,展开的组里还能再分子组
// (不分 / 按分流 / 按另一个维度),子组同一时刻也只展开一个。结构对齐上游 v0.1.458,自己重写。
import type { LogRow } from '@/composables/logRows'
import { CONNECTION_GROUP_MODE } from '@/constant'
import {
  logGroupMode,
  logGroupSelection,
  type LogGroupSelection,
  type LogSubgroupMode,
} from '@/store/logs'
import { computed, shallowRef, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'

export interface LogHeading {
  label: string
  // 名字下面的原始地址(终端的 IP、假 IP 对应的域名分组里的假 IP)
  address?: string
  // 终端分组:这个组的 IP(有它才能重命名)
  sourceAddress?: string
  // 目标分组:这个组的域名(有它才能改分流)
  routingTarget?: string
  count: number
}

export type LogItem =
  | (LogRow & { kind: 'log'; groupKey?: string })
  | (LogHeading & {
      kind: 'group'
      key: string
      sectionKey: string
      expanded: boolean
      subgroupMode?: LogSubgroupMode
      routingPolicy?: undefined
    })
  | (LogHeading & {
      kind: 'subgroup'
      key: string
      sectionKey: string
      expanded: boolean
      subgroupMode?: undefined
      routingPolicy?: string
    })

interface Bucket {
  entries: LogRow[]
  domain?: string | null
  routingTarget?: string | null
}

const keyOf = (row: LogRow, mode: LogSubgroupMode) =>
  (mode === 'routing'
    ? row.routingPolicy
    : mode === CONNECTION_GROUP_MODE.DEVICE
      ? row.sourceAddress
      : row.groupTarget) || ''

const bucketize = (rows: LogRow[], mode: LogSubgroupMode) => {
  const buckets = new Map<string, Bucket>()

  for (const row of rows) {
    const key = keyOf(row, mode)
    const bucket = buckets.get(key) ?? { entries: [] }

    bucket.entries.push(row)
    if (mode === CONNECTION_GROUP_MODE.TARGET) {
      // 组里的日志对应的域名 / 改分流域名不一致时记成 null,不显示
      if (row.fakeIpDomain) {
        if (bucket.domain === undefined) bucket.domain = row.fakeIpDomain
        else if (bucket.domain !== row.fakeIpDomain) bucket.domain = null
      }
      if (row.routingTarget) {
        if (bucket.routingTarget === undefined) bucket.routingTarget = row.routingTarget
        else if (bucket.routingTarget !== row.routingTarget) bucket.routingTarget = null
      }
    }
    buckets.set(key, bucket)
  }

  // 没有这一项的日志(键是空串)放最后
  const empty = buckets.get('')

  if (empty) {
    buckets.delete('')
    buckets.set('', empty)
  }

  return buckets
}

// 展开着的时候组的顺序固定成「看到它们的先后」,免得新日志进来把展开的组挤走
const useStableOrder = <T>(
  list: Ref<[string, T][]>,
  lock: Ref<string | null>,
): Ref<[string, T][]> => {
  const seen = new Set<string>()
  const stable = shallowRef<[string, T][]>([])

  watch(
    [list, lock],
    ([items, locked]) => {
      if (!locked) {
        seen.clear()
        stable.value = items

        return
      }

      const byKey = new Map(items)

      for (const key of byKey.keys()) seen.add(key)
      stable.value = [...seen].flatMap((key) =>
        byKey.has(key) ? [[key, byKey.get(key) as T]] : [],
      )
    },
    { immediate: true, flush: 'sync' },
  )

  return stable
}

export const useLogGroups = (rows: Ref<LogRow[]>) => {
  const { t } = useI18n()
  const mode = logGroupMode
  const selection = computed<LogGroupSelection | undefined>(() =>
    mode.value === 'none' ? undefined : logGroupSelection.value[mode.value],
  )
  const subgroupModeMemory = new Map<string, LogSubgroupMode>()

  watch(mode, () => subgroupModeMemory.clear(), { flush: 'sync' })

  const groups = computed(() =>
    mode.value === 'none' ? new Map<string, Bucket>() : bucketize(rows.value, mode.value),
  )
  const expandedGroup = computed(() => {
    const key = selection.value?.group

    return key !== undefined && groups.value.has(key) ? key : null
  })
  const orderedGroups = useStableOrder(
    computed(() => [...groups.value]),
    computed(() => (expandedGroup.value === null ? null : mode.value)),
  )
  const groupId = (key: string) => `group:${key}`
  const subgroupId = (parent: string, sub: LogSubgroupMode, key: string) =>
    JSON.stringify([parent, sub, key])
  const expandedGroupId = computed(() =>
    expandedGroup.value === null ? null : groupId(expandedGroup.value),
  )
  const subgroupMode = computed<LogSubgroupMode>(() => selection.value?.subgroupMode ?? 'none')
  const subgroups = computed(() => {
    if (expandedGroup.value === null || subgroupMode.value === 'none') return []

    const bucket = groups.value.get(expandedGroup.value)

    return bucket ? [...bucketize(bucket.entries, subgroupMode.value)] : []
  })
  const expandedSubgroup = computed(() => {
    const parent = expandedGroupId.value
    const id = selection.value?.subgroup

    return parent === null || !id
      ? null
      : subgroups.value.some(([key]) => subgroupId(parent, subgroupMode.value, key) === id)
        ? parent
        : null
  })
  const orderedSubgroups = useStableOrder(
    computed(() => subgroups.value),
    expandedSubgroup,
  )

  const heading = (m: LogSubgroupMode, key: string, bucket: Bucket): LogHeading => {
    const label =
      m === 'routing'
        ? key || t('connectionRoutingUnmatched')
        : m === CONNECTION_GROUP_MODE.DEVICE
          ? bucket.entries[0]?.sourceLabel || key || t('logUnknownDevice')
          : bucket.domain || key || t('logUnknownTarget')

    return {
      label,
      address:
        (m === CONNECTION_GROUP_MODE.DEVICE || bucket.domain) && key && key !== label
          ? key
          : undefined,
      sourceAddress: m === CONNECTION_GROUP_MODE.DEVICE && key ? key : undefined,
      routingTarget:
        m === CONNECTION_GROUP_MODE.TARGET && key ? (bucket.routingTarget ?? undefined) : undefined,
      count: bucket.entries.length,
    }
  }

  const items = computed<LogItem[]>(() => {
    const current = mode.value

    if (current === 'none') return rows.value.map((row) => ({ ...row, kind: 'log' as const }))

    const out: LogItem[] = []

    for (const [key, bucket] of orderedGroups.value) {
      const id = groupId(key)
      const expanded = expandedGroup.value === key
      const sub = subgroupMode.value

      out.push({
        kind: 'group',
        key: id,
        sectionKey: id,
        ...heading(current, key, bucket),
        expanded,
        subgroupMode: expanded ? sub : undefined,
      })
      if (!expanded) continue
      if (sub === 'none') {
        out.push(...bucket.entries.map((row) => ({ ...row, kind: 'log' as const, groupKey: id })))
        continue
      }
      for (const [subKey, subBucket] of orderedSubgroups.value) {
        const subId = subgroupId(id, sub, subKey)
        const subExpanded = selection.value?.subgroup === subId

        out.push({
          kind: 'subgroup',
          key: subId,
          sectionKey: id,
          ...heading(sub, subKey, subBucket),
          routingPolicy: sub === 'routing' && subKey ? subKey : undefined,
          expanded: subExpanded,
        })
        if (subExpanded) {
          out.push(
            ...subBucket.entries.map((row) => ({ ...row, kind: 'log' as const, groupKey: id })),
          )
        }
      }
    }

    return out
  })

  const toggle = (item: LogItem) => {
    const current = mode.value

    if (current === 'none' || item.kind === 'log') return

    const selected = selection.value

    if (item.kind === 'subgroup') {
      if (selected && item.sectionKey === expandedGroupId.value) {
        logGroupSelection.value[current] = {
          ...selected,
          subgroup: item.expanded ? null : item.key,
        }
      }

      return
    }
    if (selected) subgroupModeMemory.set(groupId(selected.group), selected.subgroupMode)
    if (item.expanded) {
      delete logGroupSelection.value[current]

      return
    }
    logGroupSelection.value[current] = {
      group: item.key.slice('group:'.length),
      subgroupMode: subgroupModeMemory.get(item.key) ?? 'none',
      subgroup: null,
    }
  }

  const setSubgroupMode = (sectionKey: string, next: LogSubgroupMode) => {
    const current = mode.value
    const selected = selection.value

    if (current === 'none' || !selected || sectionKey !== expandedGroupId.value) return
    if (selected.subgroupMode === next) return
    logGroupSelection.value[current] = { group: selected.group, subgroupMode: next, subgroup: null }
  }

  return { items, toggle, setSubgroupMode }
}
