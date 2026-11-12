import type { InjectionKey } from 'vue'

// 连接页提供、「改分流」按钮注入:点按钮就把目标交给连接页上的目标分流弹窗
export const OPEN_TARGET_ROUTING: InjectionKey<(target: string) => void> =
  Symbol('openTargetRouting')
