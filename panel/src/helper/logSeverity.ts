import { LOG_LEVEL } from '@/constant'

// 内核有的返回 warn、有的返回 warning,统一成 warning
export const normalizeLogLevel = (level: string) => {
  const lower = level.toLowerCase()

  return lower === 'warn' ? LOG_LEVEL.Warning : lower
}

const severityKeys: Record<string, string> = {
  [LOG_LEVEL.Trace]: 'logSeverityTrace',
  [LOG_LEVEL.Debug]: 'logSeverityDebug',
  [LOG_LEVEL.Info]: 'logSeverityInfo',
  [LOG_LEVEL.Warning]: 'logSeverityWarning',
  [LOG_LEVEL.Error]: 'logSeverityError',
  [LOG_LEVEL.Fatal]: 'logSeverityFatal',
  [LOG_LEVEL.Panic]: 'logSeverityPanic',
  [LOG_LEVEL.Silent]: 'logSeveritySilent',
}

// 已知级别显示本地化名称,未知级别原样大写
export const logSeverityLabel = (level: string, t: (key: string) => string) => {
  const key = severityKeys[normalizeLogLevel(level)]

  return key ? t(key) : level.toUpperCase()
}
