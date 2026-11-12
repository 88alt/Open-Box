import { blockConnectionByIdAPI, disconnectByIdAPI } from '@/api'
import ConnectionTargetRoutingButton from '@/components/connections/ConnectionTargetRoutingButton.vue'
import { useBounceOnVisible } from '@/composables/bouncein'
import { useConnections } from '@/composables/connections'
import {
  CONNECTIONS_TABLE_ACCESSOR_KEY,
  PROXY_CHAIN_DIRECTION,
  TARGET_ROUTING_COLUMN,
  type ConnectionCardColumn,
  type ConnectionCardRow,
} from '@/constant'
import {
  getDestinationFromConnection,
  getDestinationTypeFromConnection,
  getHostFromConnection,
  getInboundUserFromConnection,
  getNetworkTypeFromConnection,
  getProcessFromConnection,
} from '@/helper'
import { getDeviceLabel } from '@/helper/sourceip'
import { fromNow, isMiddleScreen, prettyBytesHelper } from '@/helper/utils'
import { i18n } from '@/i18n'
import { connectionCardLines, proxyChainDirection } from '@/store/settings'
import type { Connection } from '@/types'
import {
  ArrowDownCircleIcon,
  ArrowDownIcon,
  ArrowRightCircleIcon,
  ArrowUpCircleIcon,
  ArrowUpIcon,
  NoSymbolIcon,
  XMarkIcon,
} from '@heroicons/vue/24/outline'
import { first, last, uniq } from 'lodash'
import { defineComponent } from 'vue'
import type { JSX } from 'vue/jsx-runtime'
import ProxyName from '../proxies/ProxyName.vue'

// 桌面端三列网格:左右两列按内容宽度(最宽 30%),中间一列占满剩余
const CARD_GRID_COLUMNS = 'grid-cols-[fit-content(30%)_minmax(0,1fr)_fit-content(30%)]'
const CARD_COLUMN_ALIGN = ['justify-start', 'justify-center', 'justify-end']

export default defineComponent<{
  conn: Connection
}>({
  props: {
    conn: Object,
  },
  name: 'ConnectionCard',
  setup(props) {
    const { handlerInfo } = useConnections()

    useBounceOnVisible()

    return () => {
      const conn = props.conn
      const metadata = conn.metadata
      // 桌面端显示完整代理链;窄屏(≤768px)只留首尾两端
      const chains = isMiddleScreen.value
        ? uniq([last(conn.chains)!, first(conn.chains)!])
        : [...conn.chains].reverse()
      // 命中的路由策略(链的最后一环),只有链里既有节点又有策略组时才有
      const routingPolicy = conn.chains.length > 1 ? last(conn.chains) : undefined
      // 节点链(入口 → 出口),不含末尾的路由策略
      const nodeChain = [...(routingPolicy ? conn.chains.slice(0, -1) : conn.chains)].reverse()
      const componentMap: Record<ConnectionCardColumn, JSX.Element> = {
        [TARGET_ROUTING_COLUMN]: (
          <div class="min-w-0 truncate">{routingPolicy && <ProxyName name={routingPolicy} />}</div>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.Host]: (
          <span class="inline-flex w-80 min-w-0 grow items-center gap-1">
            <span
              class="text-main truncate"
              title={getHostFromConnection(conn)}
            >
              {getHostFromConnection(conn)}
            </span>
            <ConnectionTargetRoutingButton target={metadata.host || metadata.sniffHost || metadata.destinationIP || ''} />
          </span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.Destination]: (
          <span class="w-80 grow truncate break-all">{getDestinationFromConnection(conn)}</span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.RemoteAddress]: (
          <span class="w-80 grow truncate break-all">{conn.metadata.remoteDestination || '-'}</span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.SourceIP]: (
          <span
            class="w-40 grow truncate break-all"
            title={metadata.sourceIP || undefined}
          >
            {getDeviceLabel(metadata.sourceIP)}
          </span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.SourcePort]: (
          <span class="w-20 grow truncate break-all">{metadata.sourcePort}</span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.SniffHost]: (
          <span class="w-80 grow truncate break-all">{metadata.sniffHost || '-'}</span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.Type]: (
          <span class="w-60 grow truncate break-all">{getNetworkTypeFromConnection(conn)}</span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.Rule]: (
          <span class="w-80 grow truncate break-all">
            {conn.rule}
            {conn.rulePayload && <>: {conn.rulePayload}</>}
          </span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.Process]: (
          <span class="w-60 grow truncate break-all">{getProcessFromConnection(conn)}</span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.Chains]: (
          <span
            class={[
              'flex w-80 grow items-center gap-1 truncate break-all',
              proxyChainDirection.value === PROXY_CHAIN_DIRECTION.REVERSE &&
                'flex-row-reverse justify-end',
            ]}
          >
            {chains.map((name, index) => (
              <>
                {index > 0 && (
                  <ArrowRightCircleIcon class="h-4 w-4 shrink-0"></ArrowRightCircleIcon>
                )}
                <ProxyName name={name} />
              </>
            ))}
          </span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.Outbound]: (
          <span
            class={[
              'flex max-w-full min-w-0 items-center gap-1 overflow-hidden whitespace-nowrap',
              proxyChainDirection.value === PROXY_CHAIN_DIRECTION.REVERSE &&
                'flex-row-reverse justify-end',
            ]}
          >
            {routingPolicy && (
              <>
                <ProxyName name={routingPolicy} />
                {nodeChain.length > 0 && (
                  <span
                    class="text-base-content/60 shrink-0"
                    aria-hidden="true"
                  >
                    ·
                  </span>
                )}
              </>
            )}
            {nodeChain.map((name, index) => (
              <>
                {index > 0 && <ArrowRightCircleIcon class="size-4 shrink-0" />}
                <ProxyName name={name} />
              </>
            ))}
            {!routingPolicy && !nodeChain.length && <span>—</span>}
          </span>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.Download]: (
          <div class="flex items-center gap-1 text-xs whitespace-nowrap">
            {prettyBytesHelper(conn.download)}
            <ArrowDownIcon class="text-success h-3 w-3" />
          </div>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.Upload]: (
          <div class="flex items-center gap-1 text-xs whitespace-nowrap">
            {prettyBytesHelper(conn.upload)}
            <ArrowUpIcon class="text-info h-3 w-3" />
          </div>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed]: (
          <div class="flex items-center gap-1 text-xs whitespace-nowrap">
            {prettyBytesHelper(conn.downloadSpeed)}/s
            <ArrowDownCircleIcon class="text-success h-4 w-4" />
          </div>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed]: (
          <div class="flex items-center gap-1 text-xs whitespace-nowrap">
            {prettyBytesHelper(conn.uploadSpeed)}/s
            <ArrowUpCircleIcon class="text-info h-4 w-4" />
          </div>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.ConnectTime]: (
          <div class="gap-1 whitespace-nowrap">{fromNow(conn.start)}</div>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.DestinationType]: (
          <div class="gap-1 whitespace-nowrap">{getDestinationTypeFromConnection(conn)}</div>
        ),
        [CONNECTIONS_TABLE_ACCESSOR_KEY.InboundUser]: (
          <div class="gap-1 whitespace-nowrap">{getInboundUserFromConnection(conn)}</div>
        ),
      }
      const closeButton = (
        <button
          class="btn btn-circle btn-xs"
          aria-label={i18n.global.t('close')}
          onClick={(e) => {
            e.stopPropagation()
            disconnectByIdAPI(conn.id)
          }}
        >
          <XMarkIcon class="size-4" />
        </button>
      )
      const actions =
        metadata.smartBlock === 'normal' ? (
          <div class="flex shrink-0 gap-1">
            <button
              class="btn btn-circle btn-xs"
              aria-label={i18n.global.t('smartBlockConnection')}
              onClick={(e) => {
                e.stopPropagation()
                blockConnectionByIdAPI(conn.id)
              }}
            >
              <NoSymbolIcon class="size-4" />
            </button>
            {closeButton}
          </div>
        ) : (
          <div class="flex shrink-0 gap-1">{closeButton}</div>
        )

      // 每行固定三列;整行都是空的就跳过,全空时只显示目标
      const filledRows = connectionCardLines.value.filter((row) =>
        row.some((column) => column.length),
      )
      const rows: ConnectionCardRow[] = filledRows.length
        ? filledRows
        : [[[], [], [CONNECTIONS_TABLE_ACCESSOR_KEY.Host]]]
      const narrow = isMiddleScreen.value

      return (
        <div
          class="card app-card-inset flex cursor-pointer flex-row items-center gap-[var(--app-card-space)]"
          onClick={() => handlerInfo(conn)}
        >
          {actions}
          <div
            class={[
              'min-w-0 flex-1 gap-y-1',
              narrow ? 'flex flex-col' : ['grid gap-x-2', CARD_GRID_COLUMNS],
            ]}
          >
            {rows.map((row, rowIndex) => (
              <div
                key={rowIndex}
                class={[
                  'min-h-5 gap-2 text-sm',
                  narrow
                    ? 'flex items-center'
                    : [
                        'col-span-3 grid items-start supports-[grid-template-columns:subgrid]:grid-cols-subgrid',
                        CARD_GRID_COLUMNS,
                      ],
                ]}
              >
                {row.map((column, columnIndex) => {
                  // 同一格里已经有出站时,出站自己带路由策略,不再重复显示「路由设置」
                  const visible = column.filter(
                    (key) =>
                      key !== TARGET_ROUTING_COLUMN ||
                      (routingPolicy && !column.includes(CONNECTIONS_TABLE_ACCESSOR_KEY.Outbound)),
                  )
                  if (narrow && !visible.length) return null
                  const speedPair =
                    visible.length === 2 &&
                    visible.includes(CONNECTIONS_TABLE_ACCESSOR_KEY.DlSpeed) &&
                    visible.includes(CONNECTIONS_TABLE_ACCESSOR_KEY.UlSpeed)
                  return (
                    <div
                      key={columnIndex}
                      class={[
                        'flex min-w-0 items-center overflow-hidden',
                        CARD_COLUMN_ALIGN[columnIndex],
                        narrow && (speedPair ? 'flex-none' : 'flex-wrap'),
                        speedPair ? 'gap-x-2' : 'gap-x-1',
                        narrow && !speedPair && 'flex-auto',
                      ]}
                    >
                      {visible.map((key, index) => (
                        <div
                          key={key}
                          class="flex max-w-full min-w-0 items-center gap-1"
                        >
                          {index > 0 && !speedPair && (
                            <span
                              class="text-base-content/60"
                              aria-hidden="true"
                            >
                              ·
                            </span>
                          )}
                          {componentMap[key]}
                        </div>
                      ))}
                    </div>
                  )
                })}
              </div>
            ))}
          </div>
        </div>
      )
    }
  },
})
