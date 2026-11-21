// 自定义 DNS 服务器的输入草稿(给 CustomDnsFields 用):
// 四种协议各记一份「域名或 IP / 端口 / 引导 DNS」,切协议时输入不串;
// 存进档案的仍是 { url, bootstrap? },url 是 https:// / tls:// / tcp:// / udp:// 地址,线上格式不变。
import {
  bracketHost,
  dohHost,
  dohUrl,
  isValidPort,
  parseDohUrl,
  stripBrackets,
  type DohPath,
} from './dohAddress'
import { DNS_PROTOCOLS, dnsDefaultPort, isDnsHost, isDnsIp, type DnsProtocol } from './dnsProtocol'

export interface DnsEndpointDraft {
  server: string
  port: number
  // 解析 DoH / DoT 域名用的 DNS(IP)
  bootstrap: string
  dohPath?: DohPath
}
export interface DnsDraft {
  protocol: DnsProtocol
  endpoints: Record<DnsProtocol, DnsEndpointDraft>
}
export interface DnsValue {
  url: string
  bootstrap?: string
}

const emptyEndpoint = (protocol: DnsProtocol): DnsEndpointDraft => ({
  server: '',
  port: dnsDefaultPort(protocol),
  bootstrap: '',
})

// 已保存的值 → 草稿;没有值或读不懂时是空草稿(默认 DoH)
export const createDnsDraft = (value?: Partial<DnsValue> | null): DnsDraft => {
  const draft: DnsDraft = {
    protocol: 'https',
    endpoints: {
      udp: emptyEndpoint('udp'),
      tcp: emptyEndpoint('tcp'),
      tls: emptyEndpoint('tls'),
      https: emptyEndpoint('https'),
    },
  }
  if (!value?.url) return draft
  try {
    const url = new URL(value.url)
    const protocol = DNS_PROTOCOLS.find((p) => url.protocol === `${p}:`)
    if (protocol) {
      draft.protocol = protocol
      const parsed =
        protocol === 'https'
          ? (parseDohUrl(value.url) ?? { server: value.url, port: dnsDefaultPort('https') })
          : { server: stripBrackets(url.hostname), port: Number(url.port) || dnsDefaultPort(protocol) }
      draft.endpoints[protocol] = { ...parsed, bootstrap: value.bootstrap || '' }
      return draft
    }
  } catch {
    // 不是能解析的地址:原样放进 DoH 的输入框,让用户自己改
  }
  draft.endpoints.https.server = value.url
  draft.endpoints.https.bootstrap = value.bootstrap || ''
  return draft
}

// 当前协议下,主机是域名(不是 IP)时,要有引导 DNS 来解析它:DoH 和 DoT 才有这一项
export const dnsDraftNeedsBootstrap = ({ protocol, endpoints }: DnsDraft) => {
  const endpoint = endpoints[protocol]
  const host = stripBrackets(endpoint.server)
  const target =
    protocol === 'https' ? dohHost(endpoint) : protocol === 'tls' && isDnsHost(host) ? host : ''
  return !!target && !isDnsIp(target)
}

// 校验提示的文案键;没填地址、或填的没问题时返回空串
export const dnsDraftError = (draft: DnsDraft) => {
  const { protocol } = draft
  const endpoint = draft.endpoints[protocol]
  const host = stripBrackets(endpoint.server)
  if (!host) return ''
  if (protocol === 'https') {
    if (!dohHost(draft.endpoints.https)) return 'dnsUpstreamInvalidDohHost'
  } else if (protocol === 'tls') {
    if (!isDnsHost(host)) return 'dnsUpstreamInvalidDotHost'
  } else if (!isDnsIp(host)) {
    return 'subscriptionNodeDnsInvalidIp'
  }
  if (!isValidPort(endpoint.port)) return 'dnsUpstreamInvalidPort'
  if (!dnsDraftNeedsBootstrap(draft)) return ''
  const bootstrap = endpoint.bootstrap.trim()
  if (!bootstrap) return 'subscriptionNodeDnsNeedBootstrap'
  return isDnsIp(bootstrap) ? '' : 'subscriptionNodeDnsInvalidBootstrap'
}

// 草稿 → 要保存的值;没填地址或校验没过是 null(服务端存成没有)
export const dnsDraftValue = (draft: DnsDraft): DnsValue | null => {
  const { protocol } = draft
  const endpoint = draft.endpoints[protocol]
  const host = stripBrackets(endpoint.server)
  if (!host || dnsDraftError(draft)) return null
  const url =
    protocol === 'https'
      ? dohUrl(draft.endpoints.https)
      : `${protocol}://${bracketHost(host)}${endpoint.port === dnsDefaultPort(protocol) ? '' : `:${endpoint.port}`}`
  return { url, ...(dnsDraftNeedsBootstrap(draft) ? { bootstrap: endpoint.bootstrap.trim() } : {}) }
}
