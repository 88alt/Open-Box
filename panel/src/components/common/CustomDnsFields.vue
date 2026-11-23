<template>
  <div class="flex flex-col gap-1">
    <div class="flex flex-wrap items-center gap-2">
      <DnsProtocolSelect v-model="protocol" />
      <input
        v-model.trim="endpoint.server"
        type="text"
        class="input input-sm min-w-0 flex-1 font-mono"
        :placeholder="hostIsName ? 'dns.example.net' : $t('subscriptionNodeDnsIpPlaceholder')"
        :aria-label="
          $t(
            protocol === 'https'
              ? 'dnsUpstreamDohHost'
              : protocol === 'tls'
                ? 'dnsUpstreamDotHost'
                : 'subscriptionNodeDnsIpPlaceholder',
          )
        "
        autocomplete="off"
        spellcheck="false"
        @change="splitAddress"
        @paste="onPaste"
      />
      <input
        v-model.number="endpoint.port"
        type="number"
        min="1"
        max="65535"
        class="input input-sm w-20 shrink-0"
        :aria-label="$t('port')"
      />
      <input
        v-if="needsBootstrap"
        v-model.trim="endpoint.bootstrap"
        type="text"
        class="input input-sm w-full font-mono sm:w-48"
        :placeholder="$t('dnsUpstreamEncryptedBootstrap')"
        :aria-label="$t('dnsUpstreamEncryptedBootstrap')"
        autocomplete="off"
        spellcheck="false"
      />
    </div>
    <p
      v-if="error"
      class="text-error text-xs"
    >
      {{ $t(error) }}
    </p>
  </div>
</template>

<script setup lang="ts">
import DnsProtocolSelect from '@/components/common/DnsProtocolSelect.vue'
import { dnsDraftError, dnsDraftNeedsBootstrap, type DnsDraft } from '@/helper/customDns'
import { parseDohInput, parseDotInput, parseEndpointUrl } from '@/helper/dohAddress'
import type { DnsProtocol } from '@/helper/dnsProtocol'
import { computed } from 'vue'

// 自定义 DNS 服务器的输入:协议 + 域名或 IP + 端口,域名要解析时再多一个引导 DNS(IP)。
// 草稿由调用方用 createDnsDraft 建、用 dnsDraftValue 取值;这里只改草稿里的字段。
const props = defineProps<{ draft: DnsDraft }>()

const protocol = computed<DnsProtocol>({
  get: () => props.draft.protocol,
  set: (value) => {
    // eslint-disable-next-line vue/no-mutating-props -- 草稿是调用方传进来让这里直接编辑的
    props.draft.protocol = value
  },
})
const endpoint = computed(() => props.draft.endpoints[props.draft.protocol])
const hostIsName = computed(() => protocol.value === 'https' || protocol.value === 'tls')
const needsBootstrap = computed(() => dnsDraftNeedsBootstrap(props.draft))
const error = computed(() => dnsDraftError(props.draft))

// 框里敲 / 粘贴的是完整的 https:// 或 tls:// 地址时,按地址里的协议切换并拆成域名、端口(DoH 的路径原样保留);
// 否则按当前协议认:DoH 认 host[:port][/path],DoT 认 host:port。认不出来就原样留着让用户继续改。
// onApply 在确认要改草稿时先调用一次(粘贴时用来拦下浏览器默认的粘贴)。
const applyAddress = (text: string, onApply?: () => void) => {
  const url = parseEndpointUrl(text)
  if (url) {
    onApply?.()
    // eslint-disable-next-line vue/no-mutating-props -- 草稿是调用方传进来让这里直接编辑的
    props.draft.protocol = url.protocol
    const address = url.protocol === 'tls' ? { server: url.address.server, port: url.address.port } : url.address
    // eslint-disable-next-line vue/no-mutating-props -- 同上
    Object.assign(props.draft.endpoints[url.protocol], address)
    return
  }
  if (protocol.value === 'https') {
    const parsed = parseDohInput(text, endpoint.value.port)
    if (parsed) {
      onApply?.()
      Object.assign(endpoint.value, parsed)
    }
    return
  }
  if (protocol.value === 'tls') {
    const parsed = parseDotInput(text, endpoint.value.port)
    if (parsed) {
      onApply?.()
      Object.assign(endpoint.value, { server: parsed.server, port: parsed.port })
    }
  }
}
const splitAddress = () => applyAddress(endpoint.value.server)
const onPaste = (event: ClipboardEvent) =>
  applyAddress(event.clipboardData?.getData('text') ?? '', () => event.preventDefault())
</script>
