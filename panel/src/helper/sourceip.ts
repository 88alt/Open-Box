// 终端命名:对齐上游 v0.1.423,自己重写。
// 标签的键可以是 MAC(按终端本身保存,换 IP 也跟着走)、IP、IPv6 后缀、/正则、CIDR。
// 显示名字的优先级:MAC 标签 > IP 标签(精确 / 正则 / CIDR)> 邻居表里的主机名 > IP 本身。
import { fetchKnownClients } from '@/api/openbox'
import { sourceIPLabelList } from '@/store/settings'
import * as ipaddr from 'ipaddr.js'
import { computed, ref } from 'vue'

const CACHE_SIZE = 256
// 邻居表(IP / MAC / 主机名)一分钟内不重复取
const CLIENTS_TTL = 60_000

export interface KnownClient {
  ip: string
  name: string
  mac?: string
}

// 重命名弹窗的入参:要改名的终端 IP,以及已知的主机名(没有就空)
export interface DeviceRenameTarget {
  ip: string
  hostname?: string
}

// 规范成小写、冒号分隔的 MAC;不是合法 MAC、全 0、组播地址都返回空串
export const normalizeMac = (mac = '') => {
  const value = mac.toLowerCase().replace(/-/g, ':')

  if (!/^([0-9a-f]{2}:){5}[0-9a-f]{2}$/.test(value) || value === '00:00:00:00:00:00') return ''

  return parseInt(value.slice(0, 2), 16) % 2 === 0 ? value : ''
}

// 标签按键的种类分好表,标签一改整张表重建(查询缓存跟着清掉)
const labelTables = computed(() => {
  const exact = new Map<string, string>()
  const macs = new Map<string, string>()
  const patterns: { regex: RegExp; label: string }[] = []
  const networks: { cidr: [ipaddr.IPv4 | ipaddr.IPv6, number]; label: string }[] = []

  for (const { key, label } of sourceIPLabelList.value) {
    const mac = normalizeMac(key)

    if (mac) {
      macs.set(mac, label)
    } else if (key.startsWith('/')) {
      try {
        patterns.push({ regex: new RegExp(key.slice(1), 'i'), label })
      } catch {
        // 无效正则,忽略
      }
    } else if (key.includes('/')) {
      try {
        networks.push({ cidr: ipaddr.parseCIDR(key), label })
      } catch {
        // 无效 CIDR,忽略
      }
    } else {
      exact.set(key, label)
    }
  }

  return { exact, macs, patterns, networks, cache: new Map<string, string>() }
})

const getLabelByMac = (mac?: string) => labelTables.value.macs.get(normalizeMac(mac)) || ''

// 只按 IP 查标签(精确 / IPv6 后缀 / 正则 / CIDR),都没命中就返回 IP 本身;空 IP 是内部连接
export const getIPLabelFromMap = (ip: string) => {
  if (!ip) return ip === '' ? 'Inner' : ''

  const { exact, patterns, networks, cache } = labelTables.value
  const cached = cache.get(ip)

  if (cached !== undefined) return cached

  const addr = ipaddr.parse(ip)
  const direct = exact.get(ip)
  let suffix: string | undefined

  if (direct === undefined && addr.kind() === 'ipv6') {
    for (const [key, label] of exact) {
      if (ip.endsWith(key)) {
        suffix = label
        break
      }
    }
  }

  const label =
    direct ??
    suffix ??
    patterns.find(({ regex }) => regex.test(ip))?.label ??
    networks.find(({ cidr }) => addr.kind() === cidr[0].kind() && addr.match(cidr))?.label ??
    ip

  if (cache.size >= CACHE_SIZE) {
    const first = cache.keys().next().value

    if (first !== undefined) cache.delete(first)
  }
  cache.set(ip, label)

  return label
}

const knownClients = ref<KnownClient[]>([])
let lastFetchAt = 0
let pending: Promise<void> | null = null

// 刷新邻居表。默认一分钟内不重复取;force 用在改名弹窗里,要立刻拿到这台终端的 MAC
export const refreshKnownClients = (force = false) => {
  if (pending) return pending
  if (!force && lastFetchAt && Date.now() - lastFetchAt < CLIENTS_TTL) return Promise.resolve()

  lastFetchAt = Date.now()
  pending = fetchKnownClients()
    .then(({ clients }) => {
      knownClients.value = clients || []
    })
    .catch(() => {})
    .finally(() => {
      pending = null
    })

  return pending
}

const clientsByIP = computed(() => new Map(knownClients.value.map((client) => [client.ip, client])))

const clientsByMac = computed(() => {
  const map = new Map<string, KnownClient>()

  for (const client of knownClients.value) {
    const mac = normalizeMac(client.mac)

    if (mac) map.set(mac, client)
  }

  return map
})

export const getKnownClient = (ip: string) => clientsByIP.value.get(ip)

// 只有用户给这台终端起了名字(MAC 标签或 IP 标签)才返回,没有就空串
export const getDeviceAlias = (ip: string) => {
  if (!ip) return ''

  refreshKnownClients()

  const byMac = getLabelByMac(getKnownClient(ip)?.mac)

  if (byMac) return byMac

  try {
    const label = getIPLabelFromMap(ip)

    return label && label !== ip ? label : ''
  } catch {
    return ''
  }
}

// 终端的名字:自定义名字 > 传入的主机名 > 邻居表里的主机名,都没有就空串
export const getDeviceName = (ip: string, hostname = '') =>
  getDeviceAlias(ip) || hostname || (ip ? getKnownClient(ip)?.name : '') || ''

// 显示用的终端名字:没有名字就显示 IP 本身,空 IP 是内部连接
export const getDeviceLabel = (ip: string) => (ip ? getDeviceName(ip) || ip : getIPLabelFromMap(ip))

// 按 MAC 取终端名字:MAC 标签优先,没有就取邻居表里这个 MAC 对应的终端的名字,都没有就空串
export const getDeviceNameByMac = (mac: string) => {
  const byLabel = getLabelByMac(mac)

  if (byLabel) return byLabel

  refreshKnownClients()

  const client = clientsByMac.value.get(normalizeMac(mac))

  return client ? getDeviceName(client.ip, client.name) : ''
}
