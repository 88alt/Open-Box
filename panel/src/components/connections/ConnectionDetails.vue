<template>
  <DialogWrapper
    v-model="connectionDetailModalShow"
    :title="$t('connectionDetails')"
    :box-class="proxyChainStart ? `max-w-256` : `max-w-128`"
  >
    <div class="flex flex-col md:flex-row">
      <div class="md:w-128">
        <div
          class="tabs-box tabs tabs-xs mb-2 w-fit"
          role="tablist"
        >
          <button
            type="button"
            role="tab"
            class="tab"
            :class="{ 'tab-active': !showJSON }"
            :aria-selected="!showJSON"
            @click="showJSON = false"
          >
            {{ $t('connectionFormatted') }}
          </button>
          <button
            type="button"
            role="tab"
            class="tab"
            :class="{ 'tab-active': showJSON }"
            :aria-selected="showJSON"
            @click="showJSON = true"
          >
            {{ $t('connectionJSON') }}
          </button>
        </div>
        <VueJsonPretty
          v-if="showJSON"
          :data="infoConn"
          class="overflow-y-auto"
        >
          <template #renderNodeValue="{ node, defaultValue }">
            <template v-if="node.path.startsWith('root.chains') && proxyMap[node.content]?.icon">
              <span
                >"<ProxyIcon
                  :icon="proxyMap[node.content].icon"
                  :size="16"
                  :scale="proxyMap[node.content].iconScale"
                  class="inline-block"
                  :margin="0"
                />
                {{ node.content }}"
              </span>
            </template>
            <template v-else>
              {{ defaultValue }}
            </template>
          </template>
        </VueJsonPretty>
        <dl
          v-else
          class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm"
        >
          <template
            v-for="[key, value] in formattedRows"
            :key="key"
          >
            <dt class="text-base-content/65">{{ $t(key) }}</dt>
            <dd class="break-all">{{ value }}</dd>
          </template>
          <template v-if="infoConn">
            <dt class="text-base-content/65">{{ $t('outbound') }}</dt>
            <dd class="flex min-w-0 flex-wrap items-center gap-1">
              <template v-if="routingPolicy">
                <ProxyName :name="routingPolicy" />
                <span v-if="nodeChain.length">·</span>
              </template>
              <template
                v-for="(name, index) in nodeChain"
                :key="index"
              >
                <ArrowRightIcon
                  v-if="index > 0"
                  class="size-4 shrink-0"
                />
                <ProxyName :name="name" />
              </template>
              <span v-if="!routingPolicy && !nodeChain.length">—</span>
            </dd>
          </template>
        </dl>
        <div
          class="min-h-12 shrink-0 pt-2 text-sm"
          v-if="!showJSON && destinationIP && !isPrivateIP"
        >
          <template v-if="details">
            <div class="flex flex-wrap items-center gap-1">
              <ArrowRightCircleIcon class="h-4 w-4 shrink-0" />
              <div>
                {{ details?.ip }}
              </div>
              <div>( AS{{ details?.asn }} )</div>
            </div>
            <div class="flex flex-wrap">
              <div
                class="mr-3 flex items-center gap-1"
                v-if="details?.country"
              >
                <MapPinIcon class="h-4 w-4 shrink-0" />
                <template v-if="details?.city && details?.city !== details?.country">
                  {{ details?.city }},
                </template>
                <template v-else-if="details?.region && details?.region !== details?.country">
                  {{ details?.region }},
                </template>
                {{ details?.country }}
              </div>
              <div class="flex items-center gap-1">
                <ServerIcon class="h-4 w-4 shrink-0" />
                {{ details?.organization }}
              </div>
            </div>
          </template>
        </div>
      </div>
      <template v-if="proxyChainStart">
        <div class="divider md:divider-horizontal m-0"></div>
        <div class="md:w-128">
          <ProxyGroup
            :name="proxyChainStart"
            class="transparent-collapse"
          />
        </div>
      </template>
    </div>
  </DialogWrapper>
</template>

<script setup lang="ts">
import { getIPInfo, type IPInfo } from '@/api/geoip'
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import { useConnections } from '@/composables/connections'
import {
  getHostFromConnection,
  getNetworkTypeFromConnection,
  getProcessFromConnection,
} from '@/helper'
import { getIPLabelFromMap } from '@/helper/sourceip'
import { prettyBytesHelper } from '@/helper/utils'
import { proxyMap } from '@/store/proxies'
import {
  ArrowRightCircleIcon,
  ArrowRightIcon,
  MapPinIcon,
  ServerIcon,
} from '@heroicons/vue/24/outline'
import dayjs from 'dayjs'
import * as ipaddr from 'ipaddr.js'
import { last } from 'lodash'
import { computed, ref, watch } from 'vue'
import VueJsonPretty from 'vue-json-pretty'
import 'vue-json-pretty/lib/styles.css'
import ProxyGroup from '../proxies/ProxyGroup.vue'
import ProxyIcon from '../proxies/ProxyIcon.vue'
import ProxyName from '../proxies/ProxyName.vue'

const { infoConn, connectionDetailModalShow } = useConnections()
const details = ref<IPInfo | null>(null)
// 默认看格式化视图,每次打开弹窗都回到它;JSON 视图给要看原始字段的人
const showJSON = ref(false)
watch(connectionDetailModalShow, (show) => {
  if (show) showJSON.value = false
})

const formattedRows = computed<[string, string][]>(() => {
  const conn = infoConn.value
  if (!conn) return []
  const { metadata } = conn
  const started = dayjs(conn.start)
  const rows: [string, string][] = [
    ['host', getHostFromConnection(conn)],
    ['sourceIP', getIPLabelFromMap(metadata.sourceIP)],
    ['sourcePort', metadata.sourcePort],
    ['type', getNetworkTypeFromConnection(conn)],
    ['rule', [conn.rule, conn.rulePayload].filter(Boolean).join(' · ')],
    ['process', getProcessFromConnection(conn)],
    [
      'connectionStarted',
      conn.start && started.isValid() ? started.format('YYYY-MM-DD HH:mm:ss') : '',
    ],
    ['download', prettyBytesHelper(conn.download)],
    ['upload', prettyBytesHelper(conn.upload)],
    ['downloadSpeed', `${prettyBytesHelper(conn.downloadSpeed ?? 0)}/s`],
    ['uploadSpeed', `${prettyBytesHelper(conn.uploadSpeed ?? 0)}/s`],
  ]
  return rows.filter(([, value]) => value)
})

// 出口:链上最后一个是路由策略组,前面倒过来是实际经过的节点(与连接卡片一致)
const routingPolicy = computed(() => {
  const chains = infoConn.value?.chains ?? []
  return chains.length > 1 ? last(chains) : undefined
})
const nodeChain = computed(() => {
  const chains = infoConn.value?.chains ?? []
  return [...(routingPolicy.value ? chains.slice(0, -1) : chains)].reverse()
})

const destinationIP = computed(() => infoConn.value?.metadata.destinationIP)
const isPrivateIP = computed(() => {
  if (!destinationIP.value || !ipaddr.isValid(destinationIP.value)) {
    return false
  }

  const addr = ipaddr.parse(destinationIP.value)
  const range = addr.range()

  return ['private', 'uniqueLocal', 'loopback', 'linkLocal'].includes(range)
})

const proxyChainStart = computed(() => {
  if (!infoConn.value?.chains || !infoConn.value.chains.length) {
    return null
  }

  return last(infoConn.value.chains)
})

watch(
  () => destinationIP.value,
  (newIP) => {
    if (!newIP) {
      return
    }

    if (isPrivateIP.value) {
      details.value = null
      return
    }

    if (details.value?.ip === newIP) {
      return
    }

    details.value = null
    getIPInfo(infoConn.value?.metadata.destinationIP).then((res) => {
      details.value = res
    })
  },
)
</script>
