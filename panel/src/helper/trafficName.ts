// 流量表里"名称"后面那个灰字:DHCP 主机名,或者"本机 · WAN pppoe-wan0"这种路由器自身地址的
// 标注。主表和下钻表共用,别各写一遍。
import type { OpenboxTrafficDim, OpenboxTrafficRow } from '@/api/openbox'
import { i18n } from '@/i18n'
import { failoverLaneOfTag, isFailoverInternalTag } from '@/store/openboxFailover'
import { getNowProxyNodeName, outboundDisplayName, proxyMap } from '@/store/proxies'

export const trafficRowNote = (row: OpenboxTrafficRow, t: (key: string, params?: Record<string, unknown>) => string): string => {
  if (row.self) {
    if (row.self.kind === 'lan') return t('trafficSelfLan', { iface: row.self.iface })
    if (row.self.kind === 'wan') return t('trafficSelfWan', { iface: row.self.iface })
    return t('trafficSelfOther', { iface: row.self.iface })
  }
  return row.name || ''
}

// ---- 流量表里的节点名 ----
// 服务端把较早的明细合并成一行,键固定是 ~other;界面显示成「其他(较早的数据已合并)」。
// 节点那一维里还会出现「组」或故障转移页签的内部 tag(旧记录),显示时换成它当前实际走的那个节点,
// 同一个节点拆成的多行合并成一行(与上游 v0.1.338 一致)
const TRAFFIC_OTHER_KEY = '~other'
const isGroupKey = (key: string) => Array.isArray(proxyMap.value[key]?.all)
const isLaneKey = (key: string) => isFailoverInternalTag(key)

const resolveNodeKey = (key: string): string => {
  if (!isGroupKey(key) && !isLaneKey(key)) return key
  const now = getNowProxyNodeName(key)
  if (now && now !== key && !isGroupKey(now) && !isLaneKey(now)) return now
  return (isLaneKey(key) ? failoverLaneOfTag(key)?.lane.members?.[0] : proxyMap.value[key]?.all?.[0]) || key
}

// 终端设备那一维里,没有源 IP 的流量记录键是空串,显示成「来源未知」;访问目标原样显示;其余按节点名显示
export const trafficKeyLabel = (key: string, kind?: OpenboxTrafficDim): string => {
  if (key === TRAFFIC_OTHER_KEY) return i18n.global.t('trafficOtherMerged')
  if (kind === 'client') return key || i18n.global.t('trafficUnknownClient')
  if (kind === 'host') return key
  return outboundDisplayName(resolveNodeKey(key))
}

// 「来源未知」那一行悬停时说明为什么认不出终端;其余行不设 title
export const trafficKeyTitle = (key: string, kind?: OpenboxTrafficDim): string | undefined =>
  kind === 'client' && !key ? i18n.global.t('trafficUnknownClientHint') : undefined

export const mergeTrafficNodeRows = (rows: OpenboxTrafficRow[]): OpenboxTrafficRow[] => {
  if (!rows.some((row) => isGroupKey(row.key) || isLaneKey(row.key))) return rows
  const merged = new Map<string, OpenboxTrafficRow>()
  for (const row of rows) {
    const key = resolveNodeKey(row.key)
    const prev = merged.get(key)
    if (!prev) {
      merged.set(key, { ...row, key })
      continue
    }
    merged.set(key, { ...prev, up: prev.up + row.up, down: prev.down + row.down, conns: prev.conns + row.conns })
  }
  return [...merged.values()].sort((a, b) => b.up + b.down - (a.up + a.down) || a.key.localeCompare(b.key))
}
