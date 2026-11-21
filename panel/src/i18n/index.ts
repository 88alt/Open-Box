import { LANG } from '@/constant'
import { language } from '@/store/settings'
import { createI18n } from 'vue-i18n'

const LOADERS: Record<LANG, () => Promise<{ default: Record<string, string> }>> = {
  [LANG.EN_US]: () => import('./en'),
  [LANG.ZH_CN]: () => import('./zh'),
  [LANG.ZH_TW]: () => import('./zh-tw'),
}

export const i18n = createI18n({
  legacy: false,
  locale: language.value,
  fallbackLocale: LANG.EN_US,
  messages: {},
})

const ensureMessages = async (lang: LANG) => {
  if (i18n.global.availableLocales.includes(lang)) return
  const loader = LOADERS[lang]
  if (!loader) throw new Error(`Unsupported language: ${lang}`)
  i18n.global.setLocaleMessage(lang, (await loader()).default)
}

// 语言包按需加载:加载失败时抛错,由调用方决定如何提示并回退。
export const loadLanguage = async (lang: LANG) => {
  await ensureMessages(lang)
  if (lang !== LANG.EN_US) await ensureMessages(LANG.EN_US)
  i18n.global.locale.value = lang
  language.value = lang
}

export const initLanguage = async () => {
  try {
    await loadLanguage(language.value)
  } catch {
    await loadLanguage(LANG.EN_US)
  }
}
