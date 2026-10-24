import {
  clearStoredApiBaseUrl,
  getDefaultTokenStorage,
  readStoredApiBaseUrl,
} from '@/api/token-storage'
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  DEFAULT_SERVICE_BASE_URL,
  probeService,
  resolveBootstrapService,
  type ServiceMode,
} from '@/api/bootstrap-service'
import { pingService, setApiBaseUrl, setLocalMode, type ApiSchemas } from '@/api/client-core'
import { captureSession, isSessionCurrent } from '@/api/session-core'
import { fetchMode } from '@/api/system'

const DEFAULT_BASE_URL = DEFAULT_SERVICE_BASE_URL
const SERVER_NAME_STORAGE_KEY = 'linguaflow.server_name'

export type { ServiceMode } from '@/api/bootstrap-service'

export type BootstrapResolveResult = {
  baseUrl: string | null
  mode: ServiceMode | null
}

export type ConnectResult = {
  health: ApiSchemas['HealthResponse']
  mode: ServiceMode
}

const readStoredServerName = (): string | null => {
  if (typeof window === 'undefined') return null
  return getDefaultTokenStorage().getItem(SERVER_NAME_STORAGE_KEY)
}

const writeStoredServerName = (name: string): void => {
  if (typeof window !== 'undefined') getDefaultTokenStorage().setItem(SERVER_NAME_STORAGE_KEY, name)
}

const clearStoredServerName = (): void => {
  if (typeof window !== 'undefined') getDefaultTokenStorage().removeItem(SERVER_NAME_STORAGE_KEY)
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

  const captureServiceGuard = (): (() => boolean) => {
    const context = captureSession()
    const selected = baseUrl.value
    const stored = readStoredApiBaseUrl()
    return () =>
      isSessionCurrent(context) && baseUrl.value === selected && readStoredApiBaseUrl() === stored
  }

  const applyHealthName = (health: ApiSchemas['HealthResponse']): void => {
    const name = health.service?.trim() ?? ''
    serverName.value = name
    if (name) writeStoredServerName(name)
    else clearStoredServerName()
  }

  const setConnectedService = (url: string, health: ApiSchemas['HealthResponse']): void => {
    const trimmed = url.trim() || DEFAULT_BASE_URL
    baseUrl.value = trimmed
    hasSelected.value = true
    setApiBaseUrl(trimmed)
    applyHealthName(health)
  }

  const refreshMode = async (): Promise<ServiceMode | null> => {
    const isCurrent = captureServiceGuard()
    try {
      const response = await fetchMode()
      if (!isCurrent()) return null
      mode.value = response.mode
      setLocalMode(response.mode === 'local')
      return response.mode
    } catch {
      if (!isCurrent()) return null
      mode.value = null
      return null
    }
  }

  const resolveBaseUrlForBootstrap = async (): Promise<BootstrapResolveResult> => {
    const isCurrent = captureServiceGuard()
    const resolved = await resolveBootstrapService()
    if (!isCurrent() || !resolved) return { baseUrl: null, mode: null }
    setConnectedService(resolved.baseUrl, resolved.health)
    mode.value = resolved.mode
    setLocalMode(resolved.mode === 'local')
    return { baseUrl: resolved.baseUrl, mode: resolved.mode }
  }

  const connect = async (url: string): Promise<ConnectResult> => {
    const isCurrent = captureServiceGuard()
    const trimmed = url.trim() || DEFAULT_BASE_URL
    const probed = await probeService(trimmed)
    if (!isCurrent() || !probed) throw new Error()
    setConnectedService(trimmed, probed.health)
    mode.value = probed.mode
    setLocalMode(probed.mode === 'local')
    return probed
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
