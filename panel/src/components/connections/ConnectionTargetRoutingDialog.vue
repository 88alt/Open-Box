<template>
  <DialogWrapper
    v-model="isOpen"
    :title="$t('connectionEditTargetRouting')"
    box-class="w-full max-w-lg"
  >
    <div class="flex flex-col gap-[var(--app-card-space)]">
      <div class="flex items-center gap-2">
        <label
          class="shrink-0 text-sm"
          for="connection-routing-target"
          >{{ $t('connectionTarget') }}</label
        >
        <input
          id="connection-routing-target"
          v-model="input"
          class="input input-sm min-w-0 flex-1"
          autocomplete="off"
          spellcheck="false"
          :disabled="saving || pending.length > 0"
          @input="detectType(($event.target as HTMLInputElement).value)"
        />
        <select
          class="select select-sm w-auto shrink-0"
          :aria-label="$t('connectionTarget')"
          :value="type"
          :disabled="saving || pending.length > 0"
          @change="changeType"
        >
          <option
            v-for="item in typeOptions"
            :key="item"
            :value="item"
          >
            {{ $t(TYPE_LABELS[item]) }}
          </option>
        </select>
      </div>

      <section class="flex flex-col gap-2">
        <h3>{{ $t('connectionTargetRoutingMatches') }}</h3>
        <p class="text-base-content/60 text-sm">{{ $t('connectionTargetRoutingMatchOrder') }}</p>
        <p
          v-if="loading"
          class="text-base-content/65 text-xs"
          role="status"
        >
          {{ $t('connectionTargetRoutingLoading') }}
        </p>
        <template v-else-if="lookup">
          <ul
            v-if="shownMatches.length"
            class="divide-base-content/10 divide-y"
          >
            <li
              v-for="(match, index) in shownMatches"
              :key="index"
              class="flex items-center gap-2 py-1"
            >
              <ProxyName
                class="shrink-0"
                :name="match.name"
              />
              <div class="min-w-0 flex-1">
                <div class="truncate">
                  <template v-if="match.outbound"> → {{ match.outbound }}</template>
                </div>
                <div
                  class="text-base-content/65 truncate text-xs"
                  :title="match.value"
                >
                  {{ typeLabel(match.type) }} · {{ match.value }}
                </div>
              </div>
              <button
                type="button"
                class="btn btn-ghost btn-sm btn-square"
                :disabled="!match.removable || saving"
                :aria-label="$t('connectionTargetRoutingRemove')"
                :title="
                  $t(
                    match.removable
                      ? 'connectionTargetRoutingRemove'
                      : 'connectionTargetRoutingReadOnly',
                  )
                "
                @click="remove(match)"
              >
                <TrashIcon
                  class="size-4"
                  aria-hidden="true"
                />
              </button>
            </li>
          </ul>
          <p
            v-else-if="!lookup.incomplete"
            class="text-base-content/65 text-xs"
          >
            {{ $t('connectionTargetRoutingNoMatches') }}
          </p>
          <p
            v-if="lookup.incomplete"
            class="text-base-content/65 text-xs"
          >
            {{ $t('connectionTargetRoutingIncomplete') }}
          </p>
        </template>
      </section>

      <div class="flex flex-col gap-2">
        <span id="target-routing-policy-label">{{ $t('connectionTargetRoutingPolicy') }}</span>
        <div class="flex items-start gap-2">
          <div
            ref="pickerRef"
            class="min-w-0 flex-1"
            @keydown.esc.stop="pickerOpen = false"
          >
            <button
              type="button"
              class="btn btn-outline flex w-full justify-between font-normal"
              aria-labelledby="target-routing-policy-label target-routing-policy-value"
              :aria-expanded="pickerOpen"
              :disabled="!lookup || loading || saving"
              @click="pickerOpen = !pickerOpen"
            >
              <span
                id="target-routing-policy-value"
                class="flex min-w-0 items-center gap-2"
              >
                <ProxyName
                  v-if="selected"
                  :name="selected.name"
                />
                <span
                  v-else
                  class="truncate"
                  >{{ $t('connectionTargetRoutingNoSelection') }}</span
                >
              </span>
              <ChevronDownIcon class="size-4 shrink-0" />
            </button>
            <div
              v-if="pickerOpen"
              class="rounded-box border-base-content/10 bg-base-100 mt-2 max-h-48 overflow-y-auto border p-1"
            >
              <button
                v-for="policy in policies"
                :key="policy.id"
                type="button"
                class="btn btn-ghost flex w-full justify-start gap-2 font-normal"
                @click="pick(policy.id)"
              >
                <ProxyName :name="policy.name" />
              </button>
            </div>
          </div>
          <button
            type="button"
            class="btn"
            :disabled="!canAdd"
            @click="addPending"
          >
            {{ $t('connectionTargetRoutingAdd') }}
          </button>
        </div>
        <p
          v-if="error"
          class="text-error"
          role="alert"
        >
          {{ error }}
        </p>
        <p class="text-base-content/65 text-xs">{{ $t('connectionTargetRoutingHint') }}</p>
      </div>
    </div>
    <template #footer>
      <button
        type="button"
        class="btn btn-sm"
        :disabled="saving"
        @click="isOpen = false"
      >
        {{ $t('cancel') }}
      </button>
      <button
        type="button"
        class="btn btn-sm btn-primary"
        :disabled="!canSave"
        @click="save"
      >
        {{ $t('save') }}
      </button>
    </template>
  </DialogWrapper>
</template>

<script setup lang="ts">
import {
  addTargetRouting,
  fetchTargetRouting,
  removeTargetRouting,
  type OpenboxTargetRoutingApplied,
  type OpenboxTargetRoutingLookup,
  type OpenboxTargetRoutingMatch,
} from '@/api/openbox'
import { showNotification } from '@/helper/notification'
import { ChevronDownIcon, TrashIcon } from '@heroicons/vue/24/outline'
import { onClickOutside } from '@vueuse/core'
import ipaddr from 'ipaddr.js'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import DialogWrapper from '../common/DialogWrapper.vue'
import ProxyName from '../proxies/ProxyName.vue'

// 连接的目标(域名或 IP)。非空就打开,关掉时清空
const target = defineModel<string>({ required: true })
const { t } = useI18n()

const TYPE_LABELS: Record<string, string> = {
  domain: 'connectionTargetRoutingDomain',
  domain_suffix: 'connectionTargetRoutingSuffix',
  ip: 'connectionTargetRoutingIp',
  ip_cidr: 'connectionTargetRoutingCidr',
}
// 命中列表里的类型名:除了输入框那四种,规则里还有关键字、规则集链接
const typeLabel = (type: string) => {
  if (type in TYPE_LABELS) return t(TYPE_LABELS[type])
  if (type === 'domainSuffix') return t(TYPE_LABELS.domain_suffix)
  if (type === 'ipCidr') return t(TYPE_LABELS.ip_cidr)
  if (type === 'domainKeyword' || type === 'domain_keyword') return t('routingPolicyDomainKeyword')
  if (type === 'ruleUrl') return t('routingPolicyRuleUrl')
  return type
}
const isIPType = (value: string) => value === 'ip' || value === 'ip_cidr'

const input = ref('')
const type = ref('domain')
const typeOptions = computed(() =>
  isIPType(type.value) ? ['ip', 'ip_cidr'] : ['domain', 'domain_suffix'],
)

const lookup = ref<OpenboxTargetRoutingLookup>()
const policyId = ref('')
const pending = ref<string[]>([])
const pickerRef = ref<HTMLElement | null>(null)
const pickerOpen = ref(false)
onClickOutside(pickerRef, () => {
  pickerOpen.value = false
})
const loading = ref(false)
const saving = ref(false)
const error = ref('')
// 增删之后加一,触发重新查询
const refreshTick = ref(0)
// 输入框切到 IP 段时记一下原来的 IP,切回去能还原
let lastIP = ''
let lastCIDR = ''

const policies = computed(() => lookup.value?.policies.filter((policy) => policy.id) ?? [])
const selected = computed(() => policies.value.find((policy) => policy.id === policyId.value))

// 同一个分流组里,各类规则的先后(和内核生成路由规则的顺序一致)
const TYPE_ORDER = ['domain', 'domain_suffix', 'domain_keyword', 'ip_cidr']

// 命中列表 + 这次还没保存的新增项,新增项按「分流组先后、同组内规则类型先后」插到该在的位置
const shownMatches = computed(() => {
  const list = [...(lookup.value?.matches ?? [])]
  const addition = lookup.value?.addition

  if (!addition) return list

  const order = new Map(policies.value.map(({ id }, index) => [id, index]))
  const typeIndex = TYPE_ORDER.indexOf(addition.type)

  for (const id of pending.value) {
    const policy = policies.value.find((item) => item.id === id)

    if (!policy) continue

    const at = list.findIndex((match) => {
      if (match.source !== 'policy' && match.source !== 'ruleset') return false

      const matchOrder = order.get(match.policyId ?? '') ?? Infinity
      const newOrder = order.get(id) ?? Infinity

      return (
        matchOrder > newOrder ||
        (matchOrder === newOrder &&
          match.source === 'policy' &&
          TYPE_ORDER.indexOf(match.type) > typeIndex)
      )
    })

    list.splice(at < 0 ? list.length : at, 0, {
      source: 'policy',
      name: policy.name,
      policyId: id,
      ...addition,
      removable: true,
    })
  }

  return list
})

const canAdd = computed(() => {
  const addition = lookup.value?.addition

  return (
    !!selected.value &&
    !!addition &&
    !loading.value &&
    !saving.value &&
    !shownMatches.value.some(
      (match) =>
        match.source === 'policy' &&
        match.policyId === policyId.value &&
        match.type === addition.type &&
        match.value === addition.value,
    )
  )
})
const canSave = computed(
  () => !!lookup.value && pending.value.length > 0 && !loading.value && !saving.value,
)

const addPending = () => {
  if (!canAdd.value) return
  pending.value = [...pending.value, policyId.value]
  policyId.value = ''
  pickerOpen.value = false
}
const pick = (id: string) => {
  policyId.value = id
  pickerOpen.value = false
}

const isOpen = computed({
  get: () => !!target.value,
  set: (open) => {
    if (!open && !saving.value) target.value = ''
  },
})

const validIPv4 = (value: string) => ipaddr.IPv4.isValidFourPartDecimal(value)
const validIPv6 = (value: string) => !value.includes('%') && ipaddr.IPv6.isValid(value)
const stripBrackets = (value: string) => value.trim().replace(/^\[(.*)\]$/, '$1')

// 输入的是 IP / IP 段就把类型切过去,不是了(之前是 IP 类)就切回域名
const detectType = (value: string) => {
  const text = value.trim()

  if (!text) return

  const host = stripBrackets(text.split('/')[0])

  if (validIPv4(host) || validIPv6(host)) type.value = text.includes('/') ? 'ip_cidr' : 'ip'
  else if (isIPType(type.value)) type.value = 'domain'
}

const changeType = (event: Event) => {
  const value = (event.target as HTMLSelectElement).value
  const text = stripBrackets(input.value)

  if (value === 'ip_cidr' && (validIPv4(text) || validIPv6(text))) {
    lastIP = text
    lastCIDR = validIPv4(text)
      ? `${ipaddr.IPv4.networkAddressFromCIDR(`${text}/24`)}/24`
      : `${ipaddr.IPv6.networkAddressFromCIDR(`${text}/64`)}/64`
    input.value = lastCIDR
  } else if (value === 'ip') {
    input.value = text === lastCIDR ? lastIP : text.split('/')[0]
  }
  type.value = value
}

// 打开(目标变了)就重置,并按目标判断类型
watch(
  target,
  (value) => {
    pending.value = []
    pickerOpen.value = false
    input.value = value
    type.value = 'domain'
    lastIP = lastCIDR = ''
    detectType(value)
  },
  { flush: 'sync' },
)

const errorText = (err: unknown) => {
  const status = (err as { status?: number } | null)?.status

  if (status === 404) return t('connectionTargetRoutingUnsupported')
  if (status === 400 && !saving.value) return t('connectionTargetRoutingInvalid')

  return err instanceof Error ? err.message : String(err)
}

// 输入、类型、增删之后重新查询;输入停 180ms 再发,切换时把上一次的请求取消
watch([target, input, type, refreshTick], ([original, text, kind], _old, onCleanup) => {
  lookup.value = undefined
  policyId.value = ''
  error.value = ''
  loading.value = !!(original && text.trim())

  if (!loading.value) return

  const controller = new AbortController()
  const timer = setTimeout(async () => {
    try {
      const result = await fetchTargetRouting(text.trim(), kind, original, controller.signal)

      if (!controller.signal.aborted) lookup.value = result
    } catch (err) {
      if (!controller.signal.aborted) error.value = errorText(err)
    } finally {
      if (!controller.signal.aborted) loading.value = false
    }
  }, 180)

  onCleanup(() => {
    clearTimeout(timer)
    controller.abort()
  })
})

// 保存 / 删除之后的提示:内核没应用上就提示「已保存,内核尚未应用」
const finish = async (
  run: () => Promise<{ applied?: OpenboxTargetRoutingApplied }>,
  close: boolean,
) => {
  saving.value = true
  error.value = ''
  try {
    const { applied } = await run()
    const deferred =
      !applied?.ok ||
      applied.pendingRestart?.pending ||
      (applied.skipped && applied.skipped !== 'unchanged')

    showNotification({
      content: deferred ? 'connectionTargetRoutingDeferred' : 'connectionTargetRoutingSaved',
      type: deferred ? 'alert-warning' : 'alert-success',
    })
    if (close) target.value = ''
    else refreshTick.value++
  } catch (err) {
    error.value = errorText(err)
  } finally {
    saving.value = false
  }
}

const save = () => {
  if (canSave.value)
    void finish(() => addTargetRouting(input.value.trim(), type.value, pending.value), true)
}

const remove = (match: OpenboxTargetRoutingMatch) => {
  if (!match.removable || saving.value) return

  const addition = lookup.value?.addition

  // 还没保存的新增项:只从待加列表里去掉,不调接口
  if (
    match.source === 'policy' &&
    match.policyId &&
    pending.value.includes(match.policyId) &&
    match.type === addition?.type &&
    match.value === addition?.value
  ) {
    pending.value = pending.value.filter((id) => id !== match.policyId)

    return
  }
  void finish(() => removeTargetRouting(match), false)
}
</script>
