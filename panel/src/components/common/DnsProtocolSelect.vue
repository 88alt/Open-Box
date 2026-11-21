<template>
  <select
    v-model="protocol"
    class="select select-sm w-20 shrink-0"
    :aria-label="$t('dnsUpstreamProtocol')"
  >
    <option
      v-for="item in DNS_PROTOCOLS"
      :key="item"
      :value="item"
    >
      {{ dnsProtocolName(item) }}
    </option>
  </select>
</template>

<script setup lang="ts">
import { DNS_PROTOCOLS, dnsProtocolName, type DnsProtocol } from '@/helper/dnsProtocol'

// DNS 协议下拉:UDP / TCP / DoT / DoH
const protocol = defineModel<DnsProtocol>({ required: true })
</script>
