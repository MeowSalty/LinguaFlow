import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  type AuthSession,
  changeCurrentUserPassword,
  clearAuthTokens,
  fetchCurrentUser as fetchCurrentUserApi,
  getAccessToken,
  getRefreshToken,
  loginWithPassword,
  logout as logoutApi,
  registerAndLogin,
  setLocalMode,
  setUnauthorizedHandler,
  updateCurrentUser,
} from '@/api/client'
import type { ServiceMode } from '@/stores/service'
import { useServiceStore } from '@/stores/service'
import {
  assertSessionCurrent,
  captureSession,
  changeSessionContext,
  getSessionUserId,
  isSessionCurrent,
  onSessionChange,
  type SessionSnapshot,
} from '@/api/session-context'

type User = ApiSchemas['User']
type LoginPayload = ApiSchemas['LoginRequest']
type RegisterPayload = ApiSchemas['RegisterRequest']

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const accessToken = ref<string | null>(null)
  const refreshToken = ref<string | null>(null)
  const isReady = ref<boolean>(false)
  const profileUpdating = ref(false)
  const passwordChanging = ref(false)
  const initializationError = ref<string | null>(null)
  let bootstrapVersion = 0

  const isAuthenticated = computed(() => {
    const service = useServiceStore()
    if (service.isLocal) {
      return Boolean(user.value)
    }
    return Boolean(accessToken.value)
  })

  const applySession = (session: AuthSession): void => {
    changeSessionContext(captureSession().baseUrl, session.user.id)
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
    clearAuthTokens()
    changeSessionContext(captureSession().baseUrl, null, true)
    const context = captureSession()
    const session = await loginWithPassword(credentials)
    assertSessionCurrent(context)
    applySession(session)
  }

  const register = async (payload: RegisterPayload): Promise<SessionSnapshot> => {
    clearAuthTokens()
    changeSessionContext(captureSession().baseUrl, null, true)
    const context = captureSession()
    const session = await registerAndLogin(payload)
    assertSessionCurrent(context)
    applySession(session)
    return captureSession()
  }

  const fetchCurrentUser = async (): Promise<User | null> => {
    const context = captureSession()
    try {
      const fresh = await fetchCurrentUserApi()
      assertSessionCurrent(context)
      changeSessionContext(context.baseUrl, fresh.id)
      user.value = fresh
      initializationError.value = null
      return fresh
    } catch (error) {
      if (isSessionCurrent(context))
        initializationError.value = error instanceof Error ? error.message : String(error)
      throw error
    }
  }

  const updateProfile = async (payload: ApiSchemas['UpdateCurrentUserRequest']): Promise<User> => {
    const context = captureSession()
    profileUpdating.value = true
    try {
      const updated = await updateCurrentUser(payload)
      assertSessionCurrent(context)
      user.value = updated
      return updated
    } finally {
      if (isSessionCurrent(context)) profileUpdating.value = false
    }
  }

  const changePassword = async (payload: ApiSchemas['ChangePasswordRequest']): Promise<void> => {
    const context = captureSession()
    passwordChanging.value = true
    try {
      await changeCurrentUserPassword(payload)
      assertSessionCurrent(context)
    } finally {
      if (isSessionCurrent(context)) passwordChanging.value = false
    }
  }

  const logout = async (): Promise<void> => {
    clearSessionState()
    await logoutApi()
  }

  const handleUnauthorized = (): void => {
    changeSessionContext(captureSession().baseUrl, null, true)
    clearAuthTokens()
    clearSessionState()
  }

  const handleUnauthorizedLocal = (): void => {
    changeSessionContext(captureSession().baseUrl, null, true)
    clearAuthTokens()
    user.value = null
    accessToken.value = null
    refreshToken.value = null
  }

  const bootstrapServer = async (): Promise<void> => {
    const version = ++bootstrapVersion
    setUnauthorizedHandler(handleUnauthorized)

    const storedAccess = getAccessToken()
    const storedRefresh = getRefreshToken()

    if (!storedAccess || !storedRefresh) {
      clearSessionState()
      if (version === bootstrapVersion) isReady.value = true
      return
    }

    accessToken.value = storedAccess
    refreshToken.value = storedRefresh

    try {
      await fetchCurrentUser()
    } catch {
      // 401 已经在 fetchCurrentUser 里清理状态
    } finally {
      if (version === bootstrapVersion) isReady.value = true
    }
  }

  const bootstrapForMode = async (mode: ServiceMode): Promise<void> => {
    const version = ++bootstrapVersion
    setLocalMode(mode === 'local')

    if (mode === 'local') {
      setUnauthorizedHandler(handleUnauthorizedLocal)
      clearAuthTokens()
      clearSessionState()

      try {
        await fetchCurrentUser()
      } catch {
        if (version === bootstrapVersion) user.value = null
      } finally {
        if (version === bootstrapVersion) isReady.value = true
      }
      return
    }

    await bootstrapServer()
  }

  const bootstrap = async (): Promise<void> => {
    await bootstrapServer()
  }

  const clearSession = (): void => {
    changeSessionContext(captureSession().baseUrl, null, true)
    clearAuthTokens()
    clearSessionState()
  }

  onSessionChange(() => {
    if (getSessionUserId() == null) {
      bootstrapVersion++
      clearSessionState()
      profileUpdating.value = false
      passwordChanging.value = false
      initializationError.value = null
    }
  })

  return {
    user,
    accessToken,
    refreshToken,
    isReady,
    profileUpdating,
    passwordChanging,
    initializationError,
    isAuthenticated,
    login,
    register,
    logout,
    fetchCurrentUser,
    updateProfile,
    changePassword,
    bootstrap,
    bootstrapForMode,
    clearSession,
  }
})
