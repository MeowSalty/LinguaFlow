import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client-core'
import { apiClient, logoutCurrentSession } from './client-core'
import { captureSession, assertSessionCurrent } from './session-context'
import { getRefreshToken, setAuthSession } from './token-storage'
import { buildRequestFailureError } from './utils'

export const loginWithPassword = async (
  credentials: ApiSchemas['LoginRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['AuthSession']> => {
  const context = captureSession()
  const { data, error, response } = await client.POST('/auth/login', {
    body: credentials,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.loginFailed'), error, response)
  }

  assertSessionCurrent(context)
  setAuthSession(data)

  return data
}

export const registerAndLogin = async (
  payload: ApiSchemas['RegisterRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['AuthSession']> => {
  const context = captureSession()
  const { data, error, response } = await client.POST('/auth/register', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.registerFailed'), error, response)
  }

  assertSessionCurrent(context)
  setAuthSession(data)

  return data
}

export const refreshAuthSession = async (
  refreshToken = getRefreshToken(),
  client: ApiClient = apiClient,
): Promise<ApiSchemas['AuthSession']> => {
  const context = captureSession()
  if (!refreshToken) {
    throw new Error('Refresh token is missing.')
  }

  const { data, error, response } = await client.POST('/auth/refresh', {
    body: {
      refresh_token: refreshToken,
    },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.refreshSessionFailed'), error, response)
  }

  assertSessionCurrent(context)
  setAuthSession(data)

  return data
}

export const logout = logoutCurrentSession
