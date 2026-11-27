import { NOT_CONNECTED, PROXY_SORT_TYPE } from '@/constant'
import { isProxyGroup } from '@/helper'
import { getLatencyByName, proxiesFilter } from '@/store/proxies'
import { nodeScore } from '@/store/nodeScore'
import { hideUnavailableProxies, proxySortType, useSmartGroupSort } from '@/store/settings'
import { smartOrderMap } from '@/store/smart'
import { computed, type ComputedRef } from 'vue'

export function useRenderProxies(proxies: ComputedRef<string[]>, proxyGroup?: string) {
  const renderProxies = computed(() => {
    return getRenderProxies(proxies.value, proxyGroup)
  })
  const availableProxies = computed(() => {
    return renderProxies.value.filter(
      (proxy) => getLatencyByName(proxy, proxyGroup) !== NOT_CONNECTED,
    ).length
  })

  const proxiesCount = computed(() => {
    const all = proxies.value.length

    return `${availableProxies.value}/${all}`
  })

  return {
    renderProxies,
    proxiesCount,
  }
}

const getRenderProxies = (proxies: string[], groupName?: string) => {
  const latencyMap = new Map<string, number>()

  proxies = [...proxies]
  proxies.forEach((name) => {
    latencyMap.set(name, getLatencyByName(name, groupName))
  })

  if (hideUnavailableProxies.value) {
    proxies = proxies.filter((name) => {
      return isProxyGroup(name) || latencyMap.get(name)! > NOT_CONNECTED
    })
  }

  if (proxiesFilter.value) {
    const filters = proxiesFilter.value.split(' ').map((f) => f.toLowerCase().trim())

    proxies = proxies.filter((name) => {
      name = name.toLowerCase()
      return filters.every((f) => name.includes(f))
    })
  }

  if (useSmartGroupSort.value && smartOrderMap.value[groupName!]) {
    const orderMap = smartOrderMap.value[groupName!]

    return proxies.sort((a, b) => {
      const ia = orderMap[a] ?? Number.MAX_SAFE_INTEGER
      const ib = orderMap[b] ?? Number.MAX_SAFE_INTEGER
      return ia - ib
    })
  }

  if (proxySortType.value === PROXY_SORT_TYPE.DEFAULT) {
    return proxies
  }

  const proxyGroups: string[] = []
  const proxyNodes: string[] = []

  proxies.forEach((proxy) => {
    if (isProxyGroup(proxy)) {
      proxyGroups.push(proxy)
    } else {
      proxyNodes.push(proxy)
    }
  })

  const getLatencyForSort = (name: string) => {
    const latency = latencyMap.get(name)!
    return latency === 0 ? Infinity : latency
  }
  // 上游 v0.1.494:按节点评分排序。没有分数的节点排在最后(两种方向都是),
  // 这样「升序」看的是最差的、但不会把没测过的混在最前面
  const getScoreForSort = (name: string) => {
    const s = nodeScore(name)?.score
    return s == null || !Number.isFinite(s) ? null : s
  }
  const cmpScore = (prev: string, next: string, desc: boolean) => {
    const a = getScoreForSort(prev)
    const b = getScoreForSort(next)
    if (a == null && b == null) return 0
    if (a == null) return 1
    if (b == null) return -1
    return desc ? b - a : a - b
  }
  const sortFuncMap = {
    [PROXY_SORT_TYPE.NAME_ASC]: (prev: string, next: string) => prev.localeCompare(next),
    [PROXY_SORT_TYPE.NAME_DESC]: (prev: string, next: string) => next.localeCompare(prev),
    [PROXY_SORT_TYPE.LATENCY_ASC]: (prev: string, next: string) =>
      getLatencyForSort(prev) - getLatencyForSort(next),
    [PROXY_SORT_TYPE.LATENCY_DESC]: (prev: string, next: string) =>
      getLatencyForSort(next) - getLatencyForSort(prev),
    [PROXY_SORT_TYPE.SCORE_ASC]: (prev: string, next: string) => cmpScore(prev, next, false),
    [PROXY_SORT_TYPE.SCORE_DESC]: (prev: string, next: string) => cmpScore(prev, next, true),
  }
  const sortFunc = sortFuncMap[proxySortType.value]

  return proxyGroups.concat(proxyNodes.sort(sortFunc))
}
