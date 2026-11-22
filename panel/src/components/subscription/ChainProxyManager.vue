<template>
  <!-- 链式代理页(对齐 v0.1.311):没有标题卡片;添加按钮 Teleport 到顶部页签栏右上角的「+」
       (和共享网络页一样);空的时候中间再放一个图标和「添加链式代理」按钮,否则直接是列表。 -->
  <div class="flex flex-col gap-2">
    <Teleport
      defer
      to="#settings-header-actions"
    >
      <button
        type="button"
        class="btn btn-primary btn-circle btn-sm"
        v-tip="$t('chainProxyAdd')"
        :aria-label="$t('chainProxyAdd')"
        @click="openEditor(null)"
      >
        <PlusIcon class="h-4 w-4" />
      </button>
    </Teleport>

    <div
      v-if="loading"
      class="flex justify-center py-16"
    >
      <span class="loading loading-spinner loading-sm" />
    </div>

    <div
      v-else-if="!chains.length"
      class="flex flex-col items-center gap-3 py-14 text-center"
    >
      <LinkIcon class="text-base-content/30 h-10 w-10" />
      <p class="text-base-content/60 text-sm">
        {{ $t('chainProxyEmptyHint') }}
      </p>
      <button
        type="button"
        class="btn btn-primary btn-sm"
        @click="openEditor(null)"
      >
        <PlusIcon class="h-4 w-4" />
        {{ $t('chainProxyAdd') }}
      </button>
    </div>

    <template v-else>
      <div class="card bg-base-100 border-base-300/60 border">
        <div class="card-body gap-2 p-4">
          <div
            v-for="chain in chains"
            :key="chain.id"
            class="border-base-content/10 flex flex-row items-center gap-2 rounded-lg border p-3"
            :class="chain.enabled === false && 'opacity-50'"
          >
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span class="max-w-full truncate text-base font-medium">{{ chain.name }}</span>
                <span class="badge badge-outline badge-sm shrink-0 whitespace-nowrap">{{ $t('chainProxyVia', { upstream: chain.upstream }) }}</span>
                <!-- 逐条测速的结果(点右边的闪电):延迟 + 出口 IP。没进内核的链路是单独发请求测的,延迟会偏高 -->
                <template v-if="rowInfos[chain.id] && !testingIds.has(chain.id)">
                  <span
                    v-if="rowInfos[chain.id].ok"
                    class="text-xs font-medium"
                    :class="latencyClass(rowInfos[chain.id].ms)"
                    v-tip="inKernel(chain) ? '' : $t('chainProxyLatencyNotInKernel')"
                  >{{ rowInfos[chain.id].ms }} ms</span>
                  <span
                    v-else
                    class="text-error shrink-0 text-xs"
                    v-tip="rowInfos[chain.id].error"
                  >{{ $t('chainProxyTestFailed') }}</span>
                  <span
                    v-if="rowInfos[chain.id].ipText"
                    class="text-base-content/70 min-w-0 truncate text-xs"
                  >{{ rowInfos[chain.id].ipText }}</span>
                  <span
                    v-else-if="rowInfos[chain.id].ipFailed"
                    class="text-warning shrink-0 text-xs"
                    v-tip="rowInfos[chain.id].ipError"
                  >{{ $t('chainProxyIpUnknown') }}</span>
                </template>
                <StatusBadge
                  v-if="chain.enabled === false"
                  :on="false"
                  on-text=""
                  :off-text="$t('groupDisabledBadge')"
                />
                <span
                  v-if="upstreamMissing(chain)"
                  class="text-warning text-xs"
                >{{ $t('chainProxyUpstreamMissing', { name: chain.upstream }) }}</span>
              </div>
              <div class="text-base-content/60 mt-0.5 truncate text-xs">{{ summary(chain) }}</div>
            </div>
            <button
              type="button"
              class="btn btn-ghost btn-square btn-sm"
              :class="chain.enabled === false ? 'text-base-content/40' : 'text-success'"
              :aria-label="$t(chain.enabled === false ? 'groupEnable' : 'groupDisable')"
              v-tip="$t(chain.enabled === false ? 'groupEnable' : 'groupDisable')"
              :disabled="saving"
              @click="toggleEnabled(chain)"
            >
              <PowerIcon class="h-4 w-4" />
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-square btn-sm"
              :aria-label="$t('chainProxyTest')"
              v-tip="$t('chainProxyTest')"
              :disabled="testingIds.has(chain.id)"
              @click="testRow(chain)"
            >
              <span
                v-if="testingIds.has(chain.id)"
                class="loading loading-spinner loading-xs"
              />
              <BoltIcon
                v-else
                class="h-4 w-4"
              />
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-square btn-sm"
              :aria-label="$t('groupEdit')"
              @click="openEditor(chain)"
            >
              <PencilSquareIcon class="h-4 w-4" />
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-square btn-sm hover:text-error"
              :aria-label="$t('delete')"
              v-tip="$t('delete')"
              @click="askDelete(chain)"
            >
              <TrashIcon class="h-4 w-4" />
            </button>
          </div>
        </div>
      </div>
    </template>

    <DialogWrapper
      v-model="showEditor"
      :title="$t(editing ? 'chainProxyEditTitle' : 'chainProxyAddTitle')"
      box-class="w-full max-w-xl"
    >
      <div class="flex flex-col gap-4 text-sm">
        <div class="flex flex-col gap-1">
          <label class="text-xs font-medium">{{ $t('chainProxyNameLabel') }}</label>
          <input
            v-model="draft.name"
            type="text"
            maxlength="64"
            class="input input-sm w-full"
            :placeholder="$t('chainProxyNamePlaceholder')"
          />
        </div>

        <!-- 节点:「链接」粘一条分享链接 / 节点配置,也认「用户名:密码@主机:端口」;「字段」按住宅代理商表格里的账号一栏栏填。
             协议(HTTP / HTTPS / SOCKS5)两种填法共用,保存时拼成链接 -->
        <div class="flex flex-col gap-1">
          <div class="flex flex-wrap items-center gap-2">
            <label class="text-xs font-medium">{{ $t('chainProxyLinkLabel') }}</label>
            <div
              role="tablist"
              v-segmented
              class="tabs-box tabs tabs-xs w-fit"
            >
              <button
                v-for="m in MODES"
                :key="m.value"
                type="button"
                role="tab"
                class="tab"
                :class="mode === m.value && 'tab-active'"
                :aria-selected="mode === m.value"
                @click="setMode(m.value)"
              >{{ $t(m.label) }}</button>
            </div>
            <div
              role="tablist"
              v-segmented
              class="tabs-box tabs tabs-xs ml-auto w-fit"
            >
              <button
                v-for="s in SCHEMES"
                :key="s.value"
                type="button"
                role="tab"
                class="tab"
                :class="fields.scheme === s.value && 'tab-active'"
                :aria-selected="fields.scheme === s.value"
                @click="setScheme(s.value)"
              >{{ s.label }}</button>
            </div>
          </div>

          <template v-if="mode === 'link'">
            <textarea
              :value="draft.link"
              rows="4"
              spellcheck="false"
              class="textarea textarea-sm w-full font-mono"
              :placeholder="$t('chainProxyLinkPlaceholder')"
              @input="onLinkInput"
            />
            <p class="text-base-content/50 text-xs">{{ $t('chainProxyLinkHint') }}</p>
          </template>
          <template v-else>
            <div class="grid grid-cols-[minmax(0,1fr)_6rem] gap-2">
              <input
                v-model.trim="fields.host"
                type="text"
                spellcheck="false"
                class="input input-sm w-full font-mono"
                :placeholder="$t('chainProxyHostLabel')"
              />
              <input
                v-model.trim="fields.port"
                type="text"
                inputmode="numeric"
                class="input input-sm w-full font-mono"
                :placeholder="$t('chainProxyPortLabel')"
              />
            </div>
            <div class="grid grid-cols-2 gap-2">
              <input
                v-model="fields.username"
                type="text"
                autocomplete="off"
                spellcheck="false"
                class="input input-sm w-full font-mono"
                :placeholder="$t('chainProxyUserLabel')"
              />
              <PasswordInput
                v-model="fields.password"
                autocomplete="off"
                spellcheck="false"
                class="w-full font-mono"
                :placeholder="$t('chainProxyPassLabel')"
              />
            </div>
          </template>
        </div>

        <div class="flex flex-col gap-1">
          <div class="flex items-center justify-between gap-2">
            <label class="text-xs font-medium">{{ $t('chainProxyUpstreamLabel') }}</label>
            <button
              type="button"
              class="btn btn-ghost btn-xs shrink-0 gap-1 font-normal"
              :aria-controls="dnsPanelId"
              :aria-expanded="dnsOpen"
              @click="dnsOpen = !dnsOpen"
            >
              {{ $t('clientRouteDnsLabel') }}
              <ChevronDownIcon
                class="h-3.5 w-3.5 transition-transform"
                :class="{ 'rotate-180': dnsOpen }"
                aria-hidden="true"
              />
            </button>
          </div>
          <OutboundPicker
            v-model="draft.upstream"
            :options="pickerOptions"
            :placeholder="$t('outboundPickerPlaceholder')"
          />
          <p class="text-base-content/50 text-xs">{{ $t('chainProxyHint') }}</p>
          <div
            v-show="dnsOpen"
            :id="dnsPanelId"
            class="flex flex-col gap-1"
          >
            <CustomDnsFields :draft="dnsDraft" />
            <p class="text-base-content/50 text-xs">{{ $t('chainProxyDnsHint') }}</p>
          </div>
        </div>

        <!-- 测速:结果和按钮同一行,出口信息只占一行小字 -->
        <div class="flex flex-col gap-1">
          <div class="flex flex-wrap items-center gap-2">
            <LatencyPill
              :ms="testResult?.ok ? (testResult.ms ?? 0) : undefined"
              :error="testResult && !testResult.ok ? $t('chainProxyTestFailed') : undefined"
              :loading="testing"
              :test-label="$t('chainProxyTest')"
              @click="runTest"
            />
            <template v-if="testResult && !testing">
              <span
                v-if="!testResult.ok"
                class="text-error text-xs break-all"
              >{{ testResult.error }}</span>
              <span
                v-if="testResult.via"
                class="text-base-content/50 text-xs"
              >{{ $t('chainProxyTestVia', { via: testResult.via }) }}</span>
              <a
                v-if="exitIp"
                :href="ipDetailUrl(exitIp.ip)"
                target="_blank"
                rel="noopener noreferrer"
                class="link link-hover ml-auto inline-flex shrink-0 items-center gap-0.5 text-xs"
                v-tip="$t('chainProxyIpDetailTip', { site: IP_DETAIL_SITE })"
              >
                {{ $t('chainProxyIpDetail') }}
                <ArrowTopRightOnSquareIcon class="h-3 w-3" />
              </a>
            </template>
          </div>
          <div
            v-if="testResult && !testing && ipSummary"
            class="text-base-content/70 text-xs break-all"
          >
            {{ ipSummary }}
          </div>
          <p class="text-base-content/50 text-xs">{{ $t('chainProxyTestHint') }}</p>
        </div>
      </div>

      <!-- 取消 / 保存放在弹窗底栏,和内容区分开 -->
      <template #footer>
        <button
          type="button"
          class="btn btn-sm"
          @click="showEditor = false"
        >
          {{ $t('cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="saving"
          @click="saveDraft"
        >
          <span
            v-if="saving"
            class="loading loading-spinner loading-xs"
          />
          {{ $t('subscriptionSave') }}
        </button>
      </template>
    </DialogWrapper>

    <DialogWrapper
      v-model="showDelete"
      :title="$t('chainProxyTab')"
    >
      <div class="flex flex-col gap-4 p-2">
        <p class="text-sm">{{ $t('chainProxyDeleteConfirm', { name: pendingDelete?.name || '' }) }}</p>
        <div class="flex justify-end gap-2">
          <button
            type="button"
            class="btn btn-sm"
            @click="showDelete = false"
          >
            {{ $t('cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-error btn-sm"
            :disabled="saving"
            @click="confirmDelete"
          >
            <span
              v-if="saving"
              class="loading loading-spinner loading-xs"
            />
            {{ $t('confirm') }}
          </button>
        </div>
      </div>
    </DialogWrapper>
  </div>
</template>

<script setup lang="ts">
import { fetchNodeGroups, fetchProfile, saveProfile, testChainLatency } from '@/api/openbox'
import type { OpenboxChainLatencyResult, OpenboxChainProxy } from '@/api/openbox'
import CustomDnsFields from '@/components/common/CustomDnsFields.vue'
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import LatencyPill from '@/components/common/LatencyPill.vue'
import OutboundPicker from '@/components/common/OutboundPicker.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import { createDnsDraft, dnsDraftError, dnsDraftValue } from '@/helper/customDns'
import { showNotification } from '@/helper/notification'
import { speedtestTimeout, speedtestUrl } from '@/store/settings'
import {
  ArrowTopRightOnSquareIcon,
  BoltIcon,
  LinkIcon,
  PencilSquareIcon,
  PlusIcon,
  PowerIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'
import { ChevronDownIcon } from '@heroicons/vue/24/outline'
import { computed, onMounted, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const chains = ref<OpenboxChainProxy[]>([])
const groupNames = ref<string[]>([])
// 所有已知的节点 / 节点组名(含已停用、内置的),判断上游是否还在
const knownNames = ref<Set<string>>(new Set())
const nodeOptions = ref<Array<{ name: string; subscription: string }>>([])
const loading = ref(true)
const saving = ref(false)

const errorMessage = (error: unknown) => (error instanceof Error ? error.message : String(error))

const load = async () => {
  try {
    const [profile, payload] = await Promise.all([fetchProfile(), fetchNodeGroups()])
    chains.value = profile.chainProxies ?? []
    // 内置的直连 / 拒绝、已停用的组不能当上游
    groupNames.value = payload.groups.filter((g) => !g.kind && g.enabled !== false).map((g) => g.name)
    nodeOptions.value = payload.availableNodes
    knownNames.value = new Set([...payload.groups.map((g) => g.name), ...payload.availableNodes.map((n) => n.name)])
  } catch (error) {
    showNotification({ content: 'routingLoadFailed', params: { message: errorMessage(error) }, type: 'alert-error' })
  } finally {
    loading.value = false
  }
}
onMounted(load)

// 上游已经在名字后面的「经 xxx」徽标里了,这一行只写节点本身
const summary = (chain: OpenboxChainProxy) =>
  chain.node ? `${chain.node.type} · ${chain.node.server}:${chain.node.port}` : ''

// 上游「已不存在」:既不是节点 / 节点组,也不是另一条链式代理
const upstreamMissing = (chain: OpenboxChainProxy) =>
  !!chain.upstream &&
  !knownNames.value.has(chain.upstream) &&
  !chains.value.some((c) => c.id !== chain.id && c.name === chain.upstream)

// 这条链路有没有进内核:启用着、上游也还在才进
const inKernel = (chain: OpenboxChainProxy) => chain.enabled !== false && !upstreamMissing(chain)

// 整份写回;服务端校验不过(重名、上游自己、节点内容解析失败等)会回一句话,原样提示。
// 发出去的只留可写的字段,node 摘要由服务端按 link 重算
const persist = async (next: OpenboxChainProxy[]) => {
  saving.value = true
  try {
    const plain = next.map(({ id, enabled, name, link, upstream, dns }) => ({
      id,
      enabled,
      name,
      link,
      upstream,
      ...(dns?.url ? { dns } : {}),
    }))
    const profile = await saveProfile({ chainProxies: plain })
    chains.value = profile.chainProxies ?? []
    showNotification({ content: 'chainProxySaved', type: 'alert-success' })
    return true
  } catch (error) {
    showNotification({ content: 'saveFailed', params: { message: errorMessage(error) }, type: 'alert-error' })
    return false
  } finally {
    saving.value = false
  }
}

const toggleEnabled = (chain: OpenboxChainProxy) => {
  void persist(chains.value.map((c) => (c.id === chain.id ? { ...c, enabled: c.enabled === false } : c)))
}

// ---------- 节点的两种填法 ----------
type Mode = 'link' | 'fields'
type Scheme = 'http' | 'https' | 'socks5'
const MODES: { value: Mode; label: string }[] = [
  { value: 'link', label: 'chainProxyModeLink' },
  { value: 'fields', label: 'chainProxyModeFields' },
]
const SCHEMES: { value: Scheme; label: string }[] = [
  { value: 'http', label: 'HTTP' },
  { value: 'https', label: 'HTTPS' },
  { value: 'socks5', label: 'SOCKS5' },
]
const emptyFields = () => ({ scheme: 'http' as Scheme, host: '', port: '', username: '', password: '' })
const mode = ref<Mode>('fields')
const fields = ref(emptyFields())

const safeDecode = (text: string) => {
  try {
    return decodeURIComponent(text)
  } catch {
    return text
  }
}

// 链接开头的协议;socks / socks5h 都算 SOCKS5。没有协议头(或是 ss:// vless:// 这类)返回 undefined
const SCHEME_PREFIX = /^(https?|socks5h?|socks):\/\//i
const schemeOf = (link: string): Scheme | undefined => {
  const name = SCHEME_PREFIX.exec(link.trim())?.[1]?.toLowerCase()
  return name === 'http' || name === 'https' ? name : name ? 'socks5' : undefined
}

// 没带协议头的简写补成完整链接:「主机:端口」「用户名:密码@主机:端口」「主机:端口:用户名:密码」;认不出来返回 ''
const BARE_HOST = /^[A-Za-z0-9.-]+$|^\[[0-9A-Fa-f:.]+\]$/
const BARE_PORT = /^\d{1,5}$/
const completeBare = (text: string, scheme: Scheme): string => {
  if (/\s|:\/\//.test(text)) return ''
  const enc = encodeURIComponent
  const at = text.lastIndexOf('@')
  if (at > 0) {
    const cred = text.slice(0, at)
    const addr = text.slice(at + 1)
    const colon = addr.lastIndexOf(':')
    if (colon <= 0) return ''
    const host = addr.slice(0, colon)
    const port = addr.slice(colon + 1)
    const split = cred.indexOf(':')
    if (!BARE_HOST.test(host) || !BARE_PORT.test(port) || split <= 0) return ''
    return `${scheme}://${enc(cred.slice(0, split))}:${enc(cred.slice(split + 1))}@${host}:${port}`
  }
  const parts = text.split(':')
  if (parts.length === 2 && BARE_HOST.test(parts[0]) && BARE_PORT.test(parts[1])) return `${scheme}://${parts[0]}:${parts[1]}`
  if (parts.length >= 4 && BARE_HOST.test(parts[0]) && BARE_PORT.test(parts[1]) && parts[2]) {
    return `${scheme}://${enc(parts[2])}:${enc(parts.slice(3).join(':'))}@${parts[0]}:${parts[1]}`
  }
  return ''
}

// 链接 / 简写拆成字段;不是「协议://[账号@]主机:端口」这种形状(ss:// vless:// 或一段配置)就拆不了
const LINK_RE = /^(?:https?|socks5h?|socks):\/\/(?:([^@/?#]*)@)?(\[[^\]]+\]|[^:/?#@]+):(\d{1,5})(?:[/?#].*)?$/i
const parseLink = (raw: string, fallback: Scheme = 'http') => {
  const text = raw.trim()
  const link = completeBare(text, fallback) || text
  const scheme = schemeOf(link)
  const m = LINK_RE.exec(link)
  if (!scheme || !m) return null
  const auth = m[1] ?? ''
  const split = auth.indexOf(':')
  return {
    scheme,
    host: m[2],
    port: m[3],
    username: safeDecode(split >= 0 ? auth.slice(0, split) : auth),
    password: split >= 0 ? safeDecode(auth.slice(split + 1)) : '',
  }
}
const composeLink = (): string => {
  const f = fields.value
  const auth = f.username || f.password ? `${encodeURIComponent(f.username.trim())}:${encodeURIComponent(f.password)}@` : ''
  return `${f.scheme}://${auth}${f.host.trim()}:${f.port.trim()}`
}

// 「链接」里手输 / 粘贴时,协议头跟着协议标签走
const onLinkInput = (event: Event) => {
  draft.value.link = (event.target as HTMLTextAreaElement).value
  fields.value.scheme = schemeOf(draft.value.link) ?? fields.value.scheme
}

// 切协议标签:「链接」里已经带着协议头的,把协议头一并换掉
const setScheme = (next: Scheme) => {
  if (mode.value === 'link' && schemeOf(draft.value.link) && next !== fields.value.scheme) {
    draft.value.link = draft.value.link.trim().replace(SCHEME_PREFIX, `${next}://`)
  }
  fields.value.scheme = next
}

const setMode = (next: Mode) => {
  if (mode.value === next) return
  if (next === 'fields') {
    // 链接拆得开就拆进字段,拆不开字段清空(协议留着)
    fields.value = parseLink(draft.value.link, fields.value.scheme) ?? { ...emptyFields(), scheme: fields.value.scheme }
  } else if (fields.value.host.trim() && fields.value.port.trim()) {
    // 字段填了主机和端口就把链接带过去,免得切过去是一片空白;能写成简写的就写简写
    const full = composeLink()
    const f = fields.value
    const bare = `${f.username || f.password ? `${f.username.trim()}:${f.password}@` : ''}${f.host.trim()}:${f.port.trim()}`
    draft.value.link = completeBare(bare, f.scheme) === full ? bare : full
  }
  mode.value = next
}

// 当前这种填法下要保存 / 测速的链接;填得不对返回要提示的文案键。先查链接本身,再查上游
const resolveLink = (): { link: string } | { error: string } => {
  let link = draft.value.link.trim()
  if (mode.value === 'fields') {
    if (!fields.value.host.trim()) return { error: 'chainProxyErrHost' }
    const port = Number(fields.value.port.trim())
    if (!/^\d{1,5}$/.test(fields.value.port.trim()) || port < 1 || port > 65535) return { error: 'chainProxyErrPort' }
    link = composeLink()
  } else {
    link = completeBare(link, fields.value.scheme) || link
  }
  if (!link) return { error: 'chainProxyErrLink' }
  return draft.value.upstream.trim() ? { link } : { error: 'chainProxyErrUpstream' }
}

// ---------- 编辑 ----------
const showEditor = ref(false)
const editing = ref<OpenboxChainProxy | null>(null)
const draft = ref({ id: '', enabled: true, name: '', link: '', upstream: '' })
// 自定义 DNS:终端直选这个节点时用。默认收起,已设置过就展开;校验没过也会展开
const dnsDraft = ref(createDnsDraft())
const dnsOpen = ref(false)
const dnsPanelId = `chain-dns-${useId()}`
const testing = ref(false)
const testResult = ref<OpenboxChainLatencyResult | null>(null)

const openEditor = (chain: OpenboxChainProxy | null) => {
  editing.value = chain
  draft.value = chain
    ? { id: chain.id, enabled: chain.enabled !== false, name: chain.name, link: chain.link, upstream: chain.upstream }
    : { id: `chain-${Date.now().toString(36)}`, enabled: true, name: '', link: '', upstream: '' }
  dnsDraft.value = createDnsDraft(chain?.dns)
  dnsOpen.value = !!chain?.dns?.url
  // 新建默认按字段填;编辑时链接是标准的 http / socks5 就拆成字段,否则留在「链接」里
  const link = chain?.link ?? ''
  const parsed = link ? parseLink(link) : null
  fields.value = parsed ?? { ...emptyFields(), scheme: schemeOf(link) ?? 'http' }
  mode.value = parsed || !link ? 'fields' : 'link'
  testResult.value = null
  showEditor.value = true
}

// 上游可选:节点、节点组;别的链式代理也能当上游(服务端会查环),自己不行
const pickerOptions = computed(() => {
  const self = editing.value?.name
  const seen = new Set<string>()
  const nodes: Array<{ name: string; subscription: string }> = []
  const push = (name: string, subscription: string) => {
    if (!name || name === self || seen.has(name)) return
    seen.add(name)
    nodes.push({ name, subscription })
  }
  for (const n of nodeOptions.value) push(n.name, n.subscription)
  for (const c of chains.value) if (c.id !== draft.value.id) push(c.name, t('chainProxyTab'))
  return { builtin: [], groups: groupNames.value.filter((name) => name !== self), nodes }
})

const saveDraft = async () => {
  const name = draft.value.name.trim()
  if (!name) return showNotification({ content: 'chainProxyErrName', type: 'alert-error' })
  const resolved = resolveLink()
  if ('error' in resolved) return showNotification({ content: resolved.error, type: 'alert-error' })
  const { link } = resolved
  const upstream = draft.value.upstream.trim()
  if (upstream === name) return showNotification({ content: 'chainProxyErrSelf', type: 'alert-error' })
  const dnsError = dnsDraftError(dnsDraft.value)
  if (dnsError) {
    dnsOpen.value = true
    return showNotification({ content: dnsError, type: 'alert-error' })
  }
  const dns = dnsDraftValue(dnsDraft.value)
  const item: OpenboxChainProxy = {
    id: draft.value.id,
    enabled: draft.value.enabled,
    name,
    link,
    upstream,
    ...(dns ? { dns } : {}),
  }
  const next = editing.value ? chains.value.map((c) => (c.id === item.id ? item : c)) : [...chains.value, item]
  if (await persist(next)) {
    delete rowTests.value[item.id]
    showEditor.value = false
  }
}

// ---------- 删除 ----------
const showDelete = ref(false)
const pendingDelete = ref<OpenboxChainProxy | null>(null)
const askDelete = (chain: OpenboxChainProxy) => {
  pendingDelete.value = chain
  showDelete.value = true
}
const confirmDelete = async () => {
  const target = pendingDelete.value
  if (!target) return
  if (await persist(chains.value.filter((c) => c.id !== target.id))) {
    delete rowTests.value[target.id]
    showDelete.value = false
    pendingDelete.value = null
  }
}

// ---------- 测速 / IP 地区 ----------
// 后端只认这几家的主机名,顺序即兜底顺序
const ipUrls = () => ['https://api.ip.sb/geoip', 'https://ipwho.is', 'https://api.ipapi.is']

// 延迟的颜色:越慢越偏红
const latencyClass = (ms: number) => (ms < 300 ? 'text-success' : ms < 800 ? 'text-warning' : 'text-error')

// 经这条链路发一次测速请求;后端的 IP 接口白名单和这里对不上时只丢掉 IP 信息,延迟照测
type ChainTestBase = Omit<Parameters<typeof testChainLatency>[0], 'ipUrls'>
const requestChainTest = async (base: ChainTestBase) => {
  try {
    return await testChainLatency({ ...base, ipUrls: ipUrls() })
  } catch (error) {
    if (!/ipUrls/.test(errorMessage(error))) throw error
    return await testChainLatency(base)
  }
}

const runTest = async () => {
  if (testing.value) return
  const resolved = resolveLink()
  if ('error' in resolved) return showNotification({ content: resolved.error, type: 'alert-error' })
  const { link } = resolved
  const upstream = draft.value.upstream.trim()
  testing.value = true
  testResult.value = null
  try {
    testResult.value = await requestChainTest({
      link,
      upstream,
      testUrl: speedtestUrl.value,
      timeoutMs: speedtestTimeout.value,
    })
  } catch (error) {
    testResult.value = { via: '', ok: false, error: errorMessage(error) }
  } finally {
    testing.value = false
  }
}

// 后端把 IP 信息接口的正文原样交回,这里按答话那一家的格式取出 IP、国家、运营商
const parseExitIp = (ip: NonNullable<OpenboxChainLatencyResult['ip']>) => {
  try {
    const data = JSON.parse(ip.body || '{}')
    const host = new URL(ip.url || '').hostname
    const [country, org] =
      host === 'api.ipapi.is'
        ? [data.location?.country, data.asn?.org]
        : host === 'ipwho.is'
          ? [data.country, data.connection?.org]
          : [data.country, data.organization]
    const address = String(data.ip ?? '')
    return address ? { ip: address, place: [country, org].filter(Boolean).join(' ') } : null
  } catch {
    return null
  }
}

const ipSummary = computed(() => {
  const ip = testResult.value?.ip
  if (!ip) return ''
  if (!ip.ok) return t('chainProxyIpFailed', { error: ip.error || '' })
  const exit = parseExitIp(ip)
  return exit ? `${t('chainProxyExitIp')} ${exit.ip} · ${exit.place}` : ''
})

// 测速通过后,在 IP 详情站点查看这个出口 IP(和节点信息面板用同一个站点)
const IP_DETAIL_SITE = 'ping0.cc'
const ipDetailUrl = (ip: string) => `https://${IP_DETAIL_SITE}/ip/${encodeURIComponent(ip)}`
const exitIp = computed(() => {
  const ip = testResult.value?.ip
  return ip?.ok ? parseExitIp(ip) : null
})

// ---------- 列表里逐条测速 ----------
interface RowInfo {
  ok: boolean
  ms: number
  error: string
  ipText: string
  ipFailed: boolean
  ipError: string
}
const rowTests = ref<Record<string, OpenboxChainLatencyResult>>({})
const testingIds = ref<Set<string>>(new Set())

const rowInfos = computed(() => {
  const infos: Record<string, RowInfo> = {}
  for (const [id, result] of Object.entries(rowTests.value)) {
    const exit = result.ip?.ok ? parseExitIp(result.ip) : null
    infos[id] = {
      ok: !!result.ok,
      ms: result.ms ?? 0,
      error: result.error ?? '',
      ipText: exit ? [exit.ip, exit.place].filter(Boolean).join(' · ') : '',
      ipFailed: !!result.ip && !result.ip.ok,
      ipError: result.ip?.error ?? '',
    }
  }
  return infos
})

const testRow = async (chain: OpenboxChainProxy) => {
  if (testingIds.value.has(chain.id)) return
  testingIds.value = new Set(testingIds.value).add(chain.id)
  try {
    rowTests.value[chain.id] = await requestChainTest({
      link: chain.link,
      upstream: chain.upstream,
      testUrl: speedtestUrl.value,
      timeoutMs: speedtestTimeout.value,
    })
  } catch (error) {
    rowTests.value[chain.id] = { via: '', ok: false, error: errorMessage(error) }
  } finally {
    const rest = new Set(testingIds.value)
    rest.delete(chain.id)
    testingIds.value = rest
  }
}
</script>
