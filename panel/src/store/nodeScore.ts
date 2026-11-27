// 节点评分的共享缓存(上游 v0.1.494 代理页用):
// 代理页一屏可能有上百个节点卡,每张卡各自去拉 /api/openbox/node-quality 会把后端打爆,
// 所以这里整页拉一次(allSubscriptions + details=nodes),按节点名摊平成一张表给卡片查。
//
// 刷新时机:进入代理页时拉一次;后台节点质量是每 10 秒更新的,这里也按 30 秒过期,
// 过期后再进页面会重拉。拿不到就返回空表,卡片上不显示评分那一段(不影响其它功能)。
import { fetchNodeQuality } from '@/api/openbox'
import { ref } from 'vue'

type ScoreEntry = { score: number | null; stale: boolean }

const scores = ref<Record<string, ScoreEntry>>({})
const loadedAt = ref(0)
const loading = ref(false)

// 评分多久算过期(毫秒)
const TTL = 30_000

export const nodeScore = (name?: string | null): ScoreEntry | null => {
  if (!name) return null
  return scores.value[name] ?? null
}

export const nodeScoresLoading = loading

// 拉一次全部节点的评分。force=true 时忽略 TTL(刷新评分按钮用)
export const loadNodeScores = async (force = false) => {
  if (loading.value) return
  const now = Date.now()
  if (!force && loadedAt.value && now - loadedAt.value < TTL) return
  loading.value = true
  try {
    const res = await fetchNodeQuality({ allSubscriptions: true, details: 'nodes' })
    const next: Record<string, ScoreEntry> = {}
    for (const sub of res.subscriptions) {
      for (const n of sub.nodes ?? []) {
        // 同名节点在不同订阅里极少见;后出现的覆盖前面的,与按名字查的口径一致
        next[n.tag] = { score: n.score, stale: n.scoreStale === true }
      }
    }
    scores.value = next
    loadedAt.value = Date.now()
  } catch {
    // 拉不到就保持上一次的表(首次为空),不弹错、不打断代理页
  } finally {
    loading.value = false
  }
}
