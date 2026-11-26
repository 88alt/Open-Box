<template>
  <!-- 两行网格:上面是固定的控制区(前缀、总开关、命名模板、地区关键词标题),下面是地区列表等
       内容的独立滚动区。列表再长,上面的开关和模板也一直在眼前,滚动条只出现在列表那一块,
       与上游 v0.1.382 一致。第一行 minmax(0,max-content):控制区矮时按内容高度,太高时自己也能滚;
       第二行至少留 4rem,不会被挤没。 -->
  <div class="grid min-h-0 flex-1 grid-rows-[minmax(0,max-content)_minmax(4rem,1fr)] gap-4">
    <div class="flex flex-col gap-4 overflow-y-auto">
      <!-- 两个开关并排(上游 488 的结构):左边「节点名前加订阅名」、紧跟「重命名节点」,恢复默认推到最右。
           前缀存的是开关而不是文本:存文本的话,用户改了订阅名,前缀还留着旧名字。
           它和重命名规则无关(关了重命名也能加前缀),所以放在重命名总开关前面。 -->
      <div class="app-inline-gap flex flex-wrap items-center gap-y-2">
        <div class="app-inline-gap flex min-w-0 items-center">
          <label class="flex cursor-pointer items-center gap-2 text-sm">
            <span>{{ $t('subscriptionRenamePrefixLabel') }}</span>
            <input
              v-model="usePrefix"
              type="checkbox"
              class="toggle toggle-primary toggle-xs shrink-0"
              role="switch"
            >
          </label>
          <!-- 重命名总开关:关掉时下面的规则整块收起、节点保留机场原始名字 -->
          <label class="flex cursor-pointer items-center gap-2 text-sm">
            <span>{{ $t('subscriptionRenameEditorTitle') }}</span>
            <input
              v-model="enabled"
              type="checkbox"
              class="toggle toggle-primary toggle-xs shrink-0"
              role="switch"
            >
          </label>
        </div>
        <button
          v-if="enabled"
          type="button"
          class="btn btn-ghost btn-xs ml-auto shrink-0"
          @click="resetToDefaults"
        >
          <ArrowUturnLeftIcon class="h-3.5 w-3.5" />
          {{ $t('subscriptionRenameRestoreDefaults') }}
        </button>
      </div>
      <!-- Template + unknown label + seq padding -->
      <div
        v-if="enabled"
        class="flex flex-col gap-2"
      >
        <!-- 命名模板不再是一个要手写 {region}-{feature}-{seq} 的文本框:那对小白等于
             没说。改成三块可拖拽的牌子,拖出来的先后顺序就是节点名的组成顺序,分隔符
             固定用 "-"。下面那行实时显示算出来的样子。
             代价:自定义分隔符和模板里夹带的固定文字不再可编辑(旧配置若有,会在下次
             保存时按当前顺序规范化)。换来的是这一屏不用再解释什么叫占位符。 -->
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-base-content/50 text-xs">{{ $t('subscriptionRenameTokensHint') }}</span>
          <!-- force-fallback:走 Sortable 自己的指针实现,不用浏览器原生 HTML5 拖放。
               原生拖放会拖出一张浏览器自绘的半透明快照(小徽章上尤其难看),而且在触屏
               上行为不一致;fallback 模式拖的是 Sortable 复制出来的真实元素。 -->
          <Draggable
            v-model="tokenOrder"
            :animation="150"
            :force-fallback="true"
            :fallback-tolerance="5"
            :fallback-on-body="true"
            class="flex flex-wrap items-center gap-1.5"
            ghost-class="opacity-40"
            :item-key="(item: string) => item"
          >
            <template #item="{ element: token }">
              <span
                class="badge badge-outline badge-sm cursor-move select-none"
                :title="token"
              >
                <Bars3Icon class="mr-1 h-3 w-3 opacity-50" />
                {{ $t(TOKEN_LABELS[token]) }}
              </span>
            </template>
          </Draggable>

          <!-- 序号位数收成一行、推到最右:它是给序号补零的位数(不补零时 美国-10 会
               排在 美国-2 前面,因为节点名到处都按文本排序)。
               控件与上游 488 一致:label + join 步进器(减按钮 / 数字框 / 加按钮),只在一个
               1~4 的小范围里选,单独一个数字框还不如给两个按钮。 -->
          <div class="ml-auto flex items-center gap-1.5 text-xs">
            <label
              :for="seqPadId"
              class="text-base-content/50"
            >{{ $t('subscriptionRenameSeqPadLabel') }}:</label>
            <div class="join inline-flex max-w-full items-stretch">
              <button
                type="button"
                class="btn btn-sm join-item w-10 shrink-0 px-0"
                :aria-label="$t('numberDecrease', { label: $t('subscriptionRenameSeqPadLabel') })"
                @click="bumpSeqPad(-1)"
              >
                −
              </button>
              <label class="input input-sm join-item flex w-20 min-w-0 justify-center gap-0">
                <input
                  :id="seqPadId"
                  v-model.number="seqPad"
                  type="number"
                  min="1"
                  max="4"
                  class="tabular-nums text-center"
                  :aria-label="$t('subscriptionRenameSeqPadLabel')"
                  :title="$t('subscriptionRenameSeqPadHint')"
                >
              </label>
              <button
                type="button"
                class="btn btn-sm join-item w-10 shrink-0 px-0"
                :aria-label="$t('numberIncrease', { label: $t('subscriptionRenameSeqPadLabel') })"
                @click="bumpSeqPad(1)"
              >
                +
              </button>
            </div>
          </div>
        </div>
        <!-- 效果示例:重命名关着时显示机场原始名字的样子,前缀开着时带上订阅名 -->
        <p class="text-base-content text-xs">
          {{ $t('subscriptionRenameTemplateExample', { example: templateExample }) }}
        </p>
      </div>

    </div>

    <div class="min-h-0 flex-1 overflow-y-auto">
      <div class="flex flex-col gap-4">
        <!-- Region dictionary:关了重命名也要留着——地区识别给国旗和按地区选成员的节点组用。
             上游 488 把它放在下面这块可滚动区的最上面(控制区只留开关、命名模板和效果)。 -->
        <div class="flex flex-col gap-2">
          <div class="flex items-center justify-between">
            <h3 class="font-medium text-xs">{{ $t('subscriptionRenameRegionDictLabel') }}</h3>
            <button
              type="button"
              class="btn btn-ghost btn-xs"
              @click="addRegionRow"
            >
              <PlusIcon class="h-3.5 w-3.5" />
              {{ $t('subscriptionRenameAddRow') }}
            </button>
          </div>
          <p class="text-base-content/50 text-xs">{{ $t('subscriptionRenameRegionDictHint') }}</p>
        </div>
        <div class="flex flex-col gap-2">
          <!-- 顺序即优先级:matchRegion 返回第一个命中的地区,所以排在前面的先生效。
               比如「香港」排在「中国」前面,节点名「中国香港 01」才会被认成香港。
               handle 限定成那个图标:不限定的话按住输入框拖会变成拖行,连字都选不了。 -->
          <Draggable
            v-model="regionRows"
            :animation="150"
            :force-fallback="true"
            :fallback-tolerance="5"
            :fallback-on-body="true"
            handle=".drag-handle"
            ghost-class="opacity-40"
            item-key="id"
            class="flex flex-col gap-1.5"
          >
            <template #item="{ element: row }">
              <div class="app-inline-gap flex items-center">
                <Bars3Icon class="drag-handle text-base-content/40 h-4 w-4 shrink-0 cursor-move" />
                <!-- 一行 = 一个国家/地区。绑到具体国家(而不是随手写的名字)之后,才谈得上
                     配一面对应的国旗,匹配出来的节点也才有国别可言。还没绑上的(老档案里
                     手写的名字)在按钮上显示它原来的名字,不会被悄悄清掉。 -->
                <div class="w-32 shrink-0">
                  <CountrySelect
                    :model-value="row.code"
                    :placeholder="row.name || $t('subscriptionRenameRegionNamePlaceholder')"
                    @update:model-value="pickCountry(row, $event)"
                  />
                </div>
                <!-- 关键词是标签集合而不是一行逗号文本(上游 488):每个词一个可点的牌子,
                     回车 / 逗号新增,点牌子移除;放不下一行时右侧出现展开按钮 -->
                <TagInput
                  :ref="(el: unknown) => setRowInput(row.id, el)"
                  v-model="row.keywords"
                  class="min-w-0 flex-1"
                  :label="[row.name, $t('subscriptionRenameRegionDictLabel')].filter(Boolean).join(' · ')"
                  :placeholder="$t('subscriptionRenameKeywordsPlaceholder')"
                />
                <button
                  type="button"
                  class="btn btn-ghost btn-square btn-sm shrink-0 hover:text-error"
                  :aria-label="$t('subscriptionRenameRemoveRow')"
                  @click="removeRegionRow(row.id)"
                >
                  <TrashIcon class="h-4 w-4" />
                </button>
              </div>
            </template>
          </Draggable>
          <div>
            <p
              v-if="regionRows.length === 0"
              class="text-base-content/50 text-xs"
            >
              {{ $t('subscriptionRenameNoRows') }}
            </p>
          </div>
        </div>

        <!-- 「无法识别地区时的标签」紧跟在地区关键词后面:它就是这张表全都没命中时的
             兜底值,挨着它要兜底的那份清单最好懂。原来它在最上面和序号位数并排,离得远。 -->
        <div
          v-if="enabled"
          class="flex flex-col gap-1"
        >
          <label class="text-xs font-medium">{{ $t('subscriptionRenameUnknownLabel') }}</label>
          <input
            v-model="unknownLabel"
            type="text"
            class="input input-sm w-full"
          />
        </div>

        <!-- 特征不再是「标签 + 同义词」两层结构:命中哪个关键词就把那个词本身(转大写)
             写进节点名。所以这里只需要一行关键词,不再有标签列,也不再有多行增删。
             例:关键词填 iplc,ipv6,节点名 "美国 IPLC IPv6 01" → 美国-IPLC-IPV6-01。 -->
        <div
          v-if="enabled"
          class="flex flex-col gap-1"
        >
          <label class="text-xs font-medium">{{ $t('subscriptionRenameFeatureDictLabel') }}</label>
          <p class="text-base-content/50 text-xs">{{ $t('subscriptionRenameFeatureDictHint') }}</p>
          <TagInput
            ref="featureInput"
            :model-value="featureKeywords"
            :label="$t('subscriptionRenameFeatureDictLabel')"
            :placeholder="$t('subscriptionRenameKeywordsPlaceholder')"
            @update:model-value="featureKeywords = $event"
          />
        </div>

        <!-- 过滤:命中就整条不导入。机场订阅里常混着「官网｜https://xxx」「请提工单开通」
             这类公告条目,它们不是节点,却会被当成节点参与分组、出现在策略组里。 -->
        <div class="flex flex-col gap-1">
          <label class="text-xs font-medium">{{ $t('subscriptionRenameExcludeLabel') }}</label>
          <p class="text-base-content/50 text-xs">{{ $t('subscriptionRenameExcludeHint') }}</p>
          <TagInput
            ref="excludeInput"
            :model-value="excludeKeywords"
            :label="$t('subscriptionRenameExcludeLabel')"
            :placeholder="$t('subscriptionRenameKeywordsPlaceholder')"
            @update:model-value="excludeKeywords = $event"
          />
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { OpenboxRenameOptions } from '@/api/openbox'
import {
  DEFAULT_EXCLUDE_KEYWORDS,
  DEFAULT_FEATURE_KEYWORDS,
  DEFAULT_REGION_DICT,
  DEFAULT_RENAME_TEMPLATE,
  DEFAULT_SEQ_PAD,
  DEFAULT_UNKNOWN_LABEL,
} from './rename-defaults'
import CountrySelect from '@/components/common/CountrySelect.vue'
import TagInput from '@/components/common/TagInput.vue'
import { countryName, findCountry, findCountryByName } from '@/constant/countries'
import { ArrowUturnLeftIcon, Bars3Icon, PlusIcon, TrashIcon } from '@heroicons/vue/24/outline'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Draggable from 'vuedraggable'

const emit = defineEmits<{
  change: [OpenboxRenameOptions]
}>()

// 编辑已有订阅时用它的已存规则起步。没有这个 prop 的话编辑器总是从默认词典起步,
// 而它挂载时就会 emit 一次(watch immediate),等于一打开编辑弹窗就把用户辛苦调过的
// 重命名规则悄悄改回默认——保存下去才发现节点名全变了。
const props = defineProps<{ initial?: OpenboxRenameOptions | null; subscriptionName?: string }>()

const { t, locale } = useI18n()

interface RegionRow {
  id: string
  // ISO 3166-1 alpha-2;空 = 这一行还没绑定国家(老档案里手写的名字)
  code: string
  name: string
  // 关键词是数组(TagInput 的模型),保存时直接当 keywords 发出去
  keywords: string[]
}

const makeId = () =>
  typeof crypto !== 'undefined' && crypto.randomUUID
    ? crypto.randomUUID()
    : `row-${Math.random().toString(36).slice(2)}`

// 用不带空格的英文逗号拼接:分隔符本身就是逗号,后面那个空格只是显示习惯,
// 而它会让用户以为空格是格式的一部分(splitKeywords 本来就会 trim,加不加都能解析)。
const toRegionRows = (dict: { code?: string; name: string; keywords: string[] }[]): RegionRow[] =>
  dict.map((entry) => ({
    id: makeId(),
    // 老档案里 code 存的是一个随机行号(那时它不表示国家),认不出来就退回按名字认
    // ——存的就是「香港」「美国」这些,正好能对上目录。两条都认不出才留空(自定义地区)。
    code: findCountry(entry.code || '')?.code || findCountryByName(entry.name)?.code || '',
    name: entry.name,
    keywords: [...entry.keywords],
  }))

// 选中一个国家:名字跟着走,关键词在"还看得出是上一个国家的"时一并换掉。
// 判据是子集而不是全等:老档案里存的关键词往往是默认值的一个子集(比如只剩
// hk,香港),用全等判的话它们永远算"用户改过的",换成法国之后还挂着香港的词。
// 反过来,只要有一个词不属于上一个国家(用户自己加的),整行就不动——精心加过的
// 关键词不该因为换个国家就没了。
const pickCountry = (row: RegionRow, code: string) => {
  const country = findCountry(code)
  if (!country) return
  const previous = findCountry(row.code)
  const current = row.keywords.map((k) => k.toLowerCase())
  const previousKeywords = new Set((previous?.keywords || []).map((k) => k.toLowerCase()))
  const fromPreviousCountry =
    !current.length || (previous !== undefined && current.every((k) => previousKeywords.has(k)))
  row.code = country.code
  row.name = countryName(country, locale.value)
  if (fromPreviousCountry) row.keywords = [...country.keywords]
}

// 兼容旧档案:老的 featureDict 是 [{label, keywords}],扁平化成一条关键词表。
const toFeatureKeywords = (input: unknown): string[] => {
  if (!Array.isArray(input)) return []
  const out: string[] = []
  for (const item of input) {
    if (typeof item === 'string') {
      if (item.trim()) out.push(item.trim())
    } else if (item && Array.isArray((item as { keywords?: unknown }).keywords)) {
      for (const kw of (item as { keywords: unknown[] }).keywords) {
        if (String(kw).trim()) out.push(String(kw).trim())
      }
    }
  }
  return out
}

// 逐项回退到默认值:已存规则里缺哪一项(老版本存下的记录可能没有 featureDict)就用
// 默认补上,而不是整份 initial 有就全用、没有就全默认。
const init = props.initial
const unknownLabel = ref(init?.unknownLabel || DEFAULT_UNKNOWN_LABEL)
const seqPad = ref(init?.seqPad ?? DEFAULT_SEQ_PAD)
// 序号位数步进器的 label/for 关联(与上游一样:id 挂在 input 上,label 用 for 指过来)
const seqPadId = 'subscription-rename-seq-pad'
const bumpSeqPad = (delta: number) => {
  const current = Number.isFinite(seqPad.value) ? Number(seqPad.value) : DEFAULT_SEQ_PAD
  seqPad.value = Math.min(4, Math.max(1, Math.floor(current) + delta))
}
// 必须是 ref 而不是 reactive:vuedraggable 的 v-model 在拖放结束时会**整体赋一个新
// 数组**,reactive 数组没法被重新赋值,拖了不会生效。
const regionRows = ref<RegionRow[]>(
  toRegionRows(init?.regionDict?.length ? init.regionDict : DEFAULT_REGION_DICT),
)
// 特征只有一行:命中哪个关键词就显示哪个词(转大写)。旧档案里的两层 featureDict
// 会被扁平化过来,不至于一升级就把用户配过的词全丢掉。
const initialFeatureKeywords =
  toFeatureKeywords(init?.featureKeywords) .length
    ? toFeatureKeywords(init?.featureKeywords)
    : toFeatureKeywords(init?.featureDict).length
      ? toFeatureKeywords(init?.featureDict)
      : [...DEFAULT_FEATURE_KEYWORDS]
const featureKeywords = ref<string[]>([...initialFeatureKeywords])

// 过滤关键词。显式给了空数组就尊重"不过滤",只有整个字段缺失才回落到默认值——
// 否则用户清空这一栏、保存、再打开,默认词又自己冒回来了。
const excludeKeywords = ref<string[]>([
  ...(Array.isArray(init?.excludeKeywords) ? init.excludeKeywords : DEFAULT_EXCLUDE_KEYWORDS),
])

// Rows the user left completely blank (never typed a name/label or any keyword) are dropped
// from what gets sent — they're just an empty slot mid-edit, not a real (and invalid) dict entry.
// 记号 → 中文名。界面上只出现人话,拼进模板的仍是 {region} 这类记号。
const TOKEN_LABELS: Record<string, string> = {
  '{region}': 'subscriptionRenameTokenRegion',
  '{feature}': 'subscriptionRenameTokenFeature',
  '{seq}': 'subscriptionRenameTokenSeq',
}
const ALL_TOKENS = ['{region}', '{feature}', '{seq}']

// 从已存模板里读出记号顺序(模板里没出现的记号补在后面),这样打开旧订阅时牌子的
// 顺序和它原本的命名方式一致。
const orderFromTemplate = (tpl: string) => {
  const found = ALL_TOKENS
    .map((tk) => ({ tk, at: tpl.indexOf(tk) }))
    .filter((x) => x.at >= 0)
    .sort((a, b) => a.at - b.at)
    .map((x) => x.tk)
  return [...found, ...ALL_TOKENS.filter((tk) => !found.includes(tk))]
}

const tokenOrder = ref<string[]>(orderFromTemplate(init?.template || DEFAULT_RENAME_TEMPLATE))
// 顺序即模板。分隔符固定 "-";无特征时 applyTemplate 会把 {feature} 连同相邻的一个
// 分隔符一起去掉,所以三块牌子常驻不会留下 "美国--01" 这种空档。
const template = computed(() => tokenOrder.value.join('-'))

// 用订阅名做前缀(「机场名称 | 香港-01」),一眼看出节点来自哪个订阅。
// 默认打开;只有明确存过 false(用户关过)才关
const usePrefix = ref(init?.usePrefix !== false)
// 重命名总开关:默认开;只有明确存过 false 才关
const enabled = ref(init?.enabled !== false)

const options = computed<OpenboxRenameOptions>(() => ({
  enabled: enabled.value,
  template: template.value || DEFAULT_RENAME_TEMPLATE,
  usePrefix: usePrefix.value,
  unknownLabel: unknownLabel.value.trim() || DEFAULT_UNKNOWN_LABEL,
  seqPad: Number.isFinite(seqPad.value) && seqPad.value > 0 ? Math.floor(seqPad.value) : DEFAULT_SEQ_PAD,
  regionDict: regionRows.value
    .filter((row) => row.name.trim() || row.keywords.length > 0)
    // code 是国家代码(没挑国家的行留空),不再是行号:节点的国别归属就是靠它带出去的
    .map((row) => ({ code: row.code, name: row.name.trim(), keywords: row.keywords.filter((k) => k.trim()) })),
  featureKeywords: [...featureKeywords.value],
  excludeKeywords: [...excludeKeywords.value],
}))

watch(
  options,
  (value) => {
    emit('change', value)
  },
  { immediate: true, deep: true },
)

// 保存前把各标签输入里「打了字但还没回车」的那段也收进去。
// TagInput 在失焦时会自己 commit,点保存按钮本来也会触发失焦;但键盘回车 + 立刻点保存、
// 或者用鼠标中键之类的情况下提交顺序不保证,这里显式收一次最稳。
const rowInputs = new Map<string, InstanceType<typeof TagInput> | null>()
const setRowInput = (id: string, el: unknown) => {
  if (el) rowInputs.set(id, el as InstanceType<typeof TagInput>)
  else rowInputs.delete(id)
}
const featureInput = ref<InstanceType<typeof TagInput> | null>(null)
const excludeInput = ref<InstanceType<typeof TagInput> | null>(null)

const commitAll = (): OpenboxRenameOptions => {
  for (const input of rowInputs.values()) input?.commit()
  featureInput.value?.commit()
  excludeInput.value?.commit()
  return options.value
}

defineExpose({ commitAll })

const addRegionRow = () => {
  regionRows.value.push({ id: makeId(), code: '', name: '', keywords: [] })
}
const removeRegionRow = (id: string) => {
  const index = regionRows.value.findIndex((row) => row.id === id)
  if (index !== -1) regionRows.value.splice(index, 1)
}



// 拿当前模板 + 当前序号位数算一个真实样例,改模板时实时跟着变。序号用 seqPad 补零,
// 和真正改名时的行为一致,免得示例和结果对不上。
const templateExample = computed(() => {
  const pad = Number.isFinite(seqPad.value) && seqPad.value > 0 ? Math.floor(seqPad.value) : DEFAULT_SEQ_PAD
  const sample = enabled.value
    ? (template.value || DEFAULT_RENAME_TEMPLATE)
        .replace(/\{region\}/g, t('subscriptionRenameSampleRegion'))
        .replace(/\{feature\}/g, t('subscriptionRenameSampleFeature'))
        .replace(/\{seq\}/g, String(1).padStart(pad, '0'))
    : t('subscriptionRenameSampleOriginal')
  const name = props.subscriptionName?.trim()
  return usePrefix.value && name ? `${name} | ${sample}` : sample
})

const resetToDefaults = () => {
  enabled.value = true
  tokenOrder.value = orderFromTemplate(DEFAULT_RENAME_TEMPLATE)
  unknownLabel.value = DEFAULT_UNKNOWN_LABEL
  seqPad.value = DEFAULT_SEQ_PAD
  regionRows.value = toRegionRows(DEFAULT_REGION_DICT)
  usePrefix.value = true
  featureKeywords.value = [...DEFAULT_FEATURE_KEYWORDS]
  excludeKeywords.value = [...DEFAULT_EXCLUDE_KEYWORDS]
}
</script>
