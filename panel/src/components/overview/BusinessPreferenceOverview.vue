<template>
  <section
    class="card bg-base-100 border-base-300/60 app-card-inset min-w-0 border"
    data-business-preference-overview
  >
    <div class="flex items-center gap-2">
      <h2 class="text-base font-semibold">{{ $t('businessPreference') }}</h2>
      <HelpTrigger
        :label="$t('bandwidthSettingsHelpLabel', { title: $t('businessPreference') })"
        @click="openHelp"
      />
      <button
        v-if="enabled.length"
        type="button"
        class="btn btn-ghost btn-xs ml-auto font-normal"
        :aria-expanded="expanded"
        aria-controls="business-preference-groups"
        @click="expanded = !expanded"
      >
        {{ $t(expanded ? 'businessOverviewCollapse' : 'businessOverviewExpand') }}
        <ChevronUpIcon
          v-if="expanded"
          class="h-4 w-4"
        />
        <ChevronDownIcon
          v-else
          class="h-4 w-4"
        />
      </button>
    </div>

    <!-- 有分组开着业务优选:一行汇总(开启数 + 改选条数)、一行按原因拆分、一行最近一次 -->
    <div
      v-if="data && enabled.length"
      class="mt-1 flex flex-col gap-1 text-sm"
    >
      <div class="flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <span>{{ $t('businessOverviewEnabled', { count: enabled.length }) }}</span>
        <strong
          v-if="totals"
          class="tabular-nums"
        >
          {{
            $t('businessOverviewHits', {
              minutes: data.windowMinutes,
              count: totals.reliability + totals.performance,
            })
          }}
        </strong>
        <span
          v-else
          class="text-base-content/60"
          >{{ $t('businessOverviewNoStats') }}</span
        >
      </div>
      <p
        v-if="totals"
        class="text-base-content/60 text-xs"
      >
        {{ $t('businessOverviewBreakdown', totals) }}
      </p>
      <p
        v-if="latest"
        class="text-base-content/70 text-xs break-words"
      >
        {{ $t('businessOverviewLatest', { group: latest.name, ...describe(latest.hit) }) }}
      </p>
    </div>

    <!-- 一个分组都没开:点去分组设置打开 -->
    <div
      v-else-if="data"
      class="mt-1 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-sm"
    >
      <span class="text-base-content/60">{{ $t('businessOverviewOff') }}</span>
      <RouterLink
        class="link link-primary"
        :to="GROUPS_SETTINGS"
        >{{ $t('businessOverviewSettings') }}</RouterLink
      >
    </div>

    <p
      v-else
      class="text-base-content/60 mt-1 text-sm"
    >
      {{ $t(failed ? 'businessOverview_unavailable' : 'businessOverviewLoading') }}
    </p>

    <ExpandTransition>
      <ul
        v-if="expanded && data && enabled.length"
        id="business-preference-groups"
        class="app-card-gap mt-3 grid grid-cols-1"
      >
        <li
          v-for="group in enabled"
          :key="group.id"
          class="app-card-inset bg-base-100 border-base-300/60 rounded-box flex min-w-0 flex-col gap-1 border text-sm"
        >
          <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <span class="min-w-0 font-medium break-words">{{ group.name }}</span>
            <span
              class="text-xs"
              :class="group.state === 'running' ? 'text-success' : 'text-base-content/60'"
            >
              {{ $t(`businessOverview_${group.state}`) }}
            </span>
          </div>
          <p
            v-if="group.lane"
            class="text-base-content/60 text-xs break-words"
          >
            {{ $t('businessOverviewLane', { name: group.lane }) }}
          </p>
          <p
            v-if="group.reliability !== null && group.performance !== null"
            class="text-base-content/60 text-xs"
          >
            {{ $t('businessOverviewWindow', { minutes: data.windowMinutes }) }} ·
            {{
              $t('businessOverviewBreakdown', {
                reliability: group.reliability,
                performance: group.performance,
              })
            }}
          </p>
          <p
            v-if="group.last"
            class="text-base-content/70 text-xs break-words"
          >
            {{ $t('businessOverviewSwitch', describe(group.last)) }}
          </p>
        </li>
      </ul>
    </ExpandTransition>

    <!-- 说明弹窗:这张卡的问号按钮点开,内容和上游同款(条数口径、例子、统计窗口) -->
    <DialogWrapper
      v-model="helpOpen"
      :title="$t('businessPreference')"
      header-class="pr-10!"
    >
      <template #footer>
        <button
          ref="closeHelp"
          type="button"
          class="btn btn-sm"
          @click="helpOpen = false"
        >
          {{ $t('close') }}
        </button>
      </template>
      <div class="flex flex-col gap-3 text-sm leading-relaxed">
        <div class="bg-base-200 flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-lg p-3">
          <strong v-if="data">
            {{
              enabled.length
                ? $t('businessOverviewEnabled', { count: enabled.length })
                : $t('businessOverviewOff')
            }}
          </strong>
          <RouterLink
            class="link link-primary"
            :to="GROUPS_SETTINGS"
            @click="helpOpen = false"
          >
            {{ $t('businessOverviewSettings') }}
          </RouterLink>
        </div>
        <p>{{ $t('businessOverviewHelpCount') }}</p>
        <p>{{ $t('businessOverviewHelpExample') }}</p>
        <p>{{ $t('businessOverviewHelpWindow') }}</p>
      </div>
    </DialogWrapper>
  </section>
</template>

<script setup lang="ts">
import {
  fetchBusinessPreferenceStatus,
  type OpenboxBusinessPreferenceGroup,
  type OpenboxBusinessPreferenceHit,
  type OpenboxBusinessPreferenceStatus,
} from '@/api/openbox'
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import ExpandTransition from '@/components/common/ExpandTransition.vue'
import HelpTrigger from '@/components/common/HelpTrigger.vue'
import { ChevronDownIcon, ChevronUpIcon } from '@heroicons/vue/24/outline'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const { t, locale } = useI18n()

// 分组设置页:业务优选的开关在分组编辑器里(和上游同一个去处)
const GROUPS_SETTINGS = { path: '/settings', query: { tab: 'groups' } }
// 上游每 30 秒拉一次;页面切到后台就停,回来立刻补一次
const INTERVAL = 30000

const data = ref<OpenboxBusinessPreferenceStatus | null>(null)
const failed = ref(false)
const expanded = ref(false)
const helpOpen = ref(false)
const closeHelp = ref<HTMLButtonElement | null>(null)

const enabled = computed<OpenboxBusinessPreferenceGroup[]>(() => data.value?.groups ?? [])
// 两张卡都可能给出统计;只要有一组统计不到,总数就不显示,免得少算
const totals = computed(() => {
  const sum = { reliability: 0, performance: 0 }
  for (const group of enabled.value) {
    if (group.reliability === null || group.performance === null) return null
    sum.reliability += group.reliability
    sum.performance += group.performance
  }
  return sum
})
const latest = computed(() => {
  let hit: { name: string; hit: OpenboxBusinessPreferenceHit } | undefined
  for (const group of enabled.value) {
    const last = group.last
    if (!last) continue
    if (!hit || last.at > hit.hit.at) hit = { name: group.name, hit: last }
  }
  return hit
})

const timeFormat = computed(() => new Intl.DateTimeFormat(locale.value, { timeStyle: 'medium' }))
// 「最近改选」那一行的参数:时间、起止节点、原因(原因文案按 reliability / performance 取)
const describe = (hit: OpenboxBusinessPreferenceHit) => ({
  from: hit.from,
  to: hit.to,
  time: timeFormat.value.format(hit.at),
  reason: t(`businessOverviewReason_${hit.reason}`),
})

let controller: AbortController | null = null
let timer: ReturnType<typeof setInterval> | null = null

const load = async () => {
  if (controller || document.hidden) return
  const current = new AbortController()
  controller = current
  try {
    const status = await fetchBusinessPreferenceStatus(current.signal)
    if (current.signal.aborted) return
    data.value = status
    failed.value = false
  } catch {
    if (!current.signal.aborted) {
      data.value = null
      failed.value = true
    }
  } finally {
    controller = null
  }
}

// 说明弹窗关掉后把焦点还给问号按钮
let helpTrigger: HTMLElement | null = null
const openHelp = (e: Event) => {
  helpTrigger = e.currentTarget as HTMLElement
  helpOpen.value = true
}
watch(helpOpen, (open) => {
  if (open) return
  nextTick(() => (helpTrigger ?? closeHelp.value)?.focus({ preventScroll: true }))
})

const onVisibility = () => {
  if (!document.hidden) void load()
}
onMounted(() => {
  void load()
  timer = setInterval(() => void load(), INTERVAL)
  document.addEventListener('visibilitychange', onVisibility)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
  document.removeEventListener('visibilitychange', onVisibility)
  controller?.abort()
})
</script>
