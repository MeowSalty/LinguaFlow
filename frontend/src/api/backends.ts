import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type Backend = ApiSchemas['Backend']
type CreateBackendPayload = ApiSchemas['CreateBackendRequest']
type UpdateBackendPayload = ApiSchemas['UpdateBackendRequest']

export const fetchBackends = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['BackendListResponse']> => {
  const { data, error, response } = await client.GET('/backends')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchBackendsFailed'), error, response)
  }

  return data
}

export const createBackend = async (
  payload: CreateBackendPayload,
  client: ApiClient = apiClient,
): Promise<Backend> => {
  const { data, error, response } = await client.POST('/backends', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createBackendFailed'), error, response)
  }

  return data
}

export const updateBackend = async (
  backendId: number,
  payload: UpdateBackendPayload,
  client: ApiClient = apiClient,
): Promise<Backend> => {
  const { data, error, response } = await client.PUT('/backends/{backendId}', {
    params: { path: { backendId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateBackendFailed'), error, response)
  }

  return data
}

export const deleteBackend = async (
  backendId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/backends/{backendId}', {
    params: { path: { backendId } },
  })

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deleteBackendFailed'), error, response)
  }
}
