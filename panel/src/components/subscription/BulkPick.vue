<template>
  <!-- 全选 / 反选 / 清空选择 + 右端的订阅多选筛选。三个按钮作用于「当前筛选与过滤框
       框出来的那一批」(范围由父组件算好传进事件里),所以筛选和它们放在同一行:
       选中一条订阅再按「全选」,加进来的正好是那条订阅的节点。 -->
  <div class="flex items-center gap-1">
    <button
      type="button"
      class="btn btn-ghost btn-xs"
      @click="$emit('selectAll')"
    >
      {{ $t('groupSelectAll') }}
    </button>
    <button
      type="button"
      class="btn btn-ghost btn-xs"
      @click="$emit('invert')"
    >
      {{ $t('groupInvert') }}
    </button>
    <button
      type="button"
      class="btn btn-ghost btn-xs"
      @click="$emit('clear')"
    >
      {{ $t('groupSelectNone') }}
    </button>
    <SubscriptionFilter
      class="ml-auto w-32 max-w-[45%]"
      :model-value="subscription"
      :options="options"
      @update:model-value="$emit('update:subscription', $event)"
    />
  </div>
</template>

<script setup lang="ts">
import SubscriptionFilter from '@/components/subscription/SubscriptionFilter.vue'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  // 节点实际来自的订阅名(去重后)。为空时筛选里只有「全部」。
  subscriptions: string[]
  // 选中的筛选值,可多选;空数组 = 全部
  subscription: string[]
  // 候选里只有节点(故障转移):不给「全部节点组 / 全部节点」两项
  nodesOnly?: boolean
  // 只给出其中几类('group' / 'node');不传 = 两类都给
  kinds?: ('group' | 'node')[]
}>()

defineEmits<{
  selectAll: []
  invert: []
  clear: []
  'update:subscription': [string[]]
}>()

const { t } = useI18n()

// 值带前缀是为了不和订阅名撞车:订阅名是用户随便起的,直接拿名字当值的话,
// 有人把订阅命名成「全部节点」就分不出这一项到底指哪个了。
const options = computed(() => [
  ...(!props.nodesOnly && (!props.kinds || props.kinds.includes('group'))
    ? [{ value: 'kind:group', label: t('groupFilterAllGroups') }]
    : []),
  ...(!props.nodesOnly && (!props.kinds || props.kinds.includes('node'))
    ? [{ value: 'kind:node', label: t('groupFilterAllNodes') }]
    : []),
  ...props.subscriptions.map((name) => ({ value: `sub:${name}`, label: name })),
])
</script>
