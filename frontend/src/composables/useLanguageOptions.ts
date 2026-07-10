import { type SelectOption } from 'naive-ui'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

export function useLanguageOptions() {
  const { t } = useI18n()

  const targetLanguageOptions = computed<SelectOption[]>(() => [
    { label: t('projects.languages.zhHans'), value: 'zh-Hans' },
    { label: t('projects.languages.zhHant'), value: 'zh-Hant' },
    { label: t('projects.languages.enUS'), value: 'en-US' },
    { label: t('projects.languages.enGB'), value: 'en-GB' },
    { label: t('projects.languages.ja'), value: 'ja' },
    { label: t('projects.languages.ko'), value: 'ko' },
    { label: t('projects.languages.fr'), value: 'fr' },
    { label: t('projects.languages.de'), value: 'de' },
    { label: t('projects.languages.es'), value: 'es' },
  ])

  const sourceLanguageOptions = computed<SelectOption[]>(() => [
    { label: t('projects.languages.auto'), value: 'auto' },
    ...targetLanguageOptions.value,
  ])

  return { targetLanguageOptions, sourceLanguageOptions }
}
