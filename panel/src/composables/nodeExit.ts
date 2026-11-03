import { fetchNodeExit, type OpenboxNodeExit } from '@/api/openbox'
import { ref } from 'vue'

// 一个节点的出口查询状态。NodeInfoPanel 自己用一份,质量卡等需要共用同一份结果的地方可以传进去
export const useNodeExit = (name: string) => {
  const exit = ref<OpenboxNodeExit | null>(null)
  const error = ref('')
  const loading = ref(false)
  const load = async (refresh = false) => {
    if (loading.value) return
    loading.value = true
    error.value = ''
    try {
      exit.value = await fetchNodeExit(name, refresh)
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err)
    } finally {
      loading.value = false
    }
  }
  return { exit, error, loading, load }
}

export type NodeExitState = ReturnType<typeof useNodeExit>
