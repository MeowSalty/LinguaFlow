import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client-core'
import { apiClient, logoutCurrentSession, refreshTokenOnce } from './client-core'
import { captureSession, assertSessionCurrent } from './session-context'
import { setAuthSession, type AuthTokens } from './token-storage'
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

/**
 * Public facade over the single rotation coordinator in `client-core`: one
 * in-tab flight, cross-tab Web Lock coordination and safe reuse of tokens that
 * were already rotated.
 */
export const refreshAuthSession = (
  accessTokenUsed?: string | null,
  epochUsed?: string,
): Promise<AuthTokens> => refreshTokenOnce(accessTokenUsed, epochUsed)

export const logout = logoutCurrentSession
