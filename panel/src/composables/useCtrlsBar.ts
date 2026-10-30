import { useCurrentElement, useElementBounding } from '@vueuse/core'
import { computed, onUnmounted, watch } from 'vue'
import { ctrlsBottom } from './paddingViews'

export function useCtrlsBar(width: number = 720) {
  const element = useCurrentElement()
  const { width: ctrlsBarWidth } = useElementBounding(element)
  // 控制栏嵌在页签栏里(连接页)时,页面内容的上边距要让到整条页签栏的下沿
  const bottomTarget = computed<HTMLElement | undefined>(() => {
    const el = element.value as HTMLElement | undefined
    return (el?.closest?.('.settings-menu') as HTMLElement | null) ?? el
  })
  const { bottom: ctrlsBarBottom } = useElementBounding(bottomTarget)
  const isLargeCtrlsBar = computed(() => {
    return ctrlsBarWidth.value > width
  })

  watch(
    ctrlsBarBottom,
    () => {
      ctrlsBottom.value = ctrlsBarBottom.value
    },
    { immediate: true },
  )

  onUnmounted(() => {
    ctrlsBottom.value = 0
  })

  return {
    isLargeCtrlsBar,
  }
}
