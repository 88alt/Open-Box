import {
  ArrowsRightLeftIcon,
  Cog6ToothIcon,
  CpuChipIcon,
  CubeTransparentIcon,
  DocumentTextIcon,
  GlobeAltIcon,
  MapIcon,
  RssIcon,
  SwatchIcon,
} from '@heroicons/vue/24/outline'

export const IS_APPLE_DEVICE = /Mac|iPod|iPhone|iPad/.test(navigator.platform)

export const GLOBAL = 'GLOBAL'
// 默认使用 HTTP，减少探测时的 TLS 握手；仍支持用户自定义 HTTPS 地址
export const TEST_URL = 'http://www.gstatic.com/generate_204'
// 直连出站专用的测速地址。默认那个是 Google 的域名,从国内直连去测,量出来的是"直连到
// Google 有多远",不是直连线路本身的质量。直连默认使用华为的连通性检测地址。
export const DIRECT_TEST_URL = 'http://connectivitycheck.platform.hicloud.com/generate_204'
export const IPV6_TEST_URL = 'http://ipv6.google.com/generate_204'
export const NOT_CONNECTED = 0
export enum LANG {
  EN_US = 'en-US',
  ZH_CN = 'zh-CN',
  ZH_TW = 'zh-TW',
}

export enum FONTS {
  MI_SANS = 'MiSans',
  SARASA_UI = 'SarasaUi',
  PING_FANG = 'PingFang',
  FIRA_SANS = 'FiraSans',
  SYSTEM_UI = 'SystemUI',
}

export enum EMOJIS {
  TWEMOJI = 'twemoji',
  NOTO_COLOR_EMOJI = 'noto-color-emoji',
}

export enum CONNECTIONS_TABLE_ACCESSOR_KEY {
  Close = 'close',
  Type = 'type',
  Process = 'process',
  Host = 'host',
  Rule = 'rule',
  Chains = 'chains',
  Outbound = 'outbound',
  DlSpeed = 'dlSpeed',
  UlSpeed = 'ulSpeed',
  Download = 'dl',
  Upload = 'ul',
  ConnectTime = 'connectTime',
  SourceIP = 'sourceIP',
  SourcePort = 'sourcePort',
  SniffHost = 'sniffHost',
  Destination = 'destination',
  DestinationType = 'destinationType',
  RemoteAddress = 'remoteAddress',
  InboundUser = 'inboundUser',
}

export enum TABLE_WIDTH_MODE {
  AUTO = 'auto',
  MANUAL = 'manual',
}

export enum PROXY_SORT_TYPE {
  DEFAULT = 'defaultsort',
  NAME_ASC = 'nameasc',
  NAME_DESC = 'namedesc',
  LATENCY_ASC = 'latencyasc',
  LATENCY_DESC = 'latencydesc',
}

export enum PROXY_PREVIEW_TYPE {
  AUTO = 'auto',
  DOTS = 'dots',
  BAR = 'bar',
}

export enum PROXY_TAB_TYPE {
  POLICY = 'policyGroup',
  NODE = 'nodeGroup',
  PROVIDER = 'proxyProvider',
}

export enum SORT_TYPE {
  HOST = 'host',
  CHAINS = 'chains',
  RULE = 'rule',
  TYPE = 'type',
  CONNECT_TIME = 'connectTime',
  DOWNLOAD = 'download',
  DOWNLOAD_SPEED = 'downloadSpeed',
  UPLOAD = 'upload',
  UPLOAD_SPEED = 'uploadSpeed',
  SOURCE_IP = 'sourceIP',
  INBOUND_USER = 'inboundUser',
}

export enum SORT_DIRECTION {
  ASC = 'asc',
  DESC = 'desc',
}

// 表头点击排序:排序项 → 它对应的表格列(没有对应列的排序项,表格里选不到)
export const SORT_TYPE_COLUMN: Record<SORT_TYPE, CONNECTIONS_TABLE_ACCESSOR_KEY> = {
  [SORT_TYPE.HOST]: CONNECTIONS_TABLE_ACCESSOR_KEY.Host,
  [SORT_TYPE.CHAINS]: CONNECTIONS_TABLE_ACCESSOR_KEY.Chains,
  [SORT_TYPE.RULE]: CONNECTIONS_TABLE_ACCESSOR_KEY.Rule,
  [SORT_TYPE.TYPE]: CONNECTIONS_TABLE_ACCESSOR_KEY.Type,
  [SORT_TYPE.CONNECT_TIME]: CONNECTIONS_TABLE_ACCESSOR_KEY.ConnectTime,
  [SORT_TYPE.DOWNLOAD]: CONNECTIONS_TABLE_ACCESSOR_KEY.Download,
  [SORT_TYPE.DOWNLOAD_SPEED]: CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed,
  [SORT_TYPE.UPLOAD]: CONNECTIONS_TABLE_ACCESSOR_KEY.Upload,
  [SORT_TYPE.UPLOAD_SPEED]: CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed,
  [SORT_TYPE.SOURCE_IP]: CONNECTIONS_TABLE_ACCESSOR_KEY.SourceIP,
  [SORT_TYPE.INBOUND_USER]: CONNECTIONS_TABLE_ACCESSOR_KEY.InboundUser,
}

// 连接页的分组方式:值同时是页签文案的 i18n 键(和上游 v0.1.423 一致)
export enum CONNECTION_GROUP_MODE {
  DEVICE = 'connectionsByDevice',
  TARGET = 'connectionsByTarget',
}

// 展开一个分组之后,组内还能再分:不分 / 按分流 / 按另一个维度(终端分组里是目标,目标分组里是终端)
export type CONNECTION_SUBGROUP_MODE = 'none' | 'routing' | CONNECTION_GROUP_MODE

export enum LOG_LEVEL {
  Trace = 'trace',
  Debug = 'debug',
  Info = 'info',
  Warning = 'warning',
  Error = 'error',
  Fatal = 'fatal',
  Panic = 'panic',
  Silent = 'silent',
}

export enum ROUTE_NAME {
  overview = 'overview',
  proxies = 'proxies',
  connections = 'connections',
  logs = 'logs',
  rules = 'rules',
  subscriptions = 'subscriptions',
  routing = 'routing',
  kernel = 'kernel',
  settings = 'settings',
  login = 'login',
  setup = 'setup',
}

export const ROUTE_ICON_MAP = {
  [ROUTE_NAME.overview]: CubeTransparentIcon,
  [ROUTE_NAME.proxies]: GlobeAltIcon,
  [ROUTE_NAME.connections]: ArrowsRightLeftIcon,
  [ROUTE_NAME.rules]: SwatchIcon,
  [ROUTE_NAME.subscriptions]: RssIcon,
  [ROUTE_NAME.routing]: MapIcon,
  [ROUTE_NAME.kernel]: CpuChipIcon,
  [ROUTE_NAME.logs]: DocumentTextIcon,
  [ROUTE_NAME.settings]: Cog6ToothIcon,
  [ROUTE_NAME.login]: CubeTransparentIcon,
  [ROUTE_NAME.setup]: CubeTransparentIcon,
}

export enum TABLE_SIZE {
  SMALL = 'small',
  LARGE = 'large',
}

export enum PROXY_CARD_SIZE {
  SMALL = 'small',
  LARGE = 'large',
}

export enum MIN_PROXY_CARD_WIDTH {
  SMALL = 130,
  LARGE = 155,
}

export enum PROXY_CHAIN_DIRECTION {
  NORMAL = 'normal',
  REVERSE = 'reverse',
}

export enum PROXY_TYPE {
  Direct = 'direct',
  Reject = 'reject',
  RejectDrop = 'rejectdrop',
  Compatible = 'compatible',
  Pass = 'pass',
  Dns = 'dns',
  Selector = 'selector',
  Fallback = 'fallback',
  URLTest = 'urltest',
  Smart = 'smart',
  LoadBalance = 'loadbalance',
}

// 连接卡片里的「路由设置」虚拟列(对齐上游 v0.1.423):显示这条连接命中的路由策略,不是 CONNECTIONS_TABLE_ACCESSOR_KEY 里的真实列
export const TARGET_ROUTING_COLUMN = 'targetRouting'

// 「关闭」按钮固定在卡片最左,不属于可排布的列
export type ConnectionCardColumn =
  | Exclude<CONNECTIONS_TABLE_ACCESSOR_KEY, CONNECTIONS_TABLE_ACCESSOR_KEY.Close>
  | typeof TARGET_ROUTING_COLUMN

// 卡片每一行固定三列(左 / 中 / 右),每列是一组字段
export type ConnectionCardRow = [
  ConnectionCardColumn[],
  ConnectionCardColumn[],
  ConnectionCardColumn[],
]

// 表头 / 卡片设置里个别列的显示名与列 key 不同:源 IP 叫「设备」,主机叫「目标」
export const CONNECTION_COLUMN_LABEL_MAP: Partial<Record<CONNECTIONS_TABLE_ACCESSOR_KEY, string>> =
  {
    [CONNECTIONS_TABLE_ACCESSOR_KEY.SourceIP]: 'connectionDevice',
    [CONNECTIONS_TABLE_ACCESSOR_KEY.Host]: 'connectionTarget',
  }

export const DEFAULT_CONNECTION_CARD_PC: ConnectionCardRow[] = [
  [
    [CONNECTIONS_TABLE_ACCESSOR_KEY.SourceIP],
    [TARGET_ROUTING_COLUMN, CONNECTIONS_TABLE_ACCESSOR_KEY.Outbound],
    [CONNECTIONS_TABLE_ACCESSOR_KEY.Host],
  ],
  [
    [CONNECTIONS_TABLE_ACCESSOR_KEY.ConnectTime],
    [CONNECTIONS_TABLE_ACCESSOR_KEY.Type],
    [CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed, CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed],
  ],
]

export const DEFAULT_CONNECTION_CARD_MOBILE: ConnectionCardRow[] = [
  [[CONNECTIONS_TABLE_ACCESSOR_KEY.SourceIP], [], [CONNECTIONS_TABLE_ACCESSOR_KEY.Host]],
  [[CONNECTIONS_TABLE_ACCESSOR_KEY.Outbound], [], []],
  [
    [CONNECTIONS_TABLE_ACCESSOR_KEY.ConnectTime],
    [CONNECTIONS_TABLE_ACCESSOR_KEY.Type],
    [CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed, CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed],
  ],
]

// 表格的「关闭」列固定在最左,不进列设置
export const DEFAULT_CONNECTION_TABLE_COLUMNS: CONNECTIONS_TABLE_ACCESSOR_KEY[] = [
  CONNECTIONS_TABLE_ACCESSOR_KEY.SourceIP,
  CONNECTIONS_TABLE_ACCESSOR_KEY.Outbound,
  CONNECTIONS_TABLE_ACCESSOR_KEY.Host,
  CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed,
  CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed,
  CONNECTIONS_TABLE_ACCESSOR_KEY.ConnectTime,
]

export const ALL_THEME = [
  'light',
  'dark',
  'light-legacy',
  'dark-legacy',
  'cupcake',
  'bumblebee',
  'emerald',
  'corporate',
  'synthwave',
  'retro',
  'cyberpunk',
  'valentine',
  'halloween',
  'garden',
  'forest',
  'aqua',
  'lofi',
  'pastel',
  'fantasy',
  'wireframe',
  'black',
  'luxury',
  'dracula',
  'cmyk',
  'autumn',
  'business',
  'acid',
  'lemonade',
  'night',
  'coffee',
  'winter',
  'dim',
  'nord',
  'sunset',
  'caramellatte',
  'abyss',
  'silk',
]

export const DEFAULT_THEME = {
  name: 'custom',
  id: '',
  '--border': '1px',
  '--color-base-100': '#ffffff',
  '--color-base-200': '#fcfcfc',
  '--color-base-300': '#f2f2f2',
  '--color-base-content': '#2d2d33',
  '--color-primary': '#5a3cd2',
  '--color-primary-content': '#f3efff',
  '--color-secondary': '#ea4c5a',
  '--color-secondary-content': '#fff1f2',
  '--color-accent': '#49c6c1',
  '--color-accent-content': '#285e66',
  '--color-neutral': '#1e1e1f',
  '--color-neutral-content': '#ececec',
  '--color-info': '#5b90ff',
  '--color-info-content': '#273c66',
  '--color-success': '#44c07a',
  '--color-success-content': '#1d472f',
  '--color-warning': '#e5a300',
  '--color-warning-content': '#705322',
  '--color-error': '#d13a30',
  '--color-error-content': '#551d1d',
  '--depth': '0',
  '--noise': '0',
  '--radius-box': '1rem',
  '--radius-field': '0.5rem',
  '--radius-selector': '1rem',
  '--size-field': '0.25rem',
  '--size-selector': '0.25rem',
  'color-scheme': 'dark',
  default: false,
  prefersdark: false,
}

export type THEME = Record<string, string>

// 上游 v0.1.396 起 IP 信息只剩 Net.Coffee 一个来源
export enum IP_INFO_API {
  NETCOFFEE = 'Net.Coffee',
}

// 设置页顶部的一级页签。SETTINGS_MENU_KEY(见下)是「面板设置」这一个页签**内部**
// 堆叠的那几组卡片,两者层级不同:一级页签切换的是整块内容,SETTINGS_MENU_KEY 只
// 决定面板设置里各组卡片的显示与顺序(仍由齿轮里的可见性弹窗控制)。
// 「连接」页顶栏的三个页签(原来的规则页、日志页并进了连接页)
export enum CONNECTION_WORKSPACE_TAB {
  routing = 'routing',
  connections = 'connections',
  logs = 'logs',
}

export enum SETTINGS_TAB {
  panel = 'panel',
  subscriptions = 'subscriptions',
  groups = 'groups',
  routing = 'routing',
  clients = 'clients',
  chain = 'chain',
  dns = 'dns',
  kernel = 'kernel',
  share = 'share',
  clientApp = 'clientApp',
}

export enum SETTINGS_MENU_KEY {
  general = 'generalSettings',
  backend = 'backendSettings',
  proxies = 'proxySettings',
  connections = 'connectionSettings',
  overview = 'overviewSettings',
}

export enum OVERVIEW_CARD {
  ChartsCard = 'ChartsCard',
  NetworkCard = 'NetworkCard',
  ProviderTrafficOverview = 'ProviderTrafficOverview',
  TopologyCharts = 'TopologyCharts',
  ConnectionHistory = 'ConnectionHistory',
  RuleHitCountCard = 'RuleHitCountCard',
}
