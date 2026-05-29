import { createI18n } from 'vue-i18n'

import {
  DEFAULT_LOCALE,
  FALLBACK_LOCALE,
  isSupportedLocale,
  messages,
  type SupportedLocale,
} from '@/locales'

const STORAGE_KEY = 'linguaflow.locale'

const readStoredLocale = (): SupportedLocale => {
  if (typeof window === 'undefined') {
    return DEFAULT_LOCALE
  }

  const stored = window.localStorage.getItem(STORAGE_KEY)
  return stored && isSupportedLocale(stored) ? stored : DEFAULT_LOCALE
}

export const writeStoredLocale = (locale: SupportedLocale): void => {
  if (typeof window === 'undefined') {
    return
  }

  window.localStorage.setItem(STORAGE_KEY, locale)
}

export const i18n = createI18n({
  legacy: false,
  locale: readStoredLocale(),
  fallbackLocale: FALLBACK_LOCALE,
  messages,
  datetimeFormats: {
    [DEFAULT_LOCALE]: {
      short: {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
      },
    },
  },
  numberFormats: {
    [DEFAULT_LOCALE]: {
      decimal: {
        style: 'decimal',
      },
    },
  },
  missingWarn: import.meta.env.DEV,
  fallbackWarn: import.meta.env.DEV,
})

export const setI18nLocale = (locale: SupportedLocale): void => {
  i18n.global.locale.value = locale
  writeStoredLocale(locale)
  if (typeof document !== 'undefined') {
    document.documentElement.lang = locale
  }
}

export const getInitialLocale = (): SupportedLocale => readStoredLocale()

export const t = i18n.global.t
