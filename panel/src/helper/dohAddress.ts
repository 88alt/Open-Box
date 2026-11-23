// DoH / DoT 地址的输入 / 展示:界面上拆成「域名或 IP」「端口」,DoH 的路径不显示、但原样保留;
// 存进档案的仍是完整的 https:// / tls:// 地址,所以后端和线上格式不变。
import { dnsDefaultPort, isDnsHost } from './dnsProtocol'

export interface DohPath {
  host: string
  suffix: string
}
export interface DohEndpoint {
  server: string
  port: number
  dohPath?: DohPath
}
export interface DotEndpoint {
  server: string
  port: number
  // 地址里是否写明了端口(没写就用调用方给的端口)
  explicitPort: boolean
}

const DEFAULT_DOH_PORT = 443
const DEFAULT_DOH_SUFFIX = '/dns-query'

export const isValidPort = (value: unknown): value is number =>
  typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= 65535

// [::1] → ::1
export const stripBrackets = (value: string) => value.trim().replace(/^\[([^\]]+)\]$/, '$1')
// ::1 → [::1]
export const bracketHost = (host: string) => (host.includes(':') ? `[${host}]` : host)

// 完整的 https:// 地址 → 主机、端口、路径;带账号密码 / # / 空白、端口越界都算不合法
export const parseDohUrl = (text: string): DohEndpoint | null => {
  const raw = text.trim()
  if (!/^https:\/\//i.test(raw) || /\s/.test(raw) || raw.length > 2048) return null
  try {
    const url = new URL(raw)
    const host = stripBrackets(url.hostname)
    const port = Number(url.port || DEFAULT_DOH_PORT)
    if (!host || url.username || url.password || url.hash || !isValidPort(port)) return null
    return { server: host, port, dohPath: { host, suffix: url.pathname + url.search } }
  } catch {
    return null
  }
}

// 用户在「域名或 IP」框里敲的 / 粘贴的内容:完整地址,或 host[:port][/path];都认不出来返回 null(原样保留让用户继续改)
export const parseDohInput = (text: string, fallbackPort: number): DohEndpoint | null => {
  const raw = text.trim()
  if (/^https:\/\//i.test(raw)) return parseDohUrl(raw)
  const match = /^(?:\[[^\]]+\]|[^:/?#\\@\s]+)(?::(\d+))?(?=[/?]|$)/.exec(raw)
  if (!match) return null
  const hasPath = match[0] !== raw
  const typedPort = match[1]
  if (!hasPath && !typedPort) return null
  const parsed = parseDohUrl(`https://${raw}${hasPath ? '' : DEFAULT_DOH_SUFFIX}`)
  return parsed && { ...parsed, port: typedPort ? parsed.port : fallbackPort }
}

// 框里的主机是不是能用的:IP、域名,或和已保存路径同一个主机
export const dohHost = ({ server, dohPath }: Pick<DohEndpoint, 'server' | 'dohPath'>) => {
  const host = stripBrackets(server)
  return isDnsHost(host) || host === dohPath?.host ? host : ''
}

// 拼回存档用的完整地址;主机或端口不对返回空串
export const dohUrl = (endpoint: DohEndpoint) => {
  const host = dohHost(endpoint)
  if (!host || !isValidPort(endpoint.port)) return ''
  const port = endpoint.port === DEFAULT_DOH_PORT ? '' : `:${endpoint.port}`
  const suffix = endpoint.dohPath?.host === host ? endpoint.dohPath.suffix : DEFAULT_DOH_SUFFIX
  return `https://${bracketHost(host)}${port}${suffix}`
}

// 完整的 tls:// 地址 → 主机、端口:只认 tls://host[:port](可带结尾的 /),不带账号 / 查询 / # / 路径;
// 主机要是能用的 DNS 主机(IP 或域名,不能是 wan)。没写端口时用 fallbackPort(不合法就用 853)
export const parseTlsUrl = (
  text: string,
  fallbackPort: number = dnsDefaultPort('tls'),
): DotEndpoint | null => {
  const raw = text.trim()
  if (!/^tls:\/\//i.test(raw) || /\s/.test(raw) || raw.length > 2048) return null
  const rest = raw.slice('tls://'.length)
  if (!rest || /[@?#]/.test(rest)) return null
  const slash = rest.indexOf('/')
  const authority = slash >= 0 ? (slash === rest.length - 1 ? rest.slice(0, slash) : '') : rest
  if (!authority) return null
  let host = ''
  let port = isValidPort(fallbackPort) ? fallbackPort : dnsDefaultPort('tls')
  let explicitPort = false
  if (authority.startsWith('[')) {
    const close = authority.indexOf(']')
    if (close <= 1) return null
    host = authority.slice(1, close)
    const tail = authority.slice(close + 1)
    if (tail) {
      if (!/^:\d+$/.test(tail)) return null
      port = Number(tail.slice(1))
      if (!isValidPort(port)) return null
      explicitPort = true
    }
  } else {
    const colons = (authority.match(/:/g) || []).length
    if (colons > 1) return null
    if (colons === 1) {
      const at = authority.indexOf(':')
      const portText = authority.slice(at + 1)
      if (!/^\d+$/.test(portText)) return null
      port = Number(portText)
      if (!isValidPort(port)) return null
      host = authority.slice(0, at)
      explicitPort = true
    } else {
      host = authority
    }
  }
  const name = stripBrackets(host).toLowerCase()
  if (!name || name === 'wan' || !isDnsHost(name)) return null
  try {
    const url = new URL(`tls://${bracketHost(name)}:${port}`)
    const normalized = stripBrackets(url.hostname).toLowerCase()
    return !normalized || normalized === 'wan' || !isDnsHost(normalized)
      ? null
      : { server: normalized, port, explicitPort }
  } catch {
    return null
  }
}

// 用户在 DoT「域名或 IP」框里敲的 / 粘贴的内容:完整的 tls:// 地址,或 host:port;都认不出来返回 null
export const parseDotInput = (text: string, fallbackPort: number): DotEndpoint | null => {
  const raw = text.trim()
  if (/^tls:\/\//i.test(raw)) return parseTlsUrl(raw, dnsDefaultPort('tls'))
  if (!/^(?:\[([^\]]+)\]|([^:/?#\s@]+)):(\d+)$/.test(raw)) return null
  const parsed = parseTlsUrl(`tls://${raw}`, fallbackPort)
  return parsed ? { ...parsed, explicitPort: true } : null
}

// 粘贴 / 敲进来的是完整的 https:// 或 tls:// 地址:认出协议和主机端口,认不出来返回 null
export const parseEndpointUrl = (
  text: string,
):
  | { protocol: 'https'; address: DohEndpoint }
  | { protocol: 'tls'; address: DotEndpoint }
  | null => {
  const raw = text.trim()
  if (/^https:\/\//i.test(raw)) {
    const address = parseDohUrl(raw)
    return address ? { protocol: 'https', address } : null
  }
  if (/^tls:\/\//i.test(raw)) {
    const address = parseTlsUrl(raw, dnsDefaultPort('tls'))
    return address ? { protocol: 'tls', address } : null
  }
  return null
}
