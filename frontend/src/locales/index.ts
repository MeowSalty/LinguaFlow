import zhCN from './zh-CN'

export const DEFAULT_LOCALE = 'zh-CN'
export const FALLBACK_LOCALE = DEFAULT_LOCALE

export const localeOptions = [
  {
    code: DEFAULT_LOCALE,
    labelKey: 'locale.zhCN',
    nativeName: '简体中文',
  },
] as const

export type SupportedLocale = (typeof localeOptions)[number]['code']
export type LocaleMessageSchema = typeof zhCN

export const messages = {
  [DEFAULT_LOCALE]: zhCN,
} satisfies Record<SupportedLocale, LocaleMessageSchema>

export const supportedLocales = localeOptions.map((locale) => locale.code) as SupportedLocale[]

export const isSupportedLocale = (locale: string): locale is SupportedLocale => {
  return supportedLocales.includes(locale as SupportedLocale)
}
