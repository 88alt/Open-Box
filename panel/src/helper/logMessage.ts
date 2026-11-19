// 日志文本解析:从一行内核日志里拆出 来源 / 目标 / 端口 / 出站链 / 模块,并把同一条连接的几行日志
// 合在一起还原 fakeIP 对应的域名。行为对齐上游 v0.1.458。
import * as ipaddr from 'ipaddr.js'

// 本机路由器自己发出的连接(节点测速、内核自检等)统一记成这个来源
export const LOCAL_ROUTER_IP = '127.0.0.1'

export interface HostPort {
  host: string
  port?: string
}

export interface ParsedLog {
  contextId?: string
  module?: string
  message: string
  source?: string
  target?: string
  targetPort?: string
  outbound?: string
  fakeIpDomain?: string
  mihomoChain?: string[]
  mihomoRoute?: string
  dialFailure: boolean
  inboundTarget: boolean
}

export interface LogContext {
  source?: string
  fakeIpDomain?: string
  fakeIp?: string
  inboundTarget?: HostPort
  target?: HostPort
}

const FAKE_IP_RANGES = ['198.19.0.0/16', 'fc00::/18'].map((cidr) => ipaddr.parseCIDR(cidr))

export const isFakeIp = (host: string) => {
  if (!ipaddr.isValid(host)) return false

  const addr = ipaddr.parse(host)

  return FAKE_IP_RANGES.some((range) => addr.kind() === range[0].kind() && addr.match(range))
}

const isLoopback = (host: string) =>
  ipaddr.isValid(host) && ipaddr.process(host).range() === 'loopback'

export const isLoopbackAddress = isLoopback

// 去掉 IPv6 的方括号和域名末尾的点,统一小写
const cleanHost = (host: string) =>
  host
    .replace(/^\[|\]$/g, '')
    .replace(/\.$/, '')
    .toLowerCase()

// 只认合法 IP;未指定地址(0.0.0.0 / ::)不算来源
const normalizeIp = (host?: string) => {
  if (typeof host !== 'string' || !ipaddr.isValid(host)) return undefined

  const addr = ipaddr.process(host)

  return addr.range() === 'unspecified' ? undefined : addr.toString()
}

export const formatHostPort = (host: string, port?: string | number | null) =>
  port ? `${host.includes(':') ? `[${host}]` : host}:${port}` : host

// "[1234 5.6s] " 前缀里的连接 ID,同一条连接的多行日志共用
const CONTEXT_PREFIX = /^\[(\d+) [^\]]+\] /
const MODULE_PREFIX = /^([a-z][\w/-]*(?:\[[^\r\n]*?\])?): /i
const OUTBOUND_MODULE = /^outbound\/[\w-]+\[(.+)\]$/
const CONNECTION_LINE =
  /^(inbound|outbound) (?:redirect )?(?:packet )?connection (from|to)\s+(\[[\da-f:.]+\]|[\w.-]+)(?:\s+\([^\r\n]*?\)\s+)?(?::(\d+))?(?=$|[\s:,])/i
const DIAL_LINE =
  /^open (?:packet )?connection to (\[[\da-f:.]+\]|[\w.-]+)(?:\s+\([^\r\n]*?\)\s+)?:([0-9]+)(?= using outbound\/|: |$)/i
const DNS_LINE = /^exchange (?:failed for )?([\w.-]+) (?:\d+ )?IN\s/i
const FAKE_IP_LINE = /^found fakeip domain: ([\w.-]+)$/i
// mihomo 的连接日志: [TCP] 来源 --> 目标:端口 match 规则 using 出站
const MIHOMO_LINE =
  /^\[(?:TCP|UDP|ICMP)\] (.+?) --> (\[[^\]\s]+\]|[^:\s]+):(\d+) (?:(?:match .+? |doesn't match any rule )?using (.+)|error: .+)$/
// 后端在 mihomo 日志末尾补的出站链
const MIHOMO_CHAIN_SUFFIX = / \[openbox-chain:(\[.*\])\]$/
const MIHOMO_SOURCE = /(?:^| )(?:(\[[^\]\s]+\]|[\d.]+):\d+(?:\([^\r\n]*\))?|(mihomo))$/

const parseMihomoLine = (text: string) => {
  const suffix = MIHOMO_CHAIN_SUFFIX.exec(text)
  const matched = MIHOMO_LINE.exec(suffix ? text.slice(0, suffix.index) : text)

  if (!matched) return undefined

  const [, origin, target, port, using] = matched
  const sourceMatch = MIHOMO_SOURCE.exec(origin)

  if (!using && !origin.startsWith('dial ')) return undefined

  const route =
    using ?? (sourceMatch && origin.slice(5, sourceMatch.index).replace(/ \(match .+\)$/, ''))
  let chain: string[] | undefined

  if (suffix) {
    try {
      const list = JSON.parse(suffix[1])

      if (Array.isArray(list) && list.every((item) => typeof item === 'string')) chain = list
    } catch {
      // 链不是合法 JSON 就当没有
    }
  }

  return {
    chain,
    source: sourceMatch?.[2]
      ? LOCAL_ROUTER_IP
      : sourceMatch
        ? normalizeIp(cleanHost(sourceMatch[1]))
        : undefined,
    target: cleanHost(target),
    port,
    route: route?.trim() || undefined,
  }
}

// mihomo 的出站有时写成 "策略组[节点]",拆成 [节点, 策略组];不是这种写法就原样返回
export const splitMihomoRoute = (route: string, groupNames: Set<string>) => {
  if (groupNames.has(route) || !route.endsWith(']')) return [route]

  let group: string | undefined

  for (const match of route.matchAll(/\[/g)) {
    const name = route.slice(0, match.index)

    if (match.index < route.length - 2 && groupNames.has(name)) group = name
  }

  return group ? [route.slice(group.length + 1, -1), group] : [route]
}

export const parseLogMessage = (
  payload: string,
  sourceIP: string | undefined,
  rawPayload: string = payload,
): ParsedLog => {
  const context = CONTEXT_PREFIX.exec(payload)
  const body = context ? payload.slice(context[0].length) : payload
  const moduleMatch = MODULE_PREFIX.exec(body)
  const module = moduleMatch?.[1]
  const content = moduleMatch ? body.slice(moduleMatch[0].length) : body
  const connection =
    module?.startsWith('inbound/') || module?.startsWith('outbound/') || module === 'connection'
      ? CONNECTION_LINE.exec(content)
      : null
  const dial = DIAL_LINE.exec(content)
  const mihomo = parseMihomoLine(rawPayload)
  const connectionHost = connection ? cleanHost(connection[3]) : undefined
  const inboundFrom =
    connection?.[1] === 'inbound' && connection[2] === 'from' ? connectionHost : undefined
  const source = normalizeIp(inboundFrom) ?? normalizeIp(sourceIP) ?? mihomo?.source
  const target =
    connection?.[2] === 'to'
      ? connectionHost
      : dial
        ? cleanHost(dial[1])
        : module === 'dns'
          ? DNS_LINE.exec(content)?.[1].replace(/\.$/, '').toLowerCase()
          : mihomo?.target
  const targetPort = connection?.[2] === 'to' ? connection[4] : (dial?.[2] ?? mihomo?.port)
  const outbound = module ? OUTBOUND_MODULE.exec(module)?.[1] : undefined
  const fakeIpDomain =
    module === 'router'
      ? FAKE_IP_LINE.exec(content)?.[1].replace(/\.$/, '').toLowerCase()
      : undefined

  return {
    contextId: context?.[1],
    module,
    message: mihomo?.chain ? content.replace(MIHOMO_CHAIN_SUFFIX, '') : content,
    source,
    target,
    targetPort,
    outbound,
    fakeIpDomain,
    mihomoChain: mihomo?.chain,
    mihomoRoute: mihomo?.route,
    dialFailure: !!dial,
    inboundTarget: connection?.[1] === 'inbound' && connection[2] === 'to',
  }
}

// 把同一条连接(contextId 相同)的几行日志合起来:来源、入站目标、出站目标、fakeIP 对应的域名。
// 入站目标是 fakeIP 时,用同一连接里出站拨号的域名反推它对应的真实域名
export const buildLogContexts = (parsed: ParsedLog[]) => {
  const contexts = new Map<string, LogContext>()

  for (const item of parsed) {
    if (!item.contextId || (!item.source && !item.target && !item.fakeIpDomain)) continue

    const context = contexts.get(item.contextId) ?? {}

    if (item.source) context.source ??= item.source
    if (item.fakeIpDomain) context.fakeIpDomain ??= item.fakeIpDomain
    if (item.target) {
      const hostPort = { host: item.target, port: item.targetPort }

      if (item.inboundTarget) {
        context.inboundTarget ??= hostPort
      } else if (item.dialFailure || item.module?.startsWith('outbound/')) {
        context.target ??= hostPort
      }
    }
    contexts.set(item.contextId, context)
  }

  const pending = new Map<string, { context: LogContext; port: string; domain?: string | null }>()

  for (const [id, context] of contexts) {
    const inbound = context.inboundTarget

    if (!inbound || !isFakeIp(inbound.host)) continue

    context.fakeIp = inbound.host
    if (!context.fakeIpDomain && inbound.port) pending.set(id, { context, port: inbound.port })
  }

  if (!pending.size) return contexts

  for (const item of parsed) {
    const entry = item.contextId ? pending.get(item.contextId) : undefined

    if (!entry || !item.outbound || !item.target || item.targetPort !== entry.port) continue
    if (ipaddr.isValid(item.target)) continue

    if (entry.domain === undefined) entry.domain = item.target
    else if (entry.domain !== item.target) entry.domain = null
  }

  for (const { context, domain } of pending.values()) {
    if (domain) context.fakeIpDomain = domain
  }

  return contexts
}

// 目标地址:目标是 fakeIP(或它对应的域名)时显示域名,并单独带上 fakeIP
export const resolveLogTarget = (target: HostPort | undefined, context?: LogContext) => {
  if (!target) return {}

  const { host, port } = target
  const { fakeIp, fakeIpDomain, inboundTarget } = context ?? {}
  const samePort = !port || !inboundTarget?.port || port === inboundTarget.port

  if (fakeIp && fakeIpDomain && samePort && (host === fakeIp || host === fakeIpDomain)) {
    return { targetAddress: formatHostPort(fakeIpDomain, port), targetFakeIp: fakeIp }
  }

  return { targetAddress: formatHostPort(host, port) }
}

type Translate = (key: string, named?: Record<string, unknown>) => string

// 中文界面下把常见的英文连接日志改写成中文,其它原样返回
export const localizeLogMessage = (message: string, locale: string, t: Translate) => {
  if (!locale.startsWith('zh-')) return message

  const route =
    /^(\[(?:TCP|UDP|ICMP)\] .+? --> \S+) (?:(?:match (.+?) |(doesn't match any rule) )?using )(.+)$/.exec(
      message,
    )

  if (route) {
    const [, connection, rule, unmatched, outbound] = route

    if (rule) return t('logMessageMatched', { connection, rule, outbound })

    return t(unmatched ? 'logMessageUnmatched' : 'logMessageVia', { connection, outbound })
  }

  const line = /^(inbound|outbound) (redirect )?(packet )?connection (from|to) (.+)$/.exec(message)

  if (line) {
    const [, direction, redirect, packet, relation, endpoint] = line

    return t(direction === 'inbound' ? 'logMessageInbound' : 'logMessageOutbound', {
      connection: t(`logMessage${redirect ? 'Redirect' : ''}${packet ? 'Packet' : ''}Connection`),
      endpoint,
      relation: t(relation === 'from' ? 'logMessageFrom' : 'logMessageTo'),
    })
  }

  return message
}
