import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  clearStoredApiBaseUrl,
  pingService,
  readStoredApiBaseUrl,
  setApiBaseUrl,
  type ApiSchemas,
} from '@/api/client'

const DEFAULT_BASE_URL = '/api/v1'
const SERVER_NAME_STORAGE_KEY = 'linguaflow.server_name'

const readStoredServerName = (): string | null => {
  if (typeof window === 'undefined') {
    return null
  }

  return window.localStorage.getItem(SERVER_NAME_STORAGE_KEY)
}

const writeStoredServerName = (name: string): void => {
  if (typeof window === 'undefined') {
    return
  }

  window.localStorage.setItem(SERVER_NAME_STORAGE_KEY, name)
}

const clearStoredServerName = (): void => {
  if (typeof window === 'undefined') {
    return
  }

  window.localStorage.removeItem(SERVER_NAME_STORAGE_KEY)
}

export const useServiceStore = defineStore('service', () => {
  const stored = readStoredApiBaseUrl()
  const baseUrl = ref<string>(stored ?? DEFAULT_BASE_URL)
  const serverName = ref<string>(readStoredServerName() ?? '')
  const hasSelected = ref<boolean>(stored !== null)

  const isUsingDefault = computed(() => baseUrl.value === DEFAULT_BASE_URL)
  const displayName = computed(() => serverName.value || baseUrl.value)

  const setConnectedService = (url: string, health: ApiSchemas['HealthResponse']): void => {
    const trimmed = url.trim() || DEFAULT_BASE_URL
    const name = health.service?.trim() ?? ''
    baseUrl.value = trimmed
    serverName.value = name
    hasSelected.value = true
    setApiBaseUrl(trimmed)

    if (name) {
      writeStoredServerName(name)
    } else {
      clearStoredServerName()
    }
  }

  const connect = async (url: string): Promise<ApiSchemas['HealthResponse']> => {
    const trimmed = url.trim() || DEFAULT_BASE_URL
    const health = await pingService(trimmed)
    setConnectedService(trimmed, health)

    return health
  }

  const refreshServerName = async (): Promise<void> => {
    const health = await pingService(baseUrl.value)
    const name = health.service?.trim() ?? ''
    serverName.value = name

    if (name) {
      writeStoredServerName(name)
    } else {
      clearStoredServerName()
    }
  }

  const clear = (): void => {
    baseUrl.value = DEFAULT_BASE_URL
    serverName.value = ''
    hasSelected.value = false
    clearStoredApiBaseUrl()
    clearStoredServerName()
    setApiBaseUrl(DEFAULT_BASE_URL)
  }

  if (stored && !serverName.value) {
    void refreshServerName().catch((error: unknown) => {
      console.error(error)
    })
  }

  return {
    baseUrl,
    serverName,
    displayName,
    hasSelected,
    isUsingDefault,
    connect,
    refreshServerName,
    clear,
  }
})
