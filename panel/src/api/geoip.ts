import { i18n } from '@/i18n'

export interface IPInfo {
  ip: string
  country: string
  region: string
  city: string
  asn: string
  organization: string
}

// china
export const getIPFromIpipnetAPI = async () => {
  const response = await fetch('https://myip.ipip.net/json?t=' + Date.now())

  return (await response.json()) as {
    data: {
      ip: string
      location: string[]
    }
  }
}

// 全局 IP 信息只用 Net.Coffee(对齐上游 v0.1.396,不再提供 ip.sb / ipwho.is / ipapi.is 可选)。
// 不带 IP 时先问 cdn-cgi/trace 拿到本机出口 IP,再用 ipv2/lookup 查详情。
const NET_COFFEE_TRACE_URL = 'https://ip.net.coffee/cdn-cgi/trace'
const netCoffeeLookupUrl = (ip: string) => `https://ip.net.coffee/api/ipv2/lookup/${encodeURIComponent(ip)}`

const isIPLike = (value: unknown): value is string =>
  typeof value === 'string' && (/^\d{1,3}(\.\d{1,3}){3}$/.test(value) || (value.includes(':') && /^[0-9a-f:.]+$/i.test(value)))

const parseTraceIP = (text: string) => {
  const trace = Object.fromEntries(
    text
      .trim()
      .split(/\r?\n/)
      .map((line) => {
        const i = line.indexOf('=')
        return [line.slice(0, i), line.slice(i + 1).trim()]
      }),
  )

  if (trace.h !== 'ip.net.coffee' || !isIPLike(trace.ip)) throw new Error('Net.Coffee 出口查询失败')

  return trace.ip as string
}

// 有国家代码时按面板语言显示国家名,没有就保持接口返回的原文
const localizedCountry = (countryCode: string, fallback: string) => {
  if (!/^[A-Za-z]{2}$/.test(countryCode)) return fallback

  try {
    const locale = String(i18n.global.locale.value || 'en').replace('_', '-')

    return new Intl.DisplayNames([locale], { type: 'region' }).of(countryCode.toUpperCase()) || fallback
  } catch {
    return fallback
  }
}

type NetCoffeeLookup = {
  ip?: string
  country?: string
  countryCode?: string
  region?: string
  city?: string
  asn?: string | number
  asOrganization?: string
  isp?: string
  company_name?: string
  error?: unknown
  is_bogon?: boolean
}

export const getIPInfo = async (ip = ''): Promise<IPInfo> => {
  let target = ip

  if (!target) {
    const response = await fetch(NET_COFFEE_TRACE_URL, { signal: AbortSignal.timeout(10000), cache: 'no-store' })

    if (!response.ok) throw new Error('Net.Coffee 出口查询失败')
    target = parseTraceIP(await response.text())
  }

  const response = await fetch(netCoffeeLookupUrl(target), { signal: AbortSignal.timeout(15000) })

  if (!response.ok) throw new Error('Net.Coffee IP 查询失败')

  const data = (await response.json()) as NetCoffeeLookup

  if (!data || typeof data !== 'object' || !isIPLike(data.ip) || data.error || data.is_bogon) {
    throw new Error('Net.Coffee 返回的不是有效 IP 信息')
  }
  if (data.ip !== target) throw new Error('Net.Coffee 返回的 IP 不匹配')

  const countryCode = String(data.countryCode || '')

  return {
    ip: data.ip,
    country: localizedCountry(countryCode, String(data.country || '')),
    region: String(data.region || ''),
    city: String(data.city || ''),
    asn: String(data.asn || '').replace(/^AS/i, ''),
    organization: String(data.asOrganization || data.isp || data.company_name || ''),
  }
}
