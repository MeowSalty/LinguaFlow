import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

export const updateCurrentUser = async (
  payload: ApiSchemas['UpdateCurrentUserRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['User']> => {
  const { data, error, response } = await client.PUT('/users/me', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateCurrentUserFailed'), error, response)
  }

  return data
}

export const changeCurrentUserPassword = async (
  payload: ApiSchemas['ChangePasswordRequest'],
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.PUT('/users/me/password', {
    body: payload,
  })

  if (error) {
    throw buildRequestFailureError(t('api.errors.changeCurrentUserPasswordFailed'), error, response)
  }
}
