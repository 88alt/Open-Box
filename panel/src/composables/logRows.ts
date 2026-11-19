import { fetchClientAppInfo } from '@/api/clientApp'
import {
  buildLogContexts,
  isFakeIp,
  isLoopbackAddress,
  LOCAL_ROUTER_IP,
  localizeLogMessage,
  parseLogMessage,
  resolveLogTarget,
  splitMihomoRoute,
  type ParsedLog,
} from '@/helper/logMessage'
import { logSeverityLabel } from '@/helper/logSeverity'
import { getDeviceName } from '@/helper/sourceip'
import { logFilter, logLevelFilter, logs } from '@/store/logs'
import { proxyGroupList } from '@/store/proxies'
import type { LogWithSeq } from '@/types'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

export interface LogRow {
  key: string
  log: LogWithSeq
  level: string
  module?: string
  // 出站链(不含最后的策略组)和这条链最后经过的策略组
  outboundChain?: string[]
  routingPolicy?: string
  sourceAddress?: string
  sourceLabel?: string
  targetAddress?: string
  targetFakeIp?: string
  // 目标是域名时,给「改分流」用的域名
  routingTarget?: string
  // 按目标分组用的目标(域名 / IP,不带端口);目标是假 IP 时还原出来的域名
  groupTarget?: string
  fakeIpDomain?: string
  message: string
}

// 链的最后一个是策略组时,它就是这条连接命中的分流策略
const getRoutingPolicy = (chains: string[], groups: Set<string>) => {
  const last = chains[chains.length - 1]

  return last && groups.has(last) ? last : undefined
}

export const useLogRows = () => {
  const { t, locale } = useI18n()
  const groupNames = computed(() => new Set(proxyGroupList.value))
  const routerAddress = ref('')
  const routerName = ref('')

  // 本机路由器的名字:给它起的终端名 > 它在局域网里的名字 > 路由器标识里的名称 > Open-Box
  onMounted(async () => {
    try {
      const info = await fetchClientAppInfo()
      const ipv4 = info.lanAddresses.filter((ip) => !ip.includes(':'))

      routerAddress.value = ipv4[0] ?? info.lanAddresses[0] ?? ''
      routerName.value =
        info.serverInfo?.name?.trim() || info.serverInfoDefaults?.name || 'Open-Box'
    } catch {
      // 拿不到路由器信息就用默认名称
    }
  })

  const localRouterLabel = computed(
    () =>
      getDeviceName(LOCAL_ROUTER_IP) ||
      (routerAddress.value && getDeviceName(routerAddress.value)) ||
      routerName.value ||
      'Open-Box',
  )

  const parsedCache = new WeakMap<LogWithSeq, ParsedLog>()
  const parse = (log: LogWithSeq) => {
    let parsed = parsedCache.get(log)

    if (!parsed) {
      parsed = parseLogMessage(log.payload, log.sourceIP, log.rawPayload)
      parsedCache.set(log, parsed)
    }

    return parsed
  }
  const localize = (message: string) => localizeLogMessage(message, locale.value, t)

  const contexts = computed(() =>
    buildLogContexts(logs.value.filter((log) => !log.probe).map(parse)),
  )

  // 同一条连接的几行日志里,出现过的策略组都一样才算数;不一样就不显示
  const contextPolicies = computed(() => {
    const policies = new Map<string, string | null>()

    for (const log of logs.value) {
      if (log.probe || !log.chains?.length) continue

      const { contextId } = parse(log)
      const policy = getRoutingPolicy(log.chains, groupNames.value)

      if (!contextId || !policy) continue

      if (!policies.has(contextId)) policies.set(contextId, policy)
      else if (policies.get(contextId) !== policy) policies.set(contextId, null)
    }

    return policies
  })

  const rows = computed<LogRow[]>(() => {
    const keyword = logFilter.value
    let regex: RegExp | undefined

    // 输入的不是合法正则时,退回成不区分大小写的普通文本匹配
    try {
      regex = keyword ? new RegExp(keyword, 'i') : undefined
    } catch {
      regex = undefined
    }

    const lowerKeyword = keyword.toLowerCase()
    const isMatch = (text: string) =>
      regex ? regex.test(text) : text.toLowerCase().includes(lowerKeyword)
    const labels = new Map<string, string>()

    return logs.value.flatMap((log) => {
      const level = log.type

      if (logLevelFilter.value && level !== logLevelFilter.value) return []

      const parsed = parse(log)
      const context =
        parsed.contextId && !log.probe ? contexts.value.get(parsed.contextId) : undefined
      const rawSource = parsed.source || context?.source
      const isLocal = log.probe || (rawSource !== undefined && isLoopbackAddress(rawSource))
      const sourceAddress = isLocal ? LOCAL_ROUTER_IP : rawSource

      if (sourceAddress && !labels.has(sourceAddress)) {
        labels.set(sourceAddress, isLocal ? localRouterLabel.value : getDeviceName(sourceAddress))
      }

      const sourceLabel = (sourceAddress && labels.get(sourceAddress)) || undefined
      const target = parsed.target
        ? { host: parsed.target, port: parsed.targetPort }
        : (context?.inboundTarget ?? context?.target)
      const { targetAddress, targetFakeIp } = resolveLogTarget(target, context)
      const routingTarget = targetFakeIp
        ? context?.fakeIpDomain
        : target && !isFakeIp(target.host) && target.host !== '0.0.0.0' && target.host !== '::'
          ? target.host
          : undefined
      const message = localize(parsed.message)
      const groupTarget = log.probe
        ? targetAddress
        : (parsed.module === 'dns' ? parsed.target : undefined) ||
          context?.inboundTarget?.host ||
          parsed.target ||
          context?.target?.host
      const fakeIpDomain =
        context?.fakeIp && groupTarget === context.fakeIp ? context.fakeIpDomain : undefined
      const chain =
        (log.chains?.length ? log.chains : undefined) ??
        parsed.mihomoChain ??
        (parsed.mihomoRoute ? splitMihomoRoute(parsed.mihomoRoute, groupNames.value) : undefined)
      const outboundChain = chain ?? (parsed.outbound ? [parsed.outbound] : undefined)
      const routingPolicy = log.probe
        ? undefined
        : (chain && getRoutingPolicy(chain, groupNames.value)) ||
          (parsed.contextId
            ? (contextPolicies.value.get(parsed.contextId) ?? undefined)
            : undefined)

      if (keyword) {
        const searchable = [
          log.rawPayload,
          log.payload,
          message,
          log.time,
          log.type,
          logSeverityLabel(level, t),
          sourceAddress ?? '',
          sourceLabel ?? '',
          targetAddress ?? '',
          targetFakeIp ?? '',
          groupTarget ?? '',
          fakeIpDomain ?? '',
          ...(log.chains ?? []),
        ]

        if (!searchable.some(isMatch)) return []
      }

      return [
        {
          key: `log:${log.seq}`,
          log,
          level,
          module: parsed.module,
          outboundChain:
            routingPolicy && outboundChain?.[outboundChain.length - 1] === routingPolicy
              ? outboundChain.slice(0, -1)
              : outboundChain,
          routingPolicy,
          sourceAddress,
          sourceLabel,
          targetAddress,
          targetFakeIp,
          routingTarget,
          groupTarget,
          fakeIpDomain,
          message,
        },
      ]
    })
  })

  return { rows }
}
