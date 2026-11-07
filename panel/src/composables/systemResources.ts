import { fetchSystemResources, type SystemResources } from '@/api/openbox'
import { cpuLoadThresholds } from '@/store/settings'
import { useDocumentVisibility } from '@vueuse/core'
import {
  computed,
  effectScope,
  type EffectScope,
  getCurrentInstance,
  onUnmounted,
  ref,
  shallowRef,
  watch,
} from 'vue'

// 系统资源快照:每 2 秒取一次(页面可见时),取不到就清空;硬件负载图和侧边栏卡片、概览的「活跃终端」共用这一份
export const systemResources = shallowRef<SystemResources | null>(null)
export const systemResourcesHistory = ref<{ at: number; snapshot: SystemResources }[]>([])
export const cpuCores = computed(() => systemResources.value?.cpu.cores ?? [])
// 只有一个核时不单独列核,总负载就是它
export const hasMultipleCores = computed(() => cpuCores.value.length > 1)

const HISTORY_LENGTH = 60
const POLL_INTERVAL = 2000
const REQUEST_TIMEOUT = 5000

// CPU 使用率对应的文字颜色:低于第一个阈值绿,低于第二个橙,更高红
export const cpuLoadClass = (usage?: number | null) => {
  if (usage == null || !Number.isFinite(usage)) return ''
  const [low, medium] = cpuLoadThresholds.value
  return usage < low ? 'text-low-latency' : usage < medium ? 'text-medium-latency' : 'text-high-latency'
}

let users = 0
let scope: EffectScope | undefined

// 用到快照的组件在 setup 里调用;最后一个用户销毁后停掉轮询并清空
export const useSystemResources = () => {
  users++
  if (getCurrentInstance()) onUnmounted(releaseSystemResources)
  if (!scope) {
    scope = effectScope(true)
    scope.run(() => {
      const visibility = useDocumentVisibility()
      watch(
        visibility,
        (state, _old, onCleanup) => {
          if (state !== 'visible') return
          let stopped = false
          let timer: ReturnType<typeof setTimeout> | undefined
          let controller: AbortController | undefined
          const tick = async () => {
            controller = new AbortController()
            const abort = setTimeout(() => controller?.abort(), REQUEST_TIMEOUT)
            try {
              const snapshot = await fetchSystemResources(controller.signal)
              if (!stopped) {
                systemResources.value = snapshot
                const at = snapshot.at ?? Date.now()
                const history = systemResourcesHistory.value
                if (at > (history[history.length - 1]?.at ?? 0)) {
                  systemResourcesHistory.value = [
                    ...history.slice(1 - HISTORY_LENGTH),
                    { at, snapshot },
                  ]
                }
              }
            } catch {
              if (!stopped) systemResources.value = null
            } finally {
              clearTimeout(abort)
              if (!stopped) timer = setTimeout(tick, POLL_INTERVAL)
            }
          }
          onCleanup(() => {
            stopped = true
            clearTimeout(timer)
            controller?.abort()
            systemResources.value = null
          })
          tick()
        },
        { immediate: true },
      )
    })
  }
  return { systemResources, systemResourcesHistory }
}

const releaseSystemResources = () => {
  users = Math.max(0, users - 1)
  if (users === 0) {
    scope?.stop()
    scope = undefined
    systemResources.value = null
  }
}
