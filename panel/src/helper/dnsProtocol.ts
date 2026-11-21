// DNS 上游的协议、默认端口和主机校验,DNS 地址输入框(CustomDnsFields)和 DoH 地址解析共用。
import ipaddr from 'ipaddr.js'

export const DNS_PROTOCOLS = ['udp', 'tcp', 'tls', 'https'] as const
export type DnsProtocol = (typeof DNS_PROTOCOLS)[number]

// 界面上的叫法:tls = DoT,https = DoH,其余直接大写
export const dnsProtocolName = (protocol: string) =>
  protocol === 'tls' ? 'DoT' : protocol === 'https' ? 'DoH' : protocol.toUpperCase()

export const dnsDefaultPort = (protocol: string) => (protocol === 'tls' ? 853 : protocol === 'https' ? 443 : 53)

// 有效的 IPv4 / IPv6 地址(不带 % 区域标识)
export const isDnsIp = (value: string) =>
  !value.includes('%') && (ipaddr.IPv4.isValidFourPartDecimal(value) || ipaddr.IPv6.isValid(value))

const HOST_LABELS_RE = /^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$/i

// DNS 服务器能填的主机:有效 IP(不含 0.0.0.0 和 ::),或含字母的域名(每段 ≤63、总长 ≤253)
export const isDnsHost = (value: string) =>
  (!value.includes('%') && !['0.0.0.0', '::'].includes(value) && isDnsIp(value)) ||
  (value.length <= 253 && /[a-z]/i.test(value) && HOST_LABELS_RE.test(value))
