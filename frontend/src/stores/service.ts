import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  clearStoredApiBaseUrl,
  createApiClient,
  fetchMode,
  pingService,
  readStoredApiBaseUrl,
  setApiBaseUrl,
  type ApiSchemas,
} from '@/api/client'

const DEFAULT_BASE_URL = '/api/v1'
const SERVER_NAME_STORAGE_KEY = 'linguaflow.server_name'

export type ServiceMode = 'local' | 'server'

export type BootstrapResolveResult = {
  baseUrl: string | null
  mode: ServiceMode | null
}

export type ConnectResult = {
  health: ApiSchemas['HealthResponse']
  mode: ServiceMode
}

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

const probeBaseUrl = async (
  baseUrl: string,
): Promise<{ health: ApiSchemas['HealthResponse']; mode: ServiceMode } | null> => {
  try {
    const health = await pingService(baseUrl)
    const client = createApiClient({
      baseUrl,
      getAccessToken: () => null,
    })
    const modeResponse = await fetchMode(client)
    return { health, mode: modeResponse.mode }
  } catch {
    return null
  }
}

export const useServiceStore = defineStore('service', () => {
  const stored = readStoredApiBaseUrl()
  const baseUrl = ref<string>(stored ?? DEFAULT_BASE_URL)
  const serverName = ref<string>(readStoredServerName() ?? '')
  const hasSelected = ref<boolean>(stored !== null)
  const mode = ref<ServiceMode | null>(null)
  const isAppReady = ref<boolean>(false)

  const isLocal = computed(() => mode.value === 'local')
  const isUsingDefault = computed(() => baseUrl.value === DEFAULT_BASE_URL)
  const displayName = computed(() => serverName.value || baseUrl.value)

  const applyHealthName = (health: ApiSchemas['HealthResponse']): void => {
    const name = health.service?.trim() ?? ''
    serverName.value = name

    if (name) {
      writeStoredServerName(name)
    } else {
      clearStoredServerName()
    }
  }

  const setConnectedService = (url: string, health: ApiSchemas['HealthResponse']): void => {
    const trimmed = url.trim() || DEFAULT_BASE_URL
    baseUrl.value = trimmed
    hasSelected.value = true
    setApiBaseUrl(trimmed)
    applyHealthName(health)
  }

  const refreshMode = async (): Promise<ServiceMode | null> => {
    try {
      const response = await fetchMode()
      mode.value = response.mode
      return response.mode
    } catch {
      mode.value = null
      return null
    }
  }

  const resolveBaseUrlForBootstrap = async (): Promise<BootstrapResolveResult> => {
    const defaultProbe = await probeBaseUrl(DEFAULT_BASE_URL)
    if (defaultProbe?.mode === 'local') {
      setConnectedService(DEFAULT_BASE_URL, defaultProbe.health)
      mode.value = 'local'
      return { baseUrl: DEFAULT_BASE_URL, mode: 'local' }
    }

    const storedUrl = readStoredApiBaseUrl()
    if (storedUrl) {
      const storedProbe = await probeBaseUrl(storedUrl)
      if (storedProbe) {
        setConnectedService(storedUrl, storedProbe.health)
        mode.value = storedProbe.mode
        return { baseUrl: storedUrl, mode: storedProbe.mode }
      }
    }

    return { baseUrl: null, mode: null }
  }

  const connect = async (url: string): Promise<ConnectResult> => {
    const trimmed = url.trim() || DEFAULT_BASE_URL
    const probed = await probeBaseUrl(trimmed)
    if (!probed) {
      throw new Error()
    }
    setConnectedService(trimmed, probed.health)
    mode.value = probed.mode

    return { health: probed.health, mode: probed.mode }
  }

  const refreshServerName = async (): Promise<void> => {
    const health = await pingService(baseUrl.value)
    applyHealthName(health)
  }

  const clear = (): void => {
    baseUrl.value = DEFAULT_BASE_URL
    serverName.value = ''
    hasSelected.value = false
    mode.value = null
    clearStoredApiBaseUrl()
    clearStoredServerName()
    setApiBaseUrl(DEFAULT_BASE_URL)
  }

  return {
    baseUrl,
    serverName,
    displayName,
    hasSelected,
    isUsingDefault,
    mode,
    isLocal,
    isAppReady,
    connect,
    refreshMode,
    refreshServerName,
    resolveBaseUrlForBootstrap,
    clear,
  }
})
