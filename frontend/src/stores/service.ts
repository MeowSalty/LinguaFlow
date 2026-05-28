import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { clearStoredApiBaseUrl, readStoredApiBaseUrl, setApiBaseUrl } from '@/api/client'

const DEFAULT_BASE_URL = '/api/v1'

export const useServiceStore = defineStore('service', () => {
  const stored = readStoredApiBaseUrl()
  const baseUrl = ref<string>(stored ?? DEFAULT_BASE_URL)
  const hasSelected = ref<boolean>(stored !== null)

  const isUsingDefault = computed(() => baseUrl.value === DEFAULT_BASE_URL)

  const setBaseUrl = (url: string): void => {
    const trimmed = url.trim() || DEFAULT_BASE_URL
    baseUrl.value = trimmed
    hasSelected.value = true
    setApiBaseUrl(trimmed)
  }

  const clear = (): void => {
    baseUrl.value = DEFAULT_BASE_URL
    hasSelected.value = false
    clearStoredApiBaseUrl()
    setApiBaseUrl(DEFAULT_BASE_URL)
  }

  return {
    baseUrl,
    hasSelected,
    isUsingDefault,
    setBaseUrl,
    clear,
  }
})
