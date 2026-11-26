<template>
  <section class="card bg-base-100 border-base-300/60 border">
    <div class="card-body gap-3 p-4 text-sm">
      <div class="flex flex-col gap-1">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <h2
            id="dns-query-log-title"
            class="text-base font-semibold"
          >
            {{ $t('dfRecords') }}
          </h2>
          <form
            class="flex flex-wrap gap-2"
            @submit.prevent="searchRecords"
          >
            <!-- 上游是「relative 容器 + join-item w-full 的 text 输入框」:宽度还是 w-52,
                 但 type=text 不会带浏览器自带的搜索清除按钮(Chrome 的 search 输入框会多一个 ✕) -->
            <div class="relative w-52">
              <input
                v-model="search"
                type="text"
                class="input join-item input-sm w-full"
                :placeholder="$t('dfSearch')"
                :aria-label="$t('dfSearch')"
              />
            </div>
            <select
              v-model="result"
              class="select select-sm w-32"
              :aria-label="$t('dfResult')"
              @change="searchRecords"
            >
              <option value="">{{ $t('dfAllResults') }}</option>
              <option value="blocked">{{ $t('dfBlocked') }}</option>
              <option value="allowed">{{ $t('dfAllowed') }}</option>
              <option value="dnsmasq">{{ $t('dfDnsmasqAnswered') }}</option>
              <option value="error">{{ $t('dfError') }}</option>
              <option value="unknown">{{ $t('dfUnknown') }}</option>
            </select>
            <button
              v-tip="$t('dfQuery')"
              class="btn btn-ghost btn-square btn-sm"
              :disabled="loading"
              type="submit"
              :aria-label="$t('dfQuery')"
            >
              <ArrowPathIcon class="h-4 w-4" />
            </button>
          </form>
        </div>
        <p class="text-base-content/60 text-xs">{{ $t('dfRecordHint') }}</p>
      </div>
      <p
        v-if="(queryLog?.reason === 'user' && !queryLog?.syslog) || queryLog?.reason === 'failed'"
        class="text-base-content/60 text-xs"
      >
        {{ $t(queryLog.reason === 'failed' ? 'dfQueryLogFailed' : 'dfQueryLogUser') }}
      </p>
      <p
        v-if="!enabled || !connected"
        class="text-base-content/60 text-xs"
      >
        {{ $t(!enabled ? 'dfCollectDisabled' : 'dfDisconnected') }}
      </p>
      <!-- 表格可横向滚动,给一个可聚焦的 region(和上游一致):读屏能按「域名解析查询」定位,
           键盘也能聚焦后用方向键横向滚动 -->
      <div
        class="app-plain-table focus-visible:outline-primary overflow-x-auto focus-visible:outline-2 focus-visible:-outline-offset-2"
        role="region"
        aria-labelledby="dns-query-log-title"
        tabindex="0"
      >
        <table class="table-sm table">
          <thead>
            <tr>
              <th>{{ $t('dfTime') }}</th>
              <th>{{ $t('dfDomain') }}</th>
              <th>{{ $t('dfType') }}</th>
              <th>{{ $t('dfResult') }}</th>
              <th>{{ $t('dfSource') }}</th>
              <th>{{ $t('dfMatchedList') }}</th>
              <th>{{ $t('dfDuration') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in rows"
              :key="row.id"
            >
              <td class="text-xs whitespace-nowrap">{{ new Date(row.at).toLocaleString() }}</td>
              <td
                class="max-w-72 truncate font-mono text-xs"
                :title="row.domain"
              >
                {{ row.domain }}
              </td>
              <td>{{ row.qtype }}</td>
              <td>
                <span
                  class="badge badge-sm"
                  :class="
                    row.result === 'blocked'
                      ? 'badge-error badge-soft'
                      : row.result === 'allowed'
                        ? 'badge-success badge-soft'
                        : row.result === 'cached' || row.result === 'local'
                          ? 'badge-info badge-soft'
                          : 'badge-ghost'
                  "
                  >{{ resultLabel(row.result) }}</span
                >
              </td>
              <td class="text-xs">
                {{
                  row.source === '127.0.0.1' || row.source === '::1'
                    ? $t(row.via === 'dnsmasq' ? 'dfRouterSelf' : 'dfRouterSource')
                    : row.source || '—'
                }}
              </td>
              <td>{{ row.list || '—' }}</td>
              <td class="tabular-nums">
                {{ row.elapsed === null ? '—' : `${Math.round(row.elapsed)} ms` }}
              </td>
            </tr>
            <tr v-if="!rows.length">
              <td
                colspan="7"
                class="text-base-content/50 py-10 text-center"
              >
                {{ $t(loading ? 'dfLoading' : 'dfNoRecords') }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <AppPagination
        v-model:page="page"
        v-model:page-size="pageSize"
        :total="total"
        :page-sizes="[10, 20, 50, 100]"
        :disabled="loading"
        @change="load"
      />
    </div>
  </section>
</template>
<script setup lang="ts">
import { fetchDnsFilterRecords, type DnsFilterQueryLog, type DnsFilterRecord } from '@/api/openbox'
import AppPagination from '@/components/common/AppPagination.vue'
import { showNotification } from '@/helper/notification'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
defineProps<{ connected: boolean; enabled: boolean; queryLog?: DnsFilterQueryLog }>()
const route = useRoute(),
  { t } = useI18n()
const search = ref(String(route.query.dnsDomain || '')),
  result = ref('blocked'),
  page = ref(1),
  pageSize = ref(10),
  total = ref(0),
  loading = ref(false)
const rows = ref<DnsFilterRecord[]>([])
let serial = 0
const load = async () => {
  const id = ++serial
  loading.value = true
  try {
    const data = await fetchDnsFilterRecords(search.value, result.value, page.value, pageSize.value)
    if (id === serial) {
      rows.value = data.rows
      total.value = data.total
      page.value = data.page
      pageSize.value = data.pageSize
    }
  } catch (e) {
    if (id === serial)
      showNotification({
        content: 'routeTestRequestFailed',
        params: { message: e instanceof Error ? e.message : String(e) },
        key: 'dns-filter-records',
        type: 'alert-error',
      })
  } finally {
    if (id === serial) loading.value = false
  }
}
const resultLabel = (r: string) =>
  ({
    blocked: t('dfBlocked'),
    allowed: t('dfAllowed'),
    error: t('dfError'),
    unknown: t('dfUnknown'),
    policy: t('dfPolicy'),
    cached: t('dfCached'),
    local: t('dfLocal'),
  })[r] || r.toUpperCase()
const searchRecords = () => {
  page.value = 1
  load()
}
onMounted(load)
</script>
