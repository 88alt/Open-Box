import { renderRoutes } from '@/helper'
import { swipeInPages } from '@/store/settings'
import { useSwipe } from '@vueuse/core'
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

export const disableSwipe = ref(false)

export const useSwipeRouter = () => {
  const swiperRef = ref()
  const route = useRoute()
  const router = useRouter()
  const { direction } = useSwipe(swiperRef, { threshold: 90 })

  const swipeList = computed(() =>
    renderRoutes.value.map(
      (r) => [() => route.name === r, () => router.push({ name: r })] as const,
    ),
  )

  const getNextIndexInSwipeList = () => {
    return swipeList.value.findIndex((s) => s[0]())
  }

  const getNextRouteName = () => {
    return swipeList.value[(getNextIndexInSwipeList() + 1) % swipeList.value.length]?.[1]?.()
  }
  const getPrevRouteName = () => {
    return swipeList.value[
      (getNextIndexInSwipeList() - 1 + swipeList.value.length) % swipeList.value.length
    ]?.[1]?.()
  }

  const isInputActive = () => {
    const activeEl = document.activeElement
    return activeEl && (activeEl.tagName === 'INPUT' || activeEl.tagName === 'TEXTAREA')
  }

  watch(direction, () => {
    if (!swipeInPages.value) return

    if (
      document.querySelector('dialog:modal') ||
      isInputActive() ||
      window.getSelection()?.toString()?.length ||
      disableSwipe.value
    )
      return
    if (direction.value === 'right') {
      getPrevRouteName()
    } else if (direction.value === 'left') {
      getNextRouteName()
    }
  })

  return {
    swiperRef,
  }
}
