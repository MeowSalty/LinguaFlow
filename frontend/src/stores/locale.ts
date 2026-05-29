import { defineStore } from 'pinia'

import { getInitialLocale, setI18nLocale } from '@/i18n'
import { DEFAULT_LOCALE, isSupportedLocale, localeOptions, type SupportedLocale } from '@/locales'

export const useLocaleStore = defineStore('locale', () => {
  const currentLocale = ref<SupportedLocale>(getInitialLocale())

  const availableLocales = computed(() => localeOptions)
  const hasMultipleLocales = computed(() => availableLocales.value.length > 1)

  const setLocale = (locale: string): void => {
    const nextLocale = isSupportedLocale(locale) ? locale : DEFAULT_LOCALE
    currentLocale.value = nextLocale
    setI18nLocale(nextLocale)
  }

  setLocale(currentLocale.value)

  return {
    currentLocale,
    availableLocales,
    hasMultipleLocales,
    setLocale,
  }
})
