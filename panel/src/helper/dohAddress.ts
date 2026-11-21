// DoH 地址的输入 / 展示:界面上拆成「域名或 IP」「端口」,路径不显示、但原样保留;
// 存进档案的仍是完整的 https:// 地址,所以后端和线上格式不变。

export interface DohPath {
  host: string
  suffix: string
}
export interface DohEndpoint {
  server: string
  port: number
  dohPath?: DohPath
}

const DEFAULT_DOH_PORT = 443
const DEFAULT_DOH_SUFFIX = '/dns-query'

const IPV4_RE = /^(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)$/
const IPV6_RE = /^[0-9a-f:]+$/i

export const isIpAddress = (value: string) => {
  const host = value.trim()
  if (!host) return false
  if (IPV4_RE.test(host)) return host !== '0.0.0.0'
  if (!host.includes(':')) return false
  return IPV6_RE.test(host) && host !== '::' && host.split('::').length <= 2 && host.split(':').length <= 8
}

export const isDomainHost = (value: string) => {
  const host = value.trim()
  if (!host || isIpAddress(host) || host.length > 253 || !host.includes('.')) return false
  return host.split('.').every((label) => label.length <= 63 && /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$/.test(label))
}

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
  return isIpAddress(host) || isDomainHost(host) || host === dohPath?.host ? host : ''
}

// 拼回存档用的完整地址;主机或端口不对返回空串
export const dohUrl = (endpoint: DohEndpoint) => {
  const host = dohHost(endpoint)
  if (!host || !isValidPort(endpoint.port)) return ''
  const port = endpoint.port === DEFAULT_DOH_PORT ? '' : `:${endpoint.port}`
  const suffix = endpoint.dohPath?.host === host ? endpoint.dohPath.suffix : DEFAULT_DOH_SUFFIX
  return `https://${bracketHost(host)}${port}${suffix}`
}
