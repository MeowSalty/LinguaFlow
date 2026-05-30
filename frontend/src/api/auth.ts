import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { getRefreshToken, setAuthSession, clearAuthTokens } from './token-storage'
import { buildRequestFailureError } from './utils'

export const loginWithPassword = async (
  credentials: ApiSchemas['LoginRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['AuthSession']> => {
  const { data, error, response } = await client.POST('/auth/login', {
    body: credentials,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.loginFailed'), error, response)
  }

  setAuthSession(data)

  return data
}

export const registerAndLogin = async (
  payload: ApiSchemas['RegisterRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['AuthSession']> => {
  const { data, error, response } = await client.POST('/auth/register', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.registerFailed'), error, response)
  }

  setAuthSession(data)

  return data
}

export const refreshAuthSession = async (
  refreshToken = getRefreshToken(),
  client: ApiClient = apiClient,
): Promise<ApiSchemas['AuthSession']> => {
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

  setAuthSession(data)

  return data
}

export const logout = async (
  refreshToken = getRefreshToken(),
  client: ApiClient = apiClient,
): Promise<void> => {
  try {
    if (refreshToken) {
      const { error } = await client.POST('/auth/logout', {
        body: {
          refresh_token: refreshToken,
        },
      })

      if (error) {
        throw error
      }
    }
  } finally {
    clearAuthTokens()
  }
}
