import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  type AuthSession,
  clearAuthTokens,
  fetchCurrentUser as fetchCurrentUserApi,
  getAccessToken,
  getRefreshToken,
  loginWithPassword,
  logout as logoutApi,
  registerAndLogin,
  setUnauthorizedHandler,
} from '@/api/client'
import type { ServiceMode } from '@/stores/service'
import { useServiceStore } from '@/stores/service'

type User = ApiSchemas['User']
type LoginPayload = ApiSchemas['LoginRequest']
type RegisterPayload = ApiSchemas['RegisterRequest']

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const accessToken = ref<string | null>(null)
  const refreshToken = ref<string | null>(null)
  const isReady = ref<boolean>(false)

  const isAuthenticated = computed(() => {
    const service = useServiceStore()
    if (service.isLocal) {
      return Boolean(user.value)
    }
    return Boolean(accessToken.value)
  })

  const applySession = (session: AuthSession): void => {
    user.value = session.user
    accessToken.value = session.access_token
    refreshToken.value = session.refresh_token
  }

  const clearSessionState = (): void => {
    user.value = null
    accessToken.value = null
    refreshToken.value = null
  }

  const login = async (credentials: LoginPayload): Promise<void> => {
    const session = await loginWithPassword(credentials)
    applySession(session)
  }

  const register = async (payload: RegisterPayload): Promise<void> => {
    const session = await registerAndLogin(payload)
    applySession(session)
  }

  const fetchCurrentUser = async (): Promise<User | null> => {
    try {
      const fresh = await fetchCurrentUserApi()
      user.value = fresh
      return fresh
    } catch (error) {
      handleUnauthorized()
      throw error
    }
  }

  const logout = async (): Promise<void> => {
    const service = useServiceStore()
    if (service.isLocal) {
      clearSessionState()
      return
    }

    try {
      await logoutApi()
    } finally {
      clearSessionState()
    }
  }

  const handleUnauthorized = (): void => {
    clearAuthTokens()
    clearSessionState()
  }

  const handleUnauthorizedLocal = (): void => {
    clearAuthTokens()
    user.value = null
    accessToken.value = null
    refreshToken.value = null
  }

  const bootstrapServer = async (): Promise<void> => {
    setUnauthorizedHandler(handleUnauthorized)

    const storedAccess = getAccessToken()
    const storedRefresh = getRefreshToken()

    if (!storedAccess || !storedRefresh) {
      clearSessionState()
      isReady.value = true
      return
    }

    accessToken.value = storedAccess
    refreshToken.value = storedRefresh

    try {
      await fetchCurrentUser()
    } catch {
      // 401 已经在 fetchCurrentUser 里清理状态
    } finally {
      isReady.value = true
    }
  }

  const bootstrapForMode = async (mode: ServiceMode): Promise<void> => {
    if (mode === 'local') {
      setUnauthorizedHandler(handleUnauthorizedLocal)
      clearAuthTokens()
      clearSessionState()

      try {
        const fresh = await fetchCurrentUserApi()
        user.value = fresh
      } catch {
        user.value = null
      } finally {
        isReady.value = true
      }
      return
    }

    await bootstrapServer()
  }

  const bootstrap = async (): Promise<void> => {
    await bootstrapServer()
  }

  return {
    user,
    accessToken,
    refreshToken,
    isReady,
    isAuthenticated,
    login,
    register,
    logout,
    fetchCurrentUser,
    bootstrap,
    bootstrapForMode,
  }
})
